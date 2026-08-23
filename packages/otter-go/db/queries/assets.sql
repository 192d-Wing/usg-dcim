-- name: ListAssets :many
SELECT *
FROM assets
WHERE (sqlc.narg(site_id)::uuid          IS NULL OR site_id        = sqlc.narg(site_id))
  AND (sqlc.narg(rack_id)::uuid          IS NULL OR rack_id        = sqlc.narg(rack_id))
  AND (sqlc.narg(kind)::text             IS NULL OR kind::text     = sqlc.narg(kind))
  AND (sqlc.narg(lifecycle_state)::text  IS NULL OR lifecycle_state::text = sqlc.narg(lifecycle_state))
  AND (sqlc.narg(serial)::text           IS NULL OR serial         = sqlc.narg(serial))
  AND (sqlc.narg(hostname)::text         IS NULL OR hostname       = sqlc.narg(hostname))
  AND (sqlc.narg(scope_site_ids)::uuid[] IS NULL OR site_id        = ANY(sqlc.narg(scope_site_ids)::uuid[]))
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountAssets :one
SELECT count(*)::bigint
FROM assets
WHERE (sqlc.narg(site_id)::uuid          IS NULL OR site_id        = sqlc.narg(site_id))
  AND (sqlc.narg(rack_id)::uuid          IS NULL OR rack_id        = sqlc.narg(rack_id))
  AND (sqlc.narg(kind)::text             IS NULL OR kind::text     = sqlc.narg(kind))
  AND (sqlc.narg(lifecycle_state)::text  IS NULL OR lifecycle_state::text = sqlc.narg(lifecycle_state))
  AND (sqlc.narg(serial)::text           IS NULL OR serial         = sqlc.narg(serial))
  AND (sqlc.narg(hostname)::text         IS NULL OR hostname       = sqlc.narg(hostname))
  AND (sqlc.narg(scope_site_ids)::uuid[] IS NULL OR site_id        = ANY(sqlc.narg(scope_site_ids)::uuid[]));

-- name: GetAsset :one
SELECT *
FROM assets
WHERE id = $1;
