-- ===== Overlays =====
-- name: ListOverlays :many
SELECT id, fabric_id, name, kind::text AS kind, udp_port, mtu,
       underlay_vrf_id, description, created_at, updated_at
FROM overlays
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]))
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountOverlays :one
SELECT count(*)::bigint
FROM overlays
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]));

-- ===== VNIs =====
-- fabric_id filter pushed into SQL via a subquery on overlays.fabric_id
-- so a single query covers the Python list_vnis branches. PR 56 adds
-- the scope_fabric_ids subquery on the same join.
-- name: ListVnis :many
SELECT id, overlay_id, vni, kind::text AS kind, name, description,
       vlan_id, evpn_route_target, vrf_id, created_at, updated_at
FROM vnis
WHERE (sqlc.narg(overlay_id)::uuid IS NULL OR overlay_id = sqlc.narg(overlay_id))
  AND (sqlc.narg(fabric_id)::uuid  IS NULL OR overlay_id IN (
        SELECT id FROM overlays WHERE fabric_id = sqlc.narg(fabric_id)
      ))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR overlay_id IN (
        SELECT id FROM overlays WHERE fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[])
      ))
  AND (sqlc.narg(kind)::text       IS NULL OR kind::text = sqlc.narg(kind))
ORDER BY vni
LIMIT $1 OFFSET $2;

-- name: CountVnis :one
SELECT count(*)::bigint
FROM vnis
WHERE (sqlc.narg(overlay_id)::uuid IS NULL OR overlay_id = sqlc.narg(overlay_id))
  AND (sqlc.narg(fabric_id)::uuid  IS NULL OR overlay_id IN (
        SELECT id FROM overlays WHERE fabric_id = sqlc.narg(fabric_id)
      ))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR overlay_id IN (
        SELECT id FROM overlays WHERE fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[])
      ))
  AND (sqlc.narg(kind)::text       IS NULL OR kind::text = sqlc.narg(kind));

-- ===== VTEPs =====
-- name: ListVteps :many
SELECT id, overlay_id, asset_id, host(loopback_ip) AS loopback_ip,
       role::text AS role, description, created_at, updated_at
FROM vteps
WHERE (sqlc.narg(overlay_id)::uuid IS NULL OR overlay_id = sqlc.narg(overlay_id))
  AND (sqlc.narg(asset_id)::uuid   IS NULL OR asset_id   = sqlc.narg(asset_id))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR overlay_id IN (
        SELECT id FROM overlays WHERE fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[])
      ))
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;

-- name: CountVteps :one
SELECT count(*)::bigint
FROM vteps
WHERE (sqlc.narg(overlay_id)::uuid IS NULL OR overlay_id = sqlc.narg(overlay_id))
  AND (sqlc.narg(asset_id)::uuid   IS NULL OR asset_id   = sqlc.narg(asset_id))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR overlay_id IN (
        SELECT id FROM overlays WHERE fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[])
      ));

-- ===== VTEP/VNI memberships =====
-- overlay_id filter joins through vteps so a UI fetching all memberships
-- for an overlay doesn't have to do N requests by vtep_id. PR 56 scope
-- filter rides the same vteps join.
-- name: ListVtepMemberships :many
SELECT m.id, m.vtep_id, m.vni_id, m.created_at, m.updated_at
FROM vtep_vni_memberships m
LEFT JOIN vteps v ON v.id = m.vtep_id
WHERE (sqlc.narg(vtep_id)::uuid    IS NULL OR m.vtep_id    = sqlc.narg(vtep_id))
  AND (sqlc.narg(vni_id)::uuid     IS NULL OR m.vni_id     = sqlc.narg(vni_id))
  AND (sqlc.narg(overlay_id)::uuid IS NULL OR v.overlay_id = sqlc.narg(overlay_id))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR v.overlay_id IN (
        SELECT id FROM overlays WHERE fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[])
      ))
ORDER BY m.created_at DESC
LIMIT $1 OFFSET $2;

-- name: CountVtepMemberships :one
SELECT count(*)::bigint
FROM vtep_vni_memberships m
LEFT JOIN vteps v ON v.id = m.vtep_id
WHERE (sqlc.narg(vtep_id)::uuid    IS NULL OR m.vtep_id    = sqlc.narg(vtep_id))
  AND (sqlc.narg(vni_id)::uuid     IS NULL OR m.vni_id     = sqlc.narg(vni_id))
  AND (sqlc.narg(overlay_id)::uuid IS NULL OR v.overlay_id = sqlc.narg(overlay_id))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR v.overlay_id IN (
        SELECT id FROM overlays WHERE fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[])
      ));

-- ===== DHCP servers =====
-- auth_password is intentionally NOT selected — the API never returns it.
-- name: ListDhcpServers :many
SELECT id, name, fabric_id, kea_url, auth_username, enabled,
       last_sync_at, last_sync_status, last_sync_error, last_sync_lease_count,
       created_at, updated_at
FROM dhcp_servers
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]))
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountDhcpServers :one
SELECT count(*)::bigint
FROM dhcp_servers
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]));

-- ===== DHCP scope templates =====
-- Reusable option-bundle + timer defaults that DhcpScope rows inherit
-- from when their template_id is set. The bundle renderer reads
-- options_json + the four timer pointers via MergeTemplateIntoScope;
-- the API surface is straight CRUD with ABAC on fabric_id.
-- Unique constraint (fabric_id, name) — collisions surface as 23505
-- from CreateDhcpScopeTemplate's RETURNING, mapped to 409 via the
-- standard ErrUniqueViolation path.

-- name: ListDhcpScopeTemplates :many
SELECT id, fabric_id, name, ip_family, options_json,
       valid_lifetime_seconds, renew_timer_seconds, rebind_timer_seconds,
       preferred_lifetime_seconds, description, created_at, updated_at
FROM dhcp_scope_templates
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(ip_family)::int  IS NULL OR ip_family = sqlc.narg(ip_family))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]))
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountDhcpScopeTemplates :one
SELECT count(*)::bigint
FROM dhcp_scope_templates
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(ip_family)::int  IS NULL OR ip_family = sqlc.narg(ip_family))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]));

-- name: GetDhcpScopeTemplate :one
SELECT id, fabric_id, name, ip_family, options_json,
       valid_lifetime_seconds, renew_timer_seconds, rebind_timer_seconds,
       preferred_lifetime_seconds, description, created_at, updated_at
FROM dhcp_scope_templates
WHERE id = $1;

-- ===== DHCP scopes (CRUD reads) =====
-- LIST + GET for the per-server scope endpoint family. Mutation
-- queries (CREATE/PATCH/DELETE/RESTORE) ship in a follow-up PR.
--
-- The filter set mirrors Python's list_dhcp_scopes at
-- api/ipam.py:1946: ip_family, enabled, diff_status, include_deleted.
-- Each filter is nullable so the handler can pass NULL for "no
-- filter". include_deleted is a bool — TRUE keeps soft-deleted rows
-- in the result, FALSE filters them out (the default Python ships).

-- name: ListDhcpScopesByServer :many
SELECT id, dhcp_server_id, subnet_id, name, ip_family, prefix::text AS prefix,
       pools_json, pd_pools_json, options_json, reservations_json,
       valid_lifetime_seconds, renew_timer_seconds, rebind_timer_seconds,
       preferred_lifetime_seconds, enabled, description, kea_subnet_id,
       template_id, last_diff_at, last_diff_status, last_diff_delta_json,
       auto_push_override, deleted_at, created_at, updated_at
FROM dhcp_scopes
WHERE dhcp_server_id = $3
  AND (sqlc.arg(include_deleted)::bool OR deleted_at IS NULL)
  AND (sqlc.narg(ip_family)::int  IS NULL OR ip_family = sqlc.narg(ip_family))
  AND (sqlc.narg(enabled)::bool   IS NULL OR enabled   = sqlc.narg(enabled))
  AND (sqlc.narg(diff_status)::text IS NULL OR last_diff_status = sqlc.narg(diff_status))
ORDER BY created_at
LIMIT $1 OFFSET $2;

-- name: CountDhcpScopesByServer :one
SELECT count(*)::bigint
FROM dhcp_scopes
WHERE dhcp_server_id = $1
  AND (sqlc.arg(include_deleted)::bool OR deleted_at IS NULL)
  AND (sqlc.narg(ip_family)::int  IS NULL OR ip_family = sqlc.narg(ip_family))
  AND (sqlc.narg(enabled)::bool   IS NULL OR enabled   = sqlc.narg(enabled))
  AND (sqlc.narg(diff_status)::text IS NULL OR last_diff_status = sqlc.narg(diff_status));

-- name: GetDhcpScope :one
-- Includes soft-deleted rows so the restore endpoint can fetch the
-- target. The CRUD handlers check deleted_at and 404 if a non-
-- restore caller tries to read a tombstoned row.
SELECT id, dhcp_server_id, subnet_id, name, ip_family, prefix::text AS prefix,
       pools_json, pd_pools_json, options_json, reservations_json,
       valid_lifetime_seconds, renew_timer_seconds, rebind_timer_seconds,
       preferred_lifetime_seconds, enabled, description, kea_subnet_id,
       template_id, last_diff_at, last_diff_status, last_diff_delta_json,
       auto_push_override, deleted_at, created_at, updated_at
FROM dhcp_scopes
WHERE id = $1;
