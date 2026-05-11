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
from .models.dns import DnsHealthCheck, DnsZone, DnsZoneKind
from .models.telemetry_meta import FreshnessState, TelemetrySource
from .services import alerts as alerts_svc
from .services import dns as dns_svc
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


async def dns_sync_from_ipam(_ctx) -> dict:
    """Re-project IPAddress.dns_name into source=ipam DNS records for
    every site zone. Catches new allocations / DHCP leases that landed
    since the last cycle. Apex zones are skipped (operator-curated)."""
    async with async_session() as db:
        zones = (
            await db.execute(select(DnsZone).where(DnsZone.kind == DnsZoneKind.site))
        ).scalars().all()
        total_added = total_removed = 0
        for z in zones:
            added, removed = await dns_svc.sync_ipam_records_for_zone(db, z)
            total_added += added
            total_removed += removed
        await db.commit()
    log.info("dns_sync_from_ipam", added=total_added, removed=total_removed, zones=len(zones))
    return {"added": total_added, "removed": total_removed, "zones": len(zones)}


async def dns_rotate_zsks(_ctx) -> dict:
    """Rotate ZSKs for signed zones whose zsk_rotation_days policy has
    elapsed. KSKs are intentionally skipped here — the parent-zone DS
    upload still belongs to a human."""
    async with async_session() as db:
        result = await dns_svc.auto_rotate_due_zsks(db)
    log.info("dns_rotate_zsks", **result)
    return result


async def dns_health_checks(_ctx) -> dict:
    """Probe every enabled DnsHealthCheck and update its status. The
    bundle renderer reads this status to exclude records bound to
    unhealthy checks from the rendered zone."""
    async with async_session() as db:
        checks = (
            await db.execute(
                select(DnsHealthCheck).where(DnsHealthCheck.enabled.is_(True))
            )
        ).scalars().all()
        changed = 0
        for check in checks:
            new_status, err = await dns_svc.probe_health_check(check)
            if check.status != new_status:
                check.status = new_status
                changed += 1
            check.last_checked_at = datetime.now(UTC)
            check.last_error = err
        await db.commit()
    log.info("dns_health_checks", probed=len(checks), changed=changed)
    return {"probed": len(checks), "changed": changed}


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
        dhcp_sync, dhcp_age_out, dns_sync_from_ipam, dns_health_checks,
        dns_rotate_zsks,
    ]
    cron_jobs: ClassVar[list] = [
        cron(evaluate_alerts, second={0, 30}),
        cron(sweep_collectors, second=15),
        cron(freshness_sweep, minute=set(range(0, 60, 5))),
        # DHCP sync every 5 minutes; aging swept hourly so deprecated +
        # already-stale rows don't pile up.
        cron(dhcp_sync, minute=set(range(2, 60, 5))),
        cron(dhcp_age_out, minute={7}),
        # DNS IPAM-projection: 5 minutes offset from DHCP so a freshly-
        # ingested lease has time to land before its DNS record renders.
        cron(dns_sync_from_ipam, minute=set(range(4, 60, 5))),
        # Health-check probes run every 30s — finer than the configured
        # interval_seconds field on individual checks (the function
        # internally skips checks whose interval hasn't elapsed).
        cron(dns_health_checks, second={0, 30}),
        # ZSK rotation once a day at 03:17 UTC — the cron skips zones
        # whose policy hasn't elapsed, so a daily wakeup is cheap and
        # avoids tight loops near boundary seconds.
        cron(dns_rotate_zsks, hour={3}, minute={17}),
    ]
