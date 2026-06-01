-- DHCP drift summary (PR 9 of the DHCP push port).
--
-- Mirrors Python's GET /api/v1/ipam/dhcp/drift-summary handler
-- (api/ipam.py:2611). Three queries the handler runs in sequence:
--
--   1. ListDhcpServersForDriftSummary — every server in scope
--      (projection includes last_push_at/last_push_status which the
--      basic DhcpServer projection omits)
--   2. ListDhcpScopeDriftStatusByServers — every live scope on the
--      returned servers, with its persisted last_diff_status (set by
--      the per-scope diff endpoint + the drift_check cron + push
--      success)
--   3. ListFiringDhcpDriftAlertKeys — dedupe keys for every firing
--      Alert whose dedupe_key starts with "dhcp-drift:" (the per-
--      scope alert convention from PR 87)
--
-- All three are read-only; the handler then runs them through the
-- pure aggregator in internal/dhcp/driftsummary.

-- name: ListDhcpServersForDriftSummary :many
-- Same ABAC filter shape as ListDhcpServers (PR 54): an optional
-- fabric_id and an optional set of in-scope fabric IDs. Returns the
-- columns the per-server summary needs.
SELECT id, name, fabric_id, enabled, last_push_at, last_push_status
FROM dhcp_servers
WHERE (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]))
ORDER BY name;

-- name: ListDhcpScopeDriftStatusByServers :many
-- Project (id, dhcp_server_id, last_diff_status) for every scope on
-- the supplied server set. Excludes soft-deleted scopes; Python's
-- handler does the same at api/ipam.py:2667. The aggregator buckets
-- a NULL last_diff_status as "never_pushed" (operator's mental
-- model).
SELECT id, dhcp_server_id, last_diff_status
FROM dhcp_scopes
WHERE dhcp_server_id = ANY($1::uuid[])
  AND deleted_at IS NULL;

-- name: ListFiringDhcpDriftAlertKeys :many
-- One row per firing dhcp-drift alert. The dedupe_key carries the
-- scope UUID as a suffix ("dhcp-drift:<scope_id>", PR 87). Returns
-- the full key so the Go aggregator's string-split mirrors Python's
-- key.split(":", 1)[1] exactly.
SELECT dedupe_key
FROM alerts
WHERE state = 'firing'
  AND dedupe_key LIKE 'dhcp-drift:%';
