-- name: ListCollectors :many
SELECT id, site_id, name, version, mtls_fingerprint,
       status::text AS status, capabilities AS capabilities,
       last_seen_at, last_ingest_at, buffered_samples, enabled,
       config_overrides, created_at, updated_at
FROM collectors
WHERE (sqlc.narg(site_id)::uuid IS NULL OR site_id = sqlc.narg(site_id))
  AND (sqlc.narg(status)::text  IS NULL OR status::text = sqlc.narg(status))
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;

-- name: CountCollectors :one
SELECT count(*)::bigint FROM collectors
WHERE (sqlc.narg(site_id)::uuid IS NULL OR site_id = sqlc.narg(site_id))
  AND (sqlc.narg(status)::text  IS NULL OR status::text = sqlc.narg(status));

-- name: GetCollector :one
SELECT id, site_id, name, version, mtls_fingerprint,
       status::text AS status, capabilities AS capabilities,
       last_seen_at, last_ingest_at, buffered_samples, enabled,
       config_overrides, created_at, updated_at
FROM collectors
WHERE id = $1;
