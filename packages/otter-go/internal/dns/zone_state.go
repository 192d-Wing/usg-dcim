// Zone state-toggle endpoints (PR 70). Ports the simplest of the
// 11 missing zone operations from api/dns.py:
//
//   POST /zones/{id}/freeze       — set frozen=true
//   POST /zones/{id}/unfreeze     — set frozen=false
//   POST /zones/{id}/nsec3        — set NSEC3 params on a signed zone
//   DELETE /zones/{id}/nsec3      — clear NSEC3 params (back to NSEC)
//
// All four are pure state writes — no rendering, no key crypto.
// More complex zone ops (DNSSEC key gen/rotate, BIND import, IPAM
// sync) stay Python until the rendering service is ported.
package dns

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// nsec3SaltPattern: hex string, EVEN-length (each byte is 2 hex
// chars), up to 64 chars (32 octets). RFC 5155 allows 0-255 octets
// — 510 hex chars — but BIND/CoreDNS in practice cap much smaller;
// Python schema uses 64. NULL salt is acceptable and means
// "renderer picks a fresh random salt."
var nsec3SaltPattern = regexp.MustCompile(`^([0-9a-fA-F]{2}){1,32}$`)

func (h *Handler) freezeZone(w http.ResponseWriter, r *http.Request) {
	h.setFrozen(w, r, true, "dns_zone.freeze")
}

func (h *Handler) unfreezeZone(w http.ResponseWriter, r *http.Request) {
	h.setFrozen(w, r, false, "dns_zone.unfreeze")
}

func (h *Handler) setFrozen(
	w http.ResponseWriter, r *http.Request, frozen bool, action string,
) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	fid, ok := h.lookupFabricID(w, r.Context(), func(ctx context.Context) (uuid.UUID, error) {
		return h.Q.GetDnsZoneFabricID(ctx, id)
	}, "zone not found")
	if !ok {
		return
	}
	if !h.enforceFabric(w, r, fid, "dns:zones:update") {
		return
	}
	out, err := h.Q.SetDnsZoneFrozen(r.Context(), id, frozen)
	if err != nil {
		mapErr(w, err, "zone not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: action, TargetType: "dns_zone", TargetID: id.String(),
	})
	httpx.JSON(w, http.StatusOK, out)
}

type nsec3ParamsReq struct {
	Salt       *string `json:"salt"`
	Iterations int32   `json:"iterations"`
	OptOut     bool    `json:"opt_out"`
}

func (h *Handler) setZoneNsec3(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r, "id")
	if !ok {
		return
	}
	var req nsec3ParamsReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	// RFC 5155 §10.3 caps iterations at 150 for production zones —
	// past that resolvers refuse and DoS risk grows. Python schema
	// uses the same bound.
	if req.Iterations < 0 || req.Iterations > 150 {
		httpx.Error(w, http.StatusBadRequest, "iterations must be between 0 and 150")
		return
	}
	if req.Salt != nil && *req.Salt != "" && !nsec3SaltPattern.MatchString(*req.Salt) {
		httpx.Error(w, http.StatusBadRequest, "salt must be even-length hex up to 64 chars")
		return
	}
	// Empty string normalizes to NULL — "let the renderer pick."
	if req.Salt != nil && *req.Salt == "" {
		req.Salt = nil
	}

	// Look up zone first to enforce frozen-check + signed-check
	// before mutating.
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
	if !h.enforceFabric(w, r, zone.FabricID, "dns:zones:update") {
		return
	}
	if zone.Frozen {
		httpx.Error(w, http.StatusUnprocessableEntity,
			"zone is frozen — unfreeze before changing NSEC3 params")
		return
	}
	if !zone.Signed {
		httpx.Error(w, http.StatusUnprocessableEntity,
			"zone is not signed — enable DNSSEC first")
		return
	}
	out, err := h.Q.SetDnsZoneNsec3(r.Context(), dbq.SetDnsZoneNsec3Params{
		ID: id, Salt: req.Salt, Iterations: req.Iterations, OptOut: req.OptOut,
	})
	if err != nil {
		mapErr(w, err, "zone not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "dns_zone.set_nsec3", TargetType: "dns_zone", TargetID: id.String(),
	})
	httpx.JSON(w, http.StatusOK, out)
}

// previewZone renders the zone + every record into BIND text.
// Reads are the same shape as the Python handler: zone fetch +
// unpaginated record list, run through the pure renderer.
//
// PR 71. Tested in renderer_test.go (pure rendering) +
// zone_state_test.go (handler glue).
func (h *Handler) previewZone(w http.ResponseWriter, r *http.Request) {
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
	if !h.enforceFabric(w, r, zone.FabricID, "dns:zones:read") {
		return
	}
	records, err := h.Q.ListAllRecordsInZone(r.Context(), id)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	text, err := renderZoneFile(zone, records)
	if err != nil {
		// Malformed record data — surface to the operator rather
		// than silently dropping the row. The single-record GET
		// will show which record has bad data.
		httpx.Error(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"zone_id":      id.String(),
		"name":         zone.Name,
		"text":         text,
		"record_count": len(records),
	})
}

func (h *Handler) clearZoneNsec3(w http.ResponseWriter, r *http.Request) {
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
	if !h.enforceFabric(w, r, zone.FabricID, "dns:zones:update") {
		return
	}
	if zone.Frozen {
		httpx.Error(w, http.StatusUnprocessableEntity,
			"zone is frozen — unfreeze before clearing NSEC3 params")
		return
	}
	// Clearing reverts the zone to NSEC mode. Safe on a zone that
	// was never in NSEC3 mode — just rewrites the same values.
	out, err := h.Q.SetDnsZoneNsec3(r.Context(), dbq.SetDnsZoneNsec3Params{
		ID: id, Salt: nil, Iterations: 0, OptOut: false,
	})
	if err != nil {
		mapErr(w, err, "zone not found")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "dns_zone.clear_nsec3", TargetType: "dns_zone", TargetID: id.String(),
	})
	httpx.JSON(w, http.StatusOK, out)
}
