// Package ingest serves /api/v1/ingest/telemetry — the JSON fallback
// telemetry path. The high-throughput path lives in heron over mTLS;
// this endpoint exists for collectors without a heron-shaped client
// (operator tools, smoke tests, mode-zero bootstrap).
//
// Wire shape + status semantics match Python's services/telemetry.py:
//   - Per-(asset_id, metric) freshness rows are upserted with
//     freshness='current' so the UI's stale/current indicator updates
//     before the samples land.
//   - Samples are INSERTed into the TimescaleDB hypertable with
//     ON CONFLICT DO NOTHING on uq_telem_sample_dedup so collector
//     retries on a flaky network are no-ops.
//   - WriteHypertable=false skips the sample INSERT entirely (matches
//     Python's settings.telemetry_write_hypertable opt-out for stock
//     Postgres without the TimescaleDB extension); freshness still
//     updates so charts/forecasts show the source as live even with
//     no samples persisted.
//
// Metrics emission deliberately omitted: heron already emits the
// dcim_telemetry_* counters via the canonical mTLS path; this fallback
// is observability-blind by design (matches the cutover plan in
// packages/otter/src/dcim/metrics.py:27 where DCIM_DISABLE_GO_PORTED_
// METRICS gates the Python side off once heron owns ingest).
package ingest

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/auth"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

const capIngestWrite = "collectors:ingest:write"

type Querier interface {
	UpsertTelemetrySource(ctx context.Context, arg dbq.UpsertTelemetrySourceParams) error
	InsertTelemetrySample(ctx context.Context, arg dbq.InsertTelemetrySampleParams) error
}

type Handler struct {
	Q Querier
	// WriteHypertable mirrors Python's settings.telemetry_write_hypertable.
	// false → skip the InsertTelemetrySample loop entirely (freshness
	// still updates). Set from DCIM_TELEMETRY_WRITE_HYPERTABLE in main.go.
	WriteHypertable bool
}

func (h *Handler) Mount(r chi.Router) {
	r.Route("/ingest", func(r chi.Router) {
		r.With(auth.RequireCapability(capIngestWrite)).Post("/telemetry", h.postTelemetry)
	})
}

// Wire-shape mirror of Python's schemas/telemetry.py TelemetrySample/Batch.
// Validation bounds (8-64 char batch_id, 1-5000 samples) match Python's
// Field(min_length=..., max_length=...) so a bad batch fails the same
// way across the cutover.
type sample struct {
	AssetID uuid.UUID         `json:"asset_id"`
	Metric  string            `json:"metric"`
	Value   float64           `json:"value"`
	Unit    *string           `json:"unit"`
	TS      time.Time         `json:"ts"`
	Tags    map[string]string `json:"tags"`
}

type batch struct {
	BatchID     string    `json:"batch_id"`
	SiteID      uuid.UUID `json:"site_id"`
	CollectorID uuid.UUID `json:"collector_id"`
	Samples     []sample  `json:"samples"`
}

type ingestOut struct {
	Accepted   int    `json:"accepted"`
	Errors     bool   `json:"errors"`
	ReceivedAt string `json:"received_at"`
}

const (
	batchIDMinLen = 8
	batchIDMaxLen = 64
	samplesMaxLen = 5000
)

func (h *Handler) postTelemetry(w http.ResponseWriter, r *http.Request) {
	var b batch
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad request body")
		return
	}
	if msg := validateBatch(&b); msg != "" {
		httpx.Error(w, http.StatusBadRequest, msg)
		return
	}

	receivedAt := time.Now().UTC()
	if err := h.upsertFreshness(r, b, receivedAt); err != nil {
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	written := h.insertSamples(r, b, receivedAt)

	httpx.JSON(w, http.StatusOK, ingestOut{
		Accepted: len(b.Samples), Errors: !written,
		// Python uses datetime.isoformat(); RFC3339Nano matches modulo
		// the trailing 'Z'-vs-'+00:00' nit. Charts/dashboards key off
		// the value-shape not the byte form.
		ReceivedAt: receivedAt.Format(time.RFC3339Nano),
	})
}

func validateBatch(b *batch) string {
	if n := len(b.BatchID); n < batchIDMinLen || n > batchIDMaxLen {
		return "batch_id length must be 8..64"
	}
	if b.SiteID == uuid.Nil || b.CollectorID == uuid.Nil {
		return "site_id and collector_id required"
	}
	if n := len(b.Samples); n == 0 || n > samplesMaxLen {
		return "samples length must be 1..5000"
	}
	// Per-sample required-field check. Python's pydantic TelemetrySample
	// 422s on missing asset_id/metric/value/ts; Go's json.Decoder
	// silently zero-fills (metric=""/ts=year-0001/asset_id=uuid.Nil).
	for _, s := range b.Samples {
		if s.AssetID == uuid.Nil {
			return "samples[].asset_id required"
		}
		if s.Metric == "" {
			return "samples[].metric required"
		}
		if s.TS.IsZero() {
			return "samples[].ts required"
		}
	}
	return ""
}

// upsertFreshness applies Python's per-(asset_id, metric) dedup —
// the last sample for each pair wins, mirroring the dict-overwrite
// semantics of `by_key[(asset_id, metric)] = s`.
func (h *Handler) upsertFreshness(r *http.Request, b batch, receivedAt time.Time) error {
	type freshKey struct {
		assetID uuid.UUID
		metric  string
	}
	freshBy := make(map[freshKey]sample, len(b.Samples))
	for _, s := range b.Samples {
		freshBy[freshKey{s.AssetID, s.Metric}] = s
	}
	for key, s := range freshBy {
		if err := h.Q.UpsertTelemetrySource(r.Context(), dbq.UpsertTelemetrySourceParams{
			SiteID: b.SiteID, AssetID: key.assetID, CollectorID: b.CollectorID,
			Metric: key.metric, Unit: s.Unit,
			LastSuccessAt: receivedAt, LastReadingAt: s.TS, LastValue: s.Value,
		}); err != nil {
			return err
		}
	}
	return nil
}

// insertSamples returns true iff every row landed. Partial-commit
// caveat: per-row Exec is auto-committed, so a mid-batch failure
// leaves the earlier rows persisted (matches the JSON-fallback
// design — heron is canonical, and ON CONFLICT DO NOTHING on the
// dedup constraint makes a full retry safe).
func (h *Handler) insertSamples(r *http.Request, b batch, receivedAt time.Time) bool {
	if !h.WriteHypertable {
		// Stock-Postgres opt-out (matches Python's
		// settings.telemetry_write_hypertable=False branch).
		return false
	}
	for i, s := range b.Samples {
		tagsJSON, err := json.Marshal(orEmpty(s.Tags))
		if err != nil {
			return false
		}
		if err := h.Q.InsertTelemetrySample(r.Context(), dbq.InsertTelemetrySampleParams{
			TS: s.TS, SiteID: b.SiteID, AssetID: s.AssetID, CollectorID: b.CollectorID,
			BatchID: b.BatchID, Seq: int32(i), Metric: s.Metric, Value: s.Value, Unit: s.Unit,
			ReceivedAt: receivedAt, Tags: tagsJSON,
		}); err != nil {
			return false
		}
	}
	return true
}

func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
