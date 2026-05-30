// Hand-maintained alongside db/queries/bgp_tcp_ao.sql. Keep both files
// in sync when adding TCP-AO key-chain queries; sqlc generate writes
// this file from the .sql source but the repo runs sqlc as a drift
// check, not a generator, so edits live here.
package dbq

import (
	"context"

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
