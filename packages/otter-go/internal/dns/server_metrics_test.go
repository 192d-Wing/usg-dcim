// PR 78 — handler tests for /dns/servers/{id}/metrics POST + GET.
package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

type fakeMetricsQ struct {
	fakeQ
	serverErr   error
	gotCreate   dbq.CreateDnsServerMetricsSampleParams
	listOut     []dbq.DnsServerMetricsSample
	gotListID   uuid.UUID
	gotCutoff   time.Time
}

func (f *fakeMetricsQ) GetDnsServer(_ context.Context, id uuid.UUID) (dbq.GetDnsServerRow, error) {
	if f.serverErr != nil {
		return dbq.GetDnsServerRow{}, f.serverErr
	}
	return dbq.GetDnsServerRow{ID: id}, nil
}

func (f *fakeMetricsQ) CreateDnsServerMetricsSample(_ context.Context, a dbq.CreateDnsServerMetricsSampleParams) (dbq.DnsServerMetricsSample, error) {
	f.gotCreate = a
	return dbq.DnsServerMetricsSample{
		ID: uuid.New(), ServerID: a.ServerID,
		IntervalSeconds: a.IntervalSeconds,
		Queries:         a.Queries,
		TopNames:        a.TopNames,
	}, nil
}

func (f *fakeMetricsQ) ListDnsServerMetricsSamples(_ context.Context, a dbq.ListDnsServerMetricsSamplesParams) ([]dbq.DnsServerMetricsSample, error) {
	f.gotListID = a.ServerID
	f.gotCutoff = a.Cutoff
	return f.listOut, nil
}

func mountMetrics(f *fakeMetricsQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}

func authedMetrics(method, path string, body []byte) *http.Request {
	var rdr *bytes.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	p := auth.Principal{Capabilities: []string{"*"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	return req
}

// ---- POST /metrics ----

func TestPostServerMetrics_HappyPath(t *testing.T) {
	id := uuid.New()
	f := &fakeMetricsQ{}
	body, _ := json.Marshal(map[string]any{
		"interval_seconds": 30,
		"queries":          1234,
		"nxdomain":         12,
		"servfail":         3,
		"noerror":          1219,
		"p50_ms":           1.5,
		"p95_ms":           8.2,
	})
	req := authedMetrics("POST", "/dns/servers/"+id.String()+"/metrics", body)
	rec := httptest.NewRecorder()
	mountMetrics(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if f.gotCreate.ServerID != id {
		t.Errorf("server_id = %s", f.gotCreate.ServerID)
	}
	if f.gotCreate.Queries != 1234 {
		t.Errorf("queries = %d", f.gotCreate.Queries)
	}
	if f.gotCreate.P50Ms == nil || *f.gotCreate.P50Ms != 1.5 {
		t.Errorf("p50_ms = %v", f.gotCreate.P50Ms)
	}
}

func TestPostServerMetrics_TopNamesForwarded(t *testing.T) {
	id := uuid.New()
	f := &fakeMetricsQ{}
	body, _ := json.Marshal(map[string]any{
		"interval_seconds": 30,
		"top_names": []map[string]any{
			{"name": "example.com", "type": "A", "count": 100},
		},
	})
	req := authedMetrics("POST", "/dns/servers/"+id.String()+"/metrics", body)
	rec := httptest.NewRecorder()
	mountMetrics(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if len(f.gotCreate.TopNames) == 0 {
		t.Errorf("top_names not forwarded")
	}
}

func TestPostServerMetrics_NullTopNamesStaysNull(t *testing.T) {
	// Collector that hasn't wired dnstap omits top_names → DB
	// column stays NULL (vs. empty array which is "observed zero").
	id := uuid.New()
	f := &fakeMetricsQ{}
	body, _ := json.Marshal(map[string]any{"interval_seconds": 30})
	req := authedMetrics("POST", "/dns/servers/"+id.String()+"/metrics", body)
	rec := httptest.NewRecorder()
	mountMetrics(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d", rec.Code)
	}
	if f.gotCreate.TopNames != nil {
		t.Errorf("TopNames should be nil for absent field, got %s", string(f.gotCreate.TopNames))
	}
}

func TestPostServerMetrics_EmptyTopNamesArrayIsDistinctFromNull(t *testing.T) {
	id := uuid.New()
	f := &fakeMetricsQ{}
	body := []byte(`{"interval_seconds": 30, "top_names": []}`)
	req := authedMetrics("POST", "/dns/servers/"+id.String()+"/metrics", body)
	rec := httptest.NewRecorder()
	mountMetrics(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d", rec.Code)
	}
	// Empty array should serialize as "[]" (not NULL).
	if string(f.gotCreate.TopNames) != "[]" {
		t.Errorf("empty array got %q, want \"[]\"", string(f.gotCreate.TopNames))
	}
}

func TestPostServerMetrics_ObservedAtOptional(t *testing.T) {
	// observed_at omitted → SQL COALESCE defaults to NOW(); handler
	// passes nil through.
	id := uuid.New()
	f := &fakeMetricsQ{}
	body, _ := json.Marshal(map[string]any{"interval_seconds": 30})
	req := authedMetrics("POST", "/dns/servers/"+id.String()+"/metrics", body)
	rec := httptest.NewRecorder()
	mountMetrics(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d", rec.Code)
	}
	if f.gotCreate.ObservedAt != nil {
		t.Errorf("ObservedAt should be nil for absent field")
	}
}

func TestPostServerMetrics_BadIntervalIs400(t *testing.T) {
	id := uuid.New()
	body, _ := json.Marshal(map[string]any{"interval_seconds": 0})
	req := authedMetrics("POST", "/dns/servers/"+id.String()+"/metrics", body)
	rec := httptest.NewRecorder()
	mountMetrics(&fakeMetricsQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestPostServerMetrics_NegativeCounterIs400(t *testing.T) {
	id := uuid.New()
	body, _ := json.Marshal(map[string]any{"interval_seconds": 30, "queries": -1})
	req := authedMetrics("POST", "/dns/servers/"+id.String()+"/metrics", body)
	rec := httptest.NewRecorder()
	mountMetrics(&fakeMetricsQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestPostServerMetrics_ServerNotFoundIs404(t *testing.T) {
	id := uuid.New()
	body, _ := json.Marshal(map[string]any{"interval_seconds": 30})
	req := authedMetrics("POST", "/dns/servers/"+id.String()+"/metrics", body)
	rec := httptest.NewRecorder()
	mountMetrics(&fakeMetricsQ{serverErr: pgx.ErrNoRows}).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestPostServerMetrics_BadUUIDIs400(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"interval_seconds": 30})
	req := authedMetrics("POST", "/dns/servers/not-a-uuid/metrics", body)
	rec := httptest.NewRecorder()
	mountMetrics(&fakeMetricsQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestPostServerMetrics_RequiresUpdateCap(t *testing.T) {
	id := uuid.New()
	body, _ := json.Marshal(map[string]any{"interval_seconds": 30})
	req := httptest.NewRequest("POST", "/dns/servers/"+id.String()+"/metrics", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	p := auth.Principal{Capabilities: []string{"dns:servers:read"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountMetrics(&fakeMetricsQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

// ---- GET /metrics ----

func TestListServerMetrics_HappyPath(t *testing.T) {
	id := uuid.New()
	f := &fakeMetricsQ{
		listOut: []dbq.DnsServerMetricsSample{
			{ID: uuid.New(), ServerID: id, Queries: 100},
		},
	}
	req := authedMetrics("GET", "/dns/servers/"+id.String()+"/metrics", nil)
	rec := httptest.NewRecorder()
	mountMetrics(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out []dbq.DnsServerMetricsSample
	_ = json.NewDecoder(rec.Body).Decode(&out)
	if len(out) != 1 || out[0].Queries != 100 {
		t.Errorf("got %+v", out)
	}
}

func TestListServerMetrics_DefaultsToOneHourWindow(t *testing.T) {
	id := uuid.New()
	f := &fakeMetricsQ{}
	before := time.Now().UTC().Add(-60 * time.Minute)
	req := authedMetrics("GET", "/dns/servers/"+id.String()+"/metrics", nil)
	rec := httptest.NewRecorder()
	mountMetrics(f).ServeHTTP(rec, req)
	_ = rec
	// Cutoff should be ~now-60m. Tolerance: 5 seconds for the test
	// runtime delta.
	gap := before.Sub(f.gotCutoff)
	if gap > 5*time.Second || gap < -5*time.Second {
		t.Errorf("cutoff %v not near expected %v (default 60m window)", f.gotCutoff, before)
	}
}

func TestListServerMetrics_CustomMinutesWindow(t *testing.T) {
	id := uuid.New()
	f := &fakeMetricsQ{}
	before := time.Now().UTC().Add(-15 * time.Minute)
	req := authedMetrics("GET", "/dns/servers/"+id.String()+"/metrics?minutes=15", nil)
	rec := httptest.NewRecorder()
	mountMetrics(f).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	gap := before.Sub(f.gotCutoff)
	if gap > 5*time.Second || gap < -5*time.Second {
		t.Errorf("cutoff %v not near expected %v (minutes=15)", f.gotCutoff, before)
	}
}

func TestListServerMetrics_RejectsMinutesOutOfRange(t *testing.T) {
	id := uuid.New()
	// minutes=0 below the 1..1440 range.
	req := authedMetrics("GET", "/dns/servers/"+id.String()+"/metrics?minutes=0", nil)
	rec := httptest.NewRecorder()
	mountMetrics(&fakeMetricsQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for minutes=0", rec.Code)
	}
	// minutes=1441 above the 24h cap.
	req = authedMetrics("GET", "/dns/servers/"+id.String()+"/metrics?minutes=1441", nil)
	rec = httptest.NewRecorder()
	mountMetrics(&fakeMetricsQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for minutes>1440", rec.Code)
	}
}

func TestListServerMetrics_ServerNotFoundIs404(t *testing.T) {
	id := uuid.New()
	req := authedMetrics("GET", "/dns/servers/"+id.String()+"/metrics", nil)
	rec := httptest.NewRecorder()
	mountMetrics(&fakeMetricsQ{serverErr: pgx.ErrNoRows}).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestListServerMetrics_BadUUIDIs400(t *testing.T) {
	req := authedMetrics("GET", "/dns/servers/not-a-uuid/metrics", nil)
	rec := httptest.NewRecorder()
	mountMetrics(&fakeMetricsQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestListServerMetrics_RequiresReadCap(t *testing.T) {
	id := uuid.New()
	req := httptest.NewRequest("GET", "/dns/servers/"+id.String()+"/metrics", nil)
	p := auth.Principal{Capabilities: []string{"dns:zones:read"}}
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	mountMetrics(&fakeMetricsQ{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}
