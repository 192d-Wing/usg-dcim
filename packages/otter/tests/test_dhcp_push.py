"""Unit tests for the Kea push renderer + response interpreter (PR 74).

Pure: no DB, no HTTP. Pin the projection from DhcpScope rows to the
exact dict shape Kea expects in subnet4/6 objects, plus the Kea
response → status interpretation that drives DhcpServer.last_push_*.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from uuid import UUID, uuid4

from dcim.services.dhcp_push import (
    _interpret_kea_response,
    render_kea_subnet4,
    render_kea_subnet6,
)


@dataclass
class _Scope:
    id: UUID
    dhcp_server_id: UUID
    ip_family: int
    prefix: str
    pools_json: list = field(default_factory=list)
    pd_pools_json: list | None = None
    options_json: list = field(default_factory=list)
    reservations_json: list = field(default_factory=list)
    valid_lifetime_seconds: int = 3600
    renew_timer_seconds: int | None = None
    rebind_timer_seconds: int | None = None
    preferred_lifetime_seconds: int | None = None


def _v4_scope(**over) -> _Scope:
    base = dict(
        id=uuid4(), dhcp_server_id=uuid4(), ip_family=4, prefix="10.0.0.0/24",
        pools_json=[{"first": "10.0.0.10", "last": "10.0.0.250"}],
    )
    base.update(over)
    return _Scope(**base)


def _v6_scope(**over) -> _Scope:
    base = dict(
        id=uuid4(), dhcp_server_id=uuid4(), ip_family=6, prefix="2001:db8::/64",
        pools_json=[{"first": "2001:db8::10", "last": "2001:db8::ffff"}],
        preferred_lifetime_seconds=1800,
    )
    base.update(over)
    return _Scope(**base)


# ----- v4 renderer -----

def test_v4_minimal_scope_emits_kea_subnet4_shape():
    out = render_kea_subnet4(_v4_scope(), kea_id=7)
    assert out == {
        "id": 7,
        "subnet": "10.0.0.0/24",
        "pools": [{"pool": "10.0.0.10 - 10.0.0.250"}],
        "option-data": [],
        "reservations": [],
        "valid-lifetime": 3600,
    }


def test_v4_renders_renew_and_rebind_timers_when_set():
    s = _v4_scope(renew_timer_seconds=1000, rebind_timer_seconds=2000)
    out = render_kea_subnet4(s, kea_id=1)
    assert out["renew-timer"] == 1000
    assert out["rebind-timer"] == 2000


def test_v4_option_data_emits_kea_keys():
    s = _v4_scope(options_json=[
        {"code": 3, "name": "routers", "data": "10.0.0.1"},
        {"name": "domain-name-servers", "data": "10.0.0.53"},
    ])
    out = render_kea_subnet4(s, kea_id=1)
    assert out["option-data"] == [
        {"code": 3, "name": "routers", "data": "10.0.0.1"},
        {"name": "domain-name-servers", "data": "10.0.0.53"},
    ]


def test_v4_reservation_emits_hw_address_and_ip_address():
    s = _v4_scope(reservations_json=[
        {"mac": "aa:bb:cc:dd:ee:ff", "ip": "10.0.0.5", "hostname": "server-1"},
    ])
    out = render_kea_subnet4(s, kea_id=1)
    assert out["reservations"] == [{
        "hw-address": "aa:bb:cc:dd:ee:ff",
        "ip-address": "10.0.0.5",
        "hostname": "server-1",
    }]


def test_v4_skips_malformed_pool_entries():
    # Missing `last` → drop, don't crash.
    s = _v4_scope(pools_json=[
        {"first": "10.0.0.10", "last": "10.0.0.250"},
        {"first": "10.0.0.30"},
    ])
    out = render_kea_subnet4(s, kea_id=1)
    assert out["pools"] == [{"pool": "10.0.0.10 - 10.0.0.250"}]


# ----- v6 renderer -----

def test_v6_minimal_scope_includes_preferred_lifetime():
    out = render_kea_subnet6(_v6_scope(), kea_id=2)
    assert out["id"] == 2
    assert out["subnet"] == "2001:db8::/64"
    assert out["pools"] == [{"pool": "2001:db8::10 - 2001:db8::ffff"}]
    assert out["valid-lifetime"] == 3600
    assert out["preferred-lifetime"] == 1800


def test_v6_pd_pools_split_prefix_and_delegated_len():
    s = _v6_scope(pd_pools_json=[
        {"prefix": "2001:db8:0:100::/56", "delegated_len": 64},
    ])
    out = render_kea_subnet6(s, kea_id=1)
    assert out["pd-pools"] == [{
        "prefix": "2001:db8:0:100::",
        "prefix-len": 56,
        "delegated-len": 64,
    }]


def test_v6_no_pd_pools_omits_pd_pools_key():
    out = render_kea_subnet6(_v6_scope(), kea_id=1)
    assert "pd-pools" not in out


def test_v6_reservation_wraps_ip_in_list_with_duid():
    # Kea v6 takes ip-addresses as a LIST per reservation.
    s = _v6_scope(reservations_json=[
        {"duid": "00:01:00:01:...", "ip": "2001:db8::5", "hostname": "node-1"},
    ])
    out = render_kea_subnet6(s, kea_id=1)
    assert out["reservations"] == [{
        "duid": "00:01:00:01:...",
        "ip-addresses": ["2001:db8::5"],
        "hostname": "node-1",
    }]


def test_v6_skips_pd_pool_without_delegated_len():
    s = _v6_scope(pd_pools_json=[
        {"prefix": "2001:db8:0:100::/56"},  # missing delegated_len
    ])
    out = render_kea_subnet6(s, kea_id=1)
    assert "pd-pools" not in out  # only one entry, dropped, list empty


# ----- response interpreter -----

def test_kea_response_result_0_is_ok():
    status, err = _interpret_kea_response([{"result": 0, "text": "ok"}])
    assert status == "ok"
    assert err is None


def test_kea_response_result_3_treated_as_ok():
    # Result 3 = "not found / empty" — fine on delete (already gone)
    # and on list calls with no rows.
    status, err = _interpret_kea_response([{"result": 3, "text": "no subnets"}])
    assert status == "ok"
    assert err is None


def test_kea_response_result_1_is_error_with_text():
    status, err = _interpret_kea_response([{"result": 1, "text": "subnet exists"}])
    assert status == "error"
    assert "subnet exists" in err


def test_kea_response_result_2_is_unsupported():
    # Subnet_cmds hook not loaded → result 2. Distinct status so the
    # UI can hint at "load the hook" vs a generic Kea error.
    status, err = _interpret_kea_response([
        {"result": 2, "text": "command 'subnet4-add' not supported"},
    ])
    assert status == "unsupported"
    assert "unsupported" in err


def test_kea_response_multi_service_picks_first_error():
    status, err = _interpret_kea_response([
        {"result": 0, "text": "ok"},
        {"result": 1, "text": "second failed"},
    ])
    assert status == "error"
    assert "second failed" in err


def test_kea_response_malformed_is_error():
    status, err = _interpret_kea_response("not a list")
    assert status == "error"
    assert err is not None
