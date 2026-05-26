"""Unit tests for DHCP scope schemas + capability registration (PR 73).

Pure: no DB, no async. Pins the schema shape, the family-discriminator
validator, and the capability code list. Integration tests against a
real Postgres + Kea CA live elsewhere (test_kea.py is for the lease
sync direction; scope push is a future PR).
"""

from __future__ import annotations

import pytest
from pydantic import ValidationError

from dcim.schemas.ipam import (
    DhcpOption,
    DhcpPdPool,
    DhcpPool,
    DhcpReservation,
    DhcpScopeCreate,
    DhcpScopeUpdate,
)
from dcim.security.capabilities import CAPABILITY_CATALOG

SERVER_ID = "00000000-0000-0000-0000-000000000001"


def _v4_payload(**over) -> dict:
    base = {
        "dhcp_server_id": SERVER_ID,
        "name": "lab-v4",
        "ip_family": 4,
        "prefix": "10.0.0.0/24",
        "pools": [{"first": "10.0.0.10", "last": "10.0.0.250"}],
    }
    base.update(over)
    return base


def _v6_payload(**over) -> dict:
    base = {
        "dhcp_server_id": SERVER_ID,
        "name": "lab-v6",
        "ip_family": 6,
        "prefix": "2001:db8::/64",
        "pools": [{"first": "2001:db8::10", "last": "2001:db8::ffff"}],
        "preferred_lifetime_seconds": 1800,
    }
    base.update(over)
    return base


# ----- family discriminator -----

def test_ip_family_4_passes_validation():
    s = DhcpScopeCreate(**_v4_payload())
    assert s.ip_family == 4


def test_ip_family_6_passes_validation():
    s = DhcpScopeCreate(**_v6_payload())
    assert s.ip_family == 6


def test_ip_family_5_rejected():
    # The DB CHECK constraint enforces this too; we reject at the
    # schema for an actionable error message.
    with pytest.raises(ValidationError):
        DhcpScopeCreate(**_v4_payload(ip_family=5))


# ----- pools / pd_pools / options / reservations -----

def test_v4_scope_omits_pd_pools_by_default():
    s = DhcpScopeCreate(**_v4_payload())
    assert s.pd_pools is None


def test_v6_scope_can_carry_pd_pools():
    s = DhcpScopeCreate(**_v6_payload(
        pd_pools=[{"prefix": "2001:db8:0:100::/56", "delegated_len": 64}],
    ))
    assert s.pd_pools == [DhcpPdPool(prefix="2001:db8:0:100::/56", delegated_len=64)]


def test_option_data_round_trips_with_explicit_code():
    s = DhcpScopeCreate(**_v4_payload(
        options=[{"code": 3, "name": "routers", "data": "10.0.0.1"}],
    ))
    assert s.options[0].code == 3
    assert s.options[0].data == "10.0.0.1"


def test_v4_reservation_uses_mac_v6_uses_duid():
    # Schema accepts both fields on the type; the API layer rejects
    # the wrong identifier for the family (see _validate_scope_family).
    s4 = DhcpScopeCreate(**_v4_payload(
        reservations=[{"mac": "aa:bb:cc:dd:ee:ff", "ip": "10.0.0.5"}],
    ))
    assert s4.reservations[0].mac == "aa:bb:cc:dd:ee:ff"
    s6 = DhcpScopeCreate(**_v6_payload(
        reservations=[{"duid": "00:01:00:01:...", "ip": "2001:db8::5"}],
    ))
    assert s6.reservations[0].duid == "00:01:00:01:..."


# ----- update path -----

def test_update_exclude_unset_keeps_immutable_fields_off_the_wire():
    # PR 73 — the API enforces ip_family/prefix/dhcp_server_id are
    # immutable. The schema shouldn't even surface those keys on
    # DhcpScopeUpdate so a confused client can't try.
    keys = set(DhcpScopeUpdate.model_fields.keys())
    assert "ip_family" not in keys
    assert "prefix" not in keys
    assert "dhcp_server_id" not in keys
    # Sanity: mutable knobs are there.
    assert "name" in keys
    assert "pools" in keys
    assert "enabled" in keys


def test_update_with_empty_diff_round_trips():
    u = DhcpScopeUpdate()
    assert u.model_dump(exclude_unset=True) == {}


def test_update_with_pools_only_emits_pools_only():
    u = DhcpScopeUpdate(pools=[DhcpPool(first="10.0.0.20", last="10.0.0.30")])
    diff = u.model_dump(exclude_unset=True)
    assert list(diff.keys()) == ["pools"]
    assert diff["pools"] == [{"first": "10.0.0.20", "last": "10.0.0.30"}]


# ----- capability registration -----

def test_dhcp_scopes_capability_codes_are_registered():
    # Catches the (easy-to-forget) capabilities.py update — without
    # this row in CAPABILITY_CATALOG, require_capability("ipam:dhcp-scopes:create")
    # raises at app boot time. `push` was added in PR 74; tests pin
    # the CRUD set and the push action explicitly.
    assert "dhcp-scopes" in CAPABILITY_CATALOG["ipam"]
    actions = set(CAPABILITY_CATALOG["ipam"]["dhcp-scopes"])
    assert {"create", "read", "update", "delete"} <= actions
    assert "push" in actions


# ----- option/pool/reservation type round-trip -----

def test_dhcp_pool_round_trips():
    p = DhcpPool(first="10.0.0.10", last="10.0.0.250")
    assert p.model_dump() == {"first": "10.0.0.10", "last": "10.0.0.250"}


def test_dhcp_option_drops_none_fields_on_dump():
    o = DhcpOption(code=3, data="10.0.0.1")
    assert o.model_dump(exclude_none=True) == {"code": 3, "data": "10.0.0.1"}


def test_dhcp_reservation_drops_unused_identifier():
    # v4 reservation: only `mac` is set, `duid` stays None and drops
    # on exclude_none — so the JSON column doesn't carry junk.
    r = DhcpReservation(mac="aa:bb:cc:dd:ee:ff", ip="10.0.0.5")
    dumped = r.model_dump(exclude_none=True)
    assert dumped == {"mac": "aa:bb:cc:dd:ee:ff", "ip": "10.0.0.5"}
    assert "duid" not in dumped
