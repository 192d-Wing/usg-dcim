-- ===== ASNs =====
-- name: ListAsns :many
SELECT id, asn, name, kind::text AS kind, organization_id, description, created_at, updated_at
FROM bgp_asns
WHERE (sqlc.narg(kind)::text IS NULL OR kind::text = sqlc.narg(kind))
ORDER BY asn
LIMIT $1 OFFSET $2;

-- name: CountAsns :one
SELECT count(*)::bigint
FROM bgp_asns
WHERE (sqlc.narg(kind)::text IS NULL OR kind::text = sqlc.narg(kind));

-- ===== Prefix lists =====
-- name: ListPrefixLists :many
SELECT id, name, family::text AS family, description, created_at, updated_at
FROM bgp_prefix_lists
WHERE (sqlc.narg(family)::text IS NULL OR family::text = sqlc.narg(family))
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountPrefixLists :one
SELECT count(*)::bigint
FROM bgp_prefix_lists
WHERE (sqlc.narg(family)::text IS NULL OR family::text = sqlc.narg(family));

-- ===== Prefix list entries =====
-- name: ListPrefixListEntries :many
SELECT id, prefix_list_id, seq, action::text AS action,
       host(prefix) || '/' || masklen(prefix) AS prefix,
       ge, le, description, created_at, updated_at
FROM bgp_prefix_list_entries
WHERE (sqlc.narg(prefix_list_id)::uuid IS NULL OR prefix_list_id = sqlc.narg(prefix_list_id))
ORDER BY prefix_list_id, seq
LIMIT $1 OFFSET $2;

-- name: CountPrefixListEntries :one
SELECT count(*)::bigint
FROM bgp_prefix_list_entries
WHERE (sqlc.narg(prefix_list_id)::uuid IS NULL OR prefix_list_id = sqlc.narg(prefix_list_id));
