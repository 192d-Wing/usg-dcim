"""Drift alerts via NotificationChannel (PR 86).

The scheduled drift cron (PR 81 + 80) refreshes per-scope drift state
every 15 minutes. This module dispatches a notification when a scope
transitions out of in_sync — operators hear about new drift without
having to watch the LIST endpoint.

Transient Alert object: services.notifications.dispatch operates on
attributes (severity, summary, state, site_id, detail, ...), not on
a persisted row. We construct a SimpleNamespace that quacks like an
Alert and pass it through. No Alert table writes — drift notifications
are notification-only, not alert-engine alerts.

Dispatch scope: only `in_sync → drifted` transitions fire. The opposite
(drifted → in_sync) is a recovery; we don't double up with a "resolve"
event today because there's no live Alert row to resolve. A future PR
can add real Alert persistence + resolve hooks.

Cold start (every scope's prior_status is None) emits a flood of
"first-ever drift" alerts if any scope is currently drifted. That's
the expected behavior on a freshly-deployed system — operator sees
the state of the fleet. Subsequent runs only alert on changes.
"""

from __future__ import annotations

from datetime import UTC, datetime
from types import SimpleNamespace
from uuid import uuid4

import structlog
from sqlalchemy.ext.asyncio import AsyncSession

from ..models.alerts import AlertState, Severity
from . import notifications as notif_svc

log = structlog.get_logger("dcim.dhcp_alerts")


def _build_drift_alert(server, transition: dict):
    """Construct the transient Alert-shaped object passed to dispatch.

    The notification senders read these attributes (see
    services/notifications.py format_*_payload):
      id, severity, state, summary, detail, site_id, asset_id,
      first_seen_at, last_seen_at, dedupe_key.
    """
    now = datetime.now(UTC)
    return SimpleNamespace(
        id=uuid4(),
        severity=Severity.warning,
        state=AlertState.firing,
        summary=(
            f"DHCP scope drifted: {transition['prefix']} "
            f"on {server.name}"
        ),
        detail=(
            f"Scope {transition['scope_id']} "
            f"(prefix {transition['prefix']}) "
            f"transitioned {transition['from_status'] or '<unknown>'} "
            f"→ {transition['to_status']} on the periodic drift check. "
            f"Run GET /api/v1/ipam/dhcp/scopes/{transition['scope_id']}/diff "
            f"to inspect the delta."
        ),
        site_id=None,
        asset_id=None,
        first_seen_at=now,
        last_seen_at=now,
        dedupe_key=f"dhcp-drift:{transition['scope_id']}",
    )


async def notify_drift_transitions(
    db: AsyncSession, server, transitions: list[dict],
) -> dict:
    """Dispatch notifications for the newly-drifted scopes in the
    transitions list. Returns counts so the caller can log.

    Only emits on `in_sync → drifted` and `None → drifted` transitions.
    Other movements (drifted → in_sync recovery, error → drifted, etc.)
    are intentionally not surfaced today — they would either be too
    noisy (error states flap) or want a paired resolve event we don't
    persist (recovery).
    """
    fired = 0
    delivered = 0
    failed = 0
    for t in transitions:
        if t.get("to_status") != "drifted":
            continue
        prior = t.get("from_status")
        if prior not in (None, "in_sync"):
            # Don't alert on error → drifted or missing_from_kea →
            # drifted; those are transitions inside the "bad" group.
            continue
        alert = _build_drift_alert(server, t)
        try:
            outcomes = await notif_svc.dispatch_fire(db, alert)
        except Exception as e:  # noqa: BLE001 — never crash the cron
            failed += 1
            log.warning(
                "dhcp_alert.dispatch_failed",
                scope_id=t.get("scope_id"), error=f"{type(e).__name__}: {e}",
            )
            continue
        fired += 1
        for o in outcomes:
            if o.delivered:
                delivered += 1
            else:
                failed += 1
    return {"fired": fired, "delivered": delivered, "failed": failed}
