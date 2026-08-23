-- name: InsertAuditLog :exec
-- Append-only audit row. actor_user_id comes from a JWT bearer; for
-- API tokens it stays NULL and actor_token_id carries the token UUID
-- instead. actor_label is always set ("user:<uuid>", "token:<uuid>",
-- or a literal like "stub"/"anonymous:<email>").
INSERT INTO audit_log (
    id, occurred_at,
    actor_user_id, actor_token_id, actor_label, actor_ip,
    action, target_type, target_id, site_id, request_id,
    success, diff_json, metadata_json,
    created_at, updated_at)
VALUES (
    gen_random_uuid(), NOW(),
    sqlc.narg(actor_user_id), sqlc.narg(actor_token_id),
    sqlc.narg(actor_label), sqlc.narg(actor_ip),
    sqlc.arg(action), sqlc.narg(target_type), sqlc.narg(target_id),
    sqlc.narg(site_id), sqlc.narg(request_id),
    sqlc.arg(success),
    COALESCE(sqlc.narg(diff_json)::jsonb, '{}'::jsonb),
    COALESCE(sqlc.narg(metadata_json)::jsonb, '{}'::jsonb),
    NOW(), NOW());
