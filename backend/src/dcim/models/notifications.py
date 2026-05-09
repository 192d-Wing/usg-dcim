"""Outbound notification channels for the alert engine.

A NotificationChannel describes *where* alerts should land — generic webhook,
Slack incoming webhook, or email recipient list. Routing today is a simple
severity-threshold filter; per-rule routing can be layered on later via a
join table without changing this model.
"""

from __future__ import annotations

import enum

from sqlalchemy import JSON, Boolean, Enum, Index, String
from sqlalchemy.orm import Mapped, mapped_column

from ..db import Base
from ._mixins import Timestamped, UUIDPrimaryKey
from .alerts import Severity


class ChannelKind(str, enum.Enum):
    webhook = "webhook"
    slack = "slack"
    email = "email"


class NotificationChannel(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "notification_channels"
    __table_args__ = (
        Index("ix_notification_channels_kind", "kind"),
        Index("ix_notification_channels_enabled", "enabled"),
    )

    name: Mapped[str] = mapped_column(String(128), nullable=False, unique=True)
    kind: Mapped[ChannelKind] = mapped_column(
        Enum(ChannelKind, name="channel_kind", values_callable=lambda x: [e.value for e in x]),
        nullable=False,
    )
    # Kind-specific config:
    #   webhook -> {"url": "...", "headers": {...}}
    #   slack   -> {"webhook_url": "..."}
    #   email   -> {"recipients": ["a@b", ...]}
    config_json: Mapped[dict] = mapped_column(JSON, default=dict, nullable=False)
    # Severity floor — alerts strictly below this severity are skipped.
    # Defaults to "warning" so info-level noise stays out of pagers.
    min_severity: Mapped[Severity] = mapped_column(
        Enum(Severity, name="alert_severity"), default=Severity.warning, nullable=False,
    )
    # Notify on fire transitions, resolve transitions, or both.
    notify_on_fire: Mapped[bool] = mapped_column(Boolean, default=True, nullable=False)
    notify_on_resolve: Mapped[bool] = mapped_column(Boolean, default=True, nullable=False)
    enabled: Mapped[bool] = mapped_column(Boolean, default=True, nullable=False)
    description: Mapped[str | None] = mapped_column(String(512))
