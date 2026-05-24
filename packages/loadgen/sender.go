package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// sample mirrors heron's wire format. Kept inline so loadgen has no
// dependency on the heron module and can be built standalone.
type sample struct {
	AssetID uuid.UUID `json:"asset_id"`
	Metric  string    `json:"metric"`
	Value   float64   `json:"value"`
	Unit    string    `json:"unit,omitempty"`
	Ts      time.Time `json:"ts"`
}

type batch struct {
	BatchID     string    `json:"batch_id"`
	SiteID      uuid.UUID `json:"site_id"`
	CollectorID uuid.UUID `json:"collector_id"`
	Samples     []sample  `json:"samples"`
}

// collector represents one simulated site collector — a single goroutine
// that POSTs one batch per poll interval, repeated for the run duration.
type collector struct {
	siteID      uuid.UUID
	collectorID uuid.UUID
	assets      []uuid.UUID
	metrics     []string // metric names rotated through; one batch covers all
	target      string   // full URL (e.g. http://localhost:8081/api/v1/ingest/telemetry)
	bearer      string   // optional Bearer token
	client      *http.Client
}

func newCollector(target, bearer string, assets int, metrics []string, client *http.Client) *collector {
	c := &collector{
		siteID:      uuid.New(),
		collectorID: uuid.New(),
		metrics:     metrics,
		target:      target,
		bearer:      bearer,
		client:      client,
	}
	c.assets = make([]uuid.UUID, assets)
	for i := range c.assets {
		c.assets[i] = uuid.New()
	}
	return c
}

// run blocks until ctx is canceled, sending one batch per pollEvery.
// The first batch fires after a random jitter in [0, pollEvery) so
// hundreds of collectors don't synchronize on the same wall-clock
// tick — keeps load steady rather than spiky.
func (c *collector) run(ctx context.Context, pollEvery time.Duration, t *tracker) {
	jitter := time.Duration(rand.Int63n(int64(pollEvery)))
	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter):
	}
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	c.sendBatch(ctx, t)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			c.sendBatch(ctx, t)
		}
	}
}

func (c *collector) sendBatch(ctx context.Context, t *tracker) {
	now := time.Now().UTC()
	// One sample per asset × metric. With the defaults (50 assets × 4
	// metrics) this is a 200-sample batch — within heron's 1000-sample
	// soft cap.
	samples := make([]sample, 0, len(c.assets)*len(c.metrics))
	for _, a := range c.assets {
		for _, m := range c.metrics {
			samples = append(samples, sample{
				AssetID: a,
				Metric:  m,
				Value:   rand.Float64() * 100,
				Unit:    unitFor(m),
				Ts:      now,
			})
		}
	}
	body, err := json.Marshal(batch{
		BatchID:     uuid.New().String(),
		SiteID:      c.siteID,
		CollectorID: c.collectorID,
		Samples:     samples,
	})
	if err != nil {
		t.recordError()
		return
	}
	t.recordBytes(len(body))

	req, err := http.NewRequestWithContext(ctx, "POST", c.target, bytes.NewReader(body))
	if err != nil {
		t.recordError()
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}

	start := time.Now()
	resp, err := c.client.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		t.recordError()
		return
	}
	defer resp.Body.Close()
	// Drain body to allow connection reuse.
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.recordError()
		return
	}
	t.record(elapsed)
}

// unitFor picks a plausible unit string for a metric name so the
// generated payload doesn't look obviously synthetic to downstream
// validators. Loose mapping — not load-bearing.
func unitFor(metric string) string {
	switch metric {
	case "cpu_pct", "mem_pct", "disk_pct":
		return "percent"
	case "temp_c":
		return "celsius"
	case "power_w":
		return "watts"
	case "fan_rpm":
		return "rpm"
	}
	return ""
}

// formatError is a tiny helper used by main.go to render fatal errors.
func formatError(err error) string {
	return fmt.Sprintf("loadgen: %v", err)
}
