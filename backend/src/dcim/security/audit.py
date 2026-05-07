"""Append an audit_log row inside the request transaction."""

from __future__ import annotations

from datetime import UTC, datetime
from uuid import UUID

import structlog
from sqlalchemy.ext.asyncio import AsyncSession

from ..models.audit import AuditLog
from .deps import Principal

log = structlog.get_logger("dcim.audit")


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
        diff_json=diff or {},
        metadata_json=metadata or {},
    )
    db.add(row)
    log.info("audit", action=action, target=f"{target_type}:{target_id}", actor=principal.label, success=success)
