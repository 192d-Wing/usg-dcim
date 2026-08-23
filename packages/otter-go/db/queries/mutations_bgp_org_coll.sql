-- ===== BGP ASNs =====
-- name: CreateAsn :one
INSERT INTO bgp_asns (id, asn, name, kind, organization_id, description, created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(asn), sqlc.arg(name), sqlc.arg(kind)::bgp_asn_kind, sqlc.arg(organization_id), sqlc.arg(description), NOW(), NOW())
RETURNING *;

-- name: UpdateAsn :one
UPDATE bgp_asns
SET name            = COALESCE(sqlc.narg(name)::text, name),
    kind            = COALESCE(sqlc.narg(kind)::bgp_asn_kind, kind),
    organization_id = CASE WHEN sqlc.arg(org_set)::bool THEN sqlc.narg(organization_id)::uuid ELSE organization_id END,
    description     = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    updated_at      = NOW()
WHERE id = $1
RETURNING *;

-- name: DeleteAsn :exec
DELETE FROM bgp_asns WHERE id = $1;

-- ===== Prefix lists =====
-- name: CreatePrefixList :one
INSERT INTO bgp_prefix_lists (id, name, family, description, created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(name), sqlc.arg(family)::address_family_v4v6, sqlc.arg(description), NOW(), NOW())
RETURNING *;

-- name: UpdatePrefixList :one
UPDATE bgp_prefix_lists
SET name        = COALESCE(sqlc.narg(name)::text, name),
    family      = COALESCE(sqlc.narg(family)::address_family_v4v6, family),
    description = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    updated_at  = NOW()
WHERE id = $1
RETURNING *;

-- name: DeletePrefixList :exec
DELETE FROM bgp_prefix_lists WHERE id = $1;

-- ===== Prefix list entries =====
-- name: CreatePrefixListEntry :one
INSERT INTO bgp_prefix_list_entries (id, prefix_list_id, seq, action, prefix, ge, le, description, created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(prefix_list_id), sqlc.arg(seq), sqlc.arg(action)::bgp_policy_action, sqlc.arg(prefix)::cidr, sqlc.arg(ge), sqlc.arg(le), sqlc.arg(description), NOW(), NOW())
RETURNING id, prefix_list_id, seq, action::text AS action,
          (host(prefix) || '/' || masklen(prefix))::text AS prefix,
          ge, le, description, created_at, updated_at;

-- name: UpdatePrefixListEntry :one
UPDATE bgp_prefix_list_entries
SET seq    = COALESCE(sqlc.narg(seq)::int, seq),
    action = COALESCE(sqlc.narg(action)::bgp_policy_action, action),
    prefix = COALESCE(sqlc.narg(prefix)::cidr, prefix),
    ge     = CASE WHEN sqlc.arg(ge_set)::bool THEN sqlc.narg(ge)::int ELSE ge END,
    le     = CASE WHEN sqlc.arg(le_set)::bool THEN sqlc.narg(le)::int ELSE le END,
    description = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    updated_at  = NOW()
WHERE id = $1
RETURNING id, prefix_list_id, seq, action::text AS action,
          (host(prefix) || '/' || masklen(prefix))::text AS prefix,
          ge, le, description, created_at, updated_at;

-- name: DeletePrefixListEntry :exec
DELETE FROM bgp_prefix_list_entries WHERE id = $1;

-- ===== Community lists =====
-- name: CreateCommunityList :one
INSERT INTO bgp_community_lists (id, name, kind, description, created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(name), sqlc.arg(kind)::bgp_community_kind, sqlc.arg(description), NOW(), NOW())
RETURNING *;

-- name: UpdateCommunityList :one
UPDATE bgp_community_lists
SET name        = COALESCE(sqlc.narg(name)::text, name),
    kind        = COALESCE(sqlc.narg(kind)::bgp_community_kind, kind),
    description = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    updated_at  = NOW()
WHERE id = $1
RETURNING *;

-- name: DeleteCommunityList :exec
DELETE FROM bgp_community_lists WHERE id = $1;

-- ===== Community list entries =====
-- name: CreateCommunityListEntry :one
INSERT INTO bgp_community_list_entries (id, community_list_id, seq, action, value, description, created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(community_list_id), sqlc.arg(seq), sqlc.arg(action)::bgp_policy_action, sqlc.arg(value), sqlc.arg(description), NOW(), NOW())
RETURNING *;

-- name: UpdateCommunityListEntry :one
UPDATE bgp_community_list_entries
SET seq    = COALESCE(sqlc.narg(seq)::int, seq),
    action = COALESCE(sqlc.narg(action)::bgp_policy_action, action),
    value  = COALESCE(sqlc.narg(value)::text, value),
    description = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    updated_at  = NOW()
WHERE id = $1
RETURNING *;

-- name: DeleteCommunityListEntry :exec
DELETE FROM bgp_community_list_entries WHERE id = $1;

-- ===== Route maps =====
-- name: CreateRouteMap :one
INSERT INTO bgp_route_maps (id, name, description, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, NOW(), NOW())
RETURNING id, name, description, created_at, updated_at;

-- name: UpdateRouteMap :one
UPDATE bgp_route_maps
SET name        = COALESCE(sqlc.narg(name)::text, name),
    description = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    updated_at  = NOW()
WHERE id = $1
RETURNING id, name, description, created_at, updated_at;

-- name: DeleteRouteMap :exec
DELETE FROM bgp_route_maps WHERE id = $1;

-- ===== Route map entries =====
-- name: CreateRouteMapEntry :one
INSERT INTO bgp_route_map_entries (id, route_map_id, seq, action,
                                   match_prefix_list_id, match_community_list_id, match_as_path_regex,
                                   set_local_pref, set_med, set_community,
                                   description, created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(route_map_id), sqlc.arg(seq), sqlc.arg(action)::bgp_policy_action,
        sqlc.arg(match_prefix_list_id), sqlc.arg(match_community_list_id), sqlc.arg(match_as_path_regex),
        sqlc.arg(set_local_pref), sqlc.arg(set_med), sqlc.arg(set_community),
        sqlc.arg(description), NOW(), NOW())
RETURNING *;

-- name: UpdateRouteMapEntry :one
UPDATE bgp_route_map_entries
SET seq    = COALESCE(sqlc.narg(seq)::int, seq),
    action = COALESCE(sqlc.narg(action)::bgp_policy_action, action),
    match_prefix_list_id    = CASE WHEN sqlc.arg(mpl_set)::bool THEN sqlc.narg(match_prefix_list_id)::uuid    ELSE match_prefix_list_id END,
    match_community_list_id = CASE WHEN sqlc.arg(mcl_set)::bool THEN sqlc.narg(match_community_list_id)::uuid ELSE match_community_list_id END,
    match_as_path_regex     = CASE WHEN sqlc.arg(asp_set)::bool THEN sqlc.narg(match_as_path_regex)::text     ELSE match_as_path_regex END,
    set_local_pref          = CASE WHEN sqlc.arg(slp_set)::bool THEN sqlc.narg(set_local_pref)::int           ELSE set_local_pref END,
    set_med                 = CASE WHEN sqlc.arg(med_set)::bool THEN sqlc.narg(set_med)::int                  ELSE set_med END,
    set_community           = CASE WHEN sqlc.arg(sc_set)::bool  THEN sqlc.narg(set_community)::text           ELSE set_community END,
    description             = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: DeleteRouteMapEntry :exec
DELETE FROM bgp_route_map_entries WHERE id = $1;

-- ===== Organization =====
-- name: CreateOrganization :one
INSERT INTO organizations (
    id, name, arin_org_id,
    address_line1, address_line2, city, state_province, postal_code, country,
    phone, email,
    admin_poc_name, admin_poc_email, admin_poc_phone,
    tech_poc_name,  tech_poc_email,  tech_poc_phone,
    abuse_poc_name, abuse_poc_email, abuse_poc_phone,
    noc_poc_name,   noc_poc_email,   noc_poc_phone,
    description, created_at, updated_at)
VALUES (
    gen_random_uuid(), $1, $2,
    $3, $4, $5, $6, $7, $8,
    $9, $10,
    $11, $12, $13,
    $14, $15, $16,
    $17, $18, $19,
    $20, $21, $22,
    $23, NOW(), NOW())
RETURNING id, name, arin_org_id,
          address_line1, address_line2, city, state_province, postal_code, country,
          phone, email,
          admin_poc_name, admin_poc_email, admin_poc_phone,
          tech_poc_name,  tech_poc_email,  tech_poc_phone,
          abuse_poc_name, abuse_poc_email, abuse_poc_phone,
          noc_poc_name,   noc_poc_email,   noc_poc_phone,
          description, created_at, updated_at;

-- name: UpdateOrganization :one
-- Many fields. PATCH uses COALESCE for required-typed columns
-- (caller passes nil to skip) and CASE WHEN <name>_set for nullable
-- columns where explicit null should clear.
UPDATE organizations
SET name            = COALESCE(sqlc.narg(name)::text, name),
    arin_org_id     = CASE WHEN sqlc.arg(arin_set)::bool          THEN sqlc.narg(arin_org_id)::text     ELSE arin_org_id END,
    address_line1   = COALESCE(sqlc.narg(address_line1)::text, address_line1),
    address_line2   = CASE WHEN sqlc.arg(addr2_set)::bool         THEN sqlc.narg(address_line2)::text   ELSE address_line2 END,
    city            = COALESCE(sqlc.narg(city)::text, city),
    state_province  = CASE WHEN sqlc.arg(state_set)::bool         THEN sqlc.narg(state_province)::text  ELSE state_province END,
    postal_code     = CASE WHEN sqlc.arg(postal_set)::bool        THEN sqlc.narg(postal_code)::text     ELSE postal_code END,
    country         = COALESCE(sqlc.narg(country)::text, country),
    phone           = CASE WHEN sqlc.arg(phone_set)::bool         THEN sqlc.narg(phone)::text           ELSE phone END,
    email           = CASE WHEN sqlc.arg(email_set)::bool         THEN sqlc.narg(email)::text           ELSE email END,
    admin_poc_name  = COALESCE(sqlc.narg(admin_poc_name)::text, admin_poc_name),
    admin_poc_email = COALESCE(sqlc.narg(admin_poc_email)::text, admin_poc_email),
    admin_poc_phone = CASE WHEN sqlc.arg(apoc_phone_set)::bool    THEN sqlc.narg(admin_poc_phone)::text ELSE admin_poc_phone END,
    tech_poc_name   = COALESCE(sqlc.narg(tech_poc_name)::text, tech_poc_name),
    tech_poc_email  = COALESCE(sqlc.narg(tech_poc_email)::text, tech_poc_email),
    tech_poc_phone  = CASE WHEN sqlc.arg(tpoc_phone_set)::bool    THEN sqlc.narg(tech_poc_phone)::text  ELSE tech_poc_phone END,
    abuse_poc_name  = COALESCE(sqlc.narg(abuse_poc_name)::text, abuse_poc_name),
    abuse_poc_email = COALESCE(sqlc.narg(abuse_poc_email)::text, abuse_poc_email),
    abuse_poc_phone = CASE WHEN sqlc.arg(abpoc_phone_set)::bool   THEN sqlc.narg(abuse_poc_phone)::text ELSE abuse_poc_phone END,
    noc_poc_name    = CASE WHEN sqlc.arg(npoc_name_set)::bool     THEN sqlc.narg(noc_poc_name)::text    ELSE noc_poc_name END,
    noc_poc_email   = CASE WHEN sqlc.arg(npoc_email_set)::bool    THEN sqlc.narg(noc_poc_email)::text   ELSE noc_poc_email END,
    noc_poc_phone   = CASE WHEN sqlc.arg(npoc_phone_set)::bool    THEN sqlc.narg(noc_poc_phone)::text   ELSE noc_poc_phone END,
    description     = CASE WHEN sqlc.arg(description_set)::bool   THEN sqlc.narg(description)::text     ELSE description END,
    updated_at      = NOW()
WHERE id = $1
RETURNING id, name, arin_org_id,
          address_line1, address_line2, city, state_province, postal_code, country,
          phone, email,
          admin_poc_name, admin_poc_email, admin_poc_phone,
          tech_poc_name,  tech_poc_email,  tech_poc_phone,
          abuse_poc_name, abuse_poc_email, abuse_poc_phone,
          noc_poc_name,   noc_poc_email,   noc_poc_phone,
          description, created_at, updated_at;

-- name: CountAsnsForOrganization :one
SELECT count(*)::bigint FROM bgp_asns WHERE organization_id = sqlc.arg(org_id)::uuid;

-- name: DeleteOrganization :exec
DELETE FROM organizations WHERE id = $1;

-- ===== Collectors =====

-- name: EnrollCollector :one
-- Creates a pending collector row with the hashed enrollment token.
-- The collector exchanges the plaintext token for an mTLS cert + API
-- token on first call. capabilities is the JSONB array of collector
-- self-declared capability codes (kept as JSONB for parity with Python).
INSERT INTO collectors (id, site_id, name, capabilities, status,
                        enrollment_token_hash, buffered_samples, enabled,
                        config_overrides, created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(site_id), sqlc.arg(name), sqlc.arg(capabilities_json)::jsonb, 'pending'::collector_status,
        sqlc.arg(enrollment_token_hash)::text, 0, true, '{}'::jsonb, NOW(), NOW())
RETURNING id, site_id;

-- name: HeartbeatCollector :one
-- Updates the collector row on heartbeat and returns the current
-- config_overrides so the response can echo them back to the agent.
-- version is non-null only when the agent advertised one; null
-- preserves the existing value via COALESCE. status flips between
-- 'healthy' and 'degraded' based on whether last_error was set.
UPDATE collectors
SET last_seen_at      = sqlc.arg(last_seen_at)::timestamptz,
    buffered_samples  = sqlc.arg(buffered_samples)::int,
    version           = COALESCE(sqlc.narg(version)::text, version),
    status            = sqlc.arg(status)::collector_status,
    updated_at        = NOW()
WHERE id = $1
RETURNING config_overrides;

-- name: InsertCollectorHeartbeat :exec
-- Heartbeat audit-trail row. metrics_json is the agent's free-form
-- key/value blob. last_error is nullable; queue_depth defaults to 0
-- when the agent doesn't send it.
INSERT INTO collector_heartbeats (id, collector_id, received_at,
                                  queue_depth, last_error, metrics_json,
                                  created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(collector_id), sqlc.arg(received_at)::timestamptz,
        sqlc.arg(queue_depth)::int, sqlc.arg(last_error), sqlc.arg(metrics_json)::jsonb,
        NOW(), NOW());

-- name: SetCollectorConfigOverrides :one
UPDATE collectors
SET config_overrides = sqlc.arg(config_overrides)::jsonb,
    updated_at = NOW()
WHERE id = $1
RETURNING id, site_id, name, version, mtls_fingerprint,
          status::text AS status, capabilities,
          last_seen_at, last_ingest_at, buffered_samples, enabled,
          config_overrides, created_at, updated_at;

-- name: SetCollectorEnabled :one
UPDATE collectors
SET enabled = $2,
    updated_at = NOW()
WHERE id = $1
RETURNING id, site_id, name, version, mtls_fingerprint,
          status::text AS status, capabilities,
          last_seen_at, last_ingest_at, buffered_samples, enabled,
          config_overrides, created_at, updated_at;

-- name: DecommissionCollector :one
UPDATE collectors
SET status = 'decommissioned'::collector_status,
    updated_at = NOW()
WHERE id = $1
RETURNING id, site_id, name, version, mtls_fingerprint,
          status::text AS status, capabilities,
          last_seen_at, last_ingest_at, buffered_samples, enabled,
          config_overrides, created_at, updated_at;
