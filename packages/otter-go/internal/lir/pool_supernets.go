// Pool ↔ supernet linkage endpoints: list pool source supernets,
// attach an existing supernet as a pool source, detach. The CHECK
// constraint ck_supernet_lir_xor_owner (migration 0065) enforces
// that a tenant-owned supernet can't double as a pool source — the
// attach handler pre-checks via GetSupernetForLirAttach so a 409
// with a clear message bubbles up before the constraint fires.
package lir

import (
	"net/http"
	"strings"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type listPoolSupernetsResponse struct {
	Items  []dbq.PoolSourceSupernetRow `json:"items"`
	Total  int64                       `json:"total"`
	Limit  int32                       `json:"limit"`
	Offset int32                       `json:"offset"`
}

func (h *Handler) listPoolSupernets(w http.ResponseWriter, r *http.Request) {
	poolID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	// Confirm pool exists so the response is a clean 404 instead of
	// silently returning zero rows. Mirrors the organization
	// detail-then-action pattern.
	if _, err := h.Q.GetLirPool(r.Context(), poolID); err != nil {
		mapErr(w, err, msgPoolNotFound)
		return
	}
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	items, err := h.Q.ListPoolSourceSupernets(r.Context(), dbq.ListPoolSourceSupernetsParams{
		PoolID: poolID, Limit: limit, Offset: offset,
	})
	if err != nil {
		mapErr(w, err, "")
		return
	}
	total, err := h.Q.CountPoolSourceSupernets(r.Context(), poolID)
	if err != nil {
		mapErr(w, err, "")
		return
	}
	if items == nil {
		items = []dbq.PoolSourceSupernetRow{}
	}
	httpx.JSON(w, http.StatusOK, listPoolSupernetsResponse{
		Items: items, Total: total, Limit: limit, Offset: offset,
	})
}

type attachReq struct {
	SupernetID string `json:"supernet_id"`
}

func (h *Handler) attachPoolSupernet(w http.ResponseWriter, r *http.Request) {
	poolID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	pool, err := h.Q.GetLirPool(r.Context(), poolID)
	if err != nil {
		mapErr(w, err, msgPoolNotFound)
		return
	}
	var req attachReq
	if !decodeBody(w, r, &req) {
		return
	}
	supernetIDPtr, ok := parseOptionalUUID(w, &req.SupernetID, "supernet_id")
	if !ok {
		return
	}
	if supernetIDPtr == nil {
		httpx.Error(w, http.StatusBadRequest, "supernet_id is required")
		return
	}
	supernetID := *supernetIDPtr
	candidate, err := h.Q.GetSupernetForLirAttach(r.Context(), supernetID)
	if err != nil {
		mapErr(w, err, "supernet not found")
		return
	}
	if status, msg := validateAttachCandidate(pool, candidate); status != 0 {
		httpx.Error(w, status, msg)
		return
	}
	if err := h.Q.AttachSupernetToPool(r.Context(), dbq.AttachSupernetToPoolParams{
		ID: supernetID, PoolID: poolID,
	}); err != nil {
		mapErr(w, err, "")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "lir.pool.attach_supernet", TargetType: "lir_pool",
		TargetID: poolID.String(),
		Metadata: map[string]any{"supernet_id": supernetID.String()},
	})
	w.WriteHeader(http.StatusNoContent)
}

// validateAttachCandidate returns (0, "") on success, or (status, msg)
// to bail. Three checks: already-pooled, tenant-owned (would violate
// ck_supernet_lir_xor_owner), family-mismatch. Pulled out of the
// handler so attachPoolSupernet stays under the cognitive-complexity
// budget and so unit tests can exercise the validation matrix
// without a fake DB.
func validateAttachCandidate(pool dbq.LirPool, candidate dbq.SupernetLirAttachRow) (int, string) {
	if candidate.LirPoolID != nil {
		return http.StatusConflict, "supernet is already attached to a pool"
	}
	if candidate.OwnerOrganizationID != nil {
		return http.StatusConflict, "tenant-owned supernet cannot be a pool source"
	}
	if supernetFamily(candidate.Prefix) != pool.IpFamily {
		return http.StatusUnprocessableEntity,
			"supernet family doesn't match pool family"
	}
	return 0, ""
}

// supernetFamily derives 4 or 6 from a CIDR string. Postgres' host()+
// masklen() formatting gives us "10.0.0.0/24" or "2001:db8::/48"; a
// colon is the unambiguous v6 marker.
func supernetFamily(prefix string) int16 {
	if strings.Contains(prefix, ":") {
		return 6
	}
	return 4
}

func (h *Handler) detachPoolSupernet(w http.ResponseWriter, r *http.Request) {
	poolID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	supernetID, ok := parseUUIDParam(w, r, "supernet_id")
	if !ok {
		return
	}
	// Verify the supernet is actually attached to THIS pool — gives a
	// 404 with a clearer message than the no-op UPDATE would produce.
	candidate, err := h.Q.GetSupernetForLirAttach(r.Context(), supernetID)
	if err != nil {
		mapErr(w, err, "supernet not found")
		return
	}
	if candidate.LirPoolID == nil || *candidate.LirPoolID != poolID {
		httpx.Error(w, http.StatusNotFound, "supernet is not attached to this pool")
		return
	}
	// Refuse if any allocation traces back to this pool supernet —
	// detach while live allocations exist would break the
	// capacity-reconstruction walk the allocator does in phase 4.
	n, err := h.Q.CountAllocationsForPoolSupernet(r.Context(), supernetID)
	if err != nil {
		mapErr(w, err, "")
		return
	}
	if n > 0 {
		httpx.Error(w, http.StatusConflict,
			"supernet has live allocations carved from it; return them first")
		return
	}
	if err := h.Q.DetachSupernetFromPool(r.Context(), dbq.DetachSupernetFromPoolParams{
		ID: supernetID, PoolID: poolID,
	}); err != nil {
		mapErr(w, err, "")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "lir.pool.detach_supernet", TargetType: "lir_pool",
		TargetID: poolID.String(),
		Metadata: map[string]any{"supernet_id": supernetID.String()},
	})
	w.WriteHeader(http.StatusNoContent)
}
