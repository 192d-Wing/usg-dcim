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

from sqlalchemy.ext.asyncio import AsyncSession

from ..models.ipam import (
    IPAddress,
    IpAddressRole,
    IpAddressSource,
    IpAddressStatus,
)


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


def _normalize_mac(mac: str | None) -> str | None:
    """Canonicalize a MAC for comparison (PR 88).

    Accepts colon, dash, dot, or no separator. Lowercases the hex.
    Returns None if the input isn't 12 hex characters once stripped.
    Used both to compare reservation MAC against IPAddress.dhcp_mac
    and to store on upsert in a stable form.
    """
    if not mac:
        return None
    cleaned = "".join(c for c in mac.lower() if c in "0123456789abcdef")
    if len(cleaned) != 12:
        return None
    return ":".join(cleaned[i:i + 2] for i in range(0, 12, 2))


def _normalize_duid(duid: str | None) -> str | None:
    """Canonicalize a DUID for comparison (PR 94).

    RFC 8415 caps DUIDs at 128 octets (1-128 bytes); canonicalized
    form is lowercase hex joined by colons. Accepts colon, dash, or
    no separator on input. Returns None if the input has fewer than
    2 hex characters (1 byte minimum per RFC) or more than 256
    (128 octets max).

    Unlike MAC (always 6 octets), DUIDs are variable-length, so the
    normalizer accepts any even hex count in the valid range and
    canonicalizes the separator.
    """
    if not duid:
        return None
    cleaned = "".join(c for c in duid.lower() if c in "0123456789abcdef")
    if len(cleaned) < 2 or len(cleaned) > 256 or len(cleaned) % 2 != 0:
        return None
    return ":".join(cleaned[i:i + 2] for i in range(0, len(cleaned), 2))


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
            continue
        # PR 88 — MAC binding check (v4). If the reservation declares
        # a MAC and the matching IPAddress carries one too, they must
        # agree. Either side NULL = check skipped (don't false-alarm
        # on missing data).
        res_mac = _normalize_mac(r.get("mac"))
        row_mac = _normalize_mac(getattr(match, "dhcp_mac", None))
        if res_mac and row_mac and res_mac != row_mac:
            entries.append(ReconcileEntry(
                reservation_ip=norm, identifier=ident,
                status="mac_mismatch", ip_address_id=str(match.id),
                ip_source=src,
                note=(
                    f"reservation expects mac={res_mac} but IPAddress "
                    f"has mac={row_mac}"
                ),
            ))
            continue
        # PR 94 — DUID binding check (v6). Parallel to the MAC check;
        # separate status so operators can distinguish v4 mac drift
        # from v6 duid drift in the report.
        res_duid = _normalize_duid(r.get("duid"))
        row_duid = _normalize_duid(getattr(match, "dhcp_duid", None))
        if res_duid and row_duid and res_duid != row_duid:
            entries.append(ReconcileEntry(
                reservation_ip=norm, identifier=ident,
                status="duid_mismatch", ip_address_id=str(match.id),
                ip_source=src,
                note=(
                    f"reservation expects duid={res_duid} but IPAddress "
                    f"has duid={row_duid}"
                ),
            ))
            continue
        # dhcp or reservation source with matching (or unknown)
        # identifier on whichever family applies.
        entries.append(ReconcileEntry(
            reservation_ip=norm, identifier=ident,
            status="clean", ip_address_id=str(match.id),
            ip_source=src,
        ))

    counts = {
        "clean": 0, "collision": 0, "unbacked": 0,
        "mac_mismatch": 0, "duid_mismatch": 0,
    }
    for e in entries:
        counts[e.status] = counts.get(e.status, 0) + 1

    return ReconcileReport(
        scope_id=str(scope.id),
        subnet_id=str(scope.subnet_id) if scope.subnet_id else None,
        total=len(entries),
        counts=counts,
        entries=entries,
    )


# ---------- mutating sync (PR 85) ----------


@dataclass
class SyncReport:
    scope_id: str
    subnet_id: str | None
    upserted: int      # new IPAddress rows created
    promoted: int      # existing dhcp-source rows flipped to reservation
    skipped_collision: int  # static-source rows left alone
    skipped_clean: int      # already source=reservation (or dhcp) — no change
    skipped_mac_mismatch: int  # PR 88 — lease MAC ≠ reservation MAC
    skipped_duid_mismatch: int  # PR 94 — lease DUID ≠ reservation DUID
    skipped_no_subnet: int  # scope.subnet_id is NULL
    entries: list[dict]


async def sync_reservations(
    db: AsyncSession,
    scope,
    ip_rows: list[IPAddress],
) -> SyncReport:
    """Materialize the scope's reservations into IPAM (PR 85).

    Walks the same matching logic as reconcile_scope, but mutates:

      * unbacked  → INSERT IPAddress(source=reservation, status=reserved,
                    subnet_id=scope.subnet_id, address=<ip>)
      * dhcp      → UPDATE source=reservation, status=reserved
                    (promote — the lease is already there; tag it as
                    a planned reservation so future syncs don't churn)
      * collision → skip (static-source rows belong to the operator;
                    they resolve manually)
      * reservation → skip (already where we want it)
      * no subnet_id → skip everything (can't insert without a parent)

    Caller owns the commit. Returns counts + per-entry decisions so
    the audit log can detail what happened.
    """
    if scope.subnet_id is None:
        return SyncReport(
            scope_id=str(scope.id), subnet_id=None,
            upserted=0, promoted=0, skipped_collision=0,
            skipped_clean=0, skipped_mac_mismatch=0,
            skipped_duid_mismatch=0,
            skipped_no_subnet=len(scope.reservations_json or []),
            entries=[
                {"reservation_ip": (r.get("ip") or ""), "decision": "skipped_no_subnet"}
                for r in (scope.reservations_json or [])
            ],
        )

    ip_index: dict[str, IPAddress] = {}
    for row in ip_rows:
        key = _normalize_ip(str(row.address).split("/")[0])
        if key is not None:
            ip_index[key] = row

    upserted = promoted = skipped_collision = skipped_clean = 0
    skipped_mac_mismatch = skipped_duid_mismatch = 0
    entries: list[dict] = []
    for r in scope.reservations_json or []:
        raw_ip = r.get("ip", "")
        norm = _normalize_ip(raw_ip)
        if norm is None:
            entries.append({"reservation_ip": raw_ip, "decision": "skipped_unparseable"})
            continue
        res_mac = _normalize_mac(r.get("mac"))
        res_duid = _normalize_duid(r.get("duid"))
        match = ip_index.get(norm)
        if match is None:
            # Unbacked — insert a fresh reservation row. PR 88/94:
            # carry the reservation's MAC and/or DUID onto the new
            # row so future syncs treat it as already-bound and the
            # row roundtrips through reconcile cleanly.
            row = IPAddress(
                subnet_id=scope.subnet_id,
                address=norm,
                role=IpAddressRole.data,
                status=IpAddressStatus.reserved,
                source=IpAddressSource.reservation,
                dhcp_mac=res_mac,
                dhcp_duid=res_duid,
            )
            db.add(row)
            upserted += 1
            entries.append({
                "reservation_ip": norm, "decision": "upserted",
            })
            continue
        src = str(match.source.value if hasattr(match.source, "value") else match.source)
        if src == IpAddressSource.static.value:
            skipped_collision += 1
            entries.append({
                "reservation_ip": norm, "decision": "skipped_collision",
                "ip_address_id": str(match.id),
            })
            continue
        # PR 88 — MAC mismatch refuses to promote (v4 path).
        row_mac = _normalize_mac(getattr(match, "dhcp_mac", None))
        if res_mac and row_mac and res_mac != row_mac:
            skipped_mac_mismatch += 1
            entries.append({
                "reservation_ip": norm, "decision": "skipped_mac_mismatch",
                "ip_address_id": str(match.id),
                "reservation_mac": res_mac, "row_mac": row_mac,
            })
            continue
        # PR 94 — DUID mismatch refuses to promote (v6 path). Same
        # rationale as MAC mismatch — promoting silently would mask
        # the lease binding conflict.
        row_duid = _normalize_duid(getattr(match, "dhcp_duid", None))
        if res_duid and row_duid and res_duid != row_duid:
            skipped_duid_mismatch += 1
            entries.append({
                "reservation_ip": norm, "decision": "skipped_duid_mismatch",
                "ip_address_id": str(match.id),
                "reservation_duid": res_duid, "row_duid": row_duid,
            })
            continue
        if src == IpAddressSource.dhcp.value:
            # Lease materialized; tag as reservation so it stops
            # looking like a transient allocation. Backfill MAC/DUID
            # when the reservation knows it but the lease didn't
            # record one — keeps reconcile and future syncs agreeing.
            match.source = IpAddressSource.reservation
            match.status = IpAddressStatus.reserved
            if res_mac and not row_mac:
                match.dhcp_mac = res_mac
            if res_duid and not row_duid:
                match.dhcp_duid = res_duid
            promoted += 1
            entries.append({
                "reservation_ip": norm, "decision": "promoted",
                "ip_address_id": str(match.id),
            })
        else:
            skipped_clean += 1
            entries.append({
                "reservation_ip": norm, "decision": "skipped_clean",
                "ip_address_id": str(match.id),
            })

    await db.flush()
    return SyncReport(
        scope_id=str(scope.id),
        subnet_id=str(scope.subnet_id),
        upserted=upserted,
        promoted=promoted,
        skipped_collision=skipped_collision,
        skipped_clean=skipped_clean,
        skipped_mac_mismatch=skipped_mac_mismatch,
        skipped_duid_mismatch=skipped_duid_mismatch,
        skipped_no_subnet=0,
        entries=entries,
    )
