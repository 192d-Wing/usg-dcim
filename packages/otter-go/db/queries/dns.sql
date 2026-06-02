-- ===== DNS Zones =====
-- scope_fabric_ids is the PR 60 ABAC LIST filter: NULL = no restriction
-- (global caller); a non-NULL slice restricts the result to zones in the
-- named fabrics. See auth.ScopedFabricFilter.
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
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]))
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountDnsZones :one
SELECT count(*)::bigint
FROM dns_zones
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(site_id)::uuid   IS NULL OR site_id   = sqlc.narg(site_id))
  AND (sqlc.narg(kind)::text      IS NULL OR kind::text = sqlc.narg(kind))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]));

-- name: GetDnsZone :one
SELECT id, name, kind::text AS kind, fabric_id, site_id, description,
       soa_mname, soa_rname, soa_refresh, soa_retry, soa_expire, soa_minimum,
       default_ttl, signed, zsk_rotation_days,
       nsec3_salt, nsec3_iterations, nsec3_opt_out,
       publish_cds, frozen, created_at, updated_at
FROM dns_zones
WHERE id = $1;

-- ===== DNS Records =====
-- 2-hop scope: record → zone → fabric. Subquery on dns_zones keeps the
-- planner happy when scope_fabric_ids is NULL.
-- name: ListDnsRecordsByZoneIDs :many
-- Bulk-fetch every record across a set of zones. The bundle
-- assembler uses this to load records for an entire fabric in a
-- single round-trip rather than per-zone. Ordered (zone_id, name,
-- type) so per-zone slicing in Go is a single contiguous scan.
SELECT id, zone_id, name, type::text AS type, ttl, data,
       source::text AS source, ipam_address_id,
       health_check_id, view_id,
       created_at, updated_at
FROM dns_records
WHERE zone_id = ANY($1::uuid[])
ORDER BY zone_id, name, type;

-- name: ListDnsViewsByFabric :many
-- Split-horizon: every view bound to a fabric, ordered by priority
-- (narrowest first wins per CoreDNS view-plugin semantics). The
-- bundle assembler iterates this list in order when emitting view-
-- scoped server blocks.
SELECT id, name, fabric_id, match_cidrs, priority, description, created_at, updated_at
FROM dns_views
WHERE fabric_id = $1
ORDER BY priority ASC, name ASC;

-- name: GetEnabledDnsCatalogZoneByFabric :one
-- One row max — uq_dns_catalog_zone_fabric guarantees a fabric
-- can have at most one catalog. Returns no-rows when the catalog
-- is missing or disabled; bundle assembler skips catalog emission
-- on either case.
SELECT id, fabric_id, name, enabled, signed, created_at, updated_at
FROM dns_catalog_zones
WHERE fabric_id = $1 AND enabled = true;

-- name: ListEnabledAuthDnsServersByFabric :many
-- For the RFC 9432 §4.2.3 catalog primaries property records —
-- consumers (BIND 9.20+) use unicast_ip to AXFR each member zone.
SELECT id, fabric_id, name, role::text AS role, unicast_ip,
       enabled, created_at, updated_at
FROM dns_servers
WHERE fabric_id = $1
  AND role = 'auth'::dns_server_role
  AND enabled = true;

-- name: ListDnsKeysByZoneIDs :many
-- Bulk-fetch every DnsKey across a set of zones. Bundle assembler
-- uses this for both DNSSEC key-file emission and CDNSKEY/CDS
-- appendix lines. Ordered (zone_id, role, key_tag) so per-zone
-- slicing is contiguous.
SELECT id, zone_id, catalog_id, role::text AS role,
       algorithm::text AS algorithm, private_pem, public_key_b64,
       key_tag, active_from, retired_at
FROM dns_keys
WHERE zone_id = ANY($1::uuid[])
ORDER BY zone_id, role, key_tag;

-- name: ListUnhealthyEnabledHealthChecksByFabric :many
-- IDs only — the bundle assembler uses the set to skip records
-- whose health_check_id is unhealthy. The renderer's `unhealthy`
-- input is a set[uuid], so we return the raw IDs.
SELECT id FROM dns_health_checks
WHERE fabric_id = $1
  AND status = 'unhealthy'::dns_health_check_status
  AND enabled = true;

-- name: ListDnsZonesByFabric :many
-- Every non-frozen zone in a fabric, for the auth bundle. Excludes
-- frozen zones (operators freeze a zone to take it off the air
-- without deleting it).
SELECT id, name, kind::text AS kind, fabric_id, site_id, description,
       soa_mname, soa_rname, soa_refresh, soa_retry, soa_expire, soa_minimum,
       default_ttl, signed, zsk_rotation_days, nsec3_salt, nsec3_iterations,
       nsec3_opt_out, publish_cds, frozen, created_at, updated_at
FROM dns_zones
WHERE fabric_id = $1
  AND frozen = false
ORDER BY name;

-- name: ListDnsRecords :many
SELECT id, zone_id, name, type::text AS type, ttl, data,
       source::text AS source, ipam_address_id, created_at, updated_at
FROM dns_records
WHERE (sqlc.narg(zone_id)::uuid IS NULL OR zone_id = sqlc.narg(zone_id))
  AND (sqlc.narg(type)::text    IS NULL OR type::text   = sqlc.narg(type))
  AND (sqlc.narg(source)::text  IS NULL OR source::text = sqlc.narg(source))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR zone_id IN (
        SELECT id FROM dns_zones WHERE fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[])
      ))
ORDER BY zone_id, name, type
LIMIT $1 OFFSET $2;

-- name: CountDnsRecords :one
SELECT count(*)::bigint
FROM dns_records
WHERE (sqlc.narg(zone_id)::uuid IS NULL OR zone_id = sqlc.narg(zone_id))
  AND (sqlc.narg(type)::text    IS NULL OR type::text   = sqlc.narg(type))
  AND (sqlc.narg(source)::text  IS NULL OR source::text = sqlc.narg(source))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR zone_id IN (
        SELECT id FROM dns_zones WHERE fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[])
      ));
