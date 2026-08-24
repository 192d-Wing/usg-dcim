-- name: ListRegions :many
-- ABAC: caller passes the set of region IDs in scope (computed from the
-- caller's site scope). NULL = no restriction.
SELECT *
FROM regions
WHERE (sqlc.narg(region_ids)::uuid[] IS NULL OR id = ANY(sqlc.narg(region_ids)::uuid[]))
ORDER BY code
LIMIT $1 OFFSET $2;

-- name: CountRegions :one
SELECT count(*)::bigint
FROM regions
WHERE (sqlc.narg(region_ids)::uuid[] IS NULL OR id = ANY(sqlc.narg(region_ids)::uuid[]));

-- name: GetRegion :one
SELECT *
FROM regions
WHERE id = $1;

-- name: ListRegionIDsForSiteIDs :many
-- Used by handler-side ABAC: collapse the in-scope site list to the
-- regions that contain them, since the Python `list_regions` filter is
-- "show regions that contain at least one in-scope site."
SELECT DISTINCT region_id
FROM sites
WHERE id = ANY(sqlc.arg(site_ids)::uuid[]);
