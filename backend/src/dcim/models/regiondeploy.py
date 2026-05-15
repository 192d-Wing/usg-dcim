"""Region deployment workflow models.

Persistence for the Region Deploy feature (see docs/dev/region-deploy.md):
a wizard-driven workstream that provisions a new Kubernetes cluster on
bare-metal at a site and rolls out the site service stack (auth DNS,
recursive DNS, DHCP, collector).

Four tables form the unit of work:

  RegionDeployment         — top-level row per deploy; owns the config JSONB
                             and the kubeconfig-secret reference.
  RegionDeploymentNode     — bare-metal nodes participating in the deploy.
                             BMC creds and (eventually) the kubeconfig are
                             stored as k8s Secrets on central; this row
                             only holds the namespace/name reference.
  RegionDeploymentEvent    — append-only event/log stream backing SSE; the
                             bigint sequence id is what UIs use for catch-up
                             (`?since=<id>`).
  RegionDeploymentService  — per-Helm-release install state.
"""

from __future__ import annotations

import enum
from datetime import datetime
from uuid import UUID

from sqlalchemy import (
    BigInteger,
    DateTime,
    Enum,
    ForeignKey,
    Index,
    String,
    Text,
    UniqueConstraint,
    func,
)
from sqlalchemy.dialects.postgresql import INET, JSONB, MACADDR
from sqlalchemy.dialects.postgresql import UUID as PgUUID
from sqlalchemy.orm import Mapped, mapped_column, relationship

from ..db import Base
from ._mixins import Timestamped, UUIDPrimaryKey


class RegionDeploymentStatus(str, enum.Enum):
    pending = "pending"
    preflight = "preflight"
    provisioning = "provisioning"
    joining = "joining"
    cni = "cni"
    apps = "apps"
    verify = "verify"
    ready = "ready"
    failed = "failed"
    aborted = "aborted"


class RegionDeploymentNodeRole(str, enum.Enum):
    control_plane = "control_plane"
    worker = "worker"
    edge = "edge"


class RegionDeploymentNodeStatus(str, enum.Enum):
    pending = "pending"
    pxe_boot = "pxe_boot"
    installing = "installing"
    joining = "joining"
    ready = "ready"
    failed = "failed"


class RegionDeploymentEventLevel(str, enum.Enum):
    info = "info"
    warn = "warn"
    error = "error"


class RegionDeploymentServiceKind(str, enum.Enum):
    dns_auth = "dns_auth"
    dns_recursive = "dns_recursive"
    dhcp = "dhcp"
    collector = "collector"


class RegionDeploymentServiceStatus(str, enum.Enum):
    pending = "pending"
    installing = "installing"
    ready = "ready"
    failed = "failed"


def _enum(py_enum: type[enum.Enum], pg_name: str) -> Enum:
    """Bind a Python enum to its server-side ENUM type by name. The type
    itself is created by the migration; we never want SQLAlchemy to
    auto-create it on model import."""

    return Enum(
        py_enum, name=pg_name, create_type=False,
        values_callable=lambda x: [e.value for e in x],
    )


class RegionDeployment(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "region_deployments"
    __table_args__ = (
        Index("ix_region_deployments_site", "site_id"),
        Index("ix_region_deployments_status", "status"),
    )

    site_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True),
        ForeignKey("sites.id", ondelete="RESTRICT"),
        nullable=False,
    )
    name: Mapped[str] = mapped_column(String(128), nullable=False)
    status: Mapped[RegionDeploymentStatus] = mapped_column(
        _enum(RegionDeploymentStatus, "region_deployment_status"),
        nullable=False, default=RegionDeploymentStatus.pending,
    )
    # Machine-readable stage key the worker is currently executing;
    # mirrors the state-machine keys in docs/dev/region-deploy.md §6.
    current_stage: Mapped[str | None] = mapped_column(String(64))
    last_error: Mapped[str | None] = mapped_column(Text)
    # See docs §4 for the config JSONB schema (CIDRs, BGP peers, edge mode,
    # selected_services). Kept untyped here — Pydantic does the shape work.
    config: Mapped[dict] = mapped_column(JSONB, nullable=False, default=dict)
    # `namespace/name` of the k8s Secret on central holding the cluster's
    # kubeconfig. Populated by the `finalize` stage; NULL until then.
    kubeconfig_secret_ref: Mapped[str | None] = mapped_column(String(255))
    created_by: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("users.id", ondelete="SET NULL"),
    )
    started_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    finished_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))

    nodes: Mapped[list[RegionDeploymentNode]] = relationship(
        back_populates="deployment", cascade="all, delete-orphan",
    )
    services: Mapped[list[RegionDeploymentService]] = relationship(
        back_populates="deployment", cascade="all, delete-orphan",
    )


class RegionDeploymentNode(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "region_deployment_nodes"
    __table_args__ = (
        UniqueConstraint("deployment_id", "mac", name="uq_rdn_deployment_mac"),
        UniqueConstraint(
            "deployment_id", "hostname", name="uq_rdn_deployment_hostname",
        ),
        Index("ix_region_deployment_nodes_deployment", "deployment_id"),
    )

    deployment_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True),
        ForeignKey("region_deployments.id", ondelete="CASCADE"),
        nullable=False,
    )
    hostname: Mapped[str] = mapped_column(String(255), nullable=False)
    mac: Mapped[str] = mapped_column(MACADDR, nullable=False)
    # Primary management address on the production VLAN. Set by the worker
    # after the node joins the cluster.
    primary_ip_v6: Mapped[str | None] = mapped_column(INET)
    # DHCPv6 lease address assigned by Smee during UEFI HTTP Boot.
    provisioning_ip_v6: Mapped[str | None] = mapped_column(INET)
    bmc_address: Mapped[str] = mapped_column(INET, nullable=False)
    # `namespace/name` of the k8s Secret with `bmc-username`/`bmc-password`
    # keys. Created at deploy-start; deleted on abort if never reached ready.
    bmc_creds_secret_ref: Mapped[str | None] = mapped_column(String(255))
    role: Mapped[RegionDeploymentNodeRole] = mapped_column(
        _enum(RegionDeploymentNodeRole, "region_deployment_node_role"),
        nullable=False,
    )
    status: Mapped[RegionDeploymentNodeStatus] = mapped_column(
        _enum(RegionDeploymentNodeStatus, "region_deployment_node_status"),
        nullable=False, default=RegionDeploymentNodeStatus.pending,
    )
    last_event: Mapped[str | None] = mapped_column(Text)
    joined_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))

    deployment: Mapped[RegionDeployment] = relationship(back_populates="nodes")


class RegionDeploymentEvent(Base):
    """Append-only event/log row.

    Doesn't use UUIDPrimaryKey — SSE catch-up relies on a monotonic
    bigint sequence (`?since=<id>` returns rows with id > since,
    ordered). UUIDs would require a separate ordering column.
    """

    __tablename__ = "region_deployment_events"
    __table_args__ = (
        Index(
            "ix_region_deployment_events_deployment_id",
            "deployment_id", "id",
        ),
    )

    id: Mapped[int] = mapped_column(BigInteger, primary_key=True, autoincrement=True)
    deployment_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True),
        ForeignKey("region_deployments.id", ondelete="CASCADE"),
        nullable=False,
    )
    stage: Mapped[str] = mapped_column(String(64), nullable=False)
    level: Mapped[RegionDeploymentEventLevel] = mapped_column(
        _enum(RegionDeploymentEventLevel, "region_deployment_event_level"),
        nullable=False, default=RegionDeploymentEventLevel.info,
    )
    message: Mapped[str] = mapped_column(Text, nullable=False)
    payload: Mapped[dict | None] = mapped_column(JSONB)
    created_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), server_default=func.now(), nullable=False,
    )


class RegionDeploymentService(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "region_deployment_services"
    __table_args__ = (
        UniqueConstraint(
            "deployment_id", "service", name="uq_rds_deployment_service",
        ),
        Index(
            "ix_region_deployment_services_deployment", "deployment_id",
        ),
    )

    deployment_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True),
        ForeignKey("region_deployments.id", ondelete="CASCADE"),
        nullable=False,
    )
    service: Mapped[RegionDeploymentServiceKind] = mapped_column(
        _enum(RegionDeploymentServiceKind, "region_deployment_service_kind"),
        nullable=False,
    )
    chart_version: Mapped[str | None] = mapped_column(String(64))
    values_override: Mapped[dict] = mapped_column(
        JSONB, nullable=False, default=dict,
    )
    status: Mapped[RegionDeploymentServiceStatus] = mapped_column(
        _enum(RegionDeploymentServiceStatus, "region_deployment_service_status"),
        nullable=False, default=RegionDeploymentServiceStatus.pending,
    )
    last_error: Mapped[str | None] = mapped_column(Text)

    deployment: Mapped[RegionDeployment] = relationship(back_populates="services")
