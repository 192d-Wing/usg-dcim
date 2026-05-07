"""Alert listing, acknowledge, and rule CRUD."""

from __future__ import annotations

from datetime import UTC, datetime
from uuid import UUID

from fastapi import APIRouter, Depends, Query
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..errors import NotFoundError
from ..models.alerts import Alert, AlertRule, AlertState
from ..schemas.alerts import AlertAck, AlertOut, AlertRuleCreate, AlertRuleOut
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
