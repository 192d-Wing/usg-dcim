from __future__ import annotations

from datetime import datetime
from uuid import UUID

from pydantic import BaseModel, ConfigDict, Field

from ..models.collectors import CollectorStatus


class CollectorEnroll(BaseModel):
    site_id: UUID
    name: str
    capabilities: list[str] = Field(default_factory=list)


class CollectorConfigOverrides(BaseModel):
    """Optional ticker intervals (seconds) pushed to a collector via its
    heartbeat response. Each field is None when the operator hasn't
    pinned it — the collector keeps its YAML default."""

    dns_metrics_interval_seconds: int | None = Field(default=None, ge=5, le=3600)
    device_poll_interval_seconds: int | None = Field(default=None, ge=5, le=3600)
    heartbeat_interval_seconds: int | None = Field(default=None, ge=5, le=3600)


class CollectorOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    site_id: UUID
    name: str
    version: str | None = None
    status: CollectorStatus
    capabilities: list[str]
    last_seen_at: datetime | None
    last_ingest_at: datetime | None
    buffered_samples: int
    enabled: bool
    config_overrides: CollectorConfigOverrides = Field(
        default_factory=CollectorConfigOverrides,
    )


class CollectorConfigPatch(BaseModel):
    """Operator-supplied overrides. None on a field clears that override
    so the collector falls back to its YAML default."""

    dns_metrics_interval_seconds: int | None = Field(default=None, ge=5, le=3600)
    device_poll_interval_seconds: int | None = Field(default=None, ge=5, le=3600)
    heartbeat_interval_seconds: int | None = Field(default=None, ge=5, le=3600)


class CollectorHeartbeatIn(BaseModel):
    queue_depth: int = 0
    buffered_samples: int = 0
    version: str | None = None
    last_error: str | None = None
    metrics: dict = Field(default_factory=dict)


class CollectorHeartbeatOut(BaseModel):
    """Heartbeat response carries the current overrides back to the
    collector. The Go collector resets its tickers when it sees a
    changed value; nil/missing keys mean "use the YAML default"."""

    ok: bool = True
    received_at: datetime
    config_overrides: CollectorConfigOverrides = Field(
        default_factory=CollectorConfigOverrides,
    )


class CollectorEnabledPatch(BaseModel):
    """Enable or disable a collector."""

    enabled: bool
