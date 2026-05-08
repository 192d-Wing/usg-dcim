"""Unit tests for the capacity-forecasting service.

The math is small but load-bearing for runway badges in the UI. These tests
fix the boundary behavior (no growth, full rack, single timestamp, distinct
timestamps) so future refactors can't silently regress.
"""

from datetime import datetime, timedelta, timezone
from types import SimpleNamespace

from dcim.services.forecast import (
    _build_timeline,
    _linear_slope,
    compute_rack_forecast,
    compute_what_if,
)


class _MountSentinel:
    rack = "rack"


def _asset(*, u: int, units: int, created_at: datetime, mount: str = "rack"):
    """Lightweight Asset stand-in. The forecast service touches only these fields."""
    return SimpleNamespace(
        rack_position_u=u,
        rack_units=units,
        created_at=created_at,
        # mount comparison uses AssetMount.rack — match by .value through a real Asset, but
        # the service compares to AssetMount.rack which is the string "rack" via the mixin.
        mount=mount,
    )


def _rack(u_height: int = 24):
    return SimpleNamespace(id="rack-1", u_height=u_height)


# ---------- _linear_slope ----------

def test_linear_slope_returns_none_for_too_few_points():
    assert _linear_slope([], []) is None
    assert _linear_slope([1.0], [2.0]) is None


def test_linear_slope_returns_none_for_zero_x_variance():
    # All x equal -> denominator is 0; slope undefined.
    assert _linear_slope([5.0, 5.0, 5.0], [1.0, 2.0, 3.0]) is None


def test_linear_slope_recovers_known_slope():
    # y = 2x + 1
    assert _linear_slope([0.0, 1.0, 2.0, 3.0], [1.0, 3.0, 5.0, 7.0]) == 2.0


# ---------- _build_timeline ----------

def test_build_timeline_skips_unplaced_and_non_rack_mount():
    now = datetime(2026, 1, 1, tzinfo=timezone.utc)
    assets = [
        _asset(u=1, units=2, created_at=now),
        _asset(u=None, units=1, created_at=now),                       # unplaced
        _asset(u=10, units=1, created_at=now, mount="vertical-left"),  # vertical PDU
        _asset(u=5, units=4, created_at=now + timedelta(days=10)),
    ]
    times, cumulative = _build_timeline(assets)
    assert len(times) == 2
    assert cumulative == [2, 6]


def test_build_timeline_sorts_by_created_at():
    t1 = datetime(2026, 1, 1, tzinfo=timezone.utc)
    t2 = datetime(2026, 2, 1, tzinfo=timezone.utc)
    assets = [
        _asset(u=10, units=1, created_at=t2),
        _asset(u=1, units=2, created_at=t1),
    ]
    times, cumulative = _build_timeline(assets)
    assert times == [t1, t2]
    assert cumulative == [2, 3]


# ---------- compute_rack_forecast ----------

def test_forecast_empty_rack_reports_full_runway_unknown():
    result = compute_rack_forecast(_rack(24), [])
    assert result["u_used"] == 0
    assert result["u_free"] == 24
    assert result["slope_u_per_day"] is None
    assert result["runway_band"] == "unknown"


def test_forecast_full_rack_is_critical():
    now = datetime(2026, 5, 1, tzinfo=timezone.utc)
    assets = [_asset(u=1, units=24, created_at=now)]
    result = compute_rack_forecast(_rack(24), assets, now=now)
    assert result["u_free"] == 0
    assert result["runway_band"] == "critical"
    assert result["projected_fill_date"] is None


def test_forecast_single_placement_has_no_slope():
    now = datetime(2026, 5, 1, tzinfo=timezone.utc)
    assets = [_asset(u=1, units=2, created_at=now)]
    result = compute_rack_forecast(_rack(24), assets, now=now)
    assert result["slope_u_per_day"] is None
    assert result["projected_fill_date"] is None


def test_forecast_projects_fill_date_from_slope():
    # Two placements 100 days apart: 4U -> 8U => 4U / 100d = 0.04 U/day.
    # 16U remaining => 400 days runway.
    now = datetime(2026, 5, 1, tzinfo=timezone.utc)
    t0 = now - timedelta(days=100)
    assets = [
        _asset(u=1, units=4, created_at=t0),
        _asset(u=10, units=4, created_at=now),
    ]
    result = compute_rack_forecast(_rack(24), assets, now=now)
    assert result["u_used"] == 8
    assert result["slope_u_per_day"] is not None
    assert abs(result["slope_u_per_day"] - 0.04) < 1e-3
    assert result["days_until_full"] is not None
    assert abs(result["days_until_full"] - 400.0) < 1.0
    assert result["runway_band"] == "healthy"  # > 90 days


def test_forecast_runway_band_critical_when_under_30_days():
    # Steep slope: 12U placed in 10 days -> 1.2 U/day. 12U remaining -> 10 days.
    now = datetime(2026, 5, 1, tzinfo=timezone.utc)
    assets = [
        _asset(u=1, units=4, created_at=now - timedelta(days=10)),
        _asset(u=10, units=8, created_at=now),
    ]
    result = compute_rack_forecast(_rack(24), assets, now=now)
    assert result["days_until_full"] is not None
    assert result["days_until_full"] < 30
    assert result["runway_band"] == "critical"


# ---------- compute_what_if ----------

def test_what_if_subtracts_from_runway():
    now = datetime(2026, 5, 1, tzinfo=timezone.utc)
    # Slope = 0.04 U/day, 16U free, 400d runway. Add 4U -> 12U free -> 300d.
    assets = [
        _asset(u=1, units=4, created_at=now - timedelta(days=100)),
        _asset(u=10, units=4, created_at=now),
    ]
    result = compute_what_if(_rack(24), assets, add_units=4, now=now)
    assert result["what_if_u_used"] == 12
    assert result["what_if_u_free"] == 12
    assert abs(result["what_if_days_until_full"] - 300.0) < 1.0
    assert result["what_if_runway_band"] == "healthy"


def test_what_if_clamps_at_full():
    now = datetime(2026, 5, 1, tzinfo=timezone.utc)
    assets = [
        _asset(u=1, units=4, created_at=now - timedelta(days=100)),
        _asset(u=10, units=4, created_at=now),
    ]
    # Adding 100U exceeds capacity; should clamp + report critical.
    result = compute_what_if(_rack(24), assets, add_units=100, now=now)
    assert result["what_if_u_used"] == 24
    assert result["what_if_u_free"] == 0
    assert result["what_if_days_until_full"] == 0
    assert result["what_if_runway_band"] == "critical"
