"""Audit log read API. Compliance-facing, capability-gated."""

from __future__ import annotations

from datetime import datetime
from uuid import UUID

from fastapi import APIRouter, Depends, Query
from pydantic import BaseModel, ConfigDict
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..models.audit import AuditLog
from ..schemas.common import Page, PageParams

from ..security.deps import Principal, require_capability
from ._pagination import paginate

router = APIRouter(prefix="/audit", tags=["audit"])

class AuditLogOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    occurred_at: datetime
    actor_user_id: UUID | None
    actor_token_id: UUID | None
    actor_label: str | None
    actor_ip: str | None
    action: str
    target_type: str | None
    target_id: str | None
    site_id: UUID | None
    request_id: str | None
    success: bool
    diff_json: dict
    metadata_json: dict

@router.get("/log", response_model=Page[AuditLogOut])
async def list_audit_log(
    params: PageParams = Depends(PageParams.from_query),
    actor_user_id: UUID | None = Query(None),
    action: str | None = Query(None, description="Exact action match, e.g. asset.update."),
    target_type: str | None = Query(None),
    target_id: str | None = Query(None),
    target_ids: str | None = Query(
        None,
        description=(
            "Comma-separated set of target ids. Useful for scoping the log "
            "to a group of related resources in a single round-trip, e.g. "
            "all records belonging to a particular DNS zone."
        ),
    ),
    site_id: UUID | None = Query(None),
    since: datetime | None = Query(None, description="Only entries at or after this timestamp."),
    until: datetime | None = Query(None, description="Only entries at or before this timestamp."),
    success: bool | None = Query(None),
    _: Principal = Depends(require_capability("audit:events:read")),
    db: AsyncSession = Depends(get_db),
):
    """Filtered audit-log listing. Sorts newest-first by default."""
    stmt = select(AuditLog)
    if actor_user_id is not None:
        stmt = stmt.where(AuditLog.actor_user_id == actor_user_id)
    if action is not None:
        stmt = stmt.where(AuditLog.action == action)
    if target_type is not None:
        stmt = stmt.where(AuditLog.target_type == target_type)
    if target_id is not None:
        stmt = stmt.where(AuditLog.target_id == target_id)
    if target_ids:
        # Trim + dedupe; empty list short-circuits to no rows so the
        # caller doesn't see "no filter" semantics by accident.
        ids = [s.strip() for s in target_ids.split(",") if s.strip()]
        if not ids:
            stmt = stmt.where(AuditLog.id.is_(None))
        else:
            stmt = stmt.where(AuditLog.target_id.in_(ids))
    if site_id is not None:
        stmt = stmt.where(AuditLog.site_id == site_id)
    if since is not None:
        stmt = stmt.where(AuditLog.occurred_at >= since)
    if until is not None:
        stmt = stmt.where(AuditLog.occurred_at <= until)
    if success is not None:
        stmt = stmt.where(AuditLog.success == success)
    # Default newest-first when caller didn't ask for a sort.
    if not params.sort:
        stmt = stmt.order_by(AuditLog.occurred_at.desc())
    return await paginate(db, stmt, model=AuditLog, params=params, out_model=AuditLogOut)

@router.get("/actions", response_model=list[str])
async def list_distinct_actions(
    _: Principal = Depends(require_capability("audit:events:read")),
    db: AsyncSession = Depends(get_db),
) -> list[str]:
    """Distinct action codes present in the log — used to populate the filter UI."""
    rows = (await db.execute(select(AuditLog.action).distinct())).all()
    return sorted({r[0] for r in rows if r[0]})
