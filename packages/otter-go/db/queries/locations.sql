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
-- PR 96 — 2-hop scope filter: room → building → site → scope. The
-- handler passes site_ids = NULL when the principal is global, an
-- empty page when scope can't reach any site, or the concrete
-- ABAC-expanded site_id set otherwise (same shape as buildings).
SELECT r.id, r.building_id, r.name, r.code, r.floor_area_sqft,
       r.design_kw::text AS design_kw,
       r.design_cooling_tons::text AS design_cooling_tons,
       r.grid_cols, r.grid_rows,
       r.created_at, r.updated_at
FROM rooms r
JOIN buildings b ON b.id = r.building_id
WHERE (sqlc.narg(building_id)::uuid IS NULL OR r.building_id = sqlc.narg(building_id))
  AND (sqlc.narg(site_ids)::uuid[] IS NULL OR b.site_id = ANY(sqlc.narg(site_ids)::uuid[]))
ORDER BY r.code
LIMIT $1 OFFSET $2;

-- name: CountRooms :one
SELECT count(*)::bigint
FROM rooms r
JOIN buildings b ON b.id = r.building_id
WHERE (sqlc.narg(building_id)::uuid IS NULL OR r.building_id = sqlc.narg(building_id))
  AND (sqlc.narg(site_ids)::uuid[] IS NULL OR b.site_id = ANY(sqlc.narg(site_ids)::uuid[]));

-- name: ListRows :many
-- PR 96 — 3-hop scope filter: row → room → building → site → scope.
SELECT r.id, r.room_id, r.name, r.code, r.created_at, r.updated_at
FROM rows r
JOIN rooms rm ON rm.id = r.room_id
JOIN buildings b ON b.id = rm.building_id
WHERE (sqlc.narg(room_id)::uuid IS NULL OR r.room_id = sqlc.narg(room_id))
  AND (sqlc.narg(site_ids)::uuid[] IS NULL OR b.site_id = ANY(sqlc.narg(site_ids)::uuid[]))
ORDER BY r.code
LIMIT $1 OFFSET $2;

-- name: CountRows :one
SELECT count(*)::bigint
FROM rows r
JOIN rooms rm ON rm.id = r.room_id
JOIN buildings b ON b.id = rm.building_id
WHERE (sqlc.narg(room_id)::uuid IS NULL OR r.room_id = sqlc.narg(room_id))
  AND (sqlc.narg(site_ids)::uuid[] IS NULL OR b.site_id = ANY(sqlc.narg(site_ids)::uuid[]));
