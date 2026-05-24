"""Collector registry + heartbeat tracking."""

from __future__ import annotations

import enum
from datetime import datetime
from uuid import UUID

from sqlalchemy import JSON, Boolean, DateTime, Enum, ForeignKey, Index, String
from sqlalchemy.dialects.postgresql import JSONB
from sqlalchemy.dialects.postgresql import UUID as PgUUID
from sqlalchemy.orm import Mapped, mapped_column

from ..db import Base
from ._mixins import Timestamped, UUIDPrimaryKey


class CollectorStatus(str, enum.Enum):
    pending = "pending"
    healthy = "healthy"
    degraded = "degraded"
    stale = "stale"
    unreachable = "unreachable"
    decommissioned = "decommissioned"


class Collector(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "collectors"
    __table_args__ = (
        Index("ix_collectors_site", "site_id"),
        Index("ix_collectors_status", "status"),
    )

    site_id: Mapped[UUID] = mapped_column(PgUUID(as_uuid=True), ForeignKey("sites.id"), nullable=False)
    name: Mapped[str] = mapped_column(String(128), nullable=False)
    version: Mapped[str | None] = mapped_column(String(32))
    mtls_fingerprint: Mapped[str | None] = mapped_column(String(128), unique=True)
    enrollment_token_hash: Mapped[str | None] = mapped_column(String(255))
    status: Mapped[CollectorStatus] = mapped_column(
        Enum(CollectorStatus, name="collector_status"), default=CollectorStatus.pending, nullable=False
    )
    capabilities: Mapped[list[str]] = mapped_column(JSON, default=list, nullable=False)
    last_seen_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    last_ingest_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    buffered_samples: Mapped[int] = mapped_column(default=0, nullable=False)
    enabled: Mapped[bool] = mapped_column(Boolean, default=True, nullable=False)
    # Runtime overrides pushed to the collector on its next heartbeat
    # response. Recognised keys: dns_metrics_interval_seconds,
    # device_poll_interval_seconds, heartbeat_interval_seconds. Empty
    # dict = the collector keeps whatever its YAML says.
    config_overrides: Mapped[dict] = mapped_column(JSONB, default=dict, nullable=False)


class CollectorHeartbeat(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "collector_heartbeats"
    __table_args__ = (Index("ix_heartbeats_collector_ts", "collector_id", "received_at"),)

    collector_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("collectors.id"), nullable=False
    )
    received_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), nullable=False)
    queue_depth: Mapped[int] = mapped_column(default=0, nullable=False)
    last_error: Mapped[str | None] = mapped_column(String(512))
    metrics_json: Mapped[dict] = mapped_column(JSON, default=dict, nullable=False)
