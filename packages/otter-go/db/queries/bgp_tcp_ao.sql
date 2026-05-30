-- TCP AO key-chains CRUD. Keys + rotate-batch land in a follow-up PR.

-- name: ListTcpAoKeyChains :many
SELECT id, name, description, created_at, updated_at
FROM tcp_ao_key_chains
ORDER BY name
LIMIT $1 OFFSET $2;

-- name: CountTcpAoKeyChains :one
SELECT count(*)::bigint FROM tcp_ao_key_chains;

-- name: GetTcpAoKeyChain :one
SELECT id, name, description, created_at, updated_at
FROM tcp_ao_key_chains
WHERE id = $1;

-- name: CreateTcpAoKeyChain :one
INSERT INTO tcp_ao_key_chains (id, name, description, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, NOW(), NOW())
RETURNING id, name, description, created_at, updated_at;

-- name: UpdateTcpAoKeyChain :one
-- name is NOT NULL; description is nullable. Both use the COALESCE
-- semantic (omit = keep current) plus an explicit set-flag for
-- description so `{"description": null}` clears the row instead of
-- silently no-op'ing. Matches Pydantic exclude_unset=True parity.
UPDATE tcp_ao_key_chains
SET name        = COALESCE(sqlc.narg(name)::text, name),
    description = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    updated_at  = NOW()
WHERE id = $1
RETURNING id, name, description, created_at, updated_at;

-- name: CountKeysInTcpAoKeyChain :one
-- DELETE guard: Python refuses to drop a chain that still has keys
-- so rotation history stays intentional. Handler reads this before
-- DeleteTcpAoKeyChain and 409s if it's non-zero.
SELECT count(*)::bigint
FROM tcp_ao_keys
WHERE key_chain_id = $1;

-- name: DeleteTcpAoKeyChain :exec
DELETE FROM tcp_ao_key_chains WHERE id = $1;
