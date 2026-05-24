"""Kea Control Agent client + DHCP lease ingest.

Talks to a Kea Control Agent over HTTP. We use the `lease4-get-all` and
`lease6-get-all` commands so a single sync grabs every active lease in
one round-trip; for very large pools (>10k leases) Kea's pagination
commands would be a future swap.

Pure helpers (`parse_kea_lease`, `lease_valid_until`,
`match_lease_to_subnet`) are exported separately so the unit suite can
exercise the parsing + matching contracts without standing up a real
Kea server.
"""

from __future__ import annotations

import ipaddress
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from typing import Any

import httpx
import structlog
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from ..models.ipam import (
    DhcpServer,
    IPAddress,
    IpAddressSource,
    IpAddressStatus,
    Subnet,
)
from .ipam import address_in_network

log = structlog.get_logger("dcim.kea")


@dataclass
class ParsedLease:
    address: str
    mac: str | None
    hostname: str | None
    valid_until: datetime | None
    state: int  # 0=default, 1=declined, 2=expired-reclaimed (Kea convention)


# ---------- pure parsing helpers ----------


def lease_valid_until(
    cltt: int | None, valid_lft: int | None,
) -> datetime | None:
    """`cltt` (client-last-transmission-time) is unix-seconds; `valid-lft` is
    the lease lifetime in seconds. The lease expires at cltt + valid_lft.

    Falls back to None when either is missing — callers treat that as
    "expiry unknown, don't age out aggressively"."""
    if cltt is None or valid_lft is None:
        return None
    try:
        return datetime.fromtimestamp(int(cltt), tz=UTC) + timedelta(seconds=int(valid_lft))
    except (ValueError, OSError):
        return None


def parse_kea_lease(raw: dict) -> ParsedLease | None:
    """Parse a single Kea lease dict (from lease4-get-all or lease6-get-all).

    Returns None for leases we want to ignore — declined / expired-reclaimed
    states, or rows missing the address. Hostname is optional and Kea may
    omit MAC for IPv6 leases (lease6 has duid instead).
    """
    addr = raw.get("ip-address")
    if not addr:
        return None
    state = int(raw.get("state", 0))
    if state in (1, 2):  # declined or expired-reclaimed
        return None
    mac = raw.get("hw-address") or raw.get("duid")
    hostname = raw.get("hostname") or None
    if hostname == "":
        hostname = None
    return ParsedLease(
        address=str(addr),
        mac=mac,
        hostname=hostname,
        valid_until=lease_valid_until(raw.get("cltt"), raw.get("valid-lft")),
        state=state,
    )


def match_lease_to_subnet(address: str, subnets: list[Subnet]) -> Subnet | None:
    """Longest-prefix match — picks the most specific subnet that contains
    the address. Returns None when no subnet covers it. Subnets with
    unparseable prefixes are silently skipped; one bad row in the DB
    shouldn't break sync for everything else."""
    candidates: list[tuple[int, Subnet]] = []
    for s in subnets:
        try:
            net = ipaddress.ip_network(str(s.prefix), strict=False)
        except ValueError:
            continue
        if not address_in_network(address, net):
            continue
        candidates.append((net.prefixlen, s))
    if not candidates:
        return None
    candidates.sort(key=lambda t: -t[0])
    return candidates[0][1]


# ---------- I/O ----------


class KeaClient:
    """Thin wrapper around Kea's Control Agent JSON API."""

    def __init__(self, base_url: str, *, username: str | None = None, password: str | None = None):
        self.base_url = base_url.rstrip("/")
        self.auth = (username, password) if username and password else None

    async def _post(
        self,
        command: str,
        services: list[str],
        arguments: dict | None = None,
    ) -> Any:
        body: dict = {"command": command, "service": services}
        if arguments is not None:
            body["arguments"] = arguments
        async with httpx.AsyncClient(timeout=30.0, auth=self.auth) as client:
            resp = await client.post(self.base_url + "/", json=body)
            resp.raise_for_status()
            return resp.json()

    async def list_leases4(self) -> list[dict]:
        body = await self._post("lease4-get-all", ["dhcp4"])
        return _extract_leases(body)

    async def list_leases6(self) -> list[dict]:
        body = await self._post("lease6-get-all", ["dhcp6"])
        return _extract_leases(body)

    # ---- Subnet config commands (PR 74). Require Kea's `subnet_cmds`
    # hook library loaded on the target server. The library ships with
    # Kea ISC Premium / OSS-from-source builds; verify with
    # `config-get` → look for libdhcp_subnet_cmds.so in hooks-libraries.
    # All four methods return Kea's raw response list so the caller
    # can inspect the `result` code (0=success; 1=error; 2=unsupported;
    # 3=empty/not-found).

    async def subnet4_add(self, subnet: dict) -> Any:
        return await self._post(
            "subnet4-add", ["dhcp4"], arguments={"subnet4": [subnet]},
        )

    async def subnet4_update(self, subnet: dict) -> Any:
        return await self._post(
            "subnet4-update", ["dhcp4"], arguments={"subnet4": [subnet]},
        )

    async def subnet4_del(self, subnet_id: int) -> Any:
        return await self._post(
            "subnet4-del", ["dhcp4"], arguments={"id": subnet_id},
        )

    async def subnet4_get(self, subnet_id: int) -> Any:
        """Fetch the live subnet4 object from Kea (PR 75 drift check).
        Response carries the full subnet definition under
        `arguments.subnet4[0]`; result=3 means the subnet isn't in
        Kea even though DCIM has a kea_subnet_id for it (drifted away)."""
        return await self._post(
            "subnet4-get", ["dhcp4"], arguments={"id": subnet_id},
        )

    async def subnet6_add(self, subnet: dict) -> Any:
        return await self._post(
            "subnet6-add", ["dhcp6"], arguments={"subnet6": [subnet]},
        )

    async def subnet6_update(self, subnet: dict) -> Any:
        return await self._post(
            "subnet6-update", ["dhcp6"], arguments={"subnet6": [subnet]},
        )

    async def subnet6_del(self, subnet_id: int) -> Any:
        return await self._post(
            "subnet6-del", ["dhcp6"], arguments={"id": subnet_id},
        )

    async def subnet6_get(self, subnet_id: int) -> Any:
        """v6 twin of subnet4_get. Response carries the subnet under
        `arguments.subnet6[0]`."""
        return await self._post(
            "subnet6-get", ["dhcp6"], arguments={"id": subnet_id},
        )

    async def config_write(self, services: list[str]) -> Any:
        """Persist the running config to disk so it survives a Kea
        restart. PR 74 calls this after a successful subnetN-add/update
        so the change isn't volatile."""
        return await self._post("config-write", services)


def _extract_leases(resp: Any) -> list[dict]:
    """Kea returns a list of per-service responses; pluck `arguments.leases`."""
    if not isinstance(resp, list):
        return []
    out: list[dict] = []
    for entry in resp:
        if not isinstance(entry, dict):
            continue
        # result code 3 = empty — not an error.
        if entry.get("result") not in (0, 3):
            continue
        leases = entry.get("arguments", {}).get("leases", [])
        if isinstance(leases, list):
            out.extend(leases)
    return out


# ---------- sync orchestrator ----------


@dataclass
class SyncResult:
    server_id: str
    upserted: int
    skipped_no_subnet: int
    leases_seen: int
    error: str | None = None


async def sync_dhcp_server(db: AsyncSession, server: DhcpServer) -> SyncResult:
    """Pull every active lease from one Kea server and upsert IPAddress rows.

    Subnet matching is scoped to the server's fabric; a lease whose address
    isn't covered by any subnet in that fabric is counted as
    skipped_no_subnet and surfaced in the result. Errors per-server don't
    raise — they're recorded on the DhcpServer row so one broken Kea
    doesn't break the whole sweep.
    """
    started_at = datetime.now(UTC)
    client = KeaClient(server.kea_url, username=server.auth_username, password=server.auth_password)

    try:
        leases4 = await client.list_leases4()
    except Exception as exc:
        return await _record_failure(db, server, started_at, str(exc))
    try:
        leases6 = await client.list_leases6()
    except Exception:
        leases6 = []

    subnets = (
        await db.execute(select(Subnet).where(Subnet.fabric_id == server.fabric_id))
    ).scalars().all()
    subnets_list = list(subnets)

    upserted = 0
    skipped = 0
    seen = 0
    for raw in [*leases4, *leases6]:
        seen += 1
        parsed = parse_kea_lease(raw)
        if parsed is None:
            continue
        subnet = match_lease_to_subnet(parsed.address, subnets_list)
        if subnet is None:
            skipped += 1
            continue
        await _upsert_dhcp_lease(db, subnet=subnet, parsed=parsed)
        upserted += 1

    server.last_sync_at = started_at
    server.last_sync_status = "ok"
    server.last_sync_error = None
    server.last_sync_lease_count = upserted
    await db.commit()
    log.info(
        "kea_sync_ok",
        server=server.name, upserted=upserted, skipped=skipped, seen=seen,
    )
    return SyncResult(
        server_id=str(server.id),
        upserted=upserted,
        skipped_no_subnet=skipped,
        leases_seen=seen,
    )


async def _record_failure(
    db: AsyncSession, server: DhcpServer, started_at: datetime, error: str,
) -> SyncResult:
    server.last_sync_at = started_at
    server.last_sync_status = "error"
    server.last_sync_error = error[:2000]
    await db.commit()
    log.warning("kea_sync_failed", server=server.name, error=error)
    return SyncResult(
        server_id=str(server.id),
        upserted=0, skipped_no_subnet=0, leases_seen=0,
        error=error,
    )


async def _upsert_dhcp_lease(
    db: AsyncSession, *, subnet: Subnet, parsed: ParsedLease,
) -> None:
    """Insert or refresh a single dhcp-sourced IPAddress row.

    Never overwrites a static / reservation row — if a hand-allocated IP
    happens to match an active lease, we leave it alone and don't fight
    the operator. The DHCP source flag is the only one we touch in this
    path."""
    existing = (
        await db.execute(
            select(IPAddress).where(
                IPAddress.subnet_id == subnet.id,
                IPAddress.address == parsed.address,
            )
        )
    ).scalar_one_or_none()
    if existing is not None:
        if existing.source != IpAddressSource.dhcp:
            return  # leave hand-allocated rows alone
        existing.dhcp_mac = parsed.mac
        existing.dns_name = parsed.hostname or existing.dns_name
        existing.dhcp_lease_expires_at = parsed.valid_until
        existing.status = IpAddressStatus.active
        return
    db.add(IPAddress(
        subnet_id=subnet.id,
        address=parsed.address,
        source=IpAddressSource.dhcp,
        status=IpAddressStatus.active,
        dns_name=parsed.hostname,
        dhcp_mac=parsed.mac,
        dhcp_lease_expires_at=parsed.valid_until,
    ))


async def sync_all_servers(db: AsyncSession) -> dict:
    """Cron entry — walk every enabled DhcpServer and sync."""
    servers = (
        await db.execute(select(DhcpServer).where(DhcpServer.enabled.is_(True)))
    ).scalars().all()
    results: list[SyncResult] = []
    for s in servers:
        results.append(await sync_dhcp_server(db, s))
    return {
        "servers": len(servers),
        "upserted": sum(r.upserted for r in results),
        "skipped_no_subnet": sum(r.skipped_no_subnet for r in results),
        "errors": [r.error for r in results if r.error],
    }


# ---------- aging ----------


async def age_out_stale_dhcp(
    db: AsyncSession, *, now: datetime | None = None, grace_seconds: int = 3600,
) -> int:
    """Mark dhcp-sourced IPs whose lease lapsed more than `grace_seconds`
    ago as deprecated, then delete those that have been deprecated for
    a full day. Static + reservation rows are never touched."""
    now = now or datetime.now(UTC)
    cutoff_deprecate = now - timedelta(seconds=grace_seconds)
    cutoff_delete = now - timedelta(days=1)

    # Step 1: deprecate active dhcp rows whose lease has lapsed.
    expired = (
        await db.execute(
            select(IPAddress).where(
                IPAddress.source == IpAddressSource.dhcp,
                IPAddress.status == IpAddressStatus.active,
                IPAddress.dhcp_lease_expires_at.is_not(None),
                IPAddress.dhcp_lease_expires_at < cutoff_deprecate,
            )
        )
    ).scalars().all()
    for ip in expired:
        ip.status = IpAddressStatus.deprecated

    # Step 2: hard-delete dhcp rows that have been deprecated > 1 day —
    # they're noise at this point.
    stale = (
        await db.execute(
            select(IPAddress).where(
                IPAddress.source == IpAddressSource.dhcp,
                IPAddress.status == IpAddressStatus.deprecated,
                IPAddress.dhcp_lease_expires_at.is_not(None),
                IPAddress.dhcp_lease_expires_at < cutoff_delete,
            )
        )
    ).scalars().all()
    for ip in stale:
        await db.delete(ip)

    await db.commit()
    return len(expired) + len(stale)
