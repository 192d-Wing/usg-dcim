"""Global search across assets, racks, sites, PDUs, hostnames, serials, IPs."""

from __future__ import annotations

import ipaddress

from fastapi import APIRouter, Depends, Query
from sqlalchemy import or_, select
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..models.inventory import Asset, Rack, Site
from ..models.ipam import Fabric, IPAddress, Subnet, Vrf
from ..security.capabilities import INVENTORY_READ
from ..security.deps import Principal, require_capability

router = APIRouter(prefix="/search", tags=["search"])


def _looks_like_ip(q: str) -> str | None:
    """Return the canonical text of `q` if it parses as an IPv4 or IPv6
    address, else None. Strips a trailing /prefix so users can paste
    "10.0.0.5/24" and still hit the address row."""
    raw = q.strip().split("/", 1)[0]
    try:
        return str(ipaddress.ip_address(raw))
    except ValueError:
        return None


async def _bulk_by_id(db: AsyncSession, model, ids: set) -> dict:
    """Fetch every row whose id is in `ids`, return {id: row}. Empty
    input → empty result without hitting the DB."""
    if not ids:
        return {}
    rows = (
        await db.execute(select(model).where(model.id.in_(ids)))
    ).scalars().all()
    return {r.id: r for r in rows}


def _enum_v(v):
    return v.value if hasattr(v, "value") else v


def _ip_search_row(
    ip: IPAddress,
    subnets: dict, vrfs: dict, fabrics: dict, assets: dict,
) -> dict:
    s = subnets.get(ip.subnet_id)
    v = vrfs.get(s.vrf_id) if s else None
    f = fabrics.get(s.fabric_id) if s else None
    a = assets.get(ip.asset_id) if ip.asset_id else None
    return {
        "id": str(ip.id),
        "address": str(ip.address).split("/", 1)[0],
        "role": _enum_v(ip.role),
        "status": _enum_v(ip.status),
        "source": _enum_v(ip.source),
        "dns_name": ip.dns_name,
        "subnet_id": str(s.id) if s else None,
        "subnet_prefix": str(s.prefix) if s else None,
        "vrf_id": str(v.id) if v else None,
        "vrf_name": v.name if v else None,
        "fabric_id": str(f.id) if f else None,
        "fabric_name": f.name if f else None,
        "asset_id": str(a.id) if a else None,
        "asset_name": a.name if a else None,
    }


async def _ip_search(db: AsyncSession, addr: str, limit: int) -> list[dict]:
    """Resolve an IP query into enriched IPAddress rows.

    Exact match on IPAddress.address, then bulk-fetch the containing
    subnet, VRF, fabric, and bound asset so the operator gets the full
    "what is this IP?" picture in one round-trip.
    """
    ip_rows = (
        await db.execute(
            select(IPAddress).where(IPAddress.address == addr).limit(limit)
        )
    ).scalars().all()
    if not ip_rows:
        return []

    subnets = await _bulk_by_id(db, Subnet, {ip.subnet_id for ip in ip_rows})
    vrfs = await _bulk_by_id(db, Vrf, {s.vrf_id for s in subnets.values()})
    fabrics = await _bulk_by_id(db, Fabric, {s.fabric_id for s in subnets.values()})
    assets = await _bulk_by_id(
        db, Asset, {ip.asset_id for ip in ip_rows if ip.asset_id is not None},
    )
    return [_ip_search_row(ip, subnets, vrfs, fabrics, assets) for ip in ip_rows]


@router.get("")
async def global_search(
    q: str = Query(min_length=2, max_length=128),
    limit: int = Query(25, ge=1, le=200),
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    pat = f"%{q}%"
    sites = (
        await db.execute(
            select(Site).where(or_(Site.name.ilike(pat), Site.code.ilike(pat))).limit(limit)
        )
    ).scalars().all()
    racks = (
        await db.execute(
            select(Rack).where(
                or_(Rack.name.ilike(pat), Rack.code.ilike(pat), Rack.serial.ilike(pat)),
            ).limit(limit)
        )
    ).scalars().all()
    assets = (
        await db.execute(
            select(Asset).where(
                or_(
                    Asset.name.ilike(pat),
                    Asset.hostname.ilike(pat),
                    Asset.serial.ilike(pat),
                    Asset.mgmt_ip.ilike(pat),
                )
            ).limit(limit)
        )
    ).scalars().all()

    addr = _looks_like_ip(q)
    ips = await _ip_search(db, addr, limit) if addr else []

    return {
        "query": q,
        "parsed_ip": addr,
        "results": {
            "sites": [{"id": str(s.id), "name": s.name, "code": s.code} for s in sites],
            "racks": [{"id": str(r.id), "name": r.name, "site_id": str(r.site_id)} for r in racks],
            "assets": [
                {
                    "id": str(a.id),
                    "name": a.name,
                    "hostname": a.hostname,
                    "serial": a.serial,
                    "kind": a.kind.value if hasattr(a.kind, "value") else a.kind,
                    "site_id": str(a.site_id),
                }
                for a in assets
            ],
            "ips": ips,
        },
    }
