package regiondeploy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/auth/authtest"
)

// fakePubSub implements PubsubSubscriber for tests. msgs is the queue
// of messages the Channel() returns; the test publishes by appending
// to msgs and calling release().
type fakePubSub struct {
	ch chan *redis.Message
}

func newFakePubSub() *fakePubSub { return &fakePubSub{ch: make(chan *redis.Message, 16)} }

func (f *fakePubSub) Subscribe(_ context.Context, _ ...string) PubsubChannelCloser {
	return &fakePubSubChannel{ch: f.ch}
}

type fakePubSubChannel struct{ ch chan *redis.Message }

func (c *fakePubSubChannel) Channel(_ ...redis.ChannelOption) <-chan *redis.Message {
	return c.ch
}
func (c *fakePubSubChannel) Close() error { return nil }

// sseFakeQ embeds fakeQ + records the SSE-relevant call params.
type sseFakeQ struct {
	*fakeQ
	listSinceCalled int64
}

func (s *sseFakeQ) ListRegionDeploymentEvents(ctx context.Context, a dbq.ListRegionDeploymentEventsParams) ([]dbq.RegionDeploymentEvent, error) {
	s.listSinceCalled = a.Since
	return s.fakeQ.ListRegionDeploymentEvents(ctx, a)
}

func mountSSE(q *sseFakeQ, redis PubsubSubscriber) http.Handler {
	r := chi.NewRouter()
	(&Handler{Q: q, Redis: redis}).Mount(r)
	return r
}

func TestSSE_NoRow_404(t *testing.T) {
	q := &sseFakeQ{fakeQ: &fakeQ{getErr: pgx.ErrNoRows}}
	rec := doReq(t, mountSSE(q, nil), wildcardP(),
		"/region-deployments/"+uuid.New().String()+"/events/stream")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSSE_BadID_400(t *testing.T) {
	q := &sseFakeQ{fakeQ: &fakeQ{}}
	rec := doReq(t, mountSSE(q, nil), wildcardP(), "/region-deployments/not-a-uuid/events/stream")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestSSE_OutOfScope_403(t *testing.T) {
	id, sid, otherSite := uuid.New(), uuid.New(), uuid.New()
	q := &sseFakeQ{fakeQ: &fakeQ{getRow: dbq.RegionDeployment{ID: id, SiteID: sid, Status: "pending"}}}
	scope := auth.Scope{SiteIDs: map[uuid.UUID]struct{}{otherSite: {}}}
	p := authtest.PrincipalWithScopes([]string{capRead}, map[string]auth.Scope{capRead: scope})
	rec := doReq(t, mountSSE(q, nil), p, "/region-deployments/"+id.String()+"/events/stream")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSSE_BackfillEmitsFrames_HonorsSinceCursor(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	q := &sseFakeQ{fakeQ: &fakeQ{
		getRow: dbq.RegionDeployment{ID: id, SiteID: sid, Status: "pending"},
		events: []dbq.RegionDeploymentEvent{
			{ID: 42, Stage: "preflight", Level: "info", Message: "first", Payload: json.RawMessage(`{}`)},
			{ID: 43, Stage: "provisioning", Level: "info", Message: "second", Payload: json.RawMessage(`{}`)},
		},
	}}
	// No redis → live phase is heartbeat-only; we close the client
	// immediately after backfill via a deadlined request context so the
	// test doesn't hang on the ticker.
	rec := streamCapture(t, mountSSE(q, nil), wildcardP(),
		"/region-deployments/"+id.String()+"/events/stream?since=10", 50*time.Millisecond)

	body := rec.Body.String()
	if !strings.Contains(body, "id: 42") || !strings.Contains(body, "id: 43") {
		t.Errorf("expected both event id frames; body=%s", body)
	}
	for _, want := range []string{`"stage":"preflight"`, `"stage":"provisioning"`, `"message":"first"`, `"message":"second"`} {
		if !strings.Contains(body, want) {
			t.Errorf("frame missing %q; body=%s", want, body)
		}
	}
	if q.listSinceCalled != 10 {
		t.Errorf("ListRegionDeploymentEvents must be called with since=10 (cursor), got %d", q.listSinceCalled)
	}
	if rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("Content-Type must be text/event-stream; got %q", rec.Header().Get("Content-Type"))
	}
}

func TestSSE_BackfillEmptyConfig_PayloadDefaultsToEmptyObject(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	q := &sseFakeQ{fakeQ: &fakeQ{
		getRow: dbq.RegionDeployment{ID: id, SiteID: sid, Status: "pending"},
		events: []dbq.RegionDeploymentEvent{
			// Payload is nil + "null" — both decode paths must render as {}.
			{ID: 1, Stage: "x", Level: "info", Message: "nil payload", Payload: nil},
			{ID: 2, Stage: "x", Level: "info", Message: "null payload", Payload: json.RawMessage("null")},
		},
	}}
	rec := streamCapture(t, mountSSE(q, nil), wildcardP(),
		"/region-deployments/"+id.String()+"/events/stream", 50*time.Millisecond)
	body := rec.Body.String()
	// `"payload":{}` should appear twice (once per event)
	if strings.Count(body, `"payload":{}`) != 2 {
		t.Errorf("expected payload:{} twice; body=%s", body)
	}
}

func TestSSE_LiveMessage_ForwardedAsSSEFrame(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	q := &sseFakeQ{fakeQ: &fakeQ{
		getRow: dbq.RegionDeployment{ID: id, SiteID: sid, Status: "provisioning"},
	}}
	ps := newFakePubSub()
	// Queue a live event before the request starts so it's delivered
	// soon after the backfill phase exits.
	live := map[string]any{
		"id": 99, "stage": "joining", "level": "info",
		"message": "live event", "payload": map[string]any{"node": "n01"},
	}
	bs, _ := json.Marshal(live)
	ps.ch <- &redis.Message{Channel: pubsubChannel(id), Payload: string(bs)}

	rec := streamCapture(t, mountSSE(q, ps), wildcardP(),
		"/region-deployments/"+id.String()+"/events/stream", 100*time.Millisecond)
	body := rec.Body.String()
	if !strings.Contains(body, "id: 99") {
		t.Errorf("expected live frame id: 99; body=%s", body)
	}
	if !strings.Contains(body, `"message":"live event"`) {
		t.Errorf("expected live message; body=%s", body)
	}
}

func TestSSE_LiveDeduplicatesBackfilledIDs(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	q := &sseFakeQ{fakeQ: &fakeQ{
		getRow: dbq.RegionDeployment{ID: id, SiteID: sid, Status: "provisioning"},
		events: []dbq.RegionDeploymentEvent{
			{ID: 5, Stage: "x", Level: "info", Message: "from-backfill", Payload: json.RawMessage(`{}`)},
		},
	}}
	ps := newFakePubSub()
	// Pubsub re-publishes id=5 (race: emit raced backfill cursor).
	dup, _ := json.Marshal(map[string]any{
		"id": 5, "stage": "x", "level": "info",
		"message": "duplicate", "payload": map[string]any{},
	})
	ps.ch <- &redis.Message{Channel: pubsubChannel(id), Payload: string(dup)}

	rec := streamCapture(t, mountSSE(q, ps), wildcardP(),
		"/region-deployments/"+id.String()+"/events/stream", 80*time.Millisecond)
	body := rec.Body.String()
	if strings.Contains(body, "duplicate") {
		t.Errorf("backfill emitted id=5; live duplicate must be suppressed; body=%s", body)
	}
	if !strings.Contains(body, "from-backfill") {
		t.Errorf("backfilled event must still appear; body=%s", body)
	}
}

func TestSSE_MalformedLivePayload_Skipped(t *testing.T) {
	id, sid := uuid.New(), uuid.New()
	q := &sseFakeQ{fakeQ: &fakeQ{
		getRow: dbq.RegionDeployment{ID: id, SiteID: sid, Status: "provisioning"},
	}}
	ps := newFakePubSub()
	ps.ch <- &redis.Message{Channel: pubsubChannel(id), Payload: "{not-json"}
	// Then a valid one — must still be delivered.
	good, _ := json.Marshal(map[string]any{"id": 7, "stage": "x", "level": "info", "message": "ok", "payload": map[string]any{}})
	ps.ch <- &redis.Message{Channel: pubsubChannel(id), Payload: string(good)}
	rec := streamCapture(t, mountSSE(q, ps), wildcardP(),
		"/region-deployments/"+id.String()+"/events/stream", 100*time.Millisecond)
	if !strings.Contains(rec.Body.String(), `"message":"ok"`) {
		t.Errorf("good frame must still be delivered after malformed; body=%s", rec.Body.String())
	}
}

func TestSSE_PubsubChannelNamespace(t *testing.T) {
	// Python's regiondeploy.events.channel_for returns dcim:deploy:<id>.
	// otter-go must match byte-for-byte so a Python emitter and a Go
	// subscriber share the same channel through the cutover.
	id := uuid.New()
	got := pubsubChannel(id)
	want := "dcim:deploy:" + id.String()
	if got != want {
		t.Errorf("channel drift: got %q want %q", got, want)
	}
}

func TestSSE_HeartbeatOnly_OnNilRedis(t *testing.T) {
	// With no Redis wired, the handler emits a heartbeat after 15s.
	// We can't wait 15s in a test; verify the backfill phase still
	// completes and headers are right.
	id, sid := uuid.New(), uuid.New()
	q := &sseFakeQ{fakeQ: &fakeQ{
		getRow: dbq.RegionDeployment{ID: id, SiteID: sid, Status: "pending"},
	}}
	rec := streamCapture(t, mountSSE(q, nil), wildcardP(),
		"/region-deployments/"+id.String()+"/events/stream", 30*time.Millisecond)
	if rec.Header().Get("X-Accel-Buffering") != "no" {
		t.Errorf("X-Accel-Buffering must be 'no' (nginx-ingress hint)")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK headers; got %d", rec.Code)
	}
}

// streamCapture issues a request whose context expires after `after`,
// captures the buffered response, and returns it. The handler exits
// when the context is cancelled. Critically: we layer the timeout
// on top of the principal-bearing context authtest.Request set up,
// instead of replacing the whole context (which would drop the auth
// principal and the handler would 500 "missing principal").
func streamCapture(t *testing.T, h http.Handler, p auth.Principal, path string, after time.Duration) *httptest.ResponseRecorder {
	t.Helper()
	req := authtest.Request(http.MethodGet, path, p, nil)
	ctx, cancel := context.WithTimeout(req.Context(), after)
	defer cancel()
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
