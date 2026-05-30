package dashboards

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

// fakeFsQ stubs the SQL slice /free-space needs. Separate from the
// enterprise tests' fakeQ so each test file's stubs stay narrow.
type fakeFsQ struct {
	fakeQ         // embed so enterprise-side methods are filled too
	racks         []dbq.Rack
	assets        []dbq.Asset
	pduTelemetry  []dbq.PduKwTelemetryRow
	gotFilter     dbq.ListRacksForFreeSpaceParams
	gotAssetRacks []uuid.UUID
	gotPduIDs     []uuid.UUID
	racksErr      error
}

func (f *fakeFsQ) ListRacksForFreeSpace(_ context.Context, arg dbq.ListRacksForFreeSpaceParams) ([]dbq.Rack, error) {
	f.gotFilter = arg
	return f.racks, f.racksErr
}
func (f *fakeFsQ) ListAssetsByRackIDs(_ context.Context, ids []uuid.UUID) ([]dbq.Asset, error) {
	f.gotAssetRacks = ids
	return f.assets, nil
}
func (f *fakeFsQ) ListPduKwTelemetry(_ context.Context, ids []uuid.UUID) ([]dbq.PduKwTelemetryRow, error) {
	f.gotPduIDs = ids
	if len(ids) == 0 {
		return nil, nil
	}
	want := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	var out []dbq.PduKwTelemetryRow
	for _, r := range f.pduTelemetry {
		if _, ok := want[r.AssetID]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func mountFs(f *fakeFsQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f, CollectorStaleSeconds: 600}).Mount(r)
	return r
}

func doFs(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	return authtest.ServeRequest(h, authtest.PrincipalWithCaps("dashboards:dashboards:read"), "GET", path, nil)
}

// Empty rack set → response carries `racks: []` not `null`, count: 0.
func TestFreeSpace_EmptyRacks(t *testing.T) {
	f := &fakeFsQ{}
	rec := doFs(t, mountFs(f), "/dashboards/free-space?u=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body freeSpaceResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Racks == nil {
		t.Error("racks should be empty array, not null")
	}
	if body.Count != 0 {
		t.Errorf("count = %d, want 0", body.Count)
	}
	if body.Query.MinU != 1 {
		t.Errorf("min_u = %d, want 1", body.Query.MinU)
	}
}

func TestFreeSpace_OneRackOneFreeRun(t *testing.T) {
	rid, sid := uuid.New(), uuid.New()
	pid := uuid.New()
	one := int32(1)
	f := &fakeFsQ{
		racks: []dbq.Rack{
			{ID: rid, SiteID: sid, Name: "rack-1", Code: "R1", UHeight: 42, MaxKw: nil},
		},
		assets: []dbq.Asset{
			// Server in slot 1, 2U.
			{ID: uuid.New(), RackID: &rid, Kind: "server", RackPositionU: intPtrLocal(1), RackUnits: intPtrLocal(2)},
			// PDU asset with no telemetry rows — kw_current stays nil.
			{ID: pid, RackID: &rid, Kind: "pdu", RackPositionU: &one, RackUnits: &one},
		},
	}
	rec := doFs(t, mountFs(f), "/dashboards/free-space?u=10")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body freeSpaceResponse
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Count != 1 || len(body.Racks) != 1 {
		t.Fatalf("expected 1 rack; got %d", body.Count)
	}
	row := body.Racks[0]
	if row.RackID != rid.String() {
		t.Errorf("rack_id = %q, want %s", row.RackID, rid)
	}
	if row.Code != "R1" || row.Name != "rack-1" {
		t.Errorf("identity wrong: %+v", row)
	}
	if row.UFree != 40 || row.BiggestContiguousFree != 40 {
		t.Errorf("u_free=%d biggest=%d, want both 40", row.UFree, row.BiggestContiguousFree)
	}
}

// min_u rejects racks whose biggest free run is shorter than min_u.
func TestFreeSpace_MinURejects(t *testing.T) {
	rid := uuid.New()
	// Fill slots 1..42 with 42 1-U assets → biggest free run = 0.
	f := &fakeFsQ{
		racks: []dbq.Rack{
			{ID: rid, UHeight: 42, Name: "r", Code: "C"},
		},
		assets: makeFillingAssets(rid, 42, 42),
	}
	rec := doFs(t, mountFs(f), "/dashboards/free-space?u=1")
	var body freeSpaceResponse
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Count != 0 {
		t.Errorf("expected 0 racks (biggest free < min_u); got %d", body.Count)
	}
}

// min_kw_headroom rejects racks whose (kw_max - kw_current) <
// requested headroom. NULL on either kw side keeps the rack in
// (Python parity — the AND-chain short-circuits to "rack passes").
func TestFreeSpace_MinKwHeadroomRejects(t *testing.T) {
	rid, sid := uuid.New(), uuid.New()
	pduID := uuid.New()
	f := &fakeFsQ{
		racks: []dbq.Rack{
			{ID: rid, SiteID: sid, UHeight: 42, MaxKw: strPtrLocal("5"), Name: "r", Code: "C"},
		},
		assets: []dbq.Asset{
			{ID: pduID, RackID: &rid, Kind: "pdu"},
		},
		pduTelemetry: []dbq.PduKwTelemetryRow{
			{AssetID: pduID, Metric: "pdu.input.kw", LastValue: strPtrLocal("4.5")},
		},
	}
	// kw_max=5, kw_current=4.5 → headroom 0.5. Request 1.0 → reject.
	rec := doFs(t, mountFs(f), "/dashboards/free-space?u=1&min_kw_headroom=1")
	var body freeSpaceResponse
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Count != 0 {
		t.Errorf("rack should be rejected; got %d", body.Count)
	}
	// Request 0.4 → accept (headroom 0.5 > 0.4).
	rec = doFs(t, mountFs(f), "/dashboards/free-space?u=1&min_kw_headroom=0.4")
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Count != 1 {
		t.Errorf("rack should be accepted; got %d", body.Count)
	}
}

// Sort order is biggest_contiguous_free DESC.
func TestFreeSpace_SortedByBiggestFreeDesc(t *testing.T) {
	rA, rB, rC := uuid.New(), uuid.New(), uuid.New()
	racks := []dbq.Rack{
		{ID: rA, UHeight: 42, Name: "A", Code: "A"},
		{ID: rB, UHeight: 42, Name: "B", Code: "B"},
		{ID: rC, UHeight: 42, Name: "C", Code: "C"},
	}
	// rA: occupied 1..20 → biggest free = 22
	// rB: empty → biggest free = 42
	// rC: occupied 1..30 → biggest free = 12
	assets := append(make([]dbq.Asset, 0),
		fillingAsset(rA, 1, 20),
		fillingAsset(rC, 1, 30),
	)
	f := &fakeFsQ{racks: racks, assets: assets}
	rec := doFs(t, mountFs(f), "/dashboards/free-space?u=1")
	var body freeSpaceResponse
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Count != 3 {
		t.Fatalf("expected 3 racks; got %d", body.Count)
	}
	if body.Racks[0].RackID != rB.String() {
		t.Errorf("first should be rB (free=42); got %q", body.Racks[0].RackID)
	}
	if body.Racks[2].RackID != rC.String() {
		t.Errorf("last should be rC (free=12); got %q", body.Racks[2].RackID)
	}
}

// site_id + region_id query params thread to the SQL query as UUID
// pointers.
func TestFreeSpace_FilterParamsThreaded(t *testing.T) {
	site, region := uuid.New(), uuid.New()
	f := &fakeFsQ{}
	doFs(t, mountFs(f), "/dashboards/free-space?u=1&site_id="+site.String()+"&region_id="+region.String())
	if f.gotFilter.SiteID == nil || *f.gotFilter.SiteID != site {
		t.Errorf("site_id not threaded; got %+v", f.gotFilter.SiteID)
	}
	if f.gotFilter.RegionID == nil || *f.gotFilter.RegionID != region {
		t.Errorf("region_id not threaded; got %+v", f.gotFilter.RegionID)
	}
}

// limit clamp 1..500; default 50.
func TestFreeSpace_LimitClamping(t *testing.T) {
	racks := make([]dbq.Rack, 5)
	for i := range racks {
		racks[i] = dbq.Rack{ID: uuid.New(), UHeight: 42, Name: "n", Code: "c"}
	}
	f := &fakeFsQ{racks: racks}
	rec := doFs(t, mountFs(f), "/dashboards/free-space?u=1&limit=2")
	var body freeSpaceResponse
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Count != 2 {
		t.Errorf("limit=2 should cap; got %d", body.Count)
	}
}

// u clamp 0..60.
func TestFreeSpace_UClamping(t *testing.T) {
	f := &fakeFsQ{}
	rec := doFs(t, mountFs(f), "/dashboards/free-space?u=999")
	var body freeSpaceResponse
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Query.MinU != 60 {
		t.Errorf("u clamped to 60; got %d", body.Query.MinU)
	}
}

func TestFreeSpace_RejectsWithoutCap(t *testing.T) {
	r := chi.NewRouter()
	(&Handler{Q: &fakeFsQ{}, CollectorStaleSeconds: 600}).Mount(r)
	rec := authtest.ServeRequest(r, authtest.PrincipalWithCaps("inventory:sites:read"), "GET", "/dashboards/free-space?u=1", nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// ---- helpers ----

func intPtrLocal(v int32) *int32   { return &v }
func strPtrLocal(s string) *string { return &s }

// makeFillingAssets places one 1-U asset per slot from 1..n.
func makeFillingAssets(rackID uuid.UUID, uHeight, n int32) []dbq.Asset {
	out := make([]dbq.Asset, 0, n)
	for i := int32(1); i <= n && i <= uHeight; i++ {
		iCopy := i
		one := int32(1)
		out = append(out, dbq.Asset{
			ID: uuid.New(), RackID: &rackID, Kind: "server",
			RackPositionU: &iCopy, RackUnits: &one,
		})
	}
	return out
}

// fillingAsset places one block of size `length` starting at slot `start`.
func fillingAsset(rackID uuid.UUID, start, length int32) dbq.Asset {
	startCopy, lengthCopy := start, length
	return dbq.Asset{
		ID: uuid.New(), RackID: &rackID, Kind: "server",
		RackPositionU: &startCopy, RackUnits: &lengthCopy,
	}
}

var _ = httptest.NewRecorder // keep import in case future tests need direct httptest access
