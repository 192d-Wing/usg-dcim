"""Read-side telemetry: time-range queries against Elastic."""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
from uuid import UUID

from fastapi import APIRouter, Depends, Query

from ..security.capabilities import TELEMETRY_READ
from ..security.deps import Principal, require_capability
from ..services.elastic import client, telemetry_index

router = APIRouter(prefix="/telemetry", tags=["telemetry"])


@router.get("/series")
async def get_series(
    site_id: UUID,
    asset_id: UUID,
    metric: str,
    start: datetime | None = Query(None),
    end: datetime | None = Query(None),
    _: Principal = Depends(require_capability(TELEMETRY_READ)),
) -> dict:
    end = end or datetime.now(UTC)
    start = start or (end - timedelta(hours=1))

    indices = []
    cur = start.replace(day=1)
    while cur <= end:
        indices.append(telemetry_index(str(site_id), cur))
        cur = (cur.replace(day=28) + timedelta(days=4)).replace(day=1)

    es = client()
    resp = await es.search(
        index=",".join(indices),
        body={
            "size": 10000,
            "_source": ["ts", "value", "unit"],
            "query": {
                "bool": {
                    "filter": [
                        {"term": {"asset_id": str(asset_id)}},
                        {"term": {"metric": metric}},
                        {"range": {"ts": {"gte": start.isoformat(), "lte": end.isoformat()}}},
                    ]
                }
            },
            "sort": [{"ts": "asc"}],
        },
        ignore_unavailable=True,
    )
    points = [{"ts": h["_source"]["ts"], "value": h["_source"]["value"]} for h in resp["hits"]["hits"]]
    return {"asset_id": str(asset_id), "metric": metric, "points": points, "count": len(points)}
