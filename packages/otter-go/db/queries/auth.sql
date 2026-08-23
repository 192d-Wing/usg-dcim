-- name: GetUser :one
SELECT *
FROM users
WHERE id = $1;

-- name: GetUserByEmail :one
SELECT *
FROM users
WHERE email = $1;

-- name: GetUserCapabilities :many
-- Permission codes granted to a user by their assigned roles. The
-- ABAC scope_dimension on role_scopes is intentionally ignored here
-- (treated as global for PR 35); PR 36 will refine this to a per-
-- capability scope set.
SELECT DISTINCT jsonb_array_elements_text(r.permission_codes::jsonb) AS code
FROM user_roles ur
JOIN roles r ON r.id = ur.role_id
WHERE ur.user_id = $1;

-- name: GetCapabilitiesForIdpRoles :many
-- Resolve OIDC-asserted role names to DCIM permission codes via
-- oidc_role_mappings. scope_dimension/target are read but PR 35
-- treats every matched cap as global.
SELECT DISTINCT jsonb_array_elements_text(r.permission_codes::jsonb) AS code
FROM oidc_role_mappings m
JOIN roles r ON r.id = m.dcim_role_id
WHERE m.idp_role = ANY($1::text[]);

-- name: IsJtiRevoked :one
-- Returns true if the JTI has been revoked (and not yet pruned past
-- expires_at). Used on every authenticated request.
SELECT EXISTS (
    SELECT 1 FROM revoked_jtis
    WHERE jti = $1
      AND expires_at > NOW()
)::bool AS revoked;
