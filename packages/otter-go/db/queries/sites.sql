-- name: ListSites :many
-- Page-bounded site list, ABAC-filtered by caller in the handler via
-- the optional in_scope array. Pass site_ids = NULL to skip the filter.
-- organization_id (PR 67) joins onto organizations.id; the legacy
-- string `organization` field is kept for backward compatibility.
SELECT id, region_id, name, code, address, latitude, longitude,
       timezone, majcom, organization, organization_id, mission_owner,
       enclave, classification, lifecycle_state, metadata_json,
       created_at, updated_at
FROM sites
WHERE (sqlc.narg(region_id)::uuid       IS NULL OR region_id       = sqlc.narg(region_id))
  AND (sqlc.narg(majcom)::text          IS NULL OR majcom          = sqlc.narg(majcom))
  AND (sqlc.narg(enclave)::text         IS NULL OR enclave         = sqlc.narg(enclave))
  AND (sqlc.narg(organization)::text    IS NULL OR organization    = sqlc.narg(organization))
  AND (sqlc.narg(organization_id)::uuid IS NULL OR organization_id = sqlc.narg(organization_id))
  AND (sqlc.narg(lifecycle_state)::text IS NULL OR lifecycle_state::text = sqlc.narg(lifecycle_state))
  AND (sqlc.narg(site_ids)::uuid[]      IS NULL OR id              = ANY(sqlc.narg(site_ids)::uuid[]))
ORDER BY code
LIMIT $1 OFFSET $2;

-- name: CountSites :one
SELECT count(*)::bigint
FROM sites
WHERE (sqlc.narg(region_id)::uuid       IS NULL OR region_id       = sqlc.narg(region_id))
  AND (sqlc.narg(majcom)::text          IS NULL OR majcom          = sqlc.narg(majcom))
  AND (sqlc.narg(enclave)::text         IS NULL OR enclave         = sqlc.narg(enclave))
  AND (sqlc.narg(organization)::text    IS NULL OR organization    = sqlc.narg(organization))
  AND (sqlc.narg(organization_id)::uuid IS NULL OR organization_id = sqlc.narg(organization_id))
  AND (sqlc.narg(lifecycle_state)::text IS NULL OR lifecycle_state::text = sqlc.narg(lifecycle_state))
  AND (sqlc.narg(site_ids)::uuid[]      IS NULL OR id              = ANY(sqlc.narg(site_ids)::uuid[]));

-- name: GetSite :one
SELECT id, region_id, name, code, address, latitude, longitude,
       timezone, majcom, organization, organization_id, mission_owner,
       enclave, classification, lifecycle_state, metadata_json,
       created_at, updated_at
FROM sites
WHERE id = $1;
