"""Audit log — every enterprise-impacting action lands here."""

from __future__ import annotations

from datetime import datetime
from uuid import UUID

from sqlalchemy import JSON, DateTime, ForeignKey, Index, String
from sqlalchemy.dialects.postgresql import UUID as PgUUID
from sqlalchemy.orm import Mapped, mapped_column

from ..db import Base
from ._mixins import Timestamped, UUIDPrimaryKey


class AuditLog(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "audit_log"
    __table_args__ = (
        Index("ix_audit_user_ts", "actor_user_id", "occurred_at"),
        Index("ix_audit_target", "target_type", "target_id"),
        Index("ix_audit_action_ts", "action", "occurred_at"),
        Index("ix_audit_site_ts", "site_id", "occurred_at"),
    )

    occurred_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), nullable=False)
    actor_user_id: Mapped[UUID | None] = mapped_column(PgUUID(as_uuid=True), ForeignKey("users.id"))
    actor_token_id: Mapped[UUID | None] = mapped_column(PgUUID(as_uuid=True), ForeignKey("api_tokens.id"))
    actor_label: Mapped[str | None] = mapped_column(String(255))
    actor_ip: Mapped[str | None] = mapped_column(String(64))

    action: Mapped[str] = mapped_column(String(64), nullable=False)  # e.g. asset.update, power.cycle
    target_type: Mapped[str | None] = mapped_column(String(64))
    target_id: Mapped[str | None] = mapped_column(String(64))
    site_id: Mapped[UUID | None] = mapped_column(PgUUID(as_uuid=True), ForeignKey("sites.id"))

    request_id: Mapped[str | None] = mapped_column(String(64))
    success: Mapped[bool] = mapped_column(default=True, nullable=False)
    diff_json: Mapped[dict] = mapped_column(JSON, default=dict, nullable=False)
    metadata_json: Mapped[dict] = mapped_column(JSON, default=dict, nullable=False)
