-- Global search queries — one read per result bucket. Caller
-- (internal/search/handler.go) runs them sequentially against a
-- single pgx connection (matches the Python AsyncSession layout).
-- ILIKE substring matches on the inventory side; the IP bucket
-- fires only when the query parses as a literal address.
--
-- Wire-shape parity: each query projects only what api/search.py shapes
-- into JSON. Full rows aren't needed.

-- name: SearchSites :many
SELECT id, name, code
FROM sites
WHERE name ILIKE sqlc.arg(pattern) OR code ILIKE sqlc.arg(pattern)
ORDER BY name
LIMIT sqlc.arg(result_limit);

-- name: SearchRacks :many
SELECT id, name, site_id
FROM racks
WHERE name ILIKE sqlc.arg(pattern) OR code ILIKE sqlc.arg(pattern) OR serial ILIKE sqlc.arg(pattern)
ORDER BY name
LIMIT sqlc.arg(result_limit);

-- name: SearchAssets :many
-- mgmt_ip is a TEXT column on assets (not INET); ILIKE works directly.
-- kind is a Postgres ENUM — ::text cast matches the codebase
-- convention (see db/generated/assets.sql.go) so pgx scans cleanly
-- without a custom type registration.
SELECT id, name, hostname, serial, kind::text AS kind, site_id
FROM assets
WHERE name ILIKE sqlc.arg(pattern) OR hostname ILIKE sqlc.arg(pattern) OR serial ILIKE sqlc.arg(pattern) OR mgmt_ip ILIKE sqlc.arg(pattern)
ORDER BY name
LIMIT sqlc.arg(result_limit);

-- name: SearchIPAddressesByHost :many
-- Exact match on the host portion of the INET column — Python's
-- api/search.py::_looks_like_ip strips any trailing /N before passing
-- the canonical host string here. host() comparison is stable across
-- stored prefix lengths (a row written as 10.0.0.5/24 still matches
-- the lookup "10.0.0.5"). role/status/source are ENUMs — see the
-- ::text cast comment on SearchAssets.
SELECT id, subnet_id, asset_id,
       host(address) AS address,
       role::text AS role, status::text AS status, source::text AS source,
       dns_name
FROM ip_addresses
WHERE host(address) = sqlc.arg(host)
ORDER BY address
LIMIT sqlc.arg(result_limit);

-- name: SearchSubnetsByIDs :many
-- Bulk fetch for the IP-search enrichment. Projects the columns the
-- response shape exposes (subnet_id, subnet_prefix, plus the FK keys
-- the handler walks to fetch vrf+fabric in the same fan-out).
SELECT id, fabric_id, vrf_id,
       (host(prefix) || '/' || masklen(prefix))::text AS prefix
FROM subnets
WHERE id = ANY($1::uuid[]);

-- name: SearchVrfsByIDs :many
SELECT id, name FROM vrfs WHERE id = ANY($1::uuid[]);

-- name: SearchFabricsByIDs :many
SELECT id, name FROM fabrics WHERE id = ANY($1::uuid[]);

-- name: SearchAssetsByIDs :many
-- For the IPAddress enrichment join — projects only id+name since
-- that's all the response shape carries (asset_id + asset_name).
SELECT id, name FROM assets WHERE id = ANY($1::uuid[]);
