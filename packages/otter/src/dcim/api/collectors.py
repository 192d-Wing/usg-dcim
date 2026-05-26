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
from ..schemas.collectors import (
    CollectorConfigOverrides,
    CollectorConfigPatch,
    CollectorEnabledPatch,
    CollectorEnroll,
    CollectorHeartbeatIn,
    CollectorHeartbeatOut,
    CollectorOut,
)
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
        db, principal.capabilities, payload.site_id, cap_code="collectors:collectors:enroll",
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

@router.post("/{collector_id}/heartbeat", response_model=CollectorHeartbeatOut)
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
    # Echo the current overrides back; the Go collector applies any
    # change on the next loop iteration. Empty/missing keys leave the
    # collector on its YAML defaults.
    overrides = CollectorConfigOverrides.model_validate(coll.config_overrides or {})
    return CollectorHeartbeatOut(received_at=now, config_overrides=overrides)


@router.patch("/{collector_id}/config", response_model=CollectorOut)
async def patch_collector_config(
    collector_id: UUID,
    payload: CollectorConfigPatch,
    principal: Principal = Depends(require_capability("collectors:collectors:update")),
    db: AsyncSession = Depends(get_db),
):
    """Set per-collector ticker overrides. Body is shape-identical to
    CollectorConfigOverrides; sending `null` on a field clears that
    override (collector falls back to its YAML default). Propagation
    is up-to-one-heartbeat-interval — the value lands on the wire in
    the next heartbeat response."""

    coll = await db.get(Collector, collector_id)
    if coll is None:
        raise NotFoundError("collector not found")
    await enforce_site_scope(
        db, principal.capabilities, coll.site_id, cap_code="collectors:collectors:update",
    )
    # model_dump(exclude_unset=False) so an explicit null in the payload
    # is honoured as "clear this override" — without it, the operator
    # would have to PATCH the entire shape every time.
    coll.config_overrides = {
        k: v for k, v in payload.model_dump().items() if v is not None
    }
    await audit.record(
        db, principal,
        action="collector.config_overrides.update",
        target_type="collector", target_id=str(collector_id),
        site_id=coll.site_id,
        metadata={"overrides": coll.config_overrides},
    )
    await db.commit()
    await db.refresh(coll)
    return coll


@router.patch("/{collector_id}/enabled", response_model=CollectorOut)
async def set_collector_enabled(
    collector_id: UUID,
    payload: CollectorEnabledPatch,
    principal: Principal = Depends(require_capability("collectors:collectors:update")),
    db: AsyncSession = Depends(get_db),
):
    """Enable or disable a collector."""
    coll = await db.get(Collector, collector_id)
    if coll is None:
        raise NotFoundError("collector not found")
    await enforce_site_scope(
        db, principal.capabilities, coll.site_id, cap_code="collectors:collectors:update",
    )
    coll.enabled = payload.enabled
    await audit.record(
        db, principal,
        action="collector.enabled.update",
        target_type="collector", target_id=str(collector_id),
        site_id=coll.site_id,
        metadata={"enabled": payload.enabled},
    )
    await db.commit()
    await db.refresh(coll)
    return coll


@router.delete("/{collector_id}", status_code=204)
async def decommission_collector(
    collector_id: UUID,
    principal: Principal = Depends(require_capability("collectors:collectors:update")),
    db: AsyncSession = Depends(get_db),
):
    """Decommission a collector (soft-delete, sets status to decommissioned)."""
    coll = await db.get(Collector, collector_id)
    if coll is None:
        raise NotFoundError("collector not found")
    await enforce_site_scope(
        db, principal.capabilities, coll.site_id, cap_code="collectors:collectors:update",
    )
    coll.status = CollectorStatus.decommissioned
    await audit.record(
        db, principal,
        action="collector.decommission",
        target_type="collector", target_id=str(collector_id),
        site_id=coll.site_id,
    )
    await db.commit()
