-- ===== DHCP bundle endpoint =====
-- Reads only. The bundle endpoint (GET /api/v1/dhcp/servers/{id}/bundle,
-- followup PR) calls all three in sequence: load the server row for
-- cache + base_config; list every live scope on it; bulk-load any
-- templates the scopes reference. The renderer then projects subnet4/
-- subnet6 arrays onto the base_config and returns the bundle. The
-- scheduler job (rerender_dhcp_bundle, followup PR) calls the same
-- query set and writes back into bundle_cache_*.

-- name: GetDhcpServerBundleRow :one
-- Reads only the columns the bundle path needs: base_config + the
-- bundle_cache_* triad. ABAC fabric_id is also returned so the
-- handler can apply the existing per-fabric scope filter without a
-- second round-trip. Auth credentials, last_sync_*, last_push_*,
-- enabled, auto_push are NOT selected — they belong to other
-- endpoints. Returns ErrNoRows on a missing or deleted server.
SELECT id, name, fabric_id, base_config,
       bundle_cache_at, bundle_cache_etag, bundle_cache_json
FROM dhcp_servers
WHERE id = $1;

-- name: ListDhcpScopesForBundle :many
-- Every live scope for one server, in stable order. The renderer
-- relies on input order to land subnet4/subnet6 arrays in a
-- deterministic Kea config; ORDER BY (ip_family, prefix) is the
-- "human read" sort (v4 before v6, lower prefixes first) so a
-- bundle diff'd against a previous render shows minimal churn.
-- Disabled scopes are returned but filtered out at render time —
-- the renderer is the single point of truth for what makes it
-- into Kea. Soft-deleted scopes (deleted_at IS NOT NULL) are
-- excluded here because they're conceptually gone.
SELECT id, dhcp_server_id, subnet_id, name, ip_family, prefix::text AS prefix,
       pools_json, pd_pools_json, options_json, reservations_json,
       valid_lifetime_seconds, renew_timer_seconds, rebind_timer_seconds,
       preferred_lifetime_seconds, enabled, description,
       kea_subnet_id, template_id,
       last_diff_at, last_diff_status, last_diff_delta_json,
       auto_push_override, deleted_at, created_at, updated_at
FROM dhcp_scopes
WHERE dhcp_server_id = $1
  AND deleted_at IS NULL
ORDER BY ip_family, prefix;

-- name: ListDhcpScopeTemplatesByIDs :many
-- Bulk-load the templates referenced by a scope set in one round-
-- trip. Caller assembles the ID set from the scope list's
-- template_id column (after de-duping); a scope with template_id
-- pointing at a missing/deleted template is handled gracefully by
-- the renderer (treats it as no template — bundle.effectiveScope
-- in PR #216).
SELECT id, fabric_id, name, ip_family, options_json,
       valid_lifetime_seconds, renew_timer_seconds, rebind_timer_seconds,
       preferred_lifetime_seconds, description, created_at, updated_at
FROM dhcp_scope_templates
WHERE id = ANY($1::uuid[]);
