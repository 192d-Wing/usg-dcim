-- name: FindCableForPort :one
-- Returns the cable that already claims (asset_id, port), if any.
-- Used by the cable POST/PATCH handlers to refuse a second cable on
-- the same physical port. excludeID is the cable being PATCHed
-- (caller passes uuid.Nil on create). The id != $3 filter MUST sit
-- outside the OR group — otherwise it only applies to the b-end
-- branch and a PATCH that repeats its own (a-end, port) self-
-- conflicts.
SELECT id, label
FROM cables
WHERE id != $3
  AND (
        (a_asset_id = $1 AND a_port = $2)
     OR (b_asset_id = $1 AND b_port = $2)
      )
LIMIT 1;

-- name: ListRackAssetsForPlacement :many
-- Placed assets in the rack on the requested face, excluding the
-- asset being created/updated. Used by the asset POST/PATCH handlers
-- to check u-grid collisions.
SELECT id, name, rack_position_u, rack_units
FROM assets
WHERE rack_id = $1
  AND id != $2
  AND mount = 'rack'::asset_mount
  AND face = $3::asset_face
  AND rack_position_u IS NOT NULL;
