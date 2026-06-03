-- name: FlipStaleTelemetrySources :exec
-- Scheduler job (Go port of Python's freshness_sweep in worker.py:55).
-- Flips current → stale for sources whose last_success_at is older
-- than max(60s, poll_interval_seconds * 3). The single UPDATE replaces
-- Python's load-all-rows-then-loop pattern; rowcount is returned so
-- the scheduler can structure-log it.
UPDATE telemetry_sources
SET freshness  = 'stale'::freshness_state,
    updated_at = NOW()
WHERE freshness        = 'current'::freshness_state
  AND last_success_at IS NOT NULL
  AND last_success_at < NOW() - (GREATEST(60, poll_interval_seconds * 3) || ' seconds')::interval;

-- name: UpsertTelemetrySource :exec
-- Per-asset/metric freshness row. Python's _update_freshness in
-- services/telemetry.py does a SELECT-then-INSERT-or-UPDATE; this
-- collapses to one statement. ON CONFLICT (asset_id, metric)
-- matches the uq_telem_source_asset_metric constraint. unit is
-- only overwritten when the caller passes a non-null value to
-- mirror Python's `existing.unit = s.unit or existing.unit`.
INSERT INTO telemetry_sources (id, site_id, asset_id, collector_id,
                               metric, unit, freshness,
                               last_success_at, last_reading_at, last_value,
                               poll_interval_seconds, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3,
        $4, $5, 'current'::freshness_state,
        $6::timestamptz, $7::timestamptz, $8::float,
        -- Match Python's TelemetrySource model default of 60s
        -- (models/telemetry_meta.py). The freshness scheduler's
        -- formula is GREATEST(60, poll_interval_seconds * 3), so
        -- this column is load-bearing on first-write — literal 0
        -- would flip new sources stale 3x faster than Python (60s
        -- vs 180s).
        60, NOW(), NOW())
ON CONFLICT (asset_id, metric) DO UPDATE
SET collector_id     = EXCLUDED.collector_id,
    -- NULLIF treats incoming "" the same as NULL so Python's
    -- `existing.unit = s.unit or existing.unit` (truthy check)
    -- parity holds even on bogus "" payloads.
    unit             = COALESCE(NULLIF(EXCLUDED.unit, ''), telemetry_sources.unit),
    last_success_at  = EXCLUDED.last_success_at,
    last_reading_at  = EXCLUDED.last_reading_at,
    last_value       = EXCLUDED.last_value,
    freshness        = 'current'::freshness_state,
    updated_at       = NOW();

-- name: InsertTelemetrySample :exec
-- Per-sample INSERT with ON CONFLICT DO NOTHING on the dedup
-- constraint (collector_id, batch_id, seq, ts). A retry of the
-- same batch is a no-op; matches Python's
-- `pg_insert.on_conflict_do_nothing(constraint=...)`. The JSON
-- ingest path is the low-throughput fallback (heron owns the
-- volume via mTLS) so per-row INSERT is fine here — we don't
-- need the bulk VALUES form.
INSERT INTO telemetry_samples (ts, site_id, asset_id, collector_id,
                               batch_id, seq, metric, value, unit,
                               received_at, tags)
VALUES ($1::timestamptz, $2, $3, $4,
        $5, $6::int, $7, $8::float, $9,
        $10::timestamptz, $11::jsonb)
ON CONFLICT ON CONSTRAINT uq_telem_sample_dedup DO NOTHING;

-- name: GetTelemetrySeries :many
-- Window query against the TimescaleDB hypertable. The
-- (asset_id, metric, ts) index from migration 0046 covers this directly.
-- LIMIT 10000 matches the prior OpenSearch ceiling so the frontend's
-- chart-rendering assumptions don't change.
SELECT ts, value
FROM telemetry_samples
WHERE site_id  = $1
  AND asset_id = $2
  AND metric   = $3
  AND ts >= $4
  AND ts <= $5
ORDER BY ts ASC
LIMIT 10000;
