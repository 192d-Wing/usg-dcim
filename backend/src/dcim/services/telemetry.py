"""Telemetry ingest pipeline.

- Bulk-writes samples to OpenSearch (per-site monthly index) — primary store.
- Dual-writes samples to the TimescaleDB `telemetry_samples` hypertable when
  ``settings.telemetry_dual_write_timescale`` is enabled. This is step 1 of
  the OpenSearch → TimescaleDB migration: parity data accumulates in the
  hypertable while readers continue to query OpenSearch. The hypertable
  write is fail-open — a Timescale outage must not reject batches that
  OpenSearch already accepted.
- Updates per-source freshness rows in Postgres so the UI can show stale/current.
- Idempotent on (collector_id, batch_id) via the document _id naming scheme
  in OpenSearch and the (collector_id, batch_id, seq, ts) unique constraint
  in the hypertable.
"""

from __future__ import annotations

from datetime import UTC, datetime
from uuid import UUID

import structlog
from opensearchpy.helpers import async_bulk
from sqlalchemy import select
from sqlalchemy.dialects.postgresql import insert as pg_insert
from sqlalchemy.exc import SQLAlchemyError
from sqlalchemy.ext.asyncio import AsyncSession

from .. import metrics
from ..models.telemetry_meta import FreshnessState, TelemetrySource
from ..models.telemetry_samples import telemetry_samples
from ..schemas.telemetry import TelemetryBatch, TelemetrySample
from ..settings import get_settings
from .opensearch import client, ensure_index, telemetry_index

log = structlog.get_logger("dcim.telemetry")


def _opensearch_actions(
    index: str, batch: TelemetryBatch, received_at: datetime,
) -> list[dict]:
    """Build the opensearch-helpers `async_bulk` action list for a batch.

    One dict per document, with `_index`, `_id`, and `_source` fields — the
    helper handles NDJSON serialization and the action/source pairing on the
    wire. The previous shape (alternating action and source dicts in a flat
    list passed via `body=`) caused opensearch-py to index each element as
    a standalone document, doubling the per-batch doc count.

    Pure function so tests can lock the shape without standing up OpenSearch.
    """
    site_id = str(batch.site_id)
    collector_id = str(batch.collector_id)
    ts_iso = received_at.isoformat()
    return [
        {
            "_index": index,
            "_id": f"{batch.collector_id}:{batch.batch_id}:{i}",
            "_source": {
                "site_id": site_id,
                "collector_id": collector_id,
                "asset_id": str(s.asset_id),
                "metric": s.metric,
                "value": s.value,
                "unit": s.unit,
                "ts": s.ts.isoformat(),
                "received_at": ts_iso,
                "tags": s.tags,
            },
        }
        for i, s in enumerate(batch.samples)
    ]


async def ingest(db: AsyncSession, batch: TelemetryBatch) -> dict:
    received_at = datetime.now(UTC)
    site_id = str(batch.site_id)
    index = telemetry_index(site_id, received_at)
    await ensure_index(index)

    actions = _opensearch_actions(index, batch, received_at)
    es = client()
    # async_bulk handles NDJSON framing + action/source pairing correctly.
    # `raise_on_error=False` mirrors the prior behavior: surface partial
    # failures but don't reject the whole batch — the freshness + hypertable
    # writes still run for the samples OpenSearch accepted.
    success, errs = await async_bulk(es, actions, raise_on_error=False)
    errors = bool(errs)
    if errors:
        log.warning(
            "telemetry_bulk_errors",
            batch=batch.batch_id, accepted=success, failed=len(errs),
        )

    await _update_freshness(db, batch.samples, batch.collector_id, batch.site_id, received_at)
    await _dual_write_timescale(db, batch, received_at)
    await db.commit()

    metrics.telemetry_samples_ingested.labels(site_id=site_id).inc(len(batch.samples))
    metrics.telemetry_ingest_batches.observe(len(batch.samples))

    return {"accepted": len(batch.samples), "errors": errors, "received_at": received_at.isoformat()}


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


async def _dual_write_timescale(
    db: AsyncSession, batch: TelemetryBatch, received_at: datetime,
) -> None:
    """Fail-open INSERT of the batch into the TimescaleDB hypertable.

    Errors here are logged + counted but never raised: OpenSearch already
    accepted the batch and is the read path, so a Timescale outage must not
    cause the collector to retry the batch (which would just amplify load
    on the failing side).
    """
    if not get_settings().telemetry_dual_write_timescale:
        metrics.telemetry_timescale_writes.labels(outcome="disabled").inc()
        return
    rows = _hypertable_rows(batch, received_at)
    stmt = pg_insert(telemetry_samples).values(rows).on_conflict_do_nothing(
        constraint="uq_telem_sample_dedup",
    )
    # SAVEPOINT so a Timescale-side failure (table missing, hypertable not
    # installed, transient error) doesn't poison the outer transaction and
    # roll back the freshness updates we just made.
    try:
        async with db.begin_nested():
            await db.execute(stmt)
    except SQLAlchemyError as e:
        metrics.telemetry_timescale_writes.labels(outcome="error").inc()
        log.warning(
            "telemetry_timescale_dual_write_failed",
            batch=batch.batch_id, count=len(rows), err=str(e),
        )
        return
    metrics.telemetry_timescale_writes.labels(outcome="ok").inc()


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
