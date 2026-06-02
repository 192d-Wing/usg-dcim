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
