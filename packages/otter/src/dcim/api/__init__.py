"""Versioned API surface — v1."""

from fastapi import APIRouter

from .admin import router as admin_router
from .alerts import router as alerts_router
from .bgp import router as bgp_router
from .collectors import router as collectors_router
from .dashboards import router as dashboards_router
from .dns import router as dns_router
from .ingest import router as ingest_router
from .inventory import router as inventory_router
from .ipam import router as ipam_router
from .notifications import router as notifications_router
from .organization import router as organization_router
from .power import router as power_router
from .regiondeploy import router as regiondeploy_router
from .search import router as search_router
from .stencils import router as stencils_router

router = APIRouter()
# /api/v1/auth/* moved to otter-go (PR 179). The umbrella chart
# routes /api/v1/auth → otter-go; Python no longer registers the
# router so a misrouted internal call fails loud instead of silently
# double-serving login/me/tokens. The schemas + models for User /
# ApiToken / Role still live under packages/otter/src/dcim/{schemas,
# models}/auth.py — they back DB rows other Python paths still read.
router.include_router(inventory_router)
router.include_router(collectors_router)
# /api/v1/ingest/telemetry remains on Python until the high-throughput
# fallback gets a Go port; heron already owns the mTLS path.
router.include_router(ingest_router)
# /api/v1/telemetry/series moved to otter-go (PR 178). The umbrella
# chart routes /api/v1/telemetry → otter-go; Python no longer registers
# the router so a misrouted internal call fails loud instead of
# silently double-serving.
router.include_router(alerts_router)
router.include_router(dashboards_router)
router.include_router(search_router)
router.include_router(stencils_router)
router.include_router(power_router)
# /api/v1/audit/* moved to otter-go (PR 180). The umbrella chart
# routes /api/v1/audit → otter-go; Python no longer registers the
# router so a misrouted internal call fails loud instead of silently
# double-serving. The audit_log table + the security.audit.record()
# helper still live in Python (mutation handlers write to it).
router.include_router(admin_router)
router.include_router(notifications_router)
router.include_router(organization_router)
router.include_router(ipam_router)
router.include_router(dns_router)
router.include_router(bgp_router)
router.include_router(regiondeploy_router)
