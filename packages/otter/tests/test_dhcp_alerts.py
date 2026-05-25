"""Unit tests for the DHCP drift alert dispatcher (PR 86).

Pure: pins the transition-filter logic (which transitions fire and
which don't), the transient-Alert object shape, and the cron-side
contract. The notification senders themselves (Slack/email/webhook)
are exercised by services.notifications tests; here we stub the
dispatcher and assert what it received.
"""

from __future__ import annotations

import asyncio
from dataclasses import dataclass

from dcim.models.alerts import AlertState, Severity
from dcim.services import dhcp_alerts


@dataclass
class _Server:
    id: object
    name: str = "kea-east-1"


def _server() -> _Server:
    return _Server(id="server-id")


def _run(coro):
    return asyncio.run(coro)


def test_in_sync_to_drifted_transition_fires_alert(monkeypatch):
    seen: list = []

    async def fake_dispatch_fire(_db, alert):
        seen.append(alert)
        return []

    monkeypatch.setattr(
        dhcp_alerts.notif_svc, "dispatch_fire", fake_dispatch_fire,
    )
    out = _run(dhcp_alerts.notify_drift_transitions(
        db=None, server=_server(),
        transitions=[{
            "scope_id": "sc-1", "prefix": "10.0.0.0/24",
            "from_status": "in_sync", "to_status": "drifted",
        }],
    ))
    assert out["fired"] == 1
    assert len(seen) == 1
    alert = seen[0]
    assert alert.severity == Severity.warning
    assert alert.state == AlertState.firing
    assert "10.0.0.0/24" in alert.summary
    assert alert.dedupe_key == "dhcp-drift:sc-1"


def test_cold_start_none_to_drifted_also_fires(monkeypatch):
    # First cron run on a fresh server: every scope's prior_status is
    # None. None → drifted is a real transition the operator wants
    # to hear about (otherwise the alert never fires post-cold-start).
    seen: list = []

    async def fake_dispatch_fire(_db, alert):
        seen.append(alert)
        return []

    monkeypatch.setattr(
        dhcp_alerts.notif_svc, "dispatch_fire", fake_dispatch_fire,
    )
    out = _run(dhcp_alerts.notify_drift_transitions(
        db=None, server=_server(),
        transitions=[{
            "scope_id": "sc-2", "prefix": "10.0.1.0/24",
            "from_status": None, "to_status": "drifted",
        }],
    ))
    assert out["fired"] == 1
    assert len(seen) == 1


def test_drifted_to_in_sync_recovery_does_not_fire(monkeypatch):
    # Recovery is real but doesn't double-fire because there's no
    # live Alert row to resolve. A future PR can add real persistence
    # + resolve hooks; today we skip to keep noise low.
    seen: list = []

    async def fake_dispatch_fire(_db, alert):
        seen.append(alert)
        return []

    monkeypatch.setattr(
        dhcp_alerts.notif_svc, "dispatch_fire", fake_dispatch_fire,
    )
    out = _run(dhcp_alerts.notify_drift_transitions(
        db=None, server=_server(),
        transitions=[{
            "scope_id": "sc-3", "prefix": "10.0.0.0/24",
            "from_status": "drifted", "to_status": "in_sync",
        }],
    ))
    assert out["fired"] == 0
    assert seen == []


def test_error_to_drifted_does_not_fire(monkeypatch):
    # Movement inside the bad-state group (error → drifted) doesn't
    # fire either. Error states can flap and we don't want to wake
    # the operator on every blip.
    seen: list = []

    async def fake_dispatch_fire(_db, alert):
        seen.append(alert)
        return []

    monkeypatch.setattr(
        dhcp_alerts.notif_svc, "dispatch_fire", fake_dispatch_fire,
    )
    out = _run(dhcp_alerts.notify_drift_transitions(
        db=None, server=_server(),
        transitions=[{
            "scope_id": "sc-4", "prefix": "10.0.0.0/24",
            "from_status": "error", "to_status": "drifted",
        }],
    ))
    assert out["fired"] == 0


def test_dispatcher_exception_does_not_abort_remaining_transitions(monkeypatch):
    # One bad dispatch should not block alerts for subsequent
    # transitions in the same cron pass.
    seen: list = []
    call_count = {"n": 0}

    async def flaky_dispatch_fire(_db, alert):
        call_count["n"] += 1
        if call_count["n"] == 1:
            raise RuntimeError("simulated webhook 500")
        seen.append(alert)
        return []

    monkeypatch.setattr(
        dhcp_alerts.notif_svc, "dispatch_fire", flaky_dispatch_fire,
    )
    out = _run(dhcp_alerts.notify_drift_transitions(
        db=None, server=_server(),
        transitions=[
            {"scope_id": "sc-a", "prefix": "10.0.0.0/24",
             "from_status": "in_sync", "to_status": "drifted"},
            {"scope_id": "sc-b", "prefix": "10.0.1.0/24",
             "from_status": "in_sync", "to_status": "drifted"},
        ],
    ))
    # Second alert fired; first counted as failed.
    assert out["fired"] == 1
    assert out["failed"] == 1
    assert len(seen) == 1


def test_no_transitions_returns_zero_counts():
    # Empty input is the steady-state case (nothing changed since
    # last cron). Don't pretend to dispatch anything.
    out = _run(dhcp_alerts.notify_drift_transitions(
        db=None, server=_server(), transitions=[],
    ))
    assert out == {"fired": 0, "delivered": 0, "failed": 0}


def test_bulk_diff_report_carries_transitions_field():
    # Wiring check — the dataclass exposes the field the cron reads.
    from dcim.services.dhcp_push import BulkDiffReport
    fields = {f.name for f in BulkDiffReport.__dataclass_fields__.values()}
    assert "transitions" in fields
