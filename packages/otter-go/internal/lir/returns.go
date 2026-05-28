// Return lifecycle handlers.
//
//   POST /allocations/{id}/return-request  (tenant)
//   POST /allocations/{id}/return-confirm  (NIC)
//
// State machine on lir_allocations.status:
//
//   active ─request─▶ return_requested ─confirm─▶ returned
//
// Confirm also flips arin_status from 'registered' to 'removing' so
// the worker picks the row up and calls Reg-RWS DELETE. Rows that
// never reached ARIN (arin_status='none' / 'failed' / etc) skip the
// remove path — there's nothing upstream to deassign.
package lir

import (
	"net/http"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// ---- return-request (tenant) ----

type returnRequestReq struct {
	Reason string `json:"reason"`
}

func (h *Handler) requestReturnAllocation(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var body returnRequestReq
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Reason == "" {
		httpx.Error(w, http.StatusUnprocessableEntity, "reason is required")
		return
	}
	existing, err := h.Q.GetLirAllocation(r.Context(), id)
	if err != nil {
		mapErr(w, err, msgAllocationNotFound)
		return
	}
	if scope := auth.FindScope(p, capAllocationsReturnRequest); scope != nil && !scope.OrganizationMatches(existing.OrganizationID) {
		httpx.Error(w, http.StatusNotFound, msgAllocationNotFound)
		return
	}
	if existing.Status != "active" {
		httpx.Error(w, http.StatusConflict,
			"allocation must be 'active' to request return")
		return
	}
	out, err := h.Q.RequestReturnLirAllocation(r.Context(), dbq.RequestReturnLirAllocationParams{
		ID: id, ReturnRequestedByUserID: p.Subject, ReturnReason: body.Reason,
	})
	if err != nil {
		// Atomic UPDATE matches only status='active'; a racer that
		// flipped first makes RETURNING empty (pgx.ErrNoRows). 409.
		status, msg := httpx.Mapped(err)
		if status == http.StatusNotFound {
			httpx.Error(w, http.StatusConflict,
				"allocation raced out of 'active' state; re-fetch and retry")
			return
		}
		httpx.Error(w, status, msg)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "lir.allocation.return-request", TargetType: "lir_allocation",
		TargetID: id.String(),
		Metadata: map[string]any{"reason": body.Reason},
	})
	httpx.JSON(w, http.StatusOK, out)
}

// ---- return-confirm (NIC) ----

type returnConfirmReq struct {
	Notes *string `json:"notes"`
}

func (h *Handler) confirmReturnAllocation(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var body returnConfirmReq
	if r.ContentLength > 0 {
		if !decodeBody(w, r, &body) {
			return
		}
	}
	existing, err := h.Q.GetLirAllocation(r.Context(), id)
	if err != nil {
		mapErr(w, err, msgAllocationNotFound)
		return
	}
	if scope := auth.FindScope(p, capAllocationsReturnConfirm); scope != nil && !scope.OrganizationMatches(existing.OrganizationID) {
		httpx.Error(w, http.StatusNotFound, msgAllocationNotFound)
		return
	}
	if existing.Status != "return_requested" {
		httpx.Error(w, http.StatusConflict,
			"allocation must be 'return_requested' to confirm return")
		return
	}
	out, err := h.Q.ConfirmReturnLirAllocation(r.Context(), dbq.ConfirmReturnLirAllocationParams{
		ID: id, ReturnedByUserID: p.Subject,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		if status == http.StatusNotFound {
			httpx.Error(w, http.StatusConflict,
				"allocation raced out of 'return_requested'; re-fetch and retry")
			return
		}
		httpx.Error(w, status, msg)
		return
	}
	meta := map[string]any{
		"prior_arin_status": existing.ArinStatus,
		"new_arin_status":   out.ArinStatus,
	}
	if body.Notes != nil {
		meta["notes"] = *body.Notes
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "lir.allocation.return-confirm", TargetType: "lir_allocation",
		TargetID: id.String(),
		Metadata: meta,
	})
	httpx.JSON(w, http.StatusOK, out)
}
