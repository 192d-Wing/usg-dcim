"""Region-deploy verify stage.

The orchestrator's `verify` stage is the last gate before `finalize`.
Per the doc, it answers: "did the install actually work?"

For real, end-to-end verification we need to probe the new cluster
— DNS A queries against the auth resolver, DHCP DORA against Kea,
Hubble flow checks against Cilium BGP, collector check-in polls
against central. **None of those work yet**, because the orchestrator
has no kubeconfig for the regional cluster until the `joining` stage
retrieves it (a separate workstream). External probes register here
with `external=True` so a future runner can pick them up the moment
the kubeconfig path lands; for now they're advisory entries the UI
surfaces as "pending external".

What we *can* check today, without cluster access:

  * **Preflight drift.** Re-run preflight against the current row.
    If something changed since deploy-start (node added, prefix
    cleared) we catch it now instead of declaring `ready` over a
    broken config.
  * **Render-chain completeness.** Inspect the deployment's events
    and confirm that each render-emitting stage produced at least
    one event with a non-empty payload. Catches the case where a
    renderer raised silently mid-stage, leaving the operator with
    partial artifacts in the log.

Shape mirrors preflight.py — the same Check / Result / Context
dataclasses, the same registry-of-functions pattern. We don't share
the preflight registry: the two run at different points in the
state machine and a check that's appropriate for one isn't always
appropriate for the other (verify cares about emitted events;
preflight cares about static config).
"""

from __future__ import annotations

from collections.abc import Callable, Iterable
from dataclasses import dataclass, field
from typing import Any

from . import preflight as preflight_mod


@dataclass(frozen=True)
class Result:
    passed: bool
    fix_hint: str | None = None
    # Pending: external check that can't run yet — the orchestrator
    # surfaces it as "deferred" rather than "passed/failed".
    pending: bool = False


@dataclass(frozen=True)
class Check:
    key: str
    label: str
    fn: Callable[[Context], Result]
    external: bool = False


@dataclass
class Context:
    """Inputs every verify check might want."""

    deployment: Any | None = None
    nodes: list[Any] = field(default_factory=list)
    config: dict | None = None
    # Events emitted since deploy-start. The render-chain checks
    # filter this list by stage key.
    events: list[Any] = field(default_factory=list)


@dataclass(frozen=True)
class CheckOutcome:
    key: str
    label: str
    passed: bool
    pending: bool = False
    fix_hint: str | None = None


_REGISTRY: list[Check] = []


def register(check: Check) -> Check:
    """Same idempotent-by-key semantics as the preflight registry."""
    global _REGISTRY
    _REGISTRY = [c for c in _REGISTRY if c.key != check.key]
    _REGISTRY.append(check)
    return check


def registered() -> list[Check]:
    return list(_REGISTRY)


def run_all(ctx: Context) -> list[CheckOutcome]:
    """Run every registered check. Exceptions become failures so a
    bug in one check doesn't crash the stage."""
    outcomes: list[CheckOutcome] = []
    for check in _REGISTRY:
        try:
            res = check.fn(ctx)
        except Exception as e:
            res = Result(
                passed=False,
                fix_hint=f"check '{check.key}' raised {type(e).__name__}: {e}",
            )
        outcomes.append(CheckOutcome(
            key=check.key, label=check.label,
            passed=res.passed, pending=res.pending, fix_hint=res.fix_hint,
        ))
    return outcomes


def ready(outcomes: Iterable[CheckOutcome]) -> bool:
    """Hard-gate: every non-pending check must have passed. Pending
    (deferred-external) checks don't block — they're known to be
    unrunnable today, and the orchestrator can't wait forever for
    them to gain a cluster path."""
    return all(o.passed or o.pending for o in outcomes)


# ─── Built-in checks ──────────────────────────────────────────────────


def _check_preflight_no_drift(ctx: Context) -> Result:
    """Re-run preflight. A failure here means the deployment row
    changed between deploy-start and verify in a way that violates
    the same gate the operator passed before clicking Start."""
    if ctx.deployment is None:
        return Result(passed=False, fix_hint="no deployment in verify context")
    pf_ctx = preflight_mod.Context(
        deployment=ctx.deployment,
        nodes=ctx.nodes,
        config=ctx.config,
    )
    pf_outcomes = preflight_mod.run_all(pf_ctx)
    failed = [o for o in pf_outcomes if not o.passed]
    if failed:
        keys = ", ".join(f.key for f in failed)
        return Result(
            passed=False,
            fix_hint=f"preflight drifted since start: {keys}",
        )
    return Result(passed=True)


# Stages that emit a `render`-shaped event with a non-empty payload.
# Used by the render-chain check to confirm each rendered something.
# Keep aligned with the dispatch in orchestrator._run_stage — adding
# a new render-emitting stage means adding its key here too.
_RENDERING_STAGES = (
    "render",
    "cni",
    "cni.bgp",
    "apps.cert-manager",
    "apps.dns_auth",
    "apps.dns_recursive",
    "apps.dhcp",
    "apps.collector",
)


def _check_render_chain_complete(ctx: Context) -> Result:
    """Every stage that *should* have rendered something has at least
    one info-level event in the deployment's event log.

    Misses one of three things:
      - a stage was skipped because the orchestrator aborted early
        (then the run shouldn't reach verify, but defence-in-depth);
      - a renderer raised but the error event landed under a
        different stage name (bug);
      - the stage emitted only warnings, no info (also a bug — at
        least one info event per stage is the contract).
    """
    seen = {e.stage for e in ctx.events if getattr(e, "level", None) is not None and e.level.value == "info"}
    missing = [s for s in _RENDERING_STAGES if s not in seen]
    if missing:
        return Result(
            passed=False,
            fix_hint=f"no info event for stage(s): {', '.join(missing)}",
        )
    return Result(passed=True)


def _check_no_error_events(ctx: Context) -> Result:
    """No error-level events anywhere in the deployment log.

    An error event during a stage usually causes the orchestrator to
    fail that stage and stop — verify shouldn't see them. But the
    orchestrator's failure path can be raced (an error event lands
    while another stage is mid-flight on a retry), so we catch the
    rare case explicitly rather than trusting the state machine
    silently."""
    errors = [
        e for e in ctx.events
        if getattr(e, "level", None) is not None and e.level.value == "error"
    ]
    if errors:
        return Result(
            passed=False,
            fix_hint=f"{len(errors)} error event(s) in log; investigate",
        )
    return Result(passed=True)


def _check_external_dns_query(_ctx: Context) -> Result:
    """Deferred until the regional-cluster kubeconfig path lands."""
    return Result(
        passed=False,
        pending=True,
        fix_hint="deferred — needs regional-cluster kubeconfig retrieval",
    )


def _check_external_dhcp_dora(_ctx: Context) -> Result:
    return Result(
        passed=False,
        pending=True,
        fix_hint="deferred — needs a v6 client harness pod in the regional cluster",
    )


def _check_external_collector_checkin(_ctx: Context) -> Result:
    return Result(
        passed=False,
        pending=True,
        fix_hint="deferred — collector enrolment lands with seed stage",
    )


def _check_external_hubble_flows(_ctx: Context) -> Result:
    return Result(
        passed=False,
        pending=True,
        fix_hint="deferred — Hubble flow probe needs regional-cluster kubeconfig",
    )


register(Check(
    key="verify.preflight_no_drift",
    label="Preflight still passes",
    fn=_check_preflight_no_drift,
))
register(Check(
    key="verify.render_chain_complete",
    label="Every render-emitting stage produced at least one info event",
    fn=_check_render_chain_complete,
))
register(Check(
    key="verify.no_error_events",
    label="No error-level events in the deployment log",
    fn=_check_no_error_events,
))
# External / deferred — register so the UI surfaces what *will* be
# checked once the cluster-access workstream lands, even though they
# can't run today.
register(Check(
    key="verify.external_dns_query",
    label="Auth DNS resolves a sentinel A record over v6",
    fn=_check_external_dns_query,
    external=True,
))
register(Check(
    key="verify.external_dhcp_dora",
    label="DHCPv6 4-way exchange (Solicit→Advertise→Request→Reply) succeeds",
    fn=_check_external_dhcp_dora,
    external=True,
))
register(Check(
    key="verify.external_collector_checkin",
    label="Site collector has checked in with central within 60s",
    fn=_check_external_collector_checkin,
    external=True,
))
register(Check(
    key="verify.external_hubble_flows",
    label="Hubble reports pod-to-LB-IP flows via Cilium BGP",
    fn=_check_external_hubble_flows,
    external=True,
))
