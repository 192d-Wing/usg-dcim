"""Conditional per-zone forwarders for the recursive CoreDNS.

One row per (fabric, zone-pattern) combination — the recursive
Corefile picks them up and emits a `<pattern>:53 { forward . … }` block
ahead of the catch-all `.:53` block.

Revision ID: 20260511_0017
Revises: 20260511_0016
Create Date: 2026-05-11
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260511_0017"
down_revision: str | None = "20260511_0016"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        """
        CREATE TABLE dns_forwarders (
            id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
            created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            name          VARCHAR(128) NOT NULL,
            fabric_id     UUID NOT NULL REFERENCES fabrics(id) ON DELETE CASCADE,
            zone_pattern  VARCHAR(253) NOT NULL,
            upstreams     JSON NOT NULL DEFAULT '[]'::json,
            description   VARCHAR(512),
            CONSTRAINT uq_dns_forwarder_fabric_zone UNIQUE (fabric_id, zone_pattern)
        )
        """
    )
    op.execute(
        "CREATE INDEX ix_dns_forwarders_fabric ON dns_forwarders (fabric_id)"
    )


def downgrade() -> None:
    op.execute("DROP TABLE IF EXISTS dns_forwarders")
