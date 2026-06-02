package ingest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
)

type fakeQ struct {
	upserts []dbq.UpsertTelemetrySourceParams
	samples []dbq.InsertTelemetrySampleParams
	// failOn lets a test simulate a DB error on the Nth sample insert
	// (1-indexed) to exercise the errors:true path.
	failOnSample int
	// failOnUpsert simulates a freshness write failure on first call.
	failOnUpsert bool
}

func (f *fakeQ) UpsertTelemetrySource(_ context.Context, a dbq.UpsertTelemetrySourceParams) error {
	if f.failOnUpsert {
		return testErr("freshness boom")
	}
	f.upserts = append(f.upserts, a)
	return nil
}
func (f *fakeQ) InsertTelemetrySample(_ context.Context, a dbq.InsertTelemetrySampleParams) error {
	f.samples = append(f.samples, a)
	if f.failOnSample > 0 && len(f.samples) == f.failOnSample {
		return testErr("sample boom")
	}
	return nil
}

type testErr string

func (e testErr) Error() string { return string(e) }

func mount(f *fakeQ, writeHyper bool) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: f, WriteHypertable: writeHyper}).Mount(r)
	return r
}

func post(t *testing.T, h http.Handler, body string, caps []string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/ingest/telemetry", strings.NewReader(body))
	ctx := auth.WithPrincipal(req.Context(), auth.Principal{Subject: uuid.New(), Capabilities: caps})
	h.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

func validBody(t *testing.T) (string, uuid.UUID, uuid.UUID, []uuid.UUID) {
	t.Helper()
	sid := uuid.New()
	cid := uuid.New()
	a1 := uuid.New()
	a2 := uuid.New()
	body := `{
		"batch_id": "batch-0001",
		"site_id": "` + sid.String() + `",
		"collector_id": "` + cid.String() + `",
		"samples": [
			{"asset_id":"` + a1.String() + `","metric":"cpu","value":0.4,"unit":"frac","ts":"2026-06-01T00:00:00Z"},
			{"asset_id":"` + a2.String() + `","metric":"temp","value":21.5,"unit":"C","ts":"2026-06-01T00:00:05Z","tags":{"room":"r1"}}
		]
	}`
	return body, sid, cid, []uuid.UUID{a1, a2}
}

func TestRouteCapability_Forbidden(t *testing.T) {
	body, _, _, _ := validBody(t)
	rec := post(t, mount(&fakeQ{}, true), body, []string{"unrelated"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 without collectors:ingest:write", rec.Code)
	}
}

func TestRouteCapability_Permitted(t *testing.T) {
	body, _, _, _ := validBody(t)
	rec := post(t, mount(&fakeQ{}, true), body, []string{capIngestWrite})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s, want 200 with collectors:ingest:write", rec.Code, rec.Body.String())
	}
}

func TestIngest_OK_HypertableWritten(t *testing.T) {
	f := &fakeQ{}
	body, _, _, assets := validBody(t)
	rec := post(t, mount(f, true), body, []string{"*"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	var out ingestOut
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Accepted != 2 {
		t.Errorf("accepted: got %d, want 2", out.Accepted)
	}
	if out.Errors {
		t.Error("errors:true on a clean batch")
	}
	if out.ReceivedAt == "" {
		t.Error("received_at empty")
	}
	if len(f.upserts) != 2 {
		t.Errorf("freshness upserts: got %d, want 2", len(f.upserts))
	}
	if len(f.samples) != 2 {
		t.Errorf("sample inserts: got %d, want 2", len(f.samples))
	}
	// seq is 0-indexed and reflects sample position in the batch.
	if f.samples[0].Seq != 0 || f.samples[1].Seq != 1 {
		t.Errorf("seq not 0/1: %d/%d", f.samples[0].Seq, f.samples[1].Seq)
	}
	// Both assets reachable via freshness.
	seen := map[uuid.UUID]bool{}
	for _, u := range f.upserts {
		seen[u.AssetID] = true
	}
	for _, a := range assets {
		if !seen[a] {
			t.Errorf("missing freshness upsert for asset %s", a)
		}
	}
}

// Python's _update_freshness dedups by (asset_id, metric) keeping
// the LAST sample. Same input fed twice for one (asset, metric)
// should issue ONE upsert with the later value.
func TestIngest_FreshnessDedupKeepsLast(t *testing.T) {
	f := &fakeQ{}
	sid := uuid.New()
	cid := uuid.New()
	aid := uuid.New()
	body := `{
		"batch_id": "batch-0001",
		"site_id": "` + sid.String() + `",
		"collector_id": "` + cid.String() + `",
		"samples": [
			{"asset_id":"` + aid.String() + `","metric":"cpu","value":0.1,"ts":"2026-06-01T00:00:00Z"},
			{"asset_id":"` + aid.String() + `","metric":"cpu","value":0.9,"ts":"2026-06-01T00:00:05Z"}
		]
	}`
	rec := post(t, mount(f, true), body, []string{"*"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if len(f.upserts) != 1 {
		t.Fatalf("freshness upserts: got %d, want 1 (dedup by asset+metric)", len(f.upserts))
	}
	if f.upserts[0].LastValue != 0.9 {
		t.Errorf("freshness LAST sample: got %v, want 0.9", f.upserts[0].LastValue)
	}
	// Both samples still INSERTed (dedup is the freshness-only thing).
	if len(f.samples) != 2 {
		t.Errorf("samples: got %d, want 2", len(f.samples))
	}
}

func TestIngest_WriteHypertableFalse_SkipsSamples(t *testing.T) {
	f := &fakeQ{}
	body, _, _, _ := validBody(t)
	rec := post(t, mount(f, false), body, []string{"*"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	var out ingestOut
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Accepted != 2 {
		t.Errorf("accepted: got %d, want 2 (freshness still counts)", out.Accepted)
	}
	if !out.Errors {
		t.Error("WriteHypertable=false → errors:true (matches Python _write_hypertable returning False)")
	}
	if len(f.upserts) != 2 {
		t.Errorf("freshness still updates: got %d, want 2", len(f.upserts))
	}
	if len(f.samples) != 0 {
		t.Errorf("samples should NOT be written when WriteHypertable=false; got %d", len(f.samples))
	}
}

func TestIngest_SampleInsertFails_ErrorsTrue(t *testing.T) {
	f := &fakeQ{failOnSample: 2}
	body, _, _, _ := validBody(t)
	rec := post(t, mount(f, true), body, []string{"*"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	var out ingestOut
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if !out.Errors {
		t.Error("sample insert error must surface as errors:true")
	}
}

func TestIngest_FreshnessFails_5xx(t *testing.T) {
	f := &fakeQ{failOnUpsert: true}
	body, _, _, _ := validBody(t)
	rec := post(t, mount(f, true), body, []string{"*"})
	if rec.Code < 500 {
		t.Errorf("got %d, want 5xx — freshness failure should not silently 200", rec.Code)
	}
}

func TestIngest_Validation_BatchIDTooShort(t *testing.T) {
	body := `{"batch_id":"short","site_id":"` + uuid.New().String() + `","collector_id":"` + uuid.New().String() + `","samples":[{"asset_id":"` + uuid.New().String() + `","metric":"cpu","value":0,"ts":"2026-06-01T00:00:00Z"}]}`
	rec := post(t, mount(&fakeQ{}, true), body, []string{"*"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 for batch_id<8 chars", rec.Code)
	}
}

func TestIngest_Validation_NoSamples(t *testing.T) {
	body := `{"batch_id":"batch-0001","site_id":"` + uuid.New().String() + `","collector_id":"` + uuid.New().String() + `","samples":[]}`
	rec := post(t, mount(&fakeQ{}, true), body, []string{"*"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 for empty samples", rec.Code)
	}
}

// Per-sample required-field validation parity with Python's pydantic
// TelemetrySample. Without these checks, missing fields silently
// zero-fill — metric="" / ts=year-0001 / asset_id=uuid.Nil all
// corrupt downstream charts and freshness rows.
func TestIngest_Validation_SampleMissingMetric(t *testing.T) {
	body := `{"batch_id":"batch-0001","site_id":"` + uuid.New().String() + `","collector_id":"` + uuid.New().String() + `","samples":[{"asset_id":"` + uuid.New().String() + `","value":1.0,"ts":"2026-06-01T00:00:00Z"}]}`
	rec := post(t, mount(&fakeQ{}, true), body, []string{"*"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 for missing sample metric", rec.Code)
	}
}

func TestIngest_Validation_SampleMissingTS(t *testing.T) {
	body := `{"batch_id":"batch-0001","site_id":"` + uuid.New().String() + `","collector_id":"` + uuid.New().String() + `","samples":[{"asset_id":"` + uuid.New().String() + `","metric":"cpu","value":1.0}]}`
	rec := post(t, mount(&fakeQ{}, true), body, []string{"*"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 for missing sample ts (year-0001 corrupts the hypertable)", rec.Code)
	}
}

func TestIngest_Validation_SampleMissingAssetID(t *testing.T) {
	body := `{"batch_id":"batch-0001","site_id":"` + uuid.New().String() + `","collector_id":"` + uuid.New().String() + `","samples":[{"metric":"cpu","value":1.0,"ts":"2026-06-01T00:00:00Z"}]}`
	rec := post(t, mount(&fakeQ{}, true), body, []string{"*"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 for missing sample asset_id", rec.Code)
	}
}

func TestIngest_Validation_MissingSiteID(t *testing.T) {
	body := `{"batch_id":"batch-0001","collector_id":"` + uuid.New().String() + `","samples":[{"asset_id":"` + uuid.New().String() + `","metric":"cpu","value":0,"ts":"2026-06-01T00:00:00Z"}]}`
	rec := post(t, mount(&fakeQ{}, true), body, []string{"*"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 for missing site_id", rec.Code)
	}
}

// Python emits tags as JSONB; an omitted/null `tags` field should
// be sent as `{}` per services/telemetry.py:42 (`s.tags` is
// `dict[str, str] = Field(default_factory=dict)`).
func TestIngest_TagsDefaultEmptyObject(t *testing.T) {
	f := &fakeQ{}
	sid := uuid.New()
	cid := uuid.New()
	aid := uuid.New()
	body := `{
		"batch_id": "batch-0001",
		"site_id": "` + sid.String() + `",
		"collector_id": "` + cid.String() + `",
		"samples": [
			{"asset_id":"` + aid.String() + `","metric":"cpu","value":0.4,"ts":"2026-06-01T00:00:00Z"}
		]
	}`
	rec := post(t, mount(f, true), body, []string{"*"})
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if len(f.samples) != 1 {
		t.Fatalf("samples: got %d, want 1", len(f.samples))
	}
	if string(f.samples[0].Tags) != "{}" {
		t.Errorf("tags JSON: got %q, want %q (Python default_factory=dict)", string(f.samples[0].Tags), "{}")
	}
}
