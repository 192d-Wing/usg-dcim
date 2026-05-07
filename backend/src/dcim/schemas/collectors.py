from __future__ import annotations

from datetime import datetime
from uuid import UUID

from pydantic import BaseModel, ConfigDict, Field

from ..models.collectors import CollectorStatus


class CollectorEnroll(BaseModel):
    site_id: UUID
    name: str
    capabilities: list[str] = Field(default_factory=list)


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


class CollectorHeartbeatIn(BaseModel):
    queue_depth: int = 0
    buffered_samples: int = 0
    version: str | None = None
    last_error: str | None = None
    metrics: dict = Field(default_factory=dict)
