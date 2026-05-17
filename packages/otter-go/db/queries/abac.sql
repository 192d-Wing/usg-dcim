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

-- Parent-fabric lookups used by PR 54 mutation handlers to enforce
-- EnforceFabricScope before update/delete of resources that don't carry
-- fabric_id in the request body. One per IPAM resource family. Subnets,
-- addresses, VNIs, VTEPs (2+ hop transitive lookups) ship in PR 55.

-- name: GetVrfFabricID :one
SELECT fabric_id FROM vrfs WHERE id = $1;

-- name: GetOverlayFabricID :one
SELECT fabric_id FROM overlays WHERE id = $1;

-- name: GetDhcpServerFabricID :one
SELECT fabric_id FROM dhcp_servers WHERE id = $1;
