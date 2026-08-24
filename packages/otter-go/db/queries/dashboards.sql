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
  AND (last_seen_at IS NULL OR last_seen_at < sqlc.arg(last_seen_at)::timestamptz);

-- name: CountStaleTelemetrySources :one
-- Telemetry sources whose freshness state is 'stale'. The freshness
-- column is a Postgres ENUM; an explicit text comparison works without
-- the ::text cast because we're comparing to a literal, not scanning.
SELECT COUNT(id) FROM telemetry_sources WHERE freshness = 'stale';

-- ---- /dashboards/forecast/* (Phase 3) — capacity forecasting ----
-- Forecasts U-fill at growth rate (linear regression on cumulative U
-- over time) + kW trend (OLS on daily-averaged PDU telemetry from the
-- TimescaleDB hypertable).

-- name: ListRacksForForecast :many
-- Optional site_id filter; limit caps result count. Caller iterates
-- the batch with compute_rack_forecast (no DB calls beyond this).
SELECT *
FROM racks
WHERE (sqlc.narg(site_id)::uuid IS NULL OR site_id = sqlc.narg(site_id)::uuid)
LIMIT sqlc.arg(result_limit);

-- name: ListKwHistorySamples :many
-- TimescaleDB time_bucket — daily AVG(value) per (day, metric) for the
-- PDU assets in the rack. metrics filter restricts to the kW/W power
-- metric set. Window bounded by (start, end) so the caller controls
-- the regression's days_back parameter.
SELECT
    time_bucket('1 day', ts)::timestamptz AS day,
    metric,
    AVG(value)::double precision AS avg_v
FROM telemetry_samples
WHERE asset_id = ANY(sqlc.arg(asset_ids)::uuid[])
  AND metric = ANY(sqlc.arg(metrics)::text[])
  AND ts >= sqlc.arg(start_ts)
  AND ts <= sqlc.arg(end_ts)
GROUP BY day, metric
ORDER BY day;

-- ---- /dashboards/racks/{rack_id} (Phase 2e) — rack detail view ----
-- Rack entity + ordered placed assets + per-asset open-alerts count +
-- per-asset telemetry freshness + power_chain rollup. compute_rack_
-- capacity reuses internal/capacity.

-- name: ListAssetsByRackOrdered :many
-- Order by rack_position_u asc nulls last (Python parity for the rack
-- visualization). ENUMs cast to ::text per convention.
SELECT *
FROM assets
WHERE rack_id = sqlc.arg(rack_id)::uuid
ORDER BY rack_position_u ASC NULLS LAST;

-- name: ListOpenAlertsByAssetIDs :many
-- Aggregation for the rack detail's per-asset open_alerts column.
-- Group by asset_id; only firing alerts count.
SELECT asset_id, COUNT(id)::bigint AS n
FROM alerts
WHERE asset_id = ANY($1::uuid[]) AND state = 'firing'
GROUP BY asset_id;

-- name: ListAssetFreshnessByIDs :many
-- Per-asset telemetry freshness counts. Caller pivots into the
-- {asset_id: {freshness: count}} nested map the response shape uses.
SELECT asset_id, freshness::text AS freshness, COUNT(*)::bigint AS n
FROM telemetry_sources
WHERE asset_id = ANY($1::uuid[])
GROUP BY asset_id, freshness;

-- name: ListOutletsByPduIDs :many
-- Outlets for the rack's PDUs — feeds compute_power_chain. The Python
-- side projected the full Outlet row; the Go port only needs id +
-- pdu_asset_id + position + label.
SELECT id, pdu_asset_id, position, label
FROM outlets
WHERE pdu_asset_id = ANY($1::uuid[]);

-- ListPowerConnectionsByOutletIDs is already declared in power.sql
-- (returns []PowerConnection); the rack-detail handler reuses that
-- query directly.

-- ---- /dashboards/sites/{site_id} (Phase 2d) — site topology + KPIs ----
-- Fans out a half-dozen queries: site topology (buildings → rooms →
-- rows → racks + all assets), the alerts/collectors KPI counters, and
-- the per-rack capacity rollup via internal/capacity.

-- name: ListBuildingsBySite :many
SELECT id, name, code FROM buildings
WHERE site_id = $1
ORDER BY code;

-- name: ListRoomsByBuildingIDs :many
-- design_kw / design_cooling_tons are NUMERIC → *string via the
-- sqlc override (a ::text cast would erase nullability); caller
-- parses to float at the response boundary (matches the max_kw
-- NUMERIC pattern in racks).
SELECT id, building_id, name, code, design_kw,
       floor_area_sqft,
       design_cooling_tons,
       grid_cols, grid_rows
FROM rooms
WHERE building_id = ANY($1::uuid[])
ORDER BY code;

-- name: ListRowsByRoomIDs :many
SELECT id, room_id, name, code FROM rows
WHERE room_id = ANY($1::uuid[])
ORDER BY code;

-- name: ListRacksByRowIDs :many
-- Rack anchor for /dashboards/buildings/{building_id} — same
-- projection as ListRacksBySite but keyed on the building's rows so
-- the fan-out stays scoped to one building.
SELECT *
FROM racks
WHERE row_id = ANY($1::uuid[])
ORDER BY code;

-- name: ListRacksBySite :many
SELECT *
FROM racks
WHERE site_id = $1
ORDER BY code;

-- name: ListAssetsBySite :many
-- Same projection as ListAssetsByRackIDs but anchored to a single
-- site. ENUMs cast to ::text matching the codebase convention.
SELECT *
FROM assets
WHERE site_id = $1;

-- name: ListSiteAlertsBySeverity :many
-- Aggregation for the site KPI block — firing alert counts grouped
-- by severity. Caller fans out into the `{severity: count}` dict +
-- the rollup `total` field.
SELECT severity::text AS severity, COUNT(id)::bigint AS n
FROM alerts
WHERE site_id = sqlc.arg(site_id)::uuid AND state = 'firing'
GROUP BY severity;

-- name: ListSiteCollectors :many
-- Per-site collector list. Caller computes the by-status breakdown +
-- the stale count using its own stale_threshold (same env var the
-- enterprise overview reads).
SELECT id, status::text AS status, enabled, last_seen_at
FROM collectors
WHERE site_id = $1;

-- ---- /dashboards/free-space (Phase 2) — rack capacity rollup ----
-- /free-space ranks racks by their biggest contiguous free U run,
-- optionally narrowing by site or region and rejecting racks that
-- have less kW headroom than the caller asked for. The implementation
-- is rack-level so the handler fans out one bulk asset lookup +
-- one bulk PDU-telemetry lookup, then computes the U/kW rollup in Go.

-- name: ListRacksForFreeSpace :many
-- Filter parameters can be NULL (don't filter). When both are set
-- Python ANDs them — match that.
SELECT *
FROM racks
WHERE (sqlc.narg(site_id)::uuid IS NULL OR site_id = sqlc.narg(site_id)::uuid)
  AND (sqlc.narg(region_id)::uuid IS NULL
       OR site_id IN (SELECT id FROM sites WHERE region_id = sqlc.narg(region_id)::uuid));

-- name: ListAssetsByRackIDs :many
-- Bulk asset fetch for many racks — Python iterates rack-by-rack and
-- runs one SELECT each; one round-trip is cheaper.
SELECT *
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
WHERE asset_id = sqlc.arg(asset_id)::uuid
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
WHERE asset_id = sqlc.arg(asset_id)::uuid
ORDER BY role, address;

-- name: ListRecentAssetAlerts :many
-- 10 most-recent alerts on the asset, ordered by last_seen_at desc.
SELECT id, severity::text AS severity, state::text AS state,
       summary, first_seen_at, last_seen_at
FROM alerts
WHERE asset_id = sqlc.arg(asset_id)::uuid
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
