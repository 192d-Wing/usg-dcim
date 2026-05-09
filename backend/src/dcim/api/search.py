"""Global search across assets, racks, sites, PDUs, hostnames, serials."""

from __future__ import annotations

from fastapi import APIRouter, Depends, Query
from sqlalchemy import or_, select
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..models.inventory import Asset, Rack, Site
from ..security.capabilities import INVENTORY_READ
from ..security.deps import Principal, require_capability

router = APIRouter(prefix="/search", tags=["search"])


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

    return {
        "query": q,
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
        },
    }
