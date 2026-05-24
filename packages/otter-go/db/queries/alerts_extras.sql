-- name: GetAlertRule :one
SELECT id, name, description, metric, operator, threshold, duration_seconds,
       severity::text AS severity, site_scope_id, asset_filter_json, enabled,
       runbook_url, created_at, updated_at
FROM alert_rules
WHERE id = $1;

-- ListMaintenanceWindows scope_site_ids semantic mirrors ListAlertRules
-- in audit_alerts.sql: NULL site_id is "enterprise-default window"
-- (applies to every site → visible to all scoped admins), so the
-- filter matches site_id IN scope OR site_id IS NULL. Mutate path
-- (PR 59) is restrictive — only global can touch null-site windows.
-- name: ListMaintenanceWindows :many
SELECT id, name, site_id, asset_filter_json,
       starts_at, ends_at, created_by, reason, created_at, updated_at
FROM maintenance_windows
WHERE (sqlc.narg(site_id)::uuid       IS NULL OR site_id = sqlc.narg(site_id))
  AND (sqlc.narg(active_at)::timestamptz IS NULL OR (starts_at <= sqlc.narg(active_at) AND ends_at >= sqlc.narg(active_at)))
  AND (sqlc.narg(upcoming_after)::timestamptz IS NULL OR ends_at >= sqlc.narg(upcoming_after))
  AND (sqlc.narg(scope_site_ids)::uuid[] IS NULL
       OR site_id IS NULL
       OR site_id = ANY(sqlc.narg(scope_site_ids)::uuid[]))
ORDER BY starts_at DESC
LIMIT $1 OFFSET $2;

-- name: CountMaintenanceWindows :one
SELECT count(*)::bigint
FROM maintenance_windows
WHERE (sqlc.narg(site_id)::uuid       IS NULL OR site_id = sqlc.narg(site_id))
  AND (sqlc.narg(active_at)::timestamptz IS NULL OR (starts_at <= sqlc.narg(active_at) AND ends_at >= sqlc.narg(active_at)))
  AND (sqlc.narg(upcoming_after)::timestamptz IS NULL OR ends_at >= sqlc.narg(upcoming_after))
  AND (sqlc.narg(scope_site_ids)::uuid[] IS NULL
       OR site_id IS NULL
       OR site_id = ANY(sqlc.narg(scope_site_ids)::uuid[]));

-- name: GetMaintenanceWindow :one
SELECT id, name, site_id, asset_filter_json,
       starts_at, ends_at, created_by, reason, created_at, updated_at
FROM maintenance_windows
WHERE id = $1;
