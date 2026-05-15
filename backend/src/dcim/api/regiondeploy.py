"""Region Deploy — read-only endpoints (PR 2).

Lifecycle-changing endpoints (start/retry/abort, event stream, kubeconfig
download) ship with PR 7+ once the orchestrator state machine lands.
"""

from __future__ import annotations

from uuid import UUID

from fastapi import APIRouter, Depends
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.orm import selectinload

from ..db import get_db
from ..errors import NotFoundError
from ..models.regiondeploy import RegionDeployment
from ..regiondeploy import preflight
from ..schemas.common import Page, PageParams
from ..schemas.regiondeploy import (
    PreflightCheckOut,
    PreflightResponse,
    RegionDeploymentOut,
    RegionDeploymentSummary,
)
from ..security.deps import Principal, require_capability
from ..security.scope import scope_filtered_site_ids
from ._pagination import empty_page, paginate

router = APIRouter(prefix="/region-deployments", tags=["region-deployments"])

# Capability constants — kept module-level so callers don't drift on
# spelling and the lint stops flagging duplicate literals as we add
# more endpoints (start/abort/retry land in PR 7+).
CAP_READ = "infrastructure:region-deployments:read"


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
        raise NotFoundError("region deployment not found")
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
        raise NotFoundError("region deployment not found")
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
