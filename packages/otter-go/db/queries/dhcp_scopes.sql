-- ===== DHCP scope tombstone retention =====
-- Go port of Python's dhcp_scope_tombstone_purge arq cron
-- (worker.py:141). Soft-deletes set deleted_at; this query hard-
-- deletes rows whose tombstone is older than the retention window
-- so the table doesn't grow unbounded. The Kea-side DELETE already
-- ran when the user soft-deleted the scope (PR 74) — this only
-- drops orphaned tombstone rows from Postgres.
-- name: PurgeExpiredDhcpScopeTombstones :exec
DELETE FROM dhcp_scopes
WHERE deleted_at IS NOT NULL
  AND deleted_at < $1;
