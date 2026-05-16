-- Three small geographic sub-resources, grouped because each is a
-- one-list-one-get under sites and the SQL shapes are nearly identical.

-- name: ListBuildings :many
SELECT id, site_id, name, code, created_at, updated_at
FROM buildings
WHERE (sqlc.narg(site_id)::uuid IS NULL OR site_id = sqlc.narg(site_id))
  AND (sqlc.narg(site_ids)::uuid[] IS NULL OR site_id = ANY(sqlc.narg(site_ids)::uuid[]))
ORDER BY code
LIMIT $1 OFFSET $2;

-- name: CountBuildings :one
SELECT count(*)::bigint
FROM buildings
WHERE (sqlc.narg(site_id)::uuid IS NULL OR site_id = sqlc.narg(site_id))
  AND (sqlc.narg(site_ids)::uuid[] IS NULL OR site_id = ANY(sqlc.narg(site_ids)::uuid[]));

-- name: ListRooms :many
SELECT id, building_id, name, code, floor_area_sqft, created_at, updated_at
FROM rooms
WHERE (sqlc.narg(building_id)::uuid IS NULL OR building_id = sqlc.narg(building_id))
ORDER BY code
LIMIT $1 OFFSET $2;

-- name: CountRooms :one
SELECT count(*)::bigint
FROM rooms
WHERE (sqlc.narg(building_id)::uuid IS NULL OR building_id = sqlc.narg(building_id));

-- name: ListRows :many
SELECT id, room_id, name, code, created_at, updated_at
FROM rows
WHERE (sqlc.narg(room_id)::uuid IS NULL OR room_id = sqlc.narg(room_id))
ORDER BY code
LIMIT $1 OFFSET $2;

-- name: CountRows :one
SELECT count(*)::bigint
FROM rows
WHERE (sqlc.narg(room_id)::uuid IS NULL OR room_id = sqlc.narg(room_id));
