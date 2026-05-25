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

-- name: ListAdminRoles :many
-- PR 75 — paginated list for /admin/roles.
SELECT id, name, description, permission_codes, is_system, created_at, updated_at
FROM roles
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountAdminRoles :one
SELECT count(*)::bigint FROM roles;

-- name: GetAdminRole :one
SELECT id, name, description, permission_codes, is_system, created_at, updated_at
FROM roles WHERE id = $1;

-- name: GetAdminRoleByName :one
SELECT id, name, description, permission_codes, is_system, created_at, updated_at
FROM roles WHERE name = $1;

-- name: CreateAdminRole :one
-- PR 75 — admin role creation. is_system is hardcoded FALSE: only
-- the migration bootstrap creates system roles, never the API.
INSERT INTO roles (id, name, description, permission_codes, is_system, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3::jsonb, FALSE, NOW(), NOW())
RETURNING id, name, description, permission_codes, is_system, created_at, updated_at;

-- name: UpdateAdminRole :one
-- PR 75 — update mutable role fields. is_system is immutable
-- (the API also refuses to update system roles entirely, but
-- the SQL guards belt-and-suspenders).
UPDATE roles
SET description     = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    permission_codes = CASE WHEN sqlc.arg(permission_codes_set)::bool THEN sqlc.narg(permission_codes)::jsonb ELSE permission_codes END,
    updated_at      = NOW()
WHERE id = $1 AND is_system = FALSE
RETURNING id, name, description, permission_codes, is_system, created_at, updated_at;

-- name: DeleteAdminRole :exec
-- PR 75 — only non-system roles deletable. The API also pre-checks
-- for assignments and refuses with 409; this SQL is defense-in-
-- depth (a race between the assignment check and the delete is
-- still caught by the FK constraint).
DELETE FROM roles WHERE id = $1 AND is_system = FALSE;

-- name: CountUserRolesForRole :one
-- PR 75 — pre-delete check: refuse if anyone has this role
-- assigned. Returning a count rather than a bool so the API can
-- include the number in the 409 response if useful.
SELECT count(*)::bigint FROM user_roles WHERE role_id = $1;

-- ===== Role assignments (user_roles + role_scopes) =====
-- PR 76 — assignments + scope rows.

-- name: ListUserAssignments :many
SELECT id, user_id, role_id, created_at, updated_at
FROM user_roles
WHERE user_id = $1
ORDER BY created_at;

-- name: GetUserRole :one
SELECT id, user_id, role_id, created_at, updated_at
FROM user_roles WHERE id = $1;

-- name: FindUserRoleByUserAndRole :one
-- Pre-check used by the API for the dup-409 path.
SELECT id, user_id, role_id, created_at, updated_at
FROM user_roles
WHERE user_id = $1 AND role_id = $2
LIMIT 1;

-- name: CreateUserRole :one
INSERT INTO user_roles (id, user_id, role_id, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, NOW(), NOW())
RETURNING id, user_id, role_id, created_at, updated_at;

-- name: DeleteUserRole :exec
-- role_scopes are deleted manually before this; we don't rely on FK
-- cascade because the schema declares no ON DELETE behavior for the
-- assignment_id FK.
DELETE FROM user_roles WHERE id = $1;

-- name: ListRoleScopesByAssignment :many
SELECT id, assignment_id, scope_type::text AS scope_type, target_id
FROM role_scopes
WHERE assignment_id = $1
ORDER BY created_at;

-- name: ListRoleScopesByAssignments :many
-- Bulk version for the list endpoint: one round-trip for N
-- assignments' scopes, bucketed in-process. Avoids N+1 on
-- /admin/users/{id}/assignments.
SELECT id, assignment_id, scope_type::text AS scope_type, target_id
FROM role_scopes
WHERE assignment_id = ANY($1::uuid[])
ORDER BY assignment_id, created_at;

-- name: CreateRoleScope :one
INSERT INTO role_scopes (id, assignment_id, scope_type, target_id, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2::scope_type, $3, NOW(), NOW())
RETURNING id, assignment_id, scope_type::text AS scope_type, target_id;

-- name: DeleteRoleScopesForAssignment :exec
DELETE FROM role_scopes WHERE assignment_id = $1;

-- name: GetRoleNamesByIDs :many
-- Hydration helper: many-to-one role lookup so the assignment-list
-- response includes role_name without a per-row GetRole.
SELECT id, name FROM roles WHERE id = ANY($1::uuid[]);

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
