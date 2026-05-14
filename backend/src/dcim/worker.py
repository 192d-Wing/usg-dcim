"""arq worker — periodic + on-demand background jobs.

Runs alongside the API in production (separate Deployment). Tasks:
  - telemetry.freshness : flip TelemetrySource.freshness to stale when overdue
  - dhcp.sync           : pull every Kea Control Agent for active leases
  - dhcp.age_out        : deprecate / delete dhcp-sourced rows whose lease lapsed
  - notify.bridge       : drain dcim:notify:bridge (alerts fired by go-alerts)
                          and call dispatch_fire/dispatch_resolve

The alert evaluation, collector sweep, and DNS health-check loops have been
moved to native Go services (services/go-alerts, services/go-dns-probe).
Their Python implementations (evaluate_alerts, sweep_collectors,
dns_health_checks) are retained below for fast rollback — re-add the
matching cron(...) entries to WorkerSettings.cron_jobs to restart them.
"""

from __future__ import annotations

import json
from datetime import UTC, datetime, timedelta
from typing import ClassVar
from uuid import UUID

import structlog
from arq import cron
from arq.connections import RedisSettings
from redis.asyncio import from_url as redis_from_url
from sqlalchemy import select

from .db import async_session
from .logging_setup import configure_logging
from .models.alerts import Alert
from .models.dns import DnsServerMetricsSample, DnsZone, DnsZoneKind
from .models.telemetry_meta import FreshnessState, TelemetrySource
from .services import alerts as alerts_svc
from .services import dns as dns_svc
from .services import kea as kea_svc
from .services import notifications as notif_svc
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


async def dns_purge_metrics(_ctx) -> dict:
    """Drop dns_server_metrics_samples older than
    settings.dns_metrics_retention_days. The table grows unbounded
    otherwise — every scrape inserts a fresh row."""
    from sqlalchemy import delete  # local import keeps cold-path light
    settings = get_settings()
    cutoff = datetime.now(UTC) - timedelta(days=settings.dns_metrics_retention_days)
    async with async_session() as db:
        result = await db.execute(
            delete(DnsServerMetricsSample)
            .where(DnsServerMetricsSample.observed_at < cutoff)
        )
        await db.commit()
        deleted = result.rowcount or 0
    log.info("dns_purge_metrics", deleted=deleted, cutoff=cutoff.isoformat())
    return {"deleted": deleted, "retention_days": settings.dns_metrics_retention_days}


async def dns_rotate_zsks(_ctx) -> dict:
    """Rotate ZSKs for signed zones whose zsk_rotation_days policy has
    elapsed. KSKs are intentionally skipped here — the parent-zone DS
    upload still belongs to a human."""
    async with async_session() as db:
        result = await dns_svc.auto_rotate_due_zsks(db)
    log.info("dns_rotate_zsks", **result)
    return result


async def dns_health_checks(_ctx) -> dict:
    """Central-side gap-filler probe loop. Only fires checks whose
    last_checked_at is older than interval_seconds * 1.5 — collectors
    that probe on their own keep last_checked_at fresh and central
    naturally backs off. Standalone deployments without collector
    probing fall through to full central probing."""
    async with async_session() as db:
        due = await dns_svc.central_health_checks_due(db)
        changed = 0
        for check in due:
            new_status, err = await dns_svc.probe_health_check(check)
            if check.status != new_status:
                check.status = new_status
                changed += 1
            check.last_checked_at = datetime.now(UTC)
            check.last_error = err
        await db.commit()
    log.info("dns_health_checks", probed=len(due), changed=changed)
    return {"probed": len(due), "changed": changed}


async def startup(ctx) -> None:
    configure_logging()
    # Expose the worker's Prometheus registry on the configured port so
    # Prometheus can scrape the business counters that the worker
    # increments (alerts_fired, alert_eval_runs, telemetry batch sizes,
    # DNS render outcomes). The api exposes its own /metrics on 8000;
    # the worker runs as a separate process with its own in-memory
    # registry, so without this its counters would be unreachable.
    # The HTTP server is daemonized inside prometheus_client — no
    # cleanup needed at shutdown.
    from prometheus_client import start_http_server
    port = get_settings().worker_metrics_port
    if port > 0:
        try:
            start_http_server(port)
            log.info("worker_metrics_listening", port=port)
        except OSError as e:
            # Most common cause: another worker replica on the same
            # host bound the port first. Log and continue — the alert
            # eval loop is the worker's primary job, metrics are
            # secondary.
            log.warning("worker_metrics_listen_failed", port=port, error=str(e))
    log.info("worker_startup")


_NOTIFY_BRIDGE_KEY = "dcim:notify:bridge"
# Max items to pull per cron tick. The Go alerts service fires at most
# once per (rule, asset) every 30s, so a few hundred per cycle is the
# realistic worst case in any production-sized deployment.
_NOTIFY_BRIDGE_BATCH = 500


async def notify_bridge(_ctx) -> dict:
    """Drain entries that the Go alerts service pushed onto
    dcim:notify:bridge and dispatch them through the Python notifier.

    Payload shape (one per LPUSH):
      {"kind": "fire"|"resolve", "alert_id": "<uuid>"}
    """
    r = redis_from_url(str(get_settings().redis_dsn), decode_responses=True)
    try:
        fired = resolved = skipped = 0
        async with async_session() as db:
            for _ in range(_NOTIFY_BRIDGE_BATCH):
                raw = await r.rpop(_NOTIFY_BRIDGE_KEY)
                if raw is None:
                    break
                try:
                    payload = json.loads(raw)
                    alert_id = UUID(payload["alert_id"])
                    kind = payload["kind"]
                except (ValueError, KeyError, TypeError) as e:
                    log.warning("notify_bridge_bad_payload", raw=raw, error=str(e))
                    skipped += 1
                    continue
                alert = await db.get(Alert, alert_id)
                if alert is None:
                    skipped += 1
                    continue
                if kind == "fire":
                    await notif_svc.dispatch_fire(db, alert)
                    fired += 1
                elif kind == "resolve":
                    await notif_svc.dispatch_resolve(db, alert)
                    resolved += 1
                else:
                    skipped += 1
    finally:
        await r.aclose()
    if fired or resolved or skipped:
        log.info("notify_bridge_drained", fired=fired, resolved=resolved, skipped=skipped)
    return {"fired": fired, "resolved": resolved, "skipped": skipped}


async def shutdown(ctx) -> None:
    log.info("worker_shutdown")


class WorkerSettings:
    redis_settings = RedisSettings.from_dsn(str(get_settings().redis_dsn))
    on_startup = startup
    on_shutdown = shutdown
    functions: ClassVar[list] = [
        # Kept registered so an operator can `arq` enqueue them ad-hoc for
        # rollback / one-shot runs even though their crons have been
        # retired in favor of services/go-alerts and services/go-dns-probe.
        evaluate_alerts, sweep_collectors, dns_health_checks,
        freshness_sweep,
        dhcp_sync, dhcp_age_out, dns_sync_from_ipam,
        dns_rotate_zsks, dns_purge_metrics,
        notify_bridge,
    ]
    cron_jobs: ClassVar[list] = [
        # Drain notification events the Go alerts service pushes onto
        # dcim:notify:bridge. Runs every 5s; each tick pulls up to
        # _NOTIFY_BRIDGE_BATCH entries so a notification burst clears
        # quickly without holding the worker for long.
        cron(notify_bridge, second=set(range(0, 60, 5))),
        cron(freshness_sweep, minute=set(range(0, 60, 5))),
        # DHCP sync every 5 minutes; aging swept hourly so deprecated +
        # already-stale rows don't pile up.
        cron(dhcp_sync, minute=set(range(2, 60, 5))),
        cron(dhcp_age_out, minute={7}),
        # DNS IPAM-projection: 5 minutes offset from DHCP so a freshly-
        # ingested lease has time to land before its DNS record renders.
        cron(dns_sync_from_ipam, minute=set(range(4, 60, 5))),
        # ZSK rotation once a day at 03:17 UTC — the cron skips zones
        # whose policy hasn't elapsed, so a daily wakeup is cheap and
        # avoids tight loops near boundary seconds.
        cron(dns_rotate_zsks, hour={3}, minute={17}),
        # Metrics retention runs hourly at :23 — far enough from the
        # other DNS cron jobs that worker bursts don't pile up.
        cron(dns_purge_metrics, minute={23}),
        # RETIRED — moved to native Go services. Re-enable for rollback:
        #   cron(evaluate_alerts, second={0, 30}),   # → services/go-alerts
        #   cron(sweep_collectors, second=15),        # → services/go-alerts
        #   cron(dns_health_checks, second={0, 30}),  # → services/go-dns-probe
    ]
