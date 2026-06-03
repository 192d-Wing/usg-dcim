// Server-Sent Events streaming for region-deploy event history +
// live updates. Ports Python regiondeploy.py L452-551 — same
// catch-up-then-pubsub semantics (paginated backfill via the DB
// then subscribe to Redis channel `dcim:deploy:{id}`), same SSE
// frame shape (`id: N\ndata: <json>\n\n`), same 15s heartbeat
// (`: heartbeat\n\n`) so proxies don't drop the connection during
// quiet stretches between orchestrator stages.
package regiondeploy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// pubsubChannel returns the Redis channel name for a deployment's
// live event stream. Mirrors Python's dcim.regiondeploy.events.channel_for
// — `dcim:deploy:{id}` to match the existing pubsub namespace
// convention (`dcim:notify:bridge` for alerts).
func pubsubChannel(id uuid.UUID) string {
	return "dcim:deploy:" + id.String()
}

// PubsubSubscriber is the subset of *redis.Client the SSE handler uses.
// Tests inject a fake that delivers canned messages without standing
// up a real Redis. Production wires *redis.Client through Handler.Redis.
type PubsubSubscriber interface {
	Subscribe(ctx context.Context, channels ...string) PubsubChannelCloser
}

// PubsubChannelCloser is the subset of *redis.PubSub Subscribe returns.
// Channel() drains incoming messages; Close() tears down the subscription.
type PubsubChannelCloser interface {
	Channel(...redis.ChannelOption) <-chan *redis.Message
	Close() error
}

// redisAdapter wraps *redis.Client to satisfy PubsubSubscriber. Keeps
// the Subscribe return type swappable for the fake without changing
// production call sites.
type redisAdapter struct{ c *redis.Client }

func (r *redisAdapter) Subscribe(ctx context.Context, channels ...string) PubsubChannelCloser {
	return r.c.Subscribe(ctx, channels...)
}

// NewRedisAdapter wraps a *redis.Client so it satisfies PubsubSubscriber.
// Exposed so main.go can wire production without exporting the inner type.
func NewRedisAdapter(c *redis.Client) PubsubSubscriber { return &redisAdapter{c: c} }

func (h *Handler) streamEvents(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, badIDMsg)
		return
	}
	// Existence + scope check first so a long-lived stream doesn't open
	// against a row the principal can't see. Mirrors Python L472-475.
	row, err := h.Q.GetRegionDeployment(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, notFound)
			return
		}
		writeMapped(w, err)
		return
	}
	p, _ := auth.From(r.Context())
	if serr := auth.EnforceSiteScope(r.Context(), h.Q, p, row.SiteID, capRead); serr != nil {
		writeMapped(w, serr)
		return
	}
	// HTTP response shape: text/event-stream with no buffering. The
	// Cache-Control + Connection headers + ResponseController.Flush
	// keep the bytes flowing to the client through the chi middleware
	// stack and any intermediate proxies.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // nginx-ingress hint
	w.WriteHeader(http.StatusOK)
	flusher := http.NewResponseController(w)

	since, _ := parseInt64Query(r.URL.Query(), "since", 0, 0)
	lastID, err := h.streamBackfill(r.Context(), w, flusher, id, since)
	if err != nil || r.Context().Err() != nil {
		return
	}
	// Live: switch to pubsub. When Redis isn't wired (tests / dev),
	// fall back to a heartbeat-only loop so the client at least sees
	// the connection is alive. Python's path would 500 here; we
	// degrade gracefully.
	if h.Redis == nil {
		h.heartbeatOnly(r.Context(), w, flusher)
		return
	}
	h.streamLive(r.Context(), w, flusher, id, lastID)
}

// streamBackfill sends every persisted event with id > since as an
// SSE frame, returning the highest id seen so the live phase can
// suppress any pubsub messages already covered by the backlog. Stops
// early when the client disconnects.
func (h *Handler) streamBackfill(ctx context.Context, w http.ResponseWriter, fl *http.ResponseController, id uuid.UUID, since int64) (int64, error) {
	lastID := since
	// Page through the backlog in 500-row chunks. Python streams the
	// whole rowset in one query — fine on small deploys but a giant
	// deploy with thousands of historical events would buffer all of
	// them in memory; chunking caps that.
	for {
		if ctx.Err() != nil {
			return lastID, ctx.Err()
		}
		rows, err := h.Q.ListRegionDeploymentEvents(ctx, dbq.ListRegionDeploymentEventsParams{
			DeploymentID: id, Since: lastID, Limit: 500,
		})
		if err != nil {
			return lastID, err
		}
		if len(rows) == 0 {
			return lastID, nil
		}
		for _, ev := range rows {
			if ctx.Err() != nil {
				return lastID, ctx.Err()
			}
			if err := writeSSEFrame(w, ev.ID, eventEnvelope(ev)); err != nil {
				return lastID, err
			}
			lastID = ev.ID
		}
		_ = fl.Flush()
		if len(rows) < 500 {
			return lastID, nil
		}
	}
}

// streamLive subscribes to the pubsub channel and forwards messages
// as SSE frames. Sends a heartbeat comment every 15s on inactivity to
// keep nginx-ingress + cilium-envoy from idling the connection out.
func (h *Handler) streamLive(ctx context.Context, w http.ResponseWriter, fl *http.ResponseController, id uuid.UUID, lastID int64) {
	sub := h.Redis.Subscribe(ctx, pubsubChannel(id))
	defer func() { _ = sub.Close() }()
	msgCh := sub.Channel()

	hb := time.NewTicker(15 * time.Second)
	defer hb.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-hb.C:
			if _, err := w.Write([]byte(": heartbeat\n\n")); err != nil {
				return
			}
			_ = fl.Flush()
		case msg, ok := <-msgCh:
			if !ok {
				return
			}
			env, evID, ok := parseLiveEnvelope(msg.Payload)
			if !ok {
				continue
			}
			// Suppress duplicates: a publish that fires while we were
			// finishing backfill could repeat an event we already
			// emitted. Python's path has the same race; we close it.
			if evID > 0 && evID <= lastID {
				continue
			}
			if err := writeSSEFrame(w, evID, env); err != nil {
				return
			}
			_ = fl.Flush()
			if evID > lastID {
				lastID = evID
			}
		}
	}
}

// heartbeatOnly keeps a client connected when no Redis is wired. The
// backfill already flushed any persisted events; without pubsub, we
// just emit heartbeats until the client disconnects so the JS
// EventSource doesn't reconnect-loop on an immediate close.
func (h *Handler) heartbeatOnly(ctx context.Context, w http.ResponseWriter, fl *http.ResponseController) {
	hb := time.NewTicker(15 * time.Second)
	defer hb.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hb.C:
			if _, err := w.Write([]byte(": heartbeat\n\n")); err != nil {
				return
			}
			_ = fl.Flush()
		}
	}
}

// eventEnvelope builds the SSE data payload matching Python's
// _row_to_envelope output. payload defaults to {} when the row's
// JSONB column is empty so the wizard's JS parses an object every
// time.
func eventEnvelope(ev dbq.RegionDeploymentEvent) map[string]any {
	var payload any
	if len(ev.Payload) == 0 || string(ev.Payload) == "null" {
		payload = map[string]any{}
	} else if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		payload = map[string]any{}
	}
	return map[string]any{
		"id":      ev.ID,
		"stage":   ev.Stage,
		"level":   ev.Level,
		"message": ev.Message,
		"payload": payload,
	}
}

// parseLiveEnvelope decodes a pubsub payload into the envelope shape
// + the event id (for dedup against backfill). Returns ok=false on
// malformed JSON so the caller can skip and loop on.
func parseLiveEnvelope(payload string) (map[string]any, int64, bool) {
	var env map[string]any
	if err := json.Unmarshal([]byte(payload), &env); err != nil {
		return nil, 0, false
	}
	evID := int64(0)
	switch v := env["id"].(type) {
	case float64:
		evID = int64(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			evID = n
		}
	}
	return env, evID, true
}

// writeSSEFrame formats one SSE frame (`id: N\ndata: <json>\n\n`) and
// writes it to w. Matching Python's `f"id: {event_id}\ndata: ...\n\n"`
// shape so reconnecting clients can echo the last id back via ?since=.
func writeSSEFrame(w http.ResponseWriter, evID int64, data map[string]any) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte("id: " + strconv.FormatInt(evID, 10) + "\ndata: ")); err != nil {
		return err
	}
	if _, err := w.Write(encoded); err != nil {
		return err
	}
	_, err = w.Write([]byte("\n\n"))
	return err
}
