"""Per-asset telemetry source registry — drives freshness UI and collector-down alerts.

Telemetry samples live in the TimescaleDB `telemetry_samples` hypertable;
this table tracks per-(asset, metric) freshness metadata the UI and alert
engine need without scanning the hypertable.
"""

from __future__ import annotations

import enum
from datetime import datetime
from uuid import UUID

from sqlalchemy import DateTime, Enum, ForeignKey, Index, String, UniqueConstraint
from sqlalchemy.dialects.postgresql import UUID as PgUUID
from sqlalchemy.orm import Mapped, mapped_column

from ..db import Base
from ._mixins import Timestamped, UUIDPrimaryKey


class FreshnessState(str, enum.Enum):
    current = "current"
    stale = "stale"
    estimated = "estimated"
    manual = "manual"
    unknown = "unknown"


class TelemetrySource(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "telemetry_sources"
    __table_args__ = (
        UniqueConstraint("asset_id", "metric", name="uq_telem_source_asset_metric"),
        Index("ix_telem_source_collector", "collector_id"),
        Index("ix_telem_source_freshness", "freshness"),
        Index("ix_telem_source_site", "site_id"),
    )

    site_id: Mapped[UUID] = mapped_column(PgUUID(as_uuid=True), ForeignKey("sites.id"), nullable=False)
    asset_id: Mapped[UUID] = mapped_column(PgUUID(as_uuid=True), ForeignKey("assets.id"), nullable=False)
    collector_id: Mapped[UUID | None] = mapped_column(PgUUID(as_uuid=True), ForeignKey("collectors.id"))
    metric: Mapped[str] = mapped_column(String(128), nullable=False)
    unit: Mapped[str | None] = mapped_column(String(32))
    source_system: Mapped[str | None] = mapped_column(String(64))  # snmp|redfish|modbus|rest|ipmi|manual
    last_success_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    last_failure_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    last_reading_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    last_value: Mapped[float | None]
    freshness: Mapped[FreshnessState] = mapped_column(
        Enum(FreshnessState, name="freshness_state"), default=FreshnessState.unknown, nullable=False
    )
    poll_interval_seconds: Mapped[int] = mapped_column(default=60, nullable=False)
