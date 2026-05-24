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
-- PR 67 — organization_id is the new FK pointer onto organizations.id
-- (PR 66). It rides alongside the legacy `organization` string for now;
-- a future PR will retire the string column once API consumers have
-- migrated.
-- name: CreateSite :one
INSERT INTO sites (id, region_id, name, code, address, latitude, longitude,
                   timezone, majcom, organization, organization_id, mission_owner,
                   enclave, classification, lifecycle_state, metadata_json,
                   created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6,
        $7, $8, $9, $10, $11,
        $12, $13, $14::lifecycle_state, COALESCE($15::jsonb, '{}'::jsonb),
        NOW(), NOW())
RETURNING id, region_id, name, code, address, latitude, longitude,
          timezone, majcom, organization, organization_id, mission_owner,
          enclave, classification, lifecycle_state, metadata_json,
          created_at, updated_at;

-- name: UpdateSite :one
UPDATE sites
SET name           = COALESCE(sqlc.narg(name)::text, name),
    address        = CASE WHEN sqlc.arg(address_set)::bool          THEN sqlc.narg(address)::text          ELSE address END,
    majcom         = CASE WHEN sqlc.arg(majcom_set)::bool           THEN sqlc.narg(majcom)::text           ELSE majcom END,
    organization   = CASE WHEN sqlc.arg(organization_set)::bool     THEN sqlc.narg(organization)::text     ELSE organization END,
    organization_id = CASE WHEN sqlc.arg(organization_id_set)::bool THEN sqlc.narg(organization_id)::uuid  ELSE organization_id END,
    mission_owner  = CASE WHEN sqlc.arg(mission_owner_set)::bool    THEN sqlc.narg(mission_owner)::text    ELSE mission_owner END,
    enclave        = CASE WHEN sqlc.arg(enclave_set)::bool          THEN sqlc.narg(enclave)::text          ELSE enclave END,
    lifecycle_state = COALESCE(sqlc.narg(lifecycle_state)::lifecycle_state, lifecycle_state),
    metadata_json  = COALESCE(sqlc.narg(metadata_json)::jsonb, metadata_json),
    updated_at     = NOW()
WHERE id = $1
RETURNING id, region_id, name, code, address, latitude, longitude,
          timezone, majcom, organization, organization_id, mission_owner,
          enclave, classification, lifecycle_state, metadata_json,
          created_at, updated_at;

-- ===== Buildings / Rooms / Rows =====
-- name: CreateBuilding :one
INSERT INTO buildings (id, site_id, name, code, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, NOW(), NOW())
RETURNING id, site_id, name, code, created_at, updated_at;

-- name: CreateRoom :one
INSERT INTO rooms (id, building_id, name, code, floor_area_sqft, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, $4, NOW(), NOW())
RETURNING id, building_id, name, code, floor_area_sqft, created_at, updated_at;

-- name: CreateRow :one
INSERT INTO rows (id, room_id, name, code, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, NOW(), NOW())
RETURNING id, room_id, name, code, created_at, updated_at;

-- ===== Racks =====
-- name: CreateRack :one
INSERT INTO racks (id, site_id, row_id, name, code, u_height, max_kw,
                   max_weight_lbs, serial, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, NOW(), NOW())
RETURNING id, site_id, row_id, name, code, u_height, max_kw,
          max_weight_lbs, serial, created_at, updated_at;

-- name: UpdateRack :one
UPDATE racks
SET name      = COALESCE(sqlc.narg(name)::text, name),
    u_height  = COALESCE(sqlc.narg(u_height)::int, u_height),
    max_kw    = CASE WHEN sqlc.arg(max_kw_set)::bool THEN sqlc.narg(max_kw)::numeric ELSE max_kw END,
    serial    = CASE WHEN sqlc.arg(serial_set)::bool THEN sqlc.narg(serial)::text   ELSE serial END,
    updated_at = NOW()
WHERE id = $1
RETURNING id, site_id, row_id, name, code, u_height, max_kw,
          max_weight_lbs, serial, created_at, updated_at;

-- name: GetRackAssetsForShrinkCheck :many
-- Used by the rack PATCH handler to refuse shrinking u_height below
-- the lowest top-of-asset position.
SELECT name, rack_position_u, rack_units
FROM assets
WHERE rack_id = $1
  AND rack_position_u IS NOT NULL;
