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


# Postgres BIGINT (and orjson, and most JSON consumers) cap at 2^63 - 1.
# IPv6 prefixes wider than ~/65 blow past that — a /48 has 2^80 hosts.
# Past this number the exact count is operationally meaningless anyway,
# so we cap and let the UI show "huge" / 0% utilization.
_INT64_MAX = (1 << 63) - 1


def find_free_prefixes_in_supernet(
    supernet_prefix: CidrLike,
    new_prefix_size: int,
    allocated_prefixes: Iterable[CidrLike],
    *,
    limit: int = 50,
) -> list[str]:
    """Walk a supernet's possible sub-prefixes and return ones that don't
    overlap any already-allocated subnet.

    Used by the carving free-space finder — "find me a /24 inside
    10.0.0.0/8 that isn't already a subnet" or "find me a /64 inside
    2001:db8::/48 to allocate". Caller passes the existing subnet
    prefixes for the supernet.

    Bounded by `limit` so a large IPv6 search (a /48 splits into 65536
    /64s) doesn't iterate the whole space when the operator only needs
    a handful of candidates.

    Returns CIDR strings sorted by network address ascending. Skips the
    candidate if its prefix length doesn't fit (parent /24, asked /16),
    if family doesn't match, or if it overlaps an existing allocation.
    """
    parent = parse_network(supernet_prefix)
    if new_prefix_size <= parent.prefixlen:
        return []
    if (parent.version == 4 and new_prefix_size > 32) or (
        parent.version == 6 and new_prefix_size > 128
    ):
        return []

    occupied = []
    for p in allocated_prefixes:
        try:
            occupied.append(parse_network(p))
        except (ValueError, TypeError):
            continue
    # Walk parent.subnets() in order; binary-searching occupied per candidate
    # keeps this O((C+O) log O) rather than O(C * O) when there are many
    # existing subnets. Occupied is sorted by network address.
    occupied.sort(key=lambda n: int(n.network_address))

    out: list[str] = []
    for cand in parent.subnets(new_prefix=new_prefix_size):
        if any(cand.overlaps(o) for o in occupied):
            continue
        out.append(str(cand))
        if len(out) >= limit:
            break
    return out


def network_capacity(network: CidrLike) -> int:
    """Number of allocatable host addresses in a network, capped at int64.

    Small prefixes get the full count (mirrors next_free_address — /31
    and /127 are point-to-point so we count both addresses; /32 and /128
    return 1). Very wide IPv6 prefixes get clamped at 2^63-1 so the
    response can be JSON-encoded without overflow."""
    net = parse_network(network)
    if (net.version == 4 and net.prefixlen >= 31) or (net.version == 6 and net.prefixlen >= 127):
        raw = net.num_addresses
    else:
        raw = max(0, net.num_addresses - 2)
    return min(raw, _INT64_MAX)


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


def assert_purpose_compatible(
    *, supernet_purpose: str | None, subnet_purpose: str | None,
) -> None:
    """Subnet purpose must match its parent supernet's purpose (or stay unset).

    A supernet without a purpose set imposes no constraint on its subnets —
    that's how operators run a generic /8 with mixed-purpose subnets carved
    out. As soon as the parent picks a purpose ("data"), every subnet under
    it has to either match or stay unlabeled."""
    if supernet_purpose is None or subnet_purpose is None:
        return
    if subnet_purpose != supernet_purpose:
        raise ValidationError(
            f"subnet purpose {subnet_purpose!r} doesn't match parent supernet "
            f"purpose {supernet_purpose!r}",
            details={
                "supernet_purpose": supernet_purpose,
                "subnet_purpose": subnet_purpose,
            },
        )


async def assert_supernet_purpose_change_safe(
    db: AsyncSession, *, supernet_id: UUID, new_purpose: str | None,
) -> None:
    """When a supernet's purpose changes, refuse if any existing subnet under
    it would no longer match. Operators have to either reset the conflicting
    subnets first or unset the supernet's purpose."""
    if new_purpose is None:
        return
    rows = (
        await db.execute(select(Subnet).where(Subnet.supernet_id == supernet_id))
    ).scalars().all()
    bad = [s for s in rows if s.purpose is not None and s.purpose != new_purpose]
    if bad:
        raise ConflictError(
            f"cannot set supernet purpose to {new_purpose!r}: "
            f"{len(bad)} child subnet(s) have a different purpose",
            details={
                "new_purpose": new_purpose,
                "conflicts": [
                    {"subnet_id": str(s.id), "prefix": str(s.prefix), "purpose": s.purpose}
                    for s in bad[:10]
                ],
            },
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
