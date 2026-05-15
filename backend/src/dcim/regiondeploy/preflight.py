"""Region-deploy pre-flight checks framework.

Per docs/dev/region-deploy.md §7, a deployment can only enter the
`provisioning` stage once every registered check passes. The UI's
**Start** button is gated on the result of this framework.

Design constraints baked into the shape here:

  * **Each check is a small, named function** — no class inheritance.
    Checks are easy to write, easy to grep for, easy to reorder.
  * **Checks return `Result(passed, fix_hint)`** — never raise. A
    raised exception is a bug in the check, not a pre-flight
    failure; we surface it as a synthetic check failure pointing at
    the check name.
  * **Checks declare what they need via dependencies** — a check
    that talks to BMCs needs the node list and BMC creds; a check
    that validates the v6 mgmt prefix needs the site IPAM. The
    `Context` dataclass passed to each check carries everything,
    and a registered check declares which fields it reads via the
    `requires` tuple (informational; used by the runner to skip
    network-hitting checks during a "dry" preflight call).
  * **Checks are pure where possible** — those that have to hit the
    network or the DB are marked `external=True`. The default runner
    runs both; a future "fast preflight" mode for the wizard's live
    feedback can run only `external=False` checks.

This module ships the framework + a handful of pure checks. Network-
or-DB-hitting checks land alongside the orchestrator modules they
share dependencies with (BMC client → PR with Redfish; IPAM site
queries → here once we wire the DB connection; etc.).
"""

from __future__ import annotations

from collections.abc import Callable, Iterable
from dataclasses import dataclass, field
from typing import Any


@dataclass(frozen=True)
class Result:
    """Outcome of a single check. `fix_hint` is the operator-facing
    message that the UI surfaces next to a failing check; it should
    be specific enough to act on (e.g. "Node node-3 (10.42.99.13):
    Redfish /redfish/v1/ returned 503"), not a generic "BMC failed"."""

    passed: bool
    fix_hint: str | None = None


@dataclass(frozen=True)
class Check:
    """A registered check.

    `key` is the stable identifier the UI uses to remember which
    checks were dismissed / which failed last time. Never rename a
    key once it's shipped — only deprecate by adding a new one.
    """

    key: str
    label: str
    fn: Callable[[Context], Result]
    external: bool = False


@dataclass
class Context:
    """Everything a check might want to inspect.

    All fields default to None so callers can construct a partial
    Context for the cheap path (e.g. wizard-side validation that
    doesn't need IPAM yet). Checks should treat missing context as
    "fail with hint" rather than crashing.
    """

    deployment: Any | None = None
    site: Any | None = None
    nodes: list[Any] = field(default_factory=list)
    config: dict | None = None


@dataclass(frozen=True)
class CheckOutcome:
    """A Check + its Result, in the shape the API layer renders.

    Separate from Check itself so we don't accidentally leak the
    callable into the response model.
    """

    key: str
    label: str
    passed: bool
    fix_hint: str | None = None


# ─── Registry ───────────────────────────────────────────────────────────


_REGISTRY: list[Check] = []


def register(check: Check) -> Check:
    """Register a check. Idempotent on key — re-registering replaces
    the prior entry, so tests can monkey-patch a check without
    leaking into other tests via global mutation."""
    global _REGISTRY
    _REGISTRY = [c for c in _REGISTRY if c.key != check.key]
    _REGISTRY.append(check)
    return check


def registered() -> list[Check]:
    """Return the live registry. Mostly for tests + the runner."""
    return list(_REGISTRY)


def run_all(ctx: Context, *, include_external: bool = True) -> list[CheckOutcome]:
    """Run every registered check against `ctx` and return the
    aggregated outcomes.

    External checks are skipped when `include_external=False`. The
    wizard uses that for live (sub-second) validation; the API
    endpoint uses the full set as a hard gate before deploy start.
    """
    outcomes: list[CheckOutcome] = []
    for check in _REGISTRY:
        if check.external and not include_external:
            continue
        try:
            res = check.fn(ctx)
        except Exception as e:
            res = Result(
                passed=False,
                fix_hint=f"check '{check.key}' raised {type(e).__name__}: {e}",
            )
        outcomes.append(
            CheckOutcome(
                key=check.key,
                label=check.label,
                passed=res.passed,
                fix_hint=res.fix_hint,
            )
        )
    return outcomes


def ready(outcomes: Iterable[CheckOutcome]) -> bool:
    """Hard-gate decision: every check must have passed."""
    return all(o.passed for o in outcomes)


# ─── Built-in pure checks ──────────────────────────────────────────────
# These don't talk to external systems — they validate the shape of
# the deployment row / config. External checks (BMC reachability, BGP
# peer up, etc.) register from the modules that own those clients.


def _check_distinct_macs(ctx: Context) -> Result:
    """Node MACs must be unique inside a deployment — Tinkerbell's
    Hardware lookup is MAC-keyed and Smee will pick whichever CR
    indexed second."""
    seen: dict[str, str] = {}
    for n in ctx.nodes:
        mac = str(n.mac).lower()
        if mac in seen:
            return Result(
                passed=False,
                fix_hint=f"MAC {mac} is assigned to both {seen[mac]} and {n.hostname}",
            )
        seen[mac] = n.hostname
    return Result(passed=True)


def _check_distinct_hostnames(ctx: Context) -> Result:
    """Hostnames flow into CR names + the Ignition /etc/hostname.
    Duplicates produce ambiguous kubectl listings and break kubeadm's
    node-name uniqueness assumption."""
    seen: set[str] = set()
    for n in ctx.nodes:
        if n.hostname in seen:
            return Result(
                passed=False,
                fix_hint=f"hostname {n.hostname} appears more than once",
            )
        seen.add(n.hostname)
    return Result(passed=True)


def _check_at_least_one_control_plane(ctx: Context) -> Result:
    """A deployment must include at least one control_plane node;
    otherwise `kubeadm init` has nowhere to run."""
    cp = [n for n in ctx.nodes if _role_of(n) == "control_plane"]
    if not cp:
        return Result(
            passed=False,
            fix_hint="no control_plane node selected; add at least one",
        )
    return Result(passed=True)


def _check_v6_pod_prefix(ctx: Context) -> Result:
    """Cilium's v6 pod-CIDR has to come from the config — there's no
    sane default for a multi-site fabric."""
    if not (ctx.config or {}).get("pod_cidr_v6"):
        return Result(
            passed=False,
            fix_hint="config.pod_cidr_v6 is unset (e.g. fd00:site:42:1000::/56)",
        )
    return Result(passed=True)


def _check_v6_svc_prefix(ctx: Context) -> Result:
    if not (ctx.config or {}).get("svc_cidr_v6"):
        return Result(
            passed=False,
            fix_hint="config.svc_cidr_v6 is unset",
        )
    return Result(passed=True)


def _check_v6_lb_pool(ctx: Context) -> Result:
    if not (ctx.config or {}).get("lb_pool_v6"):
        return Result(
            passed=False,
            fix_hint="config.lb_pool_v6 is unset (the v6 LB-IP pool Cilium advertises)",
        )
    return Result(passed=True)


def _check_bgp_peers_configured(ctx: Context) -> Result:
    peers = (ctx.config or {}).get("bgp_peers") or []
    if not peers:
        return Result(
            passed=False,
            fix_hint="config.bgp_peers is empty — Cilium needs at least one peer",
        )
    return Result(passed=True)


def _role_of(node: Any) -> str:
    """Coerce node.role (enum or string) to its string value."""
    return getattr(node.role, "value", node.role)


# Register the built-in pure checks. External checks (BMC reachable,
# BGP peer up, Tinkerbell healthy, etc.) register from their own
# modules so a partial backend (e.g. no Redfish client yet) still
# returns a coherent preflight result.
register(Check(
    key="nodes.distinct_macs",
    label="Node MACs are unique within the deployment",
    fn=_check_distinct_macs,
))
register(Check(
    key="nodes.distinct_hostnames",
    label="Node hostnames are unique within the deployment",
    fn=_check_distinct_hostnames,
))
register(Check(
    key="nodes.has_control_plane",
    label="At least one control_plane node is selected",
    fn=_check_at_least_one_control_plane,
))
register(Check(
    key="site.has_v6_pod_prefix",
    label="Site has IPv6 pod prefix allocated",
    fn=_check_v6_pod_prefix,
))
register(Check(
    key="site.has_v6_svc_prefix",
    label="Site has IPv6 service prefix allocated",
    fn=_check_v6_svc_prefix,
))
register(Check(
    key="site.has_v6_lb_pool",
    label="Site has IPv6 LB pool allocated",
    fn=_check_v6_lb_pool,
))
register(Check(
    key="bgp.peers_configured",
    label="At least one BGP peer is configured",
    fn=_check_bgp_peers_configured,
))
