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
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// ---- rotate-key ----

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
	// Inherit the algorithm of the most-recently-active key of
	// this role; fall back to ECDSAP256 if nothing's active yet.
	active, err := h.Q.ListActiveDnsKeysForZoneAndRole(r.Context(), id, role)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	algo := h.defaultDnssecAlgorithm()
	if len(active) > 0 {
		algo = active[0].Algorithm
	}
	material, err := generateDnssecKeypair(role, algo)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "key generation failed: "+err.Error())
		return
	}
	if _, err := h.Q.CreateDnsKey(r.Context(), dbq.CreateDnsKeyParams{
		ZoneID: &id, Role: material.Role, Algorithm: material.Algorithm,
		PrivatePem: material.PrivatePem, PublicKeyB64: material.PublicKeyB64,
		KeyTag: material.KeyTag,
	}); err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	// Retire the previously-active keys of this role. Sequential
	// updates (no transaction) — matches the existing codebase
	// posture. Worst case on a partial failure: a new key + one
	// or more still-active old keys; operator re-runs rotate to
	// finish the retirement (idempotent).
	for _, k := range active {
		if _, err := h.Q.RetireDnsKey(r.Context(), k.ID); err != nil {
			status, msg := httpx.Mapped(err)
			httpx.Error(w, status, msg)
			return
		}
	}
	// Bump the zone's updated_at so the SOA serial moves and the
	// bundle etag flips for downstream resolvers.
	if _, err := h.Q.TouchDnsZone(r.Context(), id); err != nil {
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
