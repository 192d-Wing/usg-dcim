-- ===== DNS blocklists =====
-- name: ListDnsBlocklists :many
SELECT id, name, fabric_id, action::text AS action,
       host(sink_ipv4) AS sink_ipv4,
       host(sink_ipv6) AS sink_ipv6,
       enabled, description, created_at, updated_at
FROM dns_blocklists
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]))
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountDnsBlocklists :one
SELECT count(*)::bigint FROM dns_blocklists
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]));

-- name: GetDnsBlocklist :one
SELECT id, name, fabric_id, action::text AS action,
       host(sink_ipv4) AS sink_ipv4,
       host(sink_ipv6) AS sink_ipv6,
       enabled, description, created_at, updated_at
FROM dns_blocklists
WHERE id = $1;

-- ===== DNS blocklist entries =====
-- name: ListDnsBlocklistEntries :many
SELECT id, blocklist_id, pattern, description, created_at, updated_at
FROM dns_blocklist_entries
WHERE blocklist_id = $3
ORDER BY pattern
LIMIT $1 OFFSET $2;

-- name: CountDnsBlocklistEntries :one
SELECT count(*)::bigint FROM dns_blocklist_entries
WHERE blocklist_id = $1;

-- ===== DNS views =====
-- name: ListDnsViews :many
SELECT id, name, fabric_id, match_cidrs, priority, description, created_at, updated_at
FROM dns_views
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]))
ORDER BY priority, name
LIMIT $1 OFFSET $2;

-- name: CountDnsViews :one
SELECT count(*)::bigint FROM dns_views
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]));

-- ===== DNS health checks =====
-- name: ListDnsHealthChecks :many
SELECT id, name, fabric_id, host(target_ip) AS target_ip,
       protocol::text AS protocol, port, path,
       interval_seconds, timeout_seconds, enabled,
       status::text AS status, last_checked_at, last_error,
       created_at, updated_at
FROM dns_health_checks
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]))
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountDnsHealthChecks :one
SELECT count(*)::bigint FROM dns_health_checks
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]));

-- ===== BGP peers (dns-managed) =====
-- name: ListBgpPeers :many
SELECT id, name, site_id, local_asn_id, peer_asn_id,
       host(peer_ip) AS peer_ip, peer_description,
       tcp_ao_key_chain_id, enabled, created_at, updated_at
FROM bgp_peers
WHERE (sqlc.narg(site_id)::uuid IS NULL OR site_id = sqlc.narg(site_id))
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountBgpPeers :one
SELECT count(*)::bigint FROM bgp_peers
WHERE (sqlc.narg(site_id)::uuid IS NULL OR site_id = sqlc.narg(site_id));

-- ===== Anycast BGP bindings =====
-- 2-hop scope: binding → dns_server → fabric. Matches the create/delete
-- ABAC anchor (PR 58) that enforces on the dns_server side.
-- name: ListAnycastBindings :many
SELECT id, dns_server_id, bgp_peer_id, created_at, updated_at
FROM anycast_bgp_bindings
WHERE (sqlc.narg(dns_server_id)::uuid IS NULL OR dns_server_id = sqlc.narg(dns_server_id))
  AND (sqlc.narg(bgp_peer_id)::uuid   IS NULL OR bgp_peer_id   = sqlc.narg(bgp_peer_id))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR dns_server_id IN (
        SELECT id FROM dns_servers WHERE fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[])
      ))
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;

-- name: CountAnycastBindings :one
SELECT count(*)::bigint FROM anycast_bgp_bindings
WHERE (sqlc.narg(dns_server_id)::uuid IS NULL OR dns_server_id = sqlc.narg(dns_server_id))
  AND (sqlc.narg(bgp_peer_id)::uuid   IS NULL OR bgp_peer_id   = sqlc.narg(bgp_peer_id))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR dns_server_id IN (
        SELECT id FROM dns_servers WHERE fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[])
      ));
