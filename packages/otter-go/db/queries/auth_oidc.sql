-- name: GetUserBySsoSubject :one
SELECT id, email, display_name, is_active, sso_subject, password_hash,
       last_login_at, idp_refresh_token, idp_refresh_token_iat,
       created_at, updated_at
FROM users
WHERE sso_subject = $1;

-- name: CreateOidcUser :one
INSERT INTO users (id, email, display_name, is_active, sso_subject,
                   last_login_at, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, TRUE, $3, NOW(), NOW(), NOW())
RETURNING id, email, display_name, is_active, sso_subject, password_hash,
          last_login_at, idp_refresh_token, idp_refresh_token_iat,
          created_at, updated_at;

-- name: UpdateOidcUserOnLogin :one
-- Updates sso_subject + display_name (only when previously unset) and
-- last_login_at. Mirrors the Python _upsert_oidc_user branch that runs
-- when a matching row already exists.
UPDATE users
SET sso_subject  = $2,
    display_name = COALESCE(display_name, $3),
    last_login_at = NOW(),
    updated_at   = NOW()
WHERE id = $1
RETURNING id, email, display_name, is_active, sso_subject, password_hash,
          last_login_at, idp_refresh_token, idp_refresh_token_iat,
          created_at, updated_at;
