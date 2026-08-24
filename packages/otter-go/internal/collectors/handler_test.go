package collectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

type fakeQ struct {
	last       dbq.ListCollectorsParams
	getCalls   int
	getMissing bool
	enroll     dbq.EnrollCollectorParams
	heartbeat  dbq.HeartbeatCollectorParams
	hbRow      dbq.InsertCollectorHeartbeatParams
	hbOverride []byte
}

// fakeAudit captures audit.Record calls so tests can assert on shape.
type fakeAudit struct {
	calls []dbq.InsertAuditLogParams
}

func (a *fakeAudit) InsertAuditLog(_ context.Context, p dbq.InsertAuditLogParams) error {
	a.calls = append(a.calls, p)
	return nil
}

func (f *fakeQ) ListCollectors(_ context.Context, a dbq.ListCollectorsParams) ([]dbq.ListCollectorsRow, error) {
	f.last = a
	return nil, nil
}
func (f *fakeQ) CountCollectors(_ context.Context, _ dbq.CountCollectorsParams) (int64, error) {
	return 0, nil
}
func (f *fakeQ) GetCollector(_ context.Context, id uuid.UUID) (dbq.GetCollectorRow, error) {
	f.getCalls++
	if f.getMissing {
		return dbq.GetCollectorRow{}, pgx.ErrNoRows
	}
	return dbq.GetCollectorRow{ID: id}, nil
}
func (f *fakeQ) EnrollCollector(_ context.Context, a dbq.EnrollCollectorParams) (dbq.EnrollCollectorRow, error) {
	f.enroll = a
	return dbq.EnrollCollectorRow{ID: uuid.New(), SiteID: a.SiteID}, nil
}
func (f *fakeQ) HeartbeatCollector(_ context.Context, a dbq.HeartbeatCollectorParams) (json.RawMessage, error) {
	f.heartbeat = a
	if f.hbOverride == nil {
		return json.RawMessage("{}"), nil
	}
	return json.RawMessage(f.hbOverride), nil
}
func (f *fakeQ) InsertCollectorHeartbeat(_ context.Context, a dbq.InsertCollectorHeartbeatParams) error {
	f.hbRow = a
	return nil
}
func (f *fakeQ) SetCollectorConfigOverrides(_ context.Context, a dbq.SetCollectorConfigOverridesParams) (dbq.SetCollectorConfigOverridesRow, error) {
	return dbq.SetCollectorConfigOverridesRow{ID: a.ID, ConfigOverrides: a.ConfigOverrides}, nil
}
func (f *fakeQ) SetCollectorEnabled(_ context.Context, a dbq.SetCollectorEnabledParams) (dbq.SetCollectorEnabledRow, error) {
	return dbq.SetCollectorEnabledRow{ID: a.ID, Enabled: a.Enabled}, nil
}
func (f *fakeQ) DecommissionCollector(_ context.Context, id uuid.UUID) (dbq.DecommissionCollectorRow, error) {
	return dbq.DecommissionCollectorRow{ID: id, Status: "decommissioned"}, nil
}

// site-scope dependency stubs — global scope, no DB lookups.
func (f *fakeQ) GetSiteRegionID(_ context.Context, _ uuid.UUID) (uuid.UUID, error) {
	return uuid.Nil, pgx.ErrNoRows
}
func (f *fakeQ) GetSiteOrganizationID(_ context.Context, _ uuid.UUID) (*uuid.UUID, error) {
	return nil, nil
}
func (f *fakeQ) ListSiteGroupIDsForSite(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

func mount(f *fakeQ) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f}).Mount(r)
	return r
}
func mountWithAudit(f *fakeQ, a *fakeAudit) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f, Audit: a}).Mount(r)
	return r
}
func do(t *testing.T, h http.Handler, p string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", p, nil)
	ctx := auth.WithPrincipal(req.Context(), auth.Principal{Subject: uuid.New(), Capabilities: []string{"*"}})
	h.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}
func doBody(t *testing.T, h http.Handler, method, p, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, p, strings.NewReader(body))
	ctx := auth.WithPrincipal(req.Context(), auth.Principal{Subject: uuid.New(), Capabilities: []string{"*"}})
	h.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

func TestList_FiltersThreaded(t *testing.T) {
	sid := uuid.New()
	f := &fakeQ{}
	rec := do(t, mount(f), "/collectors/?site_id="+sid.String()+"&status=healthy&page_size=10")
	if rec.Code != 200 {
		t.Fatalf("got %d", rec.Code)
	}
	if f.last.SiteID == nil || *f.last.SiteID != sid {
		t.Error("site_id not threaded")
	}
	if f.last.Status == nil || *f.last.Status != "healthy" {
		t.Error("status not threaded")
	}
	if f.last.Limit != 10 {
		t.Errorf("page_size alias: want 10, got %d", f.last.Limit)
	}
}

func TestList_BadSiteUUID(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/collectors/?site_id=x")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}

func TestGet_NotFound(t *testing.T) {
	rec := do(t, mount(&fakeQ{getMissing: true}), "/collectors/"+uuid.New().String())
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}

// TestRouteCapabilityCodes locks the catalog cap names onto each
// route. A revert that swaps a code to a non-catalog name fails
// here. Specifically guards enroll (collectors:collectors:enroll)
// and heartbeat (collectors:ingest:write).
func TestRouteCapabilityCodes(t *testing.T) {
	sid := uuid.New().String()
	cid := uuid.New().String()
	cases := []struct{ method, path, required string }{
		{"POST", "/collectors/enroll", "collectors:collectors:enroll"},
		{"POST", "/collectors/" + cid + "/heartbeat", "collectors:ingest:write"},
		{"PATCH", "/collectors/" + cid + "/config", "collectors:collectors:update"},
		{"PATCH", "/collectors/" + cid + "/enabled", "collectors:collectors:update"},
		{"DELETE", "/collectors/" + cid, "collectors:collectors:update"},
	}
	body := `{"site_id":"` + sid + `","name":"x"}`
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.required, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(body))
			ctx := auth.WithPrincipal(req.Context(), auth.Principal{Subject: uuid.New(), Capabilities: []string{"unrelated"}})
			mount(&fakeQ{}).ServeHTTP(rec, req.WithContext(ctx))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s without %s: got %d (want 403)", tc.method, tc.path, tc.required, rec.Code)
			}
			rec = httptest.NewRecorder()
			req = httptest.NewRequest(tc.method, tc.path, strings.NewReader(body))
			ctx = auth.WithPrincipal(req.Context(), auth.Principal{Subject: uuid.New(), Capabilities: []string{tc.required}})
			mount(&fakeQ{}).ServeHTTP(rec, req.WithContext(ctx))
			if rec.Code == http.StatusForbidden {
				t.Fatalf("%s %s with %s: got 403 (cap gate should pass)", tc.method, tc.path, tc.required)
			}
		})
	}
}

func TestEnroll_OK(t *testing.T) {
	f := &fakeQ{}
	a := &fakeAudit{}
	sid := uuid.New()
	rec := doBody(t, mountWithAudit(f, a), "POST", "/collectors/enroll",
		`{"site_id":"`+sid.String()+`","name":"agent-1","capabilities":["dns","power"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	var got enrollOut
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(got.EnrollmentToken, "enroll_") {
		t.Errorf("token prefix: %q", got.EnrollmentToken)
	}
	if got.ExpiresInSeconds != 3600 {
		t.Errorf("expires_in_seconds: got %d, want 3600", got.ExpiresInSeconds)
	}
	if f.enroll.SiteID != sid || f.enroll.Name != "agent-1" {
		t.Errorf("enroll args wrong: %+v", f.enroll)
	}
	if f.enroll.EnrollmentTokenHash == "" {
		t.Error("enrollment_token_hash empty")
	}
	if string(f.enroll.CapabilitiesJson) != `["dns","power"]` {
		t.Errorf("capabilities JSON: %q", string(f.enroll.CapabilitiesJson))
	}
	// Audit row is the security record of who enrolled the collector.
	// A refactor that drops the audit.Record call would silently break
	// the compliance story; this assertion catches it.
	if len(a.calls) != 1 {
		t.Fatalf("audit calls: got %d, want 1", len(a.calls))
	}
	if a.calls[0].Action != "collector.enroll" {
		t.Errorf("audit action: got %q, want collector.enroll", a.calls[0].Action)
	}
	if a.calls[0].TargetType == nil || *a.calls[0].TargetType != "collector" {
		t.Errorf("audit target_type: got %v", a.calls[0].TargetType)
	}
	if a.calls[0].SiteID == nil || *a.calls[0].SiteID != sid {
		t.Errorf("audit site_id: got %v, want %v", a.calls[0].SiteID, sid)
	}
}

func TestEnroll_MissingSiteID(t *testing.T) {
	rec := doBody(t, mount(&fakeQ{}), "POST", "/collectors/enroll", `{"name":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 missing site_id", rec.Code)
	}
}

// TestEnroll_EmptyNameAccepted preserves Python parity — Python's
// CollectorEnroll has no min_length on `name`, so "" is accepted.
func TestEnroll_EmptyNameAccepted(t *testing.T) {
	f := &fakeQ{}
	a := &fakeAudit{}
	sid := uuid.New()
	rec := doBody(t, mountWithAudit(f, a), "POST", "/collectors/enroll",
		`{"site_id":"`+sid.String()+`","name":""}`)
	if rec.Code != http.StatusOK {
		t.Errorf("got %d %s, want 200 (Python accepts empty name)", rec.Code, rec.Body.String())
	}
}

func TestEnroll_TokenIsRandom(t *testing.T) {
	// Two enrolls should not produce the same token. Sanity check on the random source.
	f1, f2 := &fakeQ{}, &fakeQ{}
	body := `{"site_id":"` + uuid.New().String() + `","name":"x"}`
	rec1 := doBody(t, mount(f1), "POST", "/collectors/enroll", body)
	rec2 := doBody(t, mount(f2), "POST", "/collectors/enroll", body)
	var o1, o2 enrollOut
	_ = json.Unmarshal(rec1.Body.Bytes(), &o1)
	_ = json.Unmarshal(rec2.Body.Bytes(), &o2)
	if o1.EnrollmentToken == o2.EnrollmentToken {
		t.Fatal("two enrollments produced the same token")
	}
}

func TestHeartbeat_OK_Healthy(t *testing.T) {
	f := &fakeQ{hbOverride: []byte(`{"dns_metrics_interval_seconds":30}`)}
	cid := uuid.New().String()
	rec := doBody(t, mount(f), "POST", "/collectors/"+cid+"/heartbeat",
		`{"buffered_samples":5,"version":"v1.2","metrics":{"cpu":0.4}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if f.heartbeat.Status != "healthy" {
		t.Errorf("status: got %q, want healthy", f.heartbeat.Status)
	}
	if f.heartbeat.Version == nil || *f.heartbeat.Version != "v1.2" {
		t.Errorf("version not threaded: %v", f.heartbeat.Version)
	}
	if f.heartbeat.BufferedSamples != 5 {
		t.Errorf("buffered_samples: got %d, want 5", f.heartbeat.BufferedSamples)
	}
	var out heartbeatOut
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if !out.OK {
		t.Error("ok false")
	}
	if out.ReceivedAt.IsZero() {
		t.Error("received_at not set in response")
	}
	if out.ConfigOverrides.DNSMetricsIntervalSeconds == nil || *out.ConfigOverrides.DNSMetricsIntervalSeconds != 30 {
		t.Errorf("config_overrides not echoed: %+v", out.ConfigOverrides)
	}
}

// TestHeartbeat_EmptyVersionPreservesStored guards the truthy-check
// parity with Python's `if payload.version`. An empty string must
// not overwrite the stored version — the handler nils it out and
// the SQL COALESCE preserves whatever's already in the row.
func TestHeartbeat_EmptyVersionPreservesStored(t *testing.T) {
	f := &fakeQ{}
	cid := uuid.New().String()
	rec := doBody(t, mount(f), "POST", "/collectors/"+cid+"/heartbeat",
		`{"buffered_samples":0,"version":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if f.heartbeat.Version != nil {
		t.Errorf("empty version should be nil-coalesced; got %q", *f.heartbeat.Version)
	}
}

func TestHeartbeat_DegradedOnLastError(t *testing.T) {
	f := &fakeQ{}
	cid := uuid.New().String()
	rec := doBody(t, mount(f), "POST", "/collectors/"+cid+"/heartbeat",
		`{"buffered_samples":0,"last_error":"redis disconnected"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if f.heartbeat.Status != "degraded" {
		t.Errorf("status: got %q, want degraded", f.heartbeat.Status)
	}
	if f.hbRow.LastError == nil || *f.hbRow.LastError != "redis disconnected" {
		t.Errorf("last_error not threaded into heartbeat row: %v", f.hbRow.LastError)
	}
}

func TestHeartbeat_NotFound(t *testing.T) {
	rec := doBody(t, mount(&fakeQ{getMissing: true}), "POST", "/collectors/"+uuid.New().String()+"/heartbeat", `{}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404", rec.Code)
	}
}

func TestGet_BadUUID(t *testing.T) {
	rec := do(t, mount(&fakeQ{}), "/collectors/not-a-uuid")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d", rec.Code)
	}
}
