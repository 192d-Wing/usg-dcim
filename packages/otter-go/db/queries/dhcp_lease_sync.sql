-- DHCP lease ingest from Kea (PR 15 of N). Powers
-- services/kea.py:sync_dhcp_server. Three reads + two writes:
--
--   1. ListSubnetsForFabricLeaseSync — every subnet in the server's
--      fabric. The matcher (internal/dhcp/leasesync) picks the
--      most-specific subnet that contains the lease address.
--   2. FindDhcpLeaseIPAddress — by (subnet_id, address). Returns
--      id + source so the orchestrator can branch: source=dhcp →
--      UPDATE; not in IPAM → INSERT; static/reservation → leave
--      alone.
--   3. UpdateDhcpLease — refresh an existing source=dhcp row's mac,
--      status=active, lease_expires_at, plus a "backfill dns_name
--      only when it's NULL" guard (Python parity: `parsed.hostname
--      or existing.dns_name` at services/kea.py:328).
--   4. InsertDhcpLease — new source=dhcp row when the lease isn't
--      already in IPAM.
--   5. UpdateDhcpServerSyncState — record last_sync_* on the
--      DhcpServer row so the LIST endpoint surfaces the freshness.

-- name: ListSubnetsForFabricLeaseSync :many
SELECT id, prefix::text AS prefix
FROM subnets
WHERE fabric_id = $1;

-- name: FindDhcpLeaseIPAddress :one
SELECT id, source::text AS source
FROM ip_addresses
WHERE subnet_id = sqlc.arg(subnet_id)
  AND address    = sqlc.arg(address)::inet;

-- name: UpdateDhcpLease :exec
-- Mac is overwritten unconditionally (the lease tells us the
-- current binding). dns_name uses `parsed.hostname or existing`
-- semantics — Python at services/kea.py:328 reads
-- `existing.dns_name = parsed.hostname or existing.dns_name` so a
-- truthy new hostname OVERWRITES the existing column; an empty new
-- hostname keeps the existing one. The COALESCE(new, dns_name) shape
-- below preserves the same Python semantics: NULL incoming →
-- keep existing; non-NULL incoming → overwrite. The Go orchestrator
-- maps empty Hostname to *string nil before this query runs, so
-- "" arrives as NULL and the column stays put — matching Python's
-- truthy-or fallback exactly.
UPDATE ip_addresses
SET dhcp_mac               = sqlc.narg(dhcp_mac)::text,
    dns_name               = COALESCE(sqlc.narg(dns_name)::text, dns_name),
    dhcp_lease_expires_at  = sqlc.narg(dhcp_lease_expires_at)::timestamptz,
    status                 = 'active',
    updated_at             = NOW()
WHERE id = sqlc.arg(id);

-- name: InsertDhcpLease :exec
-- Fresh dhcp-source row. role defaults to 'data' to match Python's
-- IPAddress factory default at services/kea.py:332-340.
INSERT INTO ip_addresses (
    id, subnet_id, address, role, status, source,
    dns_name, dhcp_mac, dhcp_lease_expires_at,
    created_at, updated_at
)
VALUES (gen_random_uuid(), sqlc.arg(subnet_id), sqlc.arg(address)::inet,
        'data', 'active', 'dhcp',
        sqlc.narg(dns_name), sqlc.narg(dhcp_mac),
        sqlc.narg(dhcp_lease_expires_at), NOW(), NOW());

-- name: UpdateDhcpServerSyncState :exec
-- last_sync_at/status/error/lease_count are written regardless of
-- outcome so operators see the failure in the UI without tail -f
-- on logs. Error is truncated at the Go layer to 2000 chars
-- matching Python's services/kea.py:297 `error[:2000]` clamp.
UPDATE dhcp_servers
SET last_sync_at         = sqlc.arg(last_sync_at)::timestamptz,
    last_sync_status     = sqlc.arg(last_sync_status)::text,
    last_sync_error      = sqlc.narg(last_sync_error)::text,
    last_sync_lease_count = sqlc.narg(last_sync_lease_count)::int,
    updated_at           = NOW()
WHERE id = sqlc.arg(id);

-- ===== Cron driver queries (PR 16) =====

-- name: ListEnabledDhcpServersForLeaseSync :many
-- Projection the dhcp_sync cron walks. Includes the auth password
-- because the orchestrator hands the server to KeaClient.New —
-- distinct from the bundle/push projection (which omits the
-- password for the operator-facing read endpoint).
SELECT id, fabric_id, kea_url, auth_username, auth_password
FROM dhcp_servers
WHERE enabled = TRUE
ORDER BY name;

-- name: DeprecateExpiredDhcpLeases :execrows
-- dhcp_age_out step 1 (services/kea.py:372-384). Flip active dhcp
-- rows whose lease lapsed > grace_seconds ago to status=deprecated.
-- Static + reservation rows are untouched (the source filter).
UPDATE ip_addresses
SET status     = 'deprecated',
    updated_at = NOW()
WHERE source = 'dhcp'
  AND status = 'active'
  AND dhcp_lease_expires_at IS NOT NULL
  AND dhcp_lease_expires_at < $1::timestamptz;

-- name: DeleteDeprecatedDhcpLeases :execrows
-- dhcp_age_out step 2 (services/kea.py:388-399). Hard-delete dhcp
-- rows that have been deprecated > 1 day — they're noise at this
-- point. Same source guard as step 1.
DELETE FROM ip_addresses
WHERE source = 'dhcp'
  AND status = 'deprecated'
  AND dhcp_lease_expires_at IS NOT NULL
  AND dhcp_lease_expires_at < $1::timestamptz;
