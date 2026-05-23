-- ===== Fabrics =====
-- scope_fabric_ids gates the list to a specific set of fabrics for
-- fabric-scoped principals. NULL = no scope filter (global caller).
-- See auth.ScopedFabricFilter.
-- name: ListFabrics :many
SELECT id, name, slug, description, enclave, classification,
       dns_recursive_upstreams, dns_deny_networks, catalog_transfer_acl,
       recursive_engine::text AS recursive_engine,
       created_at, updated_at
FROM fabrics
WHERE (sqlc.narg(enclave)::text IS NULL OR enclave = sqlc.narg(enclave))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]))
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountFabrics :one
SELECT count(*)::bigint
FROM fabrics
WHERE (sqlc.narg(enclave)::text IS NULL OR enclave = sqlc.narg(enclave))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]));

-- name: GetFabric :one
SELECT id, name, slug, description, enclave, classification,
       dns_recursive_upstreams, dns_deny_networks, catalog_transfer_acl,
       recursive_engine::text AS recursive_engine,
       created_at, updated_at
FROM fabrics
WHERE id = $1;

-- ===== Supernets =====
-- The parent_supernet_id filter has three modes that don't compose
-- naturally with a single nullable param: (a) no filter, (b) "where
-- parent_supernet_id IS NULL" for top-level supernets, (c) "where
-- parent_supernet_id = X" for a specific parent. Handler picks the
-- right query via the top_level flag.

-- name: ListSupernets :many
SELECT id, fabric_id, vrf_id, parent_supernet_id, site_id,
       host(prefix) || '/' || masklen(prefix) AS prefix,
       name, description, purpose, created_at, updated_at
FROM supernets
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(vrf_id)::uuid    IS NULL OR vrf_id    = sqlc.narg(vrf_id))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]))
  AND (
        sqlc.narg(parent_filter_mode)::text = 'any'
     OR (sqlc.narg(parent_filter_mode)::text = 'null' AND parent_supernet_id IS NULL)
     OR (sqlc.narg(parent_filter_mode)::text = 'eq'   AND parent_supernet_id = sqlc.narg(parent_supernet_id)::uuid)
  )
ORDER BY prefix
LIMIT $1 OFFSET $2;

-- name: CountSupernets :one
SELECT count(*)::bigint
FROM supernets
WHERE (sqlc.narg(fabric_id)::uuid IS NULL OR fabric_id = sqlc.narg(fabric_id))
  AND (sqlc.narg(vrf_id)::uuid    IS NULL OR vrf_id    = sqlc.narg(vrf_id))
  AND (sqlc.narg(scope_fabric_ids)::uuid[] IS NULL OR fabric_id = ANY(sqlc.narg(scope_fabric_ids)::uuid[]))
  AND (
        sqlc.narg(parent_filter_mode)::text = 'any'
     OR (sqlc.narg(parent_filter_mode)::text = 'null' AND parent_supernet_id IS NULL)
     OR (sqlc.narg(parent_filter_mode)::text = 'eq'   AND parent_supernet_id = sqlc.narg(parent_supernet_id)::uuid)
  );

-- name: GetSupernet :one
SELECT id, fabric_id, vrf_id, parent_supernet_id, site_id,
       host(prefix) || '/' || masklen(prefix) AS prefix,
       name, description, purpose, created_at, updated_at
FROM supernets
WHERE id = $1;
