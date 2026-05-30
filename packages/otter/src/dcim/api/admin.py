"""Admin routes still on Python: capability catalog + system DNS
settings. The user/role/assignment/OIDC-mapping CRUD moved to otter-go;
the umbrella chart routes /api/v1/admin to otter-go, with the two
longer-prefix paths below (/api/v1/admin/capabilities,
/api/v1/admin/system) winning under nginx-ingress longest-prefix-match
so finch keeps reaching the two unported routes here.

When the Go port lands, delete this file and drop the two longer
ingress paths.
"""

from __future__ import annotations

from datetime import datetime

from fastapi import APIRouter, Depends
from pydantic import BaseModel, ConfigDict
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..models.system import SystemSetting
from ..schemas.auth import CapabilityCatalogOut
from ..security import audit
from ..security.capabilities import CAPABILITY_CATALOG, SPECIALTY_CAPABILITIES
from ..security.deps import Principal, require_capability
from ..services.dns import (
    _SYSTEM_KEY_DNS_RECURSIVE_UPSTREAMS,
    get_system_dns_upstreams,
)
from ..settings import get_settings

router = APIRouter(prefix="/admin", tags=["admin"])


# ----------------------- Capability catalog -----------------------


@router.get("/capabilities/catalog", response_model=CapabilityCatalogOut)
async def get_capabilities_catalog(
    _: Principal = Depends(require_capability("admin:roles:read")),
) -> CapabilityCatalogOut:
    """Return the granular capability catalog so the admin UI can
    render a grouped picker. Static for the lifetime of the process —
    callers can cache aggressively."""
    return CapabilityCatalogOut(
        catalog=CAPABILITY_CATALOG,
        specialties=SPECIALTY_CAPABILITIES,
    )


# ----------------------- System DNS settings -----------------------
# Override for the env-backed dns_recursive_upstreams default. The
# fabric-level override still wins; this just gives operators an
# editable seam in front of the immutable settings.py default. New
# system-level DNS config (catch-all forwarders, etc.) lands here
# rather than in settings.py when it should be runtime-editable.


class SystemDnsSettingsOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    # The effective upstream list — DB override if present, otherwise
    # the env-backed settings.py default. This is what the renderer
    # will use today; the UI shows it as the "current" value.
    recursive_upstreams: list[str]
    # True when the value comes from a system_settings row, false when
    # it's still the env-backed default. Lets the UI render a "reset
    # to default" affordance.
    override_active: bool
    # The env-backed default, surfaced separately so the UI can show
    # "current vs default" and explain why the reset button matters.
    default_recursive_upstreams: list[str]
    updated_at: datetime | None = None


class SystemDnsSettingsUpdate(BaseModel):
    # NULL clears the override (falls back to env default). Empty
    # list also clears — an explicit `[]` here is symmetric with the
    # per-fabric form, where leaving the textarea blank means "no
    # restriction / use the upstream default" rather than "lock out".
    recursive_upstreams: list[str] | None = None


@router.get("/system/dns-settings", response_model=SystemDnsSettingsOut)
async def get_system_dns_settings(
    _: Principal = Depends(require_capability("admin:system-settings:read")),
    db: AsyncSession = Depends(get_db),
) -> SystemDnsSettingsOut:
    row = await db.get(SystemSetting, _SYSTEM_KEY_DNS_RECURSIVE_UPSTREAMS)
    effective = await get_system_dns_upstreams(db)
    override_active = (
        row is not None
        and isinstance(row.value, list)
        and bool(row.value)
    )
    return SystemDnsSettingsOut(
        recursive_upstreams=effective,
        override_active=override_active,
        default_recursive_upstreams=list(
            get_settings().dns_recursive_upstreams,
        ),
        updated_at=row.updated_at if row is not None else None,
    )


def _normalize_upstreams(incoming: list[str] | None) -> list[str] | None:
    """Strip whitespace, drop empties, dedupe — preserving operator
    order so the rendered Corefile honors their preference. None or
    an all-empty list collapses back to None so the caller can treat
    "clear override" as a single case."""
    if incoming is None:
        return None
    seen: set[str] = set()
    out: list[str] = []
    for item in incoming:
        stripped = item.strip()
        if stripped and stripped not in seen:
            seen.add(stripped)
            out.append(stripped)
    return out or None


@router.put("/system/dns-settings", response_model=SystemDnsSettingsOut)
async def put_system_dns_settings(
    payload: SystemDnsSettingsUpdate,
    principal: Principal = Depends(
        require_capability("admin:system-settings:update"),
    ),
    db: AsyncSession = Depends(get_db),
) -> SystemDnsSettingsOut:
    """Set or clear the runtime override for `dns_recursive_upstreams`.

    A NULL or empty list deletes the row — the renderer falls back to
    the env-backed default. Otherwise upserts the row with the
    operator-supplied list, normalized to a deduped + stripped form."""
    normalized = _normalize_upstreams(payload.recursive_upstreams)
    row = await db.get(SystemSetting, _SYSTEM_KEY_DNS_RECURSIVE_UPSTREAMS)

    if normalized is None:
        if row is not None:
            await db.delete(row)
        action = "system_dns_upstreams.reset"
        metadata: dict = {}
    else:
        if row is None:
            row = SystemSetting(
                key=_SYSTEM_KEY_DNS_RECURSIVE_UPSTREAMS,
                value=normalized,
            )
            db.add(row)
        else:
            row.value = normalized
        action = "system_dns_upstreams.update"
        metadata = {"upstreams": normalized}

    await audit.record(
        db, principal, action=action,
        target_type="system_setting",
        target_id=_SYSTEM_KEY_DNS_RECURSIVE_UPSTREAMS,
        metadata=metadata,
    )
    await db.commit()
    return SystemDnsSettingsOut(
        recursive_upstreams=normalized or list(
            get_settings().dns_recursive_upstreams,
        ),
        override_active=normalized is not None,
        default_recursive_upstreams=list(
            get_settings().dns_recursive_upstreams,
        ),
        updated_at=row.updated_at if normalized is not None and row is not None else None,
    )
