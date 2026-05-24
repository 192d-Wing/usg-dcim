-- name: ListNotificationChannels :many
SELECT id, name, kind::text AS kind, config_json,
       severity_to_text(min_severity) AS min_severity,
       notify_on_fire, notify_on_resolve, enabled,
       created_at, updated_at
FROM notification_channels
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountNotificationChannels :one
SELECT count(*)::bigint FROM notification_channels;
