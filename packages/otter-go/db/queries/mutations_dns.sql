-- ===== DNS Zones =====
-- name: CreateDnsZone :one
INSERT INTO dns_zones (id, name, kind, fabric_id, site_id, description,
                       soa_mname, soa_rname, soa_refresh, soa_retry, soa_expire, soa_minimum,
                       default_ttl, signed, zsk_rotation_days, nsec3_iterations, nsec3_opt_out,
                       publish_cds, frozen, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2::dns_zone_kind, $3, $4, $5,
        $6, $7, $8, $9, $10, $11,
        $12, FALSE, $13, 0, FALSE,
        $14, FALSE, NOW(), NOW())
RETURNING id, name, kind::text AS kind, fabric_id, site_id, description,
          soa_mname, soa_rname, soa_refresh, soa_retry, soa_expire, soa_minimum,
          default_ttl, signed, zsk_rotation_days, nsec3_salt, nsec3_iterations,
          nsec3_opt_out, publish_cds, frozen, created_at, updated_at;

-- name: UpdateDnsZone :one
UPDATE dns_zones
SET description       = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    zsk_rotation_days = COALESCE(sqlc.narg(zsk_rotation_days)::int, zsk_rotation_days),
    soa_mname  = COALESCE(sqlc.narg(soa_mname)::text, soa_mname),
    soa_rname  = COALESCE(sqlc.narg(soa_rname)::text, soa_rname),
    soa_refresh = COALESCE(sqlc.narg(soa_refresh)::int, soa_refresh),
    soa_retry   = COALESCE(sqlc.narg(soa_retry)::int, soa_retry),
    soa_expire  = COALESCE(sqlc.narg(soa_expire)::int, soa_expire),
    soa_minimum = COALESCE(sqlc.narg(soa_minimum)::int, soa_minimum),
    default_ttl = COALESCE(sqlc.narg(default_ttl)::int, default_ttl),
    publish_cds = COALESCE(sqlc.narg(publish_cds)::bool, publish_cds),
    updated_at  = NOW()
WHERE id = $1
RETURNING id, name, kind::text AS kind, fabric_id, site_id, description,
          soa_mname, soa_rname, soa_refresh, soa_retry, soa_expire, soa_minimum,
          default_ttl, signed, zsk_rotation_days, nsec3_salt, nsec3_iterations,
          nsec3_opt_out, publish_cds, frozen, created_at, updated_at;

-- name: DeleteDnsZone :exec
DELETE FROM dns_zones WHERE id = $1;

-- name: SetDnsZoneFrozen :one
-- PR 70 — flip the maintenance-window write lock. Idempotent;
-- mutation handlers refuse on frozen=true (enforced in API
-- layer). updated_at bumps on every call so the bundle etag
-- changes even when nothing else does.
UPDATE dns_zones
SET frozen = $2, updated_at = NOW()
WHERE id = $1
RETURNING id, name, kind::text AS kind, fabric_id, site_id, description,
          soa_mname, soa_rname, soa_refresh, soa_retry, soa_expire, soa_minimum,
          default_ttl, signed, zsk_rotation_days, nsec3_salt, nsec3_iterations,
          nsec3_opt_out, publish_cds, frozen, created_at, updated_at;

-- name: ListAllRecordsInZone :many
-- PR 71 — preview reads every record in the zone (unpaginated)
-- and sorts by (name, type) for diffable output. Returns just the
-- columns the renderer needs.
SELECT id, name, type::text AS type, ttl, data
FROM dns_records
WHERE zone_id = $1
ORDER BY name, type;

-- name: SetDnsZoneNsec3 :one
-- PR 70 — set NSEC3 params on a signed zone. Salt is hex string
-- (validated by handler) or NULL to mean "renderer picks a fresh
-- random salt at sign time." API refuses on unsigned zones.
UPDATE dns_zones
SET nsec3_salt = $2,
    nsec3_iterations = $3,
    nsec3_opt_out = $4,
    updated_at = NOW()
WHERE id = $1
RETURNING id, name, kind::text AS kind, fabric_id, site_id, description,
          soa_mname, soa_rname, soa_refresh, soa_retry, soa_expire, soa_minimum,
          default_ttl, signed, zsk_rotation_days, nsec3_salt, nsec3_iterations,
          nsec3_opt_out, publish_cds, frozen, created_at, updated_at;

-- ===== DNS Records =====
-- name: CreateDnsRecord :one
INSERT INTO dns_records (id, zone_id, name, type, ttl, data, source,
                         view_id, health_check_id, description, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3::dns_record_type, $4, $5::jsonb, 'manual'::dns_record_source,
        $6, $7, $8, NOW(), NOW())
RETURNING id, zone_id, name, type::text AS type, ttl, data, source::text AS source,
          ipam_address_id, NULL::uuid AS view_id, NULL::uuid AS health_check_id,
          description, created_at, updated_at;

-- name: UpdateDnsRecord :one
UPDATE dns_records
SET name        = COALESCE(sqlc.narg(name)::text, name),
    ttl         = CASE WHEN sqlc.arg(ttl_set)::bool         THEN sqlc.narg(ttl)::int    ELSE ttl END,
    data        = COALESCE(sqlc.narg(data)::jsonb, data),
    view_id     = CASE WHEN sqlc.arg(view_set)::bool        THEN sqlc.narg(view_id)::uuid       ELSE view_id END,
    health_check_id = CASE WHEN sqlc.arg(hc_set)::bool      THEN sqlc.narg(health_check_id)::uuid ELSE health_check_id END,
    description = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    updated_at  = NOW()
WHERE id = $1
RETURNING id, zone_id, name, type::text AS type, ttl, data, source::text AS source,
          ipam_address_id, view_id, health_check_id,
          description, created_at, updated_at;

-- name: DeleteDnsRecord :exec
DELETE FROM dns_records WHERE id = $1;

-- ===== DNS Servers =====
-- name: CreateDnsServerRow :one
INSERT INTO dns_servers (id, name, site_id, fabric_id, role, unicast_ip, enabled, anycast_group_id, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, $4::dns_server_role, $5::inet, $6, $7, NOW(), NOW())
RETURNING id, name, site_id, fabric_id, role::text AS role,
          host(unicast_ip) AS unicast_ip, enabled,
          last_render_at, last_render_status, last_render_error, last_render_etag,
          coredns_version, anycast_group_id, created_at, updated_at;

-- name: UpdateDnsServerRow :one
UPDATE dns_servers
SET name             = COALESCE(sqlc.narg(name)::text, name),
    enabled          = COALESCE(sqlc.narg(enabled)::bool, enabled),
    unicast_ip       = COALESCE(sqlc.narg(unicast_ip)::inet, unicast_ip),
    anycast_group_id = CASE WHEN sqlc.arg(ag_set)::bool THEN sqlc.narg(anycast_group_id)::uuid ELSE anycast_group_id END,
    updated_at       = NOW()
WHERE id = $1
RETURNING id, name, site_id, fabric_id, role::text AS role,
          host(unicast_ip) AS unicast_ip, enabled,
          last_render_at, last_render_status, last_render_error, last_render_etag,
          coredns_version, anycast_group_id, created_at, updated_at;

-- name: DeleteDnsServerRow :exec
DELETE FROM dns_servers WHERE id = $1;

-- ===== Anycast groups =====
-- name: CreateAnycastGroup :one
INSERT INTO anycast_groups (id, name, fabric_id, service, anycast_ipv4, anycast_ipv6, description, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3::anycast_service, $4::inet, $5::inet, $6, NOW(), NOW())
RETURNING id, name, fabric_id, service::text AS service,
          host(anycast_ipv4) AS anycast_ipv4, host(anycast_ipv6) AS anycast_ipv6,
          description, created_at, updated_at;

-- name: UpdateAnycastGroup :one
UPDATE anycast_groups
SET name        = COALESCE(sqlc.narg(name)::text, name),
    anycast_ipv4 = CASE WHEN sqlc.arg(v4_set)::bool THEN sqlc.narg(anycast_ipv4)::inet ELSE anycast_ipv4 END,
    anycast_ipv6 = CASE WHEN sqlc.arg(v6_set)::bool THEN sqlc.narg(anycast_ipv6)::inet ELSE anycast_ipv6 END,
    description = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    updated_at  = NOW()
WHERE id = $1
RETURNING id, name, fabric_id, service::text AS service,
          host(anycast_ipv4) AS anycast_ipv4, host(anycast_ipv6) AS anycast_ipv6,
          description, created_at, updated_at;

-- name: DeleteAnycastGroup :exec
DELETE FROM anycast_groups WHERE id = $1;

-- ===== DNS Forwarders =====
-- name: CreateDnsForwarder :one
INSERT INTO dns_forwarders (id, name, fabric_id, zone_pattern, upstreams, description, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, $4::jsonb, $5, NOW(), NOW())
RETURNING id, name, fabric_id, zone_pattern, upstreams, description, created_at, updated_at;

-- name: UpdateDnsForwarder :one
UPDATE dns_forwarders
SET name         = COALESCE(sqlc.narg(name)::text, name),
    zone_pattern = COALESCE(sqlc.narg(zone_pattern)::text, zone_pattern),
    upstreams    = COALESCE(sqlc.narg(upstreams)::jsonb, upstreams),
    description  = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    updated_at   = NOW()
WHERE id = $1
RETURNING id, name, fabric_id, zone_pattern, upstreams, description, created_at, updated_at;

-- name: DeleteDnsForwarder :exec
DELETE FROM dns_forwarders WHERE id = $1;

-- ===== DNS Catalog Zones =====
-- name: CreateDnsCatalogZone :one
INSERT INTO dns_catalog_zones (id, fabric_id, name, enabled, signed, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, FALSE, NOW(), NOW())
RETURNING id, fabric_id, name, enabled, signed, created_at, updated_at;

-- name: UpdateDnsCatalogZone :one
UPDATE dns_catalog_zones
SET name    = COALESCE(sqlc.narg(name)::text, name),
    enabled = COALESCE(sqlc.narg(enabled)::bool, enabled),
    updated_at = NOW()
WHERE id = $1
RETURNING id, fabric_id, name, enabled, signed, created_at, updated_at;

-- name: DeleteDnsCatalogZone :exec
DELETE FROM dns_catalog_zones WHERE id = $1;

-- ===== DNS Blocklists =====
-- name: CreateDnsBlocklist :one
INSERT INTO dns_blocklists (id, name, fabric_id, action, sink_ipv4, sink_ipv6, enabled, description, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3::dns_blocklist_action, $4::inet, $5::inet, $6, $7, NOW(), NOW())
RETURNING id, name, fabric_id, action::text AS action,
          host(sink_ipv4) AS sink_ipv4, host(sink_ipv6) AS sink_ipv6,
          enabled, description, created_at, updated_at;

-- name: UpdateDnsBlocklist :one
UPDATE dns_blocklists
SET name      = COALESCE(sqlc.narg(name)::text, name),
    action    = COALESCE(sqlc.narg(action)::dns_blocklist_action, action),
    sink_ipv4 = CASE WHEN sqlc.arg(v4_set)::bool THEN sqlc.narg(sink_ipv4)::inet ELSE sink_ipv4 END,
    sink_ipv6 = CASE WHEN sqlc.arg(v6_set)::bool THEN sqlc.narg(sink_ipv6)::inet ELSE sink_ipv6 END,
    enabled   = COALESCE(sqlc.narg(enabled)::bool, enabled),
    description = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    updated_at = NOW()
WHERE id = $1
RETURNING id, name, fabric_id, action::text AS action,
          host(sink_ipv4) AS sink_ipv4, host(sink_ipv6) AS sink_ipv6,
          enabled, description, created_at, updated_at;

-- name: DeleteDnsBlocklist :exec
DELETE FROM dns_blocklists WHERE id = $1;

-- ===== DNS Blocklist Entries =====
-- name: CreateDnsBlocklistEntry :one
INSERT INTO dns_blocklist_entries (id, blocklist_id, pattern, description, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, NOW(), NOW())
RETURNING id, blocklist_id, pattern, description, created_at, updated_at;

-- name: DeleteDnsBlocklistEntry :exec
DELETE FROM dns_blocklist_entries WHERE id = $1;

-- ===== DNS Views =====
-- name: CreateDnsView :one
INSERT INTO dns_views (id, name, fabric_id, match_cidrs, priority, description, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3::jsonb, $4, $5, NOW(), NOW())
RETURNING id, name, fabric_id, match_cidrs, priority, description, created_at, updated_at;

-- name: UpdateDnsView :one
UPDATE dns_views
SET name        = COALESCE(sqlc.narg(name)::text, name),
    match_cidrs = COALESCE(sqlc.narg(match_cidrs)::jsonb, match_cidrs),
    priority    = COALESCE(sqlc.narg(priority)::int, priority),
    description = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    updated_at  = NOW()
WHERE id = $1
RETURNING id, name, fabric_id, match_cidrs, priority, description, created_at, updated_at;

-- name: DeleteDnsView :exec
DELETE FROM dns_views WHERE id = $1;

-- ===== DNS Health Checks =====
-- name: CreateDnsHealthCheck :one
INSERT INTO dns_health_checks (id, name, fabric_id, target_ip, protocol, port, path,
                               interval_seconds, timeout_seconds, enabled, status,
                               created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3::inet, $4::dns_health_check_protocol, $5, $6,
        $7, $8, $9, 'unknown'::dns_health_check_status, NOW(), NOW())
RETURNING id, name, fabric_id, host(target_ip) AS target_ip,
          protocol::text AS protocol, port, path,
          interval_seconds, timeout_seconds, enabled,
          status::text AS status, last_checked_at, last_error,
          created_at, updated_at;

-- name: UpdateDnsHealthCheck :one
UPDATE dns_health_checks
SET name             = COALESCE(sqlc.narg(name)::text, name),
    target_ip        = COALESCE(sqlc.narg(target_ip)::inet, target_ip),
    protocol         = COALESCE(sqlc.narg(protocol)::dns_health_check_protocol, protocol),
    port             = CASE WHEN sqlc.arg(port_set)::bool THEN sqlc.narg(port)::int ELSE port END,
    path             = COALESCE(sqlc.narg(path)::text, path),
    interval_seconds = COALESCE(sqlc.narg(interval_seconds)::int, interval_seconds),
    timeout_seconds  = COALESCE(sqlc.narg(timeout_seconds)::int, timeout_seconds),
    enabled          = COALESCE(sqlc.narg(enabled)::bool, enabled),
    updated_at       = NOW()
WHERE id = $1
RETURNING id, name, fabric_id, host(target_ip) AS target_ip,
          protocol::text AS protocol, port, path,
          interval_seconds, timeout_seconds, enabled,
          status::text AS status, last_checked_at, last_error,
          created_at, updated_at;

-- name: DeleteDnsHealthCheck :exec
DELETE FROM dns_health_checks WHERE id = $1;

-- name: SetDnsServerRenderStatus :exec
-- PR 73 — collector callback after every render attempt. Mirrors
-- DhcpServer.last_sync_* shape on DnsServer. coredns_version is
-- optional: if NULL it's left unchanged (existing value sticks),
-- if non-NULL it's recorded.
UPDATE dns_servers
SET last_render_at = NOW(),
    last_render_status = $2,
    last_render_error = $3,
    last_render_etag = $4,
    coredns_version = COALESCE($5, coredns_version)
WHERE id = $1;

-- ===== DnsServerMetricsSample (PR 78) =====
-- Per-interval delta sample posted by the dns-collector on every
-- scrape. Stored as raw rows; aggregation happens in the dashboard
-- handler with PostgreSQL window functions (not in this migration).

-- name: CreateDnsServerMetricsSample :one
INSERT INTO dns_server_metrics_samples (
    id, server_id, observed_at, interval_seconds,
    queries, nxdomain, servfail, noerror,
    p50_ms, p95_ms, top_names, created_at, updated_at
)
VALUES (
    gen_random_uuid(), $1, COALESCE($2::timestamptz, NOW()), $3,
    $4, $5, $6, $7, $8, $9, $10::jsonb, NOW(), NOW()
)
RETURNING id, server_id, observed_at, interval_seconds,
          queries, nxdomain, servfail, noerror,
          p50_ms, p95_ms, top_names;

-- name: ListDnsServerMetricsSamples :many
-- Recent samples for one server, oldest-first so the UI can chart
-- them directly. Caller passes the cutoff so the time-arithmetic
-- happens in Go (cleaner test surface than building intervals
-- with `INTERVAL '$2 minutes'`).
SELECT id, server_id, observed_at, interval_seconds,
       queries, nxdomain, servfail, noerror,
       p50_ms, p95_ms, top_names
FROM dns_server_metrics_samples
WHERE server_id = $1
  AND observed_at >= $2
ORDER BY observed_at ASC;

-- name: SetDnsHealthCheckResult :exec
-- PR 72 — collector callback after running one probe. Status,
-- last_checked_at, last_error are the only mutable fields. Audit
-- is intentionally skipped at the handler level — every 30s
-- probe would flood the audit log; the central worker also writes
-- this row on its fallback cycles.
UPDATE dns_health_checks
SET status = $2::dns_health_check_status,
    last_checked_at = NOW(),
    last_error = $3
WHERE id = $1;

-- ===== BGP Peers (dns-managed) =====
-- name: CreateBgpPeer :one
INSERT INTO bgp_peers (id, name, site_id, local_asn_id, peer_asn_id, peer_ip,
                       peer_description, tcp_ao_key_chain_id, enabled, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5::inet, $6, $7, $8, NOW(), NOW())
RETURNING id, name, site_id, local_asn_id, peer_asn_id,
          host(peer_ip) AS peer_ip, peer_description, tcp_ao_key_chain_id, enabled,
          created_at, updated_at;

-- name: UpdateBgpPeer :one
UPDATE bgp_peers
SET name              = COALESCE(sqlc.narg(name)::text, name),
    local_asn_id      = COALESCE(sqlc.narg(local_asn_id)::uuid, local_asn_id),
    peer_asn_id       = COALESCE(sqlc.narg(peer_asn_id)::uuid,  peer_asn_id),
    peer_ip           = COALESCE(sqlc.narg(peer_ip)::inet, peer_ip),
    peer_description  = CASE WHEN sqlc.arg(desc_set)::bool THEN sqlc.narg(peer_description)::text ELSE peer_description END,
    tcp_ao_key_chain_id = CASE WHEN sqlc.arg(ao_set)::bool THEN sqlc.narg(tcp_ao_key_chain_id)::uuid ELSE tcp_ao_key_chain_id END,
    enabled           = COALESCE(sqlc.narg(enabled)::bool, enabled),
    updated_at        = NOW()
WHERE id = $1
RETURNING id, name, site_id, local_asn_id, peer_asn_id,
          host(peer_ip) AS peer_ip, peer_description, tcp_ao_key_chain_id, enabled,
          created_at, updated_at;

-- name: DeleteBgpPeer :exec
DELETE FROM bgp_peers WHERE id = $1;

-- ===== Anycast BGP Bindings =====
-- name: CreateAnycastBinding :one
INSERT INTO anycast_bgp_bindings (id, dns_server_id, bgp_peer_id, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, NOW(), NOW())
RETURNING id, dns_server_id, bgp_peer_id, created_at, updated_at;

-- name: DeleteAnycastBinding :exec
DELETE FROM anycast_bgp_bindings WHERE id = $1;
