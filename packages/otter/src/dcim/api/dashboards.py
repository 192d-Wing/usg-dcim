"""Enterprise + per-site dashboard endpoints.

These endpoints aggregate inventory metadata + telemetry rollups + alert state
into UI-ready payloads. They cap result sizes and never return raw enterprise-wide
device lists.
"""

from __future__ import annotations

from uuid import UUID

from fastapi import APIRouter, Depends, Query
from sqlalchemy import func, select
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..models.alerts import Alert, AlertState
from ..models.inventory import Asset, Rack, Site
from ..models.telemetry_meta import TelemetrySource
from ..security.deps import Principal, require_capability

router = APIRouter(prefix="/dashboards", tags=["dashboards"])

# /api/v1/dashboards/enterprise (Phase 1) + /free-space (Phase 2
# capacity) + /sites/at-risk (Phase 2b) all moved to otter-go. The
# remaining /dashboards/* routes (racks/{id}, assets/{id}, forecasts,
# sites/{id}) stay here until the service-helper ports land.

@router.get("/racks/{rack_id}")
async def rack_detail(
    rack_id: UUID,
    _: Principal = Depends(require_capability("dashboards:dashboards:read")),
    db: AsyncSession = Depends(get_db),
):
    """Rack + ordered assets + per-asset freshness summary, used by the rack visualization."""
    from ..models.inventory import Asset
    from ..models.inventory import Rack as RackModel

    rack = await db.get(RackModel, rack_id)
    if rack is None:
        return {"error": "not_found"}
    assets = (
        await db.execute(
            select(Asset).where(Asset.rack_id == rack_id).order_by(Asset.rack_position_u.asc().nullslast())
        )
    ).scalars().all()

    # Open alerts grouped by asset
    asset_ids = [a.id for a in assets]
    open_alert_count: dict[str, int] = {}
    if asset_ids:
        rows = (
            await db.execute(
                select(Alert.asset_id, func.count(Alert.id))
                .where(Alert.asset_id.in_(asset_ids), Alert.state == AlertState.firing)
                .group_by(Alert.asset_id)
            )
        ).all()
        open_alert_count = {str(r[0]): int(r[1]) for r in rows}

    # Telemetry source freshness per asset
    fresh_rows = (
        await db.execute(
            select(TelemetrySource.asset_id, TelemetrySource.freshness, func.count())
            .where(TelemetrySource.asset_id.in_(asset_ids) if asset_ids else False)
            .group_by(TelemetrySource.asset_id, TelemetrySource.freshness)
        )
    ).all() if asset_ids else []
    by_asset_freshness: dict[str, dict[str, int]] = {}
    for aid, fresh, n in fresh_rows:
        key = fresh.value if hasattr(fresh, "value") else fresh
        by_asset_freshness.setdefault(str(aid), {})[key] = int(n)

    from ..services.capacity import compute_rack_capacity
    from ..services.power_chain import compute_power_chain
    capacity = await compute_rack_capacity(db, rack, list(assets))
    power_chain = await compute_power_chain(db, list(assets))

    def enum_val(v):
        return v.value if v is not None and hasattr(v, "value") else v

    return {
        "rack": {
            "id": str(rack.id),
            "site_id": str(rack.site_id),
            "row_id": str(rack.row_id),
            "name": rack.name,
            "code": rack.code,
            "u_height": rack.u_height,
            "max_kw": float(rack.max_kw) if rack.max_kw is not None else None,
            "serial": rack.serial,
        },
        "capacity": capacity,
        "power_chain": power_chain,
        "assets": [
            {
                "id": str(a.id),
                "name": a.name,
                "hostname": a.hostname,
                "kind": enum_val(a.kind),
                "manufacturer": a.manufacturer,
                "model": a.model,
                "serial": a.serial,
                "rack_position_u": a.rack_position_u,
                "rack_units": a.rack_units or 1,
                "face": enum_val(a.face),
                "mount": enum_val(a.mount),
                "pdu_side": enum_val(a.pdu_side),
                "psu_count": a.psu_count,
                "port_count": a.port_count,
                "lifecycle_state": enum_val(a.lifecycle_state),
                "open_alerts": open_alert_count.get(str(a.id), 0),
                "freshness": by_asset_freshness.get(str(a.id), {}),
                "redundancy": power_chain["per_asset"].get(str(a.id), {}).get("redundancy"),
            }
            for a in assets
        ],
    }

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
