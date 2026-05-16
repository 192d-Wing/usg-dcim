-- name: UpdateUserLastLogin :exec
UPDATE users SET last_login_at = NOW(), updated_at = NOW() WHERE id = $1;

-- name: InsertRevokedJti :exec
INSERT INTO revoked_jtis (jti, user_id, revoked_at, reason, expires_at)
VALUES ($1, $2, NOW(), $3, $4)
ON CONFLICT (jti) DO NOTHING;

-- ===== API tokens =====

-- name: GetApiTokenByHash :one
SELECT id, name, owner_user_id, token_hash, permission_codes, scope_json,
       expires_at, last_used_at, revoked, created_at, updated_at
FROM api_tokens
WHERE token_hash = $1;

-- name: ListApiTokensByOwner :many
SELECT id, name, owner_user_id, token_hash, permission_codes, scope_json,
       expires_at, last_used_at, revoked, created_at, updated_at
FROM api_tokens
WHERE owner_user_id = $1
ORDER BY created_at DESC;

-- name: GetApiToken :one
SELECT id, name, owner_user_id, token_hash, permission_codes, scope_json,
       expires_at, last_used_at, revoked, created_at, updated_at
FROM api_tokens
WHERE id = $1;

-- name: CreateApiToken :one
INSERT INTO api_tokens (id, name, owner_user_id, token_hash, permission_codes,
                        scope_json, expires_at, revoked, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, FALSE, NOW(), NOW())
RETURNING id, name, owner_user_id, token_hash, permission_codes, scope_json,
          expires_at, last_used_at, revoked, created_at, updated_at;

-- name: RevokeApiToken :exec
UPDATE api_tokens SET revoked = TRUE, updated_at = NOW() WHERE id = $1;

-- name: TouchApiTokenLastUsed :exec
UPDATE api_tokens SET last_used_at = NOW() WHERE id = $1;
