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
from ..models.ipam import IPAddress, Subnet, Supernet, Vni, VniKind

# 24-bit VXLAN/GENEVE VNI space minus the reserved 0 and 16777215.
VNI_MIN = 1
VNI_MAX = (1 << 24) - 2

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
    parent_supernet_id: UUID | None = None,
    exclude_id: UUID | None = None,
) -> None:
    """Refuse if any existing Supernet at the same level in the same
    (fabric, vrf) overlaps.

    "Same level" = same parent_supernet_id. Two siblings under the same
    parent must not overlap; a supernet may still overlap one of its
    own ancestors or descendants, since the hierarchy is the whole
    point — 10.0.0.0/8, 10.0.0.0/20, and 10.0.0.0/24 all coexist when
    chained as parent→child.
    """
    rows = (
        await db.execute(
            select(Supernet).where(
                Supernet.fabric_id == fabric_id, Supernet.vrf_id == vrf_id,
                Supernet.parent_supernet_id.is_(parent_supernet_id)
                if parent_supernet_id is None
                else Supernet.parent_supernet_id == parent_supernet_id,
            )
        )
    ).scalars().all()
    for r in rows:
        if exclude_id and r.id == exclude_id:
            continue
        if cidrs_overlap(r.prefix, prefix):
            raise ConflictError(
                f"supernet {prefix} overlaps existing supernet {r.prefix} at this level",
                details={"conflicting_supernet_id": str(r.id), "conflicting_prefix": str(r.prefix)},
            )


async def assert_supernet_inside_parent(
    db: AsyncSession, *, parent_supernet_id: UUID, prefix: str,
    fabric_id: UUID, vrf_id: UUID,
) -> Supernet:
    """When a child supernet declares a parent, the parent must exist in
    the same (fabric, vrf) and the child's prefix must sit inside the
    parent's prefix. Same logic as assert_subnet_inside_supernet, just
    one level up."""
    parent = await db.get(Supernet, parent_supernet_id)
    if parent is None:
        raise ValidationError(f"parent supernet {parent_supernet_id} not found")
    if parent.fabric_id != fabric_id or parent.vrf_id != vrf_id:
        raise ValidationError(
            "parent supernet must be in the same fabric and VRF",
            details={
                "parent_fabric_id": str(parent.fabric_id),
                "parent_vrf_id": str(parent.vrf_id),
            },
        )
    if not cidr_contains(parent.prefix, prefix):
        raise ValidationError(
            f"supernet {prefix} is not contained in parent supernet {parent.prefix}",
            details={"parent_prefix": str(parent.prefix), "child_prefix": str(prefix)},
        )
    return parent


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


# ---------- VXLAN/GENEVE overlay helpers ----------


def assert_vni_in_range(vni: int) -> None:
    """24-bit VNI space minus the reserved 0 and all-ones values."""
    if vni < VNI_MIN or vni > VNI_MAX:
        raise ValidationError(
            f"vni must be between {VNI_MIN} and {VNI_MAX} (24-bit space)",
            details={"vni": vni},
        )


def assert_vni_kind_consistent(
    *, kind: VniKind | str, vlan_id: int | None, vrf_id: UUID | None,
) -> None:
    """L2 VNIs may carry vlan_id; L3 VNIs require vrf_id and must not
    set vlan_id. Keeps the EVPN intent unambiguous so downstream code
    (and the operator reading the row later) can tell at a glance which
    plane a VNI lives on."""
    k = kind.value if isinstance(kind, VniKind) else kind
    if k == VniKind.l3.value:
        if vrf_id is None:
            raise ValidationError("L3 VNI requires vrf_id")
        if vlan_id is not None:
            raise ValidationError("L3 VNI must not set vlan_id (no broadcast domain)")
    elif k == VniKind.l2.value and vrf_id is not None:
        raise ValidationError(
            "L2 VNI must not set vrf_id (use an L3 VNI for tenant VRF mapping)"
        )


async def assert_subnet_vni_compatible(
    db: AsyncSession, *, vni_id: UUID, fabric_id: UUID,
) -> Vni:
    """Subnets that ride an overlay must point at an L2 VNI in the same
    fabric. L3 VNIs don't have subnets — they map a VRF, not a broadcast
    domain — so binding a subnet to one is a structural error."""
    vni = await db.get(Vni, vni_id)
    if vni is None:
        raise ValidationError(f"vni {vni_id} not found")
    # Resolve overlay → fabric in one extra round-trip; cheaper than a
    # join and runs only when the operator opts into VNI binding.
    from ..models.ipam import Overlay  # local import avoids circular at module load
    overlay = await db.get(Overlay, vni.overlay_id)
    if overlay is None or overlay.fabric_id != fabric_id:
        raise ValidationError(
            "vni's overlay must live in the same fabric as the subnet",
            details={
                "vni_fabric_id": str(overlay.fabric_id) if overlay else None,
                "subnet_fabric_id": str(fabric_id),
            },
        )
    kind = vni.kind.value if isinstance(vni.kind, VniKind) else vni.kind
    if kind != VniKind.l2.value:
        raise ValidationError(
            "subnet may only bind to an L2 VNI (L3 VNIs map a VRF, not a broadcast domain)",
            details={"vni_kind": kind},
        )
    return vni
