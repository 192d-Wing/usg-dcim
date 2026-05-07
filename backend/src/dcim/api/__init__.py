"""Versioned API surface — v1."""

from fastapi import APIRouter

from .alerts import router as alerts_router
from .auth import router as auth_router
from .collectors import router as collectors_router
from .dashboards import router as dashboards_router
from .ingest import router as ingest_router
from .inventory import router as inventory_router
from .power import router as power_router
from .search import router as search_router
from .stencils import router as stencils_router
from .telemetry import router as telemetry_router

router = APIRouter()
router.include_router(auth_router)
router.include_router(inventory_router)
router.include_router(collectors_router)
router.include_router(ingest_router)
router.include_router(telemetry_router)
router.include_router(alerts_router)
router.include_router(dashboards_router)
router.include_router(search_router)
router.include_router(stencils_router)
router.include_router(power_router)
