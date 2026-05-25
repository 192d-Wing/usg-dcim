"""Drift alerts via NotificationChannel (PR 86 + PR 87).

PR 86 dispatched transient Alert-shaped objects; PR 87 makes them
real `alert.Alert` rows. Operators get the full ack/resolve UX
they already have for metric-rule alerts, dedupe survives across
cron runs, and recovery (drifted → in_sync) emits a resolve event
that closes the firing row.

Drift alerts are fabric-rooted — DhcpScope.dhcp_server lives in a
fabric, not a single site — so Alert.site_id is NULL (migration
0059 dropped the NOT NULL constraint). LIST queries that filter
on site_id naturally exclude drift; a drift-dashboard reads
labels_json.metric == 'dhcp_scope_drift' or dedupe_key prefix.

Dedupe key shape: `dhcp-drift:<scope_id>` so a scope that flaps
drifted → in_sync → drifted reuses one Alert row (transitions
state firing ↔ resolved instead of creating a fresh row each
time).

Dispatch matrix (unchanged from PR 86 except resolve now lands):
  - in_sync → drifted   → fire   (new Alert row or re-open existing)
  - None → drifted      → fire   (cold start)
  - drifted → in_sync   → resolve (transitions firing → resolved;
                         dispatches resolve event to channels)
  - error → drifted     → no-op  (error states flap; don't churn)
  - * → not-drifted     → no-op  (drifted → error doesn't auto-resolve;
                         the underlying drift is still real, just
                         can't be measured)
"""

from __future__ import annotations

from datetime import UTC, datetime

import structlog
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from ..models.alerts import Alert, AlertState, Severity
from . import notifications as notif_svc

log = structlog.get_logger("dcim.dhcp_alerts")


def _dedupe_key(scope_id: str) -> str:
    return f"dhcp-drift:{scope_id}"


def _summary(server, transition: dict) -> str:
    return f"DHCP scope drifted: {transition['prefix']} on {server.name}"


def _detail(transition: dict) -> str:
    return (
        f"Scope {transition['scope_id']} "
        f"(prefix {transition['prefix']}) "
        f"transitioned {transition['from_status'] or '<unknown>'} "
        f"→ {transition['to_status']} on the periodic drift check. "
        f"Run GET /api/v1/ipam/dhcp/scopes/{transition['scope_id']}/diff "
        f"to inspect the delta."
    )


def _labels(transition: dict) -> dict:
    return {
        "metric": "dhcp_scope_drift",
        "scope_id": transition["scope_id"],
        "prefix": transition["prefix"],
    }


async def _fire_drift_alert(
    db: AsyncSession, server, transition: dict,
) -> Alert:
    """Upsert a firing Alert row for the drifted scope.

    If a firing row already exists for this dedupe_key, bump
    last_seen_at and reuse it (the cron observed the drift again).
    If a resolved row exists, transition it back to firing (re-open
    a recently-recovered drift that drifted again).
    If no row exists, create one.
    """
    key = _dedupe_key(transition["scope_id"])
    now = datetime.now(UTC)
    existing = (await db.execute(
        select(Alert).where(Alert.dedupe_key == key).order_by(Alert.created_at.desc()).limit(1)
    )).scalar_one_or_none()
    if existing is not None and existing.state == AlertState.firing:
        existing.last_seen_at = now
        return existing
    if existing is not None and existing.state == AlertState.resolved:
        existing.state = AlertState.firing
        existing.resolved_at = None
        existing.last_seen_at = now
        existing.summary = _summary(server, transition)
        existing.detail = _detail(transition)
        existing.labels_json = _labels(transition)
        return existing
    alert = Alert(
        rule_id=None,
        site_id=None,
        asset_id=None,
        severity=Severity.warning,
        state=AlertState.firing,
        dedupe_key=key,
        summary=_summary(server, transition),
        detail=_detail(transition),
        first_seen_at=now,
        last_seen_at=now,
        labels_json=_labels(transition),
    )
    db.add(alert)
    await db.flush()
    return alert


async def _resolve_drift_alert(
    db: AsyncSession, transition: dict,
) -> Alert | None:
    """Transition the firing drift alert (if any) for this scope to
    resolved. Returns the resolved row so the caller can dispatch a
    resolve event to channels. None if no firing row exists.
    """
    key = _dedupe_key(transition["scope_id"])
    existing = (await db.execute(
        select(Alert).where(Alert.dedupe_key == key, Alert.state == AlertState.firing)
    )).scalar_one_or_none()
    if existing is None:
        return None
    existing.state = AlertState.resolved
    existing.resolved_at = datetime.now(UTC)
    await db.flush()
    return existing


async def notify_drift_transitions(
    db: AsyncSession, server, transitions: list[dict],
) -> dict:
    """PR 87 — persist + dispatch drift events.

    Transition filter:
      - to_status='drifted' with from_status in (None, in_sync) →
        fire (upsert Alert row, dispatch_fire to channels).
      - from_status='drifted' with to_status='in_sync' →
        resolve (transition firing row to resolved, dispatch_resolve).
      - everything else → no-op.
    """
    fired = resolved = delivered = failed = 0
    for t in transitions:
        prior = t.get("from_status")
        nxt = t.get("to_status")
        try:
            if nxt == "drifted" and prior in (None, "in_sync"):
                alert = await _fire_drift_alert(db, server, t)
                outcomes = await notif_svc.dispatch_fire(db, alert)
                fired += 1
            elif prior == "drifted" and nxt == "in_sync":
                alert = await _resolve_drift_alert(db, t)
                if alert is None:
                    continue  # nothing to resolve — alert was never persisted
                outcomes = await notif_svc.dispatch_resolve(db, alert)
                resolved += 1
            else:
                continue
        except Exception as e:  # noqa: BLE001 — never crash the cron
            failed += 1
            log.warning(
                "dhcp_alert.dispatch_failed",
                scope_id=t.get("scope_id"), error=f"{type(e).__name__}: {e}",
            )
            continue
        for o in outcomes:
            if o.delivered:
                delivered += 1
            else:
                failed += 1
    return {
        "fired": fired,
        "resolved": resolved,
        "delivered": delivered,
        "failed": failed,
    }
