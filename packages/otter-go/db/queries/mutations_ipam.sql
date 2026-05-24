-- ===== Fabrics =====
-- name: CreateFabric :one
-- NOTE: slug regex + auto-create-default-VRF deferred to invariants follow-up.
INSERT INTO fabrics (id, name, slug, description, enclave, classification,
                     dns_recursive_upstreams, dns_deny_networks,
                     catalog_transfer_acl, recursive_engine, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9, NOW(), NOW())
RETURNING id, name, slug, description, enclave, classification,
          dns_recursive_upstreams, dns_deny_networks,
          catalog_transfer_acl, recursive_engine, created_at, updated_at;

-- name: UpdateFabric :one
UPDATE fabrics
SET name           = COALESCE(sqlc.narg(name)::text, name),
    slug           = COALESCE(sqlc.narg(slug)::text, slug),
    description    = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    enclave        = CASE WHEN sqlc.arg(enclave_set)::bool THEN sqlc.narg(enclave)::text ELSE enclave END,
    classification = CASE WHEN sqlc.arg(classification_set)::bool THEN sqlc.narg(classification)::text ELSE classification END,
    dns_recursive_upstreams = CASE WHEN sqlc.arg(dns_recursive_upstreams_set)::bool THEN sqlc.narg(dns_recursive_upstreams)::jsonb ELSE dns_recursive_upstreams END,
    dns_deny_networks       = CASE WHEN sqlc.arg(dns_deny_networks_set)::bool       THEN sqlc.narg(dns_deny_networks)::jsonb       ELSE dns_deny_networks END,
    catalog_transfer_acl    = CASE WHEN sqlc.arg(catalog_transfer_acl_set)::bool    THEN sqlc.narg(catalog_transfer_acl)::jsonb    ELSE catalog_transfer_acl END,
    recursive_engine        = COALESCE(sqlc.narg(recursive_engine)::text, recursive_engine),
    updated_at     = NOW()
WHERE id = $1
RETURNING id, name, slug, description, enclave, classification,
          dns_recursive_upstreams, dns_deny_networks,
          catalog_transfer_acl, recursive_engine, created_at, updated_at;

-- name: CountVrfsInFabric :one
SELECT count(*)::bigint FROM vrfs WHERE fabric_id = $1;

-- name: DeleteFabric :exec
DELETE FROM fabrics WHERE id = $1;

-- ===== VRFs =====
-- name: CreateVrf :one
INSERT INTO vrfs (id, fabric_id, name, route_target, description, is_default, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, NOW(), NOW())
RETURNING id, fabric_id, name, route_target, description, is_default, created_at, updated_at;

-- name: UpdateVrf :one
UPDATE vrfs
SET name         = COALESCE(sqlc.narg(name)::text, name),
    route_target = CASE WHEN sqlc.arg(route_target_set)::bool THEN sqlc.narg(route_target)::text ELSE route_target END,
    description  = CASE WHEN sqlc.arg(description_set)::bool  THEN sqlc.narg(description)::text  ELSE description END,
    is_default   = COALESCE(sqlc.narg(is_default)::bool, is_default),
    updated_at   = NOW()
WHERE id = $1
RETURNING id, fabric_id, name, route_target, description, is_default, created_at, updated_at;

-- name: CountSupernetsInVrf :one
SELECT count(*)::bigint FROM supernets WHERE vrf_id = $1;

-- name: DeleteVrf :exec
DELETE FROM vrfs WHERE id = $1;

-- ===== VrfBgpPeers =====
-- name: CreateVrfBgpPeer :one
INSERT INTO vrf_bgp_peers (id, vrf_id, bgp_peer_id, address_family, rd, enabled, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3::bgp_address_family, $4, $5, NOW(), NOW())
RETURNING id, vrf_id, bgp_peer_id, address_family::text AS address_family, rd, enabled, created_at, updated_at;

-- name: UpdateVrfBgpPeer :one
UPDATE vrf_bgp_peers
SET rd      = CASE WHEN sqlc.arg(rd_set)::bool THEN sqlc.narg(rd)::text ELSE rd END,
    enabled = COALESCE(sqlc.narg(enabled)::bool, enabled),
    updated_at = NOW()
WHERE id = $1
RETURNING id, vrf_id, bgp_peer_id, address_family::text AS address_family, rd, enabled, created_at, updated_at;

-- name: DeleteVrfBgpPeer :exec
DELETE FROM vrf_bgp_peers WHERE id = $1;

-- ===== Supernets =====
-- NOTE: CIDR containment + parent-tree validation deferred to invariants follow-up.
-- name: CreateSupernet :one
INSERT INTO supernets (id, fabric_id, vrf_id, parent_supernet_id, site_id, prefix,
                       name, description, purpose, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5::cidr, $6, $7, $8, NOW(), NOW())
RETURNING id, fabric_id, vrf_id, parent_supernet_id, site_id,
          host(prefix) || '/' || masklen(prefix) AS prefix,
          name, description, purpose, created_at, updated_at;

-- name: UpdateSupernet :one
UPDATE supernets
SET parent_supernet_id = CASE WHEN sqlc.arg(parent_set)::bool THEN sqlc.narg(parent_supernet_id)::uuid ELSE parent_supernet_id END,
    site_id     = CASE WHEN sqlc.arg(site_set)::bool        THEN sqlc.narg(site_id)::uuid       ELSE site_id END,
    name        = CASE WHEN sqlc.arg(name_set)::bool        THEN sqlc.narg(name)::text          ELSE name END,
    description = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text   ELSE description END,
    purpose     = CASE WHEN sqlc.arg(purpose_set)::bool     THEN sqlc.narg(purpose)::text       ELSE purpose END,
    updated_at  = NOW()
WHERE id = $1
RETURNING id, fabric_id, vrf_id, parent_supernet_id, site_id,
          host(prefix) || '/' || masklen(prefix) AS prefix,
          name, description, purpose, created_at, updated_at;

-- name: CountSubnetsInSupernet :one
SELECT count(*)::bigint FROM subnets WHERE supernet_id = $1;

-- name: DeleteSupernet :exec
DELETE FROM supernets WHERE id = $1;

-- ===== Subnets =====
-- name: CreateSubnet :one
-- fabric_id + vrf_id are pulled from the parent supernet by the handler before this query runs.
INSERT INTO subnets (id, supernet_id, fabric_id, vrf_id, site_id, vni_id, prefix,
                     name, description, purpose, vlan_id, gateway, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6::cidr, $7, $8, $9, $10, $11::inet, NOW(), NOW())
RETURNING id, supernet_id, fabric_id, vrf_id, site_id, vni_id,
          host(prefix) || '/' || masklen(prefix) AS prefix,
          name, description, purpose, vlan_id, host(gateway) AS gateway,
          created_at, updated_at;

-- name: UpdateSubnet :one
UPDATE subnets
SET supernet_id = COALESCE(sqlc.narg(supernet_id)::uuid, supernet_id),
    site_id     = CASE WHEN sqlc.arg(site_set)::bool        THEN sqlc.narg(site_id)::uuid     ELSE site_id END,
    vni_id      = CASE WHEN sqlc.arg(vni_set)::bool         THEN sqlc.narg(vni_id)::uuid      ELSE vni_id END,
    name        = CASE WHEN sqlc.arg(name_set)::bool        THEN sqlc.narg(name)::text        ELSE name END,
    description = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    purpose     = CASE WHEN sqlc.arg(purpose_set)::bool     THEN sqlc.narg(purpose)::text     ELSE purpose END,
    vlan_id     = CASE WHEN sqlc.arg(vlan_set)::bool        THEN sqlc.narg(vlan_id)::int      ELSE vlan_id END,
    gateway     = CASE WHEN sqlc.arg(gateway_set)::bool     THEN sqlc.narg(gateway)::inet     ELSE gateway END,
    updated_at  = NOW()
WHERE id = $1
RETURNING id, supernet_id, fabric_id, vrf_id, site_id, vni_id,
          host(prefix) || '/' || masklen(prefix) AS prefix,
          name, description, purpose, vlan_id, host(gateway) AS gateway,
          created_at, updated_at;

-- name: CountAddressesInSubnet :one
SELECT count(*)::bigint FROM ip_addresses WHERE subnet_id = $1;

-- name: DeleteSubnet :exec
DELETE FROM subnets WHERE id = $1;

-- name: GetSupernetVrfAndFabric :one
SELECT vrf_id, fabric_id FROM supernets WHERE id = $1;

-- ===== IPAddresses =====
-- name: CreateIPAddress :one
INSERT INTO ip_addresses (id, subnet_id, asset_id, address, role, status, source,
                          dns_name, description, dhcp_lease_expires_at, dhcp_mac,
                          created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3::inet, $4::ip_address_role, $5::ip_address_status, $6::ip_address_source,
        $7, $8, $9, $10, NOW(), NOW())
RETURNING id, subnet_id, asset_id, host(address) || '/' || masklen(address) AS address,
          role::text AS role, status::text AS status, source::text AS source,
          dns_name, description, dhcp_lease_expires_at, dhcp_mac, created_at, updated_at;

-- name: UpdateIPAddress :one
UPDATE ip_addresses
SET asset_id    = CASE WHEN sqlc.arg(asset_set)::bool       THEN sqlc.narg(asset_id)::uuid    ELSE asset_id END,
    role        = COALESCE(sqlc.narg(role)::ip_address_role, role),
    status      = COALESCE(sqlc.narg(status)::ip_address_status, status),
    dns_name    = CASE WHEN sqlc.arg(dns_set)::bool         THEN sqlc.narg(dns_name)::text    ELSE dns_name END,
    description = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    updated_at  = NOW()
WHERE id = $1
RETURNING id, subnet_id, asset_id, host(address) || '/' || masklen(address) AS address,
          role::text AS role, status::text AS status, source::text AS source,
          dns_name, description, dhcp_lease_expires_at, dhcp_mac, created_at, updated_at;

-- name: DeleteIPAddress :exec
DELETE FROM ip_addresses WHERE id = $1;

-- ===== Overlays =====
-- name: CreateOverlay :one
INSERT INTO overlays (id, fabric_id, name, kind, udp_port, mtu, underlay_vrf_id,
                      description, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3::overlay_kind, $4, $5, $6, $7, NOW(), NOW())
RETURNING id, fabric_id, name, kind::text AS kind, udp_port, mtu, underlay_vrf_id,
          description, created_at, updated_at;

-- name: UpdateOverlay :one
UPDATE overlays
SET name        = COALESCE(sqlc.narg(name)::text, name),
    kind        = COALESCE(sqlc.narg(kind)::overlay_kind, kind),
    udp_port    = COALESCE(sqlc.narg(udp_port)::int, udp_port),
    mtu         = CASE WHEN sqlc.arg(mtu_set)::bool             THEN sqlc.narg(mtu)::int                ELSE mtu END,
    underlay_vrf_id = CASE WHEN sqlc.arg(underlay_set)::bool    THEN sqlc.narg(underlay_vrf_id)::uuid   ELSE underlay_vrf_id END,
    description = CASE WHEN sqlc.arg(description_set)::bool     THEN sqlc.narg(description)::text       ELSE description END,
    updated_at  = NOW()
WHERE id = $1
RETURNING id, fabric_id, name, kind::text AS kind, udp_port, mtu, underlay_vrf_id,
          description, created_at, updated_at;

-- name: CountVnisInOverlay :one
SELECT count(*)::bigint FROM vnis WHERE overlay_id = $1;

-- name: DeleteOverlay :exec
DELETE FROM overlays WHERE id = $1;

-- ===== VNIs =====
-- name: CreateVni :one
INSERT INTO vnis (id, overlay_id, vni, kind, name, description, vlan_id,
                  evpn_route_target, vrf_id, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3::vni_kind, $4, $5, $6, $7, $8, NOW(), NOW())
RETURNING id, overlay_id, vni, kind::text AS kind, name, description, vlan_id,
          evpn_route_target, vrf_id, created_at, updated_at;

-- name: UpdateVni :one
UPDATE vnis
SET name             = CASE WHEN sqlc.arg(name_set)::bool        THEN sqlc.narg(name)::text        ELSE name END,
    description      = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    vlan_id          = CASE WHEN sqlc.arg(vlan_set)::bool        THEN sqlc.narg(vlan_id)::int      ELSE vlan_id END,
    evpn_route_target = CASE WHEN sqlc.arg(rt_set)::bool         THEN sqlc.narg(evpn_route_target)::text ELSE evpn_route_target END,
    kind             = COALESCE(sqlc.narg(kind)::vni_kind, kind),
    vrf_id           = CASE WHEN sqlc.arg(vrf_set)::bool         THEN sqlc.narg(vrf_id)::uuid      ELSE vrf_id END,
    updated_at       = NOW()
WHERE id = $1
RETURNING id, overlay_id, vni, kind::text AS kind, name, description, vlan_id,
          evpn_route_target, vrf_id, created_at, updated_at;

-- name: DeleteVni :exec
DELETE FROM vnis WHERE id = $1;

-- ===== VTEPs =====
-- name: CreateVtep :one
INSERT INTO vteps (id, overlay_id, asset_id, loopback_ip, role, description, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3::inet, $4::vtep_role, $5, NOW(), NOW())
RETURNING id, overlay_id, asset_id, host(loopback_ip) AS loopback_ip,
          role::text AS role, description, created_at, updated_at;

-- name: UpdateVtep :one
UPDATE vteps
SET loopback_ip = CASE WHEN sqlc.arg(loopback_set)::bool    THEN sqlc.narg(loopback_ip)::inet ELSE loopback_ip END,
    role        = COALESCE(sqlc.narg(role)::vtep_role, role),
    description = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    updated_at  = NOW()
WHERE id = $1
RETURNING id, overlay_id, asset_id, host(loopback_ip) AS loopback_ip,
          role::text AS role, description, created_at, updated_at;

-- name: DeleteVtep :exec
DELETE FROM vteps WHERE id = $1;

-- ===== VTEP/VNI memberships =====
-- name: CreateVtepMembership :one
INSERT INTO vtep_vni_memberships (id, vtep_id, vni_id, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, NOW(), NOW())
RETURNING id, vtep_id, vni_id, created_at, updated_at;

-- name: DeleteVtepMembership :exec
DELETE FROM vtep_vni_memberships WHERE id = $1;

-- ===== DHCP servers =====
-- name: CreateDhcpServer :one
-- auth_password is accepted as plaintext for now; encryption-at-rest
-- using the same Fernet path as IdP refresh tokens is deferred.
INSERT INTO dhcp_servers (id, name, fabric_id, kea_url, auth_username, auth_password, enabled,
                          created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, NOW(), NOW())
RETURNING id, name, fabric_id, kea_url, auth_username, enabled,
          last_sync_at, last_sync_status, last_sync_error, last_sync_lease_count,
          created_at, updated_at;

-- name: UpdateDhcpServer :one
UPDATE dhcp_servers
SET name          = COALESCE(sqlc.narg(name)::text, name),
    kea_url       = COALESCE(sqlc.narg(kea_url)::text, kea_url),
    auth_username = CASE WHEN sqlc.arg(username_set)::bool THEN sqlc.narg(auth_username)::text ELSE auth_username END,
    auth_password = CASE WHEN sqlc.arg(password_set)::bool THEN sqlc.narg(auth_password)::text ELSE auth_password END,
    enabled       = COALESCE(sqlc.narg(enabled)::bool, enabled),
    updated_at    = NOW()
WHERE id = $1
RETURNING id, name, fabric_id, kea_url, auth_username, enabled,
          last_sync_at, last_sync_status, last_sync_error, last_sync_lease_count,
          created_at, updated_at;

-- name: DeleteDhcpServer :exec
DELETE FROM dhcp_servers WHERE id = $1;
