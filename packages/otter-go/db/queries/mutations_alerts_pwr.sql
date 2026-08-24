-- ===== Alerts =====
-- name: AckAlert :one
UPDATE alerts
SET state = 'acked'::alert_state,
    acked_by = sqlc.arg(acked_by)::text,
    acked_at = NOW(),
    updated_at = NOW()
WHERE id = sqlc.arg(id)
RETURNING *;

-- ===== Alert rules =====
-- name: CreateAlertRule :one
INSERT INTO alert_rules (id, name, description, metric, operator, threshold,
                         duration_seconds, severity, site_scope_id, asset_filter_json,
                         enabled, runbook_url, created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(name), sqlc.arg(description), sqlc.arg(metric), sqlc.arg(operator), sqlc.arg(threshold),
        sqlc.arg(duration_seconds), sqlc.arg(severity), sqlc.arg(site_scope_id), COALESCE(sqlc.narg(asset_filter_json)::jsonb, '{}'::jsonb),
        sqlc.arg(enabled), sqlc.arg(runbook_url), NOW(), NOW())
RETURNING *;

-- name: UpdateAlertRule :one
UPDATE alert_rules
SET name             = COALESCE(sqlc.narg(name)::text, name),
    description      = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    metric           = COALESCE(sqlc.narg(metric)::text, metric),
    operator         = COALESCE(sqlc.narg(operator)::text, operator),
    threshold        = COALESCE(sqlc.narg(threshold)::float, threshold),
    duration_seconds = COALESCE(sqlc.narg(duration_seconds)::int, duration_seconds),
    -- ::alert_severity, not ::text: the column is an enum and
    -- COALESCE cannot mix text with it — fails at plan time.
    severity         = COALESCE(sqlc.narg(severity)::alert_severity, severity),
    site_scope_id    = CASE WHEN sqlc.arg(site_set)::bool       THEN sqlc.narg(site_scope_id)::uuid    ELSE site_scope_id END,
    -- ::json, not ::jsonb: the column is json and COALESCE cannot
    -- implicitly unify jsonb with json — ::jsonb fails at plan time.
    asset_filter_json = COALESCE(sqlc.narg(asset_filter_json)::json, asset_filter_json),
    enabled          = COALESCE(sqlc.narg(enabled)::bool, enabled),
    runbook_url      = CASE WHEN sqlc.arg(runbook_set)::bool    THEN sqlc.narg(runbook_url)::text     ELSE runbook_url END,
    updated_at       = NOW()
WHERE id = $1
RETURNING *;

-- name: DeleteAlertRule :exec
DELETE FROM alert_rules WHERE id = $1;

-- ===== Maintenance windows =====
-- name: CreateMaintenanceWindow :one
INSERT INTO maintenance_windows (id, name, site_id, asset_filter_json,
                                 starts_at, ends_at, created_by, reason,
                                 created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(name), sqlc.arg(site_id), COALESCE(sqlc.narg(asset_filter_json)::jsonb, '{}'::jsonb),
        sqlc.arg(starts_at), sqlc.arg(ends_at), sqlc.arg(created_by), sqlc.arg(reason), NOW(), NOW())
RETURNING *;

-- name: UpdateMaintenanceWindow :one
UPDATE maintenance_windows
SET name              = COALESCE(sqlc.narg(name)::text, name),
    site_id           = CASE WHEN sqlc.arg(site_set)::bool   THEN sqlc.narg(site_id)::uuid    ELSE site_id END,
    -- ::json, not ::jsonb: the column is json and COALESCE cannot
    -- implicitly unify jsonb with json — ::jsonb fails at plan time.
    asset_filter_json = COALESCE(sqlc.narg(asset_filter_json)::json, asset_filter_json),
    starts_at         = COALESCE(sqlc.narg(starts_at)::timestamptz, starts_at),
    ends_at           = COALESCE(sqlc.narg(ends_at)::timestamptz, ends_at),
    reason            = CASE WHEN sqlc.arg(reason_set)::bool THEN sqlc.narg(reason)::text     ELSE reason END,
    updated_at        = NOW()
WHERE id = $1
RETURNING *;

-- name: DeleteMaintenanceWindow :exec
DELETE FROM maintenance_windows WHERE id = $1;

-- ===== Notification channels =====
-- name: CreateNotificationChannel :one
INSERT INTO notification_channels (id, name, kind, config_json, min_severity,
                                   notify_on_fire, notify_on_resolve, enabled,
                                   created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(name), sqlc.arg(kind), COALESCE(sqlc.narg(config_json)::jsonb, '{}'::jsonb), sqlc.arg(min_severity),
        sqlc.arg(notify_on_fire), sqlc.arg(notify_on_resolve), sqlc.arg(enabled), NOW(), NOW())
RETURNING *;

-- name: UpdateNotificationChannel :one
UPDATE notification_channels
SET name              = COALESCE(sqlc.narg(name)::text, name),
    config_json       = COALESCE(sqlc.narg(config_json)::jsonb, config_json),
    -- ::alert_severity, not ::text — enum, same as severity above.
    min_severity      = COALESCE(sqlc.narg(min_severity)::alert_severity, min_severity),
    notify_on_fire    = COALESCE(sqlc.narg(notify_on_fire)::bool, notify_on_fire),
    notify_on_resolve = COALESCE(sqlc.narg(notify_on_resolve)::bool, notify_on_resolve),
    enabled           = COALESCE(sqlc.narg(enabled)::bool, enabled),
    updated_at        = NOW()
WHERE id = $1
RETURNING *;

-- name: DeleteNotificationChannel :exec
DELETE FROM notification_channels WHERE id = $1;

-- ===== Power: outlet connect/disconnect =====
-- name: GetOutletByID :one
SELECT id, pdu_asset_id, position, label, phase, max_amps, receptacle, created_at, updated_at
FROM outlets WHERE id = $1;

-- name: GetPowerConnectionByOutlet :one
SELECT id, outlet_id, asset_id, psu_index, cord_color, cord_length_m, created_at, updated_at
FROM power_connections WHERE outlet_id = $1;

-- name: CreatePowerConnection :one
INSERT INTO power_connections (id, outlet_id, asset_id, psu_index, cord_color, cord_length_m, created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(outlet_id), sqlc.arg(asset_id), sqlc.arg(psu_index), sqlc.arg(cord_color), sqlc.narg(cord_length_m)::numeric, NOW(), NOW())
RETURNING id, outlet_id, asset_id, psu_index, cord_color, cord_length_m, created_at, updated_at;

-- name: DeleteOutletConnection :exec
DELETE FROM power_connections WHERE outlet_id = $1;
