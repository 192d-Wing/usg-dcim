// Package racks holds GET handlers for /api/v1/racks. List + get; the
// Python POST/PATCH still serve writes until Phase 2.
package racks

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

type Querier interface {
	ListRacks(ctx context.Context, arg dbq.ListRacksParams) ([]dbq.Rack, error)
	CountRacks(ctx context.Context, arg dbq.CountRacksParams) (int64, error)
	GetRack(ctx context.Context, id uuid.UUID) (dbq.Rack, error)
	CreateRack(ctx context.Context, arg dbq.CreateRackParams) (dbq.Rack, error)
	UpdateRack(ctx context.Context, arg dbq.UpdateRackParams) (dbq.Rack, error)
	GetRackAssetsForShrinkCheck(ctx context.Context, rackID uuid.UUID) ([]dbq.RackPlacedAsset, error)

	// PR 54: ABAC SiteMatches expansion.
	GetSiteRegionID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	ListSiteGroupIDsForSite(ctx context.Context, siteID uuid.UUID) ([]uuid.UUID, error)
}

type Handler struct {
	Q     Querier
	Audit audit.Recorder
}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/racks", h.list)
	r.Get("/racks/{id}", h.get)
	r.With(auth.RequireCapability("inventory:racks:create")).Post("/racks", h.create)
	r.With(auth.RequireCapability("inventory:racks:update")).Patch("/racks/{id}", h.update)
}

type listResponse struct {
	Items  []dbq.Rack `json:"items"`
	Total  int64      `json:"total"`
	Limit  int32      `json:"limit"`
	Offset int32      `json:"offset"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(q.Get("limit"), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	params := dbq.ListRacksParams{Limit: limit, Offset: offset}
	if v := q.Get("site_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "site_id is not a uuid")
			return
		}
		params.SiteID = &id
	}
	if v := q.Get("row_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "row_id is not a uuid")
			return
		}
		params.RowID = &id
	}
	items, err := h.Q.ListRacks(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountRacks(r.Context(), dbq.CountRacksParams{SiteID: params.SiteID, RowID: params.RowID})
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
	rack, err := h.Q.GetRack(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "rack not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, rack)
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
