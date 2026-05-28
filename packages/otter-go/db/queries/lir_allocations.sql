-- LIR allocation engine + approval / rejection + allocation reads.
--
-- Approval flow: the handler reads pool source supernets + existing
-- allocations, runs the Go carver to pick a free prefix, then calls
-- ApproveLirRequest — a single CTE-based statement that atomically
-- (a) inserts the tenant Supernet in the landing fabric, (b) inserts
-- the LirAllocation row linking pool → tenant, and (c) flips the
-- request to 'approved'. The whole statement rolls back as a unit if
-- any CHECK / FK fails — no separate transaction infrastructure
-- needed.
--
-- Concurrency note: there is no advisory lock or unique-on-prefix
-- constraint, so two approvals against the same pool that race could
-- pick the same free slot and both succeed. In practice NIC review
-- is sequential and the UI surfaces one request at a time, so the
-- race is operationally rare. A follow-up could add either a per-
-- pool advisory lock or a unique (fabric_id, prefix) constraint on
-- supernets; both are out of scope for phase 4.

-- name: GetLandingFabric :one
-- Looks up the system-managed fabric where new tenant allocations
-- land. Migration 0065 seeded a row with slug 'lir-unassigned' + its
-- default VRF; the approval handler reads both IDs in one round-trip
-- so it can stamp them on the new tenant Supernet.
SELECT f.id AS fabric_id, v.id AS default_vrf_id
FROM fabrics f
JOIN vrfs v ON v.fabric_id = f.id AND v.is_default = TRUE
WHERE f.slug = $1::text;

-- name: ListPoolSupernetsForCarve :many
-- Source supernets the carver iterates over, ordered by network
-- address so first-fit picks the lowest-numbered free range. Only
-- supernets actually attached to the pool are returned.
SELECT id, host(prefix) || '/' || masklen(prefix) AS prefix
FROM supernets
WHERE lir_pool_id = $1
ORDER BY prefix;

-- name: ListAllocatedPrefixesInPool :many
-- Existing allocations the carver must avoid overlapping. Returned
-- allocations are excluded (status='returned') because their carved
-- range is reusable; active and return_requested rows still occupy
-- space.
SELECT pool_supernet_id,
       host(prefix) || '/' || masklen(prefix) AS prefix
FROM lir_allocations
WHERE pool_id = $1 AND status != 'returned';

-- name: ApproveLirRequest :one
-- The atomic CTE: insert tenant Supernet → insert LirAllocation →
-- flip request to approved. Single statement = atomic by Postgres
-- semantics; partial state cannot escape the network round-trip.
--
-- $1  request_id            (request being approved)
-- $2  decided_by_user_id    (NIC operator's principal subject)
-- $3  decision_notes        (optional, nullable)
-- $4  approved_pool_id      (the pool the NIC approved into; may
--                            differ from request.pool_id)
-- $5  pool_supernet_id      (the pool source supernet that gets carved)
-- $6  organization_id       (tenant — denormalized off the request)
-- $7  prefix                (the carved CIDR, as text — cast to cidr)
-- $8  landing_fabric_id     (where the new tenant Supernet lives)
-- $9  landing_vrf_id        (default VRF on the landing fabric)
-- $10 supernet_purpose      (nullable — from pool default or request)
-- $11 arin_initial_status   ('none' when pool has no ARIN handle,
--                            'pending' when it does — handler decides)
WITH new_supernet AS (
    INSERT INTO supernets (
        id, fabric_id, vrf_id, prefix,
        owner_organization_id, purpose,
        created_at, updated_at
    )
    VALUES (
        gen_random_uuid(),
        $8::uuid, $9::uuid, $7::cidr,
        $6::uuid, sqlc.narg(supernet_purpose)::text,
        NOW(), NOW()
    )
    RETURNING id, host(prefix) || '/' || masklen(prefix) AS prefix
),
new_allocation AS (
    INSERT INTO lir_allocations (
        id, request_id, organization_id, pool_id, pool_supernet_id,
        tenant_supernet_id, prefix, allocated_at, allocated_by_user_id,
        status, arin_status, arin_attempts, created_at, updated_at
    )
    SELECT
        gen_random_uuid(), $1::uuid, $6::uuid, $4::uuid, $5::uuid,
        new_supernet.id, $7::cidr, NOW(), $2::uuid,
        'active', sqlc.arg(arin_initial_status)::text, 0, NOW(), NOW()
    FROM new_supernet
    RETURNING id, tenant_supernet_id
),
updated_request AS (
    UPDATE lir_requests
    SET status = 'approved',
        decided_at = NOW(),
        decided_by_user_id = $2::uuid,
        decision_notes = sqlc.narg(decision_notes)::text,
        approved_pool_id = $4::uuid,
        updated_at = NOW()
    WHERE id = $1::uuid AND status = 'pending_approval'
    RETURNING id
)
SELECT
    updated_request.id   AS request_id,
    new_allocation.id    AS allocation_id,
    new_allocation.tenant_supernet_id AS tenant_supernet_id
FROM updated_request, new_allocation;

-- name: RejectLirRequest :one
-- Atomic check-and-flip same shape as cancel — only matches pending.
UPDATE lir_requests
SET status = 'rejected',
    decided_at = NOW(),
    decided_by_user_id = $2::uuid,
    decision_notes = sqlc.arg(reason)::text,
    updated_at = NOW()
WHERE id = $1 AND status = 'pending_approval'
RETURNING id, organization_id, requester_user_id, pool_id, site_id,
          ip_family, prefix_length, purpose, classification, justification,
          status, submitted_at, decided_at, decided_by_user_id,
          decision_notes, approved_pool_id, created_at, updated_at;

-- name: GetLirAllocation :one
SELECT id, request_id, organization_id, pool_id, pool_supernet_id,
       tenant_supernet_id,
       host(prefix) || '/' || masklen(prefix) AS prefix,
       allocated_at, allocated_by_user_id, status,
       return_requested_at, return_requested_by_user_id, return_reason,
       returned_at, returned_by_user_id,
       arin_status, arin_net_handle, arin_last_attempt_at,
       arin_last_error, arin_attempts,
       created_at, updated_at
FROM lir_allocations
WHERE id = $1;

-- name: ListLirAllocations :many
-- Org-scope filter mirrors the request listing pattern.
SELECT id, request_id, organization_id, pool_id, pool_supernet_id,
       tenant_supernet_id,
       host(prefix) || '/' || masklen(prefix) AS prefix,
       allocated_at, allocated_by_user_id, status,
       return_requested_at, return_requested_by_user_id, return_reason,
       returned_at, returned_by_user_id,
       arin_status, arin_net_handle, arin_last_attempt_at,
       arin_last_error, arin_attempts,
       created_at, updated_at
FROM lir_allocations
WHERE (sqlc.narg(scope_org_ids)::uuid[] IS NULL
       OR organization_id = ANY(sqlc.narg(scope_org_ids)::uuid[]))
  AND (sqlc.narg(status_filter)::text IS NULL OR status = sqlc.narg(status_filter)::text)
ORDER BY allocated_at DESC
LIMIT $1 OFFSET $2;

-- name: CountLirAllocations :one
SELECT count(*)::bigint
FROM lir_allocations
WHERE (sqlc.narg(scope_org_ids)::uuid[] IS NULL
       OR organization_id = ANY(sqlc.narg(scope_org_ids)::uuid[]))
  AND (sqlc.narg(status_filter)::text IS NULL OR status = sqlc.narg(status_filter)::text);
