// Package regions holds the HTTP handlers for /api/v1/regions.
// Read-only for the Phase-1 vertical slice: list + get. Create/Patch
// still served by the Python otter until Phase 2.
package regions

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// Querier is the slice of sqlc-generated *Queries this package uses;
// declared as an interface so tests don't need a live Postgres.
type Querier interface {
	ListRegions(ctx context.Context, arg dbq.ListRegionsParams) ([]dbq.Region, error)
	CountRegions(ctx context.Context, arg dbq.CountRegionsParams) (int64, error)
	GetRegion(ctx context.Context, id uuid.UUID) (dbq.Region, error)
}

type Handler struct {
	Q Querier
}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/regions", h.list)
	r.Get("/regions/{id}", h.get)
}

// listResponse mirrors the FastAPI Page[RegionOut] shape so finch
// doesn't have to branch on which backend served the request.
type listResponse struct {
	Items  []dbq.Region `json:"items"`
	Total  int64        `json:"total"`
	Limit  int32        `json:"limit"`
	Offset int32        `json:"offset"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(q.Get("limit"), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)

	// NOTE: ABAC region-id filtering is NOT applied here yet — the
	// Python list_regions runs scope_filtered_site_ids() and folds the
	// site set into regions via a subquery. Doing that requires the
	// real auth middleware (Phase 3); the stub middleware in
	// internal/auth grants `*` so for now every authenticated request
	// sees every region. That matches the stub's documented behavior.
	params := dbq.ListRegionsParams{Limit: limit, Offset: offset}

	items, err := h.Q.ListRegions(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountRegions(r.Context(), dbq.CountRegionsParams{})
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
	region, err := h.Q.GetRegion(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "region not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, region)
}

// Shared with sites — small enough to duplicate; promote to httpx if a
// third resource needs it.
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
