-- name: GetUserScopedCapabilities :many
-- One row per (capability_code, scope_type, target_id) for a user.
-- LEFT JOIN role_scopes so caps from unrestricted role assignments
-- (no RoleScope rows) come back with NULL scope_type/target_id, which
-- the resolver treats as "global". Aggregated downstream into a
-- map[string]Scope by the caller.
SELECT
    jsonb_array_elements_text(r.permission_codes::jsonb) AS code,
    rs.scope_type::text AS scope_type,
    rs.target_id
FROM user_roles ur
JOIN roles r ON r.id = ur.role_id
LEFT JOIN role_scopes rs ON rs.assignment_id = ur.id
WHERE ur.user_id = $1;

-- name: GetSiteRegionID :one
-- Site → region lookup for the SiteMatchesScope expansion. Returns the
-- region UUID a site lives in so a region-scoped principal can be
-- expanded to match sites under that region.
SELECT region_id FROM sites WHERE id = $1;

-- name: ListSiteGroupIDsForSite :many
-- All SiteGroup memberships for a site. A site-group-scoped principal
-- matches a site if any of its memberships overlap the principal's
-- site_group_ids set.
SELECT group_id FROM site_group_memberships WHERE site_id = $1;

-- Parent-fabric lookups used by mutation handlers to enforce
-- EnforceFabricScope before create/update/delete of resources that don't
-- carry fabric_id in the request body. One per IPAM resource family.
-- 1-hop lookups shipped in PR 54; 2+ hop transitive lookups (subnet/
-- address/vni/vtep/vtep-membership) shipped in PR 55.

-- name: GetVrfFabricID :one
SELECT fabric_id FROM vrfs WHERE id = $1;

-- name: GetOverlayFabricID :one
SELECT fabric_id FROM overlays WHERE id = $1;

-- name: GetDhcpServerFabricID :one
SELECT fabric_id FROM dhcp_servers WHERE id = $1;

-- name: GetSubnetFabricID :one
-- Subnets denormalize fabric_id alongside the supernet FK (see
-- CreateSubnet, which copies fabric/vrf from the parent supernet),
-- so this is effectively 1-hop even though the chain is
-- subnet → supernet → fabric in the schema.
SELECT fabric_id FROM subnets WHERE id = $1;

-- name: GetIPAddressFabricID :one
-- ip_addresses → subnets → fabric_id. Subnets carry the denormalized
-- fabric_id, so the join is one hop.
SELECT s.fabric_id
FROM ip_addresses a
JOIN subnets s ON s.id = a.subnet_id
WHERE a.id = $1;

-- name: GetVniFabricID :one
-- vnis → overlays → fabric_id.
SELECT o.fabric_id
FROM vnis v
JOIN overlays o ON o.id = v.overlay_id
WHERE v.id = $1;

-- name: GetVtepFabricID :one
-- vteps → overlays → fabric_id.
SELECT o.fabric_id
FROM vteps v
JOIN overlays o ON o.id = v.overlay_id
WHERE v.id = $1;

-- name: GetVtepMembershipFabricID :one
-- vtep_vni_memberships → vteps → overlays → fabric_id.
SELECT o.fabric_id
FROM vtep_vni_memberships m
JOIN vteps v ON v.id = m.vtep_id
JOIN overlays o ON o.id = v.overlay_id
WHERE m.id = $1;
