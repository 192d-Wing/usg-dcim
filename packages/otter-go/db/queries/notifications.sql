-- name: ListNotificationChannels :many
SELECT *
FROM notification_channels
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountNotificationChannels :one
SELECT count(*)::bigint FROM notification_channels;

-- name: ListEnabledNotificationChannels :many
-- The notify_bridge cron uses this on every tick (every 5s) to fan
-- out a single alert across all enabled channels. Unpaginated by
-- design — the channel table is bounded by operator-defined config
-- (a fleet has at most tens of channels), and the cron walks every
-- one to filter via channelMatches.
SELECT *
FROM notification_channels
WHERE enabled = true
ORDER BY name;

-- name: GetNotificationChannel :one
-- Used by POST /notifications/channels/{id}/test to load the single
-- channel before running its filter + sender. Returns the same row
-- shape as ListNotificationChannels (severity enum demoted to text
-- so the Go side doesn't need to import the Postgres enum type).
SELECT *
FROM notification_channels
WHERE id = $1;
