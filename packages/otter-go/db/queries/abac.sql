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

-- name: GetSiteOrganizationID :one
-- PR 90 — site → organization lookup. NULL when the site hasn't been
-- mapped to organizations yet (post-PR-66 migration may leave rows
-- with NULL organization_id). Used by SiteMatches to expand a
-- principal scoped on an organizations.id UUID to the matching sites.
SELECT organization_id FROM sites WHERE id = $1;

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

-- name: GetDhcpScopeFabricID :one
-- 2-hop scope → server → fabric. Used by the per-scope push / diff /
-- push-history endpoints to enforce EnforceFabricScope before the
-- scope id has been resolved to a row. INNER JOIN returns 0 rows
-- (pgx.ErrNoRows) when the scope id is missing or its server has
-- been deleted — both treated as 404 by the caller.
SELECT s.fabric_id
FROM dhcp_scopes c
JOIN dhcp_servers s ON s.id = c.dhcp_server_id
WHERE c.id = $1;

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

-- DNS parent-fabric lookups (PR 57). Most DNS resources are 1-hop
-- (have a direct fabric_id); dns_records and dns_blocklist_entries are
-- 2-hop via their parent zone / blocklist.

-- name: GetDnsZoneFabricID :one
SELECT fabric_id FROM dns_zones WHERE id = $1;

-- name: GetDnsRecordFabricID :one
SELECT z.fabric_id
FROM dns_records r
JOIN dns_zones z ON z.id = r.zone_id
WHERE r.id = $1;

-- name: GetDnsServerFabricID :one
SELECT fabric_id FROM dns_servers WHERE id = $1;

-- name: GetAnycastGroupFabricID :one
SELECT fabric_id FROM anycast_groups WHERE id = $1;

-- name: GetDnsForwarderFabricID :one
SELECT fabric_id FROM dns_forwarders WHERE id = $1;

-- name: GetDnsCatalogZoneFabricID :one
SELECT fabric_id FROM dns_catalog_zones WHERE id = $1;

-- name: GetDnsBlocklistFabricID :one
SELECT fabric_id FROM dns_blocklists WHERE id = $1;

-- name: GetDnsBlocklistEntryFabricID :one
SELECT b.fabric_id
FROM dns_blocklist_entries e
JOIN dns_blocklists b ON b.id = e.blocklist_id
WHERE e.id = $1;

-- name: GetDnsViewFabricID :one
SELECT fabric_id FROM dns_views WHERE id = $1;

-- name: GetDnsHealthCheckFabricID :one
SELECT fabric_id FROM dns_health_checks WHERE id = $1;

-- BGP peers + anycast bindings (PR 58). BGP peers are site-scoped via
-- bgp_peers.site_id (NOT fabric-scoped — bgp_peers don't carry a
-- fabric, they carry a site). anycast_bgp_bindings link a dns_server
-- (fabric-rooted) to a bgp_peer (site-rooted); ABAC enforces on the
-- fabric side via dns_server.fabric_id for create/delete.

-- name: GetBgpPeerSiteID :one
SELECT site_id FROM bgp_peers WHERE id = $1;

-- name: GetAnycastBindingDnsServerFabricID :one
SELECT s.fabric_id
FROM anycast_bgp_bindings b
JOIN dns_servers s ON s.id = b.dns_server_id
WHERE b.id = $1;

-- Alerts + maintenance windows (PR 59). Both columns are NULLABLE —
-- NULL means "enterprise default" (applies to all sites) and only a
-- global principal can mutate. A scoped principal can only touch
-- resources whose site_scope_id / site_id is inside their site scope.

-- name: GetAlertRuleSiteScopeID :one
SELECT site_scope_id FROM alert_rules WHERE id = $1;

-- name: GetMaintenanceWindowSiteID :one
SELECT site_id FROM maintenance_windows WHERE id = $1;

-- name: ListSiteIDsForExpansion :many
-- PR 62 — expand a scoped principal's region + site_group + direct
-- site dimensions into the concrete set of site IDs they can see.
-- PR 90 adds the organization_id dim: a principal scoped on an
-- organizations.id UUID sees every site with the matching FK.
-- All inputs are independently optional; any/all may be NULL. A row
-- is returned if it matches any non-NULL dimension. (Caller
-- guarantees at least one dimension is non-NULL; global principals
-- skip this query entirely.) Used by auth.ScopedSiteFilter to build
-- the scope_site_ids slice that LIST/COUNT queries on site-rooted
-- resources filter against.
SELECT DISTINCT s.id
FROM sites s
LEFT JOIN site_group_memberships sgm ON sgm.site_id = s.id
WHERE (sqlc.narg(direct_site_ids)::uuid[]  IS NOT NULL AND s.id              = ANY(sqlc.narg(direct_site_ids)::uuid[]))
   OR (sqlc.narg(region_ids)::uuid[]       IS NOT NULL AND s.region_id       = ANY(sqlc.narg(region_ids)::uuid[]))
   OR (sqlc.narg(group_ids)::uuid[]        IS NOT NULL AND sgm.group_id      = ANY(sqlc.narg(group_ids)::uuid[]))
   OR (sqlc.narg(organization_ids)::uuid[] IS NOT NULL AND s.organization_id = ANY(sqlc.narg(organization_ids)::uuid[]));
