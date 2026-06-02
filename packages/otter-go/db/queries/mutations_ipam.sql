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

-- ===== DHCP scope templates =====
-- name: CreateDhcpScopeTemplate :one
-- Unique (fabric_id, name) constraint surfaces as 23505 on RETURNING;
-- handler maps via httpx.ErrUniqueViolation → 409. options_json is
-- caller-validated JSON.
INSERT INTO dhcp_scope_templates (
    id, fabric_id, name, ip_family, options_json,
    valid_lifetime_seconds, renew_timer_seconds, rebind_timer_seconds,
    preferred_lifetime_seconds, description, created_at, updated_at
)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9, NOW(), NOW())
RETURNING id, fabric_id, name, ip_family, options_json,
          valid_lifetime_seconds, renew_timer_seconds, rebind_timer_seconds,
          preferred_lifetime_seconds, description, created_at, updated_at;

-- name: UpdateDhcpScopeTemplate :one
-- COALESCE on the non-nullable name; the four timer columns + the
-- description column are nullable so use the (set, value) CASE
-- pattern to distinguish "clear to NULL" from "key omitted".
-- options_json gets the same set/value split — Python's PATCH treats
-- {"options": null} as "clear to empty array" and missing key as
-- "keep current"; the API handler resolves that to options_set with
-- value [] vs options_set=false. ip_family + fabric_id are immutable
-- (Python doesn't expose them in the PATCH model).
UPDATE dhcp_scope_templates
SET name                       = COALESCE(sqlc.narg(name)::text, name),
    options_json               = CASE WHEN sqlc.arg(options_set)::bool
                                      THEN sqlc.narg(options_json)::jsonb
                                      ELSE options_json END,
    valid_lifetime_seconds     = CASE WHEN sqlc.arg(valid_lifetime_set)::bool
                                      THEN sqlc.narg(valid_lifetime_seconds)::int
                                      ELSE valid_lifetime_seconds END,
    renew_timer_seconds        = CASE WHEN sqlc.arg(renew_timer_set)::bool
                                      THEN sqlc.narg(renew_timer_seconds)::int
                                      ELSE renew_timer_seconds END,
    rebind_timer_seconds       = CASE WHEN sqlc.arg(rebind_timer_set)::bool
                                      THEN sqlc.narg(rebind_timer_seconds)::int
                                      ELSE rebind_timer_seconds END,
    preferred_lifetime_seconds = CASE WHEN sqlc.arg(preferred_lifetime_set)::bool
                                      THEN sqlc.narg(preferred_lifetime_seconds)::int
                                      ELSE preferred_lifetime_seconds END,
    description                = CASE WHEN sqlc.arg(description_set)::bool
                                      THEN sqlc.narg(description)::text
                                      ELSE description END,
    updated_at                 = NOW()
WHERE id = $1
RETURNING id, fabric_id, name, ip_family, options_json,
          valid_lifetime_seconds, renew_timer_seconds, rebind_timer_seconds,
          preferred_lifetime_seconds, description, created_at, updated_at;

-- name: DeleteDhcpScopeTemplate :exec
-- FK on dhcp_scopes.template_id is ON DELETE SET NULL — referencing
-- scopes fall back to their stored values automatically. Matches
-- Python's posture at api/ipam.py:2911.
DELETE FROM dhcp_scope_templates WHERE id = $1;

-- ===== DHCP scopes (mutations) =====
-- CRUD reads ship in PR 10; this block adds CREATE/PATCH/SOFT_DELETE/
-- RESTORE. ip_family + prefix + dhcp_server_id are immutable post-
-- create (Python's PATCH model omits them; the PATCH SQL doesn't
-- expose them either).

-- name: CreateDhcpScope :one
INSERT INTO dhcp_scopes (
    id, dhcp_server_id, subnet_id, template_id, name, ip_family, prefix,
    pools_json, pd_pools_json, options_json, reservations_json,
    valid_lifetime_seconds, renew_timer_seconds, rebind_timer_seconds,
    preferred_lifetime_seconds, enabled, description, auto_push_override,
    created_at, updated_at
)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6::cidr,
        $7, $8, $9, $10,
        $11, $12, $13, $14, $15, $16, $17, NOW(), NOW())
RETURNING id, dhcp_server_id, subnet_id, name, ip_family, prefix::text AS prefix,
          pools_json, pd_pools_json, options_json, reservations_json,
          valid_lifetime_seconds, renew_timer_seconds, rebind_timer_seconds,
          preferred_lifetime_seconds, enabled, description, kea_subnet_id,
          template_id, last_diff_at, last_diff_status, last_diff_delta_json,
          auto_push_override, deleted_at, created_at, updated_at;

-- name: UpdateDhcpScope :one
-- Partial update — every nullable column uses the (set, value) CASE
-- pattern; only `name`/`enabled` use COALESCE because they're NOT
-- NULL. JSONB columns get the CASE split because Python's PATCH
-- treats {"pools": null} as "clear" (writes [] for not-null-with-
-- default columns; writes NULL for pd_pools). The Go handler
-- resolves the null-vs-omitted distinction before this query runs.
UPDATE dhcp_scopes
SET name                       = COALESCE($2::text, name),
    subnet_id                  = CASE WHEN $3::bool  THEN $4::uuid  ELSE subnet_id END,
    template_id                = CASE WHEN $5::bool  THEN $6::uuid  ELSE template_id END,
    pools_json                 = CASE WHEN $7::bool  THEN $8::jsonb ELSE pools_json END,
    pd_pools_json              = CASE WHEN $9::bool  THEN $10::jsonb ELSE pd_pools_json END,
    options_json               = CASE WHEN $11::bool THEN $12::jsonb ELSE options_json END,
    reservations_json          = CASE WHEN $13::bool THEN $14::jsonb ELSE reservations_json END,
    valid_lifetime_seconds     = CASE WHEN $15::bool THEN $16::int  ELSE valid_lifetime_seconds END,
    renew_timer_seconds        = CASE WHEN $17::bool THEN $18::int  ELSE renew_timer_seconds END,
    rebind_timer_seconds       = CASE WHEN $19::bool THEN $20::int  ELSE rebind_timer_seconds END,
    preferred_lifetime_seconds = CASE WHEN $21::bool THEN $22::int  ELSE preferred_lifetime_seconds END,
    enabled                    = COALESCE($23::bool, enabled),
    description                = CASE WHEN $24::bool THEN $25::text ELSE description END,
    auto_push_override         = CASE WHEN $26::bool THEN $27::bool ELSE auto_push_override END,
    updated_at                 = NOW()
WHERE id = $1
RETURNING id, dhcp_server_id, subnet_id, name, ip_family, prefix::text AS prefix,
          pools_json, pd_pools_json, options_json, reservations_json,
          valid_lifetime_seconds, renew_timer_seconds, rebind_timer_seconds,
          preferred_lifetime_seconds, enabled, description, kea_subnet_id,
          template_id, last_diff_at, last_diff_status, last_diff_delta_json,
          auto_push_override, deleted_at, created_at, updated_at;

-- name: SoftDeleteDhcpScope :exec
-- Sets deleted_at = NOW(). Python's delete path at api/ipam.py:2193
-- assigns datetime.now(UTC); pgx's NOW() is equivalent for a UTC
-- column (the schema uses timestamptz). FK cascade is unaffected:
-- soft-deleted rows still satisfy any inbound FK that referenced
-- them.
UPDATE dhcp_scopes
SET deleted_at = NOW(), updated_at = NOW()
WHERE id = $1
  AND deleted_at IS NULL;

-- name: RestoreDhcpScope :one
-- Inverse of SoftDeleteDhcpScope. Returns the row (or pgx.ErrNoRows
-- if the scope is missing OR already restored — the handler reads
-- the row first to disambiguate "missing" vs "already-live", so the
-- "0 rows affected" case here is a race).
UPDATE dhcp_scopes
SET deleted_at = NULL, updated_at = NOW()
WHERE id = $1
  AND deleted_at IS NOT NULL
RETURNING id, dhcp_server_id, subnet_id, name, ip_family, prefix::text AS prefix,
          pools_json, pd_pools_json, options_json, reservations_json,
          valid_lifetime_seconds, renew_timer_seconds, rebind_timer_seconds,
          preferred_lifetime_seconds, enabled, description, kea_subnet_id,
          template_id, last_diff_at, last_diff_status, last_diff_delta_json,
          auto_push_override, deleted_at, created_at, updated_at;
