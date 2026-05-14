// Package forwarder ships buffered samples to the ingest endpoint and
// runs the heartbeat loop. Phase-1 parity with collector/forwarder.py:
//
//   - Batches are POST'd to telemetry_url (or ingest_url) + /api/v1/ingest/telemetry
//   - Heartbeat goes to ingest_url + /api/v1/collectors/<id>/heartbeat
//   - Auth via Bearer dcim_<token> loaded from api_token_file
//   - 5xx / network errors trigger exponential backoff; 4xx is logged
//     and the batch stays buffered (operator action required)
package forwarder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/usg-dcim/services/go-collector/internal/buffer"
	"github.com/usg-dcim/services/go-collector/internal/config"
	"github.com/usg-dcim/services/go-collector/internal/runtime"
)

const (
	defaultBatchSize = 500
	maxRetryWait     = 30 * time.Second
)

type Forwarder struct {
	cfg     *config.Config
	buf     *buffer.Buffer
	client  *http.Client
	token   string
	log     *slog.Logger
	batchID string
	rt      *runtime.Config
}

func New(cfg *config.Config, buf *buffer.Buffer, token string, log *slog.Logger, rt *runtime.Config) *Forwarder {
	return &Forwarder{
		cfg:    cfg,
		buf:    buf,
		client: &http.Client{Timeout: 30 * time.Second},
		token:  token,
		log:    log,
		rt:     rt,
	}
}

// Run drives the forward loop: drain → POST → ack → repeat. Sleeps
// when the buffer is empty. Returns only when ctx is cancelled.
func (f *Forwarder) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rows, err := f.buf.Drain(ctx, defaultBatchSize)
		if err != nil {
			f.log.Error("buffer_drain_failed", "err", err)
			if sleep(ctx, 5*time.Second) {
				return ctx.Err()
			}
			continue
		}
		if len(rows) == 0 {
			if sleep(ctx, time.Second) {
				return ctx.Err()
			}
			backoff = time.Second
			continue
		}
		ackIDs, retry, err := f.sendBatch(ctx, rows)
		if err != nil {
			f.log.Warn("send_failed", "err", err, "retry", retry, "wait", backoff.String())
			if retry {
				if sleep(ctx, backoff) {
					return ctx.Err()
				}
				backoff = nextBackoff(backoff)
				continue
			}
			// Non-retryable (4xx). Leave rows buffered; an operator
			// needs to look at the logs and fix the token/scope.
			if sleep(ctx, 30*time.Second) {
				return ctx.Err()
			}
			continue
		}
		if err := f.buf.Ack(ctx, ackIDs); err != nil {
			f.log.Error("buffer_ack_failed", "err", err, "count", len(ackIDs))
		}
		f.log.Info("batch_sent", "count", len(ackIDs))
		backoff = time.Second
	}
}

// sendBatch returns (ack ids, retryable, error). On 2xx the caller acks.
// On 5xx/network, retryable=true. On 4xx, retryable=false.
func (f *Forwarder) sendBatch(ctx context.Context, rows []buffer.Row) ([]int64, bool, error) {
	if f.batchID == "" {
		f.batchID = uuid.NewString()
	}
	payload := map[string]any{
		"batch_id":     f.batchID + "-" + uuid.NewString()[:8],
		"site_id":      f.cfg.SiteID.String(),
		"collector_id": f.cfg.CollectorID.String(),
		"samples":      make([]map[string]any, 0, len(rows)),
	}
	ids := make([]int64, 0, len(rows))
	samples := payload["samples"].([]map[string]any)
	for _, r := range rows {
		// drop corrupt rows by id (no asset_id means Drain failed to decode)
		if r.Sample.AssetID == "" {
			ids = append(ids, r.ID)
			continue
		}
		samples = append(samples, map[string]any{
			"asset_id": r.Sample.AssetID,
			"metric":   r.Sample.Metric,
			"value":    r.Sample.Value,
			"unit":     r.Sample.Unit,
			"ts":       r.Sample.Ts,
			"tags":     r.Sample.Tags,
		})
		ids = append(ids, r.ID)
	}
	payload["samples"] = samples
	if len(samples) == 0 {
		// All rows were corrupt — let the caller ack them so they
		// stop showing up on every cycle.
		return ids, false, nil
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, false, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", f.cfg.TelemetryEndpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if f.token != "" {
		req.Header.Set("Authorization", "Bearer "+f.token)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return ids, false, nil
	case resp.StatusCode >= 500:
		return nil, true, fmt.Errorf("ingest 5xx: %d", resp.StatusCode)
	default:
		return nil, false, fmt.Errorf("ingest 4xx: %d", resp.StatusCode)
	}
}

// heartbeatResponse mirrors backend CollectorHeartbeatOut. Unknown
// fields are tolerated by json.Decode for forwards-compat.
type heartbeatResponse struct {
	OK              bool   `json:"ok"`
	ReceivedAt      string `json:"received_at"`
	ConfigOverrides struct {
		DNSMetricsIntervalSeconds *int `json:"dns_metrics_interval_seconds"`
		DevicePollIntervalSeconds *int `json:"device_poll_interval_seconds"`
		HeartbeatIntervalSeconds  *int `json:"heartbeat_interval_seconds"`
	} `json:"config_overrides"`
}

// RunHeartbeat posts heartbeat metadata, decodes the response's
// config_overrides, and applies them to the shared runtime config so
// every loop picks up the new intervals on its next iteration.
//
// The heartbeat ticker itself reads the (possibly-overridden)
// interval each cycle, so a "slow heartbeats to 5 minutes" override
// takes effect on the cycle immediately after central acks it.
func (f *Forwarder) RunHeartbeat(ctx context.Context) error {
	yamlDefault := f.cfg.HeartbeatInterval()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		interval := yamlDefault
		if f.rt != nil {
			interval = f.rt.HeartbeatInterval(yamlDefault)
		}
		if sleep(ctx, interval) {
			return ctx.Err()
		}
		f.sendHeartbeat(ctx)
	}
}

func (f *Forwarder) sendHeartbeat(ctx context.Context) {
	depth, _ := f.buf.Depth(ctx)
	body, _ := json.Marshal(map[string]any{
		"buffered_samples": depth,
		"queue_depth":      depth,
		"version":          "go-collector/phase-4",
	})
	req, err := http.NewRequestWithContext(ctx, "POST", f.cfg.HeartbeatEndpoint(), bytes.NewReader(body))
	if err != nil {
		f.log.Warn("heartbeat_req_build_failed", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if f.token != "" {
		req.Header.Set("Authorization", "Bearer "+f.token)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		f.log.Warn("heartbeat_failed", "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		f.log.Warn("heartbeat_rejected", "status", resp.StatusCode)
		return
	}
	if f.rt == nil {
		return
	}
	var hr heartbeatResponse
	if err := json.NewDecoder(resp.Body).Decode(&hr); err != nil {
		// Non-fatal — Python API may not yet emit the new schema during
		// a partial cutover. Stay on YAML defaults.
		return
	}
	f.applyOverrides(&hr)
}

func (f *Forwarder) applyOverrides(hr *heartbeatResponse) {
	dur := func(secs *int) time.Duration {
		if secs == nil || *secs <= 0 {
			return 0
		}
		return time.Duration(*secs) * time.Second
	}
	dns := dur(hr.ConfigOverrides.DNSMetricsIntervalSeconds)
	pol := dur(hr.ConfigOverrides.DevicePollIntervalSeconds)
	hb := dur(hr.ConfigOverrides.HeartbeatIntervalSeconds)
	f.rt.Apply(dns, pol, hb)
	if dns > 0 || pol > 0 || hb > 0 {
		f.log.Info("config_overrides_applied",
			"dns_metrics", dns.String(),
			"device_poll", pol.String(),
			"heartbeat", hb.String(),
		)
	}
}

func nextBackoff(cur time.Duration) time.Duration {
	next := cur * 2
	if next > maxRetryWait {
		next = maxRetryWait
	}
	// jitter ±20% so a fleet doesn't synchronise on a single backoff
	jitter := time.Duration(rand.Int63n(int64(float64(next) * 0.4)))
	return next - time.Duration(float64(next)*0.2) + jitter
}

func sleep(ctx context.Context, d time.Duration) (cancelled bool) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-t.C:
		return false
	}
}

// Stub avoids unused-import warning if log/slog is not used directly.
var _ = math.Pi
