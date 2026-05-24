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
