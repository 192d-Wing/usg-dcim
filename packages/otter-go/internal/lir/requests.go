// LIR request lifecycle handlers — submit, list (org-scope filtered),
// get, cancel. Approve / reject move into a follow-up alongside the
// allocation engine (phase 4).
//
// Authorization model:
//   - submit: requires lir:requests:create. If the principal's scope
//     for that cap is non-global, the chosen organization_id must be
//     in scope.OrganizationIDs — otherwise 403.
//   - list / get: requires lir:requests:read. Non-global scope filters
//     down to scope.OrganizationIDs. A non-global principal with no
//     OrganizationIDs sees an empty list.
//   - cancel: requires lir:requests:cancel. The capability gates the
//     action; the org-scope check still applies on get-before-mutate.
//     A non-requester with the cap globally CAN cancel — that's the
//     intentional escape hatch for admins, and the audit row records
//     who did it.
package lir

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

const msgRequestNotFound = "lir request not found"

// ---- submit ----

type submitReq struct {
	OrganizationID string  `json:"organization_id"`
	PoolID         *string `json:"pool_id"`
	SiteID         *string `json:"site_id"`
	IpFamily       int16   `json:"ip_family"`
	PrefixLength   int16   `json:"prefix_length"`
	Purpose        *string `json:"purpose"`
	Classification *string `json:"classification"`
	Justification  string  `json:"justification"`
}

func (h *Handler) submitRequest(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.From(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing principal")
		return
	}
	var req submitReq
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Justification == "" {
		httpx.Error(w, http.StatusUnprocessableEntity, "justification is required")
		return
	}
	if err := validateFamilyPrefix(req.IpFamily, req.PrefixLength); err != nil {
		writeValidationError(w, err)
		return
	}
	orgIDPtr, ok := parseOptionalUUID(w, &req.OrganizationID, "organization_id")
	if !ok {
		return
	}
	if orgIDPtr == nil {
		httpx.Error(w, http.StatusBadRequest, "organization_id is required")
		return
	}
	orgID := *orgIDPtr
	// Org-scope check: a non-global principal can only submit on
	// orgs their lir:requests:create scope covers. FindScope returns
	// nil when the cap isn't held at all, but RequireCapability has
	// already gated that path — we only reach here with the cap.
	if scope := auth.FindScope(p, capRequestsCreate); scope != nil && !scope.OrganizationMatches(orgID) {
		httpx.Error(w, http.StatusForbidden,
			"organization is outside your lir:requests:create scope")
		return
	}
	poolIDPtr, ok := parseOptionalUUID(w, req.PoolID, "pool_id")
	if !ok {
		return
	}
	siteIDPtr, ok := parseOptionalUUID(w, req.SiteID, "site_id")
	if !ok {
		return
	}
	out, err := h.Q.CreateLirRequest(r.Context(), dbq.CreateLirRequestParams{
		OrganizationID:  orgID,
		RequesterUserID: p.Subject,
		PoolID:          poolIDPtr,
		SiteID:          siteIDPtr,
		IpFamily:        req.IpFamily,
		PrefixLength:    req.PrefixLength,
		Purpose:         req.Purpose,
		Classification:  req.Classification,
		Justification:   req.Justification,
	})
	if err != nil {
		mapErr(w, err, "")
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "lir.request.submit", TargetType: "lir_request",
		TargetID: out.ID.String(),
		Metadata: map[string]any{"organization_id": orgID.String()},
	})
	httpx.JSON(w, http.StatusCreated, out)
}

// ---- list ----

type listRequestsResponse struct {
	Items  []dbq.LirRequest `json:"items"`
	Total  int64            `json:"total"`
	Limit  int32            `json:"limit"`
	Offset int32            `json:"offset"`
}

// scopedOrgIDs returns (ids, scoped) where:
//   - scoped=false → principal is global; caller passes nil to SQL
//   - scoped=true, ids non-empty → restrict to those org_ids
//   - scoped=true, ids empty → no orgs in scope; caller short-circuits
//     to an empty page without hitting the DB
func scopedOrgIDs(p auth.Principal, capCode string) (ids []uuid.UUID, scoped bool) {
	s := auth.FindScope(p, capCode)
	if s == nil || s.IsGlobal {
		return nil, false
	}
	out := make([]uuid.UUID, 0, len(s.OrganizationIDs))
	for id := range s.OrganizationIDs {
		out = append(out, id)
	}
	return out, true
}

func (h *Handler) listRequests(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	var statusFilter *string
	if v := q.Get("status"); v != "" {
		statusFilter = &v
	}
	orgIDs, scoped := scopedOrgIDs(p, capRequestsRead)
	if scoped && len(orgIDs) == 0 {
		// Scoped but no orgs in scope → empty page, no SQL.
		httpx.JSON(w, http.StatusOK, listRequestsResponse{
			Items: []dbq.LirRequest{}, Total: 0, Limit: limit, Offset: offset,
		})
		return
	}
	items, err := h.Q.ListLirRequests(r.Context(), dbq.ListLirRequestsParams{
		Limit: limit, Offset: offset,
		ScopeOrgIds: orgIDs, StatusFilter: statusFilter,
	})
	if err != nil {
		mapErr(w, err, "")
		return
	}
	total, err := h.Q.CountLirRequests(r.Context(), dbq.CountLirRequestsParams{
		ScopeOrgIds: orgIDs, StatusFilter: statusFilter,
	})
	if err != nil {
		mapErr(w, err, "")
		return
	}
	if items == nil {
		items = []dbq.LirRequest{}
	}
	httpx.JSON(w, http.StatusOK, listRequestsResponse{
		Items: items, Total: total, Limit: limit, Offset: offset,
	})
}

// ---- get ----

func (h *Handler) getRequest(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	req, err := h.Q.GetLirRequest(r.Context(), id)
	if err != nil {
		mapErr(w, err, msgRequestNotFound)
		return
	}
	// Org-scope check after fetch — a 404 leaks no info about
	// orgs the caller can't see; a successful fetch then checked
	// against scope is the same pattern other site-rooted gets use.
	if scope := auth.FindScope(p, capRequestsRead); scope != nil && !scope.OrganizationMatches(req.OrganizationID) {
		httpx.Error(w, http.StatusNotFound, msgRequestNotFound)
		return
	}
	httpx.JSON(w, http.StatusOK, req)
}

// ---- cancel ----

type cancelReq struct {
	Notes *string `json:"notes"`
}

func (h *Handler) cancelRequest(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	// Pre-fetch so we can scope-check and tell a stale-status 409
	// (already approved/rejected/cancelled) apart from a not-found 404.
	existing, err := h.Q.GetLirRequest(r.Context(), id)
	if err != nil {
		mapErr(w, err, msgRequestNotFound)
		return
	}
	if scope := auth.FindScope(p, capRequestsCancel); scope != nil && !scope.OrganizationMatches(existing.OrganizationID) {
		httpx.Error(w, http.StatusNotFound, msgRequestNotFound)
		return
	}
	if existing.Status != "pending_approval" {
		httpx.Error(w, http.StatusConflict,
			"request is not in pending_approval; cannot cancel")
		return
	}
	var body cancelReq
	// Body is optional — empty POST body should still cancel. Don't
	// fail the request just because the operator omitted notes.
	if r.ContentLength > 0 {
		if !decodeBody(w, r, &body) {
			return
		}
	}
	out, err := h.Q.CancelLirRequest(r.Context(), dbq.CancelLirRequestParams{
		ID: id, Notes: body.Notes,
	})
	if err != nil {
		// The atomic WHERE id = $1 AND status = 'pending_approval'
		// returns zero rows when the request raced into a decided
		// state between our pre-fetch and the UPDATE; pgx surfaces
		// that as ErrNoRows. Map to 409 not 404 — the row exists,
		// just isn't cancellable anymore.
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusConflict,
				"request is not in pending_approval; cannot cancel")
			return
		}
		mapErr(w, err, msgRequestNotFound)
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "lir.request.cancel", TargetType: "lir_request",
		TargetID: out.ID.String(),
	})
	httpx.JSON(w, http.StatusOK, out)
}
