-- name: FindCableForPort :one
-- Returns the cable that already claims (asset_id, port), if any.
-- Used by the cable POST/PATCH handlers to refuse a second cable on
-- the same physical port. excludeID is the cable being PATCHed
-- (caller passes uuid.Nil on create). The id != exclude_id filter
-- MUST sit outside the OR group — otherwise it only applies to the
-- b-end branch and a PATCH that repeats its own (a-end, port) self-
-- conflicts.
SELECT id, label
FROM cables
WHERE id != sqlc.arg(exclude_id)
  AND (
        (a_asset_id = sqlc.arg(asset_id) AND a_port = sqlc.arg(port))
     OR (b_asset_id = sqlc.arg(asset_id) AND b_port = sqlc.arg(port))
      )
LIMIT 1;

-- name: ListRackAssetsForPlacement :many
-- Placed assets in the rack on the requested face, excluding the
-- asset being created/updated. Used by the asset POST/PATCH handlers
-- to check u-grid collisions.
SELECT id, name, rack_position_u, rack_units
FROM assets
WHERE rack_id = sqlc.arg(rack_id)::uuid
  AND id != sqlc.arg(exclude_id)
  AND mount = 'rack'::asset_mount
  AND face = sqlc.arg(face)::asset_face
  AND rack_position_u IS NOT NULL;
