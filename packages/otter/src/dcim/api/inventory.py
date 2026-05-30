"""Inventory CRUD — cables only (rest moved to otter-go).

regions/sites/buildings/rooms/rows/racks/assets moved to otter-go
(internal/{regions,sites,locations,racks,assets}). Cables stays on
Python until cables PATCH ports — Go has GET/POST/DELETE but not
PATCH. The umbrella chart routes /api/v1/inventory/cables to Python
via a longer-prefix ingress rule; the broader /api/v1/inventory rule
sends everything else to otter-go.
"""

from __future__ import annotations

from uuid import UUID

from fastapi import APIRouter, Depends, Query
from sqlalchemy import delete, or_, select
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..errors import NotFoundError, ValidationError
from ..models.inventory import (
    Asset,
    Cable,
)
from ..schemas.common import Page, PageParams
from ..schemas.inventory import (
    CableCreate,
    CableOut,
    CableUpdate,
)
from ..security import audit
from ..security.deps import Principal, require_capability
from ..security.scope import enforce_site_scope, scope_filtered_site_ids
from ._pagination import empty_page, paginate

router = APIRouter(prefix="/inventory", tags=["inventory"])

# ----------------------- Cables -----------------------
_CABLE_NOT_FOUND = "cable not found"
_CAP_CABLES_CREATE = "inventory:cables:create"
_CAP_CABLES_READ = "inventory:cables:read"
_CAP_CABLES_UPDATE = "inventory:cables:update"
_CAP_CABLES_DELETE = "inventory:cables:delete"


async def _validate_cable_endpoints(
    db: AsyncSession, a_asset_id: UUID, b_asset_id: UUID,
) -> tuple[Asset, Asset]:
    if a_asset_id == b_asset_id:
        raise ValidationError("a-end and b-end must be different assets")
    a = await db.get(Asset, a_asset_id)
    b = await db.get(Asset, b_asset_id)
    if a is None:
        raise ValidationError(f"a-end asset {a_asset_id} not found")
    if b is None:
        raise ValidationError(f"b-end asset {b_asset_id} not found")
    return a, b


def _validate_port_in_range(asset: Asset, port: str | None, end: str) -> None:
    """If the asset declares a port_count, the port must be a 1..N integer."""
    if not port or not asset.port_count:
        return
    try:
        n = int(port)
    except ValueError as exc:
        raise ValidationError(
            f"{end}-end port {port!r} on {asset.name} must be a number 1-{asset.port_count}.",
        ) from exc
    if n < 1 or n > asset.port_count:
        raise ValidationError(
            f"{end}-end port {n} on {asset.name} is outside the 1-{asset.port_count} port range.",
            details={"asset_id": str(asset.id), "port_count": asset.port_count, "requested_port": n},
        )


async def _validate_port_unused(
    db: AsyncSession, asset_id: UUID, port: str | None,
    *, exclude_cable_id: UUID | None = None, end: str,
) -> None:
    """One cable per physical port: refuse if (asset_id, port) is already claimed."""
    if not port:
        return
    stmt = select(Cable.id, Cable.label).where(
        or_(
            (Cable.a_asset_id == asset_id) & (Cable.a_port == port),
            (Cable.b_asset_id == asset_id) & (Cable.b_port == port),
        )
    )
    if exclude_cable_id is not None:
        stmt = stmt.where(Cable.id != exclude_cable_id)
    row = (await db.execute(stmt)).first()
    if row is not None:
        raise ValidationError(
            f"{end}-end port {port} is already in use by another cable.",
            details={"conflicting_cable_id": str(row.id), "conflicting_label": row.label},
        )


@router.get("/cables", response_model=Page[CableOut])
async def list_cables(
    params: PageParams = Depends(PageParams.from_query),
    site_id: UUID | None = Query(None),
    rack_id: UUID | None = Query(None),
    asset_id: UUID | None = Query(None),
    principal: Principal = Depends(require_capability(_CAP_CABLES_READ)),
    db: AsyncSession = Depends(get_db),
):
    """List cables. `rack_id` matches cables touching any asset in that rack."""
    stmt = select(Cable)
    if site_id is not None:
        stmt = stmt.where(Cable.site_id == site_id)
    if asset_id is not None:
        stmt = stmt.where(or_(Cable.a_asset_id == asset_id, Cable.b_asset_id == asset_id))
    if rack_id is not None:
        rack_assets = select(Asset.id).where(Asset.rack_id == rack_id).scalar_subquery()
        stmt = stmt.where(or_(Cable.a_asset_id.in_(rack_assets), Cable.b_asset_id.in_(rack_assets)))
    in_scope = await scope_filtered_site_ids(
        db, principal.capabilities, _CAP_CABLES_READ,
    )
    if in_scope is not None:
        if not in_scope:
            return empty_page(CableOut, params)
        stmt = stmt.where(Cable.site_id.in_(in_scope))
    return await paginate(db, stmt, model=Cable, params=params, out_model=CableOut)


@router.get("/cables/{cable_id}", response_model=CableOut)
async def get_cable(
    cable_id: UUID,
    principal: Principal = Depends(require_capability(_CAP_CABLES_READ)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Cable, cable_id)
    if obj is None:
        raise NotFoundError(_CABLE_NOT_FOUND)
    await enforce_site_scope(db, principal.capabilities, obj.site_id, _CAP_CABLES_READ)
    return obj


@router.post("/cables", response_model=CableOut, status_code=201)
async def create_cable(
    payload: CableCreate,
    principal: Principal = Depends(require_capability(_CAP_CABLES_CREATE)),
    db: AsyncSession = Depends(get_db),
):
    a, b = await _validate_cable_endpoints(db, payload.a_asset_id, payload.b_asset_id)
    # ABAC: the resolved cable.site_id (a-end's site) must be in the
    # principal's create-scope. Without this, a SiteAdmin scoped to
    # site A could POST a cable whose a-end sits in site B; the row
    # would land with site_id=B and audit fires under the wrong owner.
    await enforce_site_scope(
        db, principal.capabilities, a.site_id, _CAP_CABLES_CREATE,
    )
    _validate_port_in_range(a, payload.a_port, end="a")
    _validate_port_in_range(b, payload.b_port, end="b")
    await _validate_port_unused(db, payload.a_asset_id, payload.a_port, end="a")
    await _validate_port_unused(db, payload.b_asset_id, payload.b_port, end="b")
    data = payload.model_dump()
    # site_id always tracks the a-end asset's site so cross-site cables stay
    # discoverable from one consistent owner.
    data["site_id"] = a.site_id
    obj = Cable(**data)
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="cable.create", target_type="cable",
        target_id=str(obj.id), site_id=obj.site_id,
        metadata={"a_asset_id": str(obj.a_asset_id), "b_asset_id": str(obj.b_asset_id)},
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/cables/{cable_id}", response_model=CableOut)
async def update_cable(
    cable_id: UUID,
    payload: CableUpdate,
    principal: Principal = Depends(require_capability(_CAP_CABLES_UPDATE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Cable, cable_id)
    if obj is None:
        raise NotFoundError(_CABLE_NOT_FOUND)
    # Existing cable must be in scope before any mutation.
    await enforce_site_scope(
        db, principal.capabilities, obj.site_id, _CAP_CABLES_UPDATE,
    )
    diff = payload.model_dump(exclude_unset=True)
    new_a = diff.get("a_asset_id", obj.a_asset_id)
    new_b = diff.get("b_asset_id", obj.b_asset_id)
    new_a_port = diff.get("a_port", obj.a_port)
    new_b_port = diff.get("b_port", obj.b_port)
    a, b = await _validate_cable_endpoints(db, new_a, new_b)
    if "a_asset_id" in diff:
        # Re-pointing a-end shifts the cable to a new site; that site
        # must also be in the principal's update-scope.
        await enforce_site_scope(
            db, principal.capabilities, a.site_id, _CAP_CABLES_UPDATE,
        )
        obj.site_id = a.site_id
    # Re-validate ports whenever an end or a port string would change. Ranges
    # depend on the resolved endpoint asset's port_count; uniqueness depends on
    # the new (asset_id, port) pair. Either change can invalidate the cable.
    placement_changed = bool({"a_asset_id", "a_port", "b_asset_id", "b_port"} & diff.keys())
    if placement_changed:
        _validate_port_in_range(a, new_a_port, end="a")
        _validate_port_in_range(b, new_b_port, end="b")
        await _validate_port_unused(db, new_a, new_a_port, exclude_cable_id=cable_id, end="a")
        await _validate_port_unused(db, new_b, new_b_port, exclude_cable_id=cable_id, end="b")
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="cable.update", target_type="cable",
        target_id=str(cable_id), site_id=obj.site_id, diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/cables/{cable_id}", status_code=204)
async def delete_cable(
    cable_id: UUID,
    principal: Principal = Depends(require_capability(_CAP_CABLES_DELETE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Cable, cable_id)
    if obj is None:
        raise NotFoundError(_CABLE_NOT_FOUND)
    await enforce_site_scope(
        db, principal.capabilities, obj.site_id, _CAP_CABLES_DELETE,
    )
    site_id = obj.site_id
    await db.execute(delete(Cable).where(Cable.id == cable_id))
    await audit.record(
        db, principal, action="cable.delete", target_type="cable",
        target_id=str(cable_id), site_id=site_id,
    )
    await db.commit()
