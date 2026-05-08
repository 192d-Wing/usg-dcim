"""Capacity forecasting: project when racks/sites will fill at current growth rate.

Approach: linear regression on cumulative U-used over time, using each asset's
created_at as a proxy for placement-time. Imperfect for assets that were moved
between racks (created_at != entry-into-this-rack), but for the bulk of assets
that stay put it's a faithful trend signal. A future revision can reconstruct
exact placement timeline from the audit log.

Math: ordinary least squares on (days_since_first_placement, cumulative_u).
slope_u_per_day < epsilon → "no growth" (don't project a fill date).

kW forecasting (compute_rack_kw_forecast) reuses the same OLS slope but feeds
it daily-averaged kW samples from Elasticsearch — summed across PDU assets in
the rack. Same band semantics: critical/warning/healthy/unknown.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
from uuid import UUID

from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from ..models.inventory import Asset, AssetKind, AssetMount, Rack
from .capacity import POWER_METRIC_KW, POWER_METRIC_W

_NO_GROWTH_EPS = 1e-6


def _linear_slope(xs: list[float], ys: list[float]) -> float | None:
    """Ordinary least squares slope. Returns None when undefined (n<2 or zero variance)."""
    n = len(xs)
    if n < 2:
        return None
    mx = sum(xs) / n
    my = sum(ys) / n
    num = sum((x - mx) * (y - my) for x, y in zip(xs, ys, strict=False))
    den = sum((x - mx) ** 2 for x in xs)
    if den < _NO_GROWTH_EPS:
        return None
    return num / den


def _build_timeline(assets: list[Asset]) -> tuple[list[datetime], list[int]]:
    """Cumulative U placed in this rack over time, sorted by placement date."""
    placed = [
        a for a in assets
        if a.rack_position_u and a.mount == AssetMount.rack and a.created_at
    ]
    placed.sort(key=lambda a: a.created_at)
    times: list[datetime] = []
    cumulative: list[int] = []
    total = 0
    for a in placed:
        total += max(1, a.rack_units or 1)
        times.append(a.created_at)
        cumulative.append(total)
    return times, cumulative


def _runway_band(days: float | None) -> str:
    if days is None:
        return "unknown"
    if days < 30:
        return "critical"
    if days < 90:
        return "warning"
    return "healthy"


def compute_rack_forecast(rack: Rack, assets: list[Asset], *, now: datetime | None = None) -> dict:
    """Per-rack U-fill forecast.

    Returns a payload safe to embed under /dashboards/forecast/racks/{id}:
      - history: [{ts, u_used}] points used for the regression
      - u_used / u_total / u_free: snapshot
      - slope_u_per_day: positive number, or None when no growth detected
      - days_until_full / projected_fill_date: None when slope is None or rack is already full
      - runway_band: critical|warning|healthy|unknown — UX hint for badges
    """
    now = now or datetime.now(UTC)
    times, cumulative = _build_timeline(assets)
    u_total = rack.u_height
    u_used = cumulative[-1] if cumulative else 0
    u_free = max(0, u_total - u_used)
    history = [
        {"ts": t.isoformat(), "u_used": u}
        for t, u in zip(times, cumulative, strict=False)
    ]

    if len(times) < 2 or u_free == 0:
        return {
            "rack_id": str(rack.id),
            "u_used": u_used, "u_total": u_total, "u_free": u_free,
            "history": history,
            "slope_u_per_day": None,
            "days_until_full": None,
            "projected_fill_date": None,
            "runway_band": "unknown" if u_free else "critical",
        }

    base = times[0]
    days_x = [(t - base).total_seconds() / 86400.0 for t in times]
    slope = _linear_slope(days_x, [float(u) for u in cumulative])
    if slope is None or slope < _NO_GROWTH_EPS:
        return {
            "rack_id": str(rack.id),
            "u_used": u_used, "u_total": u_total, "u_free": u_free,
            "history": history,
            "slope_u_per_day": None,
            "days_until_full": None,
            "projected_fill_date": None,
            "runway_band": "healthy",
        }

    days_until_full = u_free / slope
    fill_date = now + timedelta(days=days_until_full)
    return {
        "rack_id": str(rack.id),
        "u_used": u_used, "u_total": u_total, "u_free": u_free,
        "history": history,
        "slope_u_per_day": round(slope, 4),
        "days_until_full": round(days_until_full, 1),
        "projected_fill_date": fill_date.isoformat(),
        "runway_band": _runway_band(days_until_full),
    }


def compute_what_if(
    rack: Rack, assets: list[Asset], *,
    add_units: int, now: datetime | None = None,
) -> dict:
    """Project the runway impact of adding `add_units` U to this rack right now.

    Reuses the historical slope from compute_rack_forecast. Caller supplies the
    delta; we recompute days_until_full assuming the snapshot jumps by add_units.
    """
    base = compute_rack_forecast(rack, assets, now=now)
    slope = base["slope_u_per_day"]
    new_u_used = min(rack.u_height, base["u_used"] + max(0, add_units))
    new_u_free = max(0, rack.u_height - new_u_used)
    if slope is None or slope < _NO_GROWTH_EPS or new_u_free == 0:
        return {
            **base,
            "what_if_add_units": add_units,
            "what_if_u_used": new_u_used,
            "what_if_u_free": new_u_free,
            "what_if_days_until_full": 0 if new_u_free == 0 else None,
            "what_if_runway_band": "critical" if new_u_free == 0 else base["runway_band"],
        }
    days = new_u_free / slope
    return {
        **base,
        "what_if_add_units": add_units,
        "what_if_u_used": new_u_used,
        "what_if_u_free": new_u_free,
        "what_if_days_until_full": round(days, 1),
        "what_if_runway_band": _runway_band(days),
    }


def slope_from_buckets(
    buckets: list[tuple[datetime, float]],
) -> tuple[float | None, float | None]:
    """OLS slope (units/day) and intercept for a list of (timestamp, value) samples.

    Returns (None, None) when there are fewer than 2 points or no x-variance.
    Pure function so tests don't need an ES mock.
    """
    if len(buckets) < 2:
        return None, None
    base = buckets[0][0]
    xs = [(t - base).total_seconds() / 86400.0 for t, _ in buckets]
    ys = [v for _, v in buckets]
    slope = _linear_slope(xs, ys)
    if slope is None:
        return None, None
    mx = sum(xs) / len(xs)
    my = sum(ys) / len(ys)
    intercept = my - slope * mx
    return slope, intercept


def _kw_runway_band(days: float | None) -> str:
    return _runway_band(days)


def _kw_payload(
    *, max_kw: float | None, days: int, samples: int,
    slope: float | None, current_kw: float | None,
    days_until_max: float | None, projected_max_date: str | None,
    band: str,
) -> dict:
    return {
        "max_kw": max_kw,
        "days": days,
        "samples": samples,
        "slope_kw_per_day": round(slope, 6) if slope is not None else None,
        "current_kw": round(current_kw, 3) if current_kw is not None else None,
        "days_until_max": days_until_max,
        "projected_max_date": projected_max_date,
        "runway_band": band,
    }


def _samples_from_es_buckets(day_buckets: list[dict]) -> list[tuple[datetime, float]]:
    samples: list[tuple[datetime, float]] = []
    for bucket in day_buckets:
        total = 0.0
        any_val = False
        for m in bucket.get("by_metric", {}).get("buckets", []):
            avg = m.get("avg_v", {}).get("value")
            if avg is None:
                continue
            v = float(avg) / 1000.0 if m["key"] in POWER_METRIC_W else float(avg)
            total += v
            any_val = True
        if any_val:
            ts = datetime.fromtimestamp(bucket["key"] / 1000.0, tz=UTC)
            samples.append((ts, total))
    return samples


def _project_kw(
    samples: list[tuple[datetime, float]],
    *, max_kw: float | None, days: int, now: datetime,
) -> dict:
    """Pure projection logic — given parsed samples, return the kW forecast payload."""
    current_kw = samples[-1][1] if samples else None

    if len(samples) < 2:
        return _kw_payload(
            max_kw=max_kw, days=days, samples=len(samples),
            slope=None, current_kw=current_kw,
            days_until_max=None, projected_max_date=None, band="unknown",
        )

    slope, _ = slope_from_buckets(samples)
    no_growth = slope is None or slope < _NO_GROWTH_EPS
    if no_growth or max_kw is None or current_kw is None:
        return _kw_payload(
            max_kw=max_kw, days=days, samples=len(samples),
            slope=slope, current_kw=current_kw,
            days_until_max=None, projected_max_date=None,
            band="healthy" if no_growth else "unknown",
        )

    headroom = max_kw - current_kw
    if headroom <= 0:
        return _kw_payload(
            max_kw=max_kw, days=days, samples=len(samples),
            slope=slope, current_kw=current_kw,
            days_until_max=0.0, projected_max_date=now.isoformat(), band="critical",
        )
    days_until = headroom / slope
    return _kw_payload(
        max_kw=max_kw, days=days, samples=len(samples),
        slope=slope, current_kw=current_kw,
        days_until_max=round(days_until, 1),
        projected_max_date=(now + timedelta(days=days_until)).isoformat(),
        band=_kw_runway_band(days_until),
    )


def _site_indices(site_id: UUID, start: datetime, end: datetime) -> list[str]:
    from .elastic import telemetry_index
    indices: list[str] = []
    cur = start.replace(day=1)
    while cur <= end:
        indices.append(telemetry_index(str(site_id), cur))
        cur = (cur.replace(day=28) + timedelta(days=4)).replace(day=1)
    return indices


async def compute_rack_kw_forecast(
    rack: Rack, rack_assets: list[Asset], *, days: int = 90,
    now: datetime | None = None,
) -> dict | None:
    """Project when this rack's kW load reaches max_kw at the current growth rate.

    Returns None when the rack has no PDU assets — kW forecasting requires
    PDU telemetry. When PDUs exist but ES has no samples or is unreachable,
    returns a payload with slope=None so the UI renders "no trend yet."
    """
    pdu_ids = [a.id for a in rack_assets if a.kind == AssetKind.pdu]
    if not pdu_ids:
        return None

    now = now or datetime.now(UTC)
    start = now - timedelta(days=days)
    max_kw = float(rack.max_kw) if rack.max_kw else None

    from .elastic import client

    metrics = list(POWER_METRIC_KW | POWER_METRIC_W)
    es = client()
    try:
        resp = await es.search(
            index=",".join(_site_indices(rack.site_id, start, now)),
            body={
                "size": 0,
                "query": {
                    "bool": {
                        "filter": [
                            {"terms": {"asset_id": [str(p) for p in pdu_ids]}},
                            {"terms": {"metric": metrics}},
                            {"range": {"ts": {"gte": start.isoformat(), "lte": now.isoformat()}}},
                        ]
                    }
                },
                "aggs": {
                    "by_day": {
                        "date_histogram": {"field": "ts", "fixed_interval": "1d"},
                        "aggs": {
                            "by_metric": {
                                "terms": {"field": "metric", "size": 20},
                                "aggs": {"avg_v": {"avg": {"field": "value"}}},
                            },
                        },
                    },
                },
            },
            ignore_unavailable=True,
        )
    except Exception:
        # ES unreachable / index missing — degrade so the U-only forecast still renders.
        return _kw_payload(
            max_kw=max_kw, days=days, samples=0,
            slope=None, current_kw=None,
            days_until_max=None, projected_max_date=None, band="unknown",
        )

    day_buckets = resp.get("aggregations", {}).get("by_day", {}).get("buckets", [])
    samples = _samples_from_es_buckets(day_buckets)
    return _project_kw(samples, max_kw=max_kw, days=days, now=now)


async def compute_site_forecast(db: AsyncSession, site_id: UUID) -> dict:
    """Site-wide rollup: aggregate U used + total across racks, and the worst per-rack runway."""
    racks = (await db.execute(select(Rack).where(Rack.site_id == site_id))).scalars().all()
    if not racks:
        return {
            "site_id": str(site_id),
            "rack_count": 0,
            "u_used": 0, "u_total": 0, "u_pct": 0.0,
            "min_runway_days": None, "min_runway_rack_id": None,
            "racks_critical": 0, "racks_warning": 0, "racks_healthy": 0,
        }
    rack_ids = [r.id for r in racks]
    all_assets = (
        await db.execute(select(Asset).where(Asset.rack_id.in_(rack_ids)))
    ).scalars().all()
    by_rack: dict[UUID, list[Asset]] = {}
    for a in all_assets:
        by_rack.setdefault(a.rack_id, []).append(a)

    u_used = u_total = 0
    min_runway: float | None = None
    min_runway_rack: UUID | None = None
    bands = {"critical": 0, "warning": 0, "healthy": 0}
    for r in racks:
        forecast = compute_rack_forecast(r, by_rack.get(r.id, []))
        u_used += forecast["u_used"]
        u_total += forecast["u_total"]
        days = forecast["days_until_full"]
        if days is not None and (min_runway is None or days < min_runway):
            min_runway = days
            min_runway_rack = r.id
        band = forecast["runway_band"]
        if band in bands:
            bands[band] += 1
    return {
        "site_id": str(site_id),
        "rack_count": len(racks),
        "u_used": u_used, "u_total": u_total,
        "u_pct": round(100.0 * u_used / u_total, 1) if u_total else 0.0,
        "min_runway_days": min_runway,
        "min_runway_rack_id": str(min_runway_rack) if min_runway_rack else None,
        "racks_critical": bands["critical"],
        "racks_warning": bands["warning"],
        "racks_healthy": bands["healthy"],
    }
