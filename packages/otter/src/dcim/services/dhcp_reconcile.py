"""Reservation ↔ IPAddress reconciliation (PR 84).

A `DhcpScope.reservations_json` entry pins a specific IP to a
specific client (MAC for v4, DUID for v6). The IPAM side keeps
`IPAddress` rows with a `source` enum: `static` (operator-allocated),
`dhcp` (learned from a lease), `reservation` (set aside for a known
client). When DCIM authors a Kea reservation, the IPAM view should
ideally already have an `IPAddress` row with `source=reservation`
for the same IP. If it doesn't, three states are possible:

  * **clean**     — every reservation matches an IPAddress with
                    source=reservation (or source=dhcp tied to the
                    same MAC/DUID, which means the lease has
                    materialized into the right row).
  * **collision** — the reservation IP exists as IPAddress with
                    source=static. The operator has hand-allocated
                    the same address to something else; pushing
                    this scope would tell Kea to hand out an IP
                    that's already in use.
  * **unbacked**  — the reservation IP doesn't appear in IPAM at
                    all. Kea will happily serve it, but the IPAM
                    inventory won't reflect the allocation —
                    nothing flags the address as taken on the next
                    static-allocation pass.

PR 84 ships warn-only: this module builds a `ReconcileReport`; the
API endpoint serves it. PR 85 will add the mutating sync — upsert
unbacked reservations as `source=reservation` IPAddress rows.

Pure-ish: takes the scope + the linked Subnet's IPAddress rows.
No DB I/O inside reconcile_scope; the API handler does the load.
"""

from __future__ import annotations

import ipaddress
from dataclasses import dataclass

from ..models.ipam import IPAddress, IpAddressSource


@dataclass
class ReconcileEntry:
    reservation_ip: str
    identifier: str  # MAC or DUID, from the scope's reservation row
    status: str      # "clean" | "collision" | "unbacked"
    ip_address_id: str | None = None  # IPAddress.id when matched
    ip_source: str | None = None      # IPAddress.source on collision
    note: str | None = None


@dataclass
class ReconcileReport:
    scope_id: str
    subnet_id: str | None
    total: int
    counts: dict[str, int]
    entries: list[ReconcileEntry]


def _normalize_ip(ip: str) -> str | None:
    """Normalize "10.0.0.05" → "10.0.0.5" and "2001:db8::1" forms so
    string comparison against IPAddress.address holds. Returns None
    on parse error (the reservation entry is malformed)."""
    try:
        return str(ipaddress.ip_address(ip.strip()))
    except (TypeError, ValueError):
        return None


def reconcile_scope(scope, ip_rows: list[IPAddress]) -> ReconcileReport:
    """Build a ReconcileReport for `scope` against the IPAddress rows
    already loaded for the linked subnet.

    Caller is responsible for the SELECT — typically:
        SELECT * FROM ip_addresses WHERE subnet_id = scope.subnet_id
    `scope.subnet_id` being NULL is reported but doesn't crash; the
    report has total=0 with an explanatory note on each reservation.
    """
    ip_index: dict[str, IPAddress] = {}
    for row in ip_rows:
        key = _normalize_ip(str(row.address).split("/")[0])
        if key is not None:
            ip_index[key] = row

    entries: list[ReconcileEntry] = []
    for r in scope.reservations_json or []:
        # Identifier — v4 uses mac, v6 uses duid; one or the other.
        ident = r.get("mac") or r.get("duid") or ""
        raw_ip = r.get("ip", "")
        norm = _normalize_ip(raw_ip)
        if norm is None:
            entries.append(ReconcileEntry(
                reservation_ip=raw_ip, identifier=ident,
                status="unbacked", note="reservation IP is not parseable",
            ))
            continue
        match = ip_index.get(norm)
        if match is None:
            entries.append(ReconcileEntry(
                reservation_ip=norm, identifier=ident,
                status="unbacked",
                note=(
                    "no IPAddress row for this IP in the linked subnet"
                    if scope.subnet_id else
                    "scope has no subnet_id — IPAM cross-check skipped"
                ),
            ))
            continue
        src = str(match.source.value if hasattr(match.source, "value") else match.source)
        if src == IpAddressSource.static.value:
            entries.append(ReconcileEntry(
                reservation_ip=norm, identifier=ident,
                status="collision", ip_address_id=str(match.id),
                ip_source=src,
                note="IPAddress is static — reservation would hand out an already-claimed IP",
            ))
        else:
            # dhcp or reservation source — counts as clean. dhcp
            # specifically means the lease has been observed; the
            # MAC on the IPAddress row should match the reservation
            # but we don't enforce that here (the reservation may
            # have been added pre-lease).
            entries.append(ReconcileEntry(
                reservation_ip=norm, identifier=ident,
                status="clean", ip_address_id=str(match.id),
                ip_source=src,
            ))

    counts = {"clean": 0, "collision": 0, "unbacked": 0}
    for e in entries:
        counts[e.status] = counts.get(e.status, 0) + 1

    return ReconcileReport(
        scope_id=str(scope.id),
        subnet_id=str(scope.subnet_id) if scope.subnet_id else None,
        total=len(entries),
        counts=counts,
        entries=entries,
    )
