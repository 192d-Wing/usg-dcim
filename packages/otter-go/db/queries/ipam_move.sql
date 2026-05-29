-- IPAM supernet 'move' endpoint — relocates a tenant-owned supernet
-- from the LIR landing fabric to its operational fabric/VRF. The
-- only mover in the system is the tenant who got the allocation
-- (gated by org scope on owner_organization_id); the source must be
-- the system landing fabric (slug='lir-unassigned'), and the
-- supernet must have no child subnets yet.
--
-- Because Subnet.fabric_id/vrf_id are denormalized off Supernet, a
-- move-with-children would create an inconsistency that's painful
-- to repair. The atomic MoveSupernet UPDATE enforces the "no
-- children" rule with NOT EXISTS so a racer that inserts a subnet
-- between our pre-check and the update can't slip through.

-- name: GetSupernetForMove :one
-- Joined projection: supernet identity + current fabric/VRF + owner
-- org (for ABAC) + the source fabric's is_system flag. Using
-- is_system instead of slug='lir-unassigned' eliminates a literal
-- that was previously duplicated across migration 0065,
-- internal/lir/handler.go, and internal/ipam/move.go. The slug
-- now lives in just one Go location (LandingFabricSlug in the lir
-- package, used only by GetLandingFabric in the approve flow).
--
-- Assumes the LIR landing fabric is the only is_system fabric in
-- the deployment (true as of migration 0065). If a future system
-- fabric is added, tighten this with a system_fabric_purpose
-- discriminator column on fabrics.
SELECT s.id,
       s.fabric_id           AS current_fabric_id,
       s.vrf_id              AS current_vrf_id,
       s.owner_organization_id,
       host(s.prefix) || '/' || masklen(s.prefix) AS prefix,
       f.is_system           AS current_fabric_is_system
FROM supernets s
JOIN fabrics f ON f.id = s.fabric_id
WHERE s.id = $1;

-- name: GetVrfForMove :one
-- The target VRF must belong to the target fabric. Returned shape
-- is just (id, fabric_id) — the handler checks vrf.fabric_id =
-- target_fabric_id before issuing the move.
SELECT id, fabric_id
FROM vrfs
WHERE id = $1;

-- name: MoveSupernet :one
-- Atomic move. Match shape mirrors the cancel/approve/reject CTE
-- pattern: WHERE matches the expected current state, RETURNING
-- comes back empty if anything raced. The NOT EXISTS subquery is
-- the no-child-subnets safety net — Subnet.supernet_id FK means
-- this is a quick index scan.
--
-- Pre-conditions enforced inline:
--   * Supernet is currently in $4 (the landing fabric ID resolved
--     by the handler from the slug).
--   * No subnet references this supernet.
-- Pre-conditions enforced by the handler before this call:
--   * Supernet is tenant-owned (owner_organization_id IS NOT NULL).
--   * Org-scope check on owner_organization_id.
--   * Fabric-scope check on $2 (the target fabric).
--   * Target VRF belongs to target fabric.
UPDATE supernets s
SET fabric_id  = $2,
    vrf_id     = $3,
    updated_at = NOW()
WHERE s.id = $1
  AND s.fabric_id = $4
  AND NOT EXISTS (SELECT 1 FROM subnets WHERE supernet_id = s.id)
RETURNING s.id, s.fabric_id, s.vrf_id, s.parent_supernet_id, s.site_id,
          host(s.prefix) || '/' || masklen(s.prefix) AS prefix,
          s.name, s.description, s.purpose,
          s.created_at, s.updated_at;
