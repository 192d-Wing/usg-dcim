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
