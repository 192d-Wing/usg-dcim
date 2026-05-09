"""Alert evaluation engine.

Periodic worker job:
  1. For each enabled rule, query Elastic for the latest values matching its asset_filter
     within `duration_seconds`.
  2. If the threshold is violated for the entire duration, upsert an Alert with a stable
     dedupe_key.  If a matching firing alert exists, bump last_seen_at.
  3. Resolve alerts whose latest reading no longer violates the threshold.
  4. Suppress alerts that fall inside an active MaintenanceWindow.

Also runs a "collector-down" sweep that checks Collector.last_seen_at against
settings.collector_stale_seconds and synthesizes alerts for stale collectors.
"""

from __future__ import annotations

import operator
from datetime import UTC, datetime, timedelta

import structlog
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from .. import metrics
from ..models.alerts import Alert, AlertRule, AlertState, MaintenanceWindow, Severity
from ..models.collectors import Collector, CollectorStatus
from ..settings import get_settings
from .elastic import client, telemetry_index

log = structlog.get_logger("dcim.alerts")

_OPS = {
    ">": operator.gt, ">=": operator.ge,
    "<": operator.lt, "<=": operator.le,
    "==": operator.eq, "!=": operator.ne,
}


def dedupe_key(rule_id: str, asset_id: str, metric: str) -> str:
    return f"{rule_id}|{asset_id}|{metric}"


async def evaluate_rules(db: AsyncSession) -> dict:
    rules = (await db.execute(select(AlertRule).where(AlertRule.enabled.is_(True)))).scalars().all()
    fired = 0
    resolved = 0
    for rule in rules:
        if rule.operator not in _OPS:
            continue
        cmp = _OPS[rule.operator]
        site_filter = rule.site_scope_id
        site_arg = str(site_filter) if site_filter else "*"
        index = telemetry_index(site_arg)
        es = client()
        resp = await es.search(
            index=index,
            body={
                "size": 0,
                "query": {
                    "bool": {
                        "filter": [
                            {"term": {"metric": rule.metric}},
                            {
                                "range": {
                                    "ts": {
                                        "gte": (
                                            datetime.now(UTC)
                                            - timedelta(seconds=rule.duration_seconds)
                                        ).isoformat()
                                    }
                                }
                            },
                        ]
                    }
                },
                "aggs": {
                    "by_asset": {
                        "terms": {"field": "asset_id", "size": 10000},
                        "aggs": {"latest": {"max": {"field": "value"}}},
                    }
                },
            },
            ignore_unavailable=True,
        )
        buckets = resp.get("aggregations", {}).get("by_asset", {}).get("buckets", [])
        for b in buckets:
            asset_id = b["key"]
            value = b["latest"]["value"]
            violates = cmp(value, rule.threshold) if value is not None else False
            key = dedupe_key(str(rule.id), asset_id, rule.metric)
            existing = (
                await db.execute(
                    select(Alert).where(Alert.dedupe_key == key, Alert.state == AlertState.firing)
                )
            ).scalar_one_or_none()
            now = datetime.now(UTC)
            if violates:
                if existing is None:
                    if await _is_suppressed(db, rule.site_scope_id):
                        continue
                    db.add(
                        Alert(
                            rule_id=rule.id,
                            site_id=rule.site_scope_id,  # type: ignore[arg-type]
                            asset_id=asset_id,
                            severity=rule.severity,
                            state=AlertState.firing,
                            dedupe_key=key,
                            summary=f"{rule.metric} {rule.operator} {rule.threshold} (got {value:.2f})",
                            detail=rule.description,
                            first_seen_at=now,
                            last_seen_at=now,
                            labels_json={"metric": rule.metric, "rule": rule.name},
                        )
                    )
                    fired += 1
                    metrics.alerts_fired.labels(severity=rule.severity.value).inc()
                else:
                    existing.last_seen_at = now
            elif existing is not None:
                existing.state = AlertState.resolved
                existing.resolved_at = now
                resolved += 1
                metrics.alerts_resolved.inc()
    await db.commit()
    metrics.alert_eval_runs.labels(outcome="ok").inc()
    log.info("alerts_evaluated", fired=fired, resolved=resolved, rules=len(rules))
    return {"fired": fired, "resolved": resolved, "rules": len(rules)}


async def _is_suppressed(db: AsyncSession, site_id) -> bool:
    if site_id is None:
        return False
    now = datetime.now(UTC)
    res = await db.execute(
        select(MaintenanceWindow).where(
            MaintenanceWindow.site_id == site_id,
            MaintenanceWindow.starts_at <= now,
            MaintenanceWindow.ends_at >= now,
        )
    )
    return res.first() is not None


async def sweep_collectors(db: AsyncSession) -> dict:
    """Mark collectors stale/unreachable and fire collector-down alerts."""
    s = get_settings()
    now = datetime.now(UTC)
    threshold = now - timedelta(seconds=s.collector_stale_seconds)
    stale = (
        await db.execute(
            select(Collector).where(
                Collector.enabled.is_(True),
                Collector.last_seen_at.is_not(None),
                Collector.last_seen_at < threshold,
            )
        )
    ).scalars().all()
    fired = 0
    for c in stale:
        c.status = CollectorStatus.stale
        key = f"collector-down|{c.id}"
        existing = (
            await db.execute(
                select(Alert).where(Alert.dedupe_key == key, Alert.state == AlertState.firing)
            )
        ).scalar_one_or_none()
        if existing is None:
            db.add(
                Alert(
                    site_id=c.site_id,
                    collector_id=c.id,
                    severity=Severity.major,
                    state=AlertState.firing,
                    dedupe_key=key,
                    summary=f"Collector {c.name} has not reported since {c.last_seen_at:%Y-%m-%d %H:%M UTC}",
                    first_seen_at=now,
                    last_seen_at=now,
                    labels_json={"kind": "collector_down", "collector": c.name},
                )
            )
            fired += 1
            metrics.alerts_fired.labels(severity=Severity.major.value).inc()
        else:
            existing.last_seen_at = now
    await db.commit()
    return {"stale": len(stale), "fired": fired}
