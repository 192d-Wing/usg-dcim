-- LIR request lifecycle — submit, list (org-scope filterable), get,
-- cancel. Approve / reject move into a follow-up alongside the
-- allocation engine (phase 4).
--
-- Status state machine (CHECK ck_lir_request_status in migration 0065):
--   pending_approval → approved | rejected | cancelled | failed
-- Phase 3 only exercises pending_approval → cancelled.

-- name: CreateLirRequest :one
INSERT INTO lir_requests (
    id, organization_id, requester_user_id, pool_id, site_id,
    ip_family, prefix_length, purpose, classification, justification,
    status, submitted_at, created_at, updated_at
)
VALUES (
    gen_random_uuid(), sqlc.arg(organization_id), sqlc.arg(requester_user_id),
    sqlc.narg(pool_id), sqlc.narg(site_id),
    sqlc.arg(ip_family)::smallint, sqlc.arg(prefix_length)::smallint,
    sqlc.narg(purpose), sqlc.narg(classification), sqlc.arg(justification),
    'pending_approval', NOW(), NOW(), NOW()
)
RETURNING id, organization_id, requester_user_id, pool_id, site_id,
          ip_family, prefix_length, purpose, classification, justification,
          status, submitted_at, decided_at, decided_by_user_id,
          decision_notes, approved_pool_id, created_at, updated_at;

-- name: GetLirRequest :one
SELECT id, organization_id, requester_user_id, pool_id, site_id,
       ip_family, prefix_length, purpose, classification, justification,
       status, submitted_at, decided_at, decided_by_user_id,
       decision_notes, approved_pool_id, created_at, updated_at
FROM lir_requests
WHERE id = $1;

-- name: ListLirRequests :many
-- Org-scope filter: pass NULL for scope_org_ids when the caller has
-- global scope (no WHERE filter on org); pass a non-empty array to
-- restrict; an empty array is treated by the handler as "no orgs in
-- scope" and short-circuits to an empty page without hitting this
-- query. Same shape as the existing IPAM scope filters use.
SELECT id, organization_id, requester_user_id, pool_id, site_id,
       ip_family, prefix_length, purpose, classification, justification,
       status, submitted_at, decided_at, decided_by_user_id,
       decision_notes, approved_pool_id, created_at, updated_at
FROM lir_requests
WHERE (sqlc.narg(scope_org_ids)::uuid[] IS NULL
       OR organization_id = ANY(sqlc.narg(scope_org_ids)::uuid[]))
  AND (sqlc.narg(status_filter)::text IS NULL OR status = sqlc.narg(status_filter)::text)
ORDER BY submitted_at DESC
LIMIT $1 OFFSET $2;

-- name: CountLirRequests :one
SELECT count(*)::bigint
FROM lir_requests
WHERE (sqlc.narg(scope_org_ids)::uuid[] IS NULL
       OR organization_id = ANY(sqlc.narg(scope_org_ids)::uuid[]))
  AND (sqlc.narg(status_filter)::text IS NULL OR status = sqlc.narg(status_filter)::text);

-- name: CancelLirRequest :one
-- Atomic check-and-flip: the WHERE matches only pending rows, so a
-- request that already moved to approved/rejected/failed (or was
-- already cancelled) leaves zero rows and the handler returns 409.
-- decided_at + decided_by_user_id stay NULL on cancel — cancel
-- distinguishes itself from approve/reject in the audit trail
-- through the status value, and the migration's
-- ck_lir_request_decision_consistency CHECK allows cancelled rows
-- to skip those fields.
UPDATE lir_requests
SET status = 'cancelled',
    decision_notes = sqlc.narg(notes)::text,
    updated_at = NOW()
WHERE id = $1 AND status = 'pending_approval'
RETURNING id, organization_id, requester_user_id, pool_id, site_id,
          ip_family, prefix_length, purpose, classification, justification,
          status, submitted_at, decided_at, decided_by_user_id,
          decision_notes, approved_pool_id, created_at, updated_at;
