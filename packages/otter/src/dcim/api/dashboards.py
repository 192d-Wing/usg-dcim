"""Enterprise + per-site dashboard endpoints.

These endpoints aggregate inventory metadata + telemetry rollups + alert state
into UI-ready payloads. They cap result sizes and never return raw enterprise-wide
device lists.
"""

from __future__ import annotations

from uuid import UUID

from fastapi import APIRouter, Depends, Query
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..models.inventory import Asset, Rack, Site
from ..security.deps import Principal, require_capability

router = APIRouter(prefix="/dashboards", tags=["dashboards"])

# /api/v1/dashboards/enterprise (Phase 1) + /free-space (Phase 2
# capacity) + /sites/at-risk (Phase 2b) all moved to otter-go. The
# remaining /dashboards/* routes (racks/{id}, assets/{id}, forecasts,
# sites/{id}) stay here until the service-helper ports land.


# /api/v1/dashboards/free-space moved to otter-go (Phase 2 of the
# dashboards port). The capacity rollup primitives live in
# packages/otter-go/internal/capacity (port of services/capacity.py);
# services/capacity.find_free_space is still imported by the racks/
# {id} and sites/{id} endpoints below and stays here until Phase 2b.

# /api/v1/dashboards/assets/{asset_id} moved to otter-go (Phase 2c).
# Identity + telemetry sources + bound IPs + 10 most-recent alerts
# joined via four sequential reads. The remaining /dashboards/*
# routes still here are racks/{id}, sites/{id}, and the 3 forecast
# endpoints (need services/power_chain + services/forecast ports).

@router.get("/forecast/racks")
async def racks_forecast_batch(
    site_id: UUID | None = Query(None),
    limit: int = Query(200, ge=1, le=1000),
    _: Principal = Depends(require_capability("dashboards:dashboards:read")),
    db: AsyncSession = Depends(get_db),
):
    """Batch forecast for many racks. Used by the racks-list runway column.

    Strips the per-rack history array to keep the payload tight; callers that
    need history fetch the per-rack endpoint.
    """
    from ..services.forecast import compute_rack_forecast

    stmt = select(Rack)
    if site_id is not None:
        stmt = stmt.where(Rack.site_id == site_id)
    racks = (await db.execute(stmt.limit(limit))).scalars().all()
    if not racks:
        return {"racks": []}
    rack_ids = [r.id for r in racks]
    all_assets = (
        await db.execute(select(Asset).where(Asset.rack_id.in_(rack_ids)))
    ).scalars().all()
    by_rack: dict[UUID, list[Asset]] = {}
    for a in all_assets:
        by_rack.setdefault(a.rack_id, []).append(a)
    out = []
    for r in racks:
        f = compute_rack_forecast(r, by_rack.get(r.id, []))
        f.pop("history", None)
        out.append(f)
    return {"racks": out}

@router.get("/forecast/racks/{rack_id}")
async def rack_forecast(
    rack_id: UUID,
    add_units: int = Query(0, ge=0, le=60, description="What-if: project runway after adding this many U."),
    kw_days: int = Query(90, ge=7, le=365, description="Window for kW-trend regression."),
    _: Principal = Depends(require_capability("dashboards:dashboards:read")),
    db: AsyncSession = Depends(get_db),
):
    """Per-rack U-fill forecast + kW-trend forecast + optional what-if delta."""
    from ..services.forecast import (
        compute_rack_forecast,
        compute_rack_kw_forecast,
        compute_what_if,
    )

    rack = await db.get(Rack, rack_id)
    if rack is None:
        return {"error": "not_found"}
    asset_list = list(
        (await db.execute(select(Asset).where(Asset.rack_id == rack_id))).scalars().all()
    )
    if add_units > 0:
        payload = compute_what_if(rack, asset_list, add_units=add_units)
    else:
        payload = compute_rack_forecast(rack, asset_list)
    payload["kw_forecast"] = await compute_rack_kw_forecast(db, rack, asset_list, days=kw_days)
    return payload

@router.get("/forecast/sites/{site_id}")
async def site_forecast(
    site_id: UUID,
    _: Principal = Depends(require_capability("dashboards:dashboards:read")),
    db: AsyncSession = Depends(get_db),
):
    """Site-wide forecast rollup: U usage, worst-case rack runway, band counts."""
    from ..services.forecast import compute_site_forecast

    site = await db.get(Site, site_id)
    if site is None:
        return {"error": "not_found"}
    return await compute_site_forecast(db, site_id)


# /api/v1/dashboards/sites/{site_id} moved to otter-go (Phase 2d).
# Site identity + region + KPIs (buildings/rooms/rows/racks + assets-
# by-lifecycle + alerts-by-severity + collectors-by-status) + capacity
# rollup + buildings/rooms/rows/racks hierarchy + orphan-rack
# defensive surface, all assembled from internal/capacity.
# ComputeManyRackCapacity + a half-dozen topology/KPI queries. The
# _enum_val / _load_site_topology / _site_capacity_rollup /
# _site_alerts_kpi / _site_collectors_kpi / _assets_by_lifecycle /
# _build_hierarchy helpers retired with the route.
