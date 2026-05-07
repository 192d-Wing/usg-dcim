from __future__ import annotations

from datetime import datetime
from uuid import UUID

from pydantic import BaseModel, Field


class TelemetrySample(BaseModel):
    asset_id: UUID
    metric: str
    value: float
    unit: str | None = None
    ts: datetime
    tags: dict[str, str] = Field(default_factory=dict)


class TelemetryBatch(BaseModel):
    """Idempotent batch posted by a collector. `batch_id` lets the ingester dedupe retries."""

    batch_id: str = Field(min_length=8, max_length=64)
    site_id: UUID
    collector_id: UUID
    samples: list[TelemetrySample] = Field(min_length=1, max_length=5000)
