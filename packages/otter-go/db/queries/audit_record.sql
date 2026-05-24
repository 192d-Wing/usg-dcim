-- name: InsertAuditLog :exec
-- Append-only audit row. actor_user_id comes from a JWT bearer; for
-- API tokens it stays NULL and actor_token_id carries the token UUID
-- instead. actor_label is always set ("user:<uuid>", "token:<uuid>",
-- or a literal like "stub"/"anonymous:<email>").
INSERT INTO audit_logs (
    id, occurred_at,
    actor_user_id, actor_token_id, actor_label, actor_ip,
    action, target_type, target_id, site_id, request_id,
    success, diff_json, metadata_json,
    created_at, updated_at)
VALUES (
    gen_random_uuid(), NOW(),
    $1, $2, $3, $4,
    $5, $6, $7, $8, $9,
    $10, COALESCE($11::jsonb, '{}'::jsonb), COALESCE($12::jsonb, '{}'::jsonb),
    NOW(), NOW());
