"""Alert listing, acknowledge, rule CRUD, and maintenance-window CRUD."""

from __future__ import annotations

from datetime import UTC, datetime
from uuid import UUID

from fastapi import APIRouter, Depends, Query
from sqlalchemy import delete, select
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..errors import NotFoundError, ValidationError
from ..models.alerts import Alert, AlertRule, AlertState, MaintenanceWindow
from ..schemas.alerts import (
    AlertAck, AlertOut, AlertRuleCreate, AlertRuleOut,
    MaintenanceWindowCreate, MaintenanceWindowOut, MaintenanceWindowUpdate,
)
from ..schemas.common import Page, PageParams
from ..security import audit
from ..security.capabilities import ALERTS_ACK, ALERTS_CONFIGURE, ALERTS_READ
from ..security.deps import Principal, require_capability
from ._pagination import paginate

router = APIRouter(prefix="/alerts", tags=["alerts"])


@router.get("", response_model=Page[AlertOut])
async def list_alerts(
    params: PageParams = Depends(PageParams.from_query),
    site_id: UUID | None = Query(None),
    state: AlertState | None = Query(None),
    severity: str | None = Query(None),
    _: Principal = Depends(require_capability(ALERTS_READ)),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(Alert)
    if site_id is not None:
        stmt = stmt.where(Alert.site_id == site_id)
    if state is not None:
        stmt = stmt.where(Alert.state == state)
    if severity is not None:
        stmt = stmt.where(Alert.severity == severity)
    return await paginate(db, stmt, model=Alert, params=params, out_model=AlertOut)


@router.post("/{alert_id}/ack", response_model=AlertOut)
async def ack_alert(
    alert_id: UUID,
    payload: AlertAck,
    principal: Principal = Depends(require_capability(ALERTS_ACK)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Alert, alert_id)
    if obj is None:
        raise NotFoundError("alert not found")
    obj.state = AlertState.acknowledged
    obj.acked_by = principal.label
    obj.acked_at = datetime.now(UTC)
    await audit.record(
        db, principal, action="alert.ack", target_type="alert", target_id=str(alert_id),
        site_id=obj.site_id, metadata={"note": payload.note},
    )
    await db.commit()
    return obj


@router.get("/rules", response_model=Page[AlertRuleOut])
async def list_rules(
    params: PageParams = Depends(PageParams.from_query),
    _: Principal = Depends(require_capability(ALERTS_READ)),
    db: AsyncSession = Depends(get_db),
):
    return await paginate(db, select(AlertRule), model=AlertRule, params=params, out_model=AlertRuleOut)


@router.post("/rules", response_model=AlertRuleOut, status_code=201)
async def create_rule(
    payload: AlertRuleCreate,
    principal: Principal = Depends(require_capability(ALERTS_CONFIGURE)),
    db: AsyncSession = Depends(get_db),
):
    obj = AlertRule(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(db, principal, action="alert_rule.create", target_type="alert_rule",
                       target_id=str(obj.id), site_id=obj.site_scope_id)
    await db.commit()
    await db.refresh(obj)
    return obj


# ----------------------- Maintenance windows -----------------------
_MW_NOT_FOUND = "maintenance window not found"


def _validate_window(starts_at: datetime, ends_at: datetime) -> None:
    if ends_at <= starts_at:
        raise ValidationError(
            "ends_at must be after starts_at",
            details={"starts_at": starts_at.isoformat(), "ends_at": ends_at.isoformat()},
        )


@router.get("/maintenance-windows", response_model=Page[MaintenanceWindowOut])
async def list_maintenance_windows(
    params: PageParams = Depends(PageParams.from_query),
    site_id: UUID | None = Query(None),
    active_at: datetime | None = Query(
        None, description="Return only windows covering this instant.",
    ),
    upcoming: bool = Query(False, description="Only return windows ending in the future."),
    _: Principal = Depends(require_capability(ALERTS_READ)),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(MaintenanceWindow)
    if site_id is not None:
        stmt = stmt.where(MaintenanceWindow.site_id == site_id)
    if active_at is not None:
        stmt = stmt.where(
            MaintenanceWindow.starts_at <= active_at,
            MaintenanceWindow.ends_at >= active_at,
        )
    if upcoming:
        stmt = stmt.where(MaintenanceWindow.ends_at >= datetime.now(UTC))
    return await paginate(
        db, stmt, model=MaintenanceWindow, params=params, out_model=MaintenanceWindowOut,
    )


@router.post("/maintenance-windows", response_model=MaintenanceWindowOut, status_code=201)
async def create_maintenance_window(
    payload: MaintenanceWindowCreate,
    principal: Principal = Depends(require_capability(ALERTS_CONFIGURE)),
    db: AsyncSession = Depends(get_db),
):
    _validate_window(payload.starts_at, payload.ends_at)
    obj = MaintenanceWindow(**payload.model_dump(), created_by=principal.label)
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="maintenance_window.create",
        target_type="maintenance_window", target_id=str(obj.id), site_id=obj.site_id,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.get("/maintenance-windows/{window_id}", response_model=MaintenanceWindowOut)
async def get_maintenance_window(
    window_id: UUID,
    _: Principal = Depends(require_capability(ALERTS_READ)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(MaintenanceWindow, window_id)
    if obj is None:
        raise NotFoundError(_MW_NOT_FOUND)
    return obj


@router.patch("/maintenance-windows/{window_id}", response_model=MaintenanceWindowOut)
async def update_maintenance_window(
    window_id: UUID,
    payload: MaintenanceWindowUpdate,
    principal: Principal = Depends(require_capability(ALERTS_CONFIGURE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(MaintenanceWindow, window_id)
    if obj is None:
        raise NotFoundError(_MW_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    new_start = diff.get("starts_at", obj.starts_at)
    new_end = diff.get("ends_at", obj.ends_at)
    _validate_window(new_start, new_end)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="maintenance_window.update",
        target_type="maintenance_window", target_id=str(window_id),
        site_id=obj.site_id, diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/maintenance-windows/{window_id}", status_code=204)
async def delete_maintenance_window(
    window_id: UUID,
    principal: Principal = Depends(require_capability(ALERTS_CONFIGURE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(MaintenanceWindow, window_id)
    if obj is None:
        raise NotFoundError(_MW_NOT_FOUND)
    site_id = obj.site_id
    await db.execute(delete(MaintenanceWindow).where(MaintenanceWindow.id == window_id))
    await audit.record(
        db, principal, action="maintenance_window.delete",
        target_type="maintenance_window", target_id=str(window_id), site_id=site_id,
    )
    await db.commit()
