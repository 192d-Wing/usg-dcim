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
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]))
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountDnsServers :one
SELECT count(*)::bigint FROM dns_servers
WHERE (sqlc.narg(site_id)::uuid   IS NULL OR site_id   = sqlc.narg(site_id))
  AND (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(role)::text      IS NULL OR role::text = sqlc.narg(role))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]));

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
       CASE WHEN anycast_ipv4 IS NULL THEN NULL ELSE host(anycast_ipv4) END AS anycast_ipv4,
       CASE WHEN anycast_ipv6 IS NULL THEN NULL ELSE host(anycast_ipv6) END AS anycast_ipv6,
       description, created_at, updated_at
FROM anycast_groups
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(service)::text   IS NULL OR service::text = sqlc.narg(service))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]))
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountAnycastGroups :one
SELECT count(*)::bigint FROM anycast_groups
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(service)::text   IS NULL OR service::text = sqlc.narg(service))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]));

-- ===== DNS forwarders =====
-- name: ListDnsForwarders :many
SELECT *
FROM dns_forwarders
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]))
ORDER BY zone_pattern
LIMIT $1 OFFSET $2;

-- name: CountDnsForwarders :one
SELECT count(*)::bigint FROM dns_forwarders
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]));

-- ===== DNS catalog zones =====
-- name: ListDnsCatalogZones :many
SELECT *
FROM dns_catalog_zones
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]))
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountDnsCatalogZones :one
SELECT count(*)::bigint FROM dns_catalog_zones
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]));

-- name: GetDnsCatalogZone :one
SELECT *
FROM dns_catalog_zones WHERE id = $1;

-- name: ListDnsKeyTagsByCatalog :many
-- Read just the key_tags for the disable-dnssec audit metadata
-- (`retired_key_tags`) — pulling the full row would scan the
-- public-key blob for nothing.
SELECT key_tag FROM dns_keys WHERE catalog_id = sqlc.arg(catalog_id)::uuid;

-- name: DeleteDnsKeysByCatalog :exec
-- Bulk-delete every signing key for a catalog zone. Mirror of
-- Python's `delete(DnsKey).where(DnsKey.catalog_id == catalog_id)`
-- in disable_catalog_dnssec.
DELETE FROM dns_keys WHERE catalog_id = sqlc.arg(catalog_id)::uuid;

-- name: SetDnsCatalogZoneSigned :exec
-- Used by disable-dnssec to clear the signed flag after the
-- key delete. Separate from UpdateDnsCatalogZone so the handler
-- doesn't have to thread enabled/name/etc. through unchanged.
UPDATE dns_catalog_zones
SET signed = $2, updated_at = NOW()
WHERE id = $1;
