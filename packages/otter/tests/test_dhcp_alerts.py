"""Unit tests for the DHCP drift alert dispatcher (PR 86 + PR 87).

Pure: pins the transition-filter logic (which transitions fire,
which resolve, which are no-ops), the dedupe-key shape, and the
cron-side counts contract. The actual Alert persistence + DB
upsert path (`_fire_drift_alert`, `_resolve_drift_alert`) runs
against a real DB via integration tests; here we stub them so
the filter logic can be exercised in isolation.
"""

from __future__ import annotations

import asyncio
from dataclasses import dataclass
from uuid import uuid4

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


def _stub_fire(monkeypatch, ret_alert=None):
    """Stub _fire_drift_alert to return a SimpleNamespace Alert. The
    real version upserts a row; tests don't need that."""
    seen: list = []

    async def fake_fire(_db, server, transition):
        from types import SimpleNamespace
        a = SimpleNamespace(
            id=uuid4(),
            severity=Severity.warning,
            state=AlertState.firing,
            summary=f"drift {transition['prefix']} on {server.name}",
            detail=f"transition {transition.get('from_status')}→{transition.get('to_status')}",
            site_id=None,
            asset_id=None,
            first_seen_at=None,
            last_seen_at=None,
            dedupe_key=dhcp_alerts._dedupe_key(transition["scope_id"]),
            labels_json={},
        )
        seen.append(("fire", a, transition))
        return ret_alert or a

    monkeypatch.setattr(dhcp_alerts, "_fire_drift_alert", fake_fire)
    return seen


def _stub_resolve(monkeypatch, ret_alert=None):
    seen: list = []

    async def fake_resolve(_db, transition):
        from types import SimpleNamespace
        if ret_alert == "missing":
            seen.append(("resolve_missing", transition))
            return None
        a = ret_alert or SimpleNamespace(
            id=uuid4(),
            severity=Severity.warning,
            state=AlertState.resolved,
            summary="resolved",
            detail=None,
            site_id=None,
            asset_id=None,
            first_seen_at=None,
            last_seen_at=None,
            resolved_at=None,
            dedupe_key=dhcp_alerts._dedupe_key(transition["scope_id"]),
            labels_json={},
        )
        seen.append(("resolve", a, transition))
        return a

    monkeypatch.setattr(dhcp_alerts, "_resolve_drift_alert", fake_resolve)
    return seen


def _stub_dispatch(monkeypatch):
    fired: list = []
    resolved: list = []

    async def fake_fire(_db, alert):
        fired.append(alert)
        return []

    async def fake_resolve(_db, alert):
        resolved.append(alert)
        return []

    monkeypatch.setattr(dhcp_alerts.notif_svc, "dispatch_fire", fake_fire)
    monkeypatch.setattr(dhcp_alerts.notif_svc, "dispatch_resolve", fake_resolve)
    return fired, resolved


# ----- transition filter: fires -----

def test_in_sync_to_drifted_fires_alert(monkeypatch):
    fire_seen = _stub_fire(monkeypatch)
    fired, _ = _stub_dispatch(monkeypatch)
    out = _run(dhcp_alerts.notify_drift_transitions(
        db=None, server=_server(),
        transitions=[{
            "scope_id": "sc-1", "prefix": "10.0.0.0/24",
            "from_status": "in_sync", "to_status": "drifted",
        }],
    ))
    assert out["fired"] == 1
    assert out["resolved"] == 0
    assert len(fire_seen) == 1
    assert len(fired) == 1


def test_none_to_drifted_cold_start_also_fires(monkeypatch):
    _stub_fire(monkeypatch)
    fired, _ = _stub_dispatch(monkeypatch)
    out = _run(dhcp_alerts.notify_drift_transitions(
        db=None, server=_server(),
        transitions=[{
            "scope_id": "sc-2", "prefix": "10.0.1.0/24",
            "from_status": None, "to_status": "drifted",
        }],
    ))
    assert out["fired"] == 1
    assert len(fired) == 1


# ----- transition filter: resolves -----

def test_drifted_to_in_sync_resolves(monkeypatch):
    # PR 87 — recovery now fires a resolve event (PR 86 dropped it).
    _stub_resolve(monkeypatch)
    _, resolved = _stub_dispatch(monkeypatch)
    out = _run(dhcp_alerts.notify_drift_transitions(
        db=None, server=_server(),
        transitions=[{
            "scope_id": "sc-3", "prefix": "10.0.0.0/24",
            "from_status": "drifted", "to_status": "in_sync",
        }],
    ))
    assert out["resolved"] == 1
    assert out["fired"] == 0
    assert len(resolved) == 1


def test_drifted_to_in_sync_with_no_firing_alert_is_noop(monkeypatch):
    # _resolve_drift_alert returns None when there's no firing row to
    # resolve (e.g. cron observed the drift transition but the prior
    # fire happened before PR 87, so no persisted alert exists).
    _stub_resolve(monkeypatch, ret_alert="missing")
    _, resolved = _stub_dispatch(monkeypatch)
    out = _run(dhcp_alerts.notify_drift_transitions(
        db=None, server=_server(),
        transitions=[{
            "scope_id": "sc-3b", "prefix": "10.0.0.0/24",
            "from_status": "drifted", "to_status": "in_sync",
        }],
    ))
    assert out["resolved"] == 0
    assert len(resolved) == 0


# ----- transition filter: no-ops -----

def test_error_to_drifted_does_not_fire(monkeypatch):
    fire_seen = _stub_fire(monkeypatch)
    fired, _ = _stub_dispatch(monkeypatch)
    out = _run(dhcp_alerts.notify_drift_transitions(
        db=None, server=_server(),
        transitions=[{
            "scope_id": "sc-4", "prefix": "10.0.0.0/24",
            "from_status": "error", "to_status": "drifted",
        }],
    ))
    assert out["fired"] == 0
    assert len(fire_seen) == 0
    assert len(fired) == 0


def test_drifted_to_error_does_not_auto_resolve(monkeypatch):
    # The drift is still real; we can't measure it but the firing
    # alert should not auto-clear. No-op is the safer default.
    _stub_resolve(monkeypatch)
    _, resolved = _stub_dispatch(monkeypatch)
    out = _run(dhcp_alerts.notify_drift_transitions(
        db=None, server=_server(),
        transitions=[{
            "scope_id": "sc-5", "prefix": "10.0.0.0/24",
            "from_status": "drifted", "to_status": "error",
        }],
    ))
    assert out["resolved"] == 0
    assert len(resolved) == 0


# ----- failure isolation -----

def test_dispatcher_exception_does_not_abort_remaining_transitions(monkeypatch):
    _stub_fire(monkeypatch)
    call_count = {"n": 0}

    async def flaky_fire(_db, alert):
        call_count["n"] += 1
        if call_count["n"] == 1:
            raise RuntimeError("simulated webhook 500")
        return []

    monkeypatch.setattr(dhcp_alerts.notif_svc, "dispatch_fire", flaky_fire)
    monkeypatch.setattr(
        dhcp_alerts.notif_svc, "dispatch_resolve",
        lambda _db, _a: asyncio.sleep(0, result=[]),  # not used
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


def test_no_transitions_returns_zero_counts():
    out = _run(dhcp_alerts.notify_drift_transitions(
        db=None, server=_server(), transitions=[],
    ))
    assert out == {"fired": 0, "resolved": 0, "delivered": 0, "failed": 0}


# ----- dedupe key shape -----

def test_dedupe_key_is_scope_scoped():
    # Same scope ID across multiple drift episodes reuses one Alert
    # row (via dedupe_key matching in _fire_drift_alert).
    assert dhcp_alerts._dedupe_key("scope-uuid-1") == "dhcp-drift:scope-uuid-1"


# ----- BulkDiffReport carries transitions -----

def test_bulk_diff_report_carries_transitions_field():
    from dcim.services.dhcp_push import BulkDiffReport
    fields = {f.name for f in BulkDiffReport.__dataclass_fields__.values()}
    assert "transitions" in fields
