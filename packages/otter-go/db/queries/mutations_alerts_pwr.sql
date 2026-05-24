-- ===== Alerts =====
-- name: AckAlert :one
UPDATE alerts
SET state = 'acked'::alert_state,
    acked_by = $2,
    acked_at = NOW(),
    updated_at = NOW()
WHERE id = $1
RETURNING id, rule_id, site_id, asset_id, collector_id, severity, state::text AS state,
          dedupe_key, correlation_key, summary, detail,
          first_seen_at, last_seen_at, acked_by, acked_at, resolved_at,
          labels_json, created_at, updated_at;

-- ===== Alert rules =====
-- name: CreateAlertRule :one
INSERT INTO alert_rules (id, name, description, metric, operator, threshold,
                         duration_seconds, severity, site_scope_id, asset_filter_json,
                         enabled, runbook_url, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5,
        $6, $7, $8, COALESCE($9::jsonb, '{}'::jsonb),
        $10, $11, NOW(), NOW())
RETURNING id, name, description, metric, operator, threshold,
          duration_seconds, severity, site_scope_id, asset_filter_json,
          enabled, runbook_url, created_at, updated_at;

-- name: UpdateAlertRule :one
UPDATE alert_rules
SET name             = COALESCE(sqlc.narg(name)::text, name),
    description      = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    metric           = COALESCE(sqlc.narg(metric)::text, metric),
    operator         = COALESCE(sqlc.narg(operator)::text, operator),
    threshold        = COALESCE(sqlc.narg(threshold)::float, threshold),
    duration_seconds = COALESCE(sqlc.narg(duration_seconds)::int, duration_seconds),
    severity         = COALESCE(sqlc.narg(severity)::text, severity),
    site_scope_id    = CASE WHEN sqlc.arg(site_set)::bool       THEN sqlc.narg(site_scope_id)::uuid    ELSE site_scope_id END,
    asset_filter_json = COALESCE(sqlc.narg(asset_filter_json)::jsonb, asset_filter_json),
    enabled          = COALESCE(sqlc.narg(enabled)::bool, enabled),
    runbook_url      = CASE WHEN sqlc.arg(runbook_set)::bool    THEN sqlc.narg(runbook_url)::text     ELSE runbook_url END,
    updated_at       = NOW()
WHERE id = $1
RETURNING id, name, description, metric, operator, threshold,
          duration_seconds, severity, site_scope_id, asset_filter_json,
          enabled, runbook_url, created_at, updated_at;

-- name: DeleteAlertRule :exec
DELETE FROM alert_rules WHERE id = $1;

-- ===== Maintenance windows =====
-- name: CreateMaintenanceWindow :one
INSERT INTO maintenance_windows (id, name, site_id, asset_filter_json,
                                 starts_at, ends_at, created_by, reason,
                                 created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, COALESCE($3::jsonb, '{}'::jsonb),
        $4, $5, $6, $7, NOW(), NOW())
RETURNING id, name, site_id, asset_filter_json,
          starts_at, ends_at, created_by, reason, created_at, updated_at;

-- name: UpdateMaintenanceWindow :one
UPDATE maintenance_windows
SET name              = COALESCE(sqlc.narg(name)::text, name),
    site_id           = CASE WHEN sqlc.arg(site_set)::bool   THEN sqlc.narg(site_id)::uuid    ELSE site_id END,
    asset_filter_json = COALESCE(sqlc.narg(asset_filter_json)::jsonb, asset_filter_json),
    starts_at         = COALESCE(sqlc.narg(starts_at)::timestamptz, starts_at),
    ends_at           = COALESCE(sqlc.narg(ends_at)::timestamptz, ends_at),
    reason            = CASE WHEN sqlc.arg(reason_set)::bool THEN sqlc.narg(reason)::text     ELSE reason END,
    updated_at        = NOW()
WHERE id = $1
RETURNING id, name, site_id, asset_filter_json,
          starts_at, ends_at, created_by, reason, created_at, updated_at;

-- name: DeleteMaintenanceWindow :exec
DELETE FROM maintenance_windows WHERE id = $1;

-- ===== Notification channels =====
-- name: CreateNotificationChannel :one
INSERT INTO notification_channels (id, name, kind, config_json, min_severity,
                                   notify_on_fire, notify_on_resolve, enabled,
                                   created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, COALESCE($3::jsonb, '{}'::jsonb), $4,
        $5, $6, $7, NOW(), NOW())
RETURNING id, name, kind, config_json, min_severity,
          notify_on_fire, notify_on_resolve, enabled, created_at, updated_at;

-- name: UpdateNotificationChannel :one
UPDATE notification_channels
SET name              = COALESCE(sqlc.narg(name)::text, name),
    config_json       = COALESCE(sqlc.narg(config_json)::jsonb, config_json),
    min_severity      = COALESCE(sqlc.narg(min_severity)::text, min_severity),
    notify_on_fire    = COALESCE(sqlc.narg(notify_on_fire)::bool, notify_on_fire),
    notify_on_resolve = COALESCE(sqlc.narg(notify_on_resolve)::bool, notify_on_resolve),
    enabled           = COALESCE(sqlc.narg(enabled)::bool, enabled),
    updated_at        = NOW()
WHERE id = $1
RETURNING id, name, kind, config_json, min_severity,
          notify_on_fire, notify_on_resolve, enabled, created_at, updated_at;

-- name: DeleteNotificationChannel :exec
DELETE FROM notification_channels WHERE id = $1;

-- ===== Power: outlet connect/disconnect =====
-- name: CreatePowerConnection :one
INSERT INTO power_connections (id, outlet_id, asset_id, psu_index, cord_color, cord_length_m, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5::numeric, NOW(), NOW())
RETURNING id, outlet_id, asset_id, psu_index, cord_color, cord_length_m, created_at, updated_at;

-- name: DeleteOutletConnection :exec
DELETE FROM power_connections WHERE outlet_id = $1;
