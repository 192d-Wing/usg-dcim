-- Admin domain queries (PR 74+). Users CRUD lands first; roles +
-- assignments + OIDC mappings land in follow-ups. All routes gate
-- on admin:* capabilities + emit audit.
--
-- The login-path reads (GetUser, GetUserBySsoSubject, etc.) live
-- in auth_oidc.sql / auth_local.sql; this file holds the admin-
-- specific mutations that aren't part of the login flow.

-- name: ListAdminUsers :many
-- PR 74 — paginated list for /admin/users. No filter args yet;
-- Python doesn't expose them either. ScopeFabricIds isn't
-- applicable here — admin reads are gated on the capability +
-- the cap can only be granted globally (no per-fabric users).
SELECT id, email, display_name, is_active, sso_subject,
       last_login_at, created_at, updated_at
FROM users
ORDER BY email
LIMIT $1 OFFSET $2;

-- name: CountAdminUsers :one
SELECT count(*)::bigint FROM users;

-- name: CreateAdminUser :one
-- PR 74 — admin user creation. No password set: production is
-- OIDC; the password_hash column stays NULL until a break-glass
-- operator runs the offline bootstrap flow. is_active defaults
-- TRUE so a newly-created user can log in immediately when their
-- OIDC subject is first observed.
INSERT INTO users (id, email, display_name, is_active, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, NOW(), NOW())
RETURNING id, email, display_name, is_active, sso_subject,
          last_login_at, created_at, updated_at;

-- name: UpdateAdminUser :one
-- PR 74 — admin update covers display_name + is_active only
-- (matches Python UserUpdate schema). email is immutable in the
-- API — operators who need to reissue an email delete + re-create
-- so the audit trail captures the identity change as two events.
UPDATE users
SET display_name = CASE WHEN sqlc.arg(display_name_set)::bool THEN sqlc.narg(display_name)::text ELSE display_name END,
    is_active    = COALESCE(sqlc.narg(is_active)::bool, is_active),
    updated_at   = NOW()
WHERE id = $1
RETURNING id, email, display_name, is_active, sso_subject,
          last_login_at, created_at, updated_at;
