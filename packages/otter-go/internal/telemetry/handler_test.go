package telemetry

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

type fakeQ struct {
	last dbq.GetTelemetrySeriesParams
	rows []dbq.TelemetryPoint
}

func (f *fakeQ) GetTelemetrySeries(_ context.Context, a dbq.GetTelemetrySeriesParams) ([]dbq.TelemetryPoint, error) {
	f.last = a
	return f.rows, nil
}

func mount(f *fakeQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}
// authedPrincipal carries the telemetry read cap so the test harness
// passes RequireCapability("telemetry:metrics:read") on every request.
// Tests that want to assert the cap gate works do so explicitly via
// TestSeries_RequiresCap below.
func authedPrincipal() auth.Principal {
	return auth.Principal{
		Capabilities: []string{"telemetry:metrics:read"},
		Label:        "test",
	}
}

func do(t *testing.T, h http.Handler, p string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", p, nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), authedPrincipal()))
	h.ServeHTTP(rec, req)
	return rec
}

func TestSeries_RequiresIDs(t *testing.T) {
	for _, p := range []string{
		"/telemetry/series",
		"/telemetry/series?site_id=x&asset_id=" + uuid.New().String() + "&metric=m",
		"/telemetry/series?site_id=" + uuid.New().String() + "&asset_id=x&metric=m",
		"/telemetry/series?site_id=" + uuid.New().String() + "&asset_id=" + uuid.New().String(),
	} {
		rec := do(t, mount(&fakeQ{}), p)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d", p, rec.Code)
		}
	}
}

func TestSeries_Defaults(t *testing.T) {
	sid, aid := uuid.New(), uuid.New()
	f := &fakeQ{rows: []dbq.TelemetryPoint{{TS: time.Now(), Value: 1.5}}}
	rec := do(t, mount(f), "/telemetry/series?site_id="+sid.String()+"&asset_id="+aid.String()+"&metric=temp_c")
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if f.last.Metric != "temp_c" {
		t.Errorf("metric: %q", f.last.Metric)
	}
	if f.last.End.Sub(f.last.Start) != time.Hour {
		t.Errorf("default window: got %v", f.last.End.Sub(f.last.Start))
	}
	var body seriesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Count != 1 || body.AssetID != aid.String() {
		t.Errorf("body: %+v", body)
	}
}

func TestSeries_ExplicitWindow(t *testing.T) {
	sid, aid := uuid.New(), uuid.New()
	f := &fakeQ{}
	start := "2026-05-15T00:00:00Z"
	end := "2026-05-15T12:00:00Z"
	rec := do(t, mount(f), "/telemetry/series?site_id="+sid.String()+"&asset_id="+aid.String()+"&metric=m&start="+start+"&end="+end)
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if f.last.Start.Format(time.RFC3339) != start || f.last.End.Format(time.RFC3339) != end {
		t.Errorf("window: %v..%v", f.last.Start, f.last.End)
	}
}

func TestSeries_BadTimestamps(t *testing.T) {
	sid, aid := uuid.New(), uuid.New()
	rec := do(t, mount(&fakeQ{}), "/telemetry/series?site_id="+sid.String()+"&asset_id="+aid.String()+"&metric=m&start=garbage")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}

// TestSeries_RejectsWithoutCap proves the RequireCapability gate
// fires. Without `telemetry:metrics:read` the request 403s before
// the handler runs (no query params parsed, no DB call).
func TestSeries_RejectsWithoutCap(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/telemetry/series?site_id="+uuid.New().String()+
		"&asset_id="+uuid.New().String()+"&metric=temp_c", nil)
	noCap := auth.Principal{Capabilities: []string{"some:other:cap"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), noCap))
	mount(&fakeQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}
