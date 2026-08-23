-- name: GetPduAsset :one
-- Confirms the asset is a PDU before returning outlets — matches the
-- Python list_outlets 404 path when asset.kind != AssetKind.pdu.
SELECT id, kind::text AS kind
FROM assets
WHERE id = $1;

-- name: ListOutletsByPdu :many
SELECT id, pdu_asset_id, position, label,
       phase, max_amps, receptacle, created_at, updated_at
FROM outlets
WHERE pdu_asset_id = $1
ORDER BY position;

-- name: ListPowerConnectionsByOutletIDs :many
SELECT id, outlet_id, asset_id, psu_index, cord_color, cord_length_m, created_at, updated_at
FROM power_connections
WHERE outlet_id = ANY(sqlc.arg(outlet_ids)::uuid[]);
