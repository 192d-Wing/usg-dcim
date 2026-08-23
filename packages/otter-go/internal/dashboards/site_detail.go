package dashboards

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/capacity"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// SiteDetailQuerier is the dbq slice /sites/{site_id} needs. Distinct
// from the other dashboard Queriers so each test file stays narrow.
type SiteDetailQuerier interface {
	GetSite(ctx context.Context, id uuid.UUID) (dbq.Site, error)
	GetRegion(ctx context.Context, id uuid.UUID) (dbq.Region, error)
	ListBuildingsBySite(ctx context.Context, siteID uuid.UUID) ([]dbq.ListBuildingsBySiteRow, error)
	ListRoomsByBuildingIDs(ctx context.Context, ids []uuid.UUID) ([]dbq.ListRoomsByBuildingIDsRow, error)
	ListRowsByRoomIDs(ctx context.Context, ids []uuid.UUID) ([]dbq.ListRowsByRoomIDsRow, error)
	ListRacksBySite(ctx context.Context, siteID uuid.UUID) ([]dbq.Rack, error)
	ListAssetsBySite(ctx context.Context, siteID uuid.UUID) ([]dbq.Asset, error)
	ListSiteAlertsBySeverity(ctx context.Context, siteID uuid.UUID) ([]dbq.ListSiteAlertsBySeverityRow, error)
	ListSiteCollectors(ctx context.Context, siteID uuid.UUID) ([]dbq.ListSiteCollectorsRow, error)
	capacity.Querier
}

// siteDetailResponse mirrors Python's api/dashboards.py::site_detail
// return shape exactly.
type siteDetailResponse struct {
	Site        siteIdentity   `json:"site"`
	Region      *siteRegion    `json:"region"`
	KPIs        siteKpis       `json:"kpis"`
	Capacity    siteCapacity   `json:"capacity"`
	Hierarchy   []buildingNode `json:"hierarchy"`
	OrphanRacks []rackNode     `json:"orphan_racks"`
}

type siteIdentity struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Code           string  `json:"code"`
	Address        *string `json:"address"`
	Timezone       *string `json:"timezone"`
	RegionID       string  `json:"region_id"`
	Majcom         *string `json:"majcom"`
	OrganizationID *string `json:"organization_id"`
	MissionOwner   *string `json:"mission_owner"`
	Enclave        *string `json:"enclave"`
	Classification *string `json:"classification"`
	LifecycleState string  `json:"lifecycle_state"`
}

type siteRegion struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Code string `json:"code"`
}

type siteKpis struct {
	Buildings    int              `json:"buildings"`
	Rooms        int              `json:"rooms"`
	Rows         int              `json:"rows"`
	Racks        int              `json:"racks"`
	Assets       map[string]int64 `json:"assets"`
	AlertsFiring map[string]int64 `json:"alerts_firing"`
	Collectors   map[string]int64 `json:"collectors"`
}

// siteCapacity is the summed rollup across the site's racks. Different
// shape from capacity.RackCapacity — kw_max_sum vs kw_max, plus
// racks_total / racks_with_kw_rating cells.
type siteCapacity struct {
	UUsed             int32    `json:"u_used"`
	UTotal            int32    `json:"u_total"`
	UFree             int32    `json:"u_free"`
	UPct              float64  `json:"u_pct"`
	KwMaxSum          *float64 `json:"kw_max_sum"`
	KwCurrent         *float64 `json:"kw_current"`
	KwPct             *float64 `json:"kw_pct"`
	RacksTotal        int      `json:"racks_total"`
	RacksWithKwRating int      `json:"racks_with_kw_rating"`
}

type buildingNode struct {
	ID    string     `json:"id"`
	Name  string     `json:"name"`
	Code  string     `json:"code"`
	Rooms []roomNode `json:"rooms"`
}

type roomNode struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Code     string    `json:"code"`
	DesignKw *float64  `json:"design_kw"`
	Rows     []rowNode `json:"rows"`
}

type rowNode struct {
	ID    string     `json:"id"`
	Name  string     `json:"name"`
	Code  string     `json:"code"`
	Racks []rackNode `json:"racks"`
}

type rackNode struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Code       string   `json:"code"`
	UHeight    int32    `json:"u_height"`
	UUsed      int32    `json:"u_used"`
	UPct       float64  `json:"u_pct"`
	KwMax      *float64 `json:"kw_max"`
	KwCurrent  *float64 `json:"kw_current"`
	AssetCount int      `json:"asset_count"`
	// Floor-plan tile placement (null grid_x/grid_y = unplaced).
	GridX        *int32 `json:"grid_x"`
	GridY        *int32 `json:"grid_y"`
	GridRotation int16  `json:"grid_rotation"`
}

func (h *Handler) siteDetail(w http.ResponseWriter, r *http.Request) {
	q, ok := h.Q.(SiteDetailQuerier)
	if !ok {
		httpx.Error(w, http.StatusInternalServerError, "site-detail requires full Querier")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "site_id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "site_id is not a uuid")
		return
	}
	ctx := r.Context()

	site, err := q.GetSite(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.JSON(w, http.StatusOK, notFoundResponse{Error: "not_found"})
			return
		}
		mapErr(w, err)
		return
	}

	region, found, err := loadOptionalRegion(ctx, q, site.RegionID)
	if err != nil {
		mapErr(w, err)
		return
	}
	var regionOut *siteRegion
	if found {
		regionOut = &siteRegion{ID: region.ID.String(), Name: region.Name, Code: region.Code}
	}

	topology, err := loadSiteTopology(ctx, q, id)
	if err != nil {
		mapErr(w, err)
		return
	}
	assetsByRack := groupAssetsByRack(topology.assets)
	rackCaps, err := capacity.ComputeManyRackCapacity(ctx, q, topology.racks, assetsByRack)
	if err != nil {
		mapErr(w, err)
		return
	}

	alertsByRow, err := q.ListSiteAlertsBySeverity(ctx, id)
	if err != nil {
		mapErr(w, err)
		return
	}
	collectorRows, err := q.ListSiteCollectors(ctx, id)
	if err != nil {
		mapErr(w, err)
		return
	}
	staleBefore := time.Now().UTC().Add(-time.Duration(h.CollectorStaleSeconds) * time.Second)

	hierarchy, orphans := buildHierarchy(topology, rackCaps, assetsByRack)
	httpx.JSON(w, http.StatusOK, siteDetailResponse{
		Site:   buildSiteIdentity(site),
		Region: regionOut,
		KPIs: siteKpis{
			Buildings:    len(topology.buildings),
			Rooms:        len(topology.rooms),
			Rows:         len(topology.rows),
			Racks:        len(topology.racks),
			Assets:       assetsByLifecycle(topology.assets),
			AlertsFiring: alertsKpiFrom(alertsByRow),
			Collectors:   collectorsKpiFrom(collectorRows, staleBefore),
		},
		Capacity:    rollupSiteCapacity(topology.racks, rackCaps),
		Hierarchy:   hierarchy,
		OrphanRacks: orphans,
	})
}

func loadOptionalRegion(ctx context.Context, q SiteDetailQuerier, id uuid.UUID) (dbq.Region, bool, error) {
	region, err := q.GetRegion(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return dbq.Region{}, false, nil
		}
		return dbq.Region{}, false, err
	}
	return region, true, nil
}

type siteTopology struct {
	buildings []dbq.ListBuildingsBySiteRow
	rooms     []dbq.ListRoomsByBuildingIDsRow
	rows      []dbq.ListRowsByRoomIDsRow
	racks     []dbq.Rack
	assets    []dbq.Asset
}

func loadSiteTopology(ctx context.Context, q SiteDetailQuerier, siteID uuid.UUID) (siteTopology, error) {
	var t siteTopology
	var err error
	t.buildings, err = q.ListBuildingsBySite(ctx, siteID)
	if err != nil {
		return t, err
	}
	t.rooms, err = listRoomsForBuildings(ctx, q, t.buildings)
	if err != nil {
		return t, err
	}
	t.rows, err = listRowsForRooms(ctx, q, t.rooms)
	if err != nil {
		return t, err
	}
	t.racks, err = q.ListRacksBySite(ctx, siteID)
	if err != nil {
		return t, err
	}
	t.assets, err = q.ListAssetsBySite(ctx, siteID)
	if err != nil {
		return t, err
	}
	return t, nil
}

func listRoomsForBuildings(ctx context.Context, q SiteDetailQuerier, buildings []dbq.ListBuildingsBySiteRow) ([]dbq.ListRoomsByBuildingIDsRow, error) {
	if len(buildings) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(buildings))
	for i, b := range buildings {
		ids[i] = b.ID
	}
	return q.ListRoomsByBuildingIDs(ctx, ids)
}

func listRowsForRooms(ctx context.Context, q SiteDetailQuerier, rooms []dbq.ListRoomsByBuildingIDsRow) ([]dbq.ListRowsByRoomIDsRow, error) {
	if len(rooms) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, len(rooms))
	for i, r := range rooms {
		ids[i] = r.ID
	}
	return q.ListRowsByRoomIDs(ctx, ids)
}

func groupAssetsByRack(assets []dbq.Asset) map[uuid.UUID][]dbq.Asset {
	out := map[uuid.UUID][]dbq.Asset{}
	for _, a := range assets {
		if a.RackID == nil {
			continue
		}
		out[*a.RackID] = append(out[*a.RackID], a)
	}
	return out
}

func assetsByLifecycle(assets []dbq.Asset) map[string]int64 {
	out := map[string]int64{
		"planned":        0,
		"staged":         0,
		"active":         0,
		"maintenance":    0,
		"decommissioned": 0,
		"retired":        0,
	}
	for _, a := range assets {
		out[a.LifecycleState]++
	}
	var total int64
	for _, n := range out {
		total += n
	}
	out["total"] = total
	return out
}

func alertsKpiFrom(rows []dbq.ListSiteAlertsBySeverityRow) map[string]int64 {
	out := map[string]int64{
		"info":     0,
		"warning":  0,
		"minor":    0,
		"major":    0,
		"critical": 0,
	}
	for _, r := range rows {
		out[r.Severity] = r.N
	}
	var total int64
	for _, n := range out {
		total += n
	}
	out["total"] = total
	return out
}

// collectorsKpiFrom mirrors Python's _site_collectors_kpi
// (api/dashboards.py: deleted). Python increments out["stale"] both
// via the per-status loop AND via the freshness check, which double-
// counts a collector whose status is literally "stale" and is also
// flagged stale by the freshness threshold. Match that semantically
// even though it's likely a Python bug — wire parity is the goal.
func collectorsKpiFrom(rows []dbq.ListSiteCollectorsRow, staleBefore time.Time) map[string]int64 {
	out := map[string]int64{
		"pending":        0,
		"healthy":        0,
		"degraded":       0,
		"stale":          0,
		"unreachable":    0,
		"decommissioned": 0,
	}
	var total int64
	for _, r := range rows {
		total++
		out[r.Status]++
		if r.Enabled && (r.LastSeenAt == nil || r.LastSeenAt.Before(staleBefore)) {
			out["stale"]++
		}
	}
	out["total"] = total
	return out
}

func rollupSiteCapacity(racks []dbq.Rack, caps map[uuid.UUID]capacity.RackCapacity) siteCapacity {
	var uUsed, uTotal int32
	var kwMaxSum, kwCurrentSum float64
	var kwMaxKnown, kwCurrentKnown bool
	racksWithKw := 0
	for _, r := range racks {
		cap := caps[r.ID]
		uUsed += cap.UUsed
		uTotal += cap.UTotal
		if cap.KwMax != nil {
			kwMaxSum += *cap.KwMax
			kwMaxKnown = true
		}
		if cap.KwCurrent != nil {
			kwCurrentSum += *cap.KwCurrent
			kwCurrentKnown = true
		}
		if r.MaxKw != nil {
			racksWithKw++
		}
	}
	uFree := uTotal - uUsed
	if uFree < 0 {
		uFree = 0
	}
	uPct := 0.0
	if uTotal > 0 {
		uPct = round1(100.0 * float64(uUsed) / float64(uTotal))
	}
	out := siteCapacity{
		UUsed:             uUsed,
		UTotal:            uTotal,
		UFree:             uFree,
		UPct:              uPct,
		RacksTotal:        len(racks),
		RacksWithKwRating: racksWithKw,
	}
	if kwMaxKnown {
		v := round2(kwMaxSum)
		out.KwMaxSum = &v
	}
	if kwCurrentKnown {
		v := round3(kwCurrentSum)
		out.KwCurrent = &v
	}
	if kwMaxKnown && kwCurrentKnown && kwMaxSum > 0 {
		v := round1(100.0 * kwCurrentSum / kwMaxSum)
		out.KwPct = &v
	}
	return out
}

func buildHierarchy(
	t siteTopology, caps map[uuid.UUID]capacity.RackCapacity,
	assetsByRack map[uuid.UUID][]dbq.Asset,
) ([]buildingNode, []rackNode) {
	roomsByBuilding := groupRoomsByBuilding(t.rooms)
	rowsByRoom := groupRowsByRoom(t.rows)
	racksByRow := groupRacksByRow(t.racks)

	placed := map[uuid.UUID]struct{}{}
	hierarchy := make([]buildingNode, 0, len(t.buildings))
	for _, b := range t.buildings {
		hierarchy = append(hierarchy, buildingNode{
			ID:    b.ID.String(),
			Name:  b.Name,
			Code:  b.Code,
			Rooms: buildRoomNodes(roomsByBuilding[b.ID], rowsByRoom, racksByRow, caps, assetsByRack, placed),
		})
	}
	orphans := make([]rackNode, 0)
	for _, r := range t.racks {
		if _, ok := placed[r.ID]; !ok {
			orphans = append(orphans, makeRackNode(r, caps, assetsByRack))
		}
	}
	return hierarchy, orphans
}

func buildRoomNodes(
	rooms []dbq.ListRoomsByBuildingIDsRow,
	rowsByRoom map[uuid.UUID][]dbq.ListRowsByRoomIDsRow,
	racksByRow map[uuid.UUID][]dbq.Rack,
	caps map[uuid.UUID]capacity.RackCapacity,
	assetsByRack map[uuid.UUID][]dbq.Asset,
	placed map[uuid.UUID]struct{},
) []roomNode {
	out := make([]roomNode, 0, len(rooms))
	for _, room := range rooms {
		out = append(out, roomNode{
			ID:       room.ID.String(),
			Name:     room.Name,
			Code:     room.Code,
			DesignKw: parseFloat(room.DesignKw),
			Rows:     buildRowNodes(rowsByRoom[room.ID], racksByRow, caps, assetsByRack, placed),
		})
	}
	return out
}

func buildRowNodes(
	rows []dbq.ListRowsByRoomIDsRow,
	racksByRow map[uuid.UUID][]dbq.Rack,
	caps map[uuid.UUID]capacity.RackCapacity,
	assetsByRack map[uuid.UUID][]dbq.Asset,
	placed map[uuid.UUID]struct{},
) []rowNode {
	out := make([]rowNode, 0, len(rows))
	for _, r := range rows {
		racks := racksByRow[r.ID]
		rackOut := make([]rackNode, 0, len(racks))
		for _, rk := range racks {
			placed[rk.ID] = struct{}{}
			rackOut = append(rackOut, makeRackNode(rk, caps, assetsByRack))
		}
		out = append(out, rowNode{
			ID: r.ID.String(), Name: r.Name, Code: r.Code, Racks: rackOut,
		})
	}
	return out
}

func makeRackNode(rk dbq.Rack, caps map[uuid.UUID]capacity.RackCapacity, assetsByRack map[uuid.UUID][]dbq.Asset) rackNode {
	cap := caps[rk.ID]
	return rackNode{
		ID:           rk.ID.String(),
		Name:         rk.Name,
		Code:         rk.Code,
		UHeight:      rk.UHeight,
		UUsed:        cap.UUsed,
		UPct:         cap.UPct,
		KwMax:        cap.KwMax,
		KwCurrent:    cap.KwCurrent,
		AssetCount:   len(assetsByRack[rk.ID]),
		GridX:        rk.GridX,
		GridY:        rk.GridY,
		GridRotation: rk.GridRotation,
	}
}

func groupRoomsByBuilding(rooms []dbq.ListRoomsByBuildingIDsRow) map[uuid.UUID][]dbq.ListRoomsByBuildingIDsRow {
	out := map[uuid.UUID][]dbq.ListRoomsByBuildingIDsRow{}
	for _, r := range rooms {
		out[r.BuildingID] = append(out[r.BuildingID], r)
	}
	return out
}

func groupRowsByRoom(rows []dbq.ListRowsByRoomIDsRow) map[uuid.UUID][]dbq.ListRowsByRoomIDsRow {
	out := map[uuid.UUID][]dbq.ListRowsByRoomIDsRow{}
	for _, r := range rows {
		out[r.RoomID] = append(out[r.RoomID], r)
	}
	return out
}

func groupRacksByRow(racks []dbq.Rack) map[uuid.UUID][]dbq.Rack {
	out := map[uuid.UUID][]dbq.Rack{}
	for _, r := range racks {
		out[r.RowID] = append(out[r.RowID], r)
	}
	return out
}

func buildSiteIdentity(site dbq.Site) siteIdentity {
	si := siteIdentity{
		ID:             site.ID.String(),
		Name:           site.Name,
		Code:           site.Code,
		Address:        site.Address,
		Timezone:       site.Timezone,
		RegionID:       site.RegionID.String(),
		Majcom:         site.Majcom,
		MissionOwner:   site.MissionOwner,
		Enclave:        site.Enclave,
		Classification: site.Classification,
		LifecycleState: site.LifecycleState,
	}
	if site.OrganizationID != nil {
		s := site.OrganizationID.String()
		si.OrganizationID = &s
	}
	return si
}

func parseFloat(s *string) *float64 {
	if s == nil {
		return nil
	}
	v, err := strconv.ParseFloat(*s, 64)
	if err != nil {
		return nil
	}
	return &v
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round2(v float64) float64 { return math.Round(v*100) / 100 }
func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
