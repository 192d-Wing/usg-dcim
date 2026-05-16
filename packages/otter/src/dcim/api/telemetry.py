"""Read-side telemetry: time-range queries against the TimescaleDB hypertable.

Step 2c of the OpenSearch → TimescaleDB migration: the final reader cutover.
After this PR no caller queries OpenSearch — only the dual-write paths in
services/telemetry.py + services/go-ingest still touch it, and those go away
in step 3.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
from uuid import UUID

from fastapi import APIRouter, Depends, Query
from sqlalchemy import text
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..security.deps import Principal, require_capability

router = APIRouter(prefix="/telemetry", tags=["telemetry"])


# The (asset_id, metric, ts) index from migration 0046 covers this query
# directly. LIMIT 10000 matches the prior OpenSearch `size: 10000` ceiling
# so the frontend's chart-rendering assumptions don't change.
_SERIES_SQL = text("""
    SELECT ts, value
    FROM telemetry_samples
    WHERE site_id = :site_id
      AND asset_id = :asset_id
      AND metric = :metric
      AND ts >= :start
      AND ts <= :end
    ORDER BY ts ASC
    LIMIT 10000
""")


@router.get("/series")
async def get_series(
    site_id: UUID,
    asset_id: UUID,
    metric: str,
    start: datetime | None = Query(None),
    end: datetime | None = Query(None),
    _: Principal = Depends(require_capability("telemetry:metrics:read")),
    db: AsyncSession = Depends(get_db),
) -> dict:
    end = end or datetime.now(UTC)
    start = start or (end - timedelta(hours=1))

    result = await db.execute(
        _SERIES_SQL,
        {
            "site_id": site_id,
            "asset_id": asset_id,
            "metric": metric,
            "start": start,
            "end": end,
        },
    )
    # Match the prior OpenSearch response shape exactly: `ts` as ISO string,
    # `value` as the raw numeric. The frontend's chart code reads both verbatim.
    points = [{"ts": row.ts.isoformat(), "value": row.value} for row in result]
    return {
        "asset_id": str(asset_id),
        "metric": metric,
        "points": points,
        "count": len(points),
    }
