package dashboards

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

// fakeSdQ stubs the SiteDetailQuerier slice. Embeds fakeQ so the
// enterprise/free-space/sites-at-risk/asset-detail surfaces are still
// satisfied at the type-assert.
type fakeSdQ struct {
	fakeQ
	site            dbq.Site
	siteErr         error
	region          dbq.Region
	regionErr       error
	buildings       []dbq.ListBuildingsBySiteRow
	rooms           []dbq.ListRoomsByBuildingIDsRow
	rows            []dbq.ListRowsByRoomIDsRow
	racks           []dbq.Rack
	assets          []dbq.Asset
	alerts          []dbq.ListSiteAlertsBySeverityRow
	collectors      []dbq.ListSiteCollectorsRow
	pduTelemetry    []dbq.ListPduKwTelemetryRow
	collectorsCallN int
}

func (f *fakeSdQ) GetSite(_ context.Context, _ uuid.UUID) (dbq.Site, error) {
	return f.site, f.siteErr
}
func (f *fakeSdQ) GetRegion(_ context.Context, _ uuid.UUID) (dbq.Region, error) {
	return f.region, f.regionErr
}
func (f *fakeSdQ) ListBuildingsBySite(_ context.Context, _ uuid.UUID) ([]dbq.ListBuildingsBySiteRow, error) {
	return f.buildings, nil
}
func (f *fakeSdQ) ListRoomsByBuildingIDs(_ context.Context, _ []uuid.UUID) ([]dbq.ListRoomsByBuildingIDsRow, error) {
	return f.rooms, nil
}
func (f *fakeSdQ) ListRowsByRoomIDs(_ context.Context, _ []uuid.UUID) ([]dbq.ListRowsByRoomIDsRow, error) {
	return f.rows, nil
}
func (f *fakeSdQ) ListRacksBySite(_ context.Context, _ uuid.UUID) ([]dbq.Rack, error) {
	return f.racks, nil
}
func (f *fakeSdQ) ListAssetsBySite(_ context.Context, _ uuid.UUID) ([]dbq.Asset, error) {
	return f.assets, nil
}
func (f *fakeSdQ) ListSiteAlertsBySeverity(_ context.Context, _ uuid.UUID) ([]dbq.ListSiteAlertsBySeverityRow, error) {
	return f.alerts, nil
}
func (f *fakeSdQ) ListSiteCollectors(_ context.Context, _ uuid.UUID) ([]dbq.ListSiteCollectorsRow, error) {
	f.collectorsCallN++
	return f.collectors, nil
}
func (f *fakeSdQ) ListPduKwTelemetry(_ context.Context, _ []uuid.UUID) ([]dbq.ListPduKwTelemetryRow, error) {
	return f.pduTelemetry, nil
}

func mountSd(f *fakeSdQ, stale int) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f, CollectorStaleSeconds: stale}).Mount(r)
	return r
}

func doSd(t *testing.T, h http.Handler, path string) (int, []byte) {
	t.Helper()
	rec := authtest.ServeRequest(h, authtest.PrincipalWithCaps(capDashboardsRead), "GET", path, nil)
	return rec.Code, rec.Body.Bytes()
}

func TestSiteDetail_NotFoundReturns200(t *testing.T) {
	f := &fakeSdQ{siteErr: pgx.ErrNoRows}
	code, body := doSd(t, mountSd(f, 600), "/dashboards/sites/"+uuid.New().String())
	if code != http.StatusOK {
		t.Errorf("status = %d, want 200 (Python parity)", code)
	}
	var resp notFoundResponse
	_ = json.Unmarshal(body, &resp)
	if resp.Error != "not_found" {
		t.Errorf("error = %q, want not_found", resp.Error)
	}
}

func TestSiteDetail_BadUUIDIs400(t *testing.T) {
	code, _ := doSd(t, mountSd(&fakeSdQ{}, 600), "/dashboards/sites/not-a-uuid")
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", code)
	}
}

func TestSiteDetail_HappyPath(t *testing.T) {
	sid, rid, bid, rmid, rwid, rkid := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	addr := "123 Main St"
	dkw := "10.00"
	f := &fakeSdQ{
		site: dbq.Site{
			ID: sid, RegionID: rid, Name: "Site A", Code: "SA",
			Address: &addr, LifecycleState: "active",
		},
		region:    dbq.Region{ID: rid, Name: "Region 1", Code: "R1"},
		buildings: []dbq.ListBuildingsBySiteRow{{ID: bid, Name: "Bldg A", Code: "BA"}},
		rooms:     []dbq.ListRoomsByBuildingIDsRow{{ID: rmid, BuildingID: bid, Name: "Room 1", Code: "RM1", DesignKw: &dkw}},
		rows:      []dbq.ListRowsByRoomIDsRow{{ID: rwid, RoomID: rmid, Name: "Row 1", Code: "RW1"}},
		racks: []dbq.Rack{
			{ID: rkid, SiteID: sid, RowID: rwid, Name: "Rack 1", Code: "RK1", UHeight: 42},
		},
		assets: []dbq.Asset{
			{ID: uuid.New(), SiteID: sid, RackID: &rkid, Kind: "server", LifecycleState: "active",
				RackPositionU: intPtrLocal(1), RackUnits: intPtrLocal(2)},
			{ID: uuid.New(), SiteID: sid, RackID: &rkid, Kind: "server", LifecycleState: "decommissioned"},
		},
		alerts: []dbq.ListSiteAlertsBySeverityRow{
			{Severity: "critical", N: 1}, {Severity: "warning", N: 3},
		},
		collectors: []dbq.ListSiteCollectorsRow{
			{ID: uuid.New(), Status: "healthy", Enabled: true, LastSeenAt: nowPtr()},
		},
	}
	code, body := doSd(t, mountSd(f, 600), "/dashboards/sites/"+sid.String())
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	var resp siteDetailResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Site.ID != sid.String() {
		t.Errorf("site.id = %q", resp.Site.ID)
	}
	if resp.Region == nil || resp.Region.Code != "R1" {
		t.Errorf("region: %+v", resp.Region)
	}
	if resp.KPIs.Buildings != 1 || resp.KPIs.Rooms != 1 || resp.KPIs.Rows != 1 || resp.KPIs.Racks != 1 {
		t.Errorf("KPI counts wrong: %+v", resp.KPIs)
	}
	if resp.KPIs.Assets["active"] != 1 || resp.KPIs.Assets["decommissioned"] != 1 || resp.KPIs.Assets["total"] != 2 {
		t.Errorf("assets kpi: %+v", resp.KPIs.Assets)
	}
	if resp.KPIs.AlertsFiring["critical"] != 1 || resp.KPIs.AlertsFiring["warning"] != 3 || resp.KPIs.AlertsFiring["total"] != 4 {
		t.Errorf("alerts kpi: %+v", resp.KPIs.AlertsFiring)
	}
	if resp.KPIs.Collectors["healthy"] != 1 || resp.KPIs.Collectors["total"] != 1 {
		t.Errorf("collectors kpi: %+v", resp.KPIs.Collectors)
	}
	if resp.Capacity.UTotal != 42 || resp.Capacity.UUsed != 2 {
		t.Errorf("capacity u: %+v", resp.Capacity)
	}
	if len(resp.Hierarchy) != 1 || resp.Hierarchy[0].Code != "BA" {
		t.Errorf("hierarchy: %+v", resp.Hierarchy)
	}
	room := resp.Hierarchy[0].Rooms[0]
	if room.DesignKw == nil || *room.DesignKw != 10.0 {
		t.Errorf("room.design_kw = %v, want 10.0", room.DesignKw)
	}
	if len(resp.OrphanRacks) != 0 {
		t.Errorf("expected no orphans; got %d", len(resp.OrphanRacks))
	}
}

// A rack whose row chain doesn't anchor under any building lands in
// orphan_racks. Defensive against historic schema inconsistency.
func TestSiteDetail_OrphanRackSurfaces(t *testing.T) {
	sid, orphanRowID, rkid := uuid.New(), uuid.New(), uuid.New()
	f := &fakeSdQ{
		site:      dbq.Site{ID: sid, Name: "S", Code: "S", LifecycleState: "active", RegionID: uuid.New()},
		regionErr: pgx.ErrNoRows,
		racks: []dbq.Rack{
			// row_id refers to a row we don't have in the topology
			{ID: rkid, SiteID: sid, RowID: orphanRowID, Name: "Orphan", Code: "ORP", UHeight: 42},
		},
	}
	code, body := doSd(t, mountSd(f, 600), "/dashboards/sites/"+sid.String())
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	var resp siteDetailResponse
	_ = json.Unmarshal(body, &resp)
	if len(resp.OrphanRacks) != 1 || resp.OrphanRacks[0].Code != "ORP" {
		t.Errorf("expected one orphan rack; got %+v", resp.OrphanRacks)
	}
	if resp.Region != nil {
		t.Errorf("region should be nil when GetRegion returned ErrNoRows; got %+v", resp.Region)
	}
}

// Collector with no last_seen_at while enabled counts as stale.
func TestSiteDetail_StaleCollectorCounted(t *testing.T) {
	sid := uuid.New()
	long := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	f := &fakeSdQ{
		site:      dbq.Site{ID: sid, Name: "S", Code: "S", LifecycleState: "active", RegionID: uuid.New()},
		regionErr: pgx.ErrNoRows,
		collectors: []dbq.ListSiteCollectorsRow{
			{Status: "healthy", Enabled: true, LastSeenAt: nil},      // stale (never reported)
			{Status: "healthy", Enabled: true, LastSeenAt: &long},    // stale (last seen 2020)
			{Status: "healthy", Enabled: false, LastSeenAt: nil},     // NOT stale (disabled)
			{Status: "healthy", Enabled: true, LastSeenAt: nowPtr()}, // NOT stale (fresh)
		},
	}
	code, body := doSd(t, mountSd(f, 60), "/dashboards/sites/"+sid.String())
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	var resp siteDetailResponse
	_ = json.Unmarshal(body, &resp)
	if resp.KPIs.Collectors["stale"] != 2 {
		t.Errorf("stale collectors = %d, want 2", resp.KPIs.Collectors["stale"])
	}
	if resp.KPIs.Collectors["total"] != 4 {
		t.Errorf("total collectors = %d, want 4", resp.KPIs.Collectors["total"])
	}
}

// Python parity: a collector whose status is literally "stale" AND is
// flagged stale by the freshness threshold gets double-counted in
// out["stale"]. See the collectorsKpiFrom docstring — likely a Python
// bug but the wire contract.
func TestSiteDetail_StaleStatusDoubleCounted(t *testing.T) {
	sid := uuid.New()
	long := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	f := &fakeSdQ{
		site:      dbq.Site{ID: sid, Name: "S", Code: "S", LifecycleState: "active", RegionID: uuid.New()},
		regionErr: pgx.ErrNoRows,
		collectors: []dbq.ListSiteCollectorsRow{
			// status=stale (+1 to "stale") AND enabled+old (+1 to "stale") = 2
			{Status: "stale", Enabled: true, LastSeenAt: &long},
		},
	}
	code, body := doSd(t, mountSd(f, 60), "/dashboards/sites/"+sid.String())
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	var resp siteDetailResponse
	_ = json.Unmarshal(body, &resp)
	if resp.KPIs.Collectors["stale"] != 2 {
		t.Errorf("Python parity: stale-status + freshness-stale should double-count; got %d", resp.KPIs.Collectors["stale"])
	}
}

// Empty topology — no buildings/rooms/rows/racks — still produces a
// valid response with all-zero counts + empty arrays.
func TestSiteDetail_EmptyTopology(t *testing.T) {
	sid := uuid.New()
	f := &fakeSdQ{
		site:      dbq.Site{ID: sid, Name: "S", Code: "S", LifecycleState: "active", RegionID: uuid.New()},
		regionErr: pgx.ErrNoRows,
	}
	code, body := doSd(t, mountSd(f, 600), "/dashboards/sites/"+sid.String())
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	for _, sub := range []string{`"hierarchy":[]`, `"orphan_racks":[]`} {
		if !strings.Contains(string(body), sub) {
			t.Errorf("expected %s; got %s", sub, body)
		}
	}
}

// Capacity rollup sums per-rack values and computes site-level
// kw_max_sum + kw_pct only when both numerators are populated.
func TestSiteDetail_CapacityRollupSums(t *testing.T) {
	sid := uuid.New()
	r1, r2 := uuid.New(), uuid.New()
	pdu1, pdu2 := uuid.New(), uuid.New()
	f := &fakeSdQ{
		site:      dbq.Site{ID: sid, Name: "S", Code: "S", LifecycleState: "active", RegionID: uuid.New()},
		regionErr: pgx.ErrNoRows,
		racks: []dbq.Rack{
			{ID: r1, SiteID: sid, UHeight: 42, MaxKw: strPtrLocal("10")},
			{ID: r2, SiteID: sid, UHeight: 42, MaxKw: strPtrLocal("5")},
		},
		assets: []dbq.Asset{
			{ID: pdu1, RackID: &r1, Kind: "pdu"},
			{ID: pdu2, RackID: &r2, Kind: "pdu"},
		},
		pduTelemetry: []dbq.ListPduKwTelemetryRow{
			{AssetID: pdu1, Metric: "pdu.input.kw", LastValue: floatPtrLocal(3.0)},
			{AssetID: pdu2, Metric: "pdu.input.kw", LastValue: floatPtrLocal(2.0)},
		},
	}
	code, body := doSd(t, mountSd(f, 600), "/dashboards/sites/"+sid.String())
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	var resp siteDetailResponse
	_ = json.Unmarshal(body, &resp)
	if resp.Capacity.KwMaxSum == nil || *resp.Capacity.KwMaxSum != 15.0 {
		t.Errorf("kw_max_sum = %v, want 15.0", resp.Capacity.KwMaxSum)
	}
	if resp.Capacity.KwCurrent == nil || *resp.Capacity.KwCurrent != 5.0 {
		t.Errorf("kw_current = %v, want 5.0", resp.Capacity.KwCurrent)
	}
	// 5 / 15 = 33.333… → round1 = 33.3
	if resp.Capacity.KwPct == nil || *resp.Capacity.KwPct < 33.2 || *resp.Capacity.KwPct > 33.4 {
		t.Errorf("kw_pct = %v, want ~33.3", resp.Capacity.KwPct)
	}
	if resp.Capacity.RacksTotal != 2 || resp.Capacity.RacksWithKwRating != 2 {
		t.Errorf("racks counters: %+v", resp.Capacity)
	}
}

func TestSiteDetail_RejectsWithoutCap(t *testing.T) {
	r := chi.NewRouter()
	(&Handler{Q: &fakeSdQ{}, CollectorStaleSeconds: 600}).Mount(r)
	rec := authtest.ServeRequest(r, authtest.PrincipalWithCaps("inventory:sites:read"),
		"GET", "/dashboards/sites/"+uuid.New().String(), nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// nowPtr returns the current UTC time wrapped in a pointer. Used by
// the collectors KPI tests so a row reads as "fresh" against any
// reasonable stale_seconds threshold.
func nowPtr() *time.Time {
	t := time.Now().UTC()
	return &t
}
