"""Unit tests for the notification dispatcher's pure parts.

`dispatch` and the per-kind `_send_*` adapters are I/O bound and covered
by the integration suite. These tests pin the routing predicate
(`channel_matches`) and the payload formatters so future refactors can't
silently break the downstream contracts that operators key off of.
"""

from datetime import UTC, datetime
from types import SimpleNamespace
from uuid import uuid4

from dcim.models.alerts import AlertState, Severity
from dcim.services.notifications import (
    channel_matches,
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
