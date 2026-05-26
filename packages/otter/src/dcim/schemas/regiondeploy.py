"""Pydantic schemas for the Region Deploy API.

Only the read-side schemas (`*Out`) are defined here for PR 2. Create/
update payloads land with PR 7 (orchestrator) and PR 13 (wizard UI),
once the shape requirements are concrete.
"""

from __future__ import annotations

from datetime import datetime
from uuid import UUID

import base64

from pydantic import BaseModel, ConfigDict, Field, model_validator

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


class RegionDeploymentKubeconfigCallback(BaseModel):
    """Payload the Tink Worker posts back to central after `kubeadm
    init` succeeds on the first control-plane node.

    The Workflow template's `kubeconfig-write` action (see
    docs/dev/region-deploy.md §3a workstream) reads
    `/etc/kubernetes/admin.conf` post-init and POSTs it here, where
    central stamps it into the deployment row so the orchestrator's
    `joining` stage can pick it up.

    Today the endpoint only records receipt — actually persisting
    the kubeconfig as a k8s Secret needs the central-cluster RBAC +
    k8s-client work that's still pending (same blocker as the apply
    path for stages 8/9/10).
    """

    # node_id: which node wrote it. Lets central correlate the
    # callback to a specific RegionDeploymentNode in case multiple
    # control planes try to write.
    node_id: UUID
    # kubeconfig: full YAML content of /etc/kubernetes/admin.conf.
    # Treated as opaque on the central side; the orchestrator uses
    # it as a kubeconfig file for client-go / kubectl wrapping.
    # Optional when `kubeconfig_b64` is supplied — the in-cluster
    # bash unit that POSTs the callback can avoid YAML→JSON escaping
    # by base64-encoding the file instead.
    kubeconfig: str | None = None
    # Base64-encoded alternative to `kubeconfig`. Exactly one of the
    # two fields must be present. The validator decodes this into
    # `kubeconfig` so handlers downstream only ever see the plain
    # YAML string.
    kubeconfig_b64: str | None = None

    @model_validator(mode="after")
    def _normalize_kubeconfig(self) -> RegionDeploymentKubeconfigCallback:
        if self.kubeconfig is not None and self.kubeconfig_b64 is not None:
            raise ValueError("supply either kubeconfig or kubeconfig_b64, not both")
        if self.kubeconfig is None and self.kubeconfig_b64 is None:
            raise ValueError("kubeconfig or kubeconfig_b64 is required")
        if self.kubeconfig_b64 is not None:
            try:
                decoded = base64.b64decode(self.kubeconfig_b64, validate=True)
            except ValueError as exc:
                raise ValueError(f"kubeconfig_b64 is not valid base64: {exc}") from exc
            self.kubeconfig = decoded.decode("utf-8")
            self.kubeconfig_b64 = None
        return self


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
