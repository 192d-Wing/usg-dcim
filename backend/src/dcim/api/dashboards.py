"""Enterprise + per-site dashboard endpoints.

These endpoints aggregate inventory metadata + telemetry rollups + alert state
into UI-ready payloads. They cap result sizes and never return raw enterprise-wide
device lists.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
from uuid import UUID

from fastapi import APIRouter, Depends, Query
from sqlalchemy import func, select
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..models.alerts import Alert, AlertState, Severity
from ..models.collectors import Collector, CollectorStatus
from ..models.inventory import Asset, Building, LifecycleState, Rack, Region, Room, Row, Site
from ..models.telemetry_meta import FreshnessState, TelemetrySource
from ..security.capabilities import DASHBOARD_READ
from ..security.deps import Principal, require_capability
from ..settings import get_settings

router = APIRouter(prefix="/dashboards", tags=["dashboards"])


@router.get("/enterprise")
async def enterprise_overview(
    _: Principal = Depends(require_capability(DASHBOARD_READ)),
    db: AsyncSession = Depends(get_db),
):
    """Top-level KPIs: site count, alerting sites, stale collectors, capacity at risk."""

    site_count = (await db.execute(select(func.count(Site.id)))).scalar_one()
    rack_count = (await db.execute(select(func.count(Rack.id)))).scalar_one()
    sites_active = (
        await db.execute(select(func.count(Site.id)).where(Site.lifecycle_state == LifecycleState.active))
    ).scalar_one()

    sites_with_critical = (
        await db.execute(
            select(func.count(func.distinct(Alert.site_id))).where(
                Alert.state == AlertState.firing, Alert.severity == Severity.critical
            )
        )
    ).scalar_one()

    s = get_settings()
    stale_threshold = datetime.now(UTC) - timedelta(seconds=s.collector_stale_seconds)
    stale_collectors = (
        await db.execute(
            select(func.count(Collector.id)).where(
                Collector.enabled.is_(True),
                (Collector.last_seen_at.is_(None)) | (Collector.last_seen_at < stale_threshold),
            )
        )
    ).scalar_one()

    healthy_collectors = (
        await db.execute(
            select(func.count(Collector.id)).where(Collector.status == CollectorStatus.healthy)
        )
    ).scalar_one()

    stale_sources = (
        await db.execute(
            select(func.count(TelemetrySource.id)).where(TelemetrySource.freshness == FreshnessState.stale)
        )
    ).scalar_one()

    return {
        "sites": {"total": site_count, "active": sites_active},
        "racks": {"total": rack_count},
        "alerts": {"sites_with_critical": sites_with_critical},
        "collectors": {"healthy": healthy_collectors, "stale": stale_collectors},
        "telemetry": {"stale_sources": stale_sources},
        "generated_at": datetime.now(UTC).isoformat(),
    }


@router.get("/sites/at-risk")
async def sites_at_risk(
    severity: Severity = Query(Severity.major),
    _: Principal = Depends(require_capability(DASHBOARD_READ)),
    db: AsyncSession = Depends(get_db),
):
    rows = (
        await db.execute(
            select(Alert.site_id, func.count(Alert.id).label("n"))
            .where(Alert.state == AlertState.firing, Alert.severity >= severity)
            .group_by(Alert.site_id)
            .order_by(func.count(Alert.id).desc())
            .limit(50)
        )
    ).all()
    return {"sites": [{"site_id": str(r.site_id), "alert_count": r.n} for r in rows]}


@router.get("/racks/{rack_id}")
async def rack_detail(
    rack_id: UUID,
    _: Principal = Depends(require_capability(DASHBOARD_READ)),
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


@router.get("/free-space")
async def free_space(
    u: int = Query(
        1, ge=0, le=60,
        description="Minimum contiguous U slots required (0 returns all racks for capacity overview)",
    ),
    site_id: UUID | None = Query(None),
    region_id: UUID | None = Query(None),
    min_kw_headroom: float | None = Query(None, description="Minimum unused kW the rack must still have"),
    limit: int = Query(50, ge=1, le=500),
    _: Principal = Depends(require_capability(DASHBOARD_READ)),
    db: AsyncSession = Depends(get_db),
):
    """Find racks with at least `u` contiguous free U slots, ranked by biggest run."""
    from ..services.capacity import find_free_space
    racks = await find_free_space(
        db, min_u=u, site_id=site_id, region_id=region_id,
        min_kw_headroom=min_kw_headroom, limit=limit,
    )
    return {"query": {"min_u": u, "site_id": str(site_id) if site_id else None,
                      "region_id": str(region_id) if region_id else None,
                      "min_kw_headroom": min_kw_headroom},
            "racks": racks, "count": len(racks)}


@router.get("/assets/{asset_id}")
async def asset_detail(
    asset_id: UUID,
    _: Principal = Depends(require_capability(DASHBOARD_READ)),
    db: AsyncSession = Depends(get_db),
):
    """Asset health: identity, telemetry sources, bound IPs, recent alerts."""
    from ..models.inventory import Asset
    from ..models.ipam import IPAddress

    asset = await db.get(Asset, asset_id)
    if asset is None:
        return {"error": "not_found"}

    sources = (
        await db.execute(
            select(TelemetrySource)
            .where(TelemetrySource.asset_id == asset_id)
            .order_by(TelemetrySource.metric.asc())
        )
    ).scalars().all()

    ip_rows = (
        await db.execute(
            select(IPAddress)
            .where(IPAddress.asset_id == asset_id)
            .order_by(IPAddress.role.asc(), IPAddress.address.asc())
        )
    ).scalars().all()

    recent_alerts = (
        await db.execute(
            select(Alert)
            .where(Alert.asset_id == asset_id)
            .order_by(Alert.last_seen_at.desc())
            .limit(10)
        )
    ).scalars().all()

    return {
        "asset": {
            "id": str(asset.id),
            "site_id": str(asset.site_id),
            "rack_id": str(asset.rack_id) if asset.rack_id else None,
            "name": asset.name,
            "hostname": asset.hostname,
            "kind": asset.kind.value if hasattr(asset.kind, "value") else asset.kind,
            "manufacturer": asset.manufacturer,
            "model": asset.model,
            "serial": asset.serial,
            "firmware": asset.firmware,
            "mgmt_ip": asset.mgmt_ip,
            "mgmt_protocol": asset.mgmt_protocol,
            "mgmt_port": asset.mgmt_port,
            "rack_position_u": asset.rack_position_u,
            "rack_units": asset.rack_units,
            "port_count": asset.port_count,
            "lifecycle_state": _enum_val(asset.lifecycle_state),
        },
        "telemetry_sources": [
            {
                "metric": s.metric,
                "unit": s.unit,
                "source_system": s.source_system,
                "freshness": s.freshness.value if hasattr(s.freshness, "value") else s.freshness,
                "last_value": float(s.last_value) if s.last_value is not None else None,
                "last_reading_at": s.last_reading_at.isoformat() if s.last_reading_at else None,
                "last_success_at": s.last_success_at.isoformat() if s.last_success_at else None,
                "poll_interval_seconds": s.poll_interval_seconds,
            }
            for s in sources
        ],
        "ip_addresses": [
            {
                "id": str(ip.id),
                "subnet_id": str(ip.subnet_id),
                "address": str(ip.address).split("/", 1)[0],
                "role": _enum_val(ip.role),
                "status": _enum_val(ip.status),
                "source": _enum_val(ip.source),
                "dns_name": ip.dns_name,
                "description": ip.description,
                "dhcp_lease_expires_at": (
                    ip.dhcp_lease_expires_at.isoformat() if ip.dhcp_lease_expires_at else None
                ),
            }
            for ip in ip_rows
        ],
        "recent_alerts": [
            {
                "id": str(a.id),
                "severity": a.severity.value if hasattr(a.severity, "value") else a.severity,
                "state": a.state.value if hasattr(a.state, "value") else a.state,
                "summary": a.summary,
                "first_seen_at": a.first_seen_at.isoformat(),
                "last_seen_at": a.last_seen_at.isoformat(),
            }
            for a in recent_alerts
        ],
    }


@router.get("/forecast/racks")
async def racks_forecast_batch(
    site_id: UUID | None = Query(None),
    limit: int = Query(200, ge=1, le=1000),
    _: Principal = Depends(require_capability(DASHBOARD_READ)),
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
    _: Principal = Depends(require_capability(DASHBOARD_READ)),
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
    payload["kw_forecast"] = await compute_rack_kw_forecast(rack, asset_list, days=kw_days)
    return payload


@router.get("/forecast/sites/{site_id}")
async def site_forecast(
    site_id: UUID,
    _: Principal = Depends(require_capability(DASHBOARD_READ)),
    db: AsyncSession = Depends(get_db),
):
    """Site-wide forecast rollup: U usage, worst-case rack runway, band counts."""
    from ..services.forecast import compute_site_forecast

    site = await db.get(Site, site_id)
    if site is None:
        return {"error": "not_found"}
    return await compute_site_forecast(db, site_id)


def _enum_val(v):
    return v.value if hasattr(v, "value") else v


async def _load_site_topology(
    db: AsyncSession, site_id: UUID,
) -> tuple[list[Building], list[Room], list[Row], list[Rack], list[Asset]]:
    buildings = (
        await db.execute(
            select(Building).where(Building.site_id == site_id).order_by(Building.code)
        )
    ).scalars().all()
    building_ids = [b.id for b in buildings]
    rooms: list[Room] = []
    if building_ids:
        rooms = list(
            (
                await db.execute(
                    select(Room).where(Room.building_id.in_(building_ids)).order_by(Room.code)
                )
            ).scalars().all()
        )
    room_ids = [r.id for r in rooms]
    rows: list[Row] = []
    if room_ids:
        rows = list(
            (
                await db.execute(
                    select(Row).where(Row.room_id.in_(room_ids)).order_by(Row.code)
                )
            ).scalars().all()
        )
    racks = list(
        (
            await db.execute(
                select(Rack).where(Rack.site_id == site_id).order_by(Rack.code)
            )
        ).scalars().all()
    )
    site_assets = list(
        (await db.execute(select(Asset).where(Asset.site_id == site_id))).scalars().all()
    )
    return list(buildings), rooms, rows, racks, site_assets


async def _site_capacity_rollup(
    db: AsyncSession, racks: list[Rack], assets_by_rack: dict[UUID, list[Asset]],
) -> tuple[dict[UUID, dict], dict]:
    """Per-rack capacity dict + summed site rollup, in one pass over racks."""
    from ..services.capacity import compute_rack_capacity

    rack_caps: dict[UUID, dict] = {}
    u_used = 0
    u_total = 0
    kw_max_sum = 0.0
    kw_current_sum = 0.0
    kw_max_known = False
    kw_current_known = False
    for r in racks:
        cap = await compute_rack_capacity(db, r, assets_by_rack.get(r.id, []))
        rack_caps[r.id] = cap
        u_used += cap["u_used"]
        u_total += cap["u_total"]
        if cap["kw_max"] is not None:
            kw_max_sum += cap["kw_max"]
            kw_max_known = True
        if cap["kw_current"] is not None:
            kw_current_sum += cap["kw_current"]
            kw_current_known = True

    rollup = {
        "u_used": u_used,
        "u_total": u_total,
        "u_free": max(0, u_total - u_used),
        "u_pct": round(100.0 * u_used / u_total, 1) if u_total else 0.0,
        "kw_max_sum": round(kw_max_sum, 2) if kw_max_known else None,
        "kw_current": round(kw_current_sum, 3) if kw_current_known else None,
        "kw_pct": (
            round(100.0 * kw_current_sum / kw_max_sum, 1)
            if kw_max_known and kw_current_known and kw_max_sum > 0
            else None
        ),
        "racks_total": len(racks),
        "racks_with_kw_rating": sum(1 for r in racks if r.max_kw is not None),
    }
    return rack_caps, rollup


async def _site_alerts_kpi(db: AsyncSession, site_id: UUID) -> dict[str, int]:
    rows = (
        await db.execute(
            select(Alert.severity, func.count(Alert.id))
            .where(Alert.site_id == site_id, Alert.state == AlertState.firing)
            .group_by(Alert.severity)
        )
    ).all()
    out: dict[str, int] = {s.value: 0 for s in Severity}
    for sev, n in rows:
        out[_enum_val(sev)] = int(n)
    out["total"] = sum(out.values())
    return out


async def _site_collectors_kpi(db: AsyncSession, site_id: UUID) -> dict[str, int]:
    collectors = (
        await db.execute(select(Collector).where(Collector.site_id == site_id))
    ).scalars().all()
    stale_threshold = datetime.now(UTC) - timedelta(
        seconds=get_settings().collector_stale_seconds
    )
    out: dict[str, int] = {st.value: 0 for st in CollectorStatus}
    out["total"] = len(collectors)
    out["stale"] = 0
    for c in collectors:
        out[_enum_val(c.status)] += 1
        if c.enabled and (c.last_seen_at is None or c.last_seen_at < stale_threshold):
            out["stale"] += 1
    return out


def _assets_by_lifecycle(site_assets: list[Asset]) -> dict[str, int]:
    out: dict[str, int] = {ls.value: 0 for ls in LifecycleState}
    out["total"] = len(site_assets)
    for a in site_assets:
        out[_enum_val(a.lifecycle_state)] += 1
    return out


def _build_hierarchy(
    buildings: list[Building], rooms: list[Room], rows: list[Row], racks: list[Rack],
    rack_caps: dict[UUID, dict], assets_by_rack: dict[UUID, list[Asset]],
) -> tuple[list[dict], list[dict]]:
    rooms_by_building: dict[UUID, list[Room]] = {}
    for room in rooms:
        rooms_by_building.setdefault(room.building_id, []).append(room)
    rows_by_room: dict[UUID, list[Row]] = {}
    for row in rows:
        rows_by_room.setdefault(row.room_id, []).append(row)
    racks_by_row: dict[UUID, list[Rack]] = {}
    for rk in racks:
        racks_by_row.setdefault(rk.row_id, []).append(rk)

    def rack_node(rk: Rack) -> dict:
        cap = rack_caps[rk.id]
        return {
            "id": str(rk.id), "name": rk.name, "code": rk.code,
            "u_height": rk.u_height, "u_used": cap["u_used"], "u_pct": cap["u_pct"],
            "kw_max": cap["kw_max"], "kw_current": cap["kw_current"],
            "asset_count": len(assets_by_rack.get(rk.id, [])),
        }

    def row_node(rw: Row) -> dict:
        return {
            "id": str(rw.id), "name": rw.name, "code": rw.code,
            "racks": [rack_node(rk) for rk in racks_by_row.get(rw.id, [])],
        }

    def room_node(rm: Room) -> dict:
        return {
            "id": str(rm.id), "name": rm.name, "code": rm.code,
            "design_kw": float(rm.design_kw) if rm.design_kw is not None else None,
            "rows": [row_node(rw) for rw in rows_by_room.get(rm.id, [])],
        }

    hierarchy = [
        {
            "id": str(b.id), "name": b.name, "code": b.code,
            "rooms": [room_node(rm) for rm in rooms_by_building.get(b.id, [])],
        }
        for b in buildings
    ]
    # Orphan racks (rack.row chain not anchored under this site's buildings list —
    # shouldn't happen with our schema, but surface defensively so the page
    # never silently drops a rack).
    placed = {rk.id for rw in rows for rk in racks_by_row.get(rw.id, [])}
    orphans = [rack_node(rk) for rk in racks if rk.id not in placed]
    return hierarchy, orphans


@router.get("/sites/{site_id}")
async def site_detail(
    site_id: UUID,
    _: Principal = Depends(require_capability(DASHBOARD_READ)),
    db: AsyncSession = Depends(get_db),
):
    """Site identity + KPIs + capacity rollup + buildings/rooms/rows/racks hierarchy.

    The hierarchy is the drill-down target from the sites list and dashboards;
    the KPIs are sized for a single overview screen. Capacity per rack reuses
    `compute_rack_capacity` so site-level numbers stay consistent with the rack
    page.
    """
    site = await db.get(Site, site_id)
    if site is None:
        return {"error": "not_found"}
    region = await db.get(Region, site.region_id)

    buildings, rooms, rows, racks, site_assets = await _load_site_topology(db, site_id)
    assets_by_rack: dict[UUID, list[Asset]] = {}
    for a in site_assets:
        if a.rack_id is not None:
            assets_by_rack.setdefault(a.rack_id, []).append(a)
    rack_caps, capacity = await _site_capacity_rollup(db, racks, assets_by_rack)
    hierarchy, orphan_racks = _build_hierarchy(
        buildings, rooms, rows, racks, rack_caps, assets_by_rack,
    )

    return {
        "site": {
            "id": str(site.id), "name": site.name, "code": site.code,
            "address": site.address, "timezone": site.timezone,
            "region_id": str(site.region_id),
            "majcom": site.majcom, "organization": site.organization,
            "mission_owner": site.mission_owner, "enclave": site.enclave,
            "classification": site.classification,
            "lifecycle_state": _enum_val(site.lifecycle_state),
        },
        "region": (
            {"id": str(region.id), "name": region.name, "code": region.code}
            if region is not None else None
        ),
        "kpis": {
            "buildings": len(buildings), "rooms": len(rooms),
            "rows": len(rows), "racks": len(racks),
            "assets": _assets_by_lifecycle(site_assets),
            "alerts_firing": await _site_alerts_kpi(db, site_id),
            "collectors": await _site_collectors_kpi(db, site_id),
        },
        "capacity": capacity,
        "hierarchy": hierarchy,
        "orphan_racks": orphan_racks,
    }
