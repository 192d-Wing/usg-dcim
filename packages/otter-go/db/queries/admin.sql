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
SELECT *
FROM roles
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountAdminRoles :one
SELECT count(*)::bigint FROM roles;

-- name: GetAdminRole :one
SELECT *
FROM roles WHERE id = $1;

-- name: GetAdminRoleByName :one
SELECT *
FROM roles WHERE name = $1;

-- name: CreateAdminRole :one
-- PR 75 — admin role creation. is_system is hardcoded FALSE: only
-- the migration bootstrap creates system roles, never the API.
INSERT INTO roles (id, name, description, permission_codes, is_system, created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(name), sqlc.arg(description), sqlc.arg(permission_codes)::jsonb, FALSE, NOW(), NOW())
RETURNING *;

-- name: UpdateAdminRole :one
-- PR 75 — update mutable role fields. is_system is immutable
-- (the API also refuses to update system roles entirely, but
-- the SQL guards belt-and-suspenders).
UPDATE roles
SET description     = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    permission_codes = CASE WHEN sqlc.arg(permission_codes_set)::bool THEN sqlc.narg(permission_codes)::jsonb ELSE permission_codes END,
    updated_at      = NOW()
WHERE id = $1 AND is_system = FALSE
RETURNING *;

-- name: DeleteAdminRole :execrows
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
SELECT *
FROM user_roles
WHERE user_id = $1
ORDER BY created_at;

-- name: GetUserRole :one
SELECT *
FROM user_roles WHERE id = $1;

-- name: FindUserRoleByUserAndRole :one
-- Pre-check used by the API for the dup-409 path.
SELECT *
FROM user_roles
WHERE user_id = $1 AND role_id = $2
LIMIT 1;

-- name: CreateUserRole :one
INSERT INTO user_roles (id, user_id, role_id, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, NOW(), NOW())
RETURNING *;

-- name: DeleteUserRole :execrows
-- role_scopes are deleted manually before this; we don't rely on FK
-- cascade because the schema declares no ON DELETE behavior for the
-- assignment_id FK.
DELETE FROM user_roles WHERE id = $1;

-- name: ListRoleScopesByAssignment :many
SELECT *
FROM role_scopes
WHERE assignment_id = $1
ORDER BY created_at;

-- name: ListRoleScopesByAssignments :many
-- Bulk version for the list endpoint: one round-trip for N
-- assignments' scopes, bucketed in-process. Avoids N+1 on
-- /admin/users/{id}/assignments.
SELECT *
FROM role_scopes
WHERE assignment_id = ANY(sqlc.arg(ids)::uuid[])
ORDER BY assignment_id, created_at;

-- name: CreateRoleScope :one
INSERT INTO role_scopes (id, assignment_id, scope_type, target_id, created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(assignment_id), sqlc.arg(scope_type)::scope_type, sqlc.arg(target_id), NOW(), NOW())
RETURNING *;

-- name: DeleteRoleScopesForAssignment :exec
DELETE FROM role_scopes WHERE assignment_id = $1;

-- name: GetRoleNamesByIDs :many
-- Hydration helper: many-to-one role lookup so the assignment-list
-- response includes role_name without a per-row GetRole.
SELECT id, name FROM roles WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- ===== OIDC Role Mappings =====
-- PR 77 — CRUD for the IdP role → DCIM role table that the OIDC
-- login flow consults to materialize a Principal's capabilities.

-- name: ListOidcRoleMappings :many
SELECT *
FROM oidc_role_mappings
ORDER BY idp_role
LIMIT $1 OFFSET $2;

-- name: CountOidcRoleMappings :one
SELECT count(*)::bigint FROM oidc_role_mappings;

-- name: GetOidcRoleMapping :one
SELECT *
FROM oidc_role_mappings WHERE id = $1;

-- name: GetOidcRoleMappingByIdpRole :one
-- Pre-check used by the create handler for the dup-409 path.
SELECT *
FROM oidc_role_mappings WHERE idp_role = $1;

-- name: CreateOidcRoleMapping :one
INSERT INTO oidc_role_mappings (id, idp_role, claim_source, dcim_role_id,
                                description, scope_dimension, scope_target,
                                created_at, updated_at)
VALUES (gen_random_uuid(), sqlc.arg(idp_role), sqlc.arg(claim_source), sqlc.arg(dcim_role_id),
        sqlc.arg(description), sqlc.narg(scope_dimension)::scope_type, sqlc.arg(scope_target), NOW(), NOW())
RETURNING *;

-- name: UpdateOidcRoleMapping :one
-- All five mutable fields are individually opt-in so callers can
-- patch a single column without resending the rest. scope_dimension
-- clears scope_target too (CASE) since target is meaningless
-- without a dimension.
UPDATE oidc_role_mappings
SET claim_source    = COALESCE(sqlc.narg(claim_source)::text, claim_source),
    dcim_role_id    = COALESCE(sqlc.narg(dcim_role_id)::uuid, dcim_role_id),
    description     = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    scope_dimension = CASE WHEN sqlc.arg(scope_dim_set)::bool THEN sqlc.narg(scope_dimension)::scope_type ELSE scope_dimension END,
    scope_target    = CASE
        WHEN sqlc.arg(scope_dim_set)::bool AND sqlc.narg(scope_dimension)::scope_type IS NULL THEN NULL
        WHEN sqlc.arg(scope_target_set)::bool THEN sqlc.narg(scope_target)::text
        ELSE scope_target
    END,
    updated_at      = NOW()
WHERE id = $1
RETURNING *;

-- name: DeleteOidcRoleMapping :execrows
DELETE FROM oidc_role_mappings WHERE id = $1;

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

-- name: SetUserPasswordHash :execrows
-- Local password set/reset for admin-created users (UX-debt batch).
-- The handler bcrypts; NULL is never written here — clearing a
-- password isn't offered.
UPDATE users SET password_hash = sqlc.arg(password_hash)::text,
                 updated_at = NOW()
WHERE id = sqlc.arg(id);
