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
from .regiondeploy.orchestrator import run_region_deploy
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


async def rerender_dhcp_bundle(_ctx, server_id: str) -> dict:
    """PR 83 — re-render the Kea bundle for one server and write it
    to dhcp_servers.bundle_cache_*. Enqueued by API handlers after
    scope create/update/delete or template update commits.

    No-op if the server is missing (race: deleted between enqueue
    and execution). Returns the new etag + element counts for log
    inspection.
    """
    from .models.ipam import DhcpScope, DhcpScopeTemplate, DhcpServer
    from .services import dhcp_bundle as bundle_svc

    async with async_session() as db:
        server = await db.get(DhcpServer, UUID(server_id))
        if server is None:
            log.info("rerender_dhcp_bundle.server_gone", server_id=server_id)
            return {"server_id": server_id, "status": "server_gone"}
        scopes = (
            await db.execute(
                select(DhcpScope).where(DhcpScope.dhcp_server_id == server.id)
            )
        ).scalars().all()
        template_ids = {s.template_id for s in scopes if s.template_id}
        templates_by_id: dict = {}
        if template_ids:
            rows = (
                await db.execute(
                    select(DhcpScopeTemplate)
                    .where(DhcpScopeTemplate.id.in_(template_ids))
                )
            ).scalars().all()
            templates_by_id = {t.id: t for t in rows}
        bundle = bundle_svc.render_kea_bundle(server, scopes, templates_by_id)
        server.bundle_cache_at = datetime.now(UTC)
        server.bundle_cache_etag = bundle.etag
        server.bundle_cache_json = {
            "server_id": bundle.server_id,
            "ctrl_agent": bundle.ctrl_agent,
            "dhcp4": bundle.dhcp4,
            "dhcp6": bundle.dhcp6,
            "etag": bundle.etag,
        }
        await db.commit()
    log.info(
        "rerender_dhcp_bundle.done",
        server_id=server_id, etag=bundle.etag,
        v4=len(bundle.dhcp4.get("subnet4", [])),
        v6=len(bundle.dhcp6.get("subnet6", [])),
    )
    return {
        "server_id": server_id,
        "etag": bundle.etag,
        "v4_subnets": len(bundle.dhcp4.get("subnet4", [])),
        "v6_subnets": len(bundle.dhcp6.get("subnet6", [])),
    }


async def dhcp_drift_check(_ctx) -> dict:
    """PR 81 — refresh persisted drift state across every enabled
    DhcpServer. Calls services.dhcp_push.diff_all_scopes once per
    server; the orchestrator already writes last_diff_* on each scope.

    Failures on one server don't abort the sweep — the per-server
    diff_all_scopes call returns per-scope errors in its report, and
    a transport failure to one server is logged + skipped here. The
    LIST endpoint's ?diff_status= filter and the push-drifted route
    read whatever this leaves on the rows.
    """
    from .models.ipam import DhcpServer  # local — same defer pattern as dhcp_sync
    from .services import dhcp_alerts, dhcp_push as dhcp_push_svc

    started = datetime.now(UTC)
    per_server: dict[str, dict] = {}
    errors = 0
    total_alerts_fired = 0
    total_alerts_resolved = 0
    async with async_session() as db:
        servers = (
            await db.execute(
                select(DhcpServer).where(DhcpServer.enabled.is_(True))
            )
        ).scalars().all()
        for srv in servers:
            try:
                report = await dhcp_push_svc.diff_all_scopes(db, srv)
                # PR 86 — surface newly-drifted scopes via the alert
                # notification channels (Slack / email / webhook).
                # Failures inside the dispatcher are caught + logged
                # there; this call won't raise.
                alert_summary = await dhcp_alerts.notify_drift_transitions(
                    db, srv, report.transitions,
                )
                total_alerts_fired += alert_summary.get("fired", 0)
                total_alerts_resolved += alert_summary.get("resolved", 0)
                per_server[str(srv.id)] = {
                    "total": report.total,
                    "counts": report.counts,
                    "alerts": alert_summary,
                }
            except Exception as e:  # noqa: BLE001 — log + continue
                errors += 1
                per_server[str(srv.id)] = {"error": f"{type(e).__name__}: {e}"}
                log.error(
                    "dhcp_drift_check.server_error",
                    server_id=str(srv.id), error=str(e),
                )
        await db.commit()
    elapsed = (datetime.now(UTC) - started).total_seconds()
    log.info(
        "dhcp_drift_check.done",
        servers=len(per_server), errors=errors,
        alerts_fired=total_alerts_fired,
        alerts_resolved=total_alerts_resolved,
        elapsed_s=round(elapsed, 3),
    )
    return {
        "servers": len(per_server),
        "errors": errors,
        "alerts_fired": total_alerts_fired,
        "alerts_resolved": total_alerts_resolved,
        "elapsed_s": elapsed,
        "per_server": per_server,
    }


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
    # No-op when settings.otel_enabled is False (the default).
    from .observability import install_worker_tracing
    install_worker_tracing()
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
        dhcp_sync, dhcp_age_out, dhcp_drift_check, rerender_dhcp_bundle,
        dns_sync_from_ipam,
        dns_rotate_zsks, dns_purge_metrics,
        notify_bridge,
        # Region-deploy orchestrator — enqueued on demand from the
        # API when an operator clicks Start. No cron entry.
        run_region_deploy,
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
        # PR 81 — refresh persisted drift state. Default :09 / :24 /
        # :39 / :54 (every 15 min, offset from the lease sync so the
        # two cron groups don't pile up). Each tick walks every
        # enabled DhcpServer; for tens of servers with tens of scopes
        # each, well under a minute.
        cron(dhcp_drift_check, minute={9, 24, 39, 54}),
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
