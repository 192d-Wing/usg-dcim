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
    AlertAck,
    AlertOut,
    AlertRuleCreate,
    AlertRuleOut,
    AlertRuleUpdate,
    MaintenanceWindowCreate,
    MaintenanceWindowOut,
    MaintenanceWindowUpdate,
)
from ..schemas.common import Page, PageParams
from ..security import audit
from ..security.deps import Principal, require_capability
from ..security.scope import enforce_site_scope, scope_filtered_site_ids
from ._pagination import empty_page, paginate

router = APIRouter(prefix="/alerts", tags=["alerts"])


async def _deny_global_for_scoped(
    db: AsyncSession, capabilities, site_id: UUID | None, cap_code: str,
) -> None:
    """Read-style scope check for resources whose site column is
    nullable and NULL means 'enterprise-wide'. Scoped users can VIEW
    a global resource (handled by the list query), but can't act on
    it via single-resource endpoints — we'd be letting them mutate
    state that affects sites outside their reach."""
    from ..errors import ForbiddenError
    if site_id is not None:
        await enforce_site_scope(db, capabilities, site_id, cap_code)
        return
    # site_id is NULL → "global". Allow only if the caller's scope for
    # this cap is global.
    from ..security.deps import find_matching_capability
    scope = find_matching_capability(capabilities, cap_code)
    if scope is not None and not scope.is_global:
        raise ForbiddenError("global resource not editable in scoped role")


@router.get("", response_model=Page[AlertOut])
async def list_alerts(
    params: PageParams = Depends(PageParams.from_query),
    site_id: UUID | None = Query(None),
    state: AlertState | None = Query(None),
    severity: str | None = Query(None),
    principal: Principal = Depends(require_capability("alerts:alerts:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(Alert)
    if site_id is not None:
        stmt = stmt.where(Alert.site_id == site_id)
    if state is not None:
        stmt = stmt.where(Alert.state == state)
    if severity is not None:
        stmt = stmt.where(Alert.severity == severity)
    in_scope = await scope_filtered_site_ids(
        db, principal.capabilities, "alerts:alerts:read",
    )
    if in_scope is not None:
        if not in_scope:
            return empty_page(AlertOut, params)
        stmt = stmt.where(Alert.site_id.in_(in_scope))
    return await paginate(db, stmt, model=Alert, params=params, out_model=AlertOut)

@router.post("/{alert_id}/ack", response_model=AlertOut)
async def ack_alert(
    alert_id: UUID,
    payload: AlertAck,
    principal: Principal = Depends(require_capability("alerts:alerts:ack")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Alert, alert_id)
    if obj is None:
        raise NotFoundError("alert not found")
    await enforce_site_scope(
        db, principal.capabilities, obj.site_id, "alerts:alerts:ack",
    )
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
    principal: Principal = Depends(require_capability("alerts:rules:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(AlertRule)
    in_scope = await scope_filtered_site_ids(
        db, principal.capabilities, "alerts:rules:read",
    )
    if in_scope is not None:
        # Scoped users see rules in their reach AND enterprise-wide
        # defaults (site_scope_id IS NULL). Empty in_scope still
        # returns the NULL-scoped rules.
        from sqlalchemy import or_
        stmt = stmt.where(
            or_(AlertRule.site_scope_id.is_(None), AlertRule.site_scope_id.in_(in_scope or [None])),
        )
    return await paginate(db, stmt, model=AlertRule, params=params, out_model=AlertRuleOut)

@router.post("/rules", response_model=AlertRuleOut, status_code=201)
async def create_rule(
    payload: AlertRuleCreate,
    principal: Principal = Depends(require_capability("alerts:rules:create")),
    db: AsyncSession = Depends(get_db),
):
    await _deny_global_for_scoped(
        db, principal.capabilities, payload.site_scope_id, "alerts:rules:create",
    )
    obj = AlertRule(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(db, principal, action="alert_rule.create", target_type="alert_rule",
                       target_id=str(obj.id), site_id=obj.site_scope_id)
    await db.commit()
    await db.refresh(obj)
    return obj

_RULE_NOT_FOUND = "alert rule not found"

@router.get("/rules/{rule_id}", response_model=AlertRuleOut)
async def get_rule(
    rule_id: UUID,
    principal: Principal = Depends(require_capability("alerts:rules:read")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(AlertRule, rule_id)
    if obj is None:
        raise NotFoundError(_RULE_NOT_FOUND)
    # Read-side: a scoped user can VIEW an enterprise default (NULL
    # site_scope_id) since it applies to their sites. Only block when
    # the rule belongs to a site outside their reach.
    if obj.site_scope_id is not None:
        await enforce_site_scope(
            db, principal.capabilities, obj.site_scope_id, "alerts:rules:read",
        )
    return obj

@router.patch("/rules/{rule_id}", response_model=AlertRuleOut)
async def update_rule(
    rule_id: UUID,
    payload: AlertRuleUpdate,
    principal: Principal = Depends(require_capability("alerts:rules:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(AlertRule, rule_id)
    if obj is None:
        raise NotFoundError(_RULE_NOT_FOUND)
    await _deny_global_for_scoped(
        db, principal.capabilities, obj.site_scope_id, "alerts:rules:update",
    )
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="alert_rule.update", target_type="alert_rule",
        target_id=str(rule_id), site_id=obj.site_scope_id, diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj

@router.delete("/rules/{rule_id}", status_code=204)
async def delete_rule(
    rule_id: UUID,
    principal: Principal = Depends(require_capability("alerts:rules:delete")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(AlertRule, rule_id)
    if obj is None:
        raise NotFoundError(_RULE_NOT_FOUND)
    await _deny_global_for_scoped(
        db, principal.capabilities, obj.site_scope_id, "alerts:rules:delete",
    )
    site_id = obj.site_scope_id
    await db.execute(delete(AlertRule).where(AlertRule.id == rule_id))
    await audit.record(
        db, principal, action="alert_rule.delete", target_type="alert_rule",
        target_id=str(rule_id), site_id=site_id,
    )
    await db.commit()

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
    principal: Principal = Depends(require_capability("maintenance:windows:read")),
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
    in_scope = await scope_filtered_site_ids(
        db, principal.capabilities, "maintenance:windows:read",
    )
    if in_scope is not None:
        from sqlalchemy import or_
        stmt = stmt.where(
            or_(MaintenanceWindow.site_id.is_(None), MaintenanceWindow.site_id.in_(in_scope or [None])),
        )
    return await paginate(
        db, stmt, model=MaintenanceWindow, params=params, out_model=MaintenanceWindowOut,
    )

@router.post("/maintenance-windows", response_model=MaintenanceWindowOut, status_code=201)
async def create_maintenance_window(
    payload: MaintenanceWindowCreate,
    principal: Principal = Depends(require_capability("maintenance:windows:create")),
    db: AsyncSession = Depends(get_db),
):
    _validate_window(payload.starts_at, payload.ends_at)
    await _deny_global_for_scoped(
        db, principal.capabilities, payload.site_id, "maintenance:windows:create",
    )
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
    principal: Principal = Depends(require_capability("maintenance:windows:read")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(MaintenanceWindow, window_id)
    if obj is None:
        raise NotFoundError(_MW_NOT_FOUND)
    if obj.site_id is not None:
        await enforce_site_scope(
            db, principal.capabilities, obj.site_id, "maintenance:windows:read",
        )
    return obj

@router.patch("/maintenance-windows/{window_id}", response_model=MaintenanceWindowOut)
async def update_maintenance_window(
    window_id: UUID,
    payload: MaintenanceWindowUpdate,
    principal: Principal = Depends(require_capability("maintenance:windows:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(MaintenanceWindow, window_id)
    if obj is None:
        raise NotFoundError(_MW_NOT_FOUND)
    await _deny_global_for_scoped(
        db, principal.capabilities, obj.site_id, "maintenance:windows:update",
    )
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
    principal: Principal = Depends(require_capability("maintenance:windows:delete")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(MaintenanceWindow, window_id)
    if obj is None:
        raise NotFoundError(_MW_NOT_FOUND)
    await _deny_global_for_scoped(
        db, principal.capabilities, obj.site_id, "maintenance:windows:delete",
    )
    site_id = obj.site_id
    await db.execute(delete(MaintenanceWindow).where(MaintenanceWindow.id == window_id))
    await audit.record(
        db, principal, action="maintenance_window.delete",
        target_type="maintenance_window", target_id=str(window_id), site_id=site_id,
    )
    await db.commit()
