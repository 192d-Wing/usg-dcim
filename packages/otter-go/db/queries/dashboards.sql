-- Enterprise dashboard rollup queries — Phase 1 of the dashboards
-- port (mirrors packages/otter/src/dcim/api/dashboards.py::
-- enterprise_overview). One scalar COUNT per cell so each query stays
-- small enough to scan into an int64 without a generated row type.
-- The handler runs them sequentially against a single pgx connection
-- (Python AsyncSession layout).
--
-- Site + rack counts reuse the existing CountSites / CountRacks
-- queries (zero-value params struct → global unscoped count;
-- LifecycleState=&"active" → active-only). Only the dashboard-
-- specific aggregates are declared here.

-- name: CountSitesWithCriticalAlerts :one
-- Distinct site_id with at least one firing critical alert. Mirrors
-- Python's COUNT(DISTINCT Alert.site_id) WHERE state='firing' AND
-- severity='critical'.
SELECT COUNT(DISTINCT site_id)
FROM alerts
WHERE state = 'firing' AND severity = 'critical';

-- name: CountHealthyCollectors :one
SELECT COUNT(id) FROM collectors WHERE status = 'healthy';

-- name: CountStaleCollectors :one
-- A collector is stale when enabled and either never reported or hasn't
-- been seen since the stale threshold (now - collector_stale_seconds).
-- Threshold passed in by the handler so the query stays deterministic
-- and the env-backed setting is read once per request.
SELECT COUNT(id)
FROM collectors
WHERE enabled = TRUE
  AND (last_seen_at IS NULL OR last_seen_at < $1);

-- name: CountStaleTelemetrySources :one
-- Telemetry sources whose freshness state is 'stale'. The freshness
-- column is a Postgres ENUM; an explicit text comparison works without
-- the ::text cast because we're comparing to a literal, not scanning.
SELECT COUNT(id) FROM telemetry_sources WHERE freshness = 'stale';
