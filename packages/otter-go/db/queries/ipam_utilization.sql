-- Queries the ipam_utilization_sweep cron uses to emit Prometheus
-- gauges. Same shape Python's worker.py:ipam_utilization_sweep runs
-- (three SELECTs, then in-memory fold): one row per subnet, one row
-- per supernet, one row per (subnet, active+reserved count). Kept
-- unpaginated because the cron walks the full fleet on every tick.

-- name: ListSubnetsForUtilization :many
SELECT id, fabric_id, supernet_id,
       (host(prefix) || '/' || masklen(prefix))::text AS prefix
FROM subnets;

-- name: ListSupernetsForUtilization :many
SELECT id, fabric_id,
       (host(prefix) || '/' || masklen(prefix))::text AS prefix
FROM supernets;

-- name: ListActiveReservedAddressCountsBySubnet :many
-- Matches Python's `WHERE status IN ('active', 'reserved')` filter.
-- Subnets with no active/reserved addresses are absent from the
-- result; the Go fold treats missing keys as count=0 (same as
-- Python's dict.get(s.id, 0)).
SELECT subnet_id, COUNT(*)::bigint AS used_count
FROM ip_addresses
WHERE status::text IN ('active', 'reserved')
  AND subnet_id IS NOT NULL
GROUP BY subnet_id;
