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

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/audit"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

type Querier interface {
	ListBuildings(ctx context.Context, arg dbq.ListBuildingsParams) ([]dbq.Building, error)
	CountBuildings(ctx context.Context, arg dbq.CountBuildingsParams) (int64, error)
	ListRooms(ctx context.Context, arg dbq.ListRoomsParams) ([]dbq.Room, error)
	CountRooms(ctx context.Context, arg dbq.CountRoomsParams) (int64, error)
	ListRows(ctx context.Context, arg dbq.ListRowsParams) ([]dbq.Row, error)
	CountRows(ctx context.Context, arg dbq.CountRowsParams) (int64, error)
	CreateBuilding(ctx context.Context, arg dbq.CreateBuildingParams) (dbq.Building, error)
	CreateRoom(ctx context.Context, arg dbq.CreateRoomParams) (dbq.Room, error)
	CreateRow(ctx context.Context, arg dbq.CreateRowParams) (dbq.Row, error)

	// PR 63: scope-filtered LIST. Expands the caller's region + group +
	// direct-site scope dimensions to a concrete site_id set.
	ListSiteIDsForExpansion(ctx context.Context, arg dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error)
}

type Handler struct {
	Q     Querier
	Audit audit.Recorder
}

func (h *Handler) Mount(r chi.Router) {
	r.Get("/buildings", h.listBuildings)
	r.Get("/rooms", h.listRooms)
	r.Get("/rows", h.listRows)
	r.With(auth.RequireCapability("inventory:buildings:create")).Post("/buildings", h.createBuilding)
	r.With(auth.RequireCapability("inventory:rooms:create")).Post("/rooms", h.createRoom)
	r.With(auth.RequireCapability("inventory:rows:create")).Post("/rows", h.createRow)
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
	limit, offset := httpx.PageBounds(q)
	p, _ := auth.From(r.Context())
	scopeSiteIds, scoped, err := auth.ScopedSiteFilter(r.Context(), h.Q, p, "inventory:buildings:read")
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if scoped && len(scopeSiteIds) == 0 {
		httpx.JSON(w, http.StatusOK, buildingsPage{Items: nil, Total: 0, Limit: limit, Offset: offset})
		return
	}
	params := dbq.ListBuildingsParams{Limit: limit, Offset: offset, SiteIds: scopeSiteIds}
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
	total, err := h.Q.CountBuildings(r.Context(), dbq.CountBuildingsParams{SiteID: params.SiteID, SiteIds: scopeSiteIds})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, buildingsPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (h *Handler) listRooms(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	// PR 96 — 2-hop ABAC: room → building → site. ScopedSiteFilter
	// expands the principal's site dims into a concrete site_id set;
	// the SQL JOIN against buildings filters rooms by parent site.
	p, _ := auth.From(r.Context())
	scopeSiteIds, scoped, err := auth.ScopedSiteFilter(r.Context(), h.Q, p, "inventory:rooms:read")
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if scoped && len(scopeSiteIds) == 0 {
		httpx.JSON(w, http.StatusOK, roomsPage{Items: nil, Total: 0, Limit: limit, Offset: offset})
		return
	}
	params := dbq.ListRoomsParams{Limit: limit, Offset: offset, SiteIds: scopeSiteIds}
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
	total, err := h.Q.CountRooms(r.Context(), dbq.CountRoomsParams{
		BuildingID: params.BuildingID, SiteIds: scopeSiteIds,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, roomsPage{Items: items, Total: total, Limit: limit, Offset: offset})
}

func (h *Handler) listRows(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := httpx.PageBounds(q)
	// PR 96 — 3-hop ABAC: row → room → building → site.
	p, _ := auth.From(r.Context())
	scopeSiteIds, scoped, err := auth.ScopedSiteFilter(r.Context(), h.Q, p, "inventory:rows:read")
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	if scoped && len(scopeSiteIds) == 0 {
		httpx.JSON(w, http.StatusOK, rowsPage{Items: nil, Total: 0, Limit: limit, Offset: offset})
		return
	}
	params := dbq.ListRowsParams{Limit: limit, Offset: offset, SiteIds: scopeSiteIds}
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
	total, err := h.Q.CountRows(r.Context(), dbq.CountRowsParams{
		RoomID: params.RoomID, SiteIds: scopeSiteIds,
	})
	if err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, rowsPage{Items: items, Total: total, Limit: limit, Offset: offset})
}
