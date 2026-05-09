"""Unit tests for the capacity helpers.

Pure functions only — `compute_rack_capacity` is async and exercises the DB
when there are PDU assets, so we keep DB-touching paths out of these tests
and cover them with the integration suite later.
"""

from types import SimpleNamespace
from uuid import uuid4

import pytest

from dcim.services.capacity import (
    _free_runs,
    compute_rack_capacity,
    slots_used,
)


def _asset(*, u, units=1, kind="server"):
    return SimpleNamespace(
        id=uuid4(),
        rack_position_u=u,
        rack_units=units,
        kind=SimpleNamespace(value=kind, name=kind, _value_=kind, __eq__=lambda self, o: o == kind),
    )


def _rack(u_height=24, max_kw=None):
    return SimpleNamespace(id=uuid4(), u_height=u_height, max_kw=max_kw)


# ---------- _free_runs ----------

def test_free_runs_empty_rack_is_one_full_run():
    used = [False] * 27  # u=1..24 all free
    runs = _free_runs(used, 24)
    assert runs == [{"start_u": 1, "length": 24}]


def test_free_runs_full_rack_has_no_runs():
    used = [False] + [True] * 24 + [False] * 2
    assert _free_runs(used, 24) == []


def test_free_runs_returns_runs_sorted_longest_first():
    # U1 free, U2-3 used, U4-7 free (4U), U8 used, U9-10 free (2U)
    used = [False, False, True, True, False, False, False, False, True, False, False]
    runs = _free_runs(used, 10)
    assert runs == [
        {"start_u": 4, "length": 4},
        {"start_u": 9, "length": 2},
        {"start_u": 1, "length": 1},
    ]


def test_free_runs_with_tied_lengths_sorts_by_position():
    # Two 2-U gaps: U1-2 and U5-6. Same length -> earlier start_u wins.
    used = [False, False, False, True, True, False, False]
    runs = _free_runs(used, 6)
    assert runs[0] == {"start_u": 1, "length": 2}
    assert runs[1] == {"start_u": 5, "length": 2}


# ---------- slots_used ----------

def test_slots_used_marks_multi_u_span():
    a = _asset(u=5, units=4)
    used = slots_used([a], u_height=24)
    assert used[5] and used[6] and used[7] and used[8]
    assert not used[4] and not used[9]


def test_slots_used_skips_unplaced_assets():
    a = _asset(u=None, units=2)
    used = slots_used([a], u_height=24)
    assert not any(used[1:25])


def test_slots_used_clamps_overflow_at_top():
    # Asset declared 4U at U23 in a 24U rack should mark U23-24 only.
    a = _asset(u=23, units=4)
    used = slots_used([a], u_height=24)
    assert used[23] and used[24]
    # Beyond top: depending on slot index it would silently no-op
    assert len(used) == 26  # u_height + 2


def test_slots_used_treats_zero_units_as_one_u():
    a = _asset(u=10, units=0)
    used = slots_used([a], u_height=24)
    assert used[10]
    assert not used[11]


# ---------- compute_rack_capacity (no PDUs => no DB) ----------

@pytest.mark.asyncio
async def test_compute_rack_capacity_empty_rack():
    cap = await compute_rack_capacity(db=None, rack=_rack(24, max_kw=10), assets=[])
    assert cap["u_used"] == 0
    assert cap["u_total"] == 24
    assert cap["u_free"] == 24
    assert cap["kw_current"] is None
    assert cap["kw_max"] == 10
    assert cap["kw_pct"] is None
    assert cap["biggest_contiguous_free"] == 24


@pytest.mark.asyncio
async def test_compute_rack_capacity_partial_fill_no_pdu():
    rack = _rack(24, max_kw=10)
    assets = [_asset(u=1, units=4), _asset(u=10, units=2)]
    cap = await compute_rack_capacity(db=None, rack=rack, assets=assets)
    assert cap["u_used"] == 6
    assert cap["u_pct"] == 25.0
    assert cap["u_free"] == 18
    assert cap["biggest_contiguous_free"] >= 12
    # No PDU assets in the list — DB never gets touched, kW stays None.
    assert cap["kw_current"] is None


@pytest.mark.asyncio
async def test_compute_rack_capacity_no_max_kw_field():
    rack = _rack(24)  # max_kw=None
    cap = await compute_rack_capacity(db=None, rack=rack, assets=[])
    assert cap["kw_max"] is None
    assert cap["kw_pct"] is None
