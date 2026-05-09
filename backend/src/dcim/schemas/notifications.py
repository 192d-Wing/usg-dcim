"""Pydantic schemas for notification channels."""

from __future__ import annotations

from datetime import datetime
from uuid import UUID

from pydantic import BaseModel, ConfigDict, Field

from ..models.alerts import Severity
from ..models.notifications import ChannelKind


class NotificationChannelBase(BaseModel):
    name: str
    kind: ChannelKind
    config_json: dict = Field(default_factory=dict)
    min_severity: Severity = Severity.warning
    notify_on_fire: bool = True
    notify_on_resolve: bool = True
    enabled: bool = True
    description: str | None = None


class NotificationChannelCreate(NotificationChannelBase):
    pass


class NotificationChannelUpdate(BaseModel):
    name: str | None = None
    config_json: dict | None = None
    min_severity: Severity | None = None
    notify_on_fire: bool | None = None
    notify_on_resolve: bool | None = None
    enabled: bool | None = None
    description: str | None = None


class NotificationChannelOut(NotificationChannelBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime
