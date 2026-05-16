"""Region Deploy API — read endpoints, lifecycle, and SSE event stream."""

from __future__ import annotations

import asyncio
import json
from uuid import UUID

from arq import create_pool
from arq.connections import RedisSettings
from fastapi import APIRouter, Depends, Query, Request
from fastapi.responses import StreamingResponse
from redis.asyncio import from_url as redis_from_url
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.orm import selectinload

from ..db import get_db
from ..errors import NotFoundError, ValidationError
from ..models.regiondeploy import (
    RegionDeployment,
    RegionDeploymentEvent,
    RegionDeploymentNode,
    RegionDeploymentStatus,
)
from ..regiondeploy import events as rd_events
from ..regiondeploy import preflight
from ..schemas.common import Page, PageParams
from ..schemas.regiondeploy import (
    PreflightCheckOut,
    PreflightResponse,
    RegionDeploymentCreate,
    RegionDeploymentEventOut,
    RegionDeploymentKubeconfigCallback,
    RegionDeploymentOut,
    RegionDeploymentSummary,
)
from ..security.deps import Principal, require_capability
from ..security.scope import enforce_site_scope, scope_filtered_site_ids
from ..settings import get_settings
from ._pagination import empty_page, paginate

router = APIRouter(prefix="/region-deployments", tags=["region-deployments"])

# Capability constants — kept module-level so callers don't drift on
# spelling and the lint stops flagging duplicate literals.
CAP_READ = "infrastructure:region-deployments:read"
CAP_CREATE = "infrastructure:region-deployments:create"
CAP_START = "infrastructure:region-deployments:start"
CAP_ABORT = "infrastructure:region-deployments:abort"

# arq queue name — leave at the default so we share the existing
# worker pool. A dedicated queue for region-deploy can land later if
# long-running deploys start blocking the telemetry/DHCP jobs.
RD_TASK = "run_region_deploy"

# Centralised "not found" message — five endpoints + the SSE stream
# 404 on the same condition; keep the wording identical.
NOT_FOUND_MSG = "region deployment not found"


@router.get("", response_model=Page[RegionDeploymentSummary])
async def list_region_deployments(
    params: PageParams = Depends(PageParams.from_query),
    principal: Principal = Depends(
        require_capability(CAP_READ),
    ),
    db: AsyncSession = Depends(get_db),
):
    """List all region deployments visible to the caller, scoped by site."""
    stmt = select(RegionDeployment)
    in_scope = await scope_filtered_site_ids(
        db, principal.capabilities, CAP_READ,
    )
    if in_scope is not None:
        if not in_scope:
            return empty_page(RegionDeploymentSummary, params)
        stmt = stmt.where(RegionDeployment.site_id.in_(in_scope))
    return await paginate(
        db, stmt,
        model=RegionDeployment, params=params, out_model=RegionDeploymentSummary,
    )


@router.get("/{deployment_id}", response_model=RegionDeploymentOut)
async def get_region_deployment(
    deployment_id: UUID,
    _: Principal = Depends(
        require_capability(CAP_READ),
    ),
    db: AsyncSession = Depends(get_db),
):
    """Fetch a single deployment with its nodes and per-service install state."""
    # selectinload pulls nodes + services in two extra queries instead of a
    # cartesian product; small N per deploy makes that the right trade-off.
    stmt = (
        select(RegionDeployment)
        .where(RegionDeployment.id == deployment_id)
        .options(
            selectinload(RegionDeployment.nodes),
            selectinload(RegionDeployment.services),
        )
    )
    row = (await db.execute(stmt)).scalar_one_or_none()
    if row is None:
        raise NotFoundError(NOT_FOUND_MSG)
    return row


@router.get("/{deployment_id}/preflight", response_model=PreflightResponse)
async def get_region_deployment_preflight(
    deployment_id: UUID,
    _: Principal = Depends(
        require_capability(CAP_READ),
    ),
    db: AsyncSession = Depends(get_db),
):
    """Run the pre-flight checklist against a deployment.

    Used by the wizard's "Review & launch" step and by the
    orchestrator's `preflight` stage (PR 7+). The UI's Start button
    binds to `ready` — true iff every check passed.

    External checks (BMC reachable, BGP peer up, Tinkerbell healthy)
    register from their owning modules; the runner skips them when
    they're not present yet, so this endpoint stays renderable
    even before those modules land.
    """
    stmt = (
        select(RegionDeployment)
        .where(RegionDeployment.id == deployment_id)
        .options(selectinload(RegionDeployment.nodes))
    )
    row = (await db.execute(stmt)).scalar_one_or_none()
    if row is None:
        raise NotFoundError(NOT_FOUND_MSG)
    ctx = preflight.Context(
        deployment=row,
        nodes=list(row.nodes),
        config=row.config,
    )
    outcomes = preflight.run_all(ctx)
    return PreflightResponse(
        ready=preflight.ready(outcomes),
        checks=[
            PreflightCheckOut(
                key=o.key, label=o.label, passed=o.passed, fix_hint=o.fix_hint,
            )
            for o in outcomes
        ],
    )


# ─── Lifecycle endpoints ────────────────────────────────────────────────


@router.post("", response_model=RegionDeploymentOut, status_code=201)
async def create_region_deployment(
    payload: RegionDeploymentCreate,
    principal: Principal = Depends(require_capability(CAP_CREATE)),
    db: AsyncSession = Depends(get_db),
):
    """Create a deployment row in `pending` state.

    Doesn't start the orchestrator — POST `/start` does that. Splits
    the wizard's two operator concerns (review inputs, then commit
    to running) into two endpoints so a half-saved deploy doesn't
    accidentally power-cycle nodes.
    """
    await enforce_site_scope(
        db, principal.capabilities, CAP_CREATE, payload.site_id,
    )
    row = RegionDeployment(
        site_id=payload.site_id,
        name=payload.name,
        config=payload.config,
    )
    db.add(row)
    await db.flush()
    for n in payload.nodes:
        db.add(RegionDeploymentNode(
            deployment_id=row.id,
            hostname=n.hostname,
            mac=n.mac,
            bmc_address=n.bmc_address,
            role=n.role,
            primary_ip_v6=n.primary_ip_v6,
            provisioning_ip_v6=n.provisioning_ip_v6,
            bmc_creds_secret_ref=n.bmc_creds_secret_ref,
        ))
    await db.commit()
    # Re-load with nodes/services so the response matches GET /{id}.
    stmt = (
        select(RegionDeployment)
        .where(RegionDeployment.id == row.id)
        .options(
            selectinload(RegionDeployment.nodes),
            selectinload(RegionDeployment.services),
        )
    )
    return (await db.execute(stmt)).scalar_one()


@router.post("/{deployment_id}/start", response_model=RegionDeploymentOut)
async def start_region_deployment(
    deployment_id: UUID,
    _: Principal = Depends(require_capability(CAP_START)),
    db: AsyncSession = Depends(get_db),
):
    """Enqueue the orchestrator for a deployment.

    Only valid when the deployment is in a startable state (pending,
    failed, or aborted). Already-running deploys raise — operators
    abort first, then start again.
    """
    row = await db.get(RegionDeployment, deployment_id)
    if row is None:
        raise NotFoundError(NOT_FOUND_MSG)
    startable = {
        RegionDeploymentStatus.pending,
        RegionDeploymentStatus.failed,
        RegionDeploymentStatus.aborted,
    }
    if row.status not in startable:
        raise ValidationError(
            f"deployment is {row.status.value}; abort first to restart",
        )
    # Reset for re-run.
    row.status = RegionDeploymentStatus.preflight
    row.last_error = None
    if row.status == RegionDeploymentStatus.failed:
        # Resume from the last failed stage. row.current_stage is left
        # as-is so the orchestrator picks up where it stopped.
        pass
    await db.commit()
    pool = await _arq_pool()
    try:
        await pool.enqueue_job(RD_TASK, str(deployment_id))
    finally:
        await pool.close()
    return await _reload(db, deployment_id)


@router.post(
    "/{deployment_id}/kubeconfig/callback",
    status_code=202,
    response_model=None,
)
async def post_kubeconfig_callback(
    deployment_id: UUID,
    payload: RegionDeploymentKubeconfigCallback,
    db: AsyncSession = Depends(get_db),
):
    """Receive the kubeadm-generated kubeconfig from a first
    control-plane node.

    The Workflow template's `kubeconfig-write` action posts here
    after `kubeadm init` succeeds. Today we only record receipt —
    actually persisting the kubeconfig as a k8s Secret on central
    needs the k8s-client + RBAC work that's still pending. The
    deployment row's `kubeconfig_secret_ref` is set to a placeholder
    name (`tinkerbell/kubeconfig-<id>`) so the orchestrator's
    joining stage knows what Secret to look for once the create
    side lands.

    Auth: deliberately NOT behind require_capability. The Tink
    Worker action runs from a freshly-booted node that doesn't have
    a DCIM token. The endpoint is hardened by:

      * accepting only deployments in `joining` or `provisioning`
        state (post-PXE, pre-finalize);
      * the deployment_id is path-scoped — a node only knows its
        own deployment id, baked into the Ignition payload.

    Once the central-cluster Secret-write path lands, the endpoint
    will additionally require a one-shot bootstrap token minted at
    deploy-start and embedded in the Workflow template. Tracked in
    docs/dev/region-deploy.md §3a kubeconfig workstream.
    """
    row = await db.get(RegionDeployment, deployment_id)
    if row is None:
        raise NotFoundError(NOT_FOUND_MSG)
    if row.status not in {
        RegionDeploymentStatus.provisioning,
        RegionDeploymentStatus.joining,
    }:
        raise ValidationError(
            f"deployment is {row.status.value}; kubeconfig callback only "
            f"accepted during provisioning/joining",
        )
    # Placeholder ref name — the orchestrator looks for this Secret
    # in the joining stage. Real Secret creation lands with the
    # central-cluster k8s-client workstream.
    row.kubeconfig_secret_ref = f"tinkerbell/kubeconfig-{deployment_id}"
    await db.commit()
    # Best-effort event for the SSE stream — gives operators visible
    # confirmation that the callback fired without exposing the
    # kubeconfig content in the event log.
    settings = get_settings()
    redis = redis_from_url(str(settings.redis_dsn), decode_responses=True)
    try:
        await rd_events.emit(
            db, redis,
            deployment_id=deployment_id,
            stage="joining",
            message=(
                f"kubeconfig callback received from node {payload.node_id} "
                f"({len(payload.kubeconfig)} bytes); "
                "Secret creation pending kubeconfig workstream"
            ),
        )
        await db.commit()
    finally:
        await redis.close()
    return None


@router.post("/{deployment_id}/abort", response_model=RegionDeploymentOut)
async def abort_region_deployment(
    deployment_id: UUID,
    _: Principal = Depends(require_capability(CAP_ABORT)),
    db: AsyncSession = Depends(get_db),
):
    """Mark the deployment aborted.

    The orchestrator polls the row between stages; on the next poll
    it sees `aborted` and exits. Power-off of in-progress nodes via
    Rufio happens inside the orchestrator's abort handling stage —
    setting the status alone here keeps the API response fast.
    """
    row = await db.get(RegionDeployment, deployment_id)
    if row is None:
        raise NotFoundError(NOT_FOUND_MSG)
    if row.status in {
        RegionDeploymentStatus.ready,
        RegionDeploymentStatus.aborted,
    }:
        raise ValidationError(
            f"deployment is {row.status.value}; cannot abort",
        )
    row.status = RegionDeploymentStatus.aborted
    await db.commit()
    return await _reload(db, deployment_id)


# ─── Event stream ───────────────────────────────────────────────────────


@router.get("/{deployment_id}/events", response_model=list[RegionDeploymentEventOut])
async def list_region_deployment_events(
    deployment_id: UUID,
    since: int = Query(0, ge=0, description="Return events with id > since"),
    limit: int = Query(500, ge=1, le=5000),
    _: Principal = Depends(require_capability(CAP_READ)),
    db: AsyncSession = Depends(get_db),
):
    """Paginated event history. The SSE endpoint uses this same
    cursor-by-id pattern internally for catch-up on reconnect."""
    stmt = (
        select(RegionDeploymentEvent)
        .where(RegionDeploymentEvent.deployment_id == deployment_id)
        .where(RegionDeploymentEvent.id > since)
        .order_by(RegionDeploymentEvent.id.asc())
        .limit(limit)
    )
    return list((await db.execute(stmt)).scalars().all())


@router.get("/{deployment_id}/events/stream")
async def stream_region_deployment_events(
    deployment_id: UUID,
    request: Request,
    since: int = Query(0, ge=0),
    _: Principal = Depends(require_capability(CAP_READ)),
    db: AsyncSession = Depends(get_db),
):
    """Server-Sent Events stream of orchestrator events.

    Catch-up semantics:
      1. Backfill from `region_deployment_events` where `id > since`.
      2. Subscribe to Redis pubsub channel `dcim:deploy:{id}` for
         live events.
      3. On disconnect (request.is_disconnected) tear down cleanly.

    The client echoes the last seen `id` back on reconnect via the
    `since` query param, so a brief network hiccup doesn't lose
    events.
    """
    # Confirm the deployment exists before opening the long-lived
    # stream — saves the client from a hung connection on a 404.
    if (await db.get(RegionDeployment, deployment_id)) is None:
        raise NotFoundError(NOT_FOUND_MSG)

    async def event_source():
        async for frame in _sse_backfill(db, deployment_id, since, request):
            yield frame
            if await request.is_disconnected():
                return
        async for frame in _sse_live(deployment_id, request):
            yield frame

    return StreamingResponse(event_source(), media_type="text/event-stream")


async def _sse_backfill(db, deployment_id, since, request):
    """Stream persisted events from the DB before attaching to pubsub.

    Pulled out so the SSE generator has a single layered shape:
    backfill → live → done. The cancellation check happens here too
    so a long backlog can't keep streaming after the client
    disconnects."""
    stmt = (
        select(RegionDeploymentEvent)
        .where(RegionDeploymentEvent.deployment_id == deployment_id)
        .where(RegionDeploymentEvent.id > since)
        .order_by(RegionDeploymentEvent.id.asc())
    )
    rows = (await db.execute(stmt)).scalars().all()
    for row in rows:
        if await request.is_disconnected():
            return
        yield _sse_event(row.id, _row_to_envelope(row))


async def _sse_live(deployment_id, request):
    """Subscribe to pubsub and forward messages as SSE frames.

    Heartbeat every 15s keeps proxies from dropping the connection
    during quiet stretches between stages. Cleanup happens in the
    `finally` so a torn-down stream doesn't leak Redis subscriptions.
    """
    settings = get_settings()
    redis = redis_from_url(str(settings.redis_dsn), decode_responses=True)
    pubsub = redis.pubsub()
    channel = rd_events.channel_for(deployment_id)
    await pubsub.subscribe(channel)
    try:
        while not await request.is_disconnected():
            frame = await _next_sse_frame(pubsub)
            if frame is not None:
                yield frame
    finally:
        await pubsub.unsubscribe(channel)
        await pubsub.close()
        await redis.close()


async def _next_sse_frame(pubsub) -> str | None:
    """Wait up to 15s for a pubsub message; return a heartbeat
    comment on timeout, the event frame on a real message, or None
    when the message is malformed (caller swallows + loops)."""
    try:
        msg = await asyncio.wait_for(
            pubsub.get_message(ignore_subscribe_messages=True, timeout=15),
            timeout=20,
        )
    except TimeoutError:
        msg = None
    if msg is None:
        return ": heartbeat\n\n"
    data = msg.get("data")
    if not data:
        return None
    try:
        env = json.loads(data)
    except json.JSONDecodeError:
        return None
    return _sse_event(env.get("id", 0), env)


# ─── helpers ────────────────────────────────────────────────────────────


async def _reload(db: AsyncSession, deployment_id: UUID) -> RegionDeployment:
    stmt = (
        select(RegionDeployment)
        .where(RegionDeployment.id == deployment_id)
        .options(
            selectinload(RegionDeployment.nodes),
            selectinload(RegionDeployment.services),
        )
    )
    return (await db.execute(stmt)).scalar_one()


async def _arq_pool():
    """Connect to the same Redis the worker uses. Caller owns close."""
    settings = get_settings()
    return await create_pool(RedisSettings.from_dsn(str(settings.redis_dsn)))


def _row_to_envelope(row: RegionDeploymentEvent) -> dict:
    return {
        "id": row.id,
        "stage": row.stage,
        "level": row.level.value,
        "message": row.message,
        "payload": row.payload or {},
    }


def _sse_event(event_id: int, data: dict) -> str:
    """Format a single SSE frame. `id:` lets the client send it back
    via Last-Event-ID / our `?since=` param on reconnect."""
    return f"id: {event_id}\ndata: {json.dumps(data, default=str)}\n\n"
