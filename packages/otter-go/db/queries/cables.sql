-- name: ListCables :many
SELECT id, site_id, a_asset_id, a_port, b_asset_id, b_port,
       medium, color, length_m, label, face, created_at, updated_at
FROM cables
WHERE (sqlc.narg(site_id)::uuid  IS NULL OR site_id      = sqlc.narg(site_id))
  AND (sqlc.narg(asset_id)::uuid IS NULL OR (a_asset_id = sqlc.narg(asset_id) OR b_asset_id = sqlc.narg(asset_id)))
  -- rack_id filter pushed into SQL via a subquery on assets.rack_id.
  AND (sqlc.narg(rack_id)::uuid  IS NULL OR (
        a_asset_id IN (SELECT id FROM assets WHERE rack_id = sqlc.narg(rack_id))
     OR b_asset_id IN (SELECT id FROM assets WHERE rack_id = sqlc.narg(rack_id))
  ))
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;

-- name: CountCables :one
SELECT count(*)::bigint
FROM cables
WHERE (sqlc.narg(site_id)::uuid  IS NULL OR site_id      = sqlc.narg(site_id))
  AND (sqlc.narg(asset_id)::uuid IS NULL OR (a_asset_id = sqlc.narg(asset_id) OR b_asset_id = sqlc.narg(asset_id)))
  AND (sqlc.narg(rack_id)::uuid  IS NULL OR (
        a_asset_id IN (SELECT id FROM assets WHERE rack_id = sqlc.narg(rack_id))
     OR b_asset_id IN (SELECT id FROM assets WHERE rack_id = sqlc.narg(rack_id))
  ));

-- name: GetCable :one
SELECT id, site_id, a_asset_id, a_port, b_asset_id, b_port,
       medium, color, length_m, label, face, created_at, updated_at
FROM cables
WHERE id = $1;
