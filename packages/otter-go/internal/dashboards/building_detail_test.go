package dashboards

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

// fakeBdQ stubs the BuildingDetailQuerier slice. Embeds fakeQ so the
// base dashboards Querier is still satisfied at the type-assert.
type fakeBdQ struct {
	fakeQ
	building     dbq.Building
	buildingErr  error
	site         dbq.Site
	rooms        []dbq.ListRoomsByBuildingIDsRow
	rows         []dbq.ListRowsByRoomIDsRow
	racks        []dbq.Rack
	assets       []dbq.Asset
	pduTelemetry []dbq.ListPduKwTelemetryRow
}

func (f *fakeBdQ) GetBuilding(_ context.Context, _ uuid.UUID) (dbq.Building, error) {
	return f.building, f.buildingErr
}
func (f *fakeBdQ) GetSite(_ context.Context, _ uuid.UUID) (dbq.Site, error) {
	return f.site, nil
}
func (f *fakeBdQ) ListRoomsByBuildingIDs(_ context.Context, _ []uuid.UUID) ([]dbq.ListRoomsByBuildingIDsRow, error) {
	return f.rooms, nil
}
func (f *fakeBdQ) ListRowsByRoomIDs(_ context.Context, _ []uuid.UUID) ([]dbq.ListRowsByRoomIDsRow, error) {
	return f.rows, nil
}
func (f *fakeBdQ) ListRacksByRowIDs(_ context.Context, _ []uuid.UUID) ([]dbq.Rack, error) {
	return f.racks, nil
}
func (f *fakeBdQ) ListAssetsByRackIDs(_ context.Context, _ []uuid.UUID) ([]dbq.Asset, error) {
	return f.assets, nil
}
func (f *fakeBdQ) ListPduKwTelemetry(_ context.Context, _ []uuid.UUID) ([]dbq.ListPduKwTelemetryRow, error) {
	return f.pduTelemetry, nil
}

func mountBd(f *fakeBdQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f, CollectorStaleSeconds: 600}).Mount(r)
	return r
}

func doBd(t *testing.T, h http.Handler, path string) (int, []byte) {
	t.Helper()
	rec := authtest.ServeRequest(h, authtest.PrincipalWithCaps(capDashboardsRead), "GET", path, nil)
	return rec.Code, rec.Body.Bytes()
}

// Go-canonical endpoint — a missing building is a real 404, not the
// ported dashboards' 200-{"error":"not_found"} parity shape.
func TestBuildingDetail_NotFoundIs404(t *testing.T) {
	f := &fakeBdQ{buildingErr: pgx.ErrNoRows}
	code, _ := doBd(t, mountBd(f), "/dashboards/buildings/"+uuid.New().String())
	if code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", code)
	}
}

func TestBuildingDetail_BadUUIDIs400(t *testing.T) {
	code, _ := doBd(t, mountBd(&fakeBdQ{}), "/dashboards/buildings/not-a-uuid")
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", code)
	}
}

func TestBuildingDetail_HappyPath(t *testing.T) {
	sid, bid, rmid, rw1, rw2, rkid := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	pduID := uuid.New()
	dkw, cool, maxKw := "12.00", "3.50", "8.00"
	kwNow := 1.25
	f := &fakeBdQ{
		building: dbq.Building{ID: bid, SiteID: sid, Name: "Bldg A", Code: "BA"},
		site:     dbq.Site{ID: sid, Name: "Site A", Code: "SA", RegionID: uuid.New(), LifecycleState: "active"},
		rooms: []dbq.ListRoomsByBuildingIDsRow{{
			ID: rmid, BuildingID: bid, Name: "Data Hall 1", Code: "DH1",
			DesignKw: &dkw, FloorAreaSqft: intPtrLocal(400), DesignCoolingTons: &cool,
			GridCols: intPtrLocal(24), GridRows: intPtrLocal(12),
		}},
		rows: []dbq.ListRowsByRoomIDsRow{
			{ID: rw1, RoomID: rmid, Name: "Row 1", Code: "RW1"},
			{ID: rw2, RoomID: rmid, Name: "Row 2", Code: "RW2"},
		},
		racks: []dbq.Rack{
			{ID: rkid, SiteID: sid, RowID: rw1, Name: "Rack 1", Code: "RK1", UHeight: 42, MaxKw: &maxKw,
				GridX: intPtrLocal(3), GridY: intPtrLocal(5), GridRotation: 90},
		},
		assets: []dbq.Asset{
			{ID: uuid.New(), SiteID: sid, RackID: &rkid, Kind: "server", LifecycleState: "active",
				RackPositionU: intPtrLocal(1), RackUnits: intPtrLocal(2)},
			{ID: pduID, SiteID: sid, RackID: &rkid, Kind: "pdu", LifecycleState: "active"},
		},
		pduTelemetry: []dbq.ListPduKwTelemetryRow{
			{AssetID: pduID, Metric: "pdu.input.kw", LastValue: &kwNow},
		},
	}
	code, body := doBd(t, mountBd(f), "/dashboards/buildings/"+bid.String())
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	var resp buildingDetailResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Building.ID != bid.String() || resp.Building.Code != "BA" || resp.Building.SiteID != sid.String() {
		t.Errorf("building: %+v", resp.Building)
	}
	if resp.Site.Code != "SA" {
		t.Errorf("site: %+v", resp.Site)
	}
	if resp.Capacity.UTotal != 42 || resp.Capacity.UUsed != 2 || resp.Capacity.RacksTotal != 1 {
		t.Errorf("building capacity: %+v", resp.Capacity)
	}
	if resp.Capacity.KwMaxSum == nil || *resp.Capacity.KwMaxSum != 8.0 {
		t.Errorf("kw_max_sum = %v, want 8.0", resp.Capacity.KwMaxSum)
	}
	if resp.Capacity.KwCurrent == nil || *resp.Capacity.KwCurrent != 1.25 {
		t.Errorf("kw_current = %v, want 1.25", resp.Capacity.KwCurrent)
	}
	if len(resp.Floors) != 1 {
		t.Fatalf("floors = %d, want 1", len(resp.Floors))
	}
	floor := resp.Floors[0]
	if floor.Code != "DH1" || floor.DesignKw == nil || *floor.DesignKw != 12.0 {
		t.Errorf("floor identity: %+v", floor)
	}
	if floor.FloorAreaSqft == nil || *floor.FloorAreaSqft != 400 {
		t.Errorf("floor_area_sqft = %v, want 400", floor.FloorAreaSqft)
	}
	if floor.DesignCoolingTons == nil || *floor.DesignCoolingTons != 3.5 {
		t.Errorf("design_cooling_tons = %v, want 3.5", floor.DesignCoolingTons)
	}
	if floor.Capacity.UUsed != 2 || floor.Capacity.UTotal != 42 {
		t.Errorf("floor capacity: %+v", floor.Capacity)
	}
	if len(floor.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(floor.Rows))
	}
	if len(floor.Rows[0].Racks) != 1 {
		t.Fatalf("row 1 racks = %d, want 1", len(floor.Rows[0].Racks))
	}
	rack := floor.Rows[0].Racks[0]
	if rack.Code != "RK1" || rack.UUsed != 2 || rack.AssetCount != 2 {
		t.Errorf("rack node: %+v", rack)
	}
	if rack.KwCurrent == nil || *rack.KwCurrent != 1.25 {
		t.Errorf("rack kw_current = %v, want 1.25", rack.KwCurrent)
	}
	if rack.GridX == nil || *rack.GridX != 3 || rack.GridY == nil || *rack.GridY != 5 || rack.GridRotation != 90 {
		t.Errorf("rack grid placement: %+v", rack)
	}
	if floor.GridCols == nil || *floor.GridCols != 24 || floor.GridRows == nil || *floor.GridRows != 12 {
		t.Errorf("floor grid dims: %+v", floor)
	}
	if len(floor.Rows[1].Racks) != 0 {
		t.Errorf("row 2 should be empty; got %+v", floor.Rows[1].Racks)
	}
}

// A building with no rooms still returns a well-formed zero payload.
func TestBuildingDetail_EmptyBuilding(t *testing.T) {
	sid, bid := uuid.New(), uuid.New()
	f := &fakeBdQ{
		building: dbq.Building{ID: bid, SiteID: sid, Name: "Empty", Code: "EM"},
		site:     dbq.Site{ID: sid, Name: "Site A", Code: "SA", RegionID: uuid.New(), LifecycleState: "active"},
	}
	code, body := doBd(t, mountBd(f), "/dashboards/buildings/"+bid.String())
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	var resp buildingDetailResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Floors) != 0 {
		t.Errorf("floors = %d, want 0", len(resp.Floors))
	}
	if resp.Capacity.UTotal != 0 || resp.Capacity.RacksTotal != 0 {
		t.Errorf("capacity: %+v", resp.Capacity)
	}
}
