"""Telemetry ingest pipeline.

- Bulk-writes samples to Elastic (per-site monthly index).
- Updates per-source freshness rows in Postgres so the UI can show stale/current.
- Idempotent on (collector_id, batch_id) via the document _id naming scheme.
"""

from __future__ import annotations

from datetime import UTC, datetime
from uuid import UUID

import structlog
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from .. import metrics
from ..models.telemetry_meta import FreshnessState, TelemetrySource
from ..schemas.telemetry import TelemetryBatch, TelemetrySample
from .elastic import client, ensure_index, telemetry_index

log = structlog.get_logger("dcim.telemetry")


async def ingest(db: AsyncSession, batch: TelemetryBatch) -> dict:
    received_at = datetime.now(UTC)
    site_id = str(batch.site_id)
    index = telemetry_index(site_id, received_at)
    await ensure_index(index)

    actions: list[dict] = []
    for i, s in enumerate(batch.samples):
        doc_id = f"{batch.collector_id}:{batch.batch_id}:{i}"
        actions.append({"index": {"_index": index, "_id": doc_id}})
        actions.append(
            {
                "site_id": site_id,
                "collector_id": str(batch.collector_id),
                "asset_id": str(s.asset_id),
                "metric": s.metric,
                "value": s.value,
                "unit": s.unit,
                "ts": s.ts.isoformat(),
                "received_at": received_at.isoformat(),
                "tags": s.tags,
            }
        )
    es = client()
    resp = await es.bulk(operations=actions, refresh=False)
    errors = resp.get("errors", False)
    if errors:
        # Surface partial failure but don't reject the batch wholesale.
        log.warning("telemetry_bulk_errors", batch=batch.batch_id, count=len(batch.samples))

    await _update_freshness(db, batch.samples, batch.collector_id, batch.site_id, received_at)
    await db.commit()

    metrics.telemetry_samples_ingested.labels(site_id=site_id).inc(len(batch.samples))
    metrics.telemetry_ingest_batches.observe(len(batch.samples))

    return {"accepted": len(batch.samples), "errors": bool(errors), "received_at": received_at.isoformat()}


async def _update_freshness(
    db: AsyncSession,
    samples: list[TelemetrySample],
    collector_id: UUID,
    site_id: UUID,
    received_at: datetime,
) -> None:
    by_key: dict[tuple[UUID, str], TelemetrySample] = {}
    for s in samples:
        by_key[(s.asset_id, s.metric)] = s

    for (asset_id, metric), s in by_key.items():
        existing = (
            await db.execute(
                select(TelemetrySource).where(
                    TelemetrySource.asset_id == asset_id, TelemetrySource.metric == metric
                )
            )
        ).scalar_one_or_none()
        if existing is None:
            db.add(
                TelemetrySource(
                    site_id=site_id,
                    asset_id=asset_id,
                    collector_id=collector_id,
                    metric=metric,
                    unit=s.unit,
                    last_success_at=received_at,
                    last_reading_at=s.ts,
                    last_value=s.value,
                    freshness=FreshnessState.current,
                )
            )
        else:
            existing.collector_id = collector_id
            existing.unit = s.unit or existing.unit
            existing.last_success_at = received_at
            existing.last_reading_at = s.ts
            existing.last_value = s.value
            existing.freshness = FreshnessState.current
