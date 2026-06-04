package nicreg

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// Capability codes — kept as constants so the route table and audit/tests
// don't drift on a rename. Mirror the `nicreg` domain in the capability
// source of truth.
const (
	capCreate  = "nicreg:requests:create"
	capRead    = "nicreg:requests:read"
	capUpdate  = "nicreg:requests:update"
	capCancel  = "nicreg:requests:cancel"
	capApprove = "nicreg:requests:approve"
	capReject  = "nicreg:requests:reject"
)

const msgNotFound = "registration not found"

// Querier is the slice of sqlc methods this handler needs. *dbq.Queries
// satisfies it (both the pool-bound handler Q and a tx-bound dbq.New(tx)).
type Querier interface {
	CreateNicRegistration(ctx context.Context, arg dbq.CreateNicRegistrationParams) (dbq.NicRegistration, error)
	GetNicRegistration(ctx context.Context, id uuid.UUID) (dbq.NicRegistration, error)
	ListNicRegistrations(ctx context.Context, arg dbq.ListNicRegistrationsParams) ([]dbq.NicRegistration, error)
	CountNicRegistrations(ctx context.Context, arg dbq.CountNicRegistrationsParams) (int64, error)
	SubmitNicRegistration(ctx context.Context, id uuid.UUID) (dbq.NicRegistration, error)
	CancelNicRegistration(ctx context.Context, arg dbq.CancelNicRegistrationParams) (dbq.NicRegistration, error)
	ApproveNicRegistration(ctx context.Context, arg dbq.ApproveNicRegistrationParams) (dbq.NicRegistration, error)
	RejectNicRegistration(ctx context.Context, arg dbq.RejectNicRegistrationParams) (dbq.NicRegistration, error)

	CreateNicRegOrganization(ctx context.Context, arg dbq.CreateNicRegOrganizationParams) (dbq.NicRegOrganization, error)
	CreateNicRegUser(ctx context.Context, arg dbq.CreateNicRegUserParams) (dbq.NicRegUser, error)
	CreateNicRegHost(ctx context.Context, arg dbq.CreateNicRegHostParams) (dbq.NicRegHost, error)
	CreateNicRegDomain(ctx context.Context, arg dbq.CreateNicRegDomainParams) (dbq.NicRegDomain, error)
	CreateNicRegNetwork(ctx context.Context, arg dbq.CreateNicRegNetworkParams) (dbq.NicRegNetwork, error)
	CreateNicRegAsn(ctx context.Context, arg dbq.CreateNicRegAsnParams) (dbq.NicRegAsn, error)
	CreateNicRegDnskey(ctx context.Context, arg dbq.CreateNicRegDnskeyParams) (dbq.NicRegDnskey, error)

	GetNicRegOrganization(ctx context.Context, registrationID uuid.UUID) (dbq.NicRegOrganization, error)
	GetNicRegUser(ctx context.Context, registrationID uuid.UUID) (dbq.NicRegUser, error)
	GetNicRegHost(ctx context.Context, registrationID uuid.UUID) (dbq.NicRegHost, error)
	GetNicRegDomain(ctx context.Context, registrationID uuid.UUID) (dbq.NicRegDomain, error)
	GetNicRegNetwork(ctx context.Context, registrationID uuid.UUID) (dbq.NicRegNetwork, error)
	GetNicRegAsn(ctx context.Context, registrationID uuid.UUID) (dbq.NicRegAsn, error)
	GetNicRegDnskey(ctx context.Context, registrationID uuid.UUID) (dbq.NicRegDnskey, error)
}

// txBeginner is the slice of *pgxpool.Pool the create path needs to run the
// header + detail inserts in one transaction. Nil in tests → autocommit.
type txBeginner interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

type Handler struct {
	Q     Querier
	Pool  txBeginner
	Audit audit.Recorder
}

// NewHandler wires the concrete pool-backed handler used by main.
func NewHandler(q Querier, pool *pgxpool.Pool, rec audit.Recorder) *Handler {
	return &Handler{Q: q, Pool: pool, Audit: rec}
}

func (h *Handler) Mount(r chi.Router) {
	r.Route("/nic-registrations", func(r chi.Router) {
		r.With(auth.RequireCapability(capRead)).Get("/schema", h.getSchema)
		r.With(auth.RequireCapability(capRead)).Get("/", h.list)
		r.With(auth.RequireCapability(capCreate)).Post("/", h.create)
		r.Route("/{id}", func(r chi.Router) {
			r.With(auth.RequireCapability(capRead)).Get("/", h.get)
			r.With(auth.RequireCapability(capUpdate)).Post("/submit", h.submit)
			r.With(auth.RequireCapability(capCancel)).Post("/cancel", h.cancel)
			r.With(auth.RequireCapability(capApprove)).Post("/approve", h.approve)
			r.With(auth.RequireCapability(capReject)).Post("/reject", h.reject)
		})
	})
}

// getSchema serves the embedded templates.json so the frontend (or an
// operator) can fetch the canonical field schema at runtime.
func (h *Handler) getSchema(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(SchemaBytes())
}

// ---- shared helpers (mirror internal/lir/handler.go) ----

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

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return false
	}
	return true
}

func writeValidationError(w http.ResponseWriter, err error) {
	var v *ValidationError
	if errors.As(err, &v) {
		httpx.Error(w, http.StatusUnprocessableEntity, v.Msg)
		return
	}
	status, msg := httpx.Mapped(err)
	httpx.Error(w, status, msg)
}

// scopedOrgIDs mirrors the LIR pattern: (nil,false)=global; (ids,true)=restrict;
// ([],true)=no orgs in scope → caller short-circuits to an empty page.
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
