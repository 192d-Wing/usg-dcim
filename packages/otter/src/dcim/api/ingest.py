"""Telemetry ingest endpoint — collectors POST batches here."""

from __future__ import annotations

from fastapi import APIRouter, Depends
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..schemas.telemetry import TelemetryBatch
from ..security.deps import Principal, require_capability
from ..services import telemetry as telem_service

router = APIRouter(prefix="/ingest", tags=["ingest"])

@router.post("/telemetry")
async def post_telemetry(
    batch: TelemetryBatch,
    _: Principal = Depends(require_capability("collectors:ingest:write")),
    db: AsyncSession = Depends(get_db),
) -> dict:
    return await telem_service.ingest(db, batch)
