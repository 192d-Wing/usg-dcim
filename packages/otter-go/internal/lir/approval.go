// Approval engine + reject handler. Approval picks a free prefix
// inside the pool's source supernets (via the Go carver), then runs
// the atomic CTE in ApproveLirRequest to insert the tenant Supernet,
// insert the LirAllocation, and flip the request to 'approved' as a
// single statement.
//
// Family-vs-pool, status-must-be-pending, and pool-enabled validations
// happen up front so an obvious bad approval bubbles up as 4xx rather
// than landing in the CTE and rolling back. The carver runs over all
// source supernets in order; first-fit lowest-prefix wins. If every
// supernet is exhausted at the requested size, the handler returns
// 409 — the request stays pending so the NIC can redirect to a
// different pool.
package lir

import (
	"context"
	"net/http"
	"net/netip"

	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// ---- approve ----

type approveReq struct {
	ApprovedPoolID *string `json:"approved_pool_id"` // override; nil = use request.pool_id
	Notes          *string `json:"notes"`
}

type approveResponse struct {
	Request    dbq.LirRequest    `json:"request"`
	Allocation dbq.LirAllocation `json:"allocation"`
}

func (h *Handler) approveRequest(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	requestID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var body approveReq
	if r.ContentLength > 0 {
		if !decodeBody(w, r, &body) {
			return
		}
	}
	existing, ok := h.loadApprovableRequest(r, w, p, requestID)
	if !ok {
		return
	}
	poolIDPtr, ok := resolveApprovedPoolID(w, body.ApprovedPoolID, existing.PoolID)
	if !ok {
		return
	}
	poolID := *poolIDPtr
	pool, ok := h.validatePoolForApproval(r, w, poolID, existing)
	if !ok {
		return
	}
	chosenSupernetID, chosenPrefix, ok := h.carveForApproval(
		r.Context(), w, poolID, int(existing.PrefixLength),
	)
	if !ok {
		return
	}
	// Landing fabric (system row from migration 0065).
	landing, err := h.Q.GetLandingFabric(r.Context(), LandingFabricSlug)
	if err != nil {
		// Loud: the system fabric should always be present. Mapping
		// to 500 here (rather than 404) so the operator sees something
		// is fundamentally wrong with the deployment.
		httpx.Error(w, http.StatusInternalServerError,
			"landing fabric not found; deployment is missing migration 0065 seed")
		return
	}
	// Initial ARIN status: 'pending' iff the pool has an upstream
	// handle to reassign under; otherwise 'none'. Phase 5 wires the
	// worker that drains 'pending' / 'failed' rows.
	arinInit := "none"
	if pool.ArinParentNetHandle != nil && *pool.ArinParentNetHandle != "" {
		arinInit = "pending"
	}
	// Carve purpose: request's purpose wins; pool default fills the gap.
	purpose := existing.Purpose
	if purpose == nil {
		purpose = pool.DefaultSupernetPurpose
	}
	result, err := h.Q.ApproveLirRequest(r.Context(), dbq.ApproveLirRequestParams{
		RequestID:         requestID,
		DecidedByUserID:   p.Subject,
		DecisionNotes:     body.Notes,
		ApprovedPoolID:    poolID,
		PoolSupernetID:    chosenSupernetID,
		OrganizationID:    existing.OrganizationID,
		Prefix:            chosenPrefix,
		LandingFabricID:   landing.FabricID,
		LandingVrfID:      landing.DefaultVrfID,
		SupernetPurpose:   purpose,
		ArinInitialStatus: arinInit,
	})
	if !writeApprovalCTEError(w, err) {
		return
	}
	h.respondApprovalSuccess(r.Context(), w, requestID, poolID, chosenPrefix, arinInit, result)
}

// writeApprovalCTEError maps the CTE's error to an HTTP response.
// Returns true when err is nil (caller continues); false when an
// error response was written and the caller should bail.
func writeApprovalCTEError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return true
	}
	// The CTE matches lir_requests on (id, status='pending_approval');
	// a racer that flipped the row to another state between our pre-
	// fetch and the CTE makes RETURNING empty, which pgx surfaces as
	// ErrNoRows. Map that to 409 (row exists, just isn't approvable);
	// other errors (CHECK / FK / network) bubble through as 5xx.
	status, msg := httpx.Mapped(err)
	if status == http.StatusNotFound {
		httpx.Error(w, http.StatusConflict,
			"request raced out of pending_approval; re-fetch and retry")
		return false
	}
	httpx.Error(w, status, msg)
	return false
}

// respondApprovalSuccess emits the audit event and writes the
// response using the rows the CTE already returned. Earlier shape
// fired two extra GetLirRequest + GetLirAllocation queries to
// rebuild the response — wasteful since the CTE's SELECT now
// includes both row shapes.
func (h *Handler) respondApprovalSuccess(
	ctx context.Context, w http.ResponseWriter,
	requestID, poolID uuid.UUID, chosenPrefix, arinInit string,
	result dbq.ApproveLirRequestRow,
) {
	req, alloc := splitApprovalRow(result)
	audit.Record(ctx, h.Audit, nil, audit.Event{
		Action: "lir.request.approve", TargetType: "lir_request",
		TargetID: requestID.String(),
		Metadata: map[string]any{
			"allocation_id":      alloc.ID.String(),
			"tenant_supernet_id": alloc.TenantSupernetID.String(),
			"approved_pool_id":   poolID.String(),
			"prefix":             chosenPrefix,
			"arin_initial":       arinInit,
		},
	})
	httpx.JSON(w, http.StatusOK, approveResponse{
		Request: req, Allocation: alloc,
	})
}

// splitApprovalRow unpacks the CTE's flat SELECT (18 LirRequest
// columns then 22 LirAllocation columns, collision-suffixed _2 by
// sqlc) back into the two entities the response shape carries.
func splitApprovalRow(r dbq.ApproveLirRequestRow) (dbq.LirRequest, dbq.LirAllocation) {
	req := dbq.LirRequest{
		ID: r.ID, OrganizationID: r.OrganizationID,
		RequesterUserID: r.RequesterUserID, PoolID: r.PoolID,
		SiteID: r.SiteID, IPFamily: r.IPFamily,
		PrefixLength: r.PrefixLength, Purpose: r.Purpose,
		Classification: r.Classification, Justification: r.Justification,
		Status: r.Status, SubmittedAt: r.SubmittedAt,
		DecidedAt: r.DecidedAt, DecidedByUserID: r.DecidedByUserID,
		DecisionNotes: r.DecisionNotes, ApprovedPoolID: r.ApprovedPoolID,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	alloc := dbq.LirAllocation{
		ID: r.ID_2, RequestID: r.RequestID,
		OrganizationID: r.OrganizationID_2, PoolID: r.PoolID_2,
		PoolSupernetID: r.PoolSupernetID, TenantSupernetID: r.TenantSupernetID,
		Prefix: r.Prefix, AllocatedAt: r.AllocatedAt,
		AllocatedByUserID: r.AllocatedByUserID, Status: r.Status_2,
		ReturnRequestedAt:       r.ReturnRequestedAt,
		ReturnRequestedByUserID: r.ReturnRequestedByUserID,
		ReturnReason:            r.ReturnReason,
		ReturnedAt:              r.ReturnedAt,
		ReturnedByUserID:        r.ReturnedByUserID,
		ArinStatus:              r.ArinStatus,
		ArinNetHandle:           r.ArinNetHandle,
		ArinLastAttemptAt:       r.ArinLastAttemptAt,
		ArinLastError:           r.ArinLastError,
		ArinAttempts:            r.ArinAttempts,
		CreatedAt:               r.CreatedAt_2,
		UpdatedAt:               r.UpdatedAt_2,
	}
	return req, alloc
}

// loadApprovableRequest fetches the request, org-scope checks against
// lir:requests:approve, and verifies it's still pending. Returns
// (request, true) on success or writes the appropriate error and
// returns (_, false).
func (h *Handler) loadApprovableRequest(
	r *http.Request, w http.ResponseWriter, p auth.Principal, requestID uuid.UUID,
) (dbq.LirRequest, bool) {
	existing, err := h.Q.GetLirRequest(r.Context(), requestID)
	if err != nil {
		mapErr(w, err, msgRequestNotFound)
		return dbq.LirRequest{}, false
	}
	if scope := auth.FindScope(p, capRequestsApprove); scope != nil && !scope.OrganizationMatches(existing.OrganizationID) {
		// Out-of-scope: 404 not 403 to mirror the get/cancel posture.
		httpx.Error(w, http.StatusNotFound, msgRequestNotFound)
		return dbq.LirRequest{}, false
	}
	if existing.Status != "pending_approval" {
		httpx.Error(w, http.StatusConflict,
			"request is not in pending_approval; cannot approve")
		return dbq.LirRequest{}, false
	}
	return existing, true
}

// validatePoolForApproval fetches the chosen pool and validates that
// it's compatible with the request: enabled, family-match, and the
// requested prefix length is inside its bounds. Returns (pool, true)
// on success or writes the error and returns (_, false).
func (h *Handler) validatePoolForApproval(
	r *http.Request, w http.ResponseWriter, poolID uuid.UUID, existing dbq.LirRequest,
) (dbq.LirPool, bool) {
	pool, err := h.Q.GetLirPool(r.Context(), poolID)
	if err != nil {
		mapErr(w, err, msgPoolNotFound)
		return dbq.LirPool{}, false
	}
	if !pool.Enabled {
		httpx.Error(w, http.StatusConflict,
			"approved pool is disabled; enable it or pick another")
		return dbq.LirPool{}, false
	}
	if pool.IPFamily != existing.IPFamily {
		httpx.Error(w, http.StatusUnprocessableEntity,
			"pool family doesn't match request family")
		return dbq.LirPool{}, false
	}
	if existing.PrefixLength < pool.MinPrefixLength || existing.PrefixLength > pool.MaxPrefixLength {
		httpx.Error(w, http.StatusUnprocessableEntity,
			"requested prefix length is outside the pool's bounds")
		return dbq.LirPool{}, false
	}
	return pool, true
}

// resolveApprovedPoolID picks the final pool. Body override wins;
// nil body falls back to the request's pool preference; both nil
// is a 422 (engine has nothing to work with).
func resolveApprovedPoolID(w http.ResponseWriter, override *string, preference *uuid.UUID) (*uuid.UUID, bool) {
	if override != nil {
		id, ok := parseOptionalUUID(w, override, "approved_pool_id")
		if !ok {
			return nil, false
		}
		if id != nil {
			return id, true
		}
	}
	if preference != nil {
		return preference, true
	}
	httpx.Error(w, http.StatusUnprocessableEntity,
		"no pool to approve into: request had no pool_id and body had no override")
	return nil, false
}

// carveForApproval reads source supernets + existing allocations for
// the pool, then walks supernets in order asking the carver for a
// free prefix at the requested size. Returns (supernet_id, prefix,
// true) on success; on failure writes the appropriate HTTP error and
// returns (_, _, false).
func (h *Handler) carveForApproval(
	ctx context.Context, w http.ResponseWriter, poolID uuid.UUID, prefixLen int,
) (uuid.UUID, string, bool) {
	source, err := h.Q.ListPoolSupernetsForCarve(ctx, poolID)
	if err != nil {
		mapErr(w, err, "")
		return uuid.Nil, "", false
	}
	if len(source) == 0 {
		httpx.Error(w, http.StatusConflict,
			"pool has no source supernets; attach one before approving")
		return uuid.Nil, "", false
	}
	allocated, err := h.Q.ListAllocatedPrefixesInPool(ctx, poolID)
	if err != nil {
		mapErr(w, err, "")
		return uuid.Nil, "", false
	}
	// Bucket existing carved prefixes by their parent pool supernet.
	// The carver only needs to see overlaps inside the supernet
	// it's iterating — cross-supernet allocations can't collide.
	occupied := map[uuid.UUID][]netip.Prefix{}
	for _, a := range allocated {
		p, err := netip.ParsePrefix(a.Prefix)
		if err != nil {
			continue
		}
		occupied[a.PoolSupernetID] = append(occupied[a.PoolSupernetID], p)
	}
	for _, src := range source {
		parent, err := netip.ParsePrefix(src.Prefix)
		if err != nil {
			continue
		}
		if got, ok := findFirstFreePrefix(parent, prefixLen, occupied[src.ID]); ok {
			return src.ID, got, true
		}
	}
	httpx.Error(w, http.StatusConflict,
		"pool exhausted: no free range at the requested prefix length")
	return uuid.Nil, "", false
}

// ---- reject ----

type rejectReq struct {
	Reason string `json:"reason"`
}

func (h *Handler) rejectRequest(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	requestID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var body rejectReq
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Reason == "" {
		httpx.Error(w, http.StatusUnprocessableEntity, "reason is required")
		return
	}
	existing, err := h.Q.GetLirRequest(r.Context(), requestID)
	if err != nil {
		mapErr(w, err, msgRequestNotFound)
		return
	}
	if scope := auth.FindScope(p, capRequestsReject); scope != nil && !scope.OrganizationMatches(existing.OrganizationID) {
		httpx.Error(w, http.StatusNotFound, msgRequestNotFound)
		return
	}
	if existing.Status != "pending_approval" {
		httpx.Error(w, http.StatusConflict,
			"request is not in pending_approval; cannot reject")
		return
	}
	out, err := h.Q.RejectLirRequest(r.Context(), dbq.RejectLirRequestParams{
		ID:              requestID,
		DecidedByUserID: p.Subject,
		Reason:          body.Reason,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		if status == http.StatusNotFound {
			httpx.Error(w, http.StatusConflict,
				"request raced out of pending_approval; re-fetch and retry")
			return
		}
		httpx.Error(w, status, msg)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "lir.request.reject", TargetType: "lir_request",
		TargetID: requestID.String(),
		Metadata: map[string]any{"reason": body.Reason},
	})
	httpx.JSON(w, http.StatusOK, out)
}
