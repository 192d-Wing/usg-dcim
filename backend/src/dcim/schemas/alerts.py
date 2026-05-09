from __future__ import annotations

from datetime import datetime
from uuid import UUID

from pydantic import BaseModel, ConfigDict, Field

from ..models.alerts import AlertState, Severity


class AlertRuleBase(BaseModel):
    name: str
    description: str | None = None
    metric: str
    operator: str
    threshold: float
    duration_seconds: int = 60
    severity: Severity
    site_scope_id: UUID | None = None
    asset_filter_json: dict = Field(default_factory=dict)
    enabled: bool = True
    runbook_url: str | None = None


class AlertRuleCreate(AlertRuleBase):
    pass


class AlertRuleUpdate(BaseModel):
    name: str | None = None
    description: str | None = None
    metric: str | None = None
    operator: str | None = None
    threshold: float | None = None
    duration_seconds: int | None = None
    severity: Severity | None = None
    site_scope_id: UUID | None = None
    asset_filter_json: dict | None = None
    enabled: bool | None = None
    runbook_url: str | None = None


class AlertRuleOut(AlertRuleBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


class AlertOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    rule_id: UUID | None
    site_id: UUID
    asset_id: UUID | None
    collector_id: UUID | None
    severity: Severity
    state: AlertState
    summary: str
    detail: str | None
    first_seen_at: datetime
    last_seen_at: datetime
    acked_by: str | None
    acked_at: datetime | None
    resolved_at: datetime | None
    labels_json: dict


class AlertAck(BaseModel):
    note: str | None = None


class MaintenanceWindowBase(BaseModel):
    name: str
    site_id: UUID | None = None
    asset_filter_json: dict = Field(default_factory=dict)
    starts_at: datetime
    ends_at: datetime
    reason: str | None = None


class MaintenanceWindowCreate(MaintenanceWindowBase):
    pass


class MaintenanceWindowUpdate(BaseModel):
    name: str | None = None
    site_id: UUID | None = None
    asset_filter_json: dict | None = None
    starts_at: datetime | None = None
    ends_at: datetime | None = None
    reason: str | None = None


class MaintenanceWindowOut(MaintenanceWindowBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_by: str | None
    created_at: datetime
    updated_at: datetime
