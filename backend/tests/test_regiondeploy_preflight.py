"""Unit tests for the region-deploy preflight framework.

Coverage targets the framework mechanics (registry idempotency,
exception-as-failure, external-skip) plus the built-in pure checks.
External-system checks (BMC reachable, BGP up) get their own tests
when their modules land — covering them here would require stubbing
clients that don't exist yet.
"""

from types import SimpleNamespace

import pytest

from dcim.regiondeploy import preflight
from dcim.regiondeploy.preflight import (
    Check,
    CheckOutcome,
    Context,
    Result,
    ready,
    register,
    registered,
    run_all,
)


def _node(hostname="control-1", mac="02:00:00:00:00:01", role="control_plane"):
    return SimpleNamespace(hostname=hostname, mac=mac, role=role)


def _ctx(**overrides):
    base = {
        "nodes": [_node()],
        "config": {
            "pod_cidr_v6": "fd00::/56",
            "svc_cidr_v6": "fd00::/108",
            "lb_pool_v6": "fd00::/112",
            "bgp_peers": [{"address": "fd00::ffff", "asn": 65000}],
        },
    }
    base.update(overrides)
    return Context(**base)


# ─── Framework mechanics ───────────────────────────────────────────────


def test_register_is_idempotent_on_key():
    # Tests can register a same-keyed check and replace the previous;
    # otherwise teardown leakage would bleed across tests.
    saved = registered()
    try:
        register(Check(key="t.example", label="A", fn=lambda _: Result(True)))
        register(Check(key="t.example", label="B", fn=lambda _: Result(True)))
        live = [c for c in registered() if c.key == "t.example"]
        assert len(live) == 1
        assert live[0].label == "B"
    finally:
        # Restore the registry so other tests in the suite see only
        # the built-ins.
        preflight._REGISTRY = list(saved)


def test_check_exception_becomes_failure_not_crash():
    def explode(_ctx):
        raise RuntimeError("boom")

    saved = registered()
    try:
        register(Check(key="t.boom", label="explodes", fn=explode))
        outcomes = run_all(_ctx())
        boom = next(o for o in outcomes if o.key == "t.boom")
        assert boom.passed is False
        assert "RuntimeError" in (boom.fix_hint or "")
        assert "boom" in (boom.fix_hint or "")
    finally:
        preflight._REGISTRY = list(saved)


def test_external_checks_skipped_when_requested():
    saved = registered()
    try:
        register(Check(
            key="t.network", label="x", fn=lambda _: Result(True), external=True,
        ))
        # full run includes it
        all_keys = [o.key for o in run_all(_ctx(), include_external=True)]
        assert "t.network" in all_keys
        # fast run excludes it
        fast_keys = [o.key for o in run_all(_ctx(), include_external=False)]
        assert "t.network" not in fast_keys
    finally:
        preflight._REGISTRY = list(saved)


def test_ready_is_true_only_when_every_outcome_passed():
    assert ready([
        CheckOutcome(key="a", label="A", passed=True),
        CheckOutcome(key="b", label="B", passed=True),
    ])
    assert not ready([
        CheckOutcome(key="a", label="A", passed=True),
        CheckOutcome(key="b", label="B", passed=False),
    ])
    # Empty iterable: vacuously ready. Defensive but the API won't
    # ever hit this — there's always at least one built-in check.
    assert ready([])


# ─── Built-in pure checks ──────────────────────────────────────────────


def test_distinct_macs_passes_for_unique():
    outcomes = run_all(_ctx(nodes=[
        _node(hostname="c1", mac="02:00:00:00:00:01"),
        _node(hostname="w1", mac="02:00:00:00:00:02"),
    ]))
    o = next(o for o in outcomes if o.key == "nodes.distinct_macs")
    assert o.passed


def test_distinct_macs_fails_with_collision_hint():
    outcomes = run_all(_ctx(nodes=[
        _node(hostname="c1", mac="02:00:00:00:00:01"),
        _node(hostname="w1", mac="02:00:00:00:00:01"),
    ]))
    o = next(o for o in outcomes if o.key == "nodes.distinct_macs")
    assert not o.passed
    assert "c1" in (o.fix_hint or "") and "w1" in (o.fix_hint or "")


def test_distinct_hostnames():
    outcomes = run_all(_ctx(nodes=[_node(hostname="c1"), _node(hostname="c1")]))
    o = next(o for o in outcomes if o.key == "nodes.distinct_hostnames")
    assert not o.passed and "c1" in (o.fix_hint or "")


@pytest.mark.parametrize(("roles", "expect_pass"), [
    (["control_plane"], True),
    (["control_plane", "worker"], True),
    (["worker"], False),
    ([], False),
])
def test_at_least_one_control_plane(roles, expect_pass):
    nodes = [_node(hostname=f"n{i}", mac=f"02:00:00:00:00:{i:02x}", role=r)
             for i, r in enumerate(roles, start=1)]
    outcomes = run_all(_ctx(nodes=nodes))
    o = next(o for o in outcomes if o.key == "nodes.has_control_plane")
    assert o.passed is expect_pass


@pytest.mark.parametrize(("key", "config_key"), [
    ("site.has_v6_pod_prefix", "pod_cidr_v6"),
    ("site.has_v6_svc_prefix", "svc_cidr_v6"),
    ("site.has_v6_lb_pool", "lb_pool_v6"),
])
def test_v6_prefix_checks(key, config_key):
    # Empty / unset config key fails with a hint that names the
    # config field (so an operator knows which form field to fill).
    ctx = _ctx()
    cfg = dict(ctx.config or {})
    cfg.pop(config_key)
    ctx = Context(nodes=ctx.nodes, config=cfg)
    o = next(o for o in run_all(ctx) if o.key == key)
    assert not o.passed
    assert config_key in (o.fix_hint or "")


def test_bgp_peers_configured():
    ctx = _ctx()
    cfg = dict(ctx.config or {})
    cfg["bgp_peers"] = []
    ctx = Context(nodes=ctx.nodes, config=cfg)
    o = next(o for o in run_all(ctx) if o.key == "bgp.peers_configured")
    assert not o.passed


def test_role_enum_coerced():
    # node.role may be a SQLAlchemy enum instance with .value, not a
    # raw string; the role check must handle both.
    class _Role:
        value = "control_plane"

    outcomes = run_all(_ctx(nodes=[
        _node(hostname="c1", mac="02:00:00:00:00:01"),
    ]))
    o = next(o for o in outcomes if o.key == "nodes.has_control_plane")
    assert o.passed

    # Same when the node's role is an enum:
    enum_node = SimpleNamespace(hostname="c2", mac="02:00:00:00:00:02", role=_Role())
    outcomes = run_all(_ctx(nodes=[enum_node]))
    o = next(o for o in outcomes if o.key == "nodes.has_control_plane")
    assert o.passed
