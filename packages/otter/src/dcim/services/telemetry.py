"""Telemetry ingest pipeline.

- Inserts samples into the TimescaleDB `telemetry_samples` hypertable.
  Idempotent on (collector_id, batch_id, seq, ts) via the unique constraint
  from migration 0046; collector retries are no-ops.
- Updates per-source freshness rows so the UI can show stale/current.

OpenSearch was the original telemetry store but was retired after the read
paths moved to the hypertable (steps 2a/2b/2c of the migration). The
`telemetry_write_hypertable` setting remains as an opt-out for deployments
running on stock Postgres without the TimescaleDB extension — when False
the freshness write still runs and the request returns 202-style success
without any sample being persisted.
"""

from __future__ import annotations

from datetime import UTC, datetime
from uuid import UUID

import structlog
from sqlalchemy import select
from sqlalchemy.dialects.postgresql import insert as pg_insert
from sqlalchemy.exc import SQLAlchemyError
from sqlalchemy.ext.asyncio import AsyncSession

from .. import metrics
from ..models.telemetry_meta import FreshnessState, TelemetrySource
from ..models.telemetry_samples import telemetry_samples
from ..schemas.telemetry import TelemetryBatch, TelemetrySample
from ..settings import get_settings

log = structlog.get_logger("dcim.telemetry")


def _hypertable_rows(batch: TelemetryBatch, received_at: datetime) -> list[dict]:
    """Build the rows we'd INSERT into telemetry_samples for this batch.

    Pure function (no DB, no settings) so tests can assert the row shape and
    dedup-key fields without standing up Postgres.
    """
    return [
        {
            "ts": s.ts,
            "site_id": batch.site_id,
            "asset_id": s.asset_id,
            "collector_id": batch.collector_id,
            "batch_id": batch.batch_id,
            "seq": i,
            "metric": s.metric,
            "value": s.value,
            "unit": s.unit,
            "received_at": received_at,
            "tags": s.tags,
        }
        for i, s in enumerate(batch.samples)
    ]


async def _write_hypertable(
    db: AsyncSession, batch: TelemetryBatch, received_at: datetime,
) -> bool:
    """INSERT the batch into the TimescaleDB hypertable. Returns True iff
    the rows landed; False on SQL error (logged + counted but not raised).

    The opt-out via `settings.telemetry_write_hypertable` is for stock-PG
    deployments that don't have the TimescaleDB extension installed; the
    request still succeeds, freshness still updates, just no sample rows.
    """
    if not get_settings().telemetry_write_hypertable:
        metrics.telemetry_timescale_writes.labels(outcome="disabled").inc()
        return False
    rows = _hypertable_rows(batch, received_at)
    stmt = pg_insert(telemetry_samples).values(rows).on_conflict_do_nothing(
        constraint="uq_telem_sample_dedup",
    )
    try:
        await db.execute(stmt)
    except SQLAlchemyError as e:
        metrics.telemetry_timescale_writes.labels(outcome="error").inc()
        log.warning(
            "telemetry_hypertable_write_failed",
            batch=batch.batch_id, count=len(rows), err=str(e),
        )
        return False
    metrics.telemetry_timescale_writes.labels(outcome="ok").inc()
    return True


async def ingest(db: AsyncSession, batch: TelemetryBatch) -> dict:
    received_at = datetime.now(UTC)
    site_id = str(batch.site_id)

    await _update_freshness(db, batch.samples, batch.collector_id, batch.site_id, received_at)
    written = await _write_hypertable(db, batch, received_at)
    await db.commit()

    metrics.telemetry_samples_ingested.labels(site_id=site_id).inc(len(batch.samples))
    metrics.telemetry_ingest_batches.observe(len(batch.samples))

    return {
        "accepted": len(batch.samples),
        "errors": not written,
        "received_at": received_at.isoformat(),
    }


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
