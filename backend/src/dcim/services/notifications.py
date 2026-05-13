"""Outbound notification dispatcher.

Walks the active NotificationChannel rows and ships an alert to each one
that matches its severity floor + event filter. Webhook/Slack adapters
hit HTTP via httpx; email is sent via aiosmtplib only when SMTP settings
are configured (otherwise the email channel is a no-op so dev environments
don't fail loudly).

The pure helpers (`channel_matches`, `format_*`) are exported so the
unit suite covers routing + payload shapes without spinning up a real
HTTP server.
"""

from __future__ import annotations

import smtplib
from dataclasses import dataclass
from email.mime.text import MIMEText
from typing import Any

import httpx
import structlog
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from ..models.alerts import Alert, AlertState, Severity
from ..models.notifications import ChannelKind, NotificationChannel
from ..settings import get_settings

log = structlog.get_logger("dcim.notifications")

# Severity ordering for threshold comparisons. Tied to roadmap convention:
# info < warning < minor < major < critical.
_SEV_ORDER = {
    Severity.info: 0,
    Severity.warning: 1,
    Severity.minor: 2,
    Severity.major: 3,
    Severity.critical: 4,
}


@dataclass
class DispatchOutcome:
    channel_id: str
    channel_name: str
    kind: str
    delivered: bool
    error: str | None = None


def _sev_value(s: Severity | str) -> int:
    if isinstance(s, str):
        s = Severity(s)
    return _SEV_ORDER.get(s, 0)


def channel_matches(
    channel: NotificationChannel, alert_severity: Severity, event: str,
) -> bool:
    """Return True if the channel should receive this (severity, event) pair.

    `event` is one of "fire" | "resolve". Channels that aren't enabled or that
    have the matching notify_on_* flag turned off are skipped.
    """
    if not channel.enabled:
        return False
    if event == "fire" and not channel.notify_on_fire:
        return False
    if event == "resolve" and not channel.notify_on_resolve:
        return False
    return _sev_value(alert_severity) >= _sev_value(channel.min_severity)


def format_webhook_payload(alert: Alert, event: str) -> dict[str, Any]:
    """JSON shape we POST to generic webhooks. Stable contract — downstream
    automations key off these field names."""
    sev = alert.severity.value if hasattr(alert.severity, "value") else alert.severity
    state = alert.state.value if hasattr(alert.state, "value") else alert.state
    return {
        "event": f"alert.{event}",
        "alert": {
            "id": str(alert.id),
            "severity": sev,
            "state": state,
            "summary": alert.summary,
            "detail": alert.detail,
            "site_id": str(alert.site_id) if alert.site_id else None,
            "asset_id": str(alert.asset_id) if alert.asset_id else None,
            "first_seen_at": alert.first_seen_at.isoformat() if alert.first_seen_at else None,
            "last_seen_at": alert.last_seen_at.isoformat() if alert.last_seen_at else None,
            "labels": alert.labels_json or {},
        },
    }


# Slack incoming-webhook payload. Color band by severity so the message
# stands out at a glance in #ops.
_SLACK_COLOR = {
    Severity.critical: "#d92626",
    Severity.major: "#e08e1b",
    Severity.minor: "#e6c01a",
    Severity.warning: "#5b9bd5",
    Severity.info: "#777777",
}


def format_slack_payload(alert: Alert, event: str) -> dict[str, Any]:
    sev = alert.severity if hasattr(alert.severity, "value") else Severity(alert.severity)
    sev_label = sev.value if hasattr(sev, "value") else sev
    color = _SLACK_COLOR.get(sev, "#777777")
    title = f"[{sev_label.upper()}] alert.{event}"
    state = alert.state.value if hasattr(alert.state, "value") else alert.state
    site = str(alert.site_id) if alert.site_id else "—"
    return {
        "attachments": [
            {
                "color": color,
                "title": title,
                "text": alert.summary,
                "fields": [
                    {"title": "State", "value": state, "short": True},
                    {"title": "Site", "value": site, "short": True},
                ],
            }
        ]
    }


def format_email_subject_body(alert: Alert, event: str) -> tuple[str, str]:
    sev = alert.severity.value if hasattr(alert.severity, "value") else alert.severity
    subject = f"[DCIM][{sev.upper()}] alert.{event}: {alert.summary}"
    body_lines = [
        f"Event: alert.{event}",
        f"Severity: {sev}",
        f"State: {alert.state.value if hasattr(alert.state, 'value') else alert.state}",
        f"Summary: {alert.summary}",
        "",
        f"Detail: {alert.detail or '(none)'}",
        f"Site: {alert.site_id or '—'}",
        f"Asset: {alert.asset_id or '—'}",
        f"First seen: {alert.first_seen_at.isoformat() if alert.first_seen_at else '—'}",
        f"Last seen: {alert.last_seen_at.isoformat() if alert.last_seen_at else '—'}",
    ]
    return subject, "\n".join(body_lines)


async def _send_webhook(channel: NotificationChannel, payload: dict) -> None:
    url = channel.config_json.get("url")
    if not url:
        raise ValueError("webhook channel missing config_json.url")
    headers = channel.config_json.get("headers") or {}
    async with httpx.AsyncClient(timeout=10.0) as client:
        resp = await client.post(url, json=payload, headers=headers)
        resp.raise_for_status()


async def _send_slack(channel: NotificationChannel, payload: dict) -> None:
    url = channel.config_json.get("webhook_url")
    if not url:
        raise ValueError("slack channel missing config_json.webhook_url")
    async with httpx.AsyncClient(timeout=10.0) as client:
        resp = await client.post(url, json=payload)
        resp.raise_for_status()


def _send_email_sync(
    recipients: list[str], subject: str, body: str,
    *, host: str, port: int, username: str | None, password: str | None, sender: str,
) -> None:
    msg = MIMEText(body)
    msg["Subject"] = subject
    msg["From"] = sender
    msg["To"] = ", ".join(recipients)
    with smtplib.SMTP(host, port, timeout=10) as smtp:
        smtp.starttls()
        if username and password:
            smtp.login(username, password)
        smtp.sendmail(sender, recipients, msg.as_string())


async def _send_email(channel: NotificationChannel, alert: Alert, event: str) -> None:
    settings = get_settings()
    if not settings.smtp_host:
        # Soft no-op so dev/CI doesn't error on missing SMTP.
        log.info(
            "email_skipped_no_smtp",
            channel=channel.name,
            reason="DCIM_SMTP_HOST not configured",
        )
        return
    recipients = channel.config_json.get("recipients") or []
    if not recipients:
        raise ValueError("email channel missing config_json.recipients")
    subject, body = format_email_subject_body(alert, event)
    # smtplib is sync; offload to a thread so we don't block the event loop.
    import asyncio
    await asyncio.to_thread(
        _send_email_sync,
        recipients, subject, body,
        host=settings.smtp_host,
        port=settings.smtp_port,
        username=settings.smtp_username,
        password=settings.smtp_password,
        sender=settings.smtp_sender,
    )


async def dispatch(db: AsyncSession, alert: Alert, event: str) -> list[DispatchOutcome]:
    """Walk all enabled channels and ship the alert to those whose filters match.

    Failures on individual channels are logged but never raised — one bad
    Slack webhook should not break the rest of the alert evaluation pass.
    """
    if event not in ("fire", "resolve"):
        raise ValueError(f"unknown event {event!r}; expected fire or resolve")

    channels = (
        await db.execute(select(NotificationChannel).where(NotificationChannel.enabled.is_(True)))
    ).scalars().all()

    sev = alert.severity if hasattr(alert.severity, "value") else Severity(alert.severity)
    targets = [c for c in channels if channel_matches(c, sev, event)]
    outcomes: list[DispatchOutcome] = []
    for c in targets:
        try:
            if c.kind == ChannelKind.webhook:
                await _send_webhook(c, format_webhook_payload(alert, event))
            elif c.kind == ChannelKind.slack:
                await _send_slack(c, format_slack_payload(alert, event))
            elif c.kind == ChannelKind.email:
                await _send_email(c, alert, event)
            outcomes.append(DispatchOutcome(
                channel_id=str(c.id), channel_name=c.name, kind=c.kind.value, delivered=True,
            ))
        except Exception as exc:
            # `alert_event` not `event` — structlog reserves `event` for
            # the log-message slot and a kwarg by that name collides
            # with the positional, raising TypeError inside the failure
            # path and turning a single bad webhook into a hard crash
            # of dispatch (defeating the entire failure-isolation goal).
            log.warning(
                "notification_failed",
                channel=c.name, kind=c.kind.value, error=str(exc),
                alert_id=str(alert.id), alert_event=event,
            )
            outcomes.append(DispatchOutcome(
                channel_id=str(c.id), channel_name=c.name, kind=c.kind.value,
                delivered=False, error=str(exc),
            ))
    return outcomes


# Convenience wrappers used by the alert engine for clarity at call sites.
async def dispatch_fire(db: AsyncSession, alert: Alert) -> list[DispatchOutcome]:
    return await dispatch(db, alert, event="fire")


async def dispatch_resolve(db: AsyncSession, alert: Alert) -> list[DispatchOutcome]:
    """Hydrate state for already-firing alerts that we just resolved.

    The alert object passed in still has state=resolved and resolved_at set.
    """
    if alert.state != AlertState.resolved:
        # Only fire resolve hooks for alerts that actually transitioned.
        return []
    return await dispatch(db, alert, event="resolve")
