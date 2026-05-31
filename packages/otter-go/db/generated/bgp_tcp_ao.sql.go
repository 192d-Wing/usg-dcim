// Hand-maintained alongside db/queries/bgp_tcp_ao.sql. Keep both files
// in sync when adding TCP-AO key-chain queries; sqlc generate writes
// this file from the .sql source but the repo runs sqlc as a drift
// check, not a generator, so edits live here.
package dbq

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const listTcpAoKeyChains = `-- name: ListTcpAoKeyChains :many
SELECT id, name, description, created_at, updated_at
FROM tcp_ao_key_chains
ORDER BY name
LIMIT $1 OFFSET $2
`

type ListTcpAoKeyChainsParams struct {
	Limit  int32 `json:"limit"`
	Offset int32 `json:"offset"`
}

func (q *Queries) ListTcpAoKeyChains(ctx context.Context, arg ListTcpAoKeyChainsParams) ([]TcpAoKeyChain, error) {
	rows, err := q.db.Query(ctx, listTcpAoKeyChains, arg.Limit, arg.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []TcpAoKeyChain
	for rows.Next() {
		var c TcpAoKeyChain
		if err := rows.Scan(&c.ID, &c.Name, &c.Description, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	return items, rows.Err()
}

const countTcpAoKeyChains = `SELECT count(*)::bigint FROM tcp_ao_key_chains`

func (q *Queries) CountTcpAoKeyChains(ctx context.Context) (int64, error) {
	row := q.db.QueryRow(ctx, countTcpAoKeyChains)
	var n int64
	err := row.Scan(&n)
	return n, err
}

const getTcpAoKeyChain = `-- name: GetTcpAoKeyChain :one
SELECT id, name, description, created_at, updated_at
FROM tcp_ao_key_chains
WHERE id = $1
`

func (q *Queries) GetTcpAoKeyChain(ctx context.Context, id uuid.UUID) (TcpAoKeyChain, error) {
	row := q.db.QueryRow(ctx, getTcpAoKeyChain, id)
	var c TcpAoKeyChain
	err := row.Scan(&c.ID, &c.Name, &c.Description, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

const createTcpAoKeyChain = `-- name: CreateTcpAoKeyChain :one
INSERT INTO tcp_ao_key_chains (id, name, description, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, NOW(), NOW())
RETURNING id, name, description, created_at, updated_at
`

type CreateTcpAoKeyChainParams struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

func (q *Queries) CreateTcpAoKeyChain(ctx context.Context, arg CreateTcpAoKeyChainParams) (TcpAoKeyChain, error) {
	row := q.db.QueryRow(ctx, createTcpAoKeyChain, arg.Name, arg.Description)
	var c TcpAoKeyChain
	err := row.Scan(&c.ID, &c.Name, &c.Description, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

const updateTcpAoKeyChain = `-- name: UpdateTcpAoKeyChain :one
UPDATE tcp_ao_key_chains
SET name        = COALESCE($2::text, name),
    description = CASE WHEN $3::bool THEN $4::text ELSE description END,
    updated_at  = NOW()
WHERE id = $1
RETURNING id, name, description, created_at, updated_at
`

type UpdateTcpAoKeyChainParams struct {
	ID             uuid.UUID `json:"id"`
	Name           *string   `json:"name"`
	DescriptionSet bool      `json:"description_set"`
	Description    *string   `json:"description"`
}

func (q *Queries) UpdateTcpAoKeyChain(ctx context.Context, arg UpdateTcpAoKeyChainParams) (TcpAoKeyChain, error) {
	row := q.db.QueryRow(ctx, updateTcpAoKeyChain, arg.ID, arg.Name, arg.DescriptionSet, arg.Description)
	var c TcpAoKeyChain
	err := row.Scan(&c.ID, &c.Name, &c.Description, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

const countKeysInTcpAoKeyChain = `SELECT count(*)::bigint FROM tcp_ao_keys WHERE key_chain_id = $1`

func (q *Queries) CountKeysInTcpAoKeyChain(ctx context.Context, keyChainID uuid.UUID) (int64, error) {
	row := q.db.QueryRow(ctx, countKeysInTcpAoKeyChain, keyChainID)
	var n int64
	err := row.Scan(&n)
	return n, err
}

const deleteTcpAoKeyChain = `DELETE FROM tcp_ao_key_chains WHERE id = $1`

func (q *Queries) DeleteTcpAoKeyChain(ctx context.Context, id uuid.UUID) error {
	_, err := q.db.Exec(ctx, deleteTcpAoKeyChain, id)
	return err
}

// ---- Keys ----

const tcpAoKeyCols = `id, key_chain_id, key_id, send_id, recv_id,
       algorithm::text AS algorithm,
       secret, valid_from, valid_to, description,
       created_at, updated_at`

func scanTcpAoKey(row interface{ Scan(...any) error }, k *TcpAoKey) error {
	return row.Scan(&k.ID, &k.KeyChainID, &k.KeyID, &k.SendID, &k.RecvID,
		&k.Algorithm, &k.Secret, &k.ValidFrom, &k.ValidTo, &k.Description,
		&k.CreatedAt, &k.UpdatedAt)
}

const listTcpAoKeys = `-- name: ListTcpAoKeys :many
SELECT ` + tcpAoKeyCols + `
FROM tcp_ao_keys
WHERE ($3::uuid IS NULL OR key_chain_id = $3)
ORDER BY key_chain_id, key_id
LIMIT $1 OFFSET $2
`

type ListTcpAoKeysParams struct {
	Limit      int32      `json:"limit"`
	Offset     int32      `json:"offset"`
	KeyChainID *uuid.UUID `json:"key_chain_id"`
}

func (q *Queries) ListTcpAoKeys(ctx context.Context, arg ListTcpAoKeysParams) ([]TcpAoKey, error) {
	rows, err := q.db.Query(ctx, listTcpAoKeys, arg.Limit, arg.Offset, arg.KeyChainID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []TcpAoKey
	for rows.Next() {
		var k TcpAoKey
		if err := scanTcpAoKey(rows, &k); err != nil {
			return nil, err
		}
		items = append(items, k)
	}
	return items, rows.Err()
}

const countTcpAoKeys = `-- name: CountTcpAoKeys :one
SELECT count(*)::bigint
FROM tcp_ao_keys
WHERE ($1::uuid IS NULL OR key_chain_id = $1)
`

type CountTcpAoKeysParams struct {
	KeyChainID *uuid.UUID `json:"key_chain_id"`
}

func (q *Queries) CountTcpAoKeys(ctx context.Context, arg CountTcpAoKeysParams) (int64, error) {
	row := q.db.QueryRow(ctx, countTcpAoKeys, arg.KeyChainID)
	var n int64
	err := row.Scan(&n)
	return n, err
}

const getTcpAoKey = `-- name: GetTcpAoKey :one
SELECT ` + tcpAoKeyCols + `
FROM tcp_ao_keys
WHERE id = $1
`

func (q *Queries) GetTcpAoKey(ctx context.Context, id uuid.UUID) (TcpAoKey, error) {
	row := q.db.QueryRow(ctx, getTcpAoKey, id)
	var k TcpAoKey
	err := scanTcpAoKey(row, &k)
	return k, err
}

const createTcpAoKey = `-- name: CreateTcpAoKey :one
INSERT INTO tcp_ao_keys (id, key_chain_id, key_id, send_id, recv_id,
                         algorithm, secret, valid_from, valid_to,
                         description, created_at, updated_at)
VALUES (gen_random_uuid(), $1, $2, $3, $4,
        $5::tcp_ao_algorithm, $6, $7, $8,
        $9, NOW(), NOW())
RETURNING ` + tcpAoKeyCols

type CreateTcpAoKeyParams struct {
	KeyChainID  uuid.UUID  `json:"key_chain_id"`
	KeyID       int32      `json:"key_id"`
	SendID      int32      `json:"send_id"`
	RecvID      int32      `json:"recv_id"`
	Algorithm   string     `json:"algorithm"`
	Secret      string     `json:"secret"`
	ValidFrom   *time.Time `json:"valid_from"`
	ValidTo     *time.Time `json:"valid_to"`
	Description *string    `json:"description"`
}

func (q *Queries) CreateTcpAoKey(ctx context.Context, arg CreateTcpAoKeyParams) (TcpAoKey, error) {
	row := q.db.QueryRow(ctx, createTcpAoKey,
		arg.KeyChainID, arg.KeyID, arg.SendID, arg.RecvID,
		arg.Algorithm, arg.Secret, arg.ValidFrom, arg.ValidTo,
		arg.Description)
	var k TcpAoKey
	err := scanTcpAoKey(row, &k)
	return k, err
}

const updateTcpAoKey = `-- name: UpdateTcpAoKey :one
UPDATE tcp_ao_keys
SET key_id      = COALESCE($2::int, key_id),
    send_id     = COALESCE($3::int, send_id),
    recv_id     = COALESCE($4::int, recv_id),
    algorithm   = COALESCE($5::tcp_ao_algorithm, algorithm),
    secret      = COALESCE($6::text, secret),
    valid_from  = CASE WHEN $7::bool  THEN $8::timestamptz  ELSE valid_from  END,
    valid_to    = CASE WHEN $9::bool  THEN $10::timestamptz ELSE valid_to    END,
    description = CASE WHEN $11::bool THEN $12::text        ELSE description END,
    updated_at  = NOW()
WHERE id = $1
RETURNING ` + tcpAoKeyCols

type UpdateTcpAoKeyParams struct {
	ID             uuid.UUID  `json:"id"`
	KeyID          *int32     `json:"key_id"`
	SendID         *int32     `json:"send_id"`
	RecvID         *int32     `json:"recv_id"`
	Algorithm      *string    `json:"algorithm"`
	Secret         *string    `json:"secret"`
	ValidFromSet   bool       `json:"valid_from_set"`
	ValidFrom      *time.Time `json:"valid_from"`
	ValidToSet     bool       `json:"valid_to_set"`
	ValidTo        *time.Time `json:"valid_to"`
	DescriptionSet bool       `json:"description_set"`
	Description    *string    `json:"description"`
}

func (q *Queries) UpdateTcpAoKey(ctx context.Context, arg UpdateTcpAoKeyParams) (TcpAoKey, error) {
	row := q.db.QueryRow(ctx, updateTcpAoKey,
		arg.ID, arg.KeyID, arg.SendID, arg.RecvID,
		arg.Algorithm, arg.Secret,
		arg.ValidFromSet, arg.ValidFrom,
		arg.ValidToSet, arg.ValidTo,
		arg.DescriptionSet, arg.Description)
	var k TcpAoKey
	err := scanTcpAoKey(row, &k)
	return k, err
}

const deleteTcpAoKey = `DELETE FROM tcp_ao_keys WHERE id = $1`

func (q *Queries) DeleteTcpAoKey(ctx context.Context, id uuid.UUID) error {
	_, err := q.db.Exec(ctx, deleteTcpAoKey, id)
	return err
}

const maxKeyIDInTcpAoKeyChain = `SELECT COALESCE(MAX(key_id), 0)::int AS max_key_id FROM tcp_ao_keys WHERE key_chain_id = $1`

func (q *Queries) MaxKeyIDInTcpAoKeyChain(ctx context.Context, keyChainID uuid.UUID) (int32, error) {
	row := q.db.QueryRow(ctx, maxKeyIDInTcpAoKeyChain, keyChainID)
	var max int32
	err := row.Scan(&max)
	return max, err
}
