// Package collectors holds GET handlers for the /api/v1/collectors resource.
// Write paths (enroll, heartbeat, config patch) still served by the Python
// otter — they need crypto + audit wiring that hasn't been ported yet.
package collectors

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type Querier interface {
	ListCollectors(ctx context.Context, arg dbq.ListCollectorsParams) ([]dbq.Collector, error)
	CountCollectors(ctx context.Context, arg dbq.CountCollectorsParams) (int64, error)
	GetCollector(ctx context.Context, id uuid.UUID) (dbq.Collector, error)
	SetCollectorConfigOverrides(ctx context.Context, arg dbq.SetCollectorConfigOverridesParams) (dbq.Collector, error)
	SetCollectorEnabled(ctx context.Context, arg dbq.SetCollectorEnabledParams) (dbq.Collector, error)
	DecommissionCollector(ctx context.Context, id uuid.UUID) (dbq.Collector, error)
}

type Handler struct{ Q Querier }

func (h *Handler) Mount(r chi.Router) {
	r.Route("/collectors", func(r chi.Router) {
		r.Get("/", h.list)
		r.Get("/{id}", h.get)
		r.With(auth.RequireCapability("collectors:collectors:update")).Patch("/{id}/config", h.patchConfig)
		r.With(auth.RequireCapability("collectors:collectors:update")).Patch("/{id}/enabled", h.patchEnabled)
		r.With(auth.RequireCapability("collectors:collectors:update")).Delete("/{id}", h.decommission)
	})
}

type listResponse struct {
	Items  []dbq.Collector `json:"items"`
	Total  int64           `json:"total"`
	Limit  int32           `json:"limit"`
	Offset int32           `json:"offset"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(pageSize(q), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)

	params := dbq.ListCollectorsParams{
		Limit:  limit,
		Offset: offset,
		Status: strPtr(q.Get("status")),
	}
	if v := q.Get("site_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "site_id is not a uuid")
			return
		}
		params.SiteID = &id
	}

	items, err := h.Q.ListCollectors(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountCollectors(r.Context(), dbq.CountCollectorsParams{
		SiteID: params.SiteID, Status: params.Status,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id is not a uuid")
		return
	}
	obj, err := h.Q.GetCollector(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "collector not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, obj)
}

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

func pageSize(q map[string][]string) string {
	if v := first(q, "limit"); v != "" {
		return v
	}
	return first(q, "page_size")
}

func first(q map[string][]string, key string) string {
	if vs := q[key]; len(vs) > 0 {
		return vs[0]
	}
	return ""
}
