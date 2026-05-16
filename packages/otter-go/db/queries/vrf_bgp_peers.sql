-- name: ListVrfBgpPeers :many
SELECT id, vrf_id, bgp_peer_id, address_family::text AS address_family,
       rd, enabled, created_at, updated_at
FROM vrf_bgp_peers
WHERE (sqlc.narg(vrf_id)::uuid         IS NULL OR vrf_id      = sqlc.narg(vrf_id))
  AND (sqlc.narg(bgp_peer_id)::uuid    IS NULL OR bgp_peer_id = sqlc.narg(bgp_peer_id))
  AND (sqlc.narg(address_family)::text IS NULL OR address_family::text = sqlc.narg(address_family))
ORDER BY created_at DESC
LIMIT $1 OFFSET $2;

-- name: CountVrfBgpPeers :one
SELECT count(*)::bigint FROM vrf_bgp_peers
WHERE (sqlc.narg(vrf_id)::uuid         IS NULL OR vrf_id      = sqlc.narg(vrf_id))
  AND (sqlc.narg(bgp_peer_id)::uuid    IS NULL OR bgp_peer_id = sqlc.narg(bgp_peer_id))
  AND (sqlc.narg(address_family)::text IS NULL OR address_family::text = sqlc.narg(address_family));
