package dashboards

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

type fakeQ struct {
	siteTotal, siteActive  int64
	rackTotal              int64
	critSites              int64
	healthyColl, staleColl int64
	staleSources           int64

	siteParams      []dbq.CountSitesParams
	rackParams      []dbq.CountRacksParams
	gotStaleBefore  time.Time
	siteErr         error
	staleCollectErr error
}

func (f *fakeQ) CountSites(_ context.Context, a dbq.CountSitesParams) (int64, error) {
	f.siteParams = append(f.siteParams, a)
	if f.siteErr != nil {
		return 0, f.siteErr
	}
	// First call is global (zero params), second is active-state.
	if a.LifecycleState != nil && *a.LifecycleState == "active" {
		return f.siteActive, nil
	}
	return f.siteTotal, nil
}
func (f *fakeQ) CountRacks(_ context.Context, a dbq.CountRacksParams) (int64, error) {
	f.rackParams = append(f.rackParams, a)
	return f.rackTotal, nil
}
func (f *fakeQ) CountSitesWithCriticalAlerts(_ context.Context) (int64, error) {
	return f.critSites, nil
}
func (f *fakeQ) CountHealthyCollectors(_ context.Context) (int64, error) {
	return f.healthyColl, nil
}
func (f *fakeQ) CountStaleCollectors(_ context.Context, before time.Time) (int64, error) {
	f.gotStaleBefore = before
	return f.staleColl, f.staleCollectErr
}
func (f *fakeQ) CountStaleTelemetrySources(_ context.Context) (int64, error) {
	return f.staleSources, nil
}

func mount(f *fakeQ, staleSeconds int) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f, CollectorStaleSeconds: staleSeconds}).Mount(r)
	return r
}

func TestEnterprise_HappyPath(t *testing.T) {
	f := &fakeQ{
		siteTotal:    42,
		siteActive:   30,
		rackTotal:    100,
		critSites:    3,
		healthyColl:  8,
		staleColl:    2,
		staleSources: 7,
	}
	rec := authtest.ServeRequest(
		mount(f, 600),
		authtest.PrincipalWithCaps("dashboards:dashboards:read"),
		"GET", "/dashboards/enterprise", nil,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body enterpriseOverview
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Sites.Total != 42 || body.Sites.Active != 30 {
		t.Errorf("sites = %+v", body.Sites)
	}
	if body.Racks.Total != 100 {
		t.Errorf("racks = %+v", body.Racks)
	}
	if body.Alerts.SitesWithCritical != 3 {
		t.Errorf("alerts.sites_with_critical = %d", body.Alerts.SitesWithCritical)
	}
	if body.Collectors.Healthy != 8 || body.Collectors.Stale != 2 {
		t.Errorf("collectors = %+v", body.Collectors)
	}
	if body.Telemetry.StaleSources != 7 {
		t.Errorf("telemetry.stale_sources = %d", body.Telemetry.StaleSources)
	}
	if body.GeneratedAt == "" {
		t.Error("generated_at should be populated")
	}
	if _, err := time.Parse(time.RFC3339Nano, body.GeneratedAt); err != nil {
		t.Errorf("generated_at not RFC3339Nano: %q (%v)", body.GeneratedAt, err)
	}
}

// The first CountSites call is global (empty params); the second
// asks for LifecycleState=active. A regression that swapped them
// would still pass the happy-path test because the fake returns
// based on the LifecycleState pointer, so pin the param shape
// explicitly.
func TestEnterprise_CountSitesParamShape(t *testing.T) {
	f := &fakeQ{siteTotal: 10, siteActive: 5}
	rec := authtest.ServeRequest(
		mount(f, 600),
		authtest.PrincipalWithCaps("dashboards:dashboards:read"),
		"GET", "/dashboards/enterprise", nil,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(f.siteParams) != 2 {
		t.Fatalf("expected 2 CountSites calls; got %d", len(f.siteParams))
	}
	if f.siteParams[0].LifecycleState != nil {
		t.Errorf("first CountSites call should be global; got LifecycleState=%v", *f.siteParams[0].LifecycleState)
	}
	if f.siteParams[1].LifecycleState == nil || *f.siteParams[1].LifecycleState != "active" {
		t.Errorf("second CountSites call should filter by active; got %+v", f.siteParams[1])
	}
}

// CountRacks is invoked once with a zero-value params struct (global
// unscoped count) — mirrors the Python COUNT(Rack.id).
func TestEnterprise_CountRacksZeroParams(t *testing.T) {
	f := &fakeQ{}
	rec := authtest.ServeRequest(
		mount(f, 600),
		authtest.PrincipalWithCaps("dashboards:dashboards:read"),
		"GET", "/dashboards/enterprise", nil,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(f.rackParams) != 1 {
		t.Fatalf("expected 1 CountRacks call; got %d", len(f.rackParams))
	}
	got := f.rackParams[0]
	if got.SiteID != nil || got.RowID != nil || got.ScopeSiteIds != nil {
		t.Errorf("rack params should be zero-value; got %+v", got)
	}
}

// The stale-before threshold passed to CountStaleCollectors must be
// `now - CollectorStaleSeconds`. Verify by using a custom stale value
// and checking the threshold is within a small window of `now - X`.
func TestEnterprise_StaleThresholdHonorsConfig(t *testing.T) {
	f := &fakeQ{}
	rec := authtest.ServeRequest(
		mount(f, 123),
		authtest.PrincipalWithCaps("dashboards:dashboards:read"),
		"GET", "/dashboards/enterprise", nil,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	want := time.Now().UTC().Add(-123 * time.Second)
	delta := want.Sub(f.gotStaleBefore)
	if delta < 0 {
		delta = -delta
	}
	// Allow 5 seconds wall-clock skew (test harness, GC pauses).
	if delta > 5*time.Second {
		t.Errorf("stale threshold = %v, want ~%v (delta %v)", f.gotStaleBefore, want, delta)
	}
}

func TestEnterprise_RejectsWithoutCap(t *testing.T) {
	rec := authtest.ServeRequest(
		mount(&fakeQ{}, 600),
		authtest.PrincipalWithCaps("inventory:sites:read"),
		"GET", "/dashboards/enterprise", nil,
	)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// A DB error on any underlying query short-circuits with a 5xx and
// no partial result on the wire.
func TestEnterprise_DBErrorIs500(t *testing.T) {
	f := &fakeQ{staleCollectErr: errFake}
	rec := authtest.ServeRequest(
		mount(f, 600),
		authtest.PrincipalWithCaps("dashboards:dashboards:read"),
		"GET", "/dashboards/enterprise", nil,
	)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// Sentinel for the DB-error test. Defined as a package-level var so
// the test doesn't depend on a specific error type matching whatever
// httpx.Mapped flattens.
var errFake = stubError("db boom")

type stubError string

func (s stubError) Error() string { return string(s) }
