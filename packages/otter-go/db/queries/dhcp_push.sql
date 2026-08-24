-- ===== DHCP push orchestrator (PR 2 of the DHCP push port) =====
-- The push_scope orchestrator (internal/dhcp/push) drives a single
-- DhcpScope onto its parent Kea server via the Kea Control Agent
-- client (PR #222). Mirrors Python's services/dhcp_push.py:392
-- push_scope. Bulk endpoints + the dhcp_sync / dhcp_age_out crons
-- come in subsequent PRs.
--
-- Queries here are deliberately narrow projections — push doesn't
-- need the full DhcpServer or DhcpScope row, only the columns the
-- orchestrator reads or writes back. Keeping the queries tight
-- mirrors PR #217's bundle-row pattern and avoids deserialization
-- cost on the per-scope-mutation hot path.

-- name: GetDhcpScopeForPush :one
-- Loads everything the renderer + orchestrator need: prefix,
-- pools/options/reservations JSON, the four timer pointers, the
-- ip_family discriminator, the optional template_id, and the
-- nullable kea_subnet_id (NULL = first push, non-NULL = update).
-- Also returns dhcp_server_id so the caller doesn't pay a second
-- round-trip to discover the parent.
SELECT id, dhcp_server_id, ip_family, prefix::text AS prefix,
       pools_json, pd_pools_json, options_json, reservations_json,
       valid_lifetime_seconds, renew_timer_seconds, rebind_timer_seconds,
       preferred_lifetime_seconds, kea_subnet_id, template_id, enabled
FROM dhcp_scopes
WHERE id = $1
  AND deleted_at IS NULL;

-- name: GetDhcpServerForPush :one
-- Auth credentials INCLUDED here — the bundle endpoint
-- (DhcpServerBundleRow) deliberately omits them because the
-- response shape was operator-facing, but push runs server-to-server
-- so it needs the basic-auth pair. Same struct shouldn't be reused
-- for both: a sloppy refactor could leak the password into a bundle
-- response. Returns enabled so the orchestrator can refuse early.
SELECT id, kea_url, auth_username, auth_password, enabled
FROM dhcp_servers
WHERE id = $1;

-- name: GetDhcpScopeTemplateForPush :one
-- Loads the template the renderer's MergeTemplateIntoScope needs.
-- Same projection as ListDhcpScopeTemplatesByIDs (PR #217), single-
-- row variant for the per-scope push path. Returns no-row when the
-- scope's template_id points at a deleted template — the
-- orchestrator handles it as MissingTemplate and renders with
-- scope-only values, matching Python.
SELECT id, fabric_id, name, ip_family, options_json,
       valid_lifetime_seconds, renew_timer_seconds, rebind_timer_seconds,
       preferred_lifetime_seconds, description, created_at, updated_at
FROM dhcp_scope_templates
WHERE id = $1;

-- name: ListKeaSubnetIDsForServer :many
-- Drives AllocateKeaSubnetID. Returns every non-NULL kea_subnet_id
-- already claimed under one DhcpServer so the allocator can pick
-- the lowest free positive int. A production fleet might persist a
-- per-server sequence; the O(n) scan matches Python's posture and
-- is fine until a server has thousands of scopes (a wild outlier).
-- The ::int cast makes the WHERE-guaranteed non-NULL visible to
-- sqlc (the column itself is nullable).
SELECT kea_subnet_id::int
FROM dhcp_scopes
WHERE dhcp_server_id = $1
  AND kea_subnet_id IS NOT NULL
  AND deleted_at IS NULL;

-- name: UpdateDhcpScopeKeaSubnetID :exec
-- Two callers: AllocateKeaSubnetID writes the freshly-allocated id
-- before the Kea RPC fires; on RPC failure the orchestrator calls
-- this again with NULL to roll back the optimistic allocation.
-- Rolling back the id (rather than leaving it claimed) lets a retry
-- pick the same low integer rather than fragmenting the namespace.
UPDATE dhcp_scopes
SET kea_subnet_id = $2,
    updated_at    = NOW()
WHERE id = $1;

-- name: UpdateDhcpScopeAfterSuccessfulPush :exec
-- Successful push is by construction a re-sync. Clears the drift
-- cache so LIST and push-drifted reflect reality. Don't touch it on
-- error — the previous diff result is still the operator's best
-- information. Matches services/dhcp_push.py:492-495.
UPDATE dhcp_scopes
SET last_diff_at         = NOW(),
    last_diff_status     = 'in_sync',
    last_diff_delta_json = NULL,
    updated_at           = NOW()
WHERE id = $1;

-- name: UpdateDhcpServerLastPush :exec
-- last_push_at/status/error are written regardless of outcome so
-- operators see the failure in the UI without tail -f'ing logs.
-- Error string is truncated to 2048 chars at the Go layer to match
-- the column constraint (the Python helper at services/dhcp_push.py:570
-- does the same `error[:2048]`).
UPDATE dhcp_servers
SET last_push_at     = NOW(),
    last_push_status = $2,
    last_push_error  = $3,
    updated_at       = NOW()
WHERE id = $1;

-- name: InsertDhcpScopePushHistory :exec
-- Append-only attempt log. Every push attempt (success, error,
-- transport failure) gets a row so the UI can show "last N pushes
-- for this scope" and the API can compute rolling success rates
-- per fabric / server. server_id is denormalized off scope so
-- server-wide history queries don't pay the scope→server join.
-- The history row is written WITHOUT a transactional barrier —
-- Python flushes inside the same outer transaction the caller
-- controls; Go does the same via the same pgxpool.Pool that
-- handles every other write in the request.
INSERT INTO dhcp_scope_push_history
       (id, scope_id, server_id, operation, kea_subnet_id, status, error, duration_ms, attempted_at)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, NOW());

-- name: ListDhcpScopePushHistoryByScope :many
-- Newest-first attempt log for one scope. Uses the
-- (scope_id, attempted_at DESC) index from migration 0064 so the
-- read is index-only. `limit` caps the response size for the
-- common "show recent activity" UI pattern; caller validates
-- 1 <= limit <= 500 against the column constraint.
SELECT id, scope_id, server_id, operation, kea_subnet_id, status,
       error, duration_ms, attempted_at
FROM dhcp_scope_push_history
WHERE scope_id = $1
ORDER BY attempted_at DESC
LIMIT $2;

-- ===== Bulk endpoints (PR 6) =====
--
-- Each query returns the narrow projection the bulk orchestrator's
-- per-scope loop needs to drive PushScope / DiffScope: just the
-- scope id (those orchestrators reload the full row themselves).
-- DiffAll also projects last_diff_status so the loop can compute
-- status transitions without a per-scope re-read before persist
-- overwrites the column.

-- name: ListEnabledScopeIDsForServer :many
-- Drives push-all. Enabled, not soft-deleted. Order by created_at
-- so push order is deterministic across runs.
SELECT id FROM dhcp_scopes
WHERE dhcp_server_id = $1
  AND enabled = TRUE
  AND deleted_at IS NULL
ORDER BY created_at;

-- name: ListDriftedScopeIDsForServer :many
-- Drives push-drifted. Same filters as push-all plus the persisted
-- drift status filter. Reads the cache populated by diff_scope /
-- diff-all / the cron — operator should run a diff pass first so
-- the cache is fresh.
SELECT id FROM dhcp_scopes
WHERE dhcp_server_id = $1
  AND enabled = TRUE
  AND deleted_at IS NULL
  AND last_diff_status = 'drifted'
ORDER BY created_at;

-- name: ListAllScopeIDsAndPriorDriftForServer :many
-- Drives diff-all. Includes DISABLED scopes (drift on a disabled
-- scope is informational — operator may have flipped enabled=False
-- locally while Kea still serves it). Projects last_diff_status so
-- the loop can compute transitions before persist_diff_state
-- overwrites it; projects prefix so transitions on never_pushed
-- scopes (where the orchestrator short-circuits without rendering
-- a DCIM subnet, so prefix isn't available from the Result) still
-- carry the prefix field operators key off.
SELECT id, prefix::text AS prefix, last_diff_status FROM dhcp_scopes
WHERE dhcp_server_id = $1
  AND deleted_at IS NULL
ORDER BY created_at;

-- name: WriteDhcpScopeDiffState :exec
-- Mirrors persist_diff_state at services/dhcp_push.py:934. Updates
-- the three last_diff_* columns: timestamp moves with NOW();
-- status carries one of in_sync / drifted / missing_from_kea /
-- never_pushed / error; delta_json carries the per-key delta map
-- when status='drifted' and NULL on every other terminal state
-- (a stale delta would mislead operators reading the LIST
-- endpoint). Used by the per-scope diff endpoint, diff-all
-- preflight, and push-drifted preflight.
UPDATE dhcp_scopes
SET last_diff_at         = NOW(),
    last_diff_status     = $2,
    last_diff_delta_json = $3,
    updated_at           = NOW()
WHERE id = $1;
