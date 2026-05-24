"""Unit tests for Kea drift detection (PR 75).

Pure: no DB, no HTTP. Pins the diff function (list-order-insensitive,
ignores Kea-added fields) and the Kea response parser
(`_extract_kea_subnet`). The async orchestrator `diff_scope` is
exercised by integration tests against a live Kea instance — out of
scope for this suite.
"""

from __future__ import annotations

from dcim.services.dhcp_push import (
    _diff_subnet_objects,
    _extract_kea_subnet,
)


def _v4_subnet(**over) -> dict:
    base = {
        "id": 1,
        "subnet": "10.0.0.0/24",
        "pools": [{"pool": "10.0.0.10 - 10.0.0.250"}],
        "option-data": [],
        "reservations": [],
        "valid-lifetime": 3600,
    }
    base.update(over)
    return base


# ----- diff function -----

def test_identical_subnets_produce_empty_delta():
    dcim = _v4_subnet()
    kea = _v4_subnet()
    assert _diff_subnet_objects(dcim, kea) == {}


def test_scalar_difference_lands_in_delta_with_both_values():
    dcim = _v4_subnet()  # valid-lifetime defaults to 3600
    kea = _v4_subnet()
    kea["valid-lifetime"] = 7200
    delta = _diff_subnet_objects(dcim, kea)
    assert delta == {"valid-lifetime": {"dcim": 3600, "kea": 7200}}


def test_list_field_order_does_not_matter():
    # DCIM lists two pools in one order; Kea reports them reversed.
    # Drift detection treats `pools` as a multiset — same content,
    # no delta.
    dcim = _v4_subnet(pools=[
        {"pool": "10.0.0.10 - 10.0.0.100"},
        {"pool": "10.0.0.150 - 10.0.0.250"},
    ])
    kea = _v4_subnet(pools=[
        {"pool": "10.0.0.150 - 10.0.0.250"},
        {"pool": "10.0.0.10 - 10.0.0.100"},
    ])
    assert _diff_subnet_objects(dcim, kea) == {}


def test_list_field_added_entry_surfaces_as_delta():
    dcim = _v4_subnet(pools=[{"pool": "10.0.0.10 - 10.0.0.100"}])
    kea = _v4_subnet(pools=[
        {"pool": "10.0.0.10 - 10.0.0.100"},
        {"pool": "10.0.0.150 - 10.0.0.250"},  # operator added directly
    ])
    delta = _diff_subnet_objects(dcim, kea)
    assert "pools" in delta


def test_kea_extra_field_is_ignored():
    # Kea adds fields DCIM doesn't author (timestamps, internal
    # stats, defaulted options). Drift detection only walks DCIM keys
    # so those don't trip a false drift.
    dcim = _v4_subnet()
    kea = _v4_subnet()
    kea["kea-internal-stats"] = {"leases-allocated": 42}
    kea["t1-percent"] = 0.5
    assert _diff_subnet_objects(dcim, kea) == {}


def test_dcim_key_missing_from_kea_surfaces_as_delta():
    dcim = _v4_subnet()
    dcim["renew-timer"] = 1000
    kea = _v4_subnet()
    delta = _diff_subnet_objects(dcim, kea)
    assert delta == {"renew-timer": {"dcim": 1000, "kea": None}}


def test_option_data_drift_on_changed_option_value():
    dcim = _v4_subnet(**{"option-data": [
        {"name": "routers", "data": "10.0.0.1"},
    ]})
    kea = _v4_subnet(**{"option-data": [
        {"name": "routers", "data": "10.0.0.254"},  # operator changed gw
    ]})
    delta = _diff_subnet_objects(dcim, kea)
    assert "option-data" in delta


def test_reservations_drift_on_new_entry():
    dcim = _v4_subnet()
    kea = _v4_subnet(reservations=[
        {"hw-address": "aa:bb:cc:dd:ee:ff", "ip-address": "10.0.0.5"},
    ])
    delta = _diff_subnet_objects(dcim, kea)
    assert "reservations" in delta


# ----- response extractor -----

def test_extract_kea_subnet_pulls_first_entry_from_arguments():
    resp = [{
        "result": 0,
        "text": "Info about IPv4 subnet 10.0.0.0/24 (id 1) returned",
        "arguments": {"subnet4": [_v4_subnet()]},
    }]
    out = _extract_kea_subnet(resp, ip_family=4)
    assert out is not None
    assert out["subnet"] == "10.0.0.0/24"


def test_extract_kea_subnet_returns_none_on_result_3_not_found():
    # Kea result=3 = "no subnet with that id" — caller maps this to
    # the missing_from_kea status.
    resp = [{"result": 3, "text": "subnet 99 not found"}]
    assert _extract_kea_subnet(resp, ip_family=4) is None


def test_extract_kea_subnet_returns_none_on_malformed_envelope():
    assert _extract_kea_subnet([], ip_family=4) is None
    assert _extract_kea_subnet("not a list", ip_family=4) is None
    # Missing arguments key → can't extract.
    assert _extract_kea_subnet([{"result": 0}], ip_family=4) is None
    # Empty subnet4 list inside arguments → nothing to pluck.
    assert _extract_kea_subnet(
        [{"result": 0, "arguments": {"subnet4": []}}], ip_family=4,
    ) is None


def test_extract_kea_subnet_picks_subnet6_key_for_v6_family():
    resp = [{
        "result": 0,
        "arguments": {"subnet6": [{"id": 2, "subnet": "2001:db8::/64"}]},
    }]
    out = _extract_kea_subnet(resp, ip_family=6)
    assert out is not None
    assert out["subnet"] == "2001:db8::/64"
