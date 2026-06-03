package regiondeploy

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/vmihailenco/msgpack/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

// fakeArq records each EnqueueArqJob call so the start tests can
// assert the wire shape, queue name, args, and that no enqueue
// happened on 422 / 403 / 404 paths.
type fakeArq struct {
	calls   int
	lastQ   string
	lastFn  string
	lastArg []any
	jobID   string
	err     error
}

func (a *fakeArq) EnqueueArqJob(_ context.Context, queue, fn string, args []any) (string, error) {
	a.calls++
	a.lastQ = queue
	a.lastFn = fn
	a.lastArg = args
	if a.err != nil {
		return "", a.err
	}
	if a.jobID == "" {
		a.jobID = "stub-job-id"
	}
	return a.jobID, nil
}

// startFakeQ embeds fakeQ + records the start-side mutation params.
type startFakeQ struct {
	*fakeQ
	startRow   dbq.StartRegionDeploymentRow
	startErr   error
	startCalls int
}

func (s *startFakeQ) StartRegionDeployment(_ context.Context, _ uuid.UUID) (dbq.StartRegionDeploymentRow, error) {
	s.startCalls++
	return s.startRow, s.startErr
}

func mountStart(q Querier, audit *fakeAudit, arq ArqEnqueuer) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: q, Audit: audit, Arq: arq}).Mount(r)
	return r
}

// newStartFake builds a startFakeQ with the row a normal `pending`
// → `preflight` flip would produce: site_id set, updated=1.
func newStartFake(prior string, updated int64) *startFakeQ {
	sid := uuid.New()
	return &startFakeQ{
		fakeQ:    &fakeQ{getRow: dbq.RegionDeployment{ID: uuid.New(), SiteID: sid, Status: "preflight"}},
		startRow: dbq.StartRegionDeploymentRow{PriorStatus: prior, Updated: updated, SiteID: &sid},
	}
}

func TestStart_OK_200_EnqueuesArqJob_AndEmitsAudit(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	q := &startFakeQ{
		fakeQ:    &fakeQ{getRow: dbq.RegionDeployment{ID: id, SiteID: sid, Name: "edge-7", Status: "preflight"}},
		startRow: dbq.StartRegionDeploymentRow{PriorStatus: "pending", Updated: 1, SiteID: &sid},
	}
	a := &fakeAudit{}
	arq := &fakeArq{jobID: "abc123"}
	rec := doPost(t, mountStart(q, a, arq), wildcardP(), "/region-deployments/"+id.String()+"/start", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if q.startCalls != 1 {
		t.Errorf("expected one StartRegionDeployment call, got %d", q.startCalls)
	}
	if arq.calls != 1 {
		t.Fatalf("expected one arq enqueue, got %d", arq.calls)
	}
	if arq.lastQ != "arq:queue" {
		t.Errorf("default queue name should be arq:queue; got %q", arq.lastQ)
	}
	if arq.lastFn != "run_region_deploy" {
		t.Errorf("function name must match Python's WorkerSettings registration; got %q", arq.lastFn)
	}
	if len(arq.lastArg) != 1 || arq.lastArg[0] != id.String() {
		t.Errorf("args should be [deploymentID.String()]; got %+v", arq.lastArg)
	}
	if len(a.rows) != 1 || a.rows[0].Action != "region_deployment.start" {
		t.Errorf("audit row missing or wrong action; got %+v", a.rows)
	}
}

func TestStart_WrongStatus_422_NoEnqueue_NoAudit(t *testing.T) {
	// provisioning is NOT in the startable set (pending/failed/aborted).
	id, sid := uuid.New(), uuid.New()
	q := &startFakeQ{
		fakeQ:    &fakeQ{getRow: dbq.RegionDeployment{ID: id, SiteID: sid, Status: "provisioning"}},
		startRow: dbq.StartRegionDeploymentRow{PriorStatus: "provisioning", Updated: 0},
	}
	a := &fakeAudit{}
	arq := &fakeArq{}
	rec := doPost(t, mountStart(q, a, arq), wildcardP(), "/region-deployments/"+id.String()+"/start", nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("provisioning")) {
		t.Errorf("error must name prior status; body=%s", rec.Body.String())
	}
	if arq.calls != 0 {
		t.Errorf("422 must not enqueue arq job; calls=%d", arq.calls)
	}
	if len(a.rows) != 0 {
		t.Errorf("422 must not emit audit; got %d rows", len(a.rows))
	}
}

func TestStart_NoRow_404(t *testing.T) {
	q := &startFakeQ{fakeQ: &fakeQ{getErr: pgx.ErrNoRows}}
	rec := doPost(t, mountStart(q, &fakeAudit{}, &fakeArq{}), wildcardP(),
		"/region-deployments/"+uuid.New().String()+"/start", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestStart_BadID_400(t *testing.T) {
	rec := doPost(t, mountStart(&startFakeQ{fakeQ: &fakeQ{}}, &fakeAudit{}, &fakeArq{}), wildcardP(),
		"/region-deployments/not-a-uuid/start", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestStart_OutOfScope_403_NoEnqueue(t *testing.T) {
	id, sid, otherSite := uuid.New(), uuid.New(), uuid.New()
	q := &startFakeQ{fakeQ: &fakeQ{
		getRow: dbq.RegionDeployment{ID: id, SiteID: sid, Status: "pending"},
	}}
	scope := auth.Scope{SiteIDs: map[uuid.UUID]struct{}{otherSite: {}}}
	p := authtest.PrincipalWithScopes([]string{capStart}, map[string]auth.Scope{capStart: scope})
	arq := &fakeArq{}
	rec := doPost(t, mountStart(q, &fakeAudit{}, arq), p, "/region-deployments/"+id.String()+"/start", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	if arq.calls != 0 {
		t.Errorf("403 must not enqueue arq job; calls=%d", arq.calls)
	}
}

func TestStart_NoCap_403(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	q := &startFakeQ{fakeQ: &fakeQ{
		getRow: dbq.RegionDeployment{ID: id, SiteID: sid, Status: "pending"},
	}}
	p := authtest.PrincipalWithCaps(capRead)
	rec := doPost(t, mountStart(q, &fakeAudit{}, &fakeArq{}), p,
		"/region-deployments/"+id.String()+"/start", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestStart_NoArq_503(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	q := &startFakeQ{
		fakeQ:    &fakeQ{getRow: dbq.RegionDeployment{ID: id, SiteID: sid, Status: "pending"}},
		startRow: dbq.StartRegionDeploymentRow{PriorStatus: "pending", Updated: 1, SiteID: &sid},
	}
	a := &fakeAudit{}
	rec := doPost(t, mountStart(q, a, nil), wildcardP(),
		"/region-deployments/"+id.String()+"/start", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if q.startCalls != 0 {
		t.Errorf("503 must short-circuit before the DB write; startCalls=%d", q.startCalls)
	}
	if len(a.rows) != 0 {
		t.Errorf("503 must not emit audit; got %d rows", len(a.rows))
	}
}

func TestStart_EnqueueFails_RecordsErrorEvent(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	q := &startFakeQ{
		fakeQ:    &fakeQ{getRow: dbq.RegionDeployment{ID: id, SiteID: sid, Status: "pending"}},
		startRow: dbq.StartRegionDeploymentRow{PriorStatus: "pending", Updated: 1, SiteID: &sid},
	}
	arq := &fakeArq{err: errors.New("redis connection refused")}
	// Wrap q so we can capture CreateRegionDeploymentEvent calls.
	wrapped := &startEventCapture{startFakeQ: q}
	rec := doPost(t, mountStart(wrapped, &fakeAudit{}, arq), wildcardP(),
		"/region-deployments/"+id.String()+"/start", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
	if wrapped.eventCalls != 1 {
		t.Fatalf("expected one error event row, got %d", wrapped.eventCalls)
	}
	if wrapped.lastEvent.Level != "error" {
		t.Errorf("event level should be error; got %q", wrapped.lastEvent.Level)
	}
	if !bytes.Contains([]byte(wrapped.lastEvent.Message), []byte("redis connection refused")) {
		t.Errorf("event message should carry the enqueue error; got %q", wrapped.lastEvent.Message)
	}
}

type startEventCapture struct {
	*startFakeQ
	eventCalls int
	lastEvent  dbq.CreateRegionDeploymentEventParams
}

func (s *startEventCapture) CreateRegionDeploymentEvent(_ context.Context, a dbq.CreateRegionDeploymentEventParams) (dbq.RegionDeploymentEvent, error) {
	s.eventCalls++
	s.lastEvent = a
	return dbq.RegionDeploymentEvent{ID: 1, Stage: a.Stage, Level: a.Level, Message: a.Message}, nil
}

// ─── Arq wire-format unit tests ─────────────────────────────────────

func TestArqJobPayload_MsgpackMatchesPythonShape(t *testing.T) {
	// The Python worker's _msgpack_loads reads the exact 5-key dict
	// arq's serialize_job emits: `{t, f, a, k, et}`. Encoding the Go
	// struct + decoding into a Go map must round-trip those keys
	// byte-for-byte equivalent to what msgpack.unpackb in Python
	// produces. This test catches any field-tag drift that would
	// cause the Python worker to silently drop a job.
	payload := arqJobPayload{
		Try:         nil,
		Function:    "run_region_deploy",
		Args:        []any{"deadbeef-1234"},
		Kwargs:      map[string]any{},
		EnqueueTime: 1234567890,
	}
	encoded, err := msgpack.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := msgpack.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"t", "f", "a", "k", "et"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("missing key %q in decoded payload; got %+v", key, decoded)
		}
	}
	if decoded["f"] != "run_region_deploy" {
		t.Errorf("function key drift; got %v", decoded["f"])
	}
	if decoded["et"] != int64(1234567890) {
		t.Errorf("enqueue_time key drift; got %v (%T)", decoded["et"], decoded["et"])
	}
}

func TestNewArqJobID_HexEncodedSixteenBytes(t *testing.T) {
	// Python's arq generates job_id via uuid4().hex — 32 lowercase
	// hex chars, no dashes. Our newArqJobID must produce the same
	// shape so Redis SETEX `arq:job:<id>` keys collide / dedup
	// correctly with what the Python pool would have written.
	id, err := newArqJobID()
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 32 {
		t.Errorf("job_id must be 32 hex chars; got %d (%q)", len(id), id)
	}
	if _, err := hex.DecodeString(id); err != nil {
		t.Errorf("job_id must be valid hex; got %q (%v)", id, err)
	}
}
