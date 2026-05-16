"""Unit tests for the verify stage framework + built-in checks."""

from types import SimpleNamespace

import pytest

from dcim.models.regiondeploy import RegionDeploymentEventLevel
from dcim.regiondeploy import verify
from dcim.regiondeploy.verify import (
    Check,
    CheckOutcome,
    Context,
    Result,
    ready,
    register,
    registered,
    run_all,
)


def _ev(stage: str, level: str = "info"):
    """Fake event row — verify checks read `.stage` and `.level.value`."""
    return SimpleNamespace(
        stage=stage,
        level=SimpleNamespace(value=level),
    )


def _ctx(events=None, config=None, nodes=None):
    return Context(
        deployment=SimpleNamespace(id="dep1"),
        nodes=nodes if nodes is not None else [
            SimpleNamespace(
                hostname="c1", mac="02:00:00:00:00:01", role="control_plane",
            ),
        ],
        config=config if config is not None else {
            "pod_cidr_v6": "fd00::/56",
            "svc_cidr_v6": "fd00::/108",
            "lb_pool_v6": "fd00::/112",
            "bgp_peers": [{"address": "fd00::ff", "asn": 65000}],
        },
        events=events or [],
    )


def _full_render_log():
    """One info event per render-emitting stage — the happy path."""
    return [
        _ev("render"),
        _ev("cni"), _ev("cni.bgp"),
        _ev("apps.cert-manager"), _ev("apps.dns_auth"),
        _ev("apps.dns_recursive"), _ev("apps.dhcp"), _ev("apps.collector"),
    ]


# ─── Framework mechanics ───────────────────────────────────────────────


def test_register_is_idempotent_on_key():
    saved = registered()
    try:
        register(Check(key="t.example", label="A", fn=lambda _: Result(True)))
        register(Check(key="t.example", label="B", fn=lambda _: Result(True)))
        live = [c for c in registered() if c.key == "t.example"]
        assert len(live) == 1 and live[0].label == "B"
    finally:
        verify._REGISTRY = list(saved)


def test_check_exception_becomes_failure():
    def explode(_ctx):
        raise RuntimeError("boom")

    saved = registered()
    try:
        register(Check(key="t.boom", label="x", fn=explode))
        outcomes = run_all(_ctx(events=_full_render_log()))
        boom = next(o for o in outcomes if o.key == "t.boom")
        assert not boom.passed
        assert "RuntimeError" in (boom.fix_hint or "")
    finally:
        verify._REGISTRY = list(saved)


def test_ready_treats_pending_as_passing_for_gate():
    # Pending checks (deferred external) don't block the gate.
    assert ready([
        CheckOutcome(key="a", label="A", passed=True),
        CheckOutcome(key="b", label="B", passed=False, pending=True),
    ])
    # Real failures still block.
    assert not ready([
        CheckOutcome(key="a", label="A", passed=True),
        CheckOutcome(key="b", label="B", passed=False),
    ])


# ─── Built-in checks ───────────────────────────────────────────────────


def test_preflight_drift_passes_when_config_still_good():
    outcomes = run_all(_ctx(events=_full_render_log()))
    o = next(o for o in outcomes if o.key == "verify.preflight_no_drift")
    assert o.passed


def test_preflight_drift_fails_when_config_cleared():
    # Drop a required v6 prefix between deploy-start and verify —
    # mirrors an operator clearing a field mid-run from another tab.
    ctx = _ctx(events=_full_render_log(), config={
        "svc_cidr_v6": "fd00::/108",
        "lb_pool_v6": "fd00::/112",
        "bgp_peers": [{"address": "fd00::ff", "asn": 65000}],
    })  # pod_cidr_v6 missing
    o = next(o for o in run_all(ctx) if o.key == "verify.preflight_no_drift")
    assert not o.passed
    assert "drifted" in (o.fix_hint or "")


def test_render_chain_complete_happy_path():
    o = next(
        o for o in run_all(_ctx(events=_full_render_log()))
        if o.key == "verify.render_chain_complete"
    )
    assert o.passed


def test_render_chain_complete_reports_missing_stage():
    # Drop one stage's event — the check should name it in the hint.
    log = [e for e in _full_render_log() if e.stage != "apps.dhcp"]
    o = next(
        o for o in run_all(_ctx(events=log))
        if o.key == "verify.render_chain_complete"
    )
    assert not o.passed
    assert "apps.dhcp" in (o.fix_hint or "")


def test_render_chain_only_counts_info_events():
    # A stage that emitted *only* warn/error events should fail the
    # render-chain check — the contract is "at least one info per
    # render-emitting stage".
    log = [
        _ev("render"),
        _ev("cni", level="warn"),       # only warn — counts as missing
        _ev("cni.bgp"),
        _ev("apps.cert-manager"), _ev("apps.dns_auth"),
        _ev("apps.dns_recursive"), _ev("apps.dhcp"), _ev("apps.collector"),
    ]
    o = next(
        o for o in run_all(_ctx(events=log))
        if o.key == "verify.render_chain_complete"
    )
    assert not o.passed
    assert "cni" in (o.fix_hint or "")


def test_no_error_events_passes_when_log_is_clean():
    o = next(
        o for o in run_all(_ctx(events=_full_render_log()))
        if o.key == "verify.no_error_events"
    )
    assert o.passed


def test_no_error_events_fails_when_log_has_errors():
    log = [*_full_render_log(), _ev("apps.dhcp", level="error")]
    o = next(
        o for o in run_all(_ctx(events=log))
        if o.key == "verify.no_error_events"
    )
    assert not o.passed
    assert "1 error event" in (o.fix_hint or "")


@pytest.mark.parametrize("key", [
    "verify.external_dns_query",
    "verify.external_dhcp_dora",
    "verify.external_collector_checkin",
    "verify.external_hubble_flows",
])
def test_external_checks_are_pending(key):
    # All external/deferred checks should surface as pending — they
    # need the regional-cluster kubeconfig that hasn't landed yet.
    o = next(o for o in run_all(_ctx(events=_full_render_log())) if o.key == key)
    assert o.pending
    assert not o.passed
    assert (o.fix_hint or "").startswith("deferred")


def test_real_event_rows_compatible():
    # The check reads .level.value — match exactly what
    # RegionDeploymentEvent.level (the SQLAlchemy enum) exposes.
    e = SimpleNamespace(stage="render", level=RegionDeploymentEventLevel.info)
    o = next(
        o for o in run_all(_ctx(events=[e]))
        if o.key == "verify.no_error_events"
    )
    assert o.passed
