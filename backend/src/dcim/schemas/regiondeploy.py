"""Pydantic schemas for the Region Deploy API.

Only the read-side schemas (`*Out`) are defined here for PR 2. Create/
update payloads land with PR 7 (orchestrator) and PR 13 (wizard UI),
once the shape requirements are concrete.
"""

from __future__ import annotations

from datetime import datetime
from uuid import UUID

from pydantic import BaseModel, ConfigDict, Field

from ..models.regiondeploy import (
    RegionDeploymentEventLevel,
    RegionDeploymentNodeRole,
    RegionDeploymentNodeStatus,
    RegionDeploymentServiceKind,
    RegionDeploymentServiceStatus,
    RegionDeploymentStatus,
)


class RegionDeploymentNodeOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    hostname: str
    mac: str
    primary_ip_v6: str | None = None
    provisioning_ip_v6: str | None = None
    bmc_address: str
    role: RegionDeploymentNodeRole
    status: RegionDeploymentNodeStatus
    last_event: str | None = None
    joined_at: datetime | None = None


class RegionDeploymentServiceOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    service: RegionDeploymentServiceKind
    chart_version: str | None = None
    status: RegionDeploymentServiceStatus
    last_error: str | None = None


class RegionDeploymentSummary(BaseModel):
    """List-view row: enough for the table without the full config blob."""

    model_config = ConfigDict(from_attributes=True)
    id: UUID
    site_id: UUID
    name: str
    status: RegionDeploymentStatus
    current_stage: str | None = None
    created_at: datetime
    started_at: datetime | None = None
    finished_at: datetime | None = None


class RegionDeploymentOut(BaseModel):
    """Detail view: includes config JSONB and child collections."""

    model_config = ConfigDict(from_attributes=True)
    id: UUID
    site_id: UUID
    name: str
    status: RegionDeploymentStatus
    current_stage: str | None = None
    last_error: str | None = None
    config: dict = Field(default_factory=dict)
    kubeconfig_secret_ref: str | None = None
    created_by: UUID | None = None
    created_at: datetime
    updated_at: datetime
    started_at: datetime | None = None
    finished_at: datetime | None = None
    nodes: list[RegionDeploymentNodeOut] = Field(default_factory=list)
    services: list[RegionDeploymentServiceOut] = Field(default_factory=list)


class RegionDeploymentEventOut(BaseModel):
    """Event-stream row. `id` is the cursor used by `?since=` catch-up."""

    model_config = ConfigDict(from_attributes=True)
    id: int
    stage: str
    level: RegionDeploymentEventLevel
    message: str
    payload: dict | None = None
    created_at: datetime
