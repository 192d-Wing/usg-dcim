// Package sites holds the HTTP handlers for the /api/v1/sites resource.
// Read-only for the vertical slice: list + get. Create/Patch still
// served by the Python otter until phase 2.
package sites

import (
	"context"
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

// Querier is the slice of sqlc-generated *Queries this package uses,
// exposed as an interface so handlers can be tested against an in-memory
// fake without spinning up Postgres. *dbq.Queries satisfies this.
type Querier interface {
	ListSites(ctx context.Context, arg dbq.ListSitesParams) ([]dbq.Site, error)
	CountSites(ctx context.Context, arg dbq.CountSitesParams) (int64, error)
	GetSite(ctx context.Context, id uuid.UUID) (dbq.Site, error)
	CreateSite(ctx context.Context, arg dbq.CreateSiteParams) (dbq.Site, error)
	UpdateSite(ctx context.Context, arg dbq.UpdateSiteParams) (dbq.Site, error)

	// PR 54: ABAC SiteMatches expansion. Region- and site-group-scoped
	// principals need these to resolve whether a target site is reachable.
	GetSiteRegionID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetSiteOrganizationID(ctx context.Context, id uuid.UUID) (*uuid.UUID, error)
	ListSiteGroupIDsForSite(ctx context.Context, siteID uuid.UUID) ([]uuid.UUID, error)

	// PR 62: scope-filtered LISTs. Expands the caller's region + group +
	// direct-site scope dimensions to a concrete site_id set in a single
	// DB call. Powers auth.ScopedSiteFilter.
	ListSiteIDsForExpansion(ctx context.Context, arg dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error)
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
	limit, offset := httpx.PageBounds(q)

	// PR 62 — resolve the caller's site scope. Scoped principals see
	// only sites reachable through their region / site_group / direct-
	// site dimensions. A scoped principal with no site-reachable
	// dimensions (enclave-only, fabric-only, etc.) gets an empty page.
	p, _ := auth.From(r.Context())
	scopeSiteIds, scoped, err := auth.ScopedSiteFilter(r.Context(), h.Q, p, "inventory:sites:read")
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if scoped && len(scopeSiteIds) == 0 {
		httpx.JSON(w, http.StatusOK, listResponse{Items: nil, Total: 0, Limit: limit, Offset: offset})
		return
	}

	params := dbq.ListSitesParams{
		Limit:          limit,
		Offset:         offset,
		Majcom:         strPtr(q.Get("majcom")),
		Enclave:        strPtr(q.Get("enclave")),
		LifecycleState: strPtr(q.Get("lifecycle_state")),
		SiteIds:        scopeSiteIds,
	}
	if rid := q.Get("region_id"); rid != "" {
		u, err := uuid.Parse(rid)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "region_id is not a uuid")
			return
		}
		params.RegionID = &u
	}
	if oid := q.Get("organization_id"); oid != "" {
		u, err := uuid.Parse(oid)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "organization_id is not a uuid")
			return
		}
		params.OrganizationID = &u
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
		OrganizationID: params.OrganizationID,
		LifecycleState: params.LifecycleState,
		SiteIds:        scopeSiteIds,
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
