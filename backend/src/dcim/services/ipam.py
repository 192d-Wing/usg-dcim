"""IPAM helpers — pure CIDR math + DB-backed validation.

The pure functions live up here so the unit suite can drive containment,
overlap, and next-available logic without spinning up Postgres. The
async helpers below them touch the DB to enforce the invariants the
roadmap calls out:

  Supernet ⊇ Subnet ⊇ IPAddress
  No two Subnets overlap inside the same (fabric, vrf)
  No two Supernets overlap inside the same (fabric, vrf)

Address space *can* repeat across VRFs — that's the point of having
VRFs in the first place.
"""

from __future__ import annotations

import ipaddress
from collections.abc import Iterable
from uuid import UUID

from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from ..errors import ConflictError, ValidationError
from ..models.ipam import IPAddress, Subnet, Supernet

# ---------- pure CIDR helpers ----------

CidrLike = str | ipaddress.IPv4Network | ipaddress.IPv6Network


def parse_network(value: CidrLike) -> ipaddress.IPv4Network | ipaddress.IPv6Network:
    """Coerce string-or-network into an ip_network. Strict=False so we accept
    `10.0.0.5/24` and snap it to the network address."""
    if isinstance(value, (ipaddress.IPv4Network, ipaddress.IPv6Network)):
        return value
    return ipaddress.ip_network(str(value), strict=False)


def parse_address(value: str) -> ipaddress.IPv4Address | ipaddress.IPv6Address:
    """Strip any /prefix the caller might have left on, then parse."""
    raw = str(value).split("/", 1)[0]
    return ipaddress.ip_address(raw)


def cidr_contains(parent: CidrLike, child: CidrLike) -> bool:
    """True iff `child` is a subset of `parent`. False on family mismatch."""
    p, c = parse_network(parent), parse_network(child)
    if p.version != c.version:
        return False
    return c.subnet_of(p)


def cidrs_overlap(a: CidrLike, b: CidrLike) -> bool:
    """True iff the two networks share any addresses. False on family mismatch."""
    pa, pb = parse_network(a), parse_network(b)
    if pa.version != pb.version:
        return False
    return pa.overlaps(pb)


def address_in_network(addr: str, network: CidrLike) -> bool:
    """Whether an INET address belongs to a CIDR network. Family-aware."""
    a = parse_address(addr)
    n = parse_network(network)
    if a.version != n.version:
        return False
    return a in n


def next_free_address(network: CidrLike, used: Iterable[str]) -> str | None:
    """First host address in `network` that's not in `used`.

    Skips network + broadcast for IPv4 prefixes /31 and shorter. For /31 and
    /32 (and IPv6 /127 and /128) we hand back every address — no host-bit
    reservation makes sense at point-to-point or single-host prefix lengths.
    """
    net = parse_network(network)
    used_set = {str(parse_address(u)) for u in used}
    if (net.version == 4 and net.prefixlen >= 31) or (net.version == 6 and net.prefixlen >= 127):
        candidates: Iterable = net
    else:
        candidates = net.hosts()
    for ip in candidates:
        s = str(ip)
        if s not in used_set:
            return s
    return None


def network_capacity(network: CidrLike) -> int:
    """Number of allocatable host addresses in a network. Mirrors the
    semantics of next_free_address — small prefixes get the full count."""
    net = parse_network(network)
    if (net.version == 4 and net.prefixlen >= 31) or (net.version == 6 and net.prefixlen >= 127):
        return net.num_addresses
    return max(0, net.num_addresses - 2)


# ---------- DB-backed validation ----------


async def assert_supernet_unique_in_vrf(
    db: AsyncSession, *, fabric_id: UUID, vrf_id: UUID, prefix: str,
    exclude_id: UUID | None = None,
) -> None:
    """Refuse if any existing Supernet in the same (fabric, vrf) overlaps."""
    rows = (
        await db.execute(
            select(Supernet).where(
                Supernet.fabric_id == fabric_id, Supernet.vrf_id == vrf_id,
            )
        )
    ).scalars().all()
    for r in rows:
        if exclude_id and r.id == exclude_id:
            continue
        if cidrs_overlap(r.prefix, prefix):
            raise ConflictError(
                f"supernet {prefix} overlaps existing supernet {r.prefix} in this VRF",
                details={"conflicting_supernet_id": str(r.id), "conflicting_prefix": str(r.prefix)},
            )


async def assert_subnet_inside_supernet(
    db: AsyncSession, *, supernet_id: UUID, prefix: str,
) -> Supernet:
    """Look up the parent Supernet and refuse if `prefix` isn't inside it."""
    parent = await db.get(Supernet, supernet_id)
    if parent is None:
        raise ValidationError(f"supernet {supernet_id} not found")
    if not cidr_contains(parent.prefix, prefix):
        raise ValidationError(
            f"subnet {prefix} is not contained in supernet {parent.prefix}",
            details={"supernet_prefix": str(parent.prefix), "subnet_prefix": str(prefix)},
        )
    return parent


async def assert_subnet_unique_in_vrf(
    db: AsyncSession, *, fabric_id: UUID, vrf_id: UUID, prefix: str,
    exclude_id: UUID | None = None,
) -> None:
    rows = (
        await db.execute(
            select(Subnet).where(Subnet.fabric_id == fabric_id, Subnet.vrf_id == vrf_id)
        )
    ).scalars().all()
    for r in rows:
        if exclude_id and r.id == exclude_id:
            continue
        if cidrs_overlap(r.prefix, prefix):
            raise ConflictError(
                f"subnet {prefix} overlaps existing subnet {r.prefix} in this VRF",
                details={"conflicting_subnet_id": str(r.id), "conflicting_prefix": str(r.prefix)},
            )


async def assert_address_in_subnet(
    db: AsyncSession, *, subnet_id: UUID, address: str,
) -> Subnet:
    subnet = await db.get(Subnet, subnet_id)
    if subnet is None:
        raise ValidationError(f"subnet {subnet_id} not found")
    if not address_in_network(address, subnet.prefix):
        raise ValidationError(
            f"address {address} is not contained in subnet {subnet.prefix}",
            details={"subnet_prefix": str(subnet.prefix), "address": str(address)},
        )
    return subnet


async def used_addresses_in_subnet(db: AsyncSession, subnet_id: UUID) -> list[str]:
    rows = (
        await db.execute(select(IPAddress.address).where(IPAddress.subnet_id == subnet_id))
    ).scalars().all()
    return [str(a).split("/", 1)[0] for a in rows]
