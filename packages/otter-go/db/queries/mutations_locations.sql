-- ===== Regions =====
-- name: CreateRegion :one
INSERT INTO regions (id, name, code, description, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, NOW(), NOW())
RETURNING id, name, code, description, created_at, updated_at;

-- name: UpdateRegion :one
UPDATE regions
SET name        = COALESCE(sqlc.narg(name)::text, name),
    description = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    updated_at  = NOW()
WHERE id = $1
RETURNING id, name, code, description, created_at, updated_at;

-- ===== Sites =====
-- PR 92 retired the legacy `organization` string column; new and
-- existing callers use organization_id (UUID FK to organizations.id)
-- exclusively.
-- name: CreateSite :one
INSERT INTO sites (id, region_id, name, code, address, latitude, longitude,
                   timezone, majcom, organization_id, mission_owner,
                   enclave, classification, lifecycle_state, metadata_json,
                   created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6,
        $7, $8, $9, $10,
        $11, $12, $13::lifecycle_state, COALESCE($14::jsonb, '{}'::jsonb),
        NOW(), NOW())
RETURNING id, region_id, name, code, address, latitude, longitude,
          timezone, majcom, organization_id, mission_owner,
          enclave, classification, lifecycle_state, metadata_json,
          created_at, updated_at;

-- name: UpdateSite :one
UPDATE sites
SET name           = COALESCE(sqlc.narg(name)::text, name),
    address        = CASE WHEN sqlc.arg(address_set)::bool          THEN sqlc.narg(address)::text          ELSE address END,
    majcom         = CASE WHEN sqlc.arg(majcom_set)::bool           THEN sqlc.narg(majcom)::text           ELSE majcom END,
    organization_id = CASE WHEN sqlc.arg(organization_id_set)::bool THEN sqlc.narg(organization_id)::uuid  ELSE organization_id END,
    mission_owner  = CASE WHEN sqlc.arg(mission_owner_set)::bool    THEN sqlc.narg(mission_owner)::text    ELSE mission_owner END,
    enclave        = CASE WHEN sqlc.arg(enclave_set)::bool          THEN sqlc.narg(enclave)::text          ELSE enclave END,
    lifecycle_state = COALESCE(sqlc.narg(lifecycle_state)::lifecycle_state, lifecycle_state),
    metadata_json  = COALESCE(sqlc.narg(metadata_json)::jsonb, metadata_json),
    updated_at     = NOW()
WHERE id = $1
RETURNING id, region_id, name, code, address, latitude, longitude,
          timezone, majcom, organization_id, mission_owner,
          enclave, classification, lifecycle_state, metadata_json,
          created_at, updated_at;

-- ===== Buildings / Rooms / Rows =====
-- name: CreateBuilding :one
INSERT INTO buildings (id, site_id, name, code, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, NOW(), NOW())
RETURNING id, site_id, name, code, created_at, updated_at;

-- name: CreateRoom :one
INSERT INTO rooms (id, building_id, name, code, floor_area_sqft,
                   design_kw, design_cooling_tons, grid_cols, grid_rows,
                   created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, NOW(), NOW())
RETURNING id, building_id, name, code, floor_area_sqft,
          design_kw::text AS design_kw,
          design_cooling_tons::text AS design_cooling_tons,
          grid_cols, grid_rows,
          created_at, updated_at;

-- name: CreateRow :one
INSERT INTO rows (id, room_id, name, code, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, NOW(), NOW())
RETURNING id, room_id, name, code, created_at, updated_at;

-- name: GetBuilding :one
SELECT id, site_id, name, code, created_at, updated_at
FROM buildings WHERE id = $1;

-- name: GetRoom :one
SELECT id, building_id, name, code, floor_area_sqft,
       design_kw::text AS design_kw,
       design_cooling_tons::text AS design_cooling_tons,
       grid_cols, grid_rows,
       created_at, updated_at
FROM rooms WHERE id = $1;

-- name: GetRow :one
SELECT id, room_id, name, code, created_at, updated_at
FROM rows WHERE id = $1;

-- Site-id walkers for ABAC. Room → building → site, row → room →
-- building → site. Used by the locations PATCH/DELETE handlers to
-- resolve a row/room/building to its owning site for EnforceSiteScope.
-- name: SiteIDForRoom :one
SELECT b.site_id FROM rooms r JOIN buildings b ON b.id = r.building_id WHERE r.id = $1;

-- name: SiteIDForRow :one
SELECT b.site_id
FROM rows w
JOIN rooms r    ON r.id = w.room_id
JOIN buildings b ON b.id = r.building_id
WHERE w.id = $1;

-- name: UpdateBuilding :one
-- Parent FK (site_id) is intentionally not patchable — moving a
-- building across sites would orphan downstream racks/assets and the
-- audit trail. Operators delete + recreate if they need to relocate.
UPDATE buildings
SET name       = COALESCE(sqlc.narg(name)::text, name),
    code       = COALESCE(sqlc.narg(code)::text, code),
    updated_at = NOW()
WHERE id = $1
RETURNING id, site_id, name, code, created_at, updated_at;

-- name: UpdateRoom :one
UPDATE rooms
SET name            = COALESCE(sqlc.narg(name)::text, name),
    code            = COALESCE(sqlc.narg(code)::text, code),
    floor_area_sqft = CASE WHEN sqlc.arg(floor_area_sqft_set)::bool THEN sqlc.narg(floor_area_sqft)::int ELSE floor_area_sqft END,
    design_kw       = CASE WHEN sqlc.arg(design_kw_set)::bool THEN sqlc.narg(design_kw)::numeric ELSE design_kw END,
    design_cooling_tons = CASE WHEN sqlc.arg(design_cooling_tons_set)::bool THEN sqlc.narg(design_cooling_tons)::numeric ELSE design_cooling_tons END,
    grid_cols       = CASE WHEN sqlc.arg(grid_cols_set)::bool THEN sqlc.narg(grid_cols)::int ELSE grid_cols END,
    grid_rows       = CASE WHEN sqlc.arg(grid_rows_set)::bool THEN sqlc.narg(grid_rows)::int ELSE grid_rows END,
    updated_at      = NOW()
WHERE id = $1
RETURNING id, building_id, name, code, floor_area_sqft,
          design_kw::text AS design_kw,
          design_cooling_tons::text AS design_cooling_tons,
          grid_cols, grid_rows,
          created_at, updated_at;

-- name: UpdateRow :one
UPDATE rows
SET name       = COALESCE(sqlc.narg(name)::text, name),
    code       = COALESCE(sqlc.narg(code)::text, code),
    updated_at = NOW()
WHERE id = $1
RETURNING id, room_id, name, code, created_at, updated_at;

-- Deletes rely on FK constraints to refuse when downstream rows
-- (rooms in a building, rows in a room, racks in a row) still exist.
-- name: DeleteBuilding :exec
DELETE FROM buildings WHERE id = $1;

-- name: DeleteRoom :exec
DELETE FROM rooms WHERE id = $1;

-- name: DeleteRow :exec
DELETE FROM rows WHERE id = $1;

-- ===== Racks =====
-- name: CreateRack :one
INSERT INTO racks (id, site_id, row_id, name, code, u_height, max_kw,
                   max_weight_lbs, serial, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, NOW(), NOW())
RETURNING id, site_id, row_id, name, code, u_height, max_kw,
          max_weight_lbs, serial, grid_x, grid_y, grid_rotation,
          created_at, updated_at;

-- name: UpdateRack :one
UPDATE racks
SET name      = COALESCE(sqlc.narg(name)::text, name),
    u_height  = COALESCE(sqlc.narg(u_height)::int, u_height),
    max_kw    = CASE WHEN sqlc.arg(max_kw_set)::bool THEN sqlc.narg(max_kw)::numeric ELSE max_kw END,
    serial    = CASE WHEN sqlc.arg(serial_set)::bool THEN sqlc.narg(serial)::text   ELSE serial END,
    grid_x    = CASE WHEN sqlc.arg(grid_x_set)::bool THEN sqlc.narg(grid_x)::int ELSE grid_x END,
    grid_y    = CASE WHEN sqlc.arg(grid_y_set)::bool THEN sqlc.narg(grid_y)::int ELSE grid_y END,
    grid_rotation = COALESCE(sqlc.narg(grid_rotation)::smallint, grid_rotation),
    updated_at = NOW()
WHERE id = $1
RETURNING id, site_id, row_id, name, code, u_height, max_kw,
          max_weight_lbs, serial, grid_x, grid_y, grid_rotation,
          created_at, updated_at;

-- name: GetRackAssetsForShrinkCheck :many
-- Used by the rack PATCH handler to refuse shrinking u_height below
-- the lowest top-of-asset position.
SELECT name, rack_position_u, rack_units
FROM assets
WHERE rack_id = $1
  AND rack_position_u IS NOT NULL;
