-- ===== Overlays =====
-- name: ListOverlays :many
SELECT id, fabric_id, name, kind::text AS kind, udp_port, mtu,
       underlay_vrf_id, description, created_at, updated_at
FROM overlays
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountOverlays :one
SELECT count(*)::bigint
FROM overlays
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id));

-- ===== VNIs =====
-- fabric_id filter pushed into SQL via a subquery on overlays.fabric_id
-- so a single query covers the Python list_vnis branches.
-- name: ListVnis :many
SELECT id, overlay_id, vni, kind::text AS kind, name, description,
       vlan_id, evpn_route_target, vrf_id, created_at, updated_at
FROM vnis
WHERE (sqlc.narg(overlay_id)::uuid IS NULL OR overlay_id = sqlc.narg(overlay_id))
  AND (sqlc.narg(fabric_id)::uuid  IS NULL OR overlay_id IN (
        SELECT id FROM overlays WHERE fabric_id = sqlc.narg(fabric_id)
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
  AND (sqlc.narg(kind)::text       IS NULL OR kind::text = sqlc.narg(kind));

-- ===== VTEPs =====
-- name: ListVteps :many
SELECT id, overlay_id, asset_id, host(loopback_ip) AS loopback_ip,
       role::text AS role, description, created_at, updated_at
FROM vteps
WHERE (sqlc.narg(overlay_id)::uuid IS NULL OR overlay_id = sqlc.narg(overlay_id))
  AND (sqlc.narg(asset_id)::uuid   IS NULL OR asset_id   = sqlc.narg(asset_id))
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;

-- name: CountVteps :one
SELECT count(*)::bigint
FROM vteps
WHERE (sqlc.narg(overlay_id)::uuid IS NULL OR overlay_id = sqlc.narg(overlay_id))
  AND (sqlc.narg(asset_id)::uuid   IS NULL OR asset_id   = sqlc.narg(asset_id));

-- ===== VTEP/VNI memberships =====
-- overlay_id filter joins through vteps so a UI fetching all memberships
-- for an overlay doesn't have to do N requests by vtep_id.
-- name: ListVtepMemberships :many
SELECT m.id, m.vtep_id, m.vni_id, m.created_at, m.updated_at
FROM vtep_vni_memberships m
LEFT JOIN vteps v ON v.id = m.vtep_id
WHERE (sqlc.narg(vtep_id)::uuid    IS NULL OR m.vtep_id    = sqlc.narg(vtep_id))
  AND (sqlc.narg(vni_id)::uuid     IS NULL OR m.vni_id     = sqlc.narg(vni_id))
  AND (sqlc.narg(overlay_id)::uuid IS NULL OR v.overlay_id = sqlc.narg(overlay_id))
ORDER BY m.created_at DESC
LIMIT $1 OFFSET $2;

-- name: CountVtepMemberships :one
SELECT count(*)::bigint
FROM vtep_vni_memberships m
LEFT JOIN vteps v ON v.id = m.vtep_id
WHERE (sqlc.narg(vtep_id)::uuid    IS NULL OR m.vtep_id    = sqlc.narg(vtep_id))
  AND (sqlc.narg(vni_id)::uuid     IS NULL OR m.vni_id     = sqlc.narg(vni_id))
  AND (sqlc.narg(overlay_id)::uuid IS NULL OR v.overlay_id = sqlc.narg(overlay_id));

-- ===== DHCP servers =====
-- auth_password is intentionally NOT selected — the API never returns it.
-- name: ListDhcpServers :many
SELECT id, name, fabric_id, kea_url, auth_username, enabled,
       last_sync_at, last_sync_status, last_sync_error, last_sync_lease_count,
       created_at, updated_at
FROM dhcp_servers
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountDhcpServers :one
SELECT count(*)::bigint
FROM dhcp_servers
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id));
