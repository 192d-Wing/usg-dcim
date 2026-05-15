"""Alert evaluation engine.

Periodic worker job:
  1. For each enabled rule, scan the telemetry_samples hypertable for the
     max `value` per asset_id matching `metric` within `duration_seconds`.
  2. If the threshold is violated for the entire duration, upsert an Alert with a stable
     dedupe_key.  If a matching firing alert exists, bump last_seen_at.
  3. Resolve alerts whose latest reading no longer violates the threshold.
  4. Suppress alerts that fall inside an active MaintenanceWindow whose
     asset_filter_json matches the offending asset (empty filter = whole site).

Step 2b of the OpenSearch → TimescaleDB migration: this reader now queries
the hypertable instead of OpenSearch. The (asset_id, metric, ts) index
covers the per-rule scan directly.

Also runs a "collector-down" sweep that checks Collector.last_seen_at against
settings.collector_stale_seconds and synthesizes alerts for stale collectors.
"""

from __future__ import annotations

import operator
from datetime import UTC, datetime, timedelta

import structlog
from sqlalchemy import select, text
from sqlalchemy.ext.asyncio import AsyncSession

from .. import metrics
from ..models.alerts import Alert, AlertRule, AlertState, MaintenanceWindow, Severity
from ..models.collectors import Collector, CollectorStatus
from ..models.inventory import Asset
from ..settings import get_settings
from . import notifications as notif_svc

# Asset columns the maintenance-window asset_filter is allowed to scope on.
# Limited to direct Asset columns to keep _is_suppressed a single-row lookup;
# scoping by row_id would require a Rack join, which we can add later if
# operators ask for it.
ASSET_FILTER_KEYS: frozenset[str] = frozenset({
    "kind", "manufacturer", "model", "rack_id", "lifecycle_state",
})

log = structlog.get_logger("dcim.alerts")

_OPS = {
    ">": operator.gt, ">=": operator.ge,
    "<": operator.lt, "<=": operator.le,
    "==": operator.eq, "!=": operator.ne,
}


def dedupe_key(rule_id: str, asset_id: str, metric: str) -> str:
    return f"{rule_id}|{asset_id}|{metric}"


# Per-asset MAX(value) over a rule's metric within the duration window.
# Reproduces the existing OpenSearch aggregation exactly (the prior code
# named this bucket "latest" but it was always MAX, not value-at-latest-ts —
# a pre-existing quirk worth a follow-up after the cutover settles).
_LATEST_PER_ASSET_SQL = text("""
    SELECT asset_id, MAX(value) AS value
    FROM telemetry_samples
    WHERE metric = :metric
      AND ts >= :since
      AND (CAST(:site_id AS uuid) IS NULL
           OR site_id = CAST(:site_id AS uuid))
    GROUP BY asset_id
""")


async def _fetch_latest_per_asset(
    db: AsyncSession, rule: AlertRule, now: datetime,
) -> list[tuple[str, float]]:
    """One (asset_id, value) tuple per asset for the rule's metric+window.

    NULL site_scope_id means the rule applies enterprise-wide; we let
    Postgres skip the site predicate by comparing the parameter against NULL.
    """
    since = now - timedelta(seconds=rule.duration_seconds)
    site_id = str(rule.site_scope_id) if rule.site_scope_id else None
    result = await db.execute(
        _LATEST_PER_ASSET_SQL,
        {"metric": rule.metric, "since": since, "site_id": site_id},
    )
    return [(str(row.asset_id), float(row.value)) for row in result]


async def _apply_rule_to_asset(
    db: AsyncSession, rule: AlertRule, asset_id: str, value: float,
    *, violates: bool, now: datetime,
) -> tuple[Alert | None, Alert | None]:
    """Decide what (if anything) to do for one (rule, asset) reading.

    Returns (fired_alert, resolved_alert) — at most one of each is non-None.
    Splits evaluate_rules' inner branch out so the outer loop stays simple.
    """
    key = dedupe_key(str(rule.id), asset_id, rule.metric)
    existing = (await db.execute(
        select(Alert).where(Alert.dedupe_key == key, Alert.state == AlertState.firing)
    )).scalar_one_or_none()

    if violates and existing is None:
        if await _is_suppressed(db, rule.site_scope_id, asset_id):
            return None, None
        new_alert = Alert(
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
        db.add(new_alert)
        metrics.alerts_fired.labels(severity=rule.severity.value).inc()
        return new_alert, None
    if violates:
        existing.last_seen_at = now
        return None, None
    if existing is not None:
        existing.state = AlertState.resolved
        existing.resolved_at = now
        metrics.alerts_resolved.inc()
        return None, existing
    return None, None


async def _evaluate_one_rule(
    db: AsyncSession, rule: AlertRule, eval_now: datetime,
) -> tuple[list[Alert], list[Alert]]:
    cmp = _OPS[rule.operator]
    rows = await _fetch_latest_per_asset(db, rule, eval_now)
    fired: list[Alert] = []
    resolved: list[Alert] = []
    for asset_id, value in rows:
        violates = cmp(value, rule.threshold) if value is not None else False
        f, r = await _apply_rule_to_asset(
            db, rule, asset_id, value,
            violates=violates, now=datetime.now(UTC),
        )
        if f is not None:
            fired.append(f)
        if r is not None:
            resolved.append(r)
    return fired, resolved


async def evaluate_rules(db: AsyncSession) -> dict:
    rules = (await db.execute(select(AlertRule).where(AlertRule.enabled.is_(True)))).scalars().all()
    fired_alerts: list[Alert] = []
    resolved_alerts: list[Alert] = []
    eval_now = datetime.now(UTC)
    for rule in rules:
        if rule.operator not in _OPS:
            continue
        f, r = await _evaluate_one_rule(db, rule, eval_now)
        fired_alerts.extend(f)
        resolved_alerts.extend(r)
    await db.commit()
    fired = len(fired_alerts)
    resolved = len(resolved_alerts)
    metrics.alert_eval_runs.labels(outcome="ok").inc()
    # Notifications fire after the commit so we never ship a webhook for an
    # alert that didn't actually persist.
    for a in fired_alerts:
        await notif_svc.dispatch_fire(db, a)
    for a in resolved_alerts:
        await notif_svc.dispatch_resolve(db, a)
    log.info("alerts_evaluated", fired=fired, resolved=resolved, rules=len(rules))
    return {"fired": fired, "resolved": resolved, "rules": len(rules)}


def _coerce(v):
    return v.value if hasattr(v, "value") else v


def asset_matches_filter(asset_attrs: dict, filter_dict: dict) -> bool:
    """Match a Pythonic asset-attrs dict against a maintenance-window filter.

    Empty filter matches everything (window covers the whole site). A scalar
    value is equality; a list value is set membership. Unknown keys cause a
    fail-safe miss — better to fire an extra alert than to silently suppress.
    Enum-valued attrs are compared on their `.value` so JSON callers can pass
    plain strings.
    """
    if not filter_dict:
        return True
    if filter_dict.keys() - ASSET_FILTER_KEYS:
        return False
    for key, expected in filter_dict.items():
        actual = _coerce(asset_attrs.get(key))
        if isinstance(expected, list):
            if actual not in [_coerce(e) for e in expected]:
                return False
        elif actual != _coerce(expected):
            return False
    return True


async def _is_suppressed(db: AsyncSession, site_id, asset_id) -> bool:
    if site_id is None:
        return False
    now = datetime.now(UTC)
    windows = (await db.execute(
        select(MaintenanceWindow).where(
            MaintenanceWindow.site_id == site_id,
            MaintenanceWindow.starts_at <= now,
            MaintenanceWindow.ends_at >= now,
        )
    )).scalars().all()
    if not windows:
        return False
    # Hot path: a window with no asset filter covers the whole site, so we
    # can short-circuit before touching the assets table.
    if any(not w.asset_filter_json for w in windows):
        return True
    asset = (await db.execute(
        select(Asset).where(Asset.id == asset_id)
    )).scalar_one_or_none()
    if asset is None:
        return False
    asset_attrs = {
        "kind": asset.kind,
        "manufacturer": asset.manufacturer,
        "model": asset.model,
        "rack_id": str(asset.rack_id) if asset.rack_id else None,
        "lifecycle_state": asset.lifecycle_state,
    }
    return any(asset_matches_filter(asset_attrs, w.asset_filter_json) for w in windows)


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
    fired_alerts: list[Alert] = []
    for c in stale:
        c.status = CollectorStatus.stale
        key = f"collector-down|{c.id}"
        existing = (
            await db.execute(
                select(Alert).where(Alert.dedupe_key == key, Alert.state == AlertState.firing)
            )
        ).scalar_one_or_none()
        if existing is None:
            new_alert = Alert(
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
            db.add(new_alert)
            fired += 1
            metrics.alerts_fired.labels(severity=Severity.major.value).inc()
            fired_alerts.append(new_alert)
        else:
            existing.last_seen_at = now
    await db.commit()
    for a in fired_alerts:
        await notif_svc.dispatch_fire(db, a)
    return {"stale": len(stale), "fired": fired}
