-- ===== DNS servers =====
-- name: ListDnsServers :many
SELECT id, name, site_id, fabric_id, role::text AS role,
       host(unicast_ip) AS unicast_ip, enabled,
       last_render_at, last_render_status, last_render_error, last_render_etag,
       coredns_version, anycast_group_id, created_at, updated_at
FROM dns_servers
WHERE (sqlc.narg(site_id)::uuid   IS NULL OR site_id   = sqlc.narg(site_id))
  AND (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(role)::text      IS NULL OR role::text = sqlc.narg(role))
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountDnsServers :one
SELECT count(*)::bigint FROM dns_servers
WHERE (sqlc.narg(site_id)::uuid   IS NULL OR site_id   = sqlc.narg(site_id))
  AND (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(role)::text      IS NULL OR role::text = sqlc.narg(role));

-- name: GetDnsServer :one
SELECT id, name, site_id, fabric_id, role::text AS role,
       host(unicast_ip) AS unicast_ip, enabled,
       last_render_at, last_render_status, last_render_error, last_render_etag,
       coredns_version, anycast_group_id, created_at, updated_at
FROM dns_servers
WHERE id = $1;

-- ===== Anycast groups =====
-- name: ListAnycastGroups :many
SELECT id, name, fabric_id, service::text AS service,
       host(anycast_ipv4) AS anycast_ipv4,
       host(anycast_ipv6) AS anycast_ipv6,
       description, created_at, updated_at
FROM anycast_groups
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(service)::text   IS NULL OR service::text = sqlc.narg(service))
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountAnycastGroups :one
SELECT count(*)::bigint FROM anycast_groups
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(service)::text   IS NULL OR service::text = sqlc.narg(service));

-- ===== DNS forwarders =====
-- name: ListDnsForwarders :many
SELECT id, name, fabric_id, zone_pattern, upstreams,
       description, created_at, updated_at
FROM dns_forwarders
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
ORDER BY zone_pattern
LIMIT $1 OFFSET $2;

-- name: CountDnsForwarders :one
SELECT count(*)::bigint FROM dns_forwarders
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id));

-- ===== DNS catalog zones =====
-- name: ListDnsCatalogZones :many
SELECT id, fabric_id, name, enabled, signed, created_at, updated_at
FROM dns_catalog_zones
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountDnsCatalogZones :one
SELECT count(*)::bigint FROM dns_catalog_zones
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id));
