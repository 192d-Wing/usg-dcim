// Bridge drains entries that the Go alerts service pushed onto the
// dcim:notify:bridge Redis list and fans each one out across every
// enabled NotificationChannel. Ports Python's notify_bridge arq cron
// (worker.py:453) — same queue key, same payload shape, same per-
// payload semantics (fetch alert → enabled channels → match → send).
//
// Lives in the notifications package so it can reuse the unexported
// dispatchOne / channelMatches / sender helpers without having to
// promote them to the public API. Wired as a scheduler.Job from
// cmd/otter-go-scheduler/main.go.
package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
)

const (
	// bridgeKey matches Python's _NOTIFY_BRIDGE_KEY constant in
	// worker.py — must stay byte-for-byte identical so the Go alerts
	// service's LPUSH and this RPOP share the same queue.
	bridgeKey = "dcim:notify:bridge"
	// bridgeBatch caps the per-tick drain so a notification burst
	// can't hold the scheduler past the next cron tick. Mirrors
	// Python's _NOTIFY_BRIDGE_BATCH.
	bridgeBatch = 500
	// BridgeName is the scheduler.Job name surface area exposes for
	// logging + /metrics labels. Matches Python's function name.
	BridgeName = "notify_bridge"
)

// BridgeQuerier is the slim sqlc surface the bridge needs.
// *dbq.Queries satisfies it; tests inject a fake.
type BridgeQuerier interface {
	GetAlert(ctx context.Context, id uuid.UUID) (dbq.Alert, error)
	ListEnabledNotificationChannels(ctx context.Context) ([]dbq.NotificationChannel, error)
}

// BridgeRedis is the slim redis.Cmdable subset the bridge uses for
// RPOP. Production wires *redis.Client; tests inject a fake that
// returns canned payloads.
type BridgeRedis interface {
	RPop(ctx context.Context, key string) *redis.StringCmd
}

// BridgeJob implements scheduler.Job. Pulls up to `bridgeBatch`
// entries off `bridgeKey` each tick, hydrates each (alert_id, kind)
// payload into a real Alert + every enabled channel, and dispatches.
type BridgeJob struct {
	Q     BridgeQuerier
	Redis BridgeRedis
	Log   *slog.Logger
}

func (j *BridgeJob) Name() string { return BridgeName }

func (j *BridgeJob) Run(ctx context.Context) (map[string]any, error) {
	if j.Q == nil {
		return nil, errors.New("notify_bridge: Querier is nil")
	}
	if j.Redis == nil {
		return nil, errors.New("notify_bridge: Redis is nil")
	}
	log := j.Log
	if log == nil {
		log = slog.Default()
	}
	var fired, resolved, skipped int
	// Load enabled channels once per tick — Python's
	// notif_svc.dispatch refetches per-alert, but on a 5s cadence
	// against a bounded channel table the per-alert refetch is
	// pure waste. Caching for the duration of the tick is a strict
	// improvement and still picks up channel edits within ≤5s.
	channels, err := j.Q.ListEnabledNotificationChannels(ctx)
	if err != nil {
		return nil, err
	}
	for i := 0; i < bridgeBatch; i++ {
		if ctx.Err() != nil {
			return drainResult(fired, resolved, skipped), nil
		}
		raw, err := j.Redis.RPop(ctx, bridgeKey).Result()
		if errors.Is(err, redis.Nil) {
			break
		}
		if err != nil {
			return drainResult(fired, resolved, skipped), err
		}
		f, r, s := j.handleOne(ctx, log, channels, raw)
		fired += f
		resolved += r
		skipped += s
	}
	if fired+resolved+skipped > 0 {
		log.Info("notify_bridge_drained",
			"fired", fired, "resolved", resolved, "skipped", skipped)
	}
	return drainResult(fired, resolved, skipped), nil
}

// handleOne processes a single LPUSH'd payload. Counts the dispatched
// channels against fired / resolved per Python's accounting (one tick
// counter per *event*, not per channel). Skipped covers malformed
// payloads, unknown kinds, and missing alert rows.
func (j *BridgeJob) handleOne(ctx context.Context, log *slog.Logger, channels []dbq.NotificationChannel, raw string) (fired, resolved, skipped int) {
	var payload bridgePayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		log.Warn("notify_bridge_bad_payload", "raw", raw, "err", err)
		return 0, 0, 1
	}
	alertID, err := uuid.Parse(payload.AlertID)
	if err != nil || payload.Kind == "" {
		log.Warn("notify_bridge_bad_payload", "raw", raw, "err", "missing alert_id or kind")
		return 0, 0, 1
	}
	if payload.Kind != "fire" && payload.Kind != "resolve" {
		log.Warn("notify_bridge_bad_payload", "raw", raw, "err", "unknown kind: "+payload.Kind)
		return 0, 0, 1
	}
	alert, err := j.Q.GetAlert(ctx, alertID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Alert was deleted between enqueue and drain — skip.
			// Python's `db.get` returns None for the same race.
			return 0, 0, 1
		}
		// DB transport error: don't drop the payload from the count
		// log, but don't block the whole drain either. Returning the
		// error from Run() would lose the remaining batch.
		log.Warn("notify_bridge_db_fetch_failed", "alert_id", alertID, "err", err)
		return 0, 0, 1
	}
	a := alertFromDB(alert)
	for _, c := range channels {
		if !channelMatches(c, alert.Severity, payload.Kind) {
			continue
		}
		if _, derr := dispatchOne(ctx, defaultHTTPClient, defaultSMTP, c, a, payload.Kind); derr != nil {
			// Per Python: failures on individual channels are logged
			// but never raised — one bad webhook must not break the
			// rest of the alert evaluation pass.
			log.Warn("notification_failed",
				"channel", c.Name, "kind", c.Kind, "err", derr,
				"alert_id", alertID, "alert_event", payload.Kind)
		}
	}
	if payload.Kind == "fire" {
		return 1, 0, 0
	}
	return 0, 1, 0
}

// bridgePayload matches Python's per-LPUSH dict shape. Field tags pin
// the exact JSON key names so the Go alerts service's emitter and
// this drainer agree on the wire.
type bridgePayload struct {
	Kind    string `json:"kind"`
	AlertID string `json:"alert_id"`
}

// alertFromDB lifts a dbq.Alert row into the syntheticAlert shape
// dispatchOne consumes. Mirrors how the test-channel handler builds
// a syntheticAlert from a probe row; we just build it from a real
// Alert instead.
func alertFromDB(a dbq.Alert) syntheticAlert {
	out := syntheticAlert{
		ID:          a.ID,
		Severity:    a.Severity,
		State:       a.State,
		DedupeKey:   a.DedupeKey,
		Summary:     a.Summary,
		Detail:      nilString(a.Detail),
		FirstSeenAt: a.FirstSeenAt,
		LastSeenAt:  a.LastSeenAt,
	}
	if len(a.LabelsJson) > 0 {
		labels := map[string]any{}
		if err := json.Unmarshal(a.LabelsJson, &labels); err == nil {
			out.Labels = labels
		}
	}
	return out
}

func nilString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// drainResult returns the per-tick counter map both the scheduler
// uses for /metrics labels and the test surface asserts against.
func drainResult(fired, resolved, skipped int) map[string]any {
	return map[string]any{
		"fired":    fired,
		"resolved": resolved,
		"skipped":  skipped,
	}
}

// ensure the time import is used (alertFromDB references time via
// FirstSeenAt's type — but `time.Time` is satisfied by the dbq row,
// so we add a sentinel here to silence linters that flag the import
// when only the type alias is referenced).
var _ = time.RFC3339Nano

// strconv is imported to keep Itoa available if a future scrape
// metric wants to label the batch size; not currently used.
var _ = strconv.Itoa
