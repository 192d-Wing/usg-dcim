"""arq worker — periodic + on-demand background jobs.

Runs alongside the API in production (separate Deployment). Tasks:
  - alerts.eval         : run alert rule evaluation
  - alerts.collectors   : sweep stale collectors and fire collector-down alerts
  - telemetry.freshness : flip TelemetrySource.freshness to stale when overdue
  - dhcp.sync           : pull every Kea Control Agent for active leases
  - dhcp.age_out        : deprecate / delete dhcp-sourced rows whose lease lapsed
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
from typing import ClassVar

import structlog
from arq import cron
from arq.connections import RedisSettings
from sqlalchemy import select

from .db import async_session
from .logging_setup import configure_logging
from .models.telemetry_meta import FreshnessState, TelemetrySource
from .services import alerts as alerts_svc
from .services import kea as kea_svc
from .settings import get_settings

log = structlog.get_logger("dcim.worker")


async def evaluate_alerts(_ctx) -> dict:
    async with async_session() as db:
        return await alerts_svc.evaluate_rules(db)


async def sweep_collectors(_ctx) -> dict:
    async with async_session() as db:
        return await alerts_svc.sweep_collectors(db)


async def freshness_sweep(_ctx) -> dict:
    """Flip TelemetrySource rows to stale when last_success_at is older than 3x poll interval."""
    now = datetime.now(UTC)
    async with async_session() as db:
        rows = (await db.execute(select(TelemetrySource))).scalars().all()
        flipped = 0
        for r in rows:
            if r.last_success_at is None:
                continue
            cutoff = now - timedelta(seconds=max(60, r.poll_interval_seconds * 3))
            if r.last_success_at < cutoff and r.freshness == FreshnessState.current:
                r.freshness = FreshnessState.stale
                flipped += 1
        await db.commit()
    log.info("freshness_sweep", flipped=flipped)
    return {"flipped": flipped}


async def dhcp_sync(_ctx) -> dict:
    async with async_session() as db:
        return await kea_svc.sync_all_servers(db)


async def dhcp_age_out(_ctx) -> dict:
    async with async_session() as db:
        n = await kea_svc.age_out_stale_dhcp(db)
    return {"aged_out": n}


async def startup(ctx) -> None:
    configure_logging()
    log.info("worker_startup")


async def shutdown(ctx) -> None:
    log.info("worker_shutdown")


class WorkerSettings:
    redis_settings = RedisSettings.from_dsn(str(get_settings().redis_dsn))
    on_startup = startup
    on_shutdown = shutdown
    functions: ClassVar[list] = [
        evaluate_alerts, sweep_collectors, freshness_sweep,
        dhcp_sync, dhcp_age_out,
    ]
    cron_jobs: ClassVar[list] = [
        cron(evaluate_alerts, second={0, 30}),
        cron(sweep_collectors, second=15),
        cron(freshness_sweep, minute=set(range(0, 60, 5))),
        # DHCP sync every 5 minutes; aging swept hourly so deprecated +
        # already-stale rows don't pile up.
        cron(dhcp_sync, minute=set(range(2, 60, 5))),
        cron(dhcp_age_out, minute={7}),
    ]
