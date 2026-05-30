// Package lir holds HTTP handlers for /api/v1/lir/* — the Local
// Internet Registry workflow. Phase 2 covers pool management (CRUD +
// pool↔supernet linkage); request submission, the approval engine,
// allocation, and ARIN Reg-RWS feed-up land in later phases.
//
// Schema is owned by Alembic migration 20260528_0065. Capability
// codes live in packages/otter/src/dcim/security/capabilities.py
// under the `lir` domain; otter-go just checks held capability
// strings via auth.RequireCapability.
package lir

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// Capability codes — kept as constants so the route table and the
// audit/test files don't drift apart on a rename. Mirror the codes
// under the `lir` domain in
// packages/otter/src/dcim/security/capabilities.py.
const (
	capPoolsRead      = "lir:pools:read"
	capPoolsCreate    = "lir:pools:create"
	capPoolsUpdate    = "lir:pools:update"
	capPoolsDelete    = "lir:pools:delete"
	capRequestsCreate  = "lir:requests:create"
	capRequestsRead    = "lir:requests:read"
	capRequestsCancel  = "lir:requests:cancel"
	capRequestsApprove      = "lir:requests:approve"
	capRequestsReject       = "lir:requests:reject"
	capAllocationsRead          = "lir:allocations:read"
	capAllocationsArinRetry     = "lir:allocations:arin-retry"
	capAllocationsReturnRequest = "lir:allocations:return-request"
	capAllocationsReturnConfirm = "lir:allocations:return-confirm"
)

// LandingFabricSlug is the seeded slug from migration 0065. Looked up
// at approve time so allocated tenant Supernets land in the system
// holding fabric; the tenant relocates them via the IPAM 'move'
// endpoint (phase 7 frontend / phase 4-followup backend).
const LandingFabricSlug = "lir-unassigned"

// Querier is the slice of sqlc methods this handler needs. Tests
// substitute an in-memory fake; *dbq.Queries satisfies it.
type Querier interface {
	// Pools
	ListLirPools(ctx context.Context, arg dbq.ListLirPoolsParams) ([]dbq.LirPool, error)
	CountLirPools(ctx context.Context) (int64, error)
	GetLirPool(ctx context.Context, id uuid.UUID) (dbq.LirPool, error)
	CreateLirPool(ctx context.Context, arg dbq.CreateLirPoolParams) (dbq.LirPool, error)
	UpdateLirPool(ctx context.Context, arg dbq.UpdateLirPoolParams) (dbq.LirPool, error)
	DeleteLirPool(ctx context.Context, id uuid.UUID) error
	CountAllocationsForPool(ctx context.Context, poolID uuid.UUID) (int64, error)

	// Pool ↔ supernet linkage
	ListPoolSourceSupernets(ctx context.Context, arg dbq.ListPoolSourceSupernetsParams) ([]dbq.PoolSourceSupernetRow, error)
	CountPoolSourceSupernets(ctx context.Context, poolID uuid.UUID) (int64, error)
	GetSupernetForLirAttach(ctx context.Context, id uuid.UUID) (dbq.SupernetLirAttachRow, error)
	AttachSupernetToPool(ctx context.Context, arg dbq.AttachSupernetToPoolParams) error
	DetachSupernetFromPool(ctx context.Context, arg dbq.DetachSupernetFromPoolParams) error
	DetachAllPoolSupernets(ctx context.Context, poolID uuid.UUID) error
	CountAllocationsForPoolSupernet(ctx context.Context, poolSupernetID uuid.UUID) (int64, error)

	// Requests
	CreateLirRequest(ctx context.Context, arg dbq.CreateLirRequestParams) (dbq.LirRequest, error)
	GetLirRequest(ctx context.Context, id uuid.UUID) (dbq.LirRequest, error)
	ListLirRequests(ctx context.Context, arg dbq.ListLirRequestsParams) ([]dbq.LirRequest, error)
	CountLirRequests(ctx context.Context, arg dbq.CountLirRequestsParams) (int64, error)
	CancelLirRequest(ctx context.Context, arg dbq.CancelLirRequestParams) (dbq.LirRequest, error)

	// Allocation engine (approve / reject + reads)
	GetLandingFabric(ctx context.Context, slug string) (dbq.LandingFabricRow, error)
	ListPoolSupernetsForCarve(ctx context.Context, poolID uuid.UUID) ([]dbq.PoolSupernetForCarveRow, error)
	ListAllocatedPrefixesInPool(ctx context.Context, poolID uuid.UUID) ([]dbq.AllocatedPrefixRow, error)
	ApproveLirRequest(ctx context.Context, arg dbq.ApproveLirRequestParams) (dbq.ApprovalResultRow, error)
	RejectLirRequest(ctx context.Context, arg dbq.RejectLirRequestParams) (dbq.LirRequest, error)
	GetLirAllocation(ctx context.Context, id uuid.UUID) (dbq.LirAllocation, error)
	ListLirAllocations(ctx context.Context, arg dbq.ListLirAllocationsParams) ([]dbq.LirAllocation, error)
	CountLirAllocations(ctx context.Context, arg dbq.CountLirAllocationsParams) (int64, error)

	// ARIN manual retry (worker-side claim/mark methods live on
	// *dbq.Queries directly — the API surface only needs reset).
	ResetArinJobForRetry(ctx context.Context, id uuid.UUID) error

	// Return lifecycle
	RequestReturnLirAllocation(ctx context.Context, arg dbq.RequestReturnLirAllocationParams) (dbq.LirAllocation, error)
	ConfirmReturnLirAllocation(ctx context.Context, arg dbq.ConfirmReturnLirAllocationParams) (dbq.LirAllocation, error)
}

type Handler struct {
	Q     Querier
	Audit audit.Recorder
}

func (h *Handler) Mount(r chi.Router) {
	r.Route("/lir", func(r chi.Router) {
		r.Route("/pools", func(r chi.Router) {
			r.With(auth.RequireCapability(capPoolsRead)).Get("/", h.listPools)
			r.With(auth.RequireCapability(capPoolsCreate)).Post("/", h.createPool)
			r.Route("/{id}", func(r chi.Router) {
				r.With(auth.RequireCapability(capPoolsRead)).Get("/", h.getPool)
				r.With(auth.RequireCapability(capPoolsUpdate)).Patch("/", h.updatePool)
				r.With(auth.RequireCapability(capPoolsDelete)).Delete("/", h.deletePool)
				r.Route("/supernets", func(r chi.Router) {
					r.With(auth.RequireCapability(capPoolsRead)).Get("/", h.listPoolSupernets)
					r.With(auth.RequireCapability(capPoolsUpdate)).Post("/", h.attachPoolSupernet)
					r.With(auth.RequireCapability(capPoolsUpdate)).Delete("/{supernet_id}", h.detachPoolSupernet)
				})
			})
		})
		r.Route("/requests", func(r chi.Router) {
			r.With(auth.RequireCapability(capRequestsRead)).Get("/", h.listRequests)
			r.With(auth.RequireCapability(capRequestsCreate)).Post("/", h.submitRequest)
			r.Route("/{id}", func(r chi.Router) {
				r.With(auth.RequireCapability(capRequestsRead)).Get("/", h.getRequest)
				r.With(auth.RequireCapability(capRequestsCancel)).Post("/cancel", h.cancelRequest)
				r.With(auth.RequireCapability(capRequestsApprove)).Post("/approve", h.approveRequest)
				r.With(auth.RequireCapability(capRequestsReject)).Post("/reject", h.rejectRequest)
			})
		})
		r.Route("/allocations", func(r chi.Router) {
			r.With(auth.RequireCapability(capAllocationsRead)).Get("/", h.listAllocations)
			r.Route("/{id}", func(r chi.Router) {
				r.With(auth.RequireCapability(capAllocationsRead)).Get("/", h.getAllocation)
				r.With(auth.RequireCapability(capAllocationsArinRetry)).
					Post("/arin/retry", h.retryAllocationArin)
				r.With(auth.RequireCapability(capAllocationsReturnRequest)).
					Post("/return-request", h.requestReturnAllocation)
				r.With(auth.RequireCapability(capAllocationsReturnConfirm)).
					Post("/return-confirm", h.confirmReturnAllocation)
			})
		})
	})
}

// ---- shared helpers ----

func mapErr(w http.ResponseWriter, err error, notFoundMsg string) {
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, notFoundMsg)
		return
	}
	status, msg := httpx.Mapped(err)
	httpx.Error(w, status, msg)
}

func parseUUIDParam(w http.ResponseWriter, r *http.Request, key string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, key))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, key+" is not a uuid")
		return uuid.Nil, false
	}
	return id, true
}

// parseOptionalUUID parses a body field that may be nil (field absent),
// non-nil empty (caller wants the column cleared), or a UUID string.
// Returns the parsed pointer + ok=false on parse failure (writes 400).
func parseOptionalUUID(w http.ResponseWriter, raw *string, field string) (*uuid.UUID, bool) {
	if raw == nil {
		return nil, true
	}
	if *raw == "" {
		// Explicit empty string → clear the column. Matches the
		// CASE-WHEN pattern in mutations_bgp_org_coll.sql where a
		// non-nil *string can carry an empty value to clear.
		return nil, true
	}
	id, err := uuid.Parse(*raw)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, field+" is not a uuid")
		return nil, false
	}
	return &id, true
}

// ---- pool reads ----

type listPoolsResponse struct {
	Items  []dbq.LirPool `json:"items"`
	Total  int64         `json:"total"`
	Limit  int32         `json:"limit"`
	Offset int32         `json:"offset"`
}

func (h *Handler) listPools(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	items, err := h.Q.ListLirPools(r.Context(), dbq.ListLirPoolsParams{Limit: limit, Offset: offset})
	if err != nil {
		mapErr(w, err, "")
		return
	}
	total, err := h.Q.CountLirPools(r.Context())
	if err != nil {
		mapErr(w, err, "")
		return
	}
	if items == nil {
		items = []dbq.LirPool{}
	}
	httpx.JSON(w, http.StatusOK, listPoolsResponse{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (h *Handler) getPool(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	pool, err := h.Q.GetLirPool(r.Context(), id)
	if err != nil {
		mapErr(w, err, "lir pool not found")
		return
	}
	httpx.JSON(w, http.StatusOK, pool)
}

// ---- shared request/validation primitives used by mutations.go ----

// validateFamilyPrefix mirrors ck_lir_pool_prefix_bounds in
// migration 0065. Family must be 4 or 6; prefix length must fit in
// the family's address width. Returned errors are surfaced as 422.
func validateFamilyPrefix(family int16, prefixLen int16) error {
	if family != 4 && family != 6 {
		return validationErr("ip_family must be 4 or 6")
	}
	if prefixLen < 0 {
		return validationErr("prefix length must be non-negative")
	}
	var cap int16 = 32
	if family == 6 {
		cap = 128
	}
	if prefixLen > cap {
		return validationErr("prefix length exceeds family cap")
	}
	return nil
}

type validationError struct{ msg string }

func (e *validationError) Error() string { return e.msg }
func validationErr(msg string) error     { return &validationError{msg: msg} }

// writeValidationError writes an HTTP error response for any err
// returned by the package's validators. *validationError surfaces
// as 422 with the field-specific message; any other non-nil error
// surfaces as the httpx default-mapped status (typically 500) so
// nothing falls through to a handler executing with bad input.
//
// Earlier shape returned bool and callers wrapped it in
// `if writeValidationError(...) { return }` — a non-validation
// error path silently skipped the return. The current shape
// always writes a response and the callers always return after
// calling.
func writeValidationError(w http.ResponseWriter, err error) {
	var v *validationError
	if errors.As(err, &v) {
		httpx.Error(w, http.StatusUnprocessableEntity, v.msg)
		return
	}
	status, msg := httpx.Mapped(err)
	httpx.Error(w, status, msg)
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return false
	}
	return true
}
