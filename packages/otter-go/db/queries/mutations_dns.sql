-- ===== DNS Zones =====
-- name: CreateDnsZone :one
INSERT INTO dns_zones (id, name, kind, fabric_id, site_id, description,
                       soa_mname, soa_rname, soa_refresh, soa_retry, soa_expire, soa_minimum,
                       default_ttl, signed, zsk_rotation_days, nsec3_iterations, nsec3_opt_out,
                       publish_cds, frozen, created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(name), sqlc.arg(kind)::dns_zone_kind, sqlc.arg(fabric_id), sqlc.arg(site_id), sqlc.arg(description),
        sqlc.arg(soa_mname), sqlc.arg(soa_rname), sqlc.arg(soa_refresh), sqlc.arg(soa_retry), sqlc.arg(soa_expire), sqlc.arg(soa_minimum),
        sqlc.arg(default_ttl), FALSE, sqlc.arg(zsk_rotation_days), 0, FALSE,
        sqlc.arg(publish_cds), FALSE, NOW(), NOW())
RETURNING *;

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
RETURNING *;

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
RETURNING *;

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
SET nsec3_salt = sqlc.narg(salt)::text,
    nsec3_iterations = sqlc.arg(iterations),
    nsec3_opt_out = sqlc.arg(opt_out),
    updated_at = NOW()
WHERE id = sqlc.arg(id)
RETURNING *;

-- ===== DNS Records =====
-- name: CreateDnsRecord :one
INSERT INTO dns_records (id, zone_id, name, type, ttl, data, source,
                         view_id, health_check_id, description, created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(zone_id), sqlc.arg(name), sqlc.arg(type)::dns_record_type, sqlc.narg(ttl), sqlc.arg(data)::jsonb, 'manual'::dns_record_source,
        sqlc.narg(view_id), sqlc.narg(health_check_id), sqlc.narg(description), NOW(), NOW())
RETURNING *;

-- name: UpdateDnsRecord :one
UPDATE dns_records
SET name        = COALESCE(sqlc.narg(name)::text, name),
    ttl         = CASE WHEN sqlc.arg(ttl_set)::bool         THEN sqlc.narg(ttl)::int    ELSE ttl END,
    -- ::json casts here and below, not ::jsonb: these columns are json
    -- and COALESCE cannot implicitly unify jsonb with json — ::jsonb
    -- fails at plan time.
    data        = COALESCE(sqlc.narg(data)::json, data),
    view_id     = CASE WHEN sqlc.arg(view_set)::bool        THEN sqlc.narg(view_id)::uuid       ELSE view_id END,
    health_check_id = CASE WHEN sqlc.arg(hc_set)::bool      THEN sqlc.narg(health_check_id)::uuid ELSE health_check_id END,
    description = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    updated_at  = NOW()
WHERE id = $1
RETURNING *;

-- name: DeleteDnsRecord :exec
DELETE FROM dns_records WHERE id = $1;

-- ===== DNS Servers =====
-- name: CreateDnsServerRow :one
INSERT INTO dns_servers (id, name, site_id, fabric_id, role, unicast_ip, enabled, anycast_group_id, created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(name), sqlc.arg(site_id), sqlc.arg(fabric_id), sqlc.arg(role)::dns_server_role, sqlc.arg(unicast_ip)::inet, sqlc.arg(enabled), sqlc.narg(anycast_group_id), NOW(), NOW())
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
VALUES (gen_random_uuid(), sqlc.arg(name), sqlc.arg(fabric_id), sqlc.arg(service)::anycast_service, sqlc.narg(anycast_ipv4)::inet, sqlc.narg(anycast_ipv6)::inet, sqlc.narg(description), NOW(), NOW())
RETURNING id, name, fabric_id, service::text AS service,
          CASE WHEN anycast_ipv4 IS NULL THEN NULL ELSE host(anycast_ipv4) END AS anycast_ipv4, CASE WHEN anycast_ipv6 IS NULL THEN NULL ELSE host(anycast_ipv6) END AS anycast_ipv6,
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
          CASE WHEN anycast_ipv4 IS NULL THEN NULL ELSE host(anycast_ipv4) END AS anycast_ipv4, CASE WHEN anycast_ipv6 IS NULL THEN NULL ELSE host(anycast_ipv6) END AS anycast_ipv6,
          description, created_at, updated_at;

-- name: DeleteAnycastGroup :exec
DELETE FROM anycast_groups WHERE id = $1;

-- ===== DNS Forwarders =====
-- name: CreateDnsForwarder :one
INSERT INTO dns_forwarders (id, name, fabric_id, zone_pattern, upstreams, description, created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(name), sqlc.arg(fabric_id), sqlc.arg(zone_pattern), sqlc.arg(upstreams)::jsonb, sqlc.narg(description), NOW(), NOW())
RETURNING *;

-- name: UpdateDnsForwarder :one
UPDATE dns_forwarders
SET name         = COALESCE(sqlc.narg(name)::text, name),
    zone_pattern = COALESCE(sqlc.narg(zone_pattern)::text, zone_pattern),
    upstreams    = COALESCE(sqlc.narg(upstreams)::json, upstreams),
    description  = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    updated_at   = NOW()
WHERE id = $1
RETURNING *;

-- name: DeleteDnsForwarder :exec
DELETE FROM dns_forwarders WHERE id = $1;

-- ===== DNS Catalog Zones =====
-- name: CreateDnsCatalogZone :one
INSERT INTO dns_catalog_zones (id, fabric_id, name, enabled, signed, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, FALSE, NOW(), NOW())
RETURNING *;

-- name: UpdateDnsCatalogZone :one
UPDATE dns_catalog_zones
SET name    = COALESCE(sqlc.narg(name)::text, name),
    enabled = COALESCE(sqlc.narg(enabled)::bool, enabled),
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: DeleteDnsCatalogZone :exec
DELETE FROM dns_catalog_zones WHERE id = $1;

-- ===== DNS Blocklists =====
-- name: CreateDnsBlocklist :one
INSERT INTO dns_blocklists (id, name, fabric_id, action, sink_ipv4, sink_ipv6, enabled, description, created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(name), sqlc.arg(fabric_id), sqlc.arg(action)::dns_blocklist_action, sqlc.narg(sink_ipv4)::inet, sqlc.narg(sink_ipv6)::inet, sqlc.arg(enabled), sqlc.narg(description), NOW(), NOW())
RETURNING id, name, fabric_id, action::text AS action,
          CASE WHEN sink_ipv4 IS NULL THEN NULL ELSE host(sink_ipv4) END AS sink_ipv4, CASE WHEN sink_ipv6 IS NULL THEN NULL ELSE host(sink_ipv6) END AS sink_ipv6,
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
          CASE WHEN sink_ipv4 IS NULL THEN NULL ELSE host(sink_ipv4) END AS sink_ipv4, CASE WHEN sink_ipv6 IS NULL THEN NULL ELSE host(sink_ipv6) END AS sink_ipv6,
          enabled, description, created_at, updated_at;

-- name: DeleteDnsBlocklist :exec
DELETE FROM dns_blocklists WHERE id = $1;

-- ===== DNS Blocklist Entries =====
-- name: CreateDnsBlocklistEntry :one
INSERT INTO dns_blocklist_entries (id, blocklist_id, pattern, description, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, NOW(), NOW())
RETURNING *;

-- name: DeleteDnsBlocklistEntry :exec
DELETE FROM dns_blocklist_entries WHERE id = $1;

-- ===== DNS Views =====
-- name: CreateDnsView :one
INSERT INTO dns_views (id, name, fabric_id, match_cidrs, priority, description, created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(name), sqlc.arg(fabric_id), sqlc.arg(match_cidrs)::jsonb, sqlc.arg(priority), sqlc.narg(description), NOW(), NOW())
RETURNING *;

-- name: UpdateDnsView :one
UPDATE dns_views
SET name        = COALESCE(sqlc.narg(name)::text, name),
    match_cidrs = COALESCE(sqlc.narg(match_cidrs)::json, match_cidrs),
    priority    = COALESCE(sqlc.narg(priority)::int, priority),
    description = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    updated_at  = NOW()
WHERE id = $1
RETURNING *;

-- name: DeleteDnsView :exec
DELETE FROM dns_views WHERE id = $1;

-- ===== DNS Health Checks =====
-- name: CreateDnsHealthCheck :one
INSERT INTO dns_health_checks (id, name, fabric_id, target_ip, protocol, port, path,
                               interval_seconds, timeout_seconds, enabled, status,
                               created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(name), sqlc.arg(fabric_id), sqlc.arg(target_ip)::inet, sqlc.arg(protocol)::dns_health_check_protocol, sqlc.narg(port), sqlc.arg(path),
        sqlc.arg(interval_seconds), sqlc.arg(timeout_seconds), sqlc.arg(enabled), 'unknown'::dns_health_check_status, NOW(), NOW())
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

-- name: SetDnsServerRenderStatus :execrows
-- PR 73 — collector callback after every render attempt. Mirrors
-- DhcpServer.last_sync_* shape on DnsServer. coredns_version is
-- optional: if NULL it's left unchanged (existing value sticks),
-- if non-NULL it's recorded.
UPDATE dns_servers
SET last_render_at = NOW(),
    last_render_status = sqlc.arg(status)::text,
    last_render_error = sqlc.narg(error)::text,
    last_render_etag = sqlc.narg(etag)::text,
    coredns_version = COALESCE(sqlc.narg(coredns_version)::text, coredns_version)
WHERE id = sqlc.arg(id);

-- ===== Dashboard (PR 84) =====

-- name: ListDnsSamplesInWindow :many
-- All samples from `cutoff` onward, optionally filtered by a set of
-- server_ids when the caller is fabric-scoped.
SELECT *
FROM dns_server_metrics_samples
WHERE observed_at >= sqlc.arg(cutoff)
  AND (sqlc.arg(server_ids)::uuid[] IS NULL OR server_id = ANY(sqlc.arg(server_ids)::uuid[]))
ORDER BY observed_at ASC;

-- name: ListDnsServersForDashboard :many
SELECT id, name, site_id, fabric_id, role::text AS role, host(unicast_ip) AS unicast_ip,
       enabled, last_render_at, last_render_status, last_render_error,
       last_render_etag, coredns_version, anycast_group_id,
       created_at, updated_at
FROM dns_servers
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id));

-- name: ListDnsZonesForDashboard :many
SELECT id, name, kind::text AS kind, fabric_id, site_id, signed,
       nsec3_iterations
FROM dns_zones
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id));

-- name: CountAnycastGroupsForDashboard :one
SELECT count(*)::bigint FROM anycast_groups
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id));

-- ===== sync-from-ipam (PR 83) =====

-- name: ListReverseZonesForSite :many
SELECT *
FROM dns_zones
WHERE kind = 'reverse'::dns_zone_kind
  AND fabric_id = sqlc.arg(fabric_id)
  AND site_id = sqlc.arg(site_id)::uuid;

-- name: ListAllSiteDnsZones :many
-- Used by the dns_sync_from_ipam scheduler job to enumerate every
-- kind=site zone whose IPAM projection should be rebuilt. Unpaginated
-- because the cron processes every site zone in one pass — apex zones
-- are skipped (operator-curated; the per-zone helper returns (0, 0) on
-- non-site kinds too, but filtering here keeps the loop tighter).
SELECT *
FROM dns_zones
WHERE kind = 'site'::dns_zone_kind
ORDER BY id;

-- name: ListSignedZonesWithZskRotation :many
-- Used by the dns_rotate_zsks scheduler job. Only signed zones with a
-- positive rotation policy are candidates; the cron then checks each
-- zone's active ZSK age in code before issuing a rotation. Frozen
-- zones are returned and skipped at the job level so the count
-- ("checked" in the result map) matches Python's notion of "rows we
-- looked at" — Python's select(...).where(...) has no frozen filter
-- either; rotate_zone_key would raise on a frozen zone there too.
SELECT *
FROM dns_zones
WHERE signed = true
  AND zsk_rotation_days > 0
ORDER BY id;

-- name: GetReverseZoneByName :one
SELECT *
FROM dns_zones
WHERE kind = 'reverse'::dns_zone_kind
  AND fabric_id = sqlc.arg(fabric_id) AND site_id = sqlc.arg(site_id)::uuid AND name = sqlc.arg(name);

-- name: CreateReverseZone :one
INSERT INTO dns_zones (
    id, name, kind, fabric_id, site_id,
    soa_mname, soa_rname, soa_refresh, soa_retry, soa_expire, soa_minimum,
    default_ttl, signed, nsec3_iterations, nsec3_opt_out, publish_cds, frozen,
    created_at, updated_at
)
VALUES (
    gen_random_uuid(), sqlc.arg(name), 'reverse'::dns_zone_kind, sqlc.arg(fabric_id), sqlc.arg(site_id)::uuid,
    'ns1', 'hostmaster', 900, 900, 1800, 60,
    60, FALSE, 0, FALSE, TRUE, FALSE,
    NOW(), NOW()
)
RETURNING *;

-- name: ListIPAddressesForSiteWithDnsName :many
-- Joins subnets+ip_addresses by site, filters to rows with dns_name set.
-- The sync projector emits A/AAAA + PTR for each row returned.
SELECT i.id, i.subnet_id, host(i.address) AS address,
       source::text AS source, dns_name
FROM ip_addresses i
JOIN subnets s ON s.id = i.subnet_id
WHERE s.site_id = sqlc.arg(site_id)::uuid
  AND i.dns_name IS NOT NULL;

-- name: DeleteIPAMRecordsInZones :exec
-- Drop every projector-owned record (source=ipam or =ddns) across
-- the named zones. Operator-authored manual rows stay put.
DELETE FROM dns_records
WHERE zone_id = ANY(sqlc.arg(zone_ids)::uuid[])
  AND source IN ('ipam'::dns_record_source, 'ddns'::dns_record_source);

-- name: CountIPAMRecordsInZones :one
-- Pre-count for the response shape (Python returns removed count).
SELECT count(*)::bigint FROM dns_records
WHERE zone_id = ANY(sqlc.arg(zone_ids)::uuid[])
  AND source IN ('ipam'::dns_record_source, 'ddns'::dns_record_source);

-- name: CreateProjectedDnsRecord :one
-- Variant of CreateDnsRecord that takes an explicit source enum
-- value (ipam or ddns) plus the IPAM back-pointer for projector-
-- owned rows. CreateDnsRecord hardcoded source='manual' which
-- doesn't fit the sync path.
INSERT INTO dns_records (
    id, zone_id, name, type, ttl, data, source, ipam_address_id,
    created_at, updated_at
)
VALUES (
    gen_random_uuid(), sqlc.arg(zone_id), sqlc.arg(name), sqlc.arg(type)::dns_record_type, sqlc.narg(ttl), sqlc.arg(data)::jsonb,
    sqlc.arg(source)::dns_record_source, sqlc.narg(ipam_address_id), NOW(), NOW()
)
RETURNING id;

-- ===== Zone import (PR 82) =====

-- name: DeleteManualRecordsInZone :many
-- Zone import replaces existing source=manual rows; IPAM-projected
-- (source=ipam / ddns) are left alone. Returns the deleted rows so
-- the audit metadata can include the count.
DELETE FROM dns_records
WHERE zone_id = $1 AND source = 'manual'::dns_record_source
RETURNING id;

-- name: UpdateDnsZoneSoa :exec
-- Apply imported SOA timers when update_soa=true on the import
-- payload. mname/rname strip the trailing dot + take only the
-- left-most label (matches Python's _ImportPayload behavior).
UPDATE dns_zones
SET soa_mname    = COALESCE(sqlc.narg(soa_mname)::text, soa_mname),
    soa_rname    = COALESCE(sqlc.narg(soa_rname)::text, soa_rname),
    soa_refresh  = COALESCE(sqlc.narg(soa_refresh)::int, soa_refresh),
    soa_retry    = COALESCE(sqlc.narg(soa_retry)::int, soa_retry),
    soa_expire   = COALESCE(sqlc.narg(soa_expire)::int, soa_expire),
    soa_minimum  = COALESCE(sqlc.narg(soa_minimum)::int, soa_minimum),
    default_ttl  = COALESCE(sqlc.narg(default_ttl)::int, default_ttl),
    updated_at   = NOW()
WHERE id = $1;

-- ===== DnsKey writes (PR 80+) =====
-- Key generation/rotation/enable + key delete. Keys are stored
-- plaintext in Postgres for the Go port (Fernet-at-rest is a
-- known gap vs Python — operators concerned about at-rest must
-- use column-level encryption or KMS).

-- name: CreateDnsKey :one
INSERT INTO dns_keys (
    id, zone_id, catalog_id, role, algorithm,
    private_pem, public_key_b64, key_tag,
    active_from, created_at, updated_at
)
VALUES (
    gen_random_uuid(), sqlc.narg(zone_id), sqlc.narg(catalog_id), sqlc.arg(role)::dns_key_role, sqlc.arg(algorithm)::dns_key_algorithm,
    sqlc.arg(private_pem), sqlc.arg(public_key_b64), sqlc.arg(key_tag), NOW(), NOW(), NOW()
)
RETURNING *;

-- name: SetDnsZoneSigned :execrows
-- Flip the signed flag without bumping the SOA serial — caller
-- (rotate-key, sync-from-ipam) bumps updated_at separately when
-- the change should propagate to resolvers.
UPDATE dns_zones SET signed = $2, updated_at = NOW() WHERE id = $1;

-- name: ListActiveDnsKeysForZoneAndRole :many
-- Used by enable-dnssec to find existing keys and by rotate-key to
-- list keys eligible for retirement. retired_at IS NULL filters to
-- the currently-signing set.
SELECT *
FROM dns_keys
WHERE zone_id = sqlc.arg(zone_id)::uuid AND role = sqlc.arg(role)::dns_key_role AND retired_at IS NULL
ORDER BY active_from DESC;

-- name: RetireDnsKey :execrows
-- Marks a key retired without deleting — the renderer still emits
-- the DNSKEY RR until the next rollover so resolvers caching the
-- old key can validate. delete_dnssec_key cleans up later.
UPDATE dns_keys SET retired_at = NOW() WHERE id = $1;

-- name: DeleteDnsKey :execrows
DELETE FROM dns_keys WHERE id = $1;

-- name: RetireAllDnsKeysForZone :execrows
-- disable-dnssec retires every key — the renderer drops DNSKEY/
-- RRSIG output and the zone goes back to unsigned.
UPDATE dns_keys SET retired_at = NOW() WHERE zone_id = sqlc.arg(zone_id)::uuid AND retired_at IS NULL;

-- name: DeleteAllDnsKeysForZone :many
-- Hard-delete every key for a zone. Returns the deleted rows so the
-- audit record can list retired key tags. Used by disable-dnssec.
DELETE FROM dns_keys WHERE zone_id = sqlc.arg(zone_id)::uuid
RETURNING *;

-- name: GetDnsKey :one
SELECT *
FROM dns_keys WHERE id = $1;

-- name: TouchDnsZone :execrows
-- Bump updated_at so the SOA serial moves and downstream resolvers
-- pick up the change on their next refresh.
UPDATE dns_zones SET updated_at = NOW() WHERE id = $1;

-- ===== DnsKey reads (PR 79) =====
-- DNSSEC key list + DS-record derivation are pure reads. The key-
-- generation/rotation surface (POST /enable-dnssec, /rotate-key/{role},
-- DELETE /keys/{id}) stays Python — those need the cryptography
-- crate and live state machines.

-- name: ListDnsKeysByZone :many
-- PR 79 — list every key bound to a zone, ordered KSK first then
-- newest active_from first (matches Python's role ASC + active_from DESC).
SELECT *
FROM dns_keys
WHERE zone_id = sqlc.arg(zone_id)::uuid
ORDER BY role::text ASC, active_from DESC;

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
    gen_random_uuid(), sqlc.arg(server_id), COALESCE(sqlc.narg(observed_at)::timestamptz, NOW()), sqlc.arg(interval_seconds),
    sqlc.arg(queries), sqlc.arg(nxdomain), sqlc.arg(servfail), sqlc.arg(noerror), sqlc.narg(p50_ms), sqlc.narg(p95_ms), sqlc.arg(top_names)::jsonb, NOW(), NOW()
)
RETURNING *;

-- name: ListDnsServerMetricsSamples :many
-- Recent samples for one server, oldest-first so the UI can chart
-- them directly. Caller passes the cutoff so the time-arithmetic
-- happens in Go (cleaner test surface than building intervals
-- with `INTERVAL '$2 minutes'`).
SELECT *
FROM dns_server_metrics_samples
WHERE server_id = sqlc.arg(server_id)
  AND observed_at >= sqlc.arg(cutoff)
ORDER BY observed_at ASC;

-- name: DeleteDnsServerMetricsSamplesOlderThan :execrows
-- Cron-driven retention: the dns_server_metrics_samples table grows
-- unbounded otherwise (every scrape inserts a fresh row). The Go
-- scheduler's dns_purge_metrics job picks the cutoff in code so the
-- retention policy stays a single deployment-config knob.
DELETE FROM dns_server_metrics_samples WHERE observed_at < sqlc.arg(cutoff);

-- name: SetDnsHealthCheckResult :execrows
-- PR 72 — collector callback after running one probe. Status,
-- last_checked_at, last_error are the only mutable fields. Audit
-- is intentionally skipped at the handler level — every 30s
-- probe would flood the audit log; the central worker also writes
-- this row on its fallback cycles.
UPDATE dns_health_checks
SET status = sqlc.arg(status)::dns_health_check_status,
    last_checked_at = NOW(),
    last_error = sqlc.narg(last_error)
WHERE id = sqlc.arg(id);

-- ===== BGP Peers (dns-managed) =====
-- name: CreateBgpPeer :one
INSERT INTO bgp_peers (id, name, site_id, local_asn_id, peer_asn_id, peer_ip,
                       peer_description, tcp_ao_key_chain_id, enabled, created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(name), sqlc.arg(site_id), sqlc.arg(local_asn_id), sqlc.arg(peer_asn_id), sqlc.arg(peer_ip)::inet, sqlc.narg(peer_description), sqlc.narg(tcp_ao_key_chain_id), sqlc.arg(enabled), NOW(), NOW())
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
