package dashboards

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/capacity"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// BuildingDetailQuerier is the dbq slice /buildings/{building_id}
// needs. Distinct from the other dashboard Queriers so each test file
// stays narrow.
type BuildingDetailQuerier interface {
	GetBuilding(ctx context.Context, id uuid.UUID) (dbq.Building, error)
	GetSite(ctx context.Context, id uuid.UUID) (dbq.Site, error)
	ListRoomsByBuildingIDs(ctx context.Context, ids []uuid.UUID) ([]dbq.ListRoomsByBuildingIDsRow, error)
	ListRowsByRoomIDs(ctx context.Context, ids []uuid.UUID) ([]dbq.ListRowsByRoomIDsRow, error)
	ListRacksByRowIDs(ctx context.Context, rowIDs []uuid.UUID) ([]dbq.Rack, error)
	ListAssetsByRackIDs(ctx context.Context, rackIDs []uuid.UUID) ([]dbq.Asset, error)
	capacity.Querier
}

// buildingDetailResponse is the floor-view payload: one building's
// rooms (floors) with their row → rack tree and per-rack capacity.
// Go-canonical endpoint (no Python ancestor), so missing buildings
// return a real 404 rather than the 200-{"error":"not_found"} parity
// quirk the ported dashboards carry.
type buildingDetailResponse struct {
	Building buildingIdentity `json:"building"`
	Site     buildingSiteRef  `json:"site"`
	Capacity siteCapacity     `json:"capacity"`
	Floors   []floorNode      `json:"floors"`
}

type buildingIdentity struct {
	ID     string `json:"id"`
	SiteID string `json:"site_id"`
	Name   string `json:"name"`
	Code   string `json:"code"`
}

type buildingSiteRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Code string `json:"code"`
}

// floorNode is a room presented as a datacenter floor: its power /
// cooling budget plus the same capacity rollup shape the site detail
// uses, so the frontend meters render identically at every level.
type floorNode struct {
	ID                string       `json:"id"`
	Name              string       `json:"name"`
	Code              string       `json:"code"`
	FloorAreaSqft     *int32       `json:"floor_area_sqft"`
	DesignKw          *float64     `json:"design_kw"`
	DesignCoolingTons *float64     `json:"design_cooling_tons"`
	GridCols          *int32       `json:"grid_cols"`
	GridRows          *int32       `json:"grid_rows"`
	Capacity          siteCapacity `json:"capacity"`
	Rows              []rowNode    `json:"rows"`
}

func (h *Handler) buildingDetail(w http.ResponseWriter, r *http.Request) {
	q, ok := h.Q.(BuildingDetailQuerier)
	if !ok {
		httpx.Error(w, http.StatusInternalServerError, "building-detail requires full Querier")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "building_id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "building_id is not a uuid")
		return
	}
	ctx := r.Context()

	building, err := q.GetBuilding(ctx, id)
	if err != nil {
		mapErr(w, err)
		return
	}
	site, err := q.GetSite(ctx, building.SiteID)
	if err != nil {
		mapErr(w, err)
		return
	}

	rooms, err := q.ListRoomsByBuildingIDs(ctx, []uuid.UUID{id})
	if err != nil {
		mapErr(w, err)
		return
	}
	rows, err := listRowsForFloors(ctx, q, rooms)
	if err != nil {
		mapErr(w, err)
		return
	}
	racks, err := listRacksForRows(ctx, q, rows)
	if err != nil {
		mapErr(w, err)
		return
	}
	assets, err := listAssetsForRacks(ctx, q, racks)
	if err != nil {
		mapErr(w, err)
		return
	}
	assetsByRack := groupAssetsByRack(assets)
	rackCaps, err := capacity.ComputeManyRackCapacity(ctx, q, racks, assetsByRack)
	if err != nil {
		mapErr(w, err)
		return
	}

	rowsByRoom := groupRowsByRoom(rows)
	racksByRow := groupRacksByRow(racks)
	placed := map[uuid.UUID]struct{}{}
	floors := make([]floorNode, 0, len(rooms))
	for _, room := range rooms {
		roomRows := rowsByRoom[room.ID]
		floorRacks := make([]dbq.Rack, 0)
		for _, rw := range roomRows {
			floorRacks = append(floorRacks, racksByRow[rw.ID]...)
		}
		floors = append(floors, floorNode{
			ID:                room.ID.String(),
			Name:              room.Name,
			Code:              room.Code,
			FloorAreaSqft:     room.FloorAreaSqft,
			DesignKw:          parseFloat(room.DesignKw),
			DesignCoolingTons: parseFloat(room.DesignCoolingTons),
			GridCols:          room.GridCols,
			GridRows:          room.GridRows,
			Capacity:          rollupSiteCapacity(floorRacks, rackCaps),
			Rows:              buildRowNodes(roomRows, racksByRow, rackCaps, assetsByRack, placed),
		})
	}

	httpx.JSON(w, http.StatusOK, buildingDetailResponse{
		Building: buildingIdentity{
			ID:     building.ID.String(),
			SiteID: building.SiteID.String(),
			Name:   building.Name,
			Code:   building.Code,
		},
		Site:     buildingSiteRef{ID: site.ID.String(), Name: site.Name, Code: site.Code},
		Capacity: rollupSiteCapacity(racks, rackCaps),
		Floors:   floors,
	})
}

func listRowsForFloors(ctx context.Context, q BuildingDetailQuerier, rooms []dbq.ListRoomsByBuildingIDsRow) ([]dbq.ListRowsByRoomIDsRow, error) {
	if len(rooms) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(rooms))
	for i, r := range rooms {
		ids[i] = r.ID
	}
	return q.ListRowsByRoomIDs(ctx, ids)
}

func listRacksForRows(ctx context.Context, q BuildingDetailQuerier, rows []dbq.ListRowsByRoomIDsRow) ([]dbq.Rack, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return q.ListRacksByRowIDs(ctx, ids)
}

func listAssetsForRacks(ctx context.Context, q BuildingDetailQuerier, racks []dbq.Rack) ([]dbq.Asset, error) {
	if len(racks) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(racks))
	for i, r := range racks {
		ids[i] = r.ID
	}
	return q.ListAssetsByRackIDs(ctx, ids)
}
