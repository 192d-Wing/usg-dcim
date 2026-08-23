-- ARIN Reg-RWS worker queries — job claim, status transitions, and
-- the manual retry reset.
--
-- Phase 5 only covers the submission direction (allocation → ARIN
-- reassign-detailed POST). Deassignment (delete) ships in phase 6
-- alongside the return-lifecycle handlers; the same claim shape will
-- pick up rows with arin_status='removing'.
--
-- Job eligibility:
--   * arin_status='pending'         → never tried, claim immediately
--   * arin_status='failed' AND      → retry per backoff schedule
--     arin_attempts < $max AND      → cap at 5 attempts total
--     arin_last_attempt_at + b < NOW
-- Excluded:
--   * pool's arin_parent_net_handle IS NULL (LIR-internal pool, no
--     ARIN integration)
--   * allocation already has arin_net_handle (already registered;
--     a 'failed' row with a handle means the removal failed, which
--     is phase 6)
--
-- Concurrency: FOR UPDATE OF a SKIP LOCKED inside the worker's
-- transaction means two worker pods running concurrently get
-- disjoint rows. Crash recovery is automatic — the row lock
-- releases when the worker's tx aborts (idle-disconnect or panic),
-- so the next tick picks the row up again.

-- name: GetSystemSetting :one
SELECT key, value, updated_at
FROM system_settings
WHERE key = $1;

-- name: GetSystemSettings :many
-- Batch lookup for the worker's LoadConfig — fetches every ARIN
-- setting (endpoint, api_key, enabled) in one round-trip instead of
-- three. Caller materializes a map[key]value on its side.
SELECT key, value, updated_at
FROM system_settings
WHERE key = ANY(sqlc.arg(keys)::text[]);

-- name: UpsertSystemSetting :exec
INSERT INTO system_settings (key, value, updated_at)
VALUES ($1, $2, NOW())
ON CONFLICT (key) DO UPDATE SET
    value = EXCLUDED.value,
    updated_at = NOW();

-- name: DeleteSystemSetting :exec
-- Reset path for the system-DNS admin endpoint: clearing the override
-- removes the row so get_system_dns_upstreams falls back to the
-- env-backed default. Idempotent — no-op when the row is absent
-- (Python parity).
DELETE FROM system_settings WHERE key = $1;

-- name: ClaimNextArinSubmitJob :one
-- Returns one eligible allocation joined with the data the worker
-- needs to build the ARIN payload: the tenant Organization's POC +
-- address fields and the pool's upstream net handle. The row stays
-- locked until the caller's tx commits or rolls back.
--
-- max_attempts (typically 5). Filter applies only to 'failed'
--      rows; 'pending' rows are always eligible regardless of attempt
--      count (a freshly-approved allocation has attempts=0).
SELECT
    a.id                                      AS allocation_id,
    a.arin_status,
    a.arin_attempts,
    (host(a.prefix) || '/' || masklen(a.prefix))::text AS prefix,
    a.organization_id,
    -- Guaranteed non-NULL by the WHERE guard below; COALESCE makes
    -- that visible to sqlc's nullability inference.
    COALESCE(p.arin_parent_net_handle, '')    AS parent_net_handle,
    o.name                                    AS org_name,
    o.arin_org_id                             AS org_arin_handle,
    o.address_line1, o.address_line2, o.city, o.state_province,
    o.postal_code, o.country,
    o.admin_poc_name, o.admin_poc_email, o.admin_poc_phone,
    o.tech_poc_name,  o.tech_poc_email,  o.tech_poc_phone,
    o.abuse_poc_name, o.abuse_poc_email, o.abuse_poc_phone
FROM lir_allocations a
JOIN organizations  o ON o.id = a.organization_id
JOIN lir_pools      p ON p.id = a.pool_id
WHERE p.arin_parent_net_handle IS NOT NULL
  AND a.arin_net_handle IS NULL
  AND (
        a.arin_status = 'pending'
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
-- 'pending' < 'failed' alphabetically: pending rows surface first;
-- among same-status rows, the oldest allocated wins.
ORDER BY a.arin_status, a.allocated_at
FOR UPDATE OF a SKIP LOCKED
LIMIT 1;

-- name: MarkArinRegistered :exec
-- Records a successful ARIN reassignment. arin_attempts still bumps
-- so the row carries a true success count for ops dashboards.
UPDATE lir_allocations
SET arin_status        = 'registered',
    arin_net_handle    = sqlc.arg(net_handle)::text,
    arin_attempts      = arin_attempts + 1,
    arin_last_attempt_at = NOW(),
    arin_last_error    = NULL,
    updated_at         = NOW()
WHERE id = sqlc.arg(id);

-- name: MarkArinFailed :exec
-- Records a failed attempt. The next claim picks the row up again
-- once arin_attempts < max AND the backoff interval elapses; once
-- arin_attempts hits the max the row sits in 'failed' indefinitely
-- and only the manual retry endpoint can reset it.
UPDATE lir_allocations
SET arin_status        = 'failed',
    arin_attempts      = arin_attempts + 1,
    arin_last_attempt_at = NOW(),
    arin_last_error    = sqlc.arg(error)::text,
    updated_at         = NOW()
WHERE id = sqlc.arg(id);

-- name: ResetArinJobForRetry :exec
-- Operator-triggered retry. Routes by direction (inferred from
-- arin_net_handle): rows with no handle were submit failures and
-- get flipped back to 'pending' for the submit claim; rows with a
-- handle were remove failures and get flipped back to 'removing'
-- for the remove claim. Earlier shape always wrote 'pending' which
-- orphaned remove-direction rows — the submit-claim query refuses
-- them on `arin_net_handle IS NULL` and the remove-claim query
-- refuses them on status not in ('removing','failed').
--
-- Refuses if the allocation is already 'registered' or in-flight
-- ('pending'/'removing') — that case isn't a retry; the worker
-- is either already handling it or it succeeded.
UPDATE lir_allocations
SET arin_status        = CASE
        WHEN arin_net_handle IS NULL THEN 'pending'
        ELSE 'removing'
    END,
    arin_attempts      = 0,
    arin_last_attempt_at = NULL,
    arin_last_error    = NULL,
    updated_at         = NOW()
WHERE id = $1 AND arin_status IN ('failed', 'none');
