-- ===== DNS Zones =====
-- name: ListDnsZones :many
SELECT id, name, kind::text AS kind, fabric_id, site_id, description,
       soa_mname, soa_rname, soa_refresh, soa_retry, soa_expire, soa_minimum,
       default_ttl, signed, zsk_rotation_days,
       nsec3_salt, nsec3_iterations, nsec3_opt_out,
       publish_cds, frozen, created_at, updated_at
FROM dns_zones
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(site_id)::uuid   IS NULL OR site_id   = sqlc.narg(site_id))
  AND (sqlc.narg(kind)::text      IS NULL OR kind::text = sqlc.narg(kind))
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountDnsZones :one
SELECT count(*)::bigint
FROM dns_zones
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(site_id)::uuid   IS NULL OR site_id   = sqlc.narg(site_id))
  AND (sqlc.narg(kind)::text      IS NULL OR kind::text = sqlc.narg(kind));

-- name: GetDnsZone :one
SELECT id, name, kind::text AS kind, fabric_id, site_id, description,
       soa_mname, soa_rname, soa_refresh, soa_retry, soa_expire, soa_minimum,
       default_ttl, signed, zsk_rotation_days,
       nsec3_salt, nsec3_iterations, nsec3_opt_out,
       publish_cds, frozen, created_at, updated_at
FROM dns_zones
WHERE id = $1;

-- ===== DNS Records =====
-- name: ListDnsRecords :many
SELECT id, zone_id, name, type::text AS type, ttl, data,
       source::text AS source, ipam_address_id, created_at, updated_at
FROM dns_records
WHERE (sqlc.narg(zone_id)::uuid IS NULL OR zone_id = sqlc.narg(zone_id))
  AND (sqlc.narg(type)::text    IS NULL OR type::text   = sqlc.narg(type))
  AND (sqlc.narg(source)::text  IS NULL OR source::text = sqlc.narg(source))
ORDER BY zone_id, name, type
LIMIT $1 OFFSET $2;

-- name: CountDnsRecords :one
SELECT count(*)::bigint
FROM dns_records
WHERE (sqlc.narg(zone_id)::uuid IS NULL OR zone_id = sqlc.narg(zone_id))
  AND (sqlc.narg(type)::text    IS NULL OR type::text   = sqlc.narg(type))
  AND (sqlc.narg(source)::text  IS NULL OR source::text = sqlc.narg(source));
