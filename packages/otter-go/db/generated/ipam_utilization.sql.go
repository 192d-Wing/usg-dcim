// Hand-edited sqlc-style generated bindings for the IPAM utilization
// cron queries. Drift-checked against db/queries/ipam_utilization.sql
// by CI's otter-go sqlc drift check.
package dbq

import (
	"context"

	"github.com/google/uuid"
)

// ---- Models ----

type SubnetForUtilizationRow struct {
	ID         uuid.UUID  `json:"id"`
	FabricID   uuid.UUID  `json:"fabric_id"`
	SupernetID *uuid.UUID `json:"supernet_id"`
	Prefix     string     `json:"prefix"`
}

type SupernetForUtilizationRow struct {
	ID       uuid.UUID `json:"id"`
	FabricID uuid.UUID `json:"fabric_id"`
	Prefix   string    `json:"prefix"`
}

type ActiveReservedAddressCountRow struct {
	SubnetID  uuid.UUID `json:"subnet_id"`
	UsedCount int64     `json:"used_count"`
}

// ---- Queries ----

const listSubnetsForUtilization = `-- name: ListSubnetsForUtilization :many
SELECT id, fabric_id, supernet_id,
       host(prefix) || '/' || masklen(prefix) AS prefix
FROM subnets
`

func (q *Queries) ListSubnetsForUtilization(ctx context.Context) ([]SubnetForUtilizationRow, error) {
	rows, err := q.db.Query(ctx, listSubnetsForUtilization)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []SubnetForUtilizationRow
	for rows.Next() {
		var r SubnetForUtilizationRow
		if err := rows.Scan(&r.ID, &r.FabricID, &r.SupernetID, &r.Prefix); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

const listSupernetsForUtilization = `-- name: ListSupernetsForUtilization :many
SELECT id, fabric_id,
       host(prefix) || '/' || masklen(prefix) AS prefix
FROM supernets
`

func (q *Queries) ListSupernetsForUtilization(ctx context.Context) ([]SupernetForUtilizationRow, error) {
	rows, err := q.db.Query(ctx, listSupernetsForUtilization)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []SupernetForUtilizationRow
	for rows.Next() {
		var r SupernetForUtilizationRow
		if err := rows.Scan(&r.ID, &r.FabricID, &r.Prefix); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}

const listActiveReservedAddressCountsBySubnet = `-- name: ListActiveReservedAddressCountsBySubnet :many
SELECT subnet_id, COUNT(*)::bigint AS used_count
FROM ip_addresses
WHERE status::text IN ('active', 'reserved')
  AND subnet_id IS NOT NULL
GROUP BY subnet_id
`

func (q *Queries) ListActiveReservedAddressCountsBySubnet(ctx context.Context) ([]ActiveReservedAddressCountRow, error) {
	rows, err := q.db.Query(ctx, listActiveReservedAddressCountsBySubnet)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []ActiveReservedAddressCountRow
	for rows.Next() {
		var r ActiveReservedAddressCountRow
		if err := rows.Scan(&r.SubnetID, &r.UsedCount); err != nil {
			return nil, err
		}
		items = append(items, r)
	}
	return items, rows.Err()
}
