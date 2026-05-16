-- ===== Community lists =====
-- name: ListCommunityLists :many
SELECT id, name, kind::text AS kind, description, created_at, updated_at
FROM bgp_community_lists
WHERE (sqlc.narg(kind)::text IS NULL OR kind::text = sqlc.narg(kind))
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountCommunityLists :one
SELECT count(*)::bigint
FROM bgp_community_lists
WHERE (sqlc.narg(kind)::text IS NULL OR kind::text = sqlc.narg(kind));

-- ===== Community list entries =====
-- name: ListCommunityListEntries :many
SELECT id, community_list_id, seq, action::text AS action,
       value, description, created_at, updated_at
FROM bgp_community_list_entries
WHERE (sqlc.narg(community_list_id)::uuid IS NULL OR community_list_id = sqlc.narg(community_list_id))
ORDER BY community_list_id, seq
LIMIT $1 OFFSET $2;

-- name: CountCommunityListEntries :one
SELECT count(*)::bigint
FROM bgp_community_list_entries
WHERE (sqlc.narg(community_list_id)::uuid IS NULL OR community_list_id = sqlc.narg(community_list_id));

-- ===== Route maps =====
-- name: ListRouteMaps :many
SELECT id, name, description, created_at, updated_at
FROM bgp_route_maps
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountRouteMaps :one
SELECT count(*)::bigint FROM bgp_route_maps;

-- ===== Route map entries =====
-- name: ListRouteMapEntries :many
SELECT id, route_map_id, seq, action::text AS action,
       match_prefix_list_id, match_community_list_id, match_as_path_regex,
       set_local_pref, set_med, set_community,
       description, created_at, updated_at
FROM bgp_route_map_entries
WHERE (sqlc.narg(route_map_id)::uuid IS NULL OR route_map_id = sqlc.narg(route_map_id))
ORDER BY route_map_id, seq
LIMIT $1 OFFSET $2;

-- name: CountRouteMapEntries :one
SELECT count(*)::bigint
FROM bgp_route_map_entries
WHERE (sqlc.narg(route_map_id)::uuid IS NULL OR route_map_id = sqlc.narg(route_map_id));
