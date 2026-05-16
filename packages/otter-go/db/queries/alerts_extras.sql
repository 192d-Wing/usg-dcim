-- name: GetAlertRule :one
SELECT id, name, description, metric, operator, threshold, duration_seconds,
       severity::text AS severity, site_scope_id, asset_filter_json, enabled,
       runbook_url, created_at, updated_at
FROM alert_rules
WHERE id = $1;

-- name: ListMaintenanceWindows :many
SELECT id, name, site_id, asset_filter_json,
       starts_at, ends_at, created_by, reason, created_at, updated_at
FROM maintenance_windows
WHERE (sqlc.narg(site_id)::uuid       IS NULL OR site_id = sqlc.narg(site_id))
  AND (sqlc.narg(active_at)::timestamptz IS NULL OR (starts_at <= sqlc.narg(active_at) AND ends_at >= sqlc.narg(active_at)))
  AND (sqlc.narg(upcoming_after)::timestamptz IS NULL OR ends_at >= sqlc.narg(upcoming_after))
ORDER BY starts_at DESC
LIMIT $1 OFFSET $2;

-- name: CountMaintenanceWindows :one
SELECT count(*)::bigint
FROM maintenance_windows
WHERE (sqlc.narg(site_id)::uuid       IS NULL OR site_id = sqlc.narg(site_id))
  AND (sqlc.narg(active_at)::timestamptz IS NULL OR (starts_at <= sqlc.narg(active_at) AND ends_at >= sqlc.narg(active_at)))
  AND (sqlc.narg(upcoming_after)::timestamptz IS NULL OR ends_at >= sqlc.narg(upcoming_after));

-- name: GetMaintenanceWindow :one
SELECT id, name, site_id, asset_filter_json,
       starts_at, ends_at, created_by, reason, created_at, updated_at
FROM maintenance_windows
WHERE id = $1;
