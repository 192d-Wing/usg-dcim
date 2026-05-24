-- name: ListRacks :many
SELECT id, site_id, row_id, name, code, u_height,
       max_kw, max_weight_lbs, serial, created_at, updated_at
FROM racks
WHERE (sqlc.narg(site_id)::uuid IS NULL OR site_id = sqlc.narg(site_id))
  AND (sqlc.narg(row_id)::uuid  IS NULL OR row_id  = sqlc.narg(row_id))
ORDER BY code
LIMIT $1 OFFSET $2;

-- name: CountRacks :one
SELECT count(*)::bigint
FROM racks
WHERE (sqlc.narg(site_id)::uuid IS NULL OR site_id = sqlc.narg(site_id))
  AND (sqlc.narg(row_id)::uuid  IS NULL OR row_id  = sqlc.narg(row_id));

-- name: GetRack :one
SELECT id, site_id, row_id, name, code, u_height,
       max_kw, max_weight_lbs, serial, created_at, updated_at
FROM racks
WHERE id = $1;
