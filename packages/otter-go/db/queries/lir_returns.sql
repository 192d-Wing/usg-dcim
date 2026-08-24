-- Return lifecycle + ARIN deassignment direction.
--
-- State machine on lir_allocations.status:
--   active           ──tenant request──▶ return_requested
--   return_requested ──NIC confirm─────▶ returned
--
-- When confirm flips status to 'returned', the same UPDATE also
-- promotes arin_status from 'registered' to 'removing' so the worker
-- picks the row up on its next tick. Allocations that never reached
-- ARIN (arin_status='none' or 'failed') just sit at returned with
-- the ARIN column untouched.
--
-- The carver (phase 4) already filters status='returned' rows out
-- of ListAllocatedPrefixesInPool, so returned ranges become
-- reclaimable as soon as confirm runs — no separate "free up the
-- supernet" step needed.

-- name: RequestReturnLirAllocation :one
-- Atomic: only flips active → return_requested. A concurrent racer
-- that confirmed the return already, or another tenant request that
-- already flipped, makes RETURNING empty and pgx surfaces ErrNoRows
-- → handler maps to 409.
UPDATE lir_allocations
SET status                    = 'return_requested',
    return_requested_at       = NOW(),
    return_requested_by_user_id = sqlc.arg(return_requested_by_user_id)::uuid,
    return_reason             = sqlc.arg(return_reason)::text,
    updated_at                = NOW()
WHERE id = sqlc.arg(id) AND status = 'active'
RETURNING id, request_id, organization_id, pool_id, pool_supernet_id,
          tenant_supernet_id,
          (host(prefix) || '/' || masklen(prefix))::text AS prefix,
          allocated_at, allocated_by_user_id, status,
          return_requested_at, return_requested_by_user_id, return_reason,
          returned_at, returned_by_user_id,
          arin_status, arin_net_handle, arin_last_attempt_at,
          arin_last_error, arin_attempts,
          created_at, updated_at;

-- name: ConfirmReturnLirAllocation :one
-- Two transitions in one statement: status → 'returned', and (only
-- if there's something to remove from ARIN) arin_status →
-- 'removing'. The CASE keeps arin_status='none' and 'failed' rows
-- untouched — those never registered upstream, so there's nothing
-- to deassign.
--
-- Resets arin_attempts/last_attempt_at/last_error on transition to
-- removing so the worker's backoff schedule starts fresh from
-- attempt 0.
UPDATE lir_allocations
SET status                = 'returned',
    returned_at           = NOW(),
    returned_by_user_id   = sqlc.arg(returned_by_user_id)::uuid,
    arin_status           = CASE
        WHEN arin_status = 'registered' THEN 'removing'
        ELSE arin_status
    END,
    arin_attempts         = CASE
        WHEN arin_status = 'registered' THEN 0
        ELSE arin_attempts
    END,
    arin_last_attempt_at  = CASE
        WHEN arin_status = 'registered' THEN NULL
        ELSE arin_last_attempt_at
    END,
    arin_last_error       = CASE
        WHEN arin_status = 'registered' THEN NULL
        ELSE arin_last_error
    END,
    updated_at            = NOW()
WHERE id = sqlc.arg(id) AND status = 'return_requested'
RETURNING id, request_id, organization_id, pool_id, pool_supernet_id,
          tenant_supernet_id,
          (host(prefix) || '/' || masklen(prefix))::text AS prefix,
          allocated_at, allocated_by_user_id, status,
          return_requested_at, return_requested_by_user_id, return_reason,
          returned_at, returned_by_user_id,
          arin_status, arin_net_handle, arin_last_attempt_at,
          arin_last_error, arin_attempts,
          created_at, updated_at;

-- name: ClaimNextArinRemoveJob :one
-- Same FOR UPDATE SKIP LOCKED shape as the submit claim. Eligibility:
-- arin_status='removing' OR ('failed' with a net_handle, meaning a
-- prior remove attempt failed). The arin_net_handle IS NOT NULL
-- guard is what distinguishes a failed-remove from a failed-submit
-- row (the submit handler claim does the inverse).
SELECT
    a.id                                      AS allocation_id,
    a.arin_status,
    a.arin_attempts,
    (host(a.prefix) || '/' || masklen(a.prefix))::text AS prefix,
    -- Both handles are guaranteed non-NULL by the WHERE guards below;
    -- COALESCE makes that visible to sqlc's nullability inference.
    COALESCE(a.arin_net_handle, '')           AS net_handle,
    COALESCE(p.arin_parent_net_handle, '')    AS parent_net_handle
FROM lir_allocations a
JOIN lir_pools      p ON p.id = a.pool_id
WHERE p.arin_parent_net_handle IS NOT NULL
  AND a.arin_net_handle IS NOT NULL
  AND (
        a.arin_status = 'removing'
        OR (
            a.arin_status = 'failed'
            AND a.arin_attempts < sqlc.arg(max_attempts)::int
            AND (
                a.arin_last_attempt_at IS NULL
                OR a.arin_last_attempt_at + (
                    CASE a.arin_attempts
                        WHEN 1 THEN INTERVAL '1 minute'
                        WHEN 2 THEN INTERVAL '5 minutes'
                        WHEN 3 THEN INTERVAL '30 minutes'
                        ELSE INTERVAL '2 hours'
                    END
                ) < NOW()
            )
        )
      )
ORDER BY a.arin_status, a.returned_at NULLS LAST, a.allocated_at
FOR UPDATE OF a SKIP LOCKED
LIMIT 1;

-- name: MarkArinRemoved :exec
-- Successful deassignment. arin_net_handle stays for the audit
-- trail; arin_status flips to 'removed' as the terminal state.
UPDATE lir_allocations
SET arin_status        = 'removed',
    arin_attempts      = arin_attempts + 1,
    arin_last_attempt_at = NOW(),
    arin_last_error    = NULL,
    updated_at         = NOW()
WHERE id = $1;
