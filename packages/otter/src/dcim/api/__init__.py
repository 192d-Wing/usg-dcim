"""Versioned API surface — v1."""

from fastapi import APIRouter

from .alerts import router as alerts_router
from .bgp import router as bgp_router
from .collectors import router as collectors_router
from .dns import router as dns_router
from .ingest import router as ingest_router
from .ipam import router as ipam_router
from .notifications import router as notifications_router
from .organization import router as organization_router
from .power import router as power_router
from .regiondeploy import router as regiondeploy_router
from .stencils import router as stencils_router

router = APIRouter()
# /api/v1/auth/* moved to otter-go (PR 179). The umbrella chart
# routes /api/v1/auth → otter-go; Python no longer registers the
# router so a misrouted internal call fails loud instead of silently
# double-serving login/me/tokens. The schemas + models for User /
# ApiToken / Role still live under packages/otter/src/dcim/{schemas,
# models}/auth.py — they back DB rows other Python paths still read.
#
# /api/v1/inventory/* fully moved to otter-go (this PR). Cables PATCH
# was the last gap; with it ported, the longer-prefix
# /api/v1/inventory/cables → otter ingress rule was collapsed.
router.include_router(collectors_router)
# /api/v1/ingest/telemetry remains on Python until the high-throughput
# fallback gets a Go port; heron already owns the mTLS path.
router.include_router(ingest_router)
# /api/v1/telemetry/series moved to otter-go (PR 178). The umbrella
# chart routes /api/v1/telemetry → otter-go; Python no longer registers
# the router so a misrouted internal call fails loud instead of
# silently double-serving.
router.include_router(alerts_router)
# /api/v1/dashboards/* fully moved to otter-go across phases 1-3.
# enterprise + free-space + sites/at-risk + assets/{id} + sites/{id}
# + racks/{id} + 3 forecast endpoints all on otter-go now;
# services/{capacity,power_chain,forecast}.py retired alongside.
# /api/v1/search moved to otter-go (PR-search). The umbrella chart
# routes /api/v1/search → otter-go; Python no longer registers the
# router so a misrouted internal call fails loud instead of silently
# double-serving.
router.include_router(stencils_router)
router.include_router(power_router)
# /api/v1/audit/* moved to otter-go (PR 180). The umbrella chart
# routes /api/v1/audit → otter-go; Python no longer registers the
# router so a misrouted internal call fails loud instead of silently
# double-serving. The audit_log table + the security.audit.record()
# helper still live in Python (mutation handlers write to it).
#
# /api/v1/admin/* fully moved to otter-go — including the
# capabilities/catalog + system/dns-settings routes that briefly
# lingered behind the ingress split in PR #182. The longer-prefix
# ingress paths that kept those on Python are gone.
router.include_router(notifications_router)
router.include_router(organization_router)
router.include_router(ipam_router)
router.include_router(dns_router)
router.include_router(bgp_router)
router.include_router(regiondeploy_router)
