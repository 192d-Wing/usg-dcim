-- LIR (Local Internet Registry) queries — pools + pool↔supernet
-- linkage. Request submission / allocation engine queries land in a
-- separate file as those slices ship.
--
-- Schema is owned by Alembic migration 20260528_0065 in
-- packages/otter/src/dcim/migrations/. Keep this file aligned by
-- column list, types, and CHECK semantics whenever that migration
-- chain advances.

-- ===== Pools =====

-- name: ListLirPools :many
SELECT id, name, slug, description, ip_family, fabric_id,
       classification, min_prefix_length, max_prefix_length,
       default_supernet_purpose, arin_parent_net_handle,
       enabled, created_at, updated_at
FROM lir_pools
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountLirPools :one
SELECT count(*)::bigint FROM lir_pools;

-- name: GetLirPool :one
SELECT id, name, slug, description, ip_family, fabric_id,
       classification, min_prefix_length, max_prefix_length,
       default_supernet_purpose, arin_parent_net_handle,
       enabled, created_at, updated_at
FROM lir_pools
WHERE id = $1;

-- name: CreateLirPool :one
INSERT INTO lir_pools (
    id, name, slug, description, ip_family, fabric_id,
    classification, min_prefix_length, max_prefix_length,
    default_supernet_purpose, arin_parent_net_handle,
    enabled, created_at, updated_at
)
VALUES (
    gen_random_uuid(), $1, $2, $3, $4::smallint, $5,
    $6, $7::smallint, $8::smallint,
    $9, $10,
    COALESCE($11::bool, TRUE), NOW(), NOW()
)
RETURNING id, name, slug, description, ip_family, fabric_id,
          classification, min_prefix_length, max_prefix_length,
          default_supernet_purpose, arin_parent_net_handle,
          enabled, created_at, updated_at;

-- name: UpdateLirPool :one
-- Partial update. The migration's CHECK ck_lir_pool_prefix_bounds
-- enforces min <= max and family-vs-prefix cap; the handler still
-- pre-validates so a bad PATCH gets a 422 instead of a 500.
-- ip_family is intentionally immutable — changing it would orphan
-- every allocation under the pool.
UPDATE lir_pools
SET name                     = COALESCE(sqlc.narg(name)::text, name),
    slug                     = COALESCE(sqlc.narg(slug)::text, slug),
    description              = CASE WHEN sqlc.arg(description_set)::bool
                                    THEN sqlc.narg(description)::text
                                    ELSE description END,
    fabric_id                = CASE WHEN sqlc.arg(fabric_set)::bool
                                    THEN sqlc.narg(fabric_id)::uuid
                                    ELSE fabric_id END,
    classification           = CASE WHEN sqlc.arg(classification_set)::bool
                                    THEN sqlc.narg(classification)::text
                                    ELSE classification END,
    min_prefix_length        = COALESCE(sqlc.narg(min_prefix_length)::smallint,
                                        min_prefix_length),
    max_prefix_length        = COALESCE(sqlc.narg(max_prefix_length)::smallint,
                                        max_prefix_length),
    default_supernet_purpose = CASE WHEN sqlc.arg(purpose_set)::bool
                                    THEN sqlc.narg(default_supernet_purpose)::text
                                    ELSE default_supernet_purpose END,
    arin_parent_net_handle   = CASE WHEN sqlc.arg(arin_set)::bool
                                    THEN sqlc.narg(arin_parent_net_handle)::text
                                    ELSE arin_parent_net_handle END,
    enabled                  = COALESCE(sqlc.narg(enabled)::bool, enabled),
    updated_at               = NOW()
WHERE id = $1
RETURNING id, name, slug, description, ip_family, fabric_id,
          classification, min_prefix_length, max_prefix_length,
          default_supernet_purpose, arin_parent_net_handle,
          enabled, created_at, updated_at;

-- name: DeleteLirPool :exec
DELETE FROM lir_pools WHERE id = $1;

-- ===== Pool ↔ supernet linkage =====

-- name: CountAllocationsForPool :one
-- Refuses pool delete when allocations still reference it.
SELECT count(*)::bigint FROM lir_allocations WHERE pool_id = $1;

-- name: ListPoolSourceSupernets :many
-- Returns supernets attached to a pool. The CIDR is normalized via
-- host/masklen so the wire type stays a plain string (the existing
-- IPAM queries use the same pattern).
SELECT id, fabric_id, vrf_id, site_id,
       host(prefix) || '/' || masklen(prefix) AS prefix,
       name, description, purpose,
       lir_pool_id, owner_organization_id,
       created_at, updated_at
FROM supernets
WHERE lir_pool_id = $1
ORDER BY prefix
LIMIT $2 OFFSET $3;

-- name: CountPoolSourceSupernets :one
SELECT count(*)::bigint FROM supernets WHERE lir_pool_id = $1;

-- name: GetSupernetForLirAttach :one
-- Compact projection used by the attach handler: current pool, owner
-- (for the tenant-exclusion check), and prefix (for the family-match
-- check, derived from ":" in the host form).
SELECT id, lir_pool_id, owner_organization_id,
       host(prefix) || '/' || masklen(prefix) AS prefix
FROM supernets
WHERE id = $1;

-- name: AttachSupernetToPool :exec
-- The migration's ck_supernet_lir_xor_owner CHECK rejects setting
-- lir_pool_id when owner_organization_id is non-null; the handler
-- pre-checks via GetSupernetForLirAttach so the user gets a 409 with
-- a clear message instead of an opaque constraint-violation 500.
UPDATE supernets
SET lir_pool_id = $2, updated_at = NOW()
WHERE id = $1;

-- name: DetachSupernetFromPool :exec
UPDATE supernets
SET lir_pool_id = NULL, updated_at = NOW()
WHERE id = $1 AND lir_pool_id = $2;

-- name: DetachAllPoolSupernets :exec
-- Used by DeleteLirPool: clears lir_pool_id on every source supernet
-- so the FK doesn't ON DELETE SET NULL silently (which would still
-- work, but losing the explicit step would also lose the audit trail
-- for the cascade).
UPDATE supernets
SET lir_pool_id = NULL, updated_at = NOW()
WHERE lir_pool_id = $1;

-- name: CountAllocationsForPoolSupernet :one
-- Refuses detach while allocations still trace back to this pool
-- supernet (LirAllocation.pool_supernet_id).
SELECT count(*)::bigint FROM lir_allocations WHERE pool_supernet_id = $1;
