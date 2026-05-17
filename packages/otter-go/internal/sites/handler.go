// Package sites holds the HTTP handlers for the /api/v1/sites resource.
// Read-only for the vertical slice: list + get. Create/Patch still
// served by the Python otter until phase 2.
package sites

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// Querier is the slice of sqlc-generated *Queries this package uses,
// exposed as an interface so handlers can be tested against an in-memory
// fake without spinning up Postgres. *dbq.Queries satisfies this.
type Querier interface {
	ListSites(ctx context.Context, arg dbq.ListSitesParams) ([]dbq.Site, error)
	CountSites(ctx context.Context, arg dbq.CountSitesParams) (int64, error)
	GetSite(ctx context.Context, id uuid.UUID) (dbq.Site, error)
	CreateSite(ctx context.Context, arg dbq.CreateSiteParams) (dbq.Site, error)
	UpdateSite(ctx context.Context, arg dbq.UpdateSiteParams) (dbq.Site, error)
}

type Handler struct {
	Q     Querier
	Audit audit.Recorder
}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/sites", h.list)
	r.Get("/sites/{id}", h.get)
	r.With(auth.RequireCapability("inventory:sites:create")).Post("/sites", h.create)
	r.With(auth.RequireCapability("inventory:sites:update")).Patch("/sites/{id}", h.update)
}

// listResponse mirrors the FastAPI Page[SiteOut] shape so finch
// doesn't have to branch on which backend served the request.
type listResponse struct {
	Items  []dbq.Site `json:"items"`
	Total  int64      `json:"total"`
	Limit  int32      `json:"limit"`
	Offset int32      `json:"offset"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(q.Get("limit"), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)

	params := dbq.ListSitesParams{
		Limit:          limit,
		Offset:         offset,
		Majcom:         strPtr(q.Get("majcom")),
		Enclave:        strPtr(q.Get("enclave")),
		Organization:   strPtr(q.Get("organization")),
		LifecycleState: strPtr(q.Get("lifecycle_state")),
	}
	if rid := q.Get("region_id"); rid != "" {
		u, err := uuid.Parse(rid)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "region_id is not a uuid")
			return
		}
		params.RegionID = &u
	}

	items, err := h.Q.ListSites(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountSites(r.Context(), dbq.CountSitesParams{
		RegionID:       params.RegionID,
		Majcom:         params.Majcom,
		Enclave:        params.Enclave,
		Organization:   params.Organization,
		LifecycleState: params.LifecycleState,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse{
		Items: items, Total: total, Limit: limit, Offset: offset,
	})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	site, err := h.Q.GetSite(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "site not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, site)
}

// strPtr returns nil for empty strings so sqlc.narg() picks up the
// IS NULL branch in the query.
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func parseInt32(s string, def, lo, hi int32) int32 {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	v := int32(n)
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
