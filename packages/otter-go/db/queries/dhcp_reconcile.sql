-- DHCP reservation ↔ IPAM reconciliation (PR 12 of the DHCP push
-- port). Ports Python's services/dhcp_reconcile.py's reconcile_scope.
-- The orchestrator loads every IPAddress row in the scope's linked
-- subnet, cross-checks each reservation in scope.reservations_json,
-- and emits a per-reservation report.
--
-- Narrow projection: only the four columns the matcher reads. The
-- standard IPAddress projection omits dhcp_duid (PR 94 in Python);
-- the reconcile path needs both dhcp_mac (v4 binding check, PR 88)
-- and dhcp_duid (v6 binding check, PR 94), so we materialize them
-- here instead of widening the global IPAddress projection.

-- name: ListIPAddressesInSubnetForReconcile :many
SELECT id, address::text AS address, source::text AS source,
       dhcp_mac, dhcp_duid
FROM ip_addresses
WHERE subnet_id = $1;

-- ===== Mutating reconcile sync (PR 13) =====
-- Ports services/dhcp_reconcile.py:sync_reservations. INSERT for the
-- "unbacked" branch (creates a fresh reservation row); UPDATE for
-- the "promote dhcp → reservation" branch (flips an existing lease
-- to reservation, backfills mac/duid/dns_name when they're NULL on
-- the row but set on the reservation).

-- name: InsertReservationIPAddress :one
-- Creates a fresh IPAddress with source=reservation, status=reserved.
-- role defaults to 'data' per Python at services/dhcp_reconcile.py:306.
-- Returns id so the handler can reference it in the per-entry
-- decision payload.
INSERT INTO ip_addresses (
    id, subnet_id, address, role, status, source,
    dhcp_mac, dhcp_duid, dns_name,
    created_at, updated_at
)
VALUES (gen_random_uuid(), $1, $2::inet, 'data', 'reserved', 'reservation',
        $3, $4, $5, NOW(), NOW())
RETURNING id;

-- name: PromoteDhcpLeaseToReservation :exec
-- Flips an existing source='dhcp' row to source='reservation' +
-- status='reserved'. Backfills dhcp_mac / dhcp_duid / dns_name when
-- the reservation knows them but the row's column is NULL — never
-- overwrites a populated column (Python at services/dhcp_reconcile.
-- py:354-367). The COALESCE-on-NULL guards encode the "only
-- backfill, don't overwrite" rule entirely on the DB side.
UPDATE ip_addresses
SET source     = 'reservation',
    status     = 'reserved',
    dhcp_mac   = COALESCE(dhcp_mac,  $2::text),
    dhcp_duid  = COALESCE(dhcp_duid, $3::text),
    dns_name   = COALESCE(dns_name,  $4::text),
    updated_at = NOW()
WHERE id = $1;
