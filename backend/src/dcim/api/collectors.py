"""Collector enrollment, listing, heartbeats, and freshness reporting."""

from __future__ import annotations

import secrets
from datetime import UTC, datetime
from uuid import UUID

from fastapi import APIRouter, Depends
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..errors import NotFoundError
from ..models.collectors import Collector, CollectorHeartbeat, CollectorStatus
from ..schemas.collectors import CollectorEnroll, CollectorHeartbeatIn, CollectorOut
from ..schemas.common import Page, PageParams
from ..security import audit
from ..security.deps import Principal, require_capability
from ..security.scope import enforce_site_scope, scope_filtered_site_ids
from ..security.tokens import hash_api_token
from ._pagination import empty_page, paginate

router = APIRouter(prefix="/collectors", tags=["collectors"])

@router.get("", response_model=Page[CollectorOut])
async def list_collectors(
    params: PageParams = Depends(PageParams.from_query),
    principal: Principal = Depends(require_capability("collectors:collectors:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(Collector)
    in_scope = await scope_filtered_site_ids(
        db, principal.capabilities, "collectors:collectors:read",
    )
    if in_scope is not None:
        if not in_scope:
            return empty_page(CollectorOut, params)
        stmt = stmt.where(Collector.site_id.in_(in_scope))
    return await paginate(db, stmt, model=Collector, params=params, out_model=CollectorOut)

@router.post("/enroll")
async def enroll_collector(
    payload: CollectorEnroll,
    principal: Principal = Depends(require_capability("collectors:collectors:enroll")),
    db: AsyncSession = Depends(get_db),
):
    """Issue a one-time enrollment token. The collector exchanges it for an mTLS cert + API token."""
    # Operators can't enroll a collector at a site outside their scope.
    await enforce_site_scope(
        db, principal.capabilities, payload.site_id, "collectors:collectors:enroll",
    )
    raw = "enroll_" + secrets.token_urlsafe(32)
    obj = Collector(
        site_id=payload.site_id,
        name=payload.name,
        capabilities=payload.capabilities,
        status=CollectorStatus.pending,
        enrollment_token_hash=hash_api_token(raw),
    )
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal,
        action="collector.enroll", target_type="collector", target_id=str(obj.id), site_id=obj.site_id,
    )
    await db.commit()
    await db.refresh(obj)
    return {"collector_id": str(obj.id), "enrollment_token": raw, "expires_in_seconds": 3600}

@router.post("/{collector_id}/heartbeat")
async def heartbeat(
    collector_id: UUID,
    payload: CollectorHeartbeatIn,
    _: Principal = Depends(require_capability("collectors:ingest:write")),
    db: AsyncSession = Depends(get_db),
):
    coll = await db.get(Collector, collector_id)
    if coll is None:
        raise NotFoundError("collector not found")
    now = datetime.now(UTC)
    coll.last_seen_at = now
    coll.buffered_samples = payload.buffered_samples
    if payload.version:
        coll.version = payload.version
    coll.status = CollectorStatus.degraded if payload.last_error else CollectorStatus.healthy
    db.add(
        CollectorHeartbeat(
            collector_id=collector_id,
            received_at=now,
            queue_depth=payload.queue_depth,
            last_error=payload.last_error,
            metrics_json=payload.metrics,
        )
    )
    await db.commit()
    return {"ok": True, "received_at": now.isoformat()}
