// Allocation list + get handlers. Mirrors the request-list pattern:
// org-scope filter via scopedOrgIDs (a non-global principal with no
// OrganizationIDs short-circuits to an empty page without hitting
// the DB), optional ?status= filter, paginated by limit/offset.
//
// Get returns 404 (not 403) on out-of-scope allocations — same
// existence-leak posture used for requests.
package lir

import (
	"net/http"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

const msgAllocationNotFound = "lir allocation not found"

type listAllocationsResponse struct {
	Items  []dbq.LirAllocation `json:"items"`
	Total  int64               `json:"total"`
	Limit  int32               `json:"limit"`
	Offset int32               `json:"offset"`
}

func (h *Handler) listAllocations(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	q := r.URL.Query()
	limit := parseInt32(pageSize(q), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	var statusFilter *string
	if v := q.Get("status"); v != "" {
		statusFilter = &v
	}
	orgIDs, scoped := scopedOrgIDs(p, capAllocationsRead)
	if scoped && len(orgIDs) == 0 {
		httpx.JSON(w, http.StatusOK, listAllocationsResponse{
			Items: []dbq.LirAllocation{}, Total: 0, Limit: limit, Offset: offset,
		})
		return
	}
	items, err := h.Q.ListLirAllocations(r.Context(), dbq.ListLirAllocationsParams{
		Limit: limit, Offset: offset,
		ScopeOrgIds: orgIDs, StatusFilter: statusFilter,
	})
	if err != nil {
		mapErr(w, err, "")
		return
	}
	total, err := h.Q.CountLirAllocations(r.Context(), dbq.CountLirAllocationsParams{
		ScopeOrgIds: orgIDs, StatusFilter: statusFilter,
	})
	if err != nil {
		mapErr(w, err, "")
		return
	}
	if items == nil {
		items = []dbq.LirAllocation{}
	}
	httpx.JSON(w, http.StatusOK, listAllocationsResponse{
		Items: items, Total: total, Limit: limit, Offset: offset,
	})
}

func (h *Handler) getAllocation(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	alloc, err := h.Q.GetLirAllocation(r.Context(), id)
	if err != nil {
		mapErr(w, err, msgAllocationNotFound)
		return
	}
	if scope := auth.FindScope(p, capAllocationsRead); scope != nil && !scope.OrganizationMatches(alloc.OrganizationID) {
		httpx.Error(w, http.StatusNotFound, msgAllocationNotFound)
		return
	}
	httpx.JSON(w, http.StatusOK, alloc)
}
