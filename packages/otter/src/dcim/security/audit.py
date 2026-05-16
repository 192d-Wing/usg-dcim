"""Append an audit_log row inside the request transaction."""

from __future__ import annotations

import enum
from datetime import UTC, date, datetime
from typing import Any
from uuid import UUID

import structlog
from sqlalchemy.ext.asyncio import AsyncSession

from ..models.audit import AuditLog
from .deps import Principal

log = structlog.get_logger("dcim.audit")


def _json_safe(value: Any) -> Any:
    """Coerce values into types the JSON column can store.

    Pydantic's `model_dump(exclude_unset=True)` returns Python objects —
    UUIDs are still UUID instances, datetimes are still datetime, enums
    are still enum members. The JSON column tries to serialize the dict
    via the stdlib encoder which doesn't know any of those types and
    blows up at write time. Normalize once here so every audit caller
    can hand us whatever shape comes out of model_dump."""
    if isinstance(value, dict):
        return {str(k): _json_safe(v) for k, v in value.items()}
    if isinstance(value, (list, tuple, set, frozenset)):
        return [_json_safe(v) for v in value]
    if isinstance(value, UUID):
        return str(value)
    if isinstance(value, (datetime, date)):
        return value.isoformat()
    if isinstance(value, enum.Enum):
        return value.value
    return value


async def record(
    db: AsyncSession,
    principal: Principal,
    *,
    action: str,
    target_type: str | None = None,
    target_id: str | None = None,
    site_id: UUID | None = None,
    success: bool = True,
    diff: dict | None = None,
    metadata: dict | None = None,
    request_id: str | None = None,
) -> None:
    row = AuditLog(
        occurred_at=datetime.now(UTC),
        actor_user_id=principal.user.id if principal.user else None,
        actor_token_id=principal.token.id if principal.token else None,
        actor_label=principal.label,
        actor_ip=principal.ip,
        action=action,
        target_type=target_type,
        target_id=target_id,
        site_id=site_id,
        request_id=request_id,
        success=success,
        diff_json=_json_safe(diff or {}),
        metadata_json=_json_safe(metadata or {}),
    )
    db.add(row)
    log.info(
        "audit", action=action, target=f"{target_type}:{target_id}",
        actor=principal.label, success=success,
    )
