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
from ..schemas.common import Page, PageParams
from ..schemas.regiondeploy import RegionDeploymentOut, RegionDeploymentSummary
from ..security.deps import Principal, require_capability
from ..security.scope import scope_filtered_site_ids
from ._pagination import empty_page, paginate

router = APIRouter(prefix="/region-deployments", tags=["region-deployments"])


@router.get("", response_model=Page[RegionDeploymentSummary])
async def list_region_deployments(
    params: PageParams = Depends(PageParams.from_query),
    principal: Principal = Depends(
        require_capability("infrastructure:region-deployments:read"),
    ),
    db: AsyncSession = Depends(get_db),
):
    """List all region deployments visible to the caller, scoped by site."""
    stmt = select(RegionDeployment)
    in_scope = await scope_filtered_site_ids(
        db, principal.capabilities, "infrastructure:region-deployments:read",
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
        require_capability("infrastructure:region-deployments:read"),
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
