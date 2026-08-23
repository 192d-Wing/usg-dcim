-- name: GetUserBySsoSubject :one
SELECT *
FROM users
WHERE sso_subject = sqlc.arg(sso_subject)::text;

-- name: CreateOidcUser :one
INSERT INTO users (id, email, display_name, is_active, sso_subject,
                   last_login_at, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, TRUE, $3, NOW(), NOW(), NOW())
RETURNING *;

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
RETURNING *;
