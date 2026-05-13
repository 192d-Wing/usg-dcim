"""Inventory CRUD — sites, buildings, rooms, rows, racks, assets.

Lists support page/page_size/sort/order and a few canonical filters; bulk endpoints
accept arrays for import flows. Every write is audited and scope-checked.
"""

from __future__ import annotations

import enum as _enum
from uuid import UUID

from fastapi import APIRouter, Depends, Query
from pydantic import BaseModel
from sqlalchemy import delete, func, or_, select
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..errors import NotFoundError, ValidationError
from ..models.inventory import (
    Asset,
    AssetFace,
    AssetMount,
    Building,
    Cable,
    LifecycleState,
    Rack,
    Region,
    Room,
    Row,
    Site,
)
from ..models.power import Outlet, PowerConnection
from ..schemas.common import BulkResult, Page, PageParams
from ..schemas.inventory import (
    AssetCreate,
    AssetOut,
    AssetUpdate,
    BuildingCreate,
    BuildingOut,
    CableCreate,
    CableOut,
    CableUpdate,
    RackCreate,
    RackOut,
    RackUpdate,
    RegionCreate,
    RegionOut,
    RegionUpdate,
    RoomCreate,
    RoomOut,
    RowCreate,
    RowOut,
    SiteCreate,
    SiteOut,
    SiteUpdate,
)
from ..security import audit
from ..security.deps import Principal, require_capability
from ..security.scope import enforce_site_scope, scope_filtered_site_ids
from ._pagination import empty_page, paginate


async def _enforce_site_scope(
    db: AsyncSession, principal: Principal, site_id: UUID | None, cap_code: str,
) -> None:
    """Thin local wrapper kept for call-site readability — the real
    work lives in security.scope.enforce_site_scope."""
    await enforce_site_scope(db, principal.capabilities, site_id, cap_code)

router = APIRouter(prefix="/inventory", tags=["inventory"])

_ASSET_NOT_FOUND = "asset not found"


# ----------------------- Regions -----------------------
@router.get("/regions", response_model=Page[RegionOut])
async def list_regions(
    params: PageParams = Depends(PageParams.from_query),
    principal: Principal = Depends(require_capability("inventory:regions:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(Region)
    # ABAC: restrict to regions that contain at least one in-scope site.
    in_scope = await scope_filtered_site_ids(
        db, principal.capabilities, "inventory:regions:read",
    )
    if in_scope is not None:
        if not in_scope:
            return empty_page(RegionOut, params)
        stmt = stmt.where(Region.id.in_(select(Site.region_id).where(Site.id.in_(in_scope))))
    return await paginate(db, stmt, model=Region, params=params, out_model=RegionOut)


@router.post("/regions", response_model=RegionOut, status_code=201)
async def create_region(
    payload: RegionCreate,
    principal: Principal = Depends(require_capability("inventory:regions:create")),
    db: AsyncSession = Depends(get_db),
):
    obj = Region(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(db, principal, action="region.create", target_type="region", target_id=str(obj.id))
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/regions/{region_id}", response_model=RegionOut)
async def update_region(
    region_id: UUID,
    payload: RegionUpdate,
    principal: Principal = Depends(require_capability("inventory:regions:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Region, region_id)
    if obj is None:
        raise NotFoundError("region not found")
    for k, v in payload.model_dump(exclude_unset=True).items():
        setattr(obj, k, v)
    await audit.record(db, principal, action="region.update", target_type="region", target_id=str(region_id))
    await db.commit()
    return obj


# ----------------------- Sites -----------------------
@router.get("/sites", response_model=Page[SiteOut])
async def list_sites(
    params: PageParams = Depends(PageParams.from_query),
    region_id: UUID | None = Query(None),
    majcom: str | None = Query(None),
    enclave: str | None = Query(None),
    organization: str | None = Query(None),
    lifecycle_state: str | None = Query(None),
    principal: Principal = Depends(require_capability("inventory:sites:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(Site)
    if region_id is not None:
        stmt = stmt.where(Site.region_id == region_id)
    if majcom is not None:
        stmt = stmt.where(Site.majcom == majcom)
    if enclave is not None:
        stmt = stmt.where(Site.enclave == enclave)
    if organization is not None:
        stmt = stmt.where(Site.organization == organization)
    if lifecycle_state is not None:
        stmt = stmt.where(Site.lifecycle_state == lifecycle_state)

    # ABAC: push the scope filter into SQL so pagination counts are
    # correct (the prior post-pagination filter broke page totals).
    in_scope = await scope_filtered_site_ids(
        db, principal.capabilities, "inventory:sites:read",
    )
    if in_scope is not None:
        if not in_scope:
            return empty_page(SiteOut, params)
        stmt = stmt.where(Site.id.in_(in_scope))

    return await paginate(db, stmt, model=Site, params=params, out_model=SiteOut)


@router.post("/sites", response_model=SiteOut, status_code=201)
async def create_site(
    payload: SiteCreate,
    principal: Principal = Depends(require_capability("inventory:sites:create")),
    db: AsyncSession = Depends(get_db),
):
    obj = Site(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(db, principal, action="site.create", target_type="site",
                       target_id=str(obj.id), site_id=obj.id)
    await db.commit()
    await db.refresh(obj)
    return obj


@router.get("/sites/{site_id}", response_model=SiteOut)
async def get_site(
    site_id: UUID,
    principal: Principal = Depends(require_capability("inventory:sites:read")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Site, site_id)
    if obj is None:
        raise NotFoundError("site not found")
    await _enforce_site_scope(db, principal, obj.id, "inventory:sites:read")
    return obj


@router.patch("/sites/{site_id}", response_model=SiteOut)
async def update_site(
    site_id: UUID,
    payload: SiteUpdate,
    principal: Principal = Depends(require_capability("inventory:sites:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Site, site_id)
    if obj is None:
        raise NotFoundError("site not found")
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(db, principal, action="site.update", target_type="site",
                       target_id=str(site_id), site_id=site_id, diff=diff)
    await db.commit()
    return obj


# ----------------------- Buildings / Rooms / Rows -----------------------
@router.get("/buildings", response_model=Page[BuildingOut])
async def list_buildings(
    params: PageParams = Depends(PageParams.from_query),
    site_id: UUID | None = Query(None),
    principal: Principal = Depends(require_capability("inventory:buildings:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(Building)
    if site_id is not None:
        stmt = stmt.where(Building.site_id == site_id)
    in_scope = await scope_filtered_site_ids(
        db, principal.capabilities, "inventory:buildings:read",
    )
    if in_scope is not None:
        if not in_scope:
            return empty_page(BuildingOut, params)
        stmt = stmt.where(Building.site_id.in_(in_scope))
    return await paginate(db, stmt, model=Building, params=params, out_model=BuildingOut)


@router.get("/rooms", response_model=Page[RoomOut])
async def list_rooms(
    params: PageParams = Depends(PageParams.from_query),
    building_id: UUID | None = Query(None),
    principal: Principal = Depends(require_capability("inventory:rooms:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(Room)
    if building_id is not None:
        stmt = stmt.where(Room.building_id == building_id)
    in_scope = await scope_filtered_site_ids(
        db, principal.capabilities, "inventory:rooms:read",
    )
    if in_scope is not None:
        if not in_scope:
            return empty_page(RoomOut, params)
        stmt = stmt.where(Room.building_id.in_(
            select(Building.id).where(Building.site_id.in_(in_scope))
        ))
    return await paginate(db, stmt, model=Room, params=params, out_model=RoomOut)


@router.get("/rows", response_model=Page[RowOut])
async def list_rows(
    params: PageParams = Depends(PageParams.from_query),
    room_id: UUID | None = Query(None),
    principal: Principal = Depends(require_capability("inventory:rows:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(Row)
    if room_id is not None:
        stmt = stmt.where(Row.room_id == room_id)
    in_scope = await scope_filtered_site_ids(
        db, principal.capabilities, "inventory:rows:read",
    )
    if in_scope is not None:
        if not in_scope:
            return empty_page(RowOut, params)
        stmt = stmt.where(Row.room_id.in_(
            select(Room.id).join(Building, Room.building_id == Building.id)
            .where(Building.site_id.in_(in_scope))
        ))
    return await paginate(db, stmt, model=Row, params=params, out_model=RowOut)


@router.post("/buildings", response_model=BuildingOut, status_code=201)
async def create_building(
    payload: BuildingCreate,
    principal: Principal = Depends(require_capability("inventory:buildings:create")),
    db: AsyncSession = Depends(get_db),
):
    obj = Building(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(db, principal, action="building.create", target_type="building",
                       target_id=str(obj.id), site_id=obj.site_id)
    await db.commit()
    await db.refresh(obj)
    return obj


@router.post("/rooms", response_model=RoomOut, status_code=201)
async def create_room(
    payload: RoomCreate,
    principal: Principal = Depends(require_capability("inventory:rooms:create")),
    db: AsyncSession = Depends(get_db),
):
    obj = Room(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(db, principal, action="room.create", target_type="room", target_id=str(obj.id))
    await db.commit()
    await db.refresh(obj)
    return obj


@router.post("/rows", response_model=RowOut, status_code=201)
async def create_row(
    payload: RowCreate,
    principal: Principal = Depends(require_capability("inventory:rows:create")),
    db: AsyncSession = Depends(get_db),
):
    obj = Row(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(db, principal, action="row.create", target_type="row", target_id=str(obj.id))
    await db.commit()
    await db.refresh(obj)
    return obj


# ----------------------- Racks -----------------------
@router.get("/racks", response_model=Page[RackOut])
async def list_racks(
    params: PageParams = Depends(PageParams.from_query),
    site_id: UUID | None = Query(None),
    row_id: UUID | None = Query(None),
    principal: Principal = Depends(require_capability("inventory:racks:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(Rack)
    if site_id is not None:
        stmt = stmt.where(Rack.site_id == site_id)
    if row_id is not None:
        stmt = stmt.where(Rack.row_id == row_id)
    in_scope = await scope_filtered_site_ids(
        db, principal.capabilities, "inventory:racks:read",
    )
    if in_scope is not None:
        if not in_scope:
            return empty_page(RackOut, params)
        stmt = stmt.where(Rack.site_id.in_(in_scope))
    return await paginate(db, stmt, model=Rack, params=params, out_model=RackOut)


@router.get("/racks/{rack_id}", response_model=RackOut)
async def get_rack(
    rack_id: UUID,
    principal: Principal = Depends(require_capability("inventory:racks:read")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Rack, rack_id)
    if obj is None:
        raise NotFoundError("rack not found")
    await _enforce_site_scope(db, principal, obj.site_id, "inventory:racks:read")
    return obj


@router.post("/racks", response_model=RackOut, status_code=201)
async def create_rack(
    payload: RackCreate,
    principal: Principal = Depends(require_capability("inventory:racks:create")),
    db: AsyncSession = Depends(get_db),
):
    obj = Rack(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(db, principal, action="rack.create", target_type="rack",
                       target_id=str(obj.id), site_id=obj.site_id)
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/racks/{rack_id}", response_model=RackOut)
async def update_rack(
    rack_id: UUID,
    payload: RackUpdate,
    principal: Principal = Depends(require_capability("inventory:racks:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Rack, rack_id)
    if obj is None:
        raise NotFoundError("rack not found")
    diff = payload.model_dump(exclude_unset=True)

    # If shrinking u_height, refuse if any placed asset would fall outside the new envelope.
    new_u = diff.get("u_height")
    if new_u is not None and new_u < obj.u_height:
        rows = (
            await db.execute(
                select(Asset.name, Asset.rack_position_u, Asset.rack_units)
                .where(
                    Asset.rack_id == rack_id,
                    Asset.rack_position_u.is_not(None),
                )
            )
        ).all()
        offenders = [
            {
                "name": r.name,
                "u": r.rack_position_u,
                "size": r.rack_units or 1,
                "top": (r.rack_position_u or 0) + (r.rack_units or 1) - 1,
            }
            for r in rows
            if (r.rack_position_u or 0) + (r.rack_units or 1) - 1 > new_u
        ]
        if offenders:
            raise ValidationError(
                f"Cannot shrink rack to {new_u}U; {len(offenders)} device(s) would be orphaned. "
                f"Move or remove them first.",
                details={"orphans": offenders, "requested_u_height": new_u},
            )

    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(db, principal, action="rack.update", target_type="rack",
                       target_id=str(rack_id), site_id=obj.site_id, diff=diff)
    await db.commit()
    await db.refresh(obj)
    return obj


# ----------------------- Assets -----------------------
@router.get("/assets", response_model=Page[AssetOut])
async def list_assets(
    params: PageParams = Depends(PageParams.from_query),
    site_id: UUID | None = Query(None),
    rack_id: UUID | None = Query(None),
    kind: str | None = Query(None),
    lifecycle_state: str | None = Query(None),
    serial: str | None = Query(None),
    hostname: str | None = Query(None),
    principal: Principal = Depends(require_capability("inventory:assets:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(Asset)
    if site_id is not None:
        stmt = stmt.where(Asset.site_id == site_id)
    if rack_id is not None:
        stmt = stmt.where(Asset.rack_id == rack_id)
    if kind is not None:
        stmt = stmt.where(Asset.kind == kind)
    if lifecycle_state is not None:
        stmt = stmt.where(Asset.lifecycle_state == lifecycle_state)
    if serial is not None:
        stmt = stmt.where(Asset.serial == serial)
    if hostname is not None:
        stmt = stmt.where(Asset.hostname == hostname)
    in_scope = await scope_filtered_site_ids(
        db, principal.capabilities, "inventory:assets:read",
    )
    if in_scope is not None:
        if not in_scope:
            return empty_page(AssetOut, params)
        stmt = stmt.where(Asset.site_id.in_(in_scope))
    return await paginate(db, stmt, model=Asset, params=params, out_model=AssetOut)


@router.get("/assets/{asset_id}", response_model=AssetOut)
async def get_asset(
    asset_id: UUID,
    principal: Principal = Depends(require_capability("inventory:assets:read")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Asset, asset_id)
    if obj is None:
        raise NotFoundError(_ASSET_NOT_FOUND)
    await _enforce_site_scope(db, principal, obj.site_id, "inventory:assets:read")
    return obj


@router.post("/assets", response_model=AssetOut, status_code=201)
async def create_asset(
    payload: AssetCreate,
    principal: Principal = Depends(require_capability("inventory:assets:create")),
    db: AsyncSession = Depends(get_db),
):
    from ..models.inventory import AssetKind, PduSide
    from ..models.power import Outlet
    obj = Asset(**payload.model_dump())
    db.add(obj)
    await db.flush()
    # Auto-seed outlets for new PDUs so the power-chain UI has slots to bind to.
    # Heuristic: 24 outlets, half on side A, half on side B, alternating C13 receptacles.
    if obj.kind == AssetKind.pdu:
        for i in range(1, 25):
            db.add(Outlet(
                pdu_asset_id=obj.id,
                position=i,
                label=f"{i:02d}",
                phase=PduSide.a if i <= 12 else PduSide.b,
                max_amps=10,
                receptacle="C13",
            ))
    await audit.record(db, principal, action="asset.create", target_type="asset",
                       target_id=str(obj.id), site_id=obj.site_id)
    await db.commit()
    await db.refresh(obj)
    return obj


_PLACEMENT_KEYS = frozenset({"rack_id", "rack_position_u", "rack_units", "face", "mount"})


def _v(v: object) -> object:
    return v.value if isinstance(v, _enum.Enum) else v


def _resolved_placement(obj: Asset, diff: dict) -> tuple:
    return (
        diff.get("rack_id", obj.rack_id),
        diff.get("rack_position_u", obj.rack_position_u),
        diff.get("rack_units", obj.rack_units) or 1,
        _v(diff.get("face", obj.face)),
        _v(diff.get("mount", obj.mount)),
    )


async def _check_u_grid_fit(
    db: AsyncSession, asset_id: UUID, target_rack: Rack,
    position_u: int, units: int, face: str,
) -> None:
    top = position_u + units - 1
    if position_u < 1 or top > target_rack.u_height:
        raise ValidationError(
            f"Placement U{position_u}-U{top} overflows {target_rack.u_height}U rack.",
            details={
                "rack_u_height": target_rack.u_height,
                "requested_u": position_u,
                "requested_top": top,
            },
        )
    others = (
        await db.execute(
            select(Asset.id, Asset.name, Asset.rack_position_u, Asset.rack_units)
            .where(
                Asset.rack_id == target_rack.id,
                Asset.id != asset_id,
                Asset.mount == AssetMount.rack,
                Asset.face == AssetFace(face),
                Asset.rack_position_u.is_not(None),
            )
        )
    ).all()
    collisions = [
        {"id": str(o.id), "name": o.name, "u": o.rack_position_u, "size": o.rack_units or 1}
        for o in others
        if top >= o.rack_position_u and position_u <= o.rack_position_u + (o.rack_units or 1) - 1
    ]
    if collisions:
        raise ValidationError(
            f"Placement U{position_u}-U{top} collides with "
            f"{len(collisions)} device(s) on the {face} face.",
            details={"collisions": collisions, "face": face},
        )


async def _validate_placement_and_resolve_target(
    db: AsyncSession, obj: Asset, diff: dict
) -> Rack | None:
    """Validate fit/overlap if the diff would change asset placement.

    Returns the target Rack (when rack_id is set) so callers can sync derived
    fields (e.g. site_id on cross-site moves). Raises ValidationError on
    overflow or slot collision.
    """
    rack_id, position_u, units, face, mount = _resolved_placement(obj, diff)
    if rack_id is None:
        return None
    target_rack = await db.get(Rack, rack_id)
    if target_rack is None:
        raise ValidationError(f"target rack {rack_id} not found")
    if mount == AssetMount.rack.value and position_u is not None:
        await _check_u_grid_fit(db, obj.id, target_rack, position_u, units, face)
    return target_rack


@router.patch("/assets/{asset_id}", response_model=AssetOut)
async def update_asset(
    asset_id: UUID,
    payload: AssetUpdate,
    principal: Principal = Depends(require_capability("inventory:assets:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Asset, asset_id)
    if obj is None:
        raise NotFoundError(_ASSET_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)

    if _PLACEMENT_KEYS & diff.keys():
        target_rack = await _validate_placement_and_resolve_target(db, obj, diff)
        # Cross-site move: keep Asset.site_id in sync with the target rack so
        # the client doesn't have to compute it on a site→rack→U pick.
        if (
            "rack_id" in diff
            and target_rack is not None
            and target_rack.site_id != obj.site_id
        ):
            obj.site_id = target_rack.site_id

    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(db, principal, action="asset.update", target_type="asset",
                       target_id=str(asset_id), site_id=obj.site_id, diff=diff)
    await db.commit()
    await db.refresh(obj)
    return obj


class _DecommissionPayload(BaseModel):
    sanitization_note: str | None = None
    reason: str | None = None


class _DecommissionImpact(BaseModel):
    """Pre-flight or post-action impact summary. `consumer_drops` are
    PowerConnections where the asset is the consumer; `pdu_drops` are
    PowerConnections served by the asset's outlets (only non-zero for
    PDUs). `downstream_assets` lists distinct asset names that lose
    power on the PDU side so the operator sees the blast radius
    before they commit."""
    consumer_drops: int = 0
    pdu_drops: int = 0
    downstream_assets: list[str] = []


class _DecommissionResult(BaseModel):
    asset: AssetOut
    impact: _DecommissionImpact


async def _decommission_impact(db: AsyncSession, asset_id: UUID) -> _DecommissionImpact:
    """Compute the drop counts + downstream-asset names a decommission
    would produce, without mutating. Pure read query — safe to call
    from the preview endpoint AND right before the actual delete to
    populate the result envelope."""
    consumer_q = await db.execute(
        select(func.count()).select_from(PowerConnection)
        .where(PowerConnection.asset_id == asset_id)
    )
    consumer_drops = int(consumer_q.scalar() or 0)
    # PDU side: PowerConnection.outlet_id → Outlet.pdu_asset_id == asset.
    # Join to Asset on the CONSUMER side of those connections so we can
    # report the names of downstream devices that would lose power.
    downstream_rows = (await db.execute(
        select(Asset.name)
        .select_from(PowerConnection)
        .join(Outlet, Outlet.id == PowerConnection.outlet_id)
        .join(Asset, Asset.id == PowerConnection.asset_id)
        .where(Outlet.pdu_asset_id == asset_id)
        .distinct()
    )).all()
    downstream_assets = sorted({row[0] for row in downstream_rows if row[0]})
    pdu_count_q = await db.execute(
        select(func.count()).select_from(PowerConnection)
        .join(Outlet, Outlet.id == PowerConnection.outlet_id)
        .where(Outlet.pdu_asset_id == asset_id)
    )
    pdu_drops = int(pdu_count_q.scalar() or 0)
    return _DecommissionImpact(
        consumer_drops=consumer_drops,
        pdu_drops=pdu_drops,
        downstream_assets=downstream_assets,
    )


@router.get(
    "/assets/{asset_id}/decommission/preview",
    response_model=_DecommissionImpact,
)
async def preview_decommission(
    asset_id: UUID,
    _: Principal = Depends(require_capability("inventory:assets:read")),
    db: AsyncSession = Depends(get_db),
):
    """Pre-flight summary the operator sees before committing the
    decommission. Returns the same impact shape the POST endpoint
    populates on success so the UI can render one consistent view.

    Read-only — no mutation, no audit entry. Operators who can read
    inventory can preview; the actual decommission still requires
    `inventory:assets:update`."""
    obj = await db.get(Asset, asset_id)
    if obj is None:
        raise NotFoundError(_ASSET_NOT_FOUND)
    return await _decommission_impact(db, asset_id)


@router.post("/assets/{asset_id}/decommission", response_model=_DecommissionResult)
async def decommission_asset(
    asset_id: UUID,
    payload: _DecommissionPayload,
    principal: Principal = Depends(require_capability("inventory:assets:update")),
    db: AsyncSession = Depends(get_db),
):
    """Mark an asset decommissioned and drop its power connections.

    Drops connections both ways: where the asset is the consumer, and
    where it's a PDU whose outlets carry connections to other devices.
    The asset itself stays in place so historical reports keep
    resolving — flip to `retired` later to fully archive. The response
    includes the same impact shape `/preview` returns so the UI can
    surface "dropped N connections (and these downstream assets)" in
    the success toast.
    """
    obj = await db.get(Asset, asset_id)
    if obj is None:
        raise NotFoundError(_ASSET_NOT_FOUND)
    if obj.lifecycle_state == LifecycleState.decommissioned:
        raise ValidationError("asset is already decommissioned")

    # Compute impact BEFORE the deletes so the result envelope carries
    # accurate counts + downstream-asset names. Calling after the
    # deletes would return zeros.
    impact = await _decommission_impact(db, asset_id)

    (
        await db.execute(
            delete(PowerConnection).where(PowerConnection.asset_id == asset_id)
        )
    )
    (
        await db.execute(
            delete(PowerConnection).where(
                PowerConnection.outlet_id.in_(
                    select(Outlet.id).where(Outlet.pdu_asset_id == asset_id).scalar_subquery()
                )
            )
        )
    )

    prior_state = obj.lifecycle_state.value if hasattr(obj.lifecycle_state, "value") else obj.lifecycle_state
    obj.lifecycle_state = LifecycleState.decommissioned
    await audit.record(
        db, principal, action="asset.decommission", target_type="asset",
        target_id=str(asset_id), site_id=obj.site_id,
        diff={"lifecycle_state": {"from": prior_state, "to": "decommissioned"}},
        metadata={
            "sanitization_note": payload.sanitization_note,
            "reason": payload.reason,
            "dropped_power_connections": impact.consumer_drops + impact.pdu_drops,
            "downstream_assets": impact.downstream_assets,
        },
    )
    await db.commit()
    await db.refresh(obj)
    return _DecommissionResult(asset=AssetOut.model_validate(obj), impact=impact)


@router.post("/assets/bulk", response_model=BulkResult)
async def bulk_upsert_assets(
    payload: list[AssetCreate],
    principal: Principal = Depends(require_capability("inventory:bulk:execute")),
    db: AsyncSession = Depends(get_db),
):
    """Upsert by (manufacturer, serial). Use for site-wide imports."""
    result = BulkResult()
    for item in payload:
        try:
            existing = None
            if item.serial and item.manufacturer:
                existing = (
                    await db.execute(
                        select(Asset).where(
                            Asset.serial == item.serial, Asset.manufacturer == item.manufacturer
                        )
                    )
                ).scalar_one_or_none()
            if existing is None:
                db.add(Asset(**item.model_dump()))
                result.inserted += 1
            else:
                for k, v in item.model_dump().items():
                    setattr(existing, k, v)
                result.updated += 1
        except Exception as e:
            result.failed += 1
            result.errors.append({"serial": item.serial, "error": str(e)})
    await audit.record(
        db, principal, action="asset.bulk_upsert", target_type="asset",
        metadata={"inserted": result.inserted, "updated": result.updated, "failed": result.failed},
    )
    await db.commit()
    return result


# ----------------------- Cables -----------------------
_CABLE_NOT_FOUND = "cable not found"


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
    principal: Principal = Depends(require_capability("inventory:cables:read")),
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
        db, principal.capabilities, "inventory:cables:read",
    )
    if in_scope is not None:
        if not in_scope:
            return empty_page(CableOut, params)
        stmt = stmt.where(Cable.site_id.in_(in_scope))
    return await paginate(db, stmt, model=Cable, params=params, out_model=CableOut)


@router.get("/cables/{cable_id}", response_model=CableOut)
async def get_cable(
    cable_id: UUID,
    principal: Principal = Depends(require_capability("inventory:cables:read")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Cable, cable_id)
    if obj is None:
        raise NotFoundError(_CABLE_NOT_FOUND)
    await _enforce_site_scope(db, principal, obj.site_id, "inventory:cables:read")
    return obj


@router.post("/cables", response_model=CableOut, status_code=201)
async def create_cable(
    payload: CableCreate,
    principal: Principal = Depends(require_capability("inventory:cables:create")),
    db: AsyncSession = Depends(get_db),
):
    a, b = await _validate_cable_endpoints(db, payload.a_asset_id, payload.b_asset_id)
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
    principal: Principal = Depends(require_capability("inventory:cables:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Cable, cable_id)
    if obj is None:
        raise NotFoundError(_CABLE_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    new_a = diff.get("a_asset_id", obj.a_asset_id)
    new_b = diff.get("b_asset_id", obj.b_asset_id)
    new_a_port = diff.get("a_port", obj.a_port)
    new_b_port = diff.get("b_port", obj.b_port)
    a, b = await _validate_cable_endpoints(db, new_a, new_b)
    if "a_asset_id" in diff:
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
    principal: Principal = Depends(require_capability("inventory:cables:delete")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Cable, cable_id)
    if obj is None:
        raise NotFoundError(_CABLE_NOT_FOUND)
    site_id = obj.site_id
    await db.execute(delete(Cable).where(Cable.id == cable_id))
    await audit.record(
        db, principal, action="cable.delete", target_type="cable",
        target_id=str(cable_id), site_id=site_id,
    )
    await db.commit()
