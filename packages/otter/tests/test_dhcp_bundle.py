"""Unit tests for the Kea config bundle renderer (PR 76).

Pure: no DB, no HTTP. Pins the subnet array assembly (enabled
scopes only, kea_subnet_id preserved when set, bundle-local id
allocation when not), the base-config overlay (operator state
passes through; DCIM-authored subnet arrays replace whatever was
there), and etag stability (same inputs → same digest, mutation
→ new digest).
"""

from __future__ import annotations

from dataclasses import dataclass, field
from uuid import UUID, uuid4

from dcim.services.dhcp_bundle import render_kea_bundle


@dataclass
class _Server:
    id: UUID
    base_config: dict = field(default_factory=dict)


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
    kea_subnet_id: int | None = None
    enabled: bool = True


def _server(**over) -> _Server:
    return _Server(id=uuid4(), **over)


def _v4_scope(server_id, **over) -> _Scope:
    base = {
        "id": uuid4(), "dhcp_server_id": server_id, "ip_family": 4,
        "prefix": "10.0.0.0/24",
        "pools_json": [{"first": "10.0.0.10", "last": "10.0.0.250"}],
    }
    base.update(over)
    return _Scope(**base)


def _v6_scope(server_id, **over) -> _Scope:
    base = {
        "id": uuid4(), "dhcp_server_id": server_id, "ip_family": 6,
        "prefix": "2001:db8::/64",
        "pools_json": [{"first": "2001:db8::10", "last": "2001:db8::ffff"}],
        "preferred_lifetime_seconds": 1800,
    }
    base.update(over)
    return _Scope(**base)


# ----- subnet array assembly -----

def test_empty_server_with_no_scopes_emits_empty_subnet_arrays():
    s = _server()
    b = render_kea_bundle(s, [])
    assert b.dhcp4 == {"subnet4": []}
    assert b.dhcp6 == {"subnet6": []}
    assert b.ctrl_agent == {}


def test_v4_and_v6_scopes_land_in_correct_array():
    srv = _server()
    sc4 = _v4_scope(srv.id, kea_subnet_id=1)
    sc6 = _v6_scope(srv.id, kea_subnet_id=1)
    b = render_kea_bundle(srv, [sc4, sc6])
    assert len(b.dhcp4["subnet4"]) == 1
    assert b.dhcp4["subnet4"][0]["subnet"] == "10.0.0.0/24"
    assert b.dhcp4["subnet4"][0]["id"] == 1
    assert len(b.dhcp6["subnet6"]) == 1
    assert b.dhcp6["subnet6"][0]["subnet"] == "2001:db8::/64"
    assert b.dhcp6["subnet6"][0]["id"] == 1  # separate id-space per family


def test_disabled_scope_skipped_from_bundle():
    srv = _server()
    sc_ok = _v4_scope(srv.id, kea_subnet_id=1, prefix="10.0.0.0/24")
    sc_off = _v4_scope(srv.id, kea_subnet_id=2, prefix="10.0.1.0/24", enabled=False)
    b = render_kea_bundle(srv, [sc_ok, sc_off])
    assert len(b.dhcp4["subnet4"]) == 1
    assert b.dhcp4["subnet4"][0]["subnet"] == "10.0.0.0/24"


def test_pinned_kea_subnet_ids_are_preserved():
    srv = _server()
    # Two scopes already pushed with ids 7 and 9; a third is unpushed.
    a = _v4_scope(srv.id, kea_subnet_id=7, prefix="10.0.0.0/24")
    b = _v4_scope(srv.id, kea_subnet_id=9, prefix="10.0.1.0/24")
    c = _v4_scope(srv.id, kea_subnet_id=None, prefix="10.0.2.0/24")
    bundle = render_kea_bundle(srv, [a, b, c])
    ids = sorted(s["id"] for s in bundle.dhcp4["subnet4"])
    # Pinned ids stay; the unpushed one fills the lowest unused slot.
    assert 7 in ids and 9 in ids
    # Bundle-local allocation must not collide with the pinned set
    # but otherwise picks the smallest free positive int.
    assigned = next(i for i in ids if i not in (7, 9))
    assert assigned == 1


def test_v4_and_v6_id_spaces_are_independent():
    srv = _server()
    a = _v4_scope(srv.id, kea_subnet_id=1)
    b = _v6_scope(srv.id, kea_subnet_id=1)
    bundle = render_kea_bundle(srv, [a, b])
    # Both can use id=1 because they live in separate dhcp4/dhcp6
    # configs.
    assert bundle.dhcp4["subnet4"][0]["id"] == 1
    assert bundle.dhcp6["subnet6"][0]["id"] == 1


# ----- base config overlay -----

def test_operator_authored_dhcp4_fields_pass_through_verbatim():
    base = {
        "dhcp4": {
            "interfaces-config": {"interfaces": ["eth0"]},
            "lease-database": {"type": "memfile"},
            "loggers": [{"name": "kea-dhcp4", "severity": "INFO"}],
            "hooks-libraries": [
                {"library": "/usr/lib/kea/hooks/libdhcp_subnet_cmds.so"},
            ],
        },
    }
    srv = _server(base_config=base)
    b = render_kea_bundle(srv, [_v4_scope(srv.id, kea_subnet_id=1)])
    # Subnet array got installed.
    assert b.dhcp4["subnet4"][0]["subnet"] == "10.0.0.0/24"
    # Everything else stayed.
    assert b.dhcp4["interfaces-config"] == {"interfaces": ["eth0"]}
    assert b.dhcp4["lease-database"] == {"type": "memfile"}
    assert b.dhcp4["hooks-libraries"][0]["library"].endswith("libdhcp_subnet_cmds.so")


def test_dcim_subnet_array_overwrites_operator_subnet_array():
    # If the operator's base accidentally carries a subnet4 entry,
    # DCIM is authoritative — replace, don't merge.
    base = {"dhcp4": {"subnet4": [{"id": 99, "subnet": "10.99.0.0/24"}]}}
    srv = _server(base_config=base)
    sc = _v4_scope(srv.id, kea_subnet_id=1, prefix="10.0.0.0/24")
    b = render_kea_bundle(srv, [sc])
    assert len(b.dhcp4["subnet4"]) == 1
    assert b.dhcp4["subnet4"][0]["subnet"] == "10.0.0.0/24"  # DCIM's, not operator's


def test_ctrl_agent_passes_through_untouched():
    base = {"ctrl-agent": {"http-port": 8000, "control-sockets": {"dhcp4": {}}}}
    srv = _server(base_config=base)
    b = render_kea_bundle(srv, [])
    assert b.ctrl_agent == {"http-port": 8000, "control-sockets": {"dhcp4": {}}}


def test_missing_base_sections_default_to_empty_dicts():
    srv = _server(base_config={"dhcp4": {"loggers": []}})
    # No ctrl-agent or dhcp6 in base — renderer fills in empty dicts
    # (plus the empty subnet6 array on dhcp6).
    b = render_kea_bundle(srv, [])
    assert b.ctrl_agent == {}
    assert b.dhcp6 == {"subnet6": []}


# ----- etag -----

def test_etag_is_stable_across_identical_renders():
    srv = _server(base_config={"dhcp4": {"interfaces-config": {"interfaces": ["eth0"]}}})
    sc = _v4_scope(srv.id, kea_subnet_id=1)
    a = render_kea_bundle(srv, [sc])
    b = render_kea_bundle(srv, [sc])
    assert a.etag == b.etag
    assert len(a.etag) == 64  # sha256 hex digest


def test_etag_changes_when_scope_added():
    srv = _server()
    sc_a = _v4_scope(srv.id, kea_subnet_id=1)
    sc_b = _v4_scope(srv.id, kea_subnet_id=2, prefix="10.0.1.0/24")
    one = render_kea_bundle(srv, [sc_a])
    two = render_kea_bundle(srv, [sc_a, sc_b])
    assert one.etag != two.etag


def test_etag_changes_when_base_config_mutates():
    sc = _v4_scope(uuid4(), kea_subnet_id=1)
    plain = _server()
    plain_b = render_kea_bundle(plain, [sc])
    with_base = _server(base_config={"dhcp4": {"loggers": [{"name": "kea-dhcp4"}]}})
    sc.dhcp_server_id = with_base.id
    with_base_b = render_kea_bundle(with_base, [sc])
    assert plain_b.etag != with_base_b.etag


def test_etag_invariant_under_scope_order():
    # render_kea_bundle iterates the input list in order, but the
    # subnet4[] list lands in the bundle in that same order. Etag
    # should reflect that: same set, different order → different
    # etag. (List order matters in Kea config; we don't sort.)
    srv = _server()
    sc_a = _v4_scope(srv.id, kea_subnet_id=1, prefix="10.0.0.0/24")
    sc_b = _v4_scope(srv.id, kea_subnet_id=2, prefix="10.0.1.0/24")
    forward = render_kea_bundle(srv, [sc_a, sc_b])
    reverse = render_kea_bundle(srv, [sc_b, sc_a])
    # Both ids 1 and 2 are pinned; the renderer preserves input
    # ordering, so the resulting subnet4 list ordering differs and
    # the etag must reflect that.
    assert forward.etag != reverse.etag


def test_bundle_server_id_carries_string_uuid():
    srv = _server()
    b = render_kea_bundle(srv, [])
    assert b.server_id == str(srv.id)
