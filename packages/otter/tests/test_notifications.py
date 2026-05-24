"""Unit + integration tests for the notification dispatcher.

The pure tests pin the routing predicate (`channel_matches`) and the
payload formatters. The dispatch-integration tests below cover the
glue between the alert engine and the per-kind adapters — that's
where webhook delivery to silence actually happens, and the silent
class of failure (rule fires, alert persists, channel iterates,
adapter quietly stubs) is exactly what the integration coverage
prevents.
"""

import asyncio
from datetime import UTC, datetime
from types import SimpleNamespace
from uuid import uuid4

import pytest

from dcim.models.alerts import AlertState, Severity
from dcim.models.notifications import ChannelKind
from dcim.services import notifications as notif_svc
from dcim.services.notifications import (
    channel_matches,
    dispatch,
    format_email_subject_body,
    format_slack_payload,
    format_webhook_payload,
)


def _channel(
    *, enabled=True, min_severity=Severity.warning,
    notify_on_fire=True, notify_on_resolve=True,
):
    return SimpleNamespace(
        enabled=enabled,
        min_severity=min_severity,
        notify_on_fire=notify_on_fire,
        notify_on_resolve=notify_on_resolve,
    )


def _alert(*, severity=Severity.major, state=AlertState.firing):
    now = datetime(2026, 5, 9, 12, 0, tzinfo=UTC)
    return SimpleNamespace(
        id=uuid4(),
        severity=severity,
        state=state,
        summary="pdu.input.kw > 8 (got 9.42)",
        detail="threshold breached for 60s",
        site_id=uuid4(),
        asset_id=uuid4(),
        first_seen_at=now,
        last_seen_at=now,
        labels_json={"metric": "pdu.input.kw", "rule": "PDU input kW above 8"},
    )


# ---------- channel_matches ----------

def test_disabled_channel_never_matches():
    c = _channel(enabled=False)
    assert channel_matches(c, Severity.critical, "fire") is False


def test_severity_floor_blocks_lower_alerts():
    c = _channel(min_severity=Severity.major)
    assert channel_matches(c, Severity.warning, "fire") is False
    assert channel_matches(c, Severity.minor, "fire") is False
    assert channel_matches(c, Severity.major, "fire") is True
    assert channel_matches(c, Severity.critical, "fire") is True


def test_event_filter_skips_disabled_directions():
    fire_only = _channel(notify_on_fire=True, notify_on_resolve=False)
    assert channel_matches(fire_only, Severity.critical, "fire") is True
    assert channel_matches(fire_only, Severity.critical, "resolve") is False

    resolve_only = _channel(notify_on_fire=False, notify_on_resolve=True)
    assert channel_matches(resolve_only, Severity.critical, "fire") is False
    assert channel_matches(resolve_only, Severity.critical, "resolve") is True


def test_default_warning_floor_admits_warning_and_above():
    c = _channel()  # warning floor by default
    for s in (Severity.warning, Severity.minor, Severity.major, Severity.critical):
        assert channel_matches(c, s, "fire") is True
    assert channel_matches(c, Severity.info, "fire") is False


# ---------- format_webhook_payload ----------

def test_webhook_payload_shape_is_stable():
    a = _alert()
    out = format_webhook_payload(a, event="fire")
    assert out["event"] == "alert.fire"
    assert out["alert"]["severity"] == "major"
    assert out["alert"]["state"] == "firing"
    assert out["alert"]["summary"].startswith("pdu.input.kw")
    assert out["alert"]["site_id"] == str(a.site_id)
    assert out["alert"]["asset_id"] == str(a.asset_id)
    assert out["alert"]["labels"] == a.labels_json
    # Timestamps come through as ISO strings, not datetime objects.
    assert isinstance(out["alert"]["first_seen_at"], str)
    assert isinstance(out["alert"]["last_seen_at"], str)


def test_webhook_payload_handles_null_site_and_asset():
    a = _alert()
    a.site_id = None
    a.asset_id = None
    out = format_webhook_payload(a, event="resolve")
    assert out["event"] == "alert.resolve"
    assert out["alert"]["site_id"] is None
    assert out["alert"]["asset_id"] is None


# ---------- format_slack_payload ----------

def test_slack_payload_includes_color_and_title():
    a = _alert(severity=Severity.critical)
    out = format_slack_payload(a, event="fire")
    assert "attachments" in out and len(out["attachments"]) == 1
    att = out["attachments"][0]
    assert att["color"] == "#d92626"  # critical red
    assert "[CRITICAL] alert.fire" in att["title"]
    assert att["text"] == a.summary


def test_slack_payload_unknown_severity_falls_back_to_grey():
    a = _alert(severity=Severity.info)
    out = format_slack_payload(a, event="fire")
    # info maps to a defined grey, not an empty string
    assert out["attachments"][0]["color"] == "#777777"


# ---------- format_email_subject_body ----------

def test_email_subject_includes_severity_and_event():
    a = _alert(severity=Severity.major)
    subject, _ = format_email_subject_body(a, event="fire")
    assert "[DCIM]" in subject
    assert "[MAJOR]" in subject
    assert "alert.fire" in subject
    assert a.summary in subject


def test_email_body_lists_all_relevant_fields():
    a = _alert()
    _, body = format_email_subject_body(a, event="resolve")
    for needle in ["Event:", "Severity:", "State:", "Summary:", "Detail:", "Site:", "Asset:"]:
        assert needle in body


# ---------- dispatch integration ----------
#
# dispatch() walks NotificationChannel rows, filters by channel_matches,
# routes each kind to the corresponding _send_* adapter, and isolates
# per-channel failures. None of these are exercised by the formatter
# tests above. The patterns below stand up the minimal fake-DB +
# adapter monkeypatching needed to lock the contract down.


def _db_returning(channels):
    """Fake AsyncSession whose `db.execute(...).scalars().all()` returns
    the provided channel list. Avoids spinning up a real session for
    pure dispatcher tests."""
    class _Scalars:
        def __init__(self, rows): self._rows = rows
        def all(self): return self._rows
    class _Result:
        def __init__(self, rows): self._rows = rows
        def scalars(self): return _Scalars(self._rows)
    class _DB:
        def __init__(self, rows): self._rows = rows
        async def execute(self, _stmt): return _Result(self._rows)
    return _DB(channels)


def _full_channel(**overrides):
    """Build a channel-shaped object with every field dispatch reads."""
    base = {
        "id": uuid4(),
        "name": "test-channel",
        "kind": ChannelKind.webhook,
        "config_json": {"url": "https://example.invalid/hook"},
        "enabled": True,
        "min_severity": Severity.warning,
        "notify_on_fire": True,
        "notify_on_resolve": True,
    }
    base.update(overrides)
    return SimpleNamespace(**base)


def test_dispatch_rejects_unknown_event():
    """Anything other than fire/resolve must raise — protects against a
    refactor accidentally introducing a third event type that no
    adapter handles."""
    with pytest.raises(ValueError, match="unknown event"):
        asyncio.run(dispatch(_db_returning([]), _alert(), event="ack"))


def test_dispatch_invokes_webhook_adapter_with_payload(monkeypatch):
    """Webhook-kind channels MUST route through _send_webhook with the
    expected JSON shape — silent stubbing here means alerts vanish."""
    captured = []

    async def fake_webhook(ch, payload):
        captured.append((ch.name, payload))

    monkeypatch.setattr(notif_svc, "_send_webhook", fake_webhook)
    ch = _full_channel(name="wh", kind=ChannelKind.webhook)
    alert = _alert(severity=Severity.major)
    outcomes = asyncio.run(dispatch(_db_returning([ch]), alert, event="fire"))
    assert len(captured) == 1
    name, payload = captured[0]
    assert name == "wh"
    assert payload["event"] == "alert.fire"
    assert payload["alert"]["severity"] == "major"
    assert outcomes[0].delivered is True
    assert outcomes[0].kind == "webhook"


def test_dispatch_routes_each_kind_to_its_own_adapter(monkeypatch):
    """A mixed channel set must hit the webhook adapter for webhook
    rows, the slack adapter for slack rows, and the email adapter for
    email rows. Cross-routing would be a silent disaster."""
    hits = {"webhook": 0, "slack": 0, "email": 0}

    async def fake_webhook(*_a, **_k): hits["webhook"] += 1
    async def fake_slack(*_a, **_k):   hits["slack"]   += 1
    async def fake_email(*_a, **_k):   hits["email"]   += 1

    monkeypatch.setattr(notif_svc, "_send_webhook", fake_webhook)
    monkeypatch.setattr(notif_svc, "_send_slack",   fake_slack)
    monkeypatch.setattr(notif_svc, "_send_email",   fake_email)

    channels = [
        _full_channel(name="wh", kind=ChannelKind.webhook),
        _full_channel(name="sl", kind=ChannelKind.slack,
                      config_json={"webhook_url": "https://hooks.invalid/x"}),
        _full_channel(name="em", kind=ChannelKind.email,
                      config_json={"recipients": ["ops@example"]}),
    ]
    asyncio.run(dispatch(_db_returning(channels), _alert(), event="fire"))
    assert hits == {"webhook": 1, "slack": 1, "email": 1}


def test_dispatch_skips_filtered_channels(monkeypatch):
    """channel_matches drops channels below severity floor or wrong
    direction. The adapter MUST NOT be invoked for filtered channels."""
    invoked = []

    async def fake_webhook(ch, _p):
        invoked.append(ch.name)

    monkeypatch.setattr(notif_svc, "_send_webhook", fake_webhook)
    channels = [
        _full_channel(name="too-quiet", min_severity=Severity.critical),
        _full_channel(name="wrong-dir", notify_on_fire=False),
        _full_channel(name="disabled", enabled=False),
        _full_channel(name="good"),
    ]
    outcomes = asyncio.run(
        dispatch(_db_returning(channels), _alert(severity=Severity.major), event="fire"),
    )
    assert invoked == ["good"]
    # Only the matched channel surfaces in outcomes — others are
    # filtered upstream and shouldn't pollute the audit trail.
    assert [o.channel_name for o in outcomes] == ["good"]


def test_dispatch_isolates_one_bad_channel_from_the_rest(monkeypatch):
    """A failing channel must NOT break others. This is the single most
    important property of dispatch — without it, one stale Slack
    webhook silences the entire alert engine."""
    async def fake_webhook(ch, _p):
        if ch.name == "broken":
            raise RuntimeError("Slack returned 503")

    monkeypatch.setattr(notif_svc, "_send_webhook", fake_webhook)
    channels = [
        _full_channel(name="ok-1"),
        _full_channel(name="broken"),
        _full_channel(name="ok-2"),
    ]
    outcomes = asyncio.run(
        dispatch(_db_returning(channels), _alert(), event="fire"),
    )
    by_name = {o.channel_name: o for o in outcomes}
    assert by_name["ok-1"].delivered is True
    assert by_name["ok-2"].delivered is True
    assert by_name["broken"].delivered is False
    assert "503" in (by_name["broken"].error or "")


def test_dispatch_resolve_only_fires_for_transitioned_alerts(monkeypatch):
    """dispatch_resolve refuses to ship for alerts whose state isn't
    'resolved' — guards against an off-by-one in the alert engine
    where a firing alert is handed to the resolve path."""
    called = []

    async def fake_webhook(*_a, **_k):
        called.append(1)

    monkeypatch.setattr(notif_svc, "_send_webhook", fake_webhook)
    # Build an alert still in `firing` state — dispatch_resolve
    # short-circuits before walking channels.
    a = _alert(state=AlertState.firing)
    outcomes = asyncio.run(notif_svc.dispatch_resolve(_db_returning([_full_channel()]), a))
    assert outcomes == []
    assert called == []


def test_dispatch_email_no_recipients_marks_outcome_failed(monkeypatch):
    """A misconfigured email channel (no recipients) must surface as a
    failed delivery, not a silent skip — operators need to know their
    paging path is dead."""
    # Real _send_email is called; we override settings so the SMTP
    # branch is skipped only when DCIM_SMTP_HOST is set. To force the
    # recipient-check path, set host and let the function reach the
    # recipients=[] guard.
    monkeypatch.setattr(
        "dcim.services.notifications.get_settings",
        lambda: SimpleNamespace(
            smtp_host="smtp.example", smtp_port=25, smtp_username=None,
            smtp_password=None, smtp_sender="dcim@example",
        ),
    )
    # Stub out the actual SMTP sender so we never touch the network.
    monkeypatch.setattr(
        "dcim.services.notifications._send_email_sync",
        lambda *a, **kw: None,
    )
    ch = _full_channel(
        kind=ChannelKind.email,
        config_json={"recipients": []},  # missing
    )
    outcomes = asyncio.run(
        dispatch(_db_returning([ch]), _alert(), event="fire"),
    )
    assert outcomes[0].delivered is False
    assert "recipients" in (outcomes[0].error or "")
