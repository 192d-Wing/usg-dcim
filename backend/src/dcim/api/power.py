"""PDU outlets and power connections.

Endpoints support the rack power-chain UI: list outlets per PDU, attach a device
PSU to an outlet, detach. Outlets are auto-seeded when a PDU asset is created
elsewhere; this router exposes the read + connection-management surface.
"""

from __future__ import annotations

from uuid import UUID

from fastapi import APIRouter, Depends, HTTPException
from sqlalchemy import delete, select
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..errors import ConflictError, NotFoundError
from ..models.inventory import Asset, AssetKind
from ..models.power import Outlet, PowerConnection
from ..schemas.power import OutletOut, PowerConnectionCreate, PowerConnectionOut
from ..security import audit
from ..security.capabilities import INVENTORY_READ, INVENTORY_WRITE
from ..security.deps import Principal, require_capability

router = APIRouter(prefix="/power", tags=["power"])


@router.get("/pdus/{pdu_id}/outlets", response_model=list[OutletOut])
async def list_outlets(
    pdu_id: UUID,
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    pdu = await db.get(Asset, pdu_id)
    if pdu is None or pdu.kind != AssetKind.pdu:
        raise NotFoundError("PDU not found")

    outlets = (
        await db.execute(
            select(Outlet).where(Outlet.pdu_asset_id == pdu_id).order_by(Outlet.position)
        )
    ).scalars().all()
    conns = (
        await db.execute(
            select(PowerConnection).where(
                PowerConnection.outlet_id.in_([o.id for o in outlets]) if outlets else False
            )
        )
    ).scalars().all() if outlets else []
    by_outlet = {c.outlet_id: c for c in conns}

    return [
        OutletOut(
            id=o.id,
            pdu_asset_id=o.pdu_asset_id,
            position=o.position,
            label=o.label,
            phase=o.phase.value if o.phase and hasattr(o.phase, "value") else o.phase,
            max_amps=o.max_amps,
            receptacle=o.receptacle,
            connected=(
                {
                    "asset_id": str(by_outlet[o.id].asset_id),
                    "psu_index": by_outlet[o.id].psu_index,
                    "cord_color": by_outlet[o.id].cord_color,
                    "cord_length_m": by_outlet[o.id].cord_length_m,
                }
                if o.id in by_outlet else None
            ),
        )
        for o in outlets
    ]


@router.post("/outlets/{outlet_id}/connect", response_model=PowerConnectionOut, status_code=201)
async def connect_outlet(
    outlet_id: UUID,
    payload: PowerConnectionCreate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    outlet = await db.get(Outlet, outlet_id)
    if outlet is None:
        raise NotFoundError("outlet not found")
    asset = await db.get(Asset, payload.asset_id)
    if asset is None:
        raise NotFoundError("asset not found")

    # Outlet may only carry one connection (UNIQUE constraint backs this; check first for a
    # friendlier error)
    existing = (
        await db.execute(select(PowerConnection).where(PowerConnection.outlet_id == outlet_id))
    ).scalar_one_or_none()
    if existing is not None:
        raise ConflictError("outlet is already connected; disconnect it first")

    conn = PowerConnection(
        outlet_id=outlet_id,
        asset_id=payload.asset_id,
        psu_index=payload.psu_index,
        cord_color=payload.cord_color,
        cord_length_m=payload.cord_length_m,
    )
    db.add(conn)
    await db.flush()
    await audit.record(
        db, principal,
        action="power.connect", target_type="outlet", target_id=str(outlet_id),
        site_id=asset.site_id,
        metadata={"asset_id": str(payload.asset_id), "psu_index": payload.psu_index},
    )
    await db.commit()
    await db.refresh(conn)
    return conn


@router.delete("/outlets/{outlet_id}/connect", status_code=204)
async def disconnect_outlet(
    outlet_id: UUID,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    conn = (
        await db.execute(select(PowerConnection).where(PowerConnection.outlet_id == outlet_id))
    ).scalar_one_or_none()
    if conn is None:
        raise HTTPException(404, "no connection on this outlet")
    asset_id = conn.asset_id
    asset = await db.get(Asset, asset_id)
    await db.execute(delete(PowerConnection).where(PowerConnection.id == conn.id))
    await audit.record(
        db, principal,
        action="power.disconnect", target_type="outlet", target_id=str(outlet_id),
        site_id=asset.site_id if asset else None,
        metadata={"asset_id": str(asset_id)},
    )
    await db.commit()
    return None
