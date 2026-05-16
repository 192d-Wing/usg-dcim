-- ===== Audit log =====
-- name: ListAuditLog :many
SELECT id, occurred_at, actor_user_id, actor_token_id, actor_label, actor_ip,
       action, target_type, target_id, site_id, request_id, success,
       diff_json, metadata_json, created_at, updated_at
FROM audit_log
WHERE (sqlc.narg(actor_user_id)::uuid IS NULL OR actor_user_id = sqlc.narg(actor_user_id))
  AND (sqlc.narg(action)::text        IS NULL OR action        = sqlc.narg(action))
  AND (sqlc.narg(target_type)::text   IS NULL OR target_type   = sqlc.narg(target_type))
  AND (sqlc.narg(target_id)::text     IS NULL OR target_id     = sqlc.narg(target_id))
  AND (sqlc.narg(site_id)::uuid       IS NULL OR site_id       = sqlc.narg(site_id))
  AND (sqlc.narg(since)::timestamptz  IS NULL OR occurred_at  >= sqlc.narg(since))
  AND (sqlc.narg(until)::timestamptz  IS NULL OR occurred_at  <= sqlc.narg(until))
  AND (sqlc.narg(success)::bool       IS NULL OR success       = sqlc.narg(success))
ORDER BY occurred_at DESC
LIMIT $1 OFFSET $2;

-- name: CountAuditLog :one
SELECT count(*)::bigint
FROM audit_log
WHERE (sqlc.narg(actor_user_id)::uuid IS NULL OR actor_user_id = sqlc.narg(actor_user_id))
  AND (sqlc.narg(action)::text        IS NULL OR action        = sqlc.narg(action))
  AND (sqlc.narg(target_type)::text   IS NULL OR target_type   = sqlc.narg(target_type))
  AND (sqlc.narg(target_id)::text     IS NULL OR target_id     = sqlc.narg(target_id))
  AND (sqlc.narg(site_id)::uuid       IS NULL OR site_id       = sqlc.narg(site_id))
  AND (sqlc.narg(since)::timestamptz  IS NULL OR occurred_at  >= sqlc.narg(since))
  AND (sqlc.narg(until)::timestamptz  IS NULL OR occurred_at  <= sqlc.narg(until))
  AND (sqlc.narg(success)::bool       IS NULL OR success       = sqlc.narg(success));

-- name: ListAuditActions :many
-- Distinct actions seen in the log — drives the action-filter dropdown.
SELECT DISTINCT action FROM audit_log ORDER BY action;

-- ===== Alert Rules =====
-- name: ListAlertRules :many
SELECT id, name, description, metric, operator, threshold, duration_seconds,
       severity::text AS severity, site_scope_id, asset_filter_json, enabled,
       runbook_url, created_at, updated_at
FROM alert_rules
WHERE (sqlc.narg(site_scope_id)::uuid IS NULL OR site_scope_id = sqlc.narg(site_scope_id))
  AND (sqlc.narg(enabled)::bool       IS NULL OR enabled       = sqlc.narg(enabled))
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountAlertRules :one
SELECT count(*)::bigint
FROM alert_rules
WHERE (sqlc.narg(site_scope_id)::uuid IS NULL OR site_scope_id = sqlc.narg(site_scope_id))
  AND (sqlc.narg(enabled)::bool       IS NULL OR enabled       = sqlc.narg(enabled));

-- ===== Alerts =====
-- name: ListAlerts :many
SELECT id, rule_id, site_id, asset_id, collector_id,
       severity::text AS severity, state::text AS state,
       dedupe_key, correlation_key, summary, detail,
       first_seen_at, last_seen_at, acked_by, acked_at, resolved_at,
       labels_json, created_at, updated_at
FROM alerts
WHERE (sqlc.narg(site_id)::uuid  IS NULL OR site_id  = sqlc.narg(site_id))
  AND (sqlc.narg(state)::text    IS NULL OR state::text    = sqlc.narg(state))
  AND (sqlc.narg(severity)::text IS NULL OR severity::text = sqlc.narg(severity))
ORDER BY last_seen_at DESC
LIMIT $1 OFFSET $2;

-- name: CountAlerts :one
SELECT count(*)::bigint
FROM alerts
WHERE (sqlc.narg(site_id)::uuid  IS NULL OR site_id  = sqlc.narg(site_id))
  AND (sqlc.narg(state)::text    IS NULL OR state::text    = sqlc.narg(state))
  AND (sqlc.narg(severity)::text IS NULL OR severity::text = sqlc.narg(severity));
