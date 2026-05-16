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


class RegionDeploymentNodeCreate(BaseModel):
    """Per-node row supplied at deploy-creation time.

    `bmc_creds_secret_ref` is optional at create — the orchestrator
    can stamp it in once secrets are minted in the `secrets` stage.
    `primary_ip_v6` is similarly optional; populated by the joining
    stage when the node's actual address is known.
    """

    hostname: str
    mac: str
    bmc_address: str
    role: str  # control_plane | worker | edge
    primary_ip_v6: str | None = None
    provisioning_ip_v6: str | None = None
    bmc_creds_secret_ref: str | None = None


class RegionDeploymentCreate(BaseModel):
    """Payload for `POST /region-deployments`. Mirrors the wizard."""

    site_id: UUID
    name: str
    config: dict = Field(default_factory=dict)
    nodes: list[RegionDeploymentNodeCreate] = Field(default_factory=list)


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


class PreflightCheckOut(BaseModel):
    """Single pre-flight check result as the wizard renders it."""

    key: str
    label: str
    passed: bool
    fix_hint: str | None = None


class PreflightResponse(BaseModel):
    """Aggregated pre-flight outcome. `ready` is the hard-gate
    signal — true iff every check passed. The UI's `Start` button
    binds to this."""

    ready: bool
    checks: list[PreflightCheckOut] = Field(default_factory=list)


class RegionDeploymentEventOut(BaseModel):
    """Event-stream row. `id` is the cursor used by `?since=` catch-up."""

    model_config = ConfigDict(from_attributes=True)
    id: int
    stage: str
    level: RegionDeploymentEventLevel
    message: str
    payload: dict | None = None
    created_at: datetime
