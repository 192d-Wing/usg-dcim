-- ===== VRFs =====
-- scope_fabric_ids is the PR 56 ABAC LIST filter: NULL = no restriction
-- (global principal); a non-NULL slice restricts the result to vrfs in
-- the named fabrics. See auth.ScopedFabricFilter.
-- name: ListVrfs :many
SELECT id, fabric_id, name, route_target, description, is_default, created_at, updated_at
FROM vrfs
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]))
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountVrfs :one
SELECT count(*)::bigint
FROM vrfs
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]));

-- name: GetVrf :one
SELECT id, fabric_id, name, route_target, description, is_default, created_at, updated_at
FROM vrfs
WHERE id = $1;

-- ===== Subnets =====
-- name: ListSubnets :many
SELECT id, supernet_id, fabric_id, vrf_id, site_id,
       host(prefix) || '/' || masklen(prefix) AS prefix,
       name, description, purpose, vlan_id,
       host(gateway) AS gateway, vni_id, created_at, updated_at
FROM subnets
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(vrf_id)::uuid    IS NULL OR vrf_id    = sqlc.narg(vrf_id))
  AND (sqlc.narg(site_id)::uuid   IS NULL OR site_id   = sqlc.narg(site_id))
  AND (sqlc.narg(purpose)::text   IS NULL OR purpose   = sqlc.narg(purpose))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]))
ORDER BY prefix
LIMIT $1 OFFSET $2;

-- name: CountSubnets :one
SELECT count(*)::bigint
FROM subnets
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(vrf_id)::uuid    IS NULL OR vrf_id    = sqlc.narg(vrf_id))
  AND (sqlc.narg(site_id)::uuid   IS NULL OR site_id   = sqlc.narg(site_id))
  AND (sqlc.narg(purpose)::text   IS NULL OR purpose   = sqlc.narg(purpose))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]));

-- name: GetSubnet :one
SELECT id, supernet_id, fabric_id, vrf_id, site_id,
       host(prefix) || '/' || masklen(prefix) AS prefix,
       name, description, purpose, vlan_id,
       host(gateway) AS gateway, vni_id, created_at, updated_at
FROM subnets
WHERE id = $1;

-- ===== IP Addresses =====
-- 2-hop scope: address → subnet → fabric. Subnets denormalize fabric_id
-- so the subquery is a single index scan.
-- name: ListIPAddresses :many
SELECT id, subnet_id, asset_id, host(address) AS address,
       role::text AS role, status::text AS status, source::text AS source,
       dns_name, description, dhcp_lease_expires_at, dhcp_mac,
       created_at, updated_at
FROM ip_addresses
WHERE (sqlc.narg(subnet_id)::uuid IS NULL OR subnet_id = sqlc.narg(subnet_id))
  AND (sqlc.narg(asset_id)::uuid  IS NULL OR asset_id  = sqlc.narg(asset_id))
  AND (sqlc.narg(role)::text      IS NULL OR role::text   = sqlc.narg(role))
  AND (sqlc.narg(status)::text    IS NULL OR status::text = sqlc.narg(status))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR subnet_id IN (
        SELECT id FROM subnets WHERE fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[])
      ))
ORDER BY address
LIMIT $1 OFFSET $2;

-- name: CountIPAddresses :one
SELECT count(*)::bigint
FROM ip_addresses
WHERE (sqlc.narg(subnet_id)::uuid IS NULL OR subnet_id = sqlc.narg(subnet_id))
  AND (sqlc.narg(asset_id)::uuid  IS NULL OR asset_id  = sqlc.narg(asset_id))
  AND (sqlc.narg(role)::text      IS NULL OR role::text   = sqlc.narg(role))
  AND (sqlc.narg(status)::text    IS NULL OR status::text = sqlc.narg(status))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR subnet_id IN (
        SELECT id FROM subnets WHERE fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[])
      ));

-- name: GetIPAddress :one
SELECT id, subnet_id, asset_id, host(address) AS address,
       role::text AS role, status::text AS status, source::text AS source,
       dns_name, description, dhcp_lease_expires_at, dhcp_mac,
       created_at, updated_at
FROM ip_addresses
WHERE id = $1;

-- name: ListAddressStringsInSubnet :many
-- PR 64 — subnet utilization reads only the address column. Projecting
-- the single column keeps the read fast on busy subnets and matches
-- the Python services.ipam.used_addresses_in_subnet shape: one row
-- per allocated address, stripped of the /N mask.
SELECT host(address) AS address
FROM ip_addresses
WHERE subnet_id = $1;
