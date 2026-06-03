"""Region Deploy API — last Python-canonical endpoint: POST /start.

Every other route in this module ported to otter-go across PRs #201,
#261-#265. /start is the only piece still served from Python because
it needs to enqueue an arq job for the orchestrator worker (which
itself remains in Python — Tinkerbell CRD application + Cilium policy
emission + Ignition rendering all live there). Two follow-up paths
remove this last bit:

  1. Port the orchestrator to Go (the larger investment).
  2. Add an arq-compatible enqueuer in Go and flip /start's ingress.

Either way, after that lands the whole module + the otter container
+ the CI ruff+pytest job can be deleted from the umbrella chart.
"""

from __future__ import annotations

from uuid import UUID

from arq import create_pool
from arq.connections import RedisSettings
from fastapi import APIRouter, Depends
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.orm import selectinload

from ..db import get_db
from ..errors import NotFoundError, ValidationError
from ..models.regiondeploy import RegionDeployment, RegionDeploymentStatus
from ..schemas.regiondeploy import RegionDeploymentOut
from ..security.deps import Principal, require_capability
from ..settings import get_settings

router = APIRouter(prefix="/region-deployments", tags=["region-deployments"])

CAP_START = "infrastructure:region-deployments:start"
NOT_FOUND_MSG = "region deployment not found"
# arq queue name — leave at the default so we share the existing
# worker pool.
RD_TASK = "run_region_deploy"


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
    row.status = RegionDeploymentStatus.preflight
    row.last_error = None
    await db.commit()
    pool = await _arq_pool()
    try:
        await pool.enqueue_job(RD_TASK, str(deployment_id))
    finally:
        await pool.close()
    return await _reload(db, deployment_id)


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
