"""Region-deploy orchestrator — arq task + state machine.

`run_region_deploy(ctx, deployment_id)` is the arq function the API
enqueues when an operator clicks **Start**. It walks the deployment
through its stages (preflight → secrets → render → pxe → joining →
cni → apps → seed → verify → finalize), emitting events at every
transition so the UI's SSE stream surfaces progress.

This PR ships the **scaffolding only** — every stage is a stub that
emits its start/done events and advances `current_stage`/`status`.
Real implementations land in PRs 8-11, each owning the work for
their own stage(s) and landing alongside the modules they need
(Redfish client, Helm install, etc.).

Why arq?
  The project already runs an arq worker for telemetry + DHCP/DNS
  background jobs (cf. dcim.worker). Reusing it avoids a second
  task-queue runtime and gives us the existing Redis connection
  pool, retry semantics, and observability hooks.

Failure model:
  * A stage raising → the orchestrator catches, emits an error
    event, sets status=failed + last_error, and stops. Operator
    decides whether to retry from `last_successful_stage + 1`.
  * The task itself never raises out — arq treats raised tasks as
    permanently failed, which would leave `status='provisioning'`
    forever.

Abort:
  Cancellation flows via the row's status: the API endpoint flips
  status to `aborted` and the orchestrator checks the row at every
  stage transition. No background signal — keeps the cancellation
  semantics observable in DB without a separate channel.
"""

from __future__ import annotations

import contextlib
from datetime import UTC, datetime
from typing import Any
from uuid import UUID

import structlog
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.orm import selectinload

from ..db import async_session
from ..models.regiondeploy import (
    RegionDeployment,
    RegionDeploymentEventLevel,
    RegionDeploymentStatus,
)
from . import cilium as cilium_gen
from . import crd as crd_gen
from . import events, preflight
from . import ignition as ignition_gen

log = structlog.get_logger("dcim.regiondeploy.orchestrator")


# Stage order — keep in sync with docs/dev/region-deploy.md §6.
# Each tuple is (stage_key, status_when_running). The status is what
# the UI badge shows while this stage is mid-flight; on completion
# we transition to the next stage's status (or `ready` after the
# last stage).
STAGES: list[tuple[str, RegionDeploymentStatus]] = [
    ("preflight", RegionDeploymentStatus.preflight),
    ("secrets", RegionDeploymentStatus.provisioning),
    ("render", RegionDeploymentStatus.provisioning),
    ("pxe.power", RegionDeploymentStatus.provisioning),
    ("pxe.install", RegionDeploymentStatus.provisioning),
    ("joining", RegionDeploymentStatus.joining),
    ("cni", RegionDeploymentStatus.cni),
    ("cni.bgp", RegionDeploymentStatus.cni),
    ("apps.cert-manager", RegionDeploymentStatus.apps),
    ("apps.dns_auth", RegionDeploymentStatus.apps),
    ("apps.dns_recursive", RegionDeploymentStatus.apps),
    ("apps.dhcp", RegionDeploymentStatus.apps),
    ("apps.collector", RegionDeploymentStatus.apps),
    ("seed", RegionDeploymentStatus.apps),
    ("verify", RegionDeploymentStatus.verify),
    ("finalize", RegionDeploymentStatus.ready),
]


async def run_region_deploy(ctx: dict, deployment_id: str) -> dict:
    """arq entrypoint. Always returns a dict; never raises out so
    arq retries don't fight with our row-level state."""
    dep_id = UUID(deployment_id)
    redis = ctx.get("redis") if ctx else None
    try:
        async with async_session() as db:
            return await _run(db, redis, dep_id)
    except Exception as exc:
        log.exception("orchestrator_crashed", deployment_id=str(dep_id))
        # Best-effort: mark the row failed so the UI doesn't sit in
        # a transient state forever. Suppress secondary failures so
        # the original exception's reason is what the caller sees.
        with contextlib.suppress(Exception):
            async with async_session() as db:
                row = await db.get(RegionDeployment, dep_id)
                if row is not None:
                    row.status = RegionDeploymentStatus.failed
                    row.last_error = f"orchestrator crashed: {exc}"
                    row.finished_at = datetime.now(UTC)
                    await db.commit()
        return {"ok": False, "error": str(exc)}


async def _run(db: AsyncSession, redis: Any, dep_id: UUID) -> dict:
    row = await _load(db, dep_id)
    if row is None:
        log.warn("orchestrator_deployment_missing", deployment_id=str(dep_id))
        return {"ok": False, "error": "deployment not found"}
    if row.status == RegionDeploymentStatus.aborted:
        # The API may have flipped the row to aborted between
        # enqueue and the worker picking it up. Honour it.
        return {"ok": False, "aborted_before_start": True}

    # Decide where to start. New deploys start at the first stage;
    # retried deploys resume from the stage after the last completed
    # one. `current_stage` is the stage about to run (or that failed),
    # so the resume point is `current_stage` itself.
    start_index = 0
    if row.current_stage:
        for i, (key, _) in enumerate(STAGES):
            if key == row.current_stage:
                start_index = i
                break

    row.started_at = row.started_at or datetime.now(UTC)
    row.last_error = None
    await db.commit()

    for stage_key, status in STAGES[start_index:]:
        # Cancellation poll — cheap row reload between stages.
        latest = await db.get(RegionDeployment, dep_id)
        if latest is None or latest.status == RegionDeploymentStatus.aborted:
            return {"ok": False, "aborted": True, "at": stage_key}

        latest.current_stage = stage_key
        latest.status = status
        await db.commit()

        await events.emit(
            db, redis,
            deployment_id=dep_id,
            stage=stage_key,
            message=f"entering stage {stage_key}",
            payload={"status": status.value},
        )
        await db.commit()

        try:
            await _run_stage(db, redis, latest, stage_key)
        except Exception as exc:
            log.exception(
                "stage_failed",
                deployment_id=str(dep_id), stage=stage_key,
            )
            failed = await db.get(RegionDeployment, dep_id)
            if failed is not None:
                failed.status = RegionDeploymentStatus.failed
                failed.last_error = f"{stage_key}: {exc}"
                failed.finished_at = datetime.now(UTC)
                await db.commit()
            await events.emit(
                db, redis,
                deployment_id=dep_id,
                stage=stage_key,
                level=RegionDeploymentEventLevel.error,
                message=f"stage failed: {exc}",
            )
            await db.commit()
            return {"ok": False, "failed_at": stage_key, "error": str(exc)}

    # All stages completed. The finalize stage already set status=ready
    # via its STAGES tuple entry; just record finished_at.
    done = await db.get(RegionDeployment, dep_id)
    if done is not None:
        done.status = RegionDeploymentStatus.ready
        done.current_stage = None
        done.finished_at = datetime.now(UTC)
        await db.commit()
    return {"ok": True}


async def _run_stage(
    db: AsyncSession, redis: Any, row: RegionDeployment, stage_key: str,
) -> None:
    """Dispatch to per-stage handlers. Most are stubs in this PR —
    real implementations land in PRs 8-11."""
    if stage_key == "preflight":
        await _stage_preflight(db, redis, row)
        return
    if stage_key == "render":
        await _stage_render(db, redis, row)
        return
    if stage_key == "cni":
        await _stage_cni(db, redis, row)
        return
    if stage_key == "cni.bgp":
        await _stage_cni_bgp(db, redis, row)
        return
    # Remaining stages are stubs for now: emit an info event and
    # advance. The UI shows the stage tree progressing without any
    # real cluster mutations happening.
    await events.emit(
        db, redis,
        deployment_id=row.id,
        stage=stage_key,
        message=f"stub stage {stage_key} — real implementation pending",
        payload={"stub": True},
    )
    await db.commit()


async def _stage_render(
    db: AsyncSession, redis: Any, row: RegionDeployment,
) -> None:
    """Render the Tinkerbell/Rufio CRDs + per-node Ignition.

    This stage **doesn't apply anything to a cluster yet** — that's
    pending the central-cluster RBAC chart change that lets the api/
    worker service account write `tinkerbell.org` and `bmc.tinkerbell.org`
    CRs. What it does today:

      * Run the PR 4 + PR 5 generators against the deployment row.
      * Emit two events whose `payload` carries the rendered output:
          - render.crds : multi-doc YAML for all CRs (Template +
            Hardware/BMCMachine/Workflow per node)
          - render.ignition : per-node Ignition JSON dict, keyed by
            node id

    Operators inspecting `/region-deployments/{id}/events` see the
    full manifest set + Ignition payloads and can copy-paste them
    into a cluster for now. Once the apply path lands, the stage
    becomes "render → apply → confirm" without changing the event
    surface — the UI keeps working unchanged.

    Why expose the rendered output now: it makes the stage *useful*
    immediately for testing the PR 4/5 generators against real deploy
    configs, without waiting on the cluster-apply RBAC work.
    """
    # `image_url` is the Flatcar PXE image URL Smee hands clients
    # via Option 59 (v4) or as part of the iPXE script (v6). For
    # now we read it from config.flatcar_image_url, falling back to
    # an empty string so the renderer still produces inspectable
    # output even when the field's unset.
    cfg = row.config or {}
    image_url = cfg.get("flatcar_image_url", "")

    ignitions: dict[str, dict] = {}
    ignition_strs: dict[str, str] = {}
    for node in row.nodes:
        try:
            cfg_dict = ignition_gen.build_ignition(row, node)
            ignitions[str(node.id)] = cfg_dict
            ignition_strs[str(node.id)] = ignition_gen.render_ignition_for_node(
                row, node,
            )
        except ValueError as e:
            # ValueError fires when a non-first-CP node has no
            # join token yet. In the render stage that's expected —
            # the orchestrator stamps tokens in later. Skip with a
            # warning event, don't fail the stage.
            await events.emit(
                db, redis,
                deployment_id=row.id,
                stage="render",
                level=RegionDeploymentEventLevel.warn,
                message=f"ignition skipped for {node.hostname}: {e}",
            )

    crds = crd_gen.crds_for_deployment(
        row, image_url=image_url, ignition_for=ignition_strs,
    )
    crd_yaml = crd_gen.dump_yaml(crds)

    await events.emit(
        db, redis,
        deployment_id=row.id,
        stage="render",
        message=f"rendered {len(crds)} CRDs + {len(ignitions)} ignition payloads",
        payload={
            "crds_yaml": crd_yaml,
            "crd_count": len(crds),
            "ignition_for_node": ignitions,
            "note": (
                "apply path lands once central-cluster RBAC allows the "
                "api/worker SA to write tinkerbell.org CRDs"
            ),
        },
    )
    await db.commit()


async def _stage_cni(
    db: AsyncSession, redis: Any, row: RegionDeployment,
) -> None:
    """Render Cilium Helm values for the deployment.

    Same render-but-don't-apply pattern as `_stage_render`: the
    operator sees the full Helm values blob in the event payload
    and can copy-paste it into `helm install cilium cilium/cilium
    --version <ver> -f -` against the regional cluster, while the
    apply path waits on the regional-cluster kubeconfig retrieval
    workstream (`joining` stage TODO).
    """
    values = cilium_gen.render_cilium_values(row)
    version = (row.config or {}).get(
        "cilium_version", cilium_gen.DEFAULT_CILIUM_VERSION,
    )
    await events.emit(
        db, redis,
        deployment_id=row.id,
        stage="cni",
        message=f"rendered Cilium {version} values ({len(values)} top-level keys)",
        payload={
            "cilium_version": version,
            "values_yaml": cilium_gen.dump_values(values),
            "values": values,
            "note": (
                "apply path waits on regional-cluster kubeconfig retrieval"
            ),
        },
    )
    await db.commit()


async def _stage_cni_bgp(
    db: AsyncSession, redis: Any, row: RegionDeployment,
) -> None:
    """Render Cilium BGP CRDs (CiliumBGPClusterConfig / PeerConfig /
    Advertisement / LoadBalancerIPPool) from the deployment's BGP
    config.

    Skipping is *not* an error condition: a deployment that hasn't
    filled in bgp_local_asn / bgp_peers yet emits an empty render
    plus a warn event so the UI flags the gap without breaking the
    chain.
    """
    cfg = row.config or {}
    if not cfg.get("bgp_local_asn") or not cfg.get("bgp_peers"):
        await events.emit(
            db, redis,
            deployment_id=row.id,
            stage="cni.bgp",
            level=RegionDeploymentEventLevel.warn,
            message="skipping BGP render: bgp_local_asn or bgp_peers unset",
        )
        await db.commit()
        return

    crds = cilium_gen.render_bgp_crds(row)
    await events.emit(
        db, redis,
        deployment_id=row.id,
        stage="cni.bgp",
        message=f"rendered {len(crds)} Cilium BGP CRDs",
        payload={
            "crd_count": len(crds),
            "crds_yaml": cilium_gen.dump_yaml(crds),
            "crds": crds,
            "note": (
                "apply path waits on regional-cluster kubeconfig retrieval"
            ),
        },
    )
    await db.commit()


async def _stage_preflight(
    db: AsyncSession, redis: Any, row: RegionDeployment,
) -> None:
    """Re-run the preflight checks server-side — guards against the
    UI gating drifting from the orchestrator. If any check fails
    here, the stage raises and the run aborts with a stage_failed
    event whose payload carries the failing check keys."""
    ctx = preflight.Context(
        deployment=row, nodes=list(row.nodes), config=row.config,
    )
    outcomes = preflight.run_all(ctx)
    failed = [o for o in outcomes if not o.passed]
    if failed:
        keys = ", ".join(f"{o.key}" for o in failed)
        await events.emit(
            db, redis,
            deployment_id=row.id,
            stage="preflight",
            level=RegionDeploymentEventLevel.error,
            message=f"preflight failed: {keys}",
            payload={
                "failed_checks": [
                    {"key": o.key, "fix_hint": o.fix_hint} for o in failed
                ],
            },
        )
        await db.commit()
        raise RuntimeError(f"preflight failed: {keys}")
    await events.emit(
        db, redis,
        deployment_id=row.id,
        stage="preflight",
        message=f"preflight ok — {len(outcomes)} checks passed",
    )
    await db.commit()


async def _load(db: AsyncSession, dep_id: UUID) -> RegionDeployment | None:
    """Load the deployment with nodes eagerly — every stage needs them
    and the orchestrator only commits per-stage, so we don't worry
    about staleness within one stage run."""
    stmt = (
        select(RegionDeployment)
        .where(RegionDeployment.id == dep_id)
        .options(selectinload(RegionDeployment.nodes))
    )
    return (await db.execute(stmt)).scalar_one_or_none()
