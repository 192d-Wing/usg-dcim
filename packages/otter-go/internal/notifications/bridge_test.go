package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

// fakeBridgeRedis returns canned payloads on RPOP — one per call,
// in FIFO order — then redis.Nil when the queue drains.
type fakeBridgeRedis struct {
	queue []string
	calls int
	err   error
}

func (f *fakeBridgeRedis) RPop(ctx context.Context, _ string) *redis.StringCmd {
	f.calls++
	cmd := redis.NewStringCmd(ctx)
	if f.err != nil {
		cmd.SetErr(f.err)
		return cmd
	}
	if len(f.queue) == 0 {
		cmd.SetErr(redis.Nil)
		return cmd
	}
	head := f.queue[0]
	f.queue = f.queue[1:]
	cmd.SetVal(head)
	return cmd
}

type fakeBridgeQ struct {
	alerts      map[uuid.UUID]dbq.Alert
	channels    []dbq.NotificationChannel
	channelsErr error
	// getAlertErrors lets a test inject a specific error for a given
	// alert id (e.g. pgx.ErrNoRows for the deleted-race case).
	getAlertErrors map[uuid.UUID]error
}

func (f *fakeBridgeQ) GetAlert(_ context.Context, id uuid.UUID) (dbq.Alert, error) {
	if e, ok := f.getAlertErrors[id]; ok {
		return dbq.Alert{}, e
	}
	a, ok := f.alerts[id]
	if !ok {
		return dbq.Alert{}, pgx.ErrNoRows
	}
	return a, nil
}

func (f *fakeBridgeQ) ListEnabledNotificationChannels(_ context.Context) ([]dbq.NotificationChannel, error) {
	return f.channels, f.channelsErr
}

// firePayload returns a JSON payload identical to what the Go alerts
// service LPUSH's onto dcim:notify:bridge for a fire event.
func firePayload(alertID uuid.UUID) string {
	b, _ := json.Marshal(map[string]any{"kind": "fire", "alert_id": alertID.String()})
	return string(b)
}

func resolvePayload(alertID uuid.UUID) string {
	b, _ := json.Marshal(map[string]any{"kind": "resolve", "alert_id": alertID.String()})
	return string(b)
}

func TestBridge_EmptyQueue_NoOp(t *testing.T) {
	q := &fakeBridgeQ{}
	r := &fakeBridgeRedis{}
	res, err := (&BridgeJob{Q: q, Redis: r, Log: slog.Default()}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res["fired"].(int) != 0 || res["resolved"].(int) != 0 || res["skipped"].(int) != 0 {
		t.Errorf("empty queue must yield zeros; got %+v", res)
	}
	if r.calls != 1 {
		t.Errorf("expected exactly one RPOP probe; got %d", r.calls)
	}
}

func TestBridge_FirePayload_CountsFired(t *testing.T) {
	alertID := uuid.New()
	q := &fakeBridgeQ{
		alerts: map[uuid.UUID]dbq.Alert{
			alertID: {ID: alertID, Severity: "warning", State: "firing", Summary: "x"},
		},
	}
	r := &fakeBridgeRedis{queue: []string{firePayload(alertID)}}
	res, err := (&BridgeJob{Q: q, Redis: r}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res["fired"].(int) != 1 {
		t.Errorf("expected fired=1; got %+v", res)
	}
}

func TestBridge_ResolvePayload_CountsResolved(t *testing.T) {
	alertID := uuid.New()
	q := &fakeBridgeQ{
		alerts: map[uuid.UUID]dbq.Alert{
			alertID: {ID: alertID, Severity: "warning", State: "resolved", Summary: "x"},
		},
	}
	r := &fakeBridgeRedis{queue: []string{resolvePayload(alertID)}}
	res, _ := (&BridgeJob{Q: q, Redis: r}).Run(context.Background())
	if res["resolved"].(int) != 1 {
		t.Errorf("expected resolved=1; got %+v", res)
	}
}

func TestBridge_MultiplePayloads_DrainedInOneTick(t *testing.T) {
	a1, a2, a3 := uuid.New(), uuid.New(), uuid.New()
	q := &fakeBridgeQ{
		alerts: map[uuid.UUID]dbq.Alert{
			a1: {ID: a1, Severity: "warning", State: "firing", Summary: "x"},
			a2: {ID: a2, Severity: "critical", State: "firing", Summary: "x"},
			a3: {ID: a3, Severity: "warning", State: "resolved", Summary: "x"},
		},
	}
	r := &fakeBridgeRedis{queue: []string{
		firePayload(a1), firePayload(a2), resolvePayload(a3),
	}}
	res, _ := (&BridgeJob{Q: q, Redis: r}).Run(context.Background())
	if res["fired"].(int) != 2 || res["resolved"].(int) != 1 {
		t.Errorf("expected fired=2 resolved=1; got %+v", res)
	}
}

func TestBridge_AlertDeletedRace_CountsSkipped(t *testing.T) {
	// Alert was deleted between the LPUSH and our RPOP — Python's
	// `db.get` returns None for the same race and counts skipped.
	alertID := uuid.New()
	q := &fakeBridgeQ{
		alerts: map[uuid.UUID]dbq.Alert{}, // no row
	}
	r := &fakeBridgeRedis{queue: []string{firePayload(alertID)}}
	res, err := (&BridgeJob{Q: q, Redis: r}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res["skipped"].(int) != 1 || res["fired"].(int) != 0 {
		t.Errorf("expected skipped=1 fired=0 on missing alert; got %+v", res)
	}
}

func TestBridge_MalformedJSON_CountsSkipped(t *testing.T) {
	q := &fakeBridgeQ{}
	r := &fakeBridgeRedis{queue: []string{"{not-json"}}
	res, _ := (&BridgeJob{Q: q, Redis: r}).Run(context.Background())
	if res["skipped"].(int) != 1 {
		t.Errorf("malformed JSON must count skipped; got %+v", res)
	}
}

func TestBridge_MissingAlertID_CountsSkipped(t *testing.T) {
	q := &fakeBridgeQ{}
	r := &fakeBridgeRedis{queue: []string{`{"kind":"fire"}`}}
	res, _ := (&BridgeJob{Q: q, Redis: r}).Run(context.Background())
	if res["skipped"].(int) != 1 {
		t.Errorf("missing alert_id must count skipped; got %+v", res)
	}
}

func TestBridge_UnknownKind_CountsSkipped(t *testing.T) {
	q := &fakeBridgeQ{}
	id := uuid.New()
	body, _ := json.Marshal(map[string]any{"kind": "trigger", "alert_id": id.String()})
	r := &fakeBridgeRedis{queue: []string{string(body)}}
	res, _ := (&BridgeJob{Q: q, Redis: r}).Run(context.Background())
	if res["skipped"].(int) != 1 {
		t.Errorf("unknown kind must count skipped (not fired or resolved); got %+v", res)
	}
}

func TestBridge_DBFetchFails_CountsSkipped_ContinuesDrain(t *testing.T) {
	// A non-pgx.ErrNoRows DB error on GetAlert mustn't abort the
	// drain — log it, count skipped, move on. Otherwise a transient
	// hiccup loses the rest of the batch.
	a1, a2 := uuid.New(), uuid.New()
	q := &fakeBridgeQ{
		alerts: map[uuid.UUID]dbq.Alert{
			a2: {ID: a2, Severity: "warning", State: "firing", Summary: "x"},
		},
		getAlertErrors: map[uuid.UUID]error{a1: errors.New("connection reset")},
	}
	r := &fakeBridgeRedis{queue: []string{firePayload(a1), firePayload(a2)}}
	res, err := (&BridgeJob{Q: q, Redis: r}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res["skipped"].(int) != 1 || res["fired"].(int) != 1 {
		t.Errorf("expected skipped=1 fired=1 after one DB failure; got %+v", res)
	}
}

func TestBridge_BatchCap_Honored(t *testing.T) {
	// Push more than bridgeBatch entries and confirm the drain stops
	// at the cap so the scheduler can fire other jobs on the next
	// tick.
	q := &fakeBridgeQ{alerts: map[uuid.UUID]dbq.Alert{}}
	queue := make([]string, bridgeBatch+10)
	for i := range queue {
		queue[i] = `{"kind":"fire","alert_id":"00000000-0000-0000-0000-000000000000"}`
	}
	r := &fakeBridgeRedis{queue: queue}
	_, _ = (&BridgeJob{Q: q, Redis: r}).Run(context.Background())
	if r.calls != bridgeBatch {
		t.Errorf("expected drain to stop at bridgeBatch (%d) RPOP calls; got %d", bridgeBatch, r.calls)
	}
}

func TestBridge_NilQuerier_Errors(t *testing.T) {
	if _, err := (&BridgeJob{Redis: &fakeBridgeRedis{}}).Run(context.Background()); err == nil {
		t.Errorf("nil Querier should error")
	}
}

func TestBridge_NilRedis_Errors(t *testing.T) {
	if _, err := (&BridgeJob{Q: &fakeBridgeQ{}}).Run(context.Background()); err == nil {
		t.Errorf("nil Redis should error")
	}
}

func TestBridge_ChannelListError_Aborts(t *testing.T) {
	// Channel list is fetched once per tick — a failure there means
	// we can't dispatch any payload sanely. Surface the error so
	// the scheduler logs it; the queue stays intact for the next
	// tick.
	q := &fakeBridgeQ{channelsErr: errors.New("db down")}
	r := &fakeBridgeRedis{}
	if _, err := (&BridgeJob{Q: q, Redis: r}).Run(context.Background()); err == nil {
		t.Errorf("channel-list error must propagate")
	}
}

func TestBridge_BridgeKeyMatchesPython(t *testing.T) {
	// Python's _NOTIFY_BRIDGE_KEY in worker.py:446. Drift breaks
	// the cross-language queue silently — the Go alerts service
	// would push onto one list and the bridge would drain a
	// different (empty) list.
	if bridgeKey != "dcim:notify:bridge" {
		t.Errorf("bridge key drift: got %q want %q", bridgeKey, "dcim:notify:bridge")
	}
}
