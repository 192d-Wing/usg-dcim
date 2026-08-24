-- ===== Assets =====
-- name: CreateAsset :one
-- Note: PDU outlet auto-seeding (24 outlets, alternating sides, C13) is
-- deferred to a follow-up PR. Python create_asset does it inline; doing
-- the same here means a transaction wrapper that we don't yet have plumbed.
INSERT INTO assets (id, site_id, rack_id, parent_asset_id, name, hostname,
                    kind, manufacturer, model, serial, firmware,
                    rack_position_u, rack_units, face, mount, pdu_side,
                    psu_count, port_count, mgmt_ip, mgmt_protocol, mgmt_port,
                    mgmt_credentials_ref, lifecycle_state, metadata_json,
                    created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(site_id), sqlc.arg(rack_id), sqlc.arg(parent_asset_id), sqlc.arg(name), sqlc.arg(hostname),
        sqlc.arg(kind)::asset_kind, sqlc.arg(manufacturer), sqlc.arg(model), sqlc.arg(serial), sqlc.arg(firmware),
        sqlc.arg(rack_position_u), COALESCE(sqlc.narg(rack_units)::int, 1), sqlc.arg(face)::asset_face, sqlc.arg(mount)::asset_mount, sqlc.narg(pdu_side)::pdu_side,
        sqlc.arg(psu_count), sqlc.arg(port_count), sqlc.arg(mgmt_ip), sqlc.arg(mgmt_protocol), sqlc.arg(mgmt_port),
        sqlc.arg(mgmt_credentials_ref), sqlc.arg(lifecycle_state)::lifecycle_state, COALESCE(sqlc.narg(metadata_json)::jsonb, '{}'::jsonb),
        NOW(), NOW())
RETURNING *;

-- name: UpdateAsset :one
-- Placement validation (u-grid fit, slot collision) is deferred — same
-- transaction-scoping reason as above. The handler should refuse a
-- placement-changing PATCH until that lands; for now placement fields
-- write straight through.
UPDATE assets
SET name           = COALESCE(sqlc.narg(name)::text, name),
    hostname       = CASE WHEN sqlc.arg(hostname_set)::bool THEN sqlc.narg(hostname)::text ELSE hostname END,
    rack_id        = CASE WHEN sqlc.arg(rack_id_set)::bool  THEN sqlc.narg(rack_id)::uuid  ELSE rack_id END,
    rack_position_u = CASE WHEN sqlc.arg(rack_position_u_set)::bool THEN sqlc.narg(rack_position_u)::int ELSE rack_position_u END,
    rack_units     = CASE WHEN sqlc.arg(rack_units_set)::bool THEN sqlc.narg(rack_units)::int ELSE rack_units END,
    face           = COALESCE(sqlc.narg(face)::asset_face, face),
    mount          = COALESCE(sqlc.narg(mount)::asset_mount, mount),
    pdu_side       = CASE WHEN sqlc.arg(pdu_side_set)::bool THEN sqlc.narg(pdu_side)::pdu_side ELSE pdu_side END,
    psu_count      = CASE WHEN sqlc.arg(psu_count_set)::bool THEN sqlc.narg(psu_count)::int ELSE psu_count END,
    port_count     = CASE WHEN sqlc.arg(port_count_set)::bool THEN sqlc.narg(port_count)::int ELSE port_count END,
    mgmt_ip        = CASE WHEN sqlc.arg(mgmt_ip_set)::bool THEN sqlc.narg(mgmt_ip)::text ELSE mgmt_ip END,
    mgmt_protocol  = CASE WHEN sqlc.arg(mgmt_protocol_set)::bool THEN sqlc.narg(mgmt_protocol)::text ELSE mgmt_protocol END,
    mgmt_port      = CASE WHEN sqlc.arg(mgmt_port_set)::bool THEN sqlc.narg(mgmt_port)::int ELSE mgmt_port END,
    firmware       = CASE WHEN sqlc.arg(firmware_set)::bool THEN sqlc.narg(firmware)::text ELSE firmware END,
    lifecycle_state = COALESCE(sqlc.narg(lifecycle_state)::lifecycle_state, lifecycle_state),
    -- ::json, not ::jsonb: the column is json and COALESCE cannot
    -- implicitly unify jsonb with json — ::jsonb fails at plan time.
    metadata_json  = COALESCE(sqlc.narg(metadata_json)::json, metadata_json),
    updated_at     = NOW()
WHERE id = $1
RETURNING *;

-- ===== Decommission =====

-- name: CountConsumerPowerDrops :one
SELECT count(*)::bigint
FROM power_connections
WHERE asset_id = $1;

-- name: CountPduPowerDrops :one
SELECT count(*)::bigint
FROM power_connections pc
JOIN outlets o ON o.id = pc.outlet_id
WHERE o.pdu_asset_id = $1;

-- name: ListDownstreamAssetNames :many
SELECT DISTINCT a.name
FROM power_connections pc
JOIN outlets o ON o.id = pc.outlet_id
JOIN assets a ON a.id = pc.asset_id
WHERE o.pdu_asset_id = $1
ORDER BY a.name;

-- name: DeleteConsumerPowerConnections :exec
DELETE FROM power_connections WHERE asset_id = $1;

-- name: DeletePduPowerConnections :exec
DELETE FROM power_connections
WHERE outlet_id IN (SELECT id FROM outlets WHERE pdu_asset_id = $1);

-- name: SetAssetDecommissioned :one
UPDATE assets
SET lifecycle_state = 'decommissioned'::lifecycle_state,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- ===== Cables =====
-- name: CreateCable :one
-- Port-in-range + port-in-use validations are deferred to the same
-- follow-up PR as asset placement validation. site_id is set from the
-- a-end asset by the handler before this query runs.
INSERT INTO cables (id, site_id, a_asset_id, a_port, b_asset_id, b_port,
                    medium, color, length_m, label, face, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5,
        $6, $7, $8, $9, $10, NOW(), NOW())
RETURNING *;

-- name: DeleteCable :exec
DELETE FROM cables WHERE id = $1;

-- name: UpdateCable :one
-- site_id and the two asset_id columns are NOT NULL — handlers pass
-- nil to keep current. The nullable a_port/b_port/medium/color/
-- length_m/label/face columns use the (set, value) flag pattern so
-- a body of {"medium": null} clears the row, matching Pydantic's
-- model_dump(exclude_unset=True) semantics on the Python side.
UPDATE cables
SET site_id    = COALESCE(sqlc.narg(site_id)::uuid,    site_id),
    a_asset_id = COALESCE(sqlc.narg(a_asset_id)::uuid, a_asset_id),
    b_asset_id = COALESCE(sqlc.narg(b_asset_id)::uuid, b_asset_id),
    a_port     = CASE WHEN sqlc.arg(a_port_set)::bool   THEN sqlc.narg(a_port)::text    ELSE a_port   END,
    b_port     = CASE WHEN sqlc.arg(b_port_set)::bool   THEN sqlc.narg(b_port)::text    ELSE b_port   END,
    medium     = CASE WHEN sqlc.arg(medium_set)::bool   THEN sqlc.narg(medium)::text    ELSE medium   END,
    color      = CASE WHEN sqlc.arg(color_set)::bool    THEN sqlc.narg(color)::text     ELSE color    END,
    length_m   = CASE WHEN sqlc.arg(length_m_set)::bool THEN sqlc.narg(length_m)::numeric ELSE length_m END,
    label      = CASE WHEN sqlc.arg(label_set)::bool    THEN sqlc.narg(label)::text     ELSE label    END,
    face       = CASE WHEN sqlc.arg(face_set)::bool     THEN sqlc.narg(face)::text      ELSE face     END,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: GetAssetSiteID :one
-- Lookup helper used by the cables handler to set site_id from the
-- a-end asset, matching Python's behavior.
SELECT site_id FROM assets WHERE id = $1;

-- name: FindAssetByManufacturerSerial :one
-- PR 69 — bulk upsert keys on (manufacturer, serial). NULL on either
-- side means "no key, always insert" (matches Python: existing only
-- when both fields are non-empty). Returns pgx.ErrNoRows on miss so
-- the handler can branch into insert vs update.
SELECT *
FROM assets
WHERE manufacturer = sqlc.arg(manufacturer)::text AND serial = sqlc.arg(serial)::text
LIMIT 1;

-- ===== Hard delete (UX-debt batch) =====
-- Decommission remains the lifecycle path; DELETE is for mistakes
-- and test hygiene. Guards are enforced by the handler: child
-- assets and cables refuse the delete (409), IP bindings detach,
-- alerts purge, and outlets + power drops ride the FK cascades.
-- Telemetry-instrumented assets still refuse via the RESTRICT FKs
-- (mapped to a conflict error).

-- name: CountChildAssets :one
SELECT count(*)::bigint FROM assets WHERE parent_asset_id = $1;

-- name: CountCablesForAsset :one
SELECT count(*)::bigint FROM cables
WHERE a_asset_id = sqlc.arg(asset_id) OR b_asset_id = sqlc.arg(asset_id);

-- name: DetachIPAddressesFromAsset :execrows
UPDATE ip_addresses SET asset_id = NULL, updated_at = NOW()
WHERE asset_id = $1;

-- name: DeleteAlertsForAsset :execrows
DELETE FROM alerts WHERE asset_id = $1;

-- name: DeleteAsset :execrows
DELETE FROM assets WHERE id = $1;
