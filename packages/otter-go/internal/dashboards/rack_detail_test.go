package dashboards

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

// fakeRdQ stubs the RackDetailQuerier slice. Embeds fakeQ for the
// enterprise/free-space/sites-at-risk/asset-detail/site-detail surfaces.
type fakeRdQ struct {
	fakeQ
	rack         dbq.Rack
	rackErr      error
	assets       []dbq.Asset
	openAlerts   []dbq.ListOpenAlertsByAssetIDsRow
	freshness    []dbq.ListAssetFreshnessByIDsRow
	outlets      []dbq.ListOutletsByPduIDsRow
	connections  []dbq.PowerConnection
	pduTelemetry []dbq.ListPduKwTelemetryRow
}

func (f *fakeRdQ) GetRack(_ context.Context, _ uuid.UUID) (dbq.Rack, error) {
	return f.rack, f.rackErr
}
func (f *fakeRdQ) ListAssetsByRackOrdered(_ context.Context, _ uuid.UUID) ([]dbq.Asset, error) {
	return f.assets, nil
}
func (f *fakeRdQ) ListOpenAlertsByAssetIDs(_ context.Context, _ []uuid.UUID) ([]dbq.ListOpenAlertsByAssetIDsRow, error) {
	return f.openAlerts, nil
}
func (f *fakeRdQ) ListAssetFreshnessByIDs(_ context.Context, _ []uuid.UUID) ([]dbq.ListAssetFreshnessByIDsRow, error) {
	return f.freshness, nil
}
func (f *fakeRdQ) ListOutletsByPduIDs(_ context.Context, ids []uuid.UUID) ([]dbq.ListOutletsByPduIDsRow, error) {
	want := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	var out []dbq.ListOutletsByPduIDsRow
	for _, o := range f.outlets {
		if _, ok := want[o.PduAssetID]; ok {
			out = append(out, o)
		}
	}
	return out, nil
}
func (f *fakeRdQ) ListPowerConnectionsByOutletIDs(_ context.Context, ids []uuid.UUID) ([]dbq.PowerConnection, error) {
	want := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	var out []dbq.PowerConnection
	for _, c := range f.connections {
		if _, ok := want[c.OutletID]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}
func (f *fakeRdQ) ListPduKwTelemetry(_ context.Context, _ []uuid.UUID) ([]dbq.ListPduKwTelemetryRow, error) {
	return f.pduTelemetry, nil
}

func mountRd(f *fakeRdQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f, CollectorStaleSeconds: 600}).Mount(r)
	return r
}

func doRd(t *testing.T, h http.Handler, path string) (int, []byte) {
	t.Helper()
	rec := authtest.ServeRequest(h, authtest.PrincipalWithCaps(capDashboardsRead), "GET", path, nil)
	return rec.Code, rec.Body.Bytes()
}

func TestRackDetail_NotFoundReturns200(t *testing.T) {
	f := &fakeRdQ{rackErr: pgx.ErrNoRows}
	code, body := doRd(t, mountRd(f), "/dashboards/racks/"+uuid.New().String())
	if code != http.StatusOK {
		t.Errorf("status = %d, want 200 (Python parity)", code)
	}
	var resp notFoundResponse
	_ = json.Unmarshal(body, &resp)
	if resp.Error != "not_found" {
		t.Errorf("error = %q, want not_found", resp.Error)
	}
}

func TestRackDetail_BadUUIDIs400(t *testing.T) {
	code, _ := doRd(t, mountRd(&fakeRdQ{}), "/dashboards/racks/not-a-uuid")
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", code)
	}
}

func TestRackDetail_HappyPath(t *testing.T) {
	rkid, sid, rwid := uuid.New(), uuid.New(), uuid.New()
	srvID, pduAID, pduBID := uuid.New(), uuid.New(), uuid.New()
	outletA, outletB := uuid.New(), uuid.New()
	sideA, sideB := "A", "B"
	f := &fakeRdQ{
		rack: dbq.Rack{
			ID: rkid, SiteID: sid, RowID: rwid, Name: "Rack 1", Code: "R1", UHeight: 42,
		},
		assets: []dbq.Asset{
			{ID: srvID, RackID: &rkid, Kind: "server", Name: "srv-1",
				RackPositionU: intPtrLocal(1), RackUnits: intPtrLocal(2),
				LifecycleState: "active", Face: "front", Mount: "rack-front"},
			{ID: pduAID, RackID: &rkid, Kind: "pdu", Name: "pdu-a",
				PduSide: &sideA, Face: "front", Mount: "rack-side", LifecycleState: "active"},
			{ID: pduBID, RackID: &rkid, Kind: "pdu", Name: "pdu-b",
				PduSide: &sideB, Face: "rear", Mount: "rack-side", LifecycleState: "active"},
		},
		openAlerts: []dbq.ListOpenAlertsByAssetIDsRow{{AssetID: &srvID, N: 3}},
		freshness: []dbq.ListAssetFreshnessByIDsRow{
			{AssetID: srvID, Freshness: "current", N: 5},
		},
		outlets: []dbq.ListOutletsByPduIDsRow{
			{ID: outletA, PduAssetID: pduAID, Position: 1},
			{ID: outletB, PduAssetID: pduBID, Position: 1},
		},
		connections: []dbq.PowerConnection{
			{OutletID: outletA, AssetID: srvID, PsuIndex: 0},
			{OutletID: outletB, AssetID: srvID, PsuIndex: 1},
		},
	}
	code, body := doRd(t, mountRd(f), "/dashboards/racks/"+rkid.String())
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	var resp rackDetailResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Rack.Code != "R1" || resp.Rack.UHeight != 42 {
		t.Errorf("rack: %+v", resp.Rack)
	}
	if resp.Capacity.UTotal != 42 || resp.Capacity.UUsed != 2 {
		t.Errorf("capacity: %+v", resp.Capacity)
	}
	if len(resp.Assets) != 3 {
		t.Fatalf("assets len = %d, want 3", len(resp.Assets))
	}
	srv := resp.Assets[0]
	if srv.ID != srvID.String() {
		// First asset by rack_position_u asc = server at slot 1
		t.Errorf("first asset = %s, want server (%s)", srv.ID, srvID)
	}
	if srv.OpenAlerts != 3 {
		t.Errorf("server open_alerts = %d, want 3", srv.OpenAlerts)
	}
	if srv.Freshness["current"] != 5 {
		t.Errorf("server freshness.current = %d, want 5", srv.Freshness["current"])
	}
	if srv.Redundancy == nil || *srv.Redundancy != "redundant" {
		t.Errorf("server redundancy = %v, want redundant", srv.Redundancy)
	}
	if len(resp.PowerChain.PDUs) != 2 {
		t.Errorf("power_chain.pdus len = %d, want 2", len(resp.PowerChain.PDUs))
	}
}

func TestRackDetail_RackUnitsNilDefaultsTo1(t *testing.T) {
	rkid := uuid.New()
	srvID := uuid.New()
	f := &fakeRdQ{
		rack: dbq.Rack{ID: rkid, SiteID: uuid.New(), RowID: uuid.New(), UHeight: 42, Name: "r", Code: "r"},
		assets: []dbq.Asset{
			{ID: srvID, RackID: &rkid, Kind: "server", Name: "s",
				RackPositionU: intPtrLocal(1), RackUnits: nil,
				LifecycleState: "active"},
		},
	}
	code, body := doRd(t, mountRd(f), "/dashboards/racks/"+rkid.String())
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	var resp rackDetailResponse
	_ = json.Unmarshal(body, &resp)
	if len(resp.Assets) != 1 || resp.Assets[0].RackUnits != 1 {
		t.Errorf("rack_units default = %d, want 1", resp.Assets[0].RackUnits)
	}
}

// Empty assets → still renders {rack, capacity, power_chain, assets:[]}.
func TestRackDetail_NoAssets(t *testing.T) {
	rkid := uuid.New()
	f := &fakeRdQ{
		rack: dbq.Rack{ID: rkid, SiteID: uuid.New(), RowID: uuid.New(), UHeight: 42, Name: "r", Code: "r"},
	}
	code, body := doRd(t, mountRd(f), "/dashboards/racks/"+rkid.String())
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if !strings.Contains(string(body), `"assets":[]`) {
		t.Errorf("assets should encode as []; got %s", body)
	}
}

func TestRackDetail_RejectsWithoutCap(t *testing.T) {
	r := chi.NewRouter()
	(&Handler{Q: &fakeRdQ{}, CollectorStaleSeconds: 600}).Mount(r)
	rec := authtest.ServeRequest(r, authtest.PrincipalWithCaps("inventory:sites:read"),
		"GET", "/dashboards/racks/"+uuid.New().String(), nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}
