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

-- name: GetNotificationChannel :one
-- Used by POST /notifications/channels/{id}/test to load the single
-- channel before running its filter + sender. Returns the same row
-- shape as ListNotificationChannels (severity enum demoted to text
-- so the Go side doesn't need to import the Postgres enum type).
SELECT id, name, kind::text AS kind, config_json,
       min_severity::text AS min_severity,
       notify_on_fire, notify_on_resolve, enabled,
       created_at, updated_at
FROM notification_channels
WHERE id = $1;
