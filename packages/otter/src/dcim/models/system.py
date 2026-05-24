"""Deployment-wide configuration rows.

`system_settings` is a key-value table that holds settings operators
can edit at runtime — overrides for the env-backed defaults in
`settings.py`. Each row's value is a free-shape JSON blob; the access
helpers in `services/` know the expected shape per key.

Today's keys:
  - `dns_recursive_upstreams` — list[str] of "ip" or "ip:port" entries.
    Fabric-level override still wins; this beats the settings.py default.

New settings land here rather than as `DCIM_*` env vars when an
operator should be able to flip them through the UI without a
redeploy.
"""

from __future__ import annotations

from datetime import datetime

from sqlalchemy import JSON, DateTime, String, func
from sqlalchemy.orm import Mapped, mapped_column

from ..db import Base


class SystemSetting(Base):
    __tablename__ = "system_settings"

    key: Mapped[str] = mapped_column(String(64), primary_key=True)
    value: Mapped[dict | list | None] = mapped_column(JSON, nullable=True)
    updated_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True),
        server_default=func.now(),
        onupdate=func.now(),
        nullable=False,
    )
