"""Alert rules, alerts, suppressions, maintenance windows."""

from __future__ import annotations

import enum
from datetime import datetime
from uuid import UUID

from sqlalchemy import JSON, Boolean, DateTime, Enum, ForeignKey, Index, String
from sqlalchemy.dialects.postgresql import UUID as PgUUID
from sqlalchemy.orm import Mapped, mapped_column

from ..db import Base
from ._mixins import Timestamped, UUIDPrimaryKey


class Severity(str, enum.Enum):
    info = "info"
    warning = "warning"
    minor = "minor"
    major = "major"
    critical = "critical"


class AlertState(str, enum.Enum):
    firing = "firing"
    acknowledged = "acknowledged"
    suppressed = "suppressed"
    resolved = "resolved"


class AlertRule(UUIDPrimaryKey, Timestamped, Base):
    """Enterprise default rules; site overrides keyed by site_id."""

    __tablename__ = "alert_rules"
    __table_args__ = (
        Index("ix_alert_rules_metric", "metric"),
        Index("ix_alert_rules_site", "site_scope_id"),
    )

    name: Mapped[str] = mapped_column(String(128), nullable=False)
    description: Mapped[str | None] = mapped_column(String(512))
    metric: Mapped[str] = mapped_column(String(128), nullable=False)  # e.g. pdu.input.kw, sensor.temp.c
    operator: Mapped[str] = mapped_column(String(8), nullable=False)  # >, <, >=, <=, ==, !=
    threshold: Mapped[float] = mapped_column(nullable=False)
    duration_seconds: Mapped[int] = mapped_column(default=60, nullable=False)
    severity: Mapped[Severity] = mapped_column(Enum(Severity, name="alert_severity"), nullable=False)
    site_scope_id: Mapped[UUID | None] = mapped_column(PgUUID(as_uuid=True), ForeignKey("sites.id"))
    asset_filter_json: Mapped[dict] = mapped_column(JSON, default=dict, nullable=False)
    enabled: Mapped[bool] = mapped_column(Boolean, default=True, nullable=False)
    runbook_url: Mapped[str | None] = mapped_column(String(512))


class Alert(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "alerts"
    __table_args__ = (
        Index("ix_alerts_state", "state"),
        Index("ix_alerts_site", "site_id"),
        Index("ix_alerts_severity", "severity"),
        Index("ix_alerts_dedupe", "dedupe_key", unique=False),
    )

    rule_id: Mapped[UUID | None] = mapped_column(PgUUID(as_uuid=True), ForeignKey("alert_rules.id"))
    site_id: Mapped[UUID] = mapped_column(PgUUID(as_uuid=True), ForeignKey("sites.id"), nullable=False)
    asset_id: Mapped[UUID | None] = mapped_column(PgUUID(as_uuid=True), ForeignKey("assets.id"))
    collector_id: Mapped[UUID | None] = mapped_column(PgUUID(as_uuid=True), ForeignKey("collectors.id"))

    severity: Mapped[Severity] = mapped_column(Enum(Severity, name="alert_severity"), nullable=False)
    state: Mapped[AlertState] = mapped_column(
        Enum(AlertState, name="alert_state"), default=AlertState.firing, nullable=False
    )
    dedupe_key: Mapped[str] = mapped_column(String(255), nullable=False)
    correlation_key: Mapped[str | None] = mapped_column(String(255))

    summary: Mapped[str] = mapped_column(String(512), nullable=False)
    detail: Mapped[str | None] = mapped_column(String(2048))
    first_seen_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), nullable=False)
    last_seen_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), nullable=False)
    acked_by: Mapped[str | None] = mapped_column(String(255))
    acked_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    resolved_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    labels_json: Mapped[dict] = mapped_column(JSON, default=dict, nullable=False)


class MaintenanceWindow(UUIDPrimaryKey, Timestamped, Base):
    """Suppress alerts for matching scope during the window."""

    __tablename__ = "maintenance_windows"
    __table_args__ = (
        Index("ix_mw_site", "site_id"),
        Index("ix_mw_window", "starts_at", "ends_at"),
    )

    name: Mapped[str] = mapped_column(String(128), nullable=False)
    site_id: Mapped[UUID | None] = mapped_column(PgUUID(as_uuid=True), ForeignKey("sites.id"))
    asset_filter_json: Mapped[dict] = mapped_column(JSON, default=dict, nullable=False)
    starts_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), nullable=False)
    ends_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), nullable=False)
    created_by: Mapped[str | None] = mapped_column(String(255))
    reason: Mapped[str | None] = mapped_column(String(512))
