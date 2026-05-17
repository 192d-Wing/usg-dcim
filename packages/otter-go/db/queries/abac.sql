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
