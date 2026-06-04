// NIC registration lifecycle handlers — create, list, get, submit, cancel,
// approve, reject. Mirrors the org-scoped authorization model of internal/lir:
//   - create:  nicreg:requests:create; chosen organization_id must be in the
//     principal's scope when non-global.
//   - list/get: nicreg:requests:read; non-global scope filters to its orgs.
//   - submit:  nicreg:requests:update (draft -> submitted).
//   - cancel:  nicreg:requests:cancel (draft|submitted -> cancelled).
//   - approve: nicreg:requests:approve (submitted -> approved); records the
//     push_to_arin upstream-routing decision.
//   - reject:  nicreg:requests:reject (submitted -> rejected).
package nicreg

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// registrationResponse bundles the lifecycle header with its typed detail row.
type registrationResponse struct {
	Registration dbq.NicRegistration `json:"registration"`
	Detail       any                 `json:"detail"`
}

// ---- create ----

type createReq struct {
	TemplateType   string         `json:"template_type"`
	ActionType     string         `json:"action_type"`
	OrganizationID string         `json:"organization_id"`
	Status         string         `json:"status"` // "draft" | "submitted" (default)
	Payload        map[string]any `json:"payload"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.From(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "missing principal")
		return
	}
	var req createReq
	if !decodeBody(w, r, &req) {
		return
	}
	ts, known := Template(req.TemplateType)
	if !known {
		httpx.Error(w, http.StatusUnprocessableEntity, "unknown template_type: "+req.TemplateType)
		return
	}
	if !ts.ActionAllowed(req.ActionType) {
		httpx.Error(w, http.StatusUnprocessableEntity, "invalid action_type for "+req.TemplateType)
		return
	}
	status := req.Status
	if status == "" {
		status = "submitted"
	}
	if status != "draft" && status != "submitted" {
		httpx.Error(w, http.StatusUnprocessableEntity, "status must be draft or submitted")
		return
	}
	orgID, err := uuid.Parse(req.OrganizationID)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "organization_id is required and must be a uuid")
		return
	}
	if scope := auth.FindScope(p, capCreate); scope != nil && !scope.OrganizationMatches(orgID) {
		httpx.Error(w, http.StatusForbidden, "organization is outside your nicreg:requests:create scope")
		return
	}
	if req.Payload == nil {
		req.Payload = map[string]any{}
	}
	if err := Validate(req.TemplateType, req.ActionType, req.Payload); err != nil {
		writeValidationError(w, err)
		return
	}

	hdr, detail, err := h.runCreate(r.Context(), req, status, orgID, p.Subject)
	if err != nil {
		writeValidationError(w, err) // *ValidationError -> 422; else mapped
		return
	}
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: "nicreg.create", TargetType: "nic_registration",
		TargetID: hdr.ID.String(),
		Metadata: map[string]any{
			"template_type": req.TemplateType, "organization_id": orgID.String(), "status": status,
		},
	})
	httpx.JSON(w, http.StatusCreated, registrationResponse{Registration: hdr, Detail: detail})
}

// runCreate writes the header + typed detail atomically. When Pool is nil
// (tests) it falls back to autocommit on h.Q.
func (h *Handler) runCreate(ctx context.Context, req createReq, status string, orgID, requester uuid.UUID) (dbq.NicRegistration, any, error) {
	headerParams := dbq.CreateNicRegistrationParams{
		TemplateType:    req.TemplateType,
		ActionType:      req.ActionType,
		OrganizationID:  orgID,
		RequesterUserID: requester,
		Status:          status,
	}
	run := func(q Querier) (dbq.NicRegistration, any, error) {
		hdr, err := q.CreateNicRegistration(ctx, headerParams)
		if err != nil {
			return dbq.NicRegistration{}, nil, err
		}
		detail, err := insertDetail(ctx, q, hdr.ID, req.TemplateType, req.Payload)
		if err != nil {
			return dbq.NicRegistration{}, nil, err
		}
		return hdr, detail, nil
	}
	if h.Pool == nil {
		return run(h.Q)
	}
	tx, err := h.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return dbq.NicRegistration{}, nil, err
	}
	defer func() {
		rbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rbCtx)
	}()
	hdr, detail, err := run(dbq.New(tx))
	if err != nil {
		return dbq.NicRegistration{}, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return dbq.NicRegistration{}, nil, err
	}
	return hdr, detail, nil
}

// ---- list ----

type listResponse struct {
	Items  []dbq.NicRegistration `json:"items"`
	Total  int64                 `json:"total"`
	Limit  int32                 `json:"limit"`
	Offset int32                 `json:"offset"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.From(r.Context())
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	var statusFilter, typeFilter *string
	if v := q.Get("status"); v != "" {
		statusFilter = &v
	}
	if v := q.Get("template_type"); v != "" {
		typeFilter = &v
	}
	orgIDs, scoped := scopedOrgIDs(p, capRead)
	if scoped && len(orgIDs) == 0 {
		httpx.JSON(w, http.StatusOK, listResponse{Items: []dbq.NicRegistration{}, Total: 0, Limit: limit, Offset: offset})
		return
	}
	items, err := h.Q.ListNicRegistrations(r.Context(), dbq.ListNicRegistrationsParams{
		Limit: limit, Offset: offset,
		ScopeOrgIds: orgIDs, StatusFilter: statusFilter, TypeFilter: typeFilter,
	})
	if err != nil {
		mapErr(w, err, "")
		return
	}
	total, err := h.Q.CountNicRegistrations(r.Context(), dbq.CountNicRegistrationsParams{
		ScopeOrgIds: orgIDs, StatusFilter: statusFilter, TypeFilter: typeFilter,
	})
	if err != nil {
		mapErr(w, err, "")
		return
	}
	if items == nil {
		items = []dbq.NicRegistration{}
	}
	httpx.JSON(w, http.StatusOK, listResponse{Items: items, Total: total, Limit: limit, Offset: offset})
}

// loadScoped fetches the registration named by the URL {id} and enforces the
// principal's org-scope for capCode. On any failure it writes the response and
// returns ok=false — a bad id is 400, a missing row 404, and an out-of-scope
// row is masked as 404 so it leaks nothing about orgs the caller can't see.
// Shared by get/submit/cancel/approve/reject so the load+scope preamble lives
// in one place.
func (h *Handler) loadScoped(w http.ResponseWriter, r *http.Request, capCode string) (auth.Principal, dbq.NicRegistration, bool) {
	p, _ := auth.From(r.Context())
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return p, dbq.NicRegistration{}, false
	}
	reg, err := h.Q.GetNicRegistration(r.Context(), id)
	if err != nil {
		mapErr(w, err, msgNotFound)
		return p, dbq.NicRegistration{}, false
	}
	if scope := auth.FindScope(p, capCode); scope != nil && !scope.OrganizationMatches(reg.OrganizationID) {
		httpx.Error(w, http.StatusNotFound, msgNotFound)
		return p, dbq.NicRegistration{}, false
	}
	return p, reg, true
}

// conflictOr409 maps the zero-rows result of an atomic check-and-flip UPDATE
// (the row raced out of the expected status) to a 409 with msg; anything else
// goes through the default error mapping.
func conflictOr409(w http.ResponseWriter, err error, msg string) {
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusConflict, msg)
		return
	}
	mapErr(w, err, msgNotFound)
}

func auditEvent(r *http.Request, h *Handler, action, id string, meta map[string]any) {
	audit.Record(r.Context(), h.Audit, nil, audit.Event{
		Action: action, TargetType: "nic_registration", TargetID: id, Metadata: meta,
	})
}

// ---- get (header + detail) ----

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	_, reg, ok := h.loadScoped(w, r, capRead)
	if !ok {
		return
	}
	detail, err := fetchDetail(r.Context(), h.Q, reg.ID, reg.TemplateType)
	if err != nil {
		mapErr(w, err, msgNotFound)
		return
	}
	httpx.JSON(w, http.StatusOK, registrationResponse{Registration: reg, Detail: detail})
}

// ---- submit (draft -> submitted) ----

func (h *Handler) submit(w http.ResponseWriter, r *http.Request) {
	_, reg, ok := h.loadScoped(w, r, capUpdate)
	if !ok {
		return
	}
	out, err := h.Q.SubmitNicRegistration(r.Context(), reg.ID)
	if err != nil {
		conflictOr409(w, err, "registration is not a draft; cannot submit")
		return
	}
	auditEvent(r, h, "nicreg.submit", out.ID.String(), nil)
	httpx.JSON(w, http.StatusOK, out)
}

// ---- cancel (draft|submitted -> cancelled) ----

type notesReq struct {
	Notes *string `json:"notes"`
}

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) {
	_, reg, ok := h.loadScoped(w, r, capCancel)
	if !ok {
		return
	}
	var body notesReq
	if r.ContentLength > 0 && !decodeBody(w, r, &body) {
		return
	}
	out, err := h.Q.CancelNicRegistration(r.Context(), dbq.CancelNicRegistrationParams{ID: reg.ID, Notes: body.Notes})
	if err != nil {
		conflictOr409(w, err, "registration is already decided; cannot cancel")
		return
	}
	auditEvent(r, h, "nicreg.cancel", out.ID.String(), nil)
	httpx.JSON(w, http.StatusOK, out)
}

// ---- approve (submitted -> approved, with push_to_arin decision) ----

type approveReq struct {
	PushToArin *bool   `json:"push_to_arin"`
	Notes      *string `json:"notes"`
}

func (h *Handler) approve(w http.ResponseWriter, r *http.Request) {
	p, reg, ok := h.loadScoped(w, r, capApprove)
	if !ok {
		return
	}
	var body approveReq
	if r.ContentLength > 0 && !decodeBody(w, r, &body) {
		return
	}
	// push_to_arin=true only makes sense for ARIN-eligible templates
	// (network, asn). Guard so a reviewer can't flag a domain/user/etc.
	if body.PushToArin != nil && *body.PushToArin {
		if ts, known := Template(reg.TemplateType); !known || !ts.ArinEligible {
			httpx.Error(w, http.StatusUnprocessableEntity,
				"push_to_arin applies only to network and asn registrations")
			return
		}
	}
	out, err := h.Q.ApproveNicRegistration(r.Context(), dbq.ApproveNicRegistrationParams{
		ID: reg.ID, PushToArin: body.PushToArin, DecidedBy: p.Subject, Notes: body.Notes,
	})
	if err != nil {
		conflictOr409(w, err, "registration is not submitted; cannot approve")
		return
	}
	auditEvent(r, h, "nicreg.approve", out.ID.String(), map[string]any{"push_to_arin": body.PushToArin})
	httpx.JSON(w, http.StatusOK, out)
}

// ---- reject (submitted -> rejected) ----

func (h *Handler) reject(w http.ResponseWriter, r *http.Request) {
	p, reg, ok := h.loadScoped(w, r, capReject)
	if !ok {
		return
	}
	var body notesReq
	if r.ContentLength > 0 && !decodeBody(w, r, &body) {
		return
	}
	out, err := h.Q.RejectNicRegistration(r.Context(), dbq.RejectNicRegistrationParams{
		ID: reg.ID, DecidedBy: p.Subject, Notes: body.Notes,
	})
	if err != nil {
		conflictOr409(w, err, "registration is not submitted; cannot reject")
		return
	}
	auditEvent(r, h, "nicreg.reject", out.ID.String(), nil)
	httpx.JSON(w, http.StatusOK, out)
}
