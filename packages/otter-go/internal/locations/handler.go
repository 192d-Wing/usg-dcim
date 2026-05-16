// Package locations holds GET handlers for the three small geographic
// sub-resources under sites: buildings, rooms, rows. Bundled into one
// package because each is a single paginated list (no GET-by-id in
// the Python otter) and the shapes are nearly identical. Splitting
// per-resource adds boilerplate without buying anything.
//
// Create/Patch endpoints still served by Python otter until Phase 2.
package locations

import (
	"context"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type Querier interface {
	ListBuildings(ctx context.Context, arg dbq.ListBuildingsParams) ([]dbq.Building, error)
	CountBuildings(ctx context.Context, arg dbq.CountBuildingsParams) (int64, error)
	ListRooms(ctx context.Context, arg dbq.ListRoomsParams) ([]dbq.Room, error)
	CountRooms(ctx context.Context, arg dbq.CountRoomsParams) (int64, error)
	ListRows(ctx context.Context, arg dbq.ListRowsParams) ([]dbq.Row, error)
	CountRows(ctx context.Context, arg dbq.CountRowsParams) (int64, error)
}

type Handler struct {
	Q Querier
}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/buildings", h.listBuildings)
	r.Get("/rooms", h.listRooms)
	r.Get("/rows", h.listRows)
}

type buildingsPage struct {
	Items  []dbq.Building `json:"items"`
	Total  int64          `json:"total"`
	Limit  int32          `json:"limit"`
	Offset int32          `json:"offset"`
}

type roomsPage struct {
	Items  []dbq.Room `json:"items"`
	Total  int64      `json:"total"`
	Limit  int32      `json:"limit"`
	Offset int32      `json:"offset"`
}

type rowsPage struct {
	Items  []dbq.Row `json:"items"`
	Total  int64     `json:"total"`
	Limit  int32     `json:"limit"`
	Offset int32     `json:"offset"`
}

func (h *Handler) listBuildings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(q.Get("limit"), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	params := dbq.ListBuildingsParams{Limit: limit, Offset: offset}
	if v := q.Get("site_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "site_id is not a uuid")
			return
		}
		params.SiteID = &id
	}
	items, err := h.Q.ListBuildings(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountBuildings(r.Context(), dbq.CountBuildingsParams{SiteID: params.SiteID})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, buildingsPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (h *Handler) listRooms(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(q.Get("limit"), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	params := dbq.ListRoomsParams{Limit: limit, Offset: offset}
	if v := q.Get("building_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "building_id is not a uuid")
			return
		}
		params.BuildingID = &id
	}
	items, err := h.Q.ListRooms(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountRooms(r.Context(), dbq.CountRoomsParams{BuildingID: params.BuildingID})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, roomsPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (h *Handler) listRows(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseInt32(q.Get("limit"), 50, 1, 500)
	offset := parseInt32(q.Get("offset"), 0, 0, 1_000_000)
	params := dbq.ListRowsParams{Limit: limit, Offset: offset}
	if v := q.Get("room_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "room_id is not a uuid")
			return
		}
		params.RoomID = &id
	}
	items, err := h.Q.ListRows(r.Context(), params)
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	total, err := h.Q.CountRows(r.Context(), dbq.CountRowsParams{RoomID: params.RoomID})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, rowsPage{Items: items, Total: total, Limit: limit, Offset: offset})
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
