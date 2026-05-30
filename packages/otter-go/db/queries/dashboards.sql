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

-- ---- /dashboards/free-space (Phase 2) — rack capacity rollup ----
-- /free-space ranks racks by their biggest contiguous free U run,
-- optionally narrowing by site or region and rejecting racks that
-- have less kW headroom than the caller asked for. The implementation
-- is rack-level so the handler fans out one bulk asset lookup +
-- one bulk PDU-telemetry lookup, then computes the U/kW rollup in Go.

-- name: ListRacksForFreeSpace :many
-- Filter parameters can be NULL (don't filter). When both are set
-- Python ANDs them — match that.
SELECT id, site_id, row_id, name, code, u_height, max_kw, max_weight_lbs, serial, created_at, updated_at
FROM racks
WHERE ($1::uuid IS NULL OR site_id = $1::uuid)
  AND ($2::uuid IS NULL OR site_id IN (SELECT id FROM sites WHERE region_id = $2::uuid));

-- name: ListAssetsByRackIDs :many
-- Bulk asset fetch for many racks — Python iterates rack-by-rack and
-- runs one SELECT each; one round-trip is cheaper.
SELECT id, site_id, rack_id, parent_asset_id, name, hostname,
       kind::text AS kind, manufacturer, model, serial, firmware,
       rack_position_u, rack_units,
       face::text AS face, mount::text AS mount,
       pdu_side, psu_count, port_count,
       mgmt_ip, mgmt_protocol, mgmt_port, mgmt_credentials_ref,
       lifecycle_state::text AS lifecycle_state,
       install_date, warranty_expires, metadata_json,
       created_at, updated_at
FROM assets
WHERE rack_id = ANY($1::uuid[]);

-- ---- /dashboards/sites/at-risk (Phase 2b) — site alert counts ----
-- Ranks the 50 sites with the most firing alerts at or above the
-- caller-supplied severity. The alert_severity ENUM has intrinsic
-- ordering (info, warning, minor, major, critical) so the >= compare
-- works in PG once the parameter is cast.

-- name: ListSitesAtRisk :many
SELECT site_id, COUNT(id)::bigint AS alert_count
FROM alerts
WHERE state = 'firing'
  AND severity >= $1::alert_severity
GROUP BY site_id
ORDER BY COUNT(id) DESC
LIMIT 50;

-- ---- /dashboards/assets/{asset_id} (Phase 2c) — asset health view ----
-- The endpoint loads the asset entity + telemetry-source freshness +
-- bound IPs + 10 most-recent alerts. One round-trip per joined surface
-- so each query stays small enough to scan into its own projected row.

-- name: ListAssetTelemetrySources :many
-- ORDER BY metric asc matches Python's `order_by(metric.asc())`.
-- freshness ENUM cast to ::text for clean pgx scan; last_value is
-- FLOAT (not NUMERIC) so it scans straight into *float64.
SELECT metric, unit, source_system,
       freshness::text AS freshness,
       last_value, last_reading_at, last_success_at, poll_interval_seconds
FROM telemetry_sources
WHERE asset_id = $1
ORDER BY metric;

-- name: ListAssetIPAddresses :many
-- ORDER BY role asc, address asc matches Python. host(address)
-- strips the prefix length so finch gets the bare host string
-- (same trick the search handler uses).
SELECT id, subnet_id,
       host(address) AS address,
       role::text AS role, status::text AS status, source::text AS source,
       dns_name, description, dhcp_lease_expires_at
FROM ip_addresses
WHERE asset_id = $1
ORDER BY role, address;

-- name: ListRecentAssetAlerts :many
-- 10 most-recent alerts on the asset, ordered by last_seen_at desc.
SELECT id, severity::text AS severity, state::text AS state,
       summary, first_seen_at, last_seen_at
FROM alerts
WHERE asset_id = $1
ORDER BY last_seen_at DESC
LIMIT 10;

-- name: ListPduKwTelemetry :many
-- Per-PDU current-freshness telemetry rows whose metric is one of the
-- kW or W power metrics. The handler picks metric + last_value off
-- each row and rolls up by asset_id (rack).
SELECT asset_id, metric, last_value
FROM telemetry_sources
WHERE asset_id = ANY($1::uuid[])
  AND last_value IS NOT NULL
  AND freshness = 'current'
  AND metric IN (
    'pdu.input.kw', 'power.consumed.kW', 'rack.input.kw',
    'power.consumed.W', 'pdu.input.w'
  );
