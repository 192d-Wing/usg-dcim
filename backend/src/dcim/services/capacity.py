"""Rack capacity rollups: U utilization, kW utilization, contiguous free-space runs.

U utilization is computable from inventory alone. kW utilization joins inventory
to the TelemetrySource freshness table to read the latest PDU input power.

Power-metric convention: any TelemetrySource on a PDU asset whose metric matches
one of POWER_METRIC_NAMES contributes to the rack's current kW. Add new vendor-
specific metric names here if needed; alternative would be to make this a
per-site configuration.
"""

from __future__ import annotations

from uuid import UUID

from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from ..models.inventory import Asset, AssetKind, Rack
from ..models.telemetry_meta import FreshnessState, TelemetrySource

# Metrics treated as "input kW" for rollup. PDUs that report W instead get scaled.
POWER_METRIC_KW = {"pdu.input.kw", "power.consumed.kW", "rack.input.kw"}
POWER_METRIC_W = {"power.consumed.W", "pdu.input.w"}


def _free_runs(slot_used: list[bool], u_height: int) -> list[dict]:
    """Return sorted (longest first) list of contiguous free U runs as [{start_u, length}]."""
    runs: list[dict] = []
    cur_start: int | None = None
    cur_len = 0
    for u in range(1, u_height + 1):
        if not slot_used[u]:
            if cur_start is None:
                cur_start = u
            cur_len += 1
        else:
            if cur_start is not None:
                runs.append({"start_u": cur_start, "length": cur_len})
            cur_start = None
            cur_len = 0
    if cur_start is not None:
        runs.append({"start_u": cur_start, "length": cur_len})
    runs.sort(key=lambda r: (-r["length"], r["start_u"]))
    return runs


def slots_used(assets: list[Asset], u_height: int) -> list[bool]:
    """Build a 1-indexed boolean array; slot[u]=True if any placed asset occupies u."""
    used = [False] * (u_height + 2)
    for a in assets:
        if not a.rack_position_u or a.rack_position_u < 1 or a.rack_position_u > u_height:
            continue
        span = max(1, a.rack_units or 1)
        for u in range(a.rack_position_u, min(a.rack_position_u + span, u_height + 1)):
            used[u] = True
    return used


async def compute_rack_capacity(db: AsyncSession, rack: Rack, assets: list[Asset]) -> dict:
    """Capacity rollup for one rack — small, suitable to embed in /dashboards/racks/{id}."""
    used = slots_used(assets, rack.u_height)
    u_used = sum(1 for v in used[1:rack.u_height + 1] if v)
    u_total = rack.u_height
    runs = _free_runs(used, rack.u_height)

    # kW rollup from PDU telemetry. Only count current (non-stale) sources.
    pdu_ids = [a.id for a in assets if a.kind == AssetKind.pdu]
    kw_current: float | None = None
    if pdu_ids:
        rows = (
            await db.execute(
                select(TelemetrySource.metric, TelemetrySource.last_value, TelemetrySource.freshness)
                .where(
                    TelemetrySource.asset_id.in_(pdu_ids),
                    TelemetrySource.last_value.is_not(None),
                    TelemetrySource.freshness == FreshnessState.current,
                )
            )
        ).all()
        total = 0.0
        any_value = False
        for metric, value, _fresh in rows:
            if metric in POWER_METRIC_KW:
                total += float(value)
                any_value = True
            elif metric in POWER_METRIC_W:
                total += float(value) / 1000.0
                any_value = True
        if any_value:
            kw_current = round(total, 3)

    kw_max = float(rack.max_kw) if rack.max_kw is not None else None

    return {
        "u_used": u_used,
        "u_total": u_total,
        "u_pct": round(100.0 * u_used / u_total, 1) if u_total else 0.0,
        "u_free": u_total - u_used,
        "kw_current": kw_current,
        "kw_max": kw_max,
        "kw_pct": (
            round(100.0 * kw_current / kw_max, 1)
            if kw_current is not None and kw_max
            else None
        ),
        "biggest_contiguous_free": runs[0]["length"] if runs else 0,
        "free_runs": runs[:8],  # cap to keep payload tight
    }


async def find_free_space(
    db: AsyncSession,
    *,
    min_u: int,
    site_id: UUID | None = None,
    region_id: UUID | None = None,
    min_kw_headroom: float | None = None,
    limit: int = 50,
) -> list[dict]:
    """Rank racks by their biggest contiguous free run, optionally filtered by site/region/headroom."""
    stmt = select(Rack)
    if site_id is not None:
        stmt = stmt.where(Rack.site_id == site_id)
    if region_id is not None:
        from ..models.inventory import Site
        stmt = stmt.where(Rack.site_id.in_(select(Site.id).where(Site.region_id == region_id)))

    racks = (await db.execute(stmt)).scalars().all()

    out: list[dict] = []
    for r in racks:
        rack_assets = (
            await db.execute(select(Asset).where(Asset.rack_id == r.id))
        ).scalars().all()
        cap = await compute_rack_capacity(db, r, rack_assets)
        if cap["biggest_contiguous_free"] < min_u:
            continue
        if min_kw_headroom is not None and cap["kw_max"] is not None and cap["kw_current"] is not None:
            if (cap["kw_max"] - cap["kw_current"]) < min_kw_headroom:
                continue
        out.append({
            "rack_id": str(r.id),
            "site_id": str(r.site_id),
            "code": r.code,
            "name": r.name,
            "u_height": r.u_height,
            **cap,
        })
    out.sort(key=lambda x: -x["biggest_contiguous_free"])
    return out[:limit]
