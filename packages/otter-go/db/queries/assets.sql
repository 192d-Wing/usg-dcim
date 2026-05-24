-- name: ListAssets :many
SELECT id, site_id, rack_id, parent_asset_id, name, hostname,
       kind::text AS kind, manufacturer, model, serial, firmware,
       rack_position_u, rack_units, face::text AS face,
       mount::text AS mount, pdu_side::text AS pdu_side,
       psu_count, port_count,
       mgmt_ip, mgmt_protocol, mgmt_port, mgmt_credentials_ref,
       lifecycle_state::text AS lifecycle_state,
       install_date, warranty_expires, metadata_json,
       created_at, updated_at
FROM assets
WHERE (sqlc.narg(site_id)::uuid          IS NULL OR site_id        = sqlc.narg(site_id))
  AND (sqlc.narg(rack_id)::uuid          IS NULL OR rack_id        = sqlc.narg(rack_id))
  AND (sqlc.narg(kind)::text             IS NULL OR kind::text     = sqlc.narg(kind))
  AND (sqlc.narg(lifecycle_state)::text  IS NULL OR lifecycle_state::text = sqlc.narg(lifecycle_state))
  AND (sqlc.narg(serial)::text           IS NULL OR serial         = sqlc.narg(serial))
  AND (sqlc.narg(hostname)::text         IS NULL OR hostname       = sqlc.narg(hostname))
  AND (sqlc.narg(scope_site_ids)::uuid[] IS NULL OR site_id        = ANY(sqlc.narg(scope_site_ids)::uuid[]))
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountAssets :one
SELECT count(*)::bigint
FROM assets
WHERE (sqlc.narg(site_id)::uuid          IS NULL OR site_id        = sqlc.narg(site_id))
  AND (sqlc.narg(rack_id)::uuid          IS NULL OR rack_id        = sqlc.narg(rack_id))
  AND (sqlc.narg(kind)::text             IS NULL OR kind::text     = sqlc.narg(kind))
  AND (sqlc.narg(lifecycle_state)::text  IS NULL OR lifecycle_state::text = sqlc.narg(lifecycle_state))
  AND (sqlc.narg(serial)::text           IS NULL OR serial         = sqlc.narg(serial))
  AND (sqlc.narg(hostname)::text         IS NULL OR hostname       = sqlc.narg(hostname))
  AND (sqlc.narg(scope_site_ids)::uuid[] IS NULL OR site_id        = ANY(sqlc.narg(scope_site_ids)::uuid[]));

-- name: GetAsset :one
SELECT id, site_id, rack_id, parent_asset_id, name, hostname,
       kind::text AS kind, manufacturer, model, serial, firmware,
       rack_position_u, rack_units, face::text AS face,
       mount::text AS mount, pdu_side::text AS pdu_side,
       psu_count, port_count,
       mgmt_ip, mgmt_protocol, mgmt_port, mgmt_credentials_ref,
       lifecycle_state::text AS lifecycle_state,
       install_date, warranty_expires, metadata_json,
       created_at, updated_at
FROM assets
WHERE id = $1;
