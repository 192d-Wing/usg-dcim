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

-- ----- Keys -----

-- name: ListTcpAoKeys :many
-- Optional key_chain_id filter narrows to one chain's history.
SELECT id, key_chain_id, key_id, send_id, recv_id,
       algorithm::text AS algorithm,
       secret, valid_from, valid_to, description,
       created_at, updated_at
FROM tcp_ao_keys
WHERE (sqlc.narg(key_chain_id)::uuid IS NULL OR key_chain_id = sqlc.narg(key_chain_id))
ORDER BY key_chain_id, key_id
LIMIT $1 OFFSET $2;

-- name: CountTcpAoKeys :one
SELECT count(*)::bigint
FROM tcp_ao_keys
WHERE (sqlc.narg(key_chain_id)::uuid IS NULL OR key_chain_id = sqlc.narg(key_chain_id));

-- name: GetTcpAoKey :one
SELECT id, key_chain_id, key_id, send_id, recv_id,
       algorithm::text AS algorithm,
       secret, valid_from, valid_to, description,
       created_at, updated_at
FROM tcp_ao_keys
WHERE id = $1;

-- name: CreateTcpAoKey :one
INSERT INTO tcp_ao_keys (id, key_chain_id, key_id, send_id, recv_id,
                         algorithm, secret, valid_from, valid_to,
                         description, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, $4,
        $5::tcp_ao_algorithm, $6, $7, $8,
        $9, NOW(), NOW())
RETURNING id, key_chain_id, key_id, send_id, recv_id,
          algorithm::text AS algorithm,
          secret, valid_from, valid_to, description,
          created_at, updated_at;

-- name: UpdateTcpAoKey :one
-- key_chain_id is intentionally not patchable — moving a key across
-- chains would orphan rotation history and break the (chain_id,
-- key_id) unique index downstream. Operators delete + recreate to
-- relocate.
UPDATE tcp_ao_keys
SET key_id      = COALESCE(sqlc.narg(key_id)::int, key_id),
    send_id     = COALESCE(sqlc.narg(send_id)::int, send_id),
    recv_id     = COALESCE(sqlc.narg(recv_id)::int, recv_id),
    algorithm   = COALESCE(sqlc.narg(algorithm)::tcp_ao_algorithm, algorithm),
    secret      = COALESCE(sqlc.narg(secret)::text, secret),
    valid_from  = CASE WHEN sqlc.arg(valid_from_set)::bool THEN sqlc.narg(valid_from)::timestamptz ELSE valid_from END,
    valid_to    = CASE WHEN sqlc.arg(valid_to_set)::bool   THEN sqlc.narg(valid_to)::timestamptz   ELSE valid_to   END,
    description = CASE WHEN sqlc.arg(description_set)::bool THEN sqlc.narg(description)::text ELSE description END,
    updated_at  = NOW()
WHERE id = $1
RETURNING id, key_chain_id, key_id, send_id, recv_id,
          algorithm::text AS algorithm,
          secret, valid_from, valid_to, description,
          created_at, updated_at;

-- name: DeleteTcpAoKey :exec
DELETE FROM tcp_ao_keys WHERE id = $1;

-- name: MaxKeyIDInTcpAoKeyChain :one
-- Used by the rotate-batch handler to pick the next free key_id so a
-- second rotation extends the chain rather than colliding. Returns 0
-- when the chain has no keys (COALESCE keeps the type clean for the
-- handler's Go-side increment).
SELECT COALESCE(MAX(key_id), 0)::int AS max_key_id
FROM tcp_ao_keys
WHERE key_chain_id = $1;
