// PR 84 — dashboard unit + handler tests.
package dns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

// ---- unit: pct ----

func TestPct_ZeroTotal(t *testing.T) {
	if got := pct(5, 0); got != 0 {
		t.Errorf("got %v, want 0", got)
	}
}

func TestPct_Basic(t *testing.T) {
	// 50/200 = 25.00
	if got := pct(50, 200); got != 25.0 {
		t.Errorf("got %v, want 25.0", got)
	}
}

func TestPct_Rounding(t *testing.T) {
	// 1/3 = 33.333... → 33.33
	if got := pct(1, 3); got != 33.33 {
		t.Errorf("got %v, want 33.33", got)
	}
}

// ---- unit: qpsFromLastSample ----

func TestQpsFromLastSample_NilReturnsNil(t *testing.T) {
	if got := qpsFromLastSample(nil); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestQpsFromLastSample_ZeroIntervalReturnsNil(t *testing.T) {
	s := dbq.DnsMetricsSampleRow{Queries: 100, IntervalSeconds: 0}
	if got := qpsFromLastSample(&s); got != nil {
		t.Errorf("got %v, want nil for zero interval", got)
	}
}

func TestQpsFromLastSample_Basic(t *testing.T) {
	s := dbq.DnsMetricsSampleRow{Queries: 300, IntervalSeconds: 30}
	got := qpsFromLastSample(&s)
	if got == nil || *got != 10.0 {
		t.Errorf("got %v, want 10.0 (300/30)", got)
	}
}

// ---- unit: weightedLatency ----

func TestWeightedLatency_AllNilReturnsNil(t *testing.T) {
	samples := []dbq.DnsMetricsSampleRow{{Queries: 100, P50Ms: nil}}
	if got := weightedLatency(samples, extractP50); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestWeightedLatency_ZeroQueriesSkipped(t *testing.T) {
	// Sample with 0 queries shouldn't pull the weighted avg.
	p1, p2 := 5.0, 10.0
	samples := []dbq.DnsMetricsSampleRow{
		{Queries: 0, P50Ms: &p1}, // skipped
		{Queries: 100, P50Ms: &p2},
	}
	got := weightedLatency(samples, extractP50)
	if got == nil || *got != 10.0 {
		t.Errorf("got %v, want 10.0", got)
	}
}

func TestWeightedLatency_WeightsByQueries(t *testing.T) {
	// (10*100 + 20*300) / (100+300) = 7000/400 = 17.5
	p1, p2 := 10.0, 20.0
	samples := []dbq.DnsMetricsSampleRow{
		{Queries: 100, P50Ms: &p1},
		{Queries: 300, P50Ms: &p2},
	}
	got := weightedLatency(samples, extractP50)
	if got == nil || *got != 17.5 {
		t.Errorf("got %v, want 17.5", got)
	}
}

// ---- handler ----

type fakeDashboardQ struct {
	fakeQ
	servers     []dbq.DnsServerForDashboardRow
	zones       []dbq.DnsZoneForDashboardRow
	samples     []dbq.DnsMetricsSampleRow
	agCount     int64
	gotFabricID *uuid.UUID
	gotMinutes  int
}

func (f *fakeDashboardQ) ListDnsServersForDashboard(_ context.Context, fid *uuid.UUID) ([]dbq.DnsServerForDashboardRow, error) {
	f.gotFabricID = fid
	return f.servers, nil
}
func (f *fakeDashboardQ) ListDnsZonesForDashboard(_ context.Context, _ *uuid.UUID) ([]dbq.DnsZoneForDashboardRow, error) {
	return f.zones, nil
}
func (f *fakeDashboardQ) CountAnycastGroupsForDashboard(_ context.Context, _ *uuid.UUID) (int64, error) {
	return f.agCount, nil
}
func (f *fakeDashboardQ) ListDnsSamplesInWindow(_ context.Context, _ time.Time, _ []uuid.UUID) ([]dbq.DnsMetricsSampleRow, error) {
	return f.samples, nil
}

func mountDashboard(f *fakeDashboardQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

func TestDashboard_EmptyDatabaseHappyPath(t *testing.T) {
	f := &fakeDashboardQ{}
	req := httptest.NewRequest("GET", "/dns/dashboard", nil)
	p := auth.Principal{Capabilities: []string{"*"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountDashboard(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var resp dashboardResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.WindowMinutes != 60 {
		t.Errorf("default window = %d, want 60", resp.WindowMinutes)
	}
	if resp.Overall.ServersTotal != 0 {
		t.Errorf("ServersTotal = %d", resp.Overall.ServersTotal)
	}
}

func TestDashboard_CountsAggregated(t *testing.T) {
	fab := uuid.New()
	site := uuid.New()
	srvID := uuid.New()
	zsk := false
	zone1 := dbq.DnsZoneForDashboardRow{ID: uuid.New(), FabricID: fab, Signed: true, Nsec3Iterations: 5}
	zone2 := dbq.DnsZoneForDashboardRow{ID: uuid.New(), FabricID: fab, Signed: false, Nsec3Iterations: 0}
	_ = zsk
	f := &fakeDashboardQ{
		servers: []dbq.DnsServerForDashboardRow{
			{ID: srvID, FabricID: fab, SiteID: &site, Role: "auth"},
		},
		zones:   []dbq.DnsZoneForDashboardRow{zone1, zone2},
		agCount: 3,
		samples: []dbq.DnsMetricsSampleRow{
			{ServerID: srvID, ObservedAt: time.Now(), IntervalSeconds: 30,
				Queries: 600, Nxdomain: 60, Servfail: 12, Noerror: 528},
		},
	}
	req := httptest.NewRequest("GET", "/dns/dashboard", nil)
	p := auth.Principal{Capabilities: []string{"*"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountDashboard(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp dashboardResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Overall.ServersTotal != 1 {
		t.Errorf("ServersTotal = %d", resp.Overall.ServersTotal)
	}
	if resp.Overall.ZonesTotal != 2 {
		t.Errorf("ZonesTotal = %d", resp.Overall.ZonesTotal)
	}
	if resp.Overall.ZonesSigned != 1 {
		t.Errorf("ZonesSigned = %d", resp.Overall.ZonesSigned)
	}
	if resp.Overall.ZonesNsec3 != 1 {
		t.Errorf("ZonesNsec3 = %d", resp.Overall.ZonesNsec3)
	}
	if resp.Overall.AnycastGroups != 3 {
		t.Errorf("AnycastGroups = %d", resp.Overall.AnycastGroups)
	}
	if resp.Overall.QueriesTotal != 600 {
		t.Errorf("QueriesTotal = %d", resp.Overall.QueriesTotal)
	}
	// 60/600 = 10%
	if resp.Overall.NxdomainPct != 10.0 {
		t.Errorf("NxdomainPct = %v, want 10.0", resp.Overall.NxdomainPct)
	}
	// 12/600 = 2%
	if resp.Overall.ServfailPct != 2.0 {
		t.Errorf("ServfailPct = %v, want 2.0", resp.Overall.ServfailPct)
	}
	if resp.Overall.SitesActive != 1 {
		t.Errorf("SitesActive = %d", resp.Overall.SitesActive)
	}
	// qps_now = 600/30 = 20.0
	if resp.Overall.QpsNow != 20.0 {
		t.Errorf("QpsNow = %v, want 20.0", resp.Overall.QpsNow)
	}
}

func TestDashboard_FabricScopeForwarded(t *testing.T) {
	f := &fakeDashboardQ{}
	fid := uuid.New()
	req := httptest.NewRequest("GET", "/dns/dashboard?fabric_id="+fid.String(), nil)
	p := auth.Principal{Capabilities: []string{"*"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountDashboard(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if f.gotFabricID == nil || *f.gotFabricID != fid {
		t.Errorf("fabric_id not forwarded: %v", f.gotFabricID)
	}
}

func TestDashboard_BadFabricIDIs400(t *testing.T) {
	req := httptest.NewRequest("GET", "/dns/dashboard?fabric_id=not-a-uuid", nil)
	p := auth.Principal{Capabilities: []string{"*"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountDashboard(&fakeDashboardQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestDashboard_BadMinutesIs400(t *testing.T) {
	// minutes=2 below the 5..1440 range.
	req := httptest.NewRequest("GET", "/dns/dashboard?minutes=2", nil)
	p := auth.Principal{Capabilities: []string{"*"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountDashboard(&fakeDashboardQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for minutes=2", rec.Code)
	}
}

func TestDashboard_RequiresReadCap(t *testing.T) {
	req := httptest.NewRequest("GET", "/dns/dashboard", nil)
	p := auth.Principal{Capabilities: []string{"dns:zones:read"}} // wrong cap
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountDashboard(&fakeDashboardQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestDashboard_FabricScopeEmptyServersShortCircuits(t *testing.T) {
	// fabric_id set + no matching servers → empty dashboard, no
	// samples query.
	fid := uuid.New()
	f := &fakeDashboardQ{} // servers list is empty
	req := httptest.NewRequest("GET", "/dns/dashboard?fabric_id="+fid.String(), nil)
	p := auth.Principal{Capabilities: []string{"*"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountDashboard(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp dashboardResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Overall.ServersTotal != 0 {
		t.Errorf("ServersTotal should be 0 in empty scope")
	}
}

func TestDashboard_ServerHealthShape(t *testing.T) {
	srvID := uuid.New()
	site := uuid.New()
	status := "ok"
	now := time.Now()
	f := &fakeDashboardQ{
		servers: []dbq.DnsServerForDashboardRow{
			{ID: srvID, Name: "srv-1", Role: "auth", FabricID: uuid.New(),
				SiteID: &site, LastRenderStatus: &status, LastRenderAt: &now},
		},
		samples: []dbq.DnsMetricsSampleRow{
			{ServerID: srvID, IntervalSeconds: 30, Queries: 600, ObservedAt: time.Now()},
		},
	}
	req := httptest.NewRequest("GET", "/dns/dashboard", nil)
	p := auth.Principal{Capabilities: []string{"*"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountDashboard(f).ServeHTTP(rec, req)
	var resp dashboardResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if len(resp.ServerHealth) != 1 || resp.ServerHealth[0].Name != "srv-1" {
		t.Errorf("server_health = %+v", resp.ServerHealth)
	}
	if resp.ServerHealth[0].QpsNow == nil || *resp.ServerHealth[0].QpsNow != 20.0 {
		t.Errorf("QpsNow = %v, want 20.0", resp.ServerHealth[0].QpsNow)
	}
}
