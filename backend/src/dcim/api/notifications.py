"""CRUD for notification channels + a `test` endpoint that dispatches a
synthetic alert through one channel so operators can verify wiring."""

from __future__ import annotations

from datetime import UTC, datetime
from uuid import UUID, uuid4

from fastapi import APIRouter, Depends
from sqlalchemy import delete, select
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..errors import ConflictError, NotFoundError
from ..models.alerts import Alert, AlertState, Severity
from ..models.notifications import NotificationChannel
from ..schemas.common import Page, PageParams
from ..schemas.notifications import (
    NotificationChannelCreate,
    NotificationChannelOut,
    NotificationChannelUpdate,
)
from ..security import audit
from ..security.capabilities import ALERTS_CONFIGURE, ALERTS_READ
from ..security.deps import Principal, require_capability
from ..services import notifications as notif_svc
from ._pagination import paginate

router = APIRouter(prefix="/notifications", tags=["notifications"])

_CHANNEL_NOT_FOUND = "notification channel not found"


@router.get("/channels", response_model=Page[NotificationChannelOut])
async def list_channels(
    params: PageParams = Depends(PageParams.from_query),
    _: Principal = Depends(require_capability(ALERTS_READ)),
    db: AsyncSession = Depends(get_db),
):
    return await paginate(
        db, select(NotificationChannel),
        model=NotificationChannel, params=params, out_model=NotificationChannelOut,
    )


@router.post("/channels", response_model=NotificationChannelOut, status_code=201)
async def create_channel(
    payload: NotificationChannelCreate,
    principal: Principal = Depends(require_capability(ALERTS_CONFIGURE)),
    db: AsyncSession = Depends(get_db),
):
    existing = (
        await db.execute(select(NotificationChannel).where(NotificationChannel.name == payload.name))
    ).scalar_one_or_none()
    if existing is not None:
        raise ConflictError("a channel with that name already exists")
    obj = NotificationChannel(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="notification_channel.create",
        target_type="notification_channel", target_id=str(obj.id),
        metadata={"kind": payload.kind.value},
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/channels/{channel_id}", response_model=NotificationChannelOut)
async def update_channel(
    channel_id: UUID,
    payload: NotificationChannelUpdate,
    principal: Principal = Depends(require_capability(ALERTS_CONFIGURE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(NotificationChannel, channel_id)
    if obj is None:
        raise NotFoundError(_CHANNEL_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="notification_channel.update",
        target_type="notification_channel", target_id=str(channel_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/channels/{channel_id}", status_code=204)
async def delete_channel(
    channel_id: UUID,
    principal: Principal = Depends(require_capability(ALERTS_CONFIGURE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(NotificationChannel, channel_id)
    if obj is None:
        raise NotFoundError(_CHANNEL_NOT_FOUND)
    await db.execute(delete(NotificationChannel).where(NotificationChannel.id == channel_id))
    await audit.record(
        db, principal, action="notification_channel.delete",
        target_type="notification_channel", target_id=str(channel_id),
    )
    await db.commit()


@router.post("/channels/{channel_id}/test")
async def test_channel(
    channel_id: UUID,
    principal: Principal = Depends(require_capability(ALERTS_CONFIGURE)),
    db: AsyncSession = Depends(get_db),
) -> dict:
    """Dispatch a synthetic alert through a single channel.

    Useful to verify Slack URLs / SMTP wiring before relying on it for real
    incidents. The synthetic alert is never persisted.
    """
    channel = await db.get(NotificationChannel, channel_id)
    if channel is None:
        raise NotFoundError(_CHANNEL_NOT_FOUND)
    now = datetime.now(UTC)
    fake = Alert(
        id=uuid4(),
        site_id=None,  # type: ignore[arg-type]
        severity=Severity.warning,
        state=AlertState.firing,
        dedupe_key=f"test|{channel_id}",
        summary=f"Test notification for channel {channel.name!r}",
        detail=f"Triggered by {principal.label} at {now.isoformat()}",
        first_seen_at=now,
        last_seen_at=now,
        labels_json={"test": True},
    )
    # Force the channel's filters to apply, but skip persistence.
    if not channel.enabled:
        return {"delivered": False, "error": "channel is disabled"}
    if not notif_svc.channel_matches(channel, Severity.warning, "fire"):
        return {"delivered": False, "error": "channel filters skip warning-severity fires"}
    outcomes = await notif_svc.dispatch(db, fake, event="fire")
    out = next((o for o in outcomes if o.channel_id == str(channel_id)), None)
    return {
        "delivered": out.delivered if out else False,
        "error": out.error if out else "no outcome",
    }
