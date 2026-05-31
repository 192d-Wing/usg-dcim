// PR 81 — DNSSEC lifecycle: rotate-key, disable-dnssec, delete key.
//
// rotate-key: generate a fresh key of `role`, retire all existing
// active keys of that role, bump the zone's updated_at so SOA
// serial moves and downstream resolvers pick up the new key.
//
// disable-dnssec: hard-delete every key for the zone and clear
// signed=false. Reversible via enable-dnssec but operators should
// withdraw the parent DS first.
//
// DELETE /keys/{id}: purge a retired key. Refuses active keys —
// the rotation flow guarantees a successor exists before removal.
package dns

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// ---- rotate-key ----

// RotationQuerier is the slim Querier surface RotateZoneKey needs.
// Exported so packages outside `dns` (the scheduler job in
// internal/scheduler/jobs/dnssecrotate, specifically) can satisfy
// it without depending on the larger handler.Querier interface.
type RotationQuerier interface {
	ListActiveDnsKeysForZoneAndRole(ctx context.Context, zoneID uuid.UUID, role string) ([]dbq.DnsKeyRow, error)
	CreateDnsKey(ctx context.Context, arg dbq.CreateDnsKeyParams) (dbq.DnsKeyRow, error)
	RetireDnsKey(ctx context.Context, id uuid.UUID) (int64, error)
	TouchDnsZone(ctx context.Context, id uuid.UUID) (int64, error)
}

// RotateZoneKey generates a fresh key for (zone, role), inserts it,
// retires every previously-active key of that role, and bumps the
// zone's updated_at so the SOA serial moves. Returns the count of
// retired keys (callers use this to size logs / decide whether to
// audit).
//
// Preconditions enforced inside the helper as defense-in-depth:
// zone.Signed must be true and zone.Frozen must be false. Both the
// HTTP handler and the cron pre-check these (frozen via 422 / cron
// skip; unsigned via 422 / cron SQL filter), but as an exported
// helper, a future third caller that forgets either guard could
// otherwise mint a key on an unsigned zone (state corruption: a
// DNSKEY row lingers on a zone whose renderer drops it) or bypass
// an operator's freeze.
//
// Algorithm is inherited from the most-recently-active key of the
// role; if no active key exists, falls back to ECDSAP256. Caller is
// responsible for any audit emission with whichever actor info
// applies (Principal for the API, "scheduler" for the cron).
//
// Sequential statements (no transaction) — matches the existing
// codebase posture. Partial failure leaves a new key + one or more
// still-active old keys; the rotation flow is idempotent, so the
// next operator-driven rotate completes the retirement. NOTE: the
// CRON path cannot self-heal as quickly because ListActive returns
// keys ORDER BY active_from DESC, so the freshly minted partial-
// failure key resets the cron's age clock — orphans linger until
// the new key crosses zsk_rotation_days. Operators noticing the
// warn log should manually rotate to complete cleanup.
//
// ErrZoneNotSigned and ErrZoneFrozen are sentinel returns so
// callers can map them to specific HTTP statuses (the HTTP handler
// does this above the helper; if a future caller wants to do it
// post-call it can errors.Is).
var (
	ErrZoneNotSigned = errors.New("zone is not signed")
	ErrZoneFrozen    = errors.New("zone is frozen")
)

func RotateZoneKey(ctx context.Context, q RotationQuerier, zone dbq.DnsZone, role string) (retired int, err error) {
	if !zone.Signed {
		return 0, ErrZoneNotSigned
	}
	if zone.Frozen {
		return 0, ErrZoneFrozen
	}
	active, err := q.ListActiveDnsKeysForZoneAndRole(ctx, zone.ID, role)
	if err != nil {
		return 0, err
	}
	algo := defaultDnssecAlgorithm()
	if len(active) > 0 {
		algo = active[0].Algorithm
	}
	material, err := generateDnssecKeypair(role, algo)
	if err != nil {
		return 0, fmt.Errorf("key generation failed: %w", err)
	}
	zoneID := zone.ID
	if _, err := q.CreateDnsKey(ctx, dbq.CreateDnsKeyParams{
		ZoneID: &zoneID, Role: material.Role, Algorithm: material.Algorithm,
		PrivatePem: material.PrivatePem, PublicKeyB64: material.PublicKeyB64,
		KeyTag: material.KeyTag,
	}); err != nil {
		return 0, err
	}
	for _, k := range active {
		if _, err := q.RetireDnsKey(ctx, k.ID); err != nil {
			return retired, err
		}
		retired++
	}
	if _, err := q.TouchDnsZone(ctx, zone.ID); err != nil {
		return retired, err
	}
	return retired, nil
}

func (h *Handler) rotateZoneKey(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	role := chi.URLParam(r, "role")
	if role != "ksk" && role != "zsk" {
		httpx.Error(w, http.StatusBadRequest, "role must be ksk or zsk")
		return
	}
	zone, err := h.Q.GetDnsZone(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "zone not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if !h.enforceFabric(w, r, zone.FabricID, "dns:keys:rotate") {
		return
	}
	if zone.Frozen {
		httpx.Error(w, http.StatusUnprocessableEntity,
			"zone is frozen — unfreeze before rotating")
		return
	}
	if !zone.Signed {
		httpx.Error(w, http.StatusUnprocessableEntity,
			"zone is not signed — enable DNSSEC first")
		return
	}
	if _, err := RotateZoneKey(r.Context(), h.Q, zone, role); err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action:     "dns_zone.rotate_" + role,
		TargetType: "dns_zone",
		TargetID:   id.String(),
	})
	// Return the full key roster (active + retired) so the UI
	// can re-render the panel without a second GET.
	keys, err := h.Q.ListDnsKeysByZone(r.Context(), id)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, keys)
}

// defaultDnssecAlgorithm — Python reads this from settings
// (dns_dnssec_default_algorithm). For the Go port we hardcode
// ECDSAP256 (the Python default); operators wanting other algs
// will get a settings knob in a follow-up.
func defaultDnssecAlgorithm() string {
	return "ecdsap256sha256"
}

// ---- disable-dnssec ----

func (h *Handler) disableDnssec(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	zone, err := h.Q.GetDnsZone(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "zone not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if !h.enforceFabric(w, r, zone.FabricID, "dns:keys:rotate") {
		return
	}
	if zone.Frozen {
		httpx.Error(w, http.StatusUnprocessableEntity,
			"zone is frozen — unfreeze before disabling DNSSEC")
		return
	}
	// Idempotent: already-unsigned zones short-circuit to 204.
	if !zone.Signed {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Hard delete (matches Python's delete + clear signed). The
	// renderer drops DNSKEY/RRSIG output and resolvers fall back
	// to unsigned. Operators should withdraw the parent DS first.
	if _, err := h.Q.DeleteAllDnsKeysForZone(r.Context(), id); err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if _, err := h.Q.SetDnsZoneSigned(r.Context(), id, false); err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if _, err := h.Q.TouchDnsZone(r.Context(), id); err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action:     "dns_zone.disable_dnssec",
		TargetType: "dns_zone",
		TargetID:   id.String(),
	})
	w.WriteHeader(http.StatusNoContent)
}

// ---- DELETE /keys/{id} ----

func (h *Handler) deleteDnsKey(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	key, err := h.Q.GetDnsKey(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "dns key not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	// Refuse to purge an active key — rotation flow guarantees
	// a successor before removal. 422 nudges the operator to
	// rotate first.
	if key.RetiredAt == nil {
		httpx.Error(w, http.StatusUnprocessableEntity,
			"active key can't be deleted; rotate it first so the new key takes over")
		return
	}
	// Enforce fabric scope via the key's parent zone. A catalog-
	// bound key (zone_id IS NULL) falls outside this code path —
	// the catalog DNSSEC surface (PR 2090+ in Python) will handle
	// catalog-key delete when ported.
	if key.ZoneID != nil {
		zone, err := h.Q.GetDnsZone(r.Context(), *key.ZoneID)
		if err == nil {
			if !h.enforceFabric(w, r, zone.FabricID, "dns:keys:delete") {
				return
			}
			if zone.Frozen {
				httpx.Error(w, http.StatusUnprocessableEntity,
					"zone is frozen — unfreeze before deleting keys")
				return
			}
		}
	}
	if _, err := h.Q.DeleteDnsKey(r.Context(), id); err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action:     "dns_key.delete",
		TargetType: "dns_key",
		TargetID:   id.String(),
	})
	w.WriteHeader(http.StatusNoContent)
}
