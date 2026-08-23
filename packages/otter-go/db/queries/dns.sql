-- ===== DNS Zones =====
-- scope_fabric_ids is the PR 60 ABAC LIST filter: NULL = no restriction
-- (global caller); a non-NULL slice restricts the result to zones in the
-- named fabrics. See auth.ScopedFabricFilter.
-- name: ListDnsZones :many
SELECT *
FROM dns_zones
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(site_id)::uuid   IS NULL OR site_id   = sqlc.narg(site_id))
  AND (sqlc.narg(kind)::text      IS NULL OR kind::text = sqlc.narg(kind))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]))
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountDnsZones :one
SELECT count(*)::bigint
FROM dns_zones
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(site_id)::uuid   IS NULL OR site_id   = sqlc.narg(site_id))
  AND (sqlc.narg(kind)::text      IS NULL OR kind::text = sqlc.narg(kind))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]));

-- name: GetDnsZone :one
SELECT *
FROM dns_zones
WHERE id = $1;

-- ===== DNS Records =====
-- 2-hop scope: record → zone → fabric. Subquery on dns_zones keeps the
-- planner happy when scope_fabric_ids is NULL.
-- name: ListDnsRecordsByZoneIDs :many
-- Bulk-fetch every record across a set of zones. The bundle
-- assembler uses this to load records for an entire fabric in a
-- single round-trip rather than per-zone. Ordered (zone_id, name,
-- type) so per-zone slicing in Go is a single contiguous scan.
SELECT id, zone_id, name, type::text AS type, ttl, data,
       source::text AS source, ipam_address_id,
       health_check_id, view_id,
       created_at, updated_at
FROM dns_records
WHERE zone_id = ANY(sqlc.arg(zone_ids)::uuid[])
ORDER BY zone_id, name, type;

-- name: ListDnsViewsByFabric :many
-- Split-horizon: every view bound to a fabric, ordered by priority
-- (narrowest first wins per CoreDNS view-plugin semantics). The
-- bundle assembler iterates this list in order when emitting view-
-- scoped server blocks.
SELECT *
FROM dns_views
WHERE fabric_id = $1
ORDER BY priority ASC, name ASC;

-- name: ListApexZoneNamesByFabric :many
-- Recursive bundle: every apex zone bound to this fabric. The
-- recursive Corefile emits a stub-forward per apex pointing at the
-- local auth pod.
SELECT name FROM dns_zones
WHERE fabric_id = $1
  AND kind = 'apex'::dns_zone_kind;

-- name: GetSameSiteAuthUnicastIP :one
-- For a recursive server: find the auth server at the same site so
-- the recursive Corefile can stub-forward the fabric apex back to it.
-- Returns the bare IP (CIDR suffix stripped via host()). At-most-one
-- row per site is expected; LIMIT 1 keeps the query well-defined
-- under the rare misconfig of multiple auth rows.
SELECT host(unicast_ip) FROM dns_servers
WHERE site_id = $1
  AND role = 'auth'::dns_server_role
LIMIT 1;

-- name: ListDnsForwardersForBundle :many
-- Conditional forwarders configured for this fabric. Empty
-- upstreams are filtered out by the renderer (parity with Python).
SELECT zone_pattern, upstreams::jsonb
FROM dns_forwarders
WHERE fabric_id = $1;

-- name: ListEnabledBlocklistsWithPatternsByFabric :many
-- Enabled blocklists for this fabric joined with their entries as
-- a sorted JSONB array. One query instead of N+1 — the recursive
-- bundle renderer wants {action, patterns, sink_ipv4, sink_ipv6}
-- per blocklist.
SELECT
    bl.id,
    bl.action::text AS action,
    CASE WHEN bl.sink_ipv4 IS NULL THEN NULL ELSE host(bl.sink_ipv4) END AS sink_ipv4,
    CASE WHEN bl.sink_ipv6 IS NULL THEN NULL ELSE host(bl.sink_ipv6) END AS sink_ipv6,
    COALESCE(
      (SELECT jsonb_agg(e.pattern ORDER BY e.pattern)
       FROM dns_blocklist_entries e
       WHERE e.blocklist_id = bl.id),
      '[]'::jsonb
    )::jsonb AS patterns_json
FROM dns_blocklists bl
WHERE bl.fabric_id = $1
  AND bl.enabled = true;

-- name: GetFabricForRecursiveBundle :one
-- Slim projection: the fields the recursive bundle assembler reads
-- from the fabric. Wider Fabric struct queries elsewhere skip the
-- dns_allow_networks column today; pulling just this subset keeps
-- the recursive path well-defined without churning the existing
-- Fabric struct.
SELECT id,
       recursive_engine,
       dns_recursive_upstreams::jsonb,
       dns_deny_networks::jsonb,
       dns_allow_networks::jsonb
FROM fabrics
WHERE id = $1;

-- name: GetEnabledDnsCatalogZoneByFabric :one
-- One row max — uq_dns_catalog_zone_fabric guarantees a fabric
-- can have at most one catalog. Returns no-rows when the catalog
-- is missing or disabled; bundle assembler skips catalog emission
-- on either case.
SELECT *
FROM dns_catalog_zones
WHERE fabric_id = $1 AND enabled = true;

-- name: ListEnabledAuthDnsServersByFabric :many
-- For the RFC 9432 §4.2.3 catalog primaries property records —
-- consumers (BIND 9.20+) use unicast_ip to AXFR each member zone.
SELECT id, fabric_id, name, role::text AS role, unicast_ip,
       enabled, created_at, updated_at
FROM dns_servers
WHERE fabric_id = $1
  AND role = 'auth'::dns_server_role
  AND enabled = true;

-- name: ListDnsKeysByZoneIDs :many
-- Bulk-fetch every DnsKey across a set of zones. Bundle assembler
-- uses this for both DNSSEC key-file emission and CDNSKEY/CDS
-- appendix lines. Ordered (zone_id, role, key_tag) so per-zone
-- slicing is contiguous.
SELECT *
FROM dns_keys
WHERE zone_id = ANY(sqlc.arg(zone_ids)::uuid[])
ORDER BY zone_id, role, key_tag;

-- name: ListUnhealthyEnabledHealthChecksByFabric :many
-- IDs only — the bundle assembler uses the set to skip records
-- whose health_check_id is unhealthy. The renderer's `unhealthy`
-- input is a set[uuid], so we return the raw IDs.
SELECT id FROM dns_health_checks
WHERE fabric_id = $1
  AND status = 'unhealthy'::dns_health_check_status
  AND enabled = true;

-- name: ListDnsZonesByFabric :many
-- Every non-frozen zone in a fabric, for the auth bundle. Excludes
-- frozen zones (operators freeze a zone to take it off the air
-- without deleting it).
SELECT *
FROM dns_zones
WHERE fabric_id = $1
  AND frozen = false
ORDER BY name;

-- name: ListDnsRecords :many
SELECT *
FROM dns_records
WHERE (sqlc.narg(zone_id)::uuid IS NULL OR zone_id = sqlc.narg(zone_id))
  AND (sqlc.narg(type)::text    IS NULL OR type::text   = sqlc.narg(type))
  AND (sqlc.narg(source)::text  IS NULL OR source::text = sqlc.narg(source))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR zone_id IN (
        SELECT id FROM dns_zones WHERE fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[])
      ))
ORDER BY zone_id, name, type
LIMIT $1 OFFSET $2;

-- name: CountDnsRecords :one
SELECT count(*)::bigint
FROM dns_records
WHERE (sqlc.narg(zone_id)::uuid IS NULL OR zone_id = sqlc.narg(zone_id))
  AND (sqlc.narg(type)::text    IS NULL OR type::text   = sqlc.narg(type))
  AND (sqlc.narg(source)::text  IS NULL OR source::text = sqlc.narg(source))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR zone_id IN (
        SELECT id FROM dns_zones WHERE fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[])
      ));
