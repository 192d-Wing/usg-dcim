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
	GetBuilding(ctx context.Context, id uuid.UUID) (dbq.Building, error)
	GetRoom(ctx context.Context, id uuid.UUID) (dbq.Room, error)
	GetRow(ctx context.Context, id uuid.UUID) (dbq.Row, error)
	UpdateBuilding(ctx context.Context, arg dbq.UpdateBuildingParams) (dbq.Building, error)
	UpdateRoom(ctx context.Context, arg dbq.UpdateRoomParams) (dbq.Room, error)
	UpdateRow(ctx context.Context, arg dbq.UpdateRowParams) (dbq.Row, error)
	DeleteBuilding(ctx context.Context, id uuid.UUID) error
	DeleteRoom(ctx context.Context, id uuid.UUID) error
	DeleteRow(ctx context.Context, id uuid.UUID) error
	// 2- and 3-hop site walkers for ABAC on rooms / rows.
	SiteIDForRoom(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	SiteIDForRow(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	// SiteScope methods so PATCH/DELETE can EnforceSiteScope on the
	// resolved site_id.
	GetSiteRegionID(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	GetSiteOrganizationID(ctx context.Context, id uuid.UUID) (*uuid.UUID, error)
	ListSiteGroupIDsForSite(ctx context.Context, siteID uuid.UUID) ([]uuid.UUID, error)

	// PR 63: scope-filtered LIST. Expands the caller's region + group +
	// direct-site scope dimensions to a concrete site_id set.
	ListSiteIDsForExpansion(ctx context.Context, arg dbq.ListSiteIDsForExpansionParams) ([]uuid.UUID, error)
}

type Handler struct {
	Q     Querier
	Audit audit.Recorder
}

func (h *Handler) Mount(r chi.Router) {
	// Read paths gated by the matching :read capability — see
	// sites/Mount for why ScopedSiteFilter alone doesn't keep cap-less
	// principals out.
	r.With(auth.RequireCapability(capBuildingsRead)).Get("/buildings", h.listBuildings)
	r.With(auth.RequireCapability(capRoomsRead)).Get("/rooms", h.listRooms)
	r.With(auth.RequireCapability(capRowsRead)).Get("/rows", h.listRows)
	r.With(auth.RequireCapability("inventory:buildings:create")).Post("/buildings", h.createBuilding)
	r.With(auth.RequireCapability("inventory:rooms:create")).Post("/rooms", h.createRoom)
	r.With(auth.RequireCapability("inventory:rows:create")).Post("/rows", h.createRow)
	r.With(auth.RequireCapability(capBuildingsUpdate)).Patch("/buildings/{id}", h.updateBuilding)
	r.With(auth.RequireCapability(capRoomsUpdate)).Patch("/rooms/{id}", h.updateRoom)
	r.With(auth.RequireCapability(capRowsUpdate)).Patch("/rows/{id}", h.updateRow)
	r.With(auth.RequireCapability(capBuildingsDelete)).Delete("/buildings/{id}", h.deleteBuilding)
	r.With(auth.RequireCapability(capRoomsDelete)).Delete("/rooms/{id}", h.deleteRoom)
	r.With(auth.RequireCapability(capRowsDelete)).Delete("/rows/{id}", h.deleteRow)
}

const (
	capBuildingsRead   = "inventory:buildings:read"
	capRoomsRead       = "inventory:rooms:read"
	capRowsRead        = "inventory:rows:read"
	capBuildingsUpdate = "inventory:buildings:update"
	capRoomsUpdate     = "inventory:rooms:update"
	capRowsUpdate      = "inventory:rows:update"
	capBuildingsDelete = "inventory:buildings:delete"
	capRoomsDelete     = "inventory:rooms:delete"
	capRowsDelete      = "inventory:rows:delete"
)

type buildingsPage = httpx.Page[dbq.Building]

type roomsPage = httpx.Page[dbq.Room]

type rowsPage = httpx.Page[dbq.Row]

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
		httpx.JSON(w, http.StatusOK, httpx.EmptyPage[dbq.Building](limit, offset))
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
		httpx.JSON(w, http.StatusOK, httpx.EmptyPage[dbq.Room](limit, offset))
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
		httpx.JSON(w, http.StatusOK, httpx.EmptyPage[dbq.Row](limit, offset))
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
