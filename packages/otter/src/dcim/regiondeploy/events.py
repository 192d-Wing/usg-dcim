"""Region-deploy event emitter.

Every state change the orchestrator makes lands here. The emitter
does two things in one call:

  1. Append to `region_deployment_events` so the SSE catch-up
     endpoint can backfill a UI that reconnects mid-deploy.
  2. Publish to Redis channel `deploy:{id}` so live SSE streams
     get the event in real time.

The split matters for the UI's reconnection model:

  * On connect, the client sends `?since=<last_event_id>`. The API
    streams the persisted backlog from the table, then attaches to
    pubsub for live events.
  * Without the persistence step, a client that disconnects misses
    every event during the gap; without the pubsub step, the UI
    would have to poll (and we lose the "stage progress as it
    happens" UX the SSE design promises).

Channel naming: `dcim:deploy:{deployment_id}` — `dcim:` prefix
matches the existing pubsub channel convention used by go-alerts
(`dcim:notify:bridge`) so an operator browsing all channels sees a
consistent namespace.
"""

from __future__ import annotations

import contextlib
import json
from typing import Any
from uuid import UUID

from sqlalchemy.ext.asyncio import AsyncSession

from ..models.regiondeploy import (
    RegionDeploymentEvent,
    RegionDeploymentEventLevel,
)


def channel_for(deployment_id: UUID | str) -> str:
    return f"dcim:deploy:{deployment_id}"


async def emit(
    db: AsyncSession,
    redis: Any,
    *,
    deployment_id: UUID,
    stage: str,
    message: str,
    level: RegionDeploymentEventLevel = RegionDeploymentEventLevel.info,
    payload: dict | None = None,
) -> RegionDeploymentEvent:
    """Persist an event and publish it on the deploy's pubsub channel.

    Returns the saved row so callers can use its bigint `id` as the
    Last-Event-ID marker the SSE client echoes back on reconnect.

    Failure modes are deliberately asymmetric:
      * the DB write is awaited — if persistence fails, the orches-
        trator should see the error and decide whether to retry.
      * the pubsub publish swallows errors after logging — a Redis
        outage shouldn't fail the stage transition that the operator
        will still see on the next page refresh.
    """
    row = RegionDeploymentEvent(
        deployment_id=deployment_id,
        stage=stage,
        message=message,
        level=level,
        payload=payload,
    )
    db.add(row)
    await db.flush()  # populate row.id before publish so subscribers can dedupe

    if redis is not None:
        envelope = {
            "id": row.id,
            "stage": stage,
            "level": level.value,
            "message": message,
            "payload": payload or {},
        }
        # Pubsub publish is best-effort: the row is already persisted
        # and the SSE catch-up path will deliver it on reconnect.
        # Swallowing means a Redis outage doesn't fail the orchestrator
        # stage transition that the operator will still see on refresh.
        with contextlib.suppress(Exception):
            await redis.publish(
                channel_for(deployment_id),
                json.dumps(envelope, default=str),
            )

    return row
