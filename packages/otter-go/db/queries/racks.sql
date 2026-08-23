-- name: ListRacks :many
-- scope_site_ids is the PR 62 ABAC LIST filter: NULL = no restriction
-- (global caller); a non-NULL slice restricts the result to racks
-- belonging to the named sites. See auth.ScopedSiteFilter.
SELECT *
FROM racks
WHERE (sqlc.narg(site_id)::uuid IS NULL OR site_id = sqlc.narg(site_id))
  AND (sqlc.narg(row_id)::uuid  IS NULL OR row_id  = sqlc.narg(row_id))
  AND (sqlc.narg(scope_site_ids)::uuid[] IS NULL OR site_id = ANY(sqlc.narg(scope_site_ids)::uuid[]))
ORDER BY code
LIMIT $1 OFFSET $2;

-- name: CountRacks :one
SELECT count(*)::bigint
FROM racks
WHERE (sqlc.narg(site_id)::uuid IS NULL OR site_id = sqlc.narg(site_id))
  AND (sqlc.narg(row_id)::uuid  IS NULL OR row_id  = sqlc.narg(row_id))
  AND (sqlc.narg(scope_site_ids)::uuid[] IS NULL OR site_id = ANY(sqlc.narg(scope_site_ids)::uuid[]));

-- name: GetRack :one
SELECT *
FROM racks
WHERE id = $1;
