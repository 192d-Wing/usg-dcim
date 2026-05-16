"""Region deployment tables.

Adds the persistence layer for the Region Deploy workflow (see
docs/dev/region-deploy.md): a wizard-driven workstream that provisions a
new Kubernetes cluster on bare-metal at a site and rolls out the site
service stack (auth DNS, recursive DNS, DHCP, collector).

Tables:
  - region_deployments         — top-level row per deploy
  - region_deployment_nodes    — bare-metal nodes participating in a deploy
  - region_deployment_events   — append-only event/log stream (backs SSE)
  - region_deployment_services — per-service install state (Helm releases)

IPAM is intentionally NOT touched — the existing CIDR/INET columns are
already address-family-agnostic. Any pre-flight-specific tagging is
deferred to PR 6 when concrete query needs surface.
"""

from collections.abc import Sequence

import sqlalchemy as sa
from alembic import op
from sqlalchemy.dialects import postgresql

revision: str = "20260515_0048"
down_revision: str | None = "20260513_0047"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


DEPLOYMENT_STATUS = (
    "pending",
    "preflight",
    "provisioning",
    "joining",
    "cni",
    "apps",
    "verify",
    "ready",
    "failed",
    "aborted",
)
NODE_ROLE = ("control_plane", "worker", "edge")
NODE_STATUS = ("pending", "pxe_boot", "installing", "joining", "ready", "failed")
EVENT_LEVEL = ("info", "warn", "error")
SERVICE_KIND = ("dns_auth", "dns_recursive", "dhcp", "collector")
SERVICE_STATUS = ("pending", "installing", "ready", "failed")


def upgrade() -> None:
    deployment_status = postgresql.ENUM(
        *DEPLOYMENT_STATUS, name="region_deployment_status", create_type=False,
    )
    node_role = postgresql.ENUM(
        *NODE_ROLE, name="region_deployment_node_role", create_type=False,
    )
    node_status = postgresql.ENUM(
        *NODE_STATUS, name="region_deployment_node_status", create_type=False,
    )
    event_level = postgresql.ENUM(
        *EVENT_LEVEL, name="region_deployment_event_level", create_type=False,
    )
    service_kind = postgresql.ENUM(
        *SERVICE_KIND, name="region_deployment_service_kind", create_type=False,
    )
    service_status = postgresql.ENUM(
        *SERVICE_STATUS, name="region_deployment_service_status", create_type=False,
    )
    bind = op.get_bind()
    for enum in (
        deployment_status, node_role, node_status,
        event_level, service_kind, service_status,
    ):
        enum.create(bind, checkfirst=False)

    op.create_table(
        "region_deployments",
        sa.Column("id", postgresql.UUID(as_uuid=True), primary_key=True,
                  server_default=sa.text("gen_random_uuid()")),
        sa.Column("site_id", postgresql.UUID(as_uuid=True),
                  sa.ForeignKey("sites.id", ondelete="RESTRICT"), nullable=False),
        sa.Column("name", sa.String(128), nullable=False),
        sa.Column("status", deployment_status, nullable=False,
                  server_default=sa.text("'pending'::region_deployment_status")),
        sa.Column("current_stage", sa.String(64), nullable=True),
        sa.Column("last_error", sa.Text(), nullable=True),
        sa.Column("config", postgresql.JSONB(astext_type=sa.Text()),
                  nullable=False, server_default=sa.text("'{}'::jsonb")),
        sa.Column("kubeconfig_secret_ref", sa.String(255), nullable=True),
        sa.Column("created_by", postgresql.UUID(as_uuid=True),
                  sa.ForeignKey("users.id", ondelete="SET NULL"), nullable=True),
        sa.Column("created_at", sa.DateTime(timezone=True),
                  nullable=False, server_default=sa.text("now()")),
        sa.Column("updated_at", sa.DateTime(timezone=True),
                  nullable=False, server_default=sa.text("now()")),
        sa.Column("started_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("finished_at", sa.DateTime(timezone=True), nullable=True),
    )
    op.create_index(
        "ix_region_deployments_site", "region_deployments", ["site_id"],
    )
    op.create_index(
        "ix_region_deployments_status", "region_deployments", ["status"],
    )

    op.create_table(
        "region_deployment_nodes",
        sa.Column("id", postgresql.UUID(as_uuid=True), primary_key=True,
                  server_default=sa.text("gen_random_uuid()")),
        sa.Column("deployment_id", postgresql.UUID(as_uuid=True),
                  sa.ForeignKey("region_deployments.id", ondelete="CASCADE"),
                  nullable=False),
        sa.Column("hostname", sa.String(255), nullable=False),
        sa.Column("mac", postgresql.MACADDR(), nullable=False),
        sa.Column("primary_ip_v6", postgresql.INET(), nullable=True),
        sa.Column("provisioning_ip_v6", postgresql.INET(), nullable=True),
        sa.Column("bmc_address", postgresql.INET(), nullable=False),
        sa.Column("bmc_creds_secret_ref", sa.String(255), nullable=True),
        sa.Column("role", node_role, nullable=False),
        sa.Column("status", node_status, nullable=False,
                  server_default=sa.text("'pending'::region_deployment_node_status")),
        sa.Column("last_event", sa.Text(), nullable=True),
        sa.Column("created_at", sa.DateTime(timezone=True),
                  nullable=False, server_default=sa.text("now()")),
        sa.Column("updated_at", sa.DateTime(timezone=True),
                  nullable=False, server_default=sa.text("now()")),
        sa.Column("joined_at", sa.DateTime(timezone=True), nullable=True),
        sa.UniqueConstraint("deployment_id", "mac", name="uq_rdn_deployment_mac"),
        sa.UniqueConstraint(
            "deployment_id", "hostname", name="uq_rdn_deployment_hostname",
        ),
    )
    op.create_index(
        "ix_region_deployment_nodes_deployment",
        "region_deployment_nodes", ["deployment_id"],
    )

    op.create_table(
        "region_deployment_events",
        sa.Column("id", sa.BigInteger(), primary_key=True, autoincrement=True),
        sa.Column("deployment_id", postgresql.UUID(as_uuid=True),
                  sa.ForeignKey("region_deployments.id", ondelete="CASCADE"),
                  nullable=False),
        sa.Column("stage", sa.String(64), nullable=False),
        sa.Column("level", event_level, nullable=False,
                  server_default=sa.text("'info'::region_deployment_event_level")),
        sa.Column("message", sa.Text(), nullable=False),
        sa.Column("payload", postgresql.JSONB(astext_type=sa.Text()), nullable=True),
        sa.Column("created_at", sa.DateTime(timezone=True),
                  nullable=False, server_default=sa.text("now()")),
    )
    # Composite index for SSE catch-up: `WHERE deployment_id = ? AND id > ?
    # ORDER BY id`.
    op.create_index(
        "ix_region_deployment_events_deployment_id",
        "region_deployment_events", ["deployment_id", "id"],
    )

    op.create_table(
        "region_deployment_services",
        sa.Column("id", postgresql.UUID(as_uuid=True), primary_key=True,
                  server_default=sa.text("gen_random_uuid()")),
        sa.Column("deployment_id", postgresql.UUID(as_uuid=True),
                  sa.ForeignKey("region_deployments.id", ondelete="CASCADE"),
                  nullable=False),
        sa.Column("service", service_kind, nullable=False),
        sa.Column("chart_version", sa.String(64), nullable=True),
        sa.Column("values_override", postgresql.JSONB(astext_type=sa.Text()),
                  nullable=False, server_default=sa.text("'{}'::jsonb")),
        sa.Column("status", service_status, nullable=False,
                  server_default=sa.text("'pending'::region_deployment_service_status")),
        sa.Column("last_error", sa.Text(), nullable=True),
        sa.Column("created_at", sa.DateTime(timezone=True),
                  nullable=False, server_default=sa.text("now()")),
        sa.Column("updated_at", sa.DateTime(timezone=True),
                  nullable=False, server_default=sa.text("now()")),
        sa.UniqueConstraint(
            "deployment_id", "service", name="uq_rds_deployment_service",
        ),
    )
    op.create_index(
        "ix_region_deployment_services_deployment",
        "region_deployment_services", ["deployment_id"],
    )


def downgrade() -> None:
    op.drop_index(
        "ix_region_deployment_services_deployment",
        table_name="region_deployment_services",
    )
    op.drop_table("region_deployment_services")
    op.drop_index(
        "ix_region_deployment_events_deployment_id",
        table_name="region_deployment_events",
    )
    op.drop_table("region_deployment_events")
    op.drop_index(
        "ix_region_deployment_nodes_deployment",
        table_name="region_deployment_nodes",
    )
    op.drop_table("region_deployment_nodes")
    op.drop_index("ix_region_deployments_status", table_name="region_deployments")
    op.drop_index("ix_region_deployments_site", table_name="region_deployments")
    op.drop_table("region_deployments")
    bind = op.get_bind()
    for name in (
        "region_deployment_service_status",
        "region_deployment_service_kind",
        "region_deployment_event_level",
        "region_deployment_node_status",
        "region_deployment_node_role",
        "region_deployment_status",
    ):
        postgresql.ENUM(name=name).drop(bind, checkfirst=False)
