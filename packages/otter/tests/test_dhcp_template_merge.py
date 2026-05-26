"""Unit tests for DhcpScopeTemplate merge semantics (PR 78).

Pure: no DB, no HTTP. Pins the merge contract that the push, diff,
and bundle paths all rely on:
  - timer fields: scope wins when not None, else template, else
    renderer default (3600 for valid-lifetime).
  - options: merged by (code, space) or (name, space); scope
    overrides template; new entries append; order is template-first
    then scope-only.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from uuid import UUID, uuid4

from dcim.schemas.ipam import DhcpScopeTemplateCreate
from dcim.security.capabilities import CAPABILITY_CATALOG
from dcim.services.dhcp_push import (
    _merge_options,
    _option_key,
    merge_template_into_scope,
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
    valid_lifetime_seconds: int | None = None
    renew_timer_seconds: int | None = None
    rebind_timer_seconds: int | None = None
    preferred_lifetime_seconds: int | None = None
    kea_subnet_id: int | None = None
    template_id: UUID | None = None
    enabled: bool = True


@dataclass
class _Template:
    id: UUID
    fabric_id: UUID
    ip_family: int
    options_json: list = field(default_factory=list)
    valid_lifetime_seconds: int | None = None
    renew_timer_seconds: int | None = None
    rebind_timer_seconds: int | None = None
    preferred_lifetime_seconds: int | None = None


def _v4_scope(**over) -> _Scope:
    base = {
        "id": uuid4(), "dhcp_server_id": uuid4(), "ip_family": 4, "prefix": "10.0.0.0/24",
        "pools_json": [{"first": "10.0.0.10", "last": "10.0.0.250"}],
    }
    base.update(over)
    return _Scope(**base)


def _v4_template(**over) -> _Template:
    base = {"id": uuid4(), "fabric_id": uuid4(), "ip_family": 4}
    base.update(over)
    return _Template(**base)


# ----- timer merge -----

def test_template_supplies_valid_lifetime_when_scope_omits():
    s = _v4_scope(valid_lifetime_seconds=None)
    t = _v4_template(valid_lifetime_seconds=7200)
    eff = merge_template_into_scope(s, t)
    assert eff.valid_lifetime_seconds == 7200


def test_scope_value_wins_over_template_when_both_set():
    s = _v4_scope(valid_lifetime_seconds=3600)
    t = _v4_template(valid_lifetime_seconds=7200)
    eff = merge_template_into_scope(s, t)
    assert eff.valid_lifetime_seconds == 3600


def test_template_supplies_renew_and_rebind_when_scope_omits():
    s = _v4_scope()
    t = _v4_template(renew_timer_seconds=1000, rebind_timer_seconds=2000)
    eff = merge_template_into_scope(s, t)
    assert eff.renew_timer_seconds == 1000
    assert eff.rebind_timer_seconds == 2000


def test_template_supplies_preferred_lifetime_only_for_v6():
    # v6 template + v6 scope: preferred_lifetime inherits.
    s = _Scope(
        id=uuid4(), dhcp_server_id=uuid4(), ip_family=6,
        prefix="2001:db8::/64",
        pools_json=[{"first": "2001:db8::10", "last": "2001:db8::ffff"}],
        preferred_lifetime_seconds=None,
    )
    t = _Template(
        id=uuid4(), fabric_id=uuid4(), ip_family=6,
        preferred_lifetime_seconds=1800,
    )
    eff = merge_template_into_scope(s, t)
    assert eff.preferred_lifetime_seconds == 1800


def test_no_template_returns_identity_passthrough():
    s = _v4_scope(valid_lifetime_seconds=3600)
    eff = merge_template_into_scope(s, None)
    assert eff.valid_lifetime_seconds == 3600
    assert eff.prefix == s.prefix


# ----- option merge -----

def test_option_key_prefers_code_over_name():
    assert _option_key({"code": 3, "name": "routers"}) == ("code", 3, "")
    assert _option_key({"name": "routers"}) == ("name", "routers", "")


def test_merge_options_scope_overrides_template_entry_by_code():
    template = [{"code": 6, "name": "domain-name-servers", "data": "10.0.0.53"}]
    scope = [{"code": 6, "data": "10.0.0.99"}]
    out = _merge_options(template, scope)
    assert len(out) == 1
    assert out[0]["data"] == "10.0.0.99"


def test_merge_options_appends_scope_only_entries_after_template():
    template = [{"code": 6, "data": "10.0.0.53"}]
    scope = [{"code": 3, "data": "10.0.0.1"}]
    out = _merge_options(template, scope)
    assert out == [
        {"code": 6, "data": "10.0.0.53"},
        {"code": 3, "data": "10.0.0.1"},
    ]


def test_merge_options_empty_scope_yields_template_options_verbatim():
    template = [{"code": 6, "data": "10.0.0.53"}]
    out = _merge_options(template, [])
    assert out == template


def test_merge_options_empty_template_yields_scope_options_verbatim():
    scope = [{"code": 3, "data": "10.0.0.1"}]
    out = _merge_options([], scope)
    assert out == scope


def test_merge_options_keys_on_name_when_code_absent():
    template = [{"name": "domain-name", "data": "lab.local"}]
    scope = [{"name": "domain-name", "data": "prod.local"}]
    out = _merge_options(template, scope)
    assert out == [{"name": "domain-name", "data": "prod.local"}]


# ----- renderer integration -----

def test_renderer_uses_effective_valid_lifetime_from_template():
    s = _v4_scope(valid_lifetime_seconds=None)
    t = _v4_template(valid_lifetime_seconds=7200)
    eff = merge_template_into_scope(s, t)
    out = render_kea_subnet4(eff, kea_id=1)
    assert out["valid-lifetime"] == 7200


def test_renderer_falls_back_to_default_when_neither_scope_nor_template_set_lifetime():
    s = _v4_scope(valid_lifetime_seconds=None)
    eff = merge_template_into_scope(s, None)
    out = render_kea_subnet4(eff, kea_id=1)
    assert out["valid-lifetime"] == 3600  # _DEFAULT_VALID_LIFETIME


def test_renderer_merges_template_options_into_kea_option_data():
    s = _v4_scope(options_json=[{"code": 3, "data": "10.0.0.1"}])
    t = _v4_template(options_json=[
        {"code": 6, "data": "10.0.0.53"},
        {"code": 15, "name": "domain-name", "data": "lab.local"},
    ])
    eff = merge_template_into_scope(s, t)
    out = render_kea_subnet4(eff, kea_id=1)
    codes = [o.get("code") for o in out["option-data"]]
    # Template options first, then scope.
    assert codes == [6, 15, 3]


def test_v6_renderer_picks_up_template_preferred_lifetime():
    s = _Scope(
        id=uuid4(), dhcp_server_id=uuid4(), ip_family=6,
        prefix="2001:db8::/64",
        pools_json=[{"first": "2001:db8::10", "last": "2001:db8::ffff"}],
    )
    t = _Template(
        id=uuid4(), fabric_id=uuid4(), ip_family=6,
        preferred_lifetime_seconds=1800,
    )
    eff = merge_template_into_scope(s, t)
    out = render_kea_subnet6(eff, kea_id=1)
    assert out["preferred-lifetime"] == 1800


# ----- schema family validator -----

def test_template_schema_rejects_invalid_ip_family():
    import pytest
    from pydantic import ValidationError
    with pytest.raises(ValidationError):
        DhcpScopeTemplateCreate(
            fabric_id=uuid4(), name="bad", ip_family=5,
        )


# ----- capability registration -----

def test_dhcp_scope_templates_capability_codes_are_registered():
    assert "dhcp-scope-templates" in CAPABILITY_CATALOG["ipam"]
    assert set(CAPABILITY_CATALOG["ipam"]["dhcp-scope-templates"]) == {
        "create", "read", "update", "delete",
    }
