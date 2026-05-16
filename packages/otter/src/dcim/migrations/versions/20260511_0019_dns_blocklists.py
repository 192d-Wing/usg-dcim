"""Response-Policy-Zones-lite: per-fabric blocklists + entries.

Blocklists carry a single action (block → NXDOMAIN, sinkhole → captive
IP). Patterns inside a blocklist roll into one CoreDNS `template`
block in the recursive Corefile, scoped by the fabric whose recursive
servers consume the bundle.

Revision ID: 20260511_0019
Revises: 20260511_0018
Create Date: 2026-05-11
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260511_0019"
down_revision: str | None = "20260511_0018"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        "CREATE TYPE dns_blocklist_action AS ENUM ('block', 'sinkhole')"
    )
    op.execute(
        """
        CREATE TABLE dns_blocklists (
            id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
            created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            name         VARCHAR(128) NOT NULL,
            fabric_id    UUID NOT NULL REFERENCES fabrics(id) ON DELETE CASCADE,
            action       dns_blocklist_action NOT NULL,
            sink_ipv4    INET,
            sink_ipv6    INET,
            enabled      BOOLEAN NOT NULL DEFAULT TRUE,
            description  VARCHAR(512),
            CONSTRAINT uq_dns_blocklist_fabric_name UNIQUE (fabric_id, name)
        )
        """
    )
    op.execute(
        "CREATE INDEX ix_dns_blocklists_fabric ON dns_blocklists (fabric_id)"
    )
    op.execute(
        """
        CREATE TABLE dns_blocklist_entries (
            id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
            created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            blocklist_id  UUID NOT NULL REFERENCES dns_blocklists(id) ON DELETE CASCADE,
            pattern       VARCHAR(253) NOT NULL,
            description   VARCHAR(512),
            CONSTRAINT uq_dns_blocklist_entry_pattern UNIQUE (blocklist_id, pattern)
        )
        """
    )
    op.execute(
        "CREATE INDEX ix_dns_blocklist_entries_blocklist "
        "ON dns_blocklist_entries (blocklist_id)"
    )


def downgrade() -> None:
    op.execute("DROP TABLE IF EXISTS dns_blocklist_entries")
    op.execute("DROP TABLE IF EXISTS dns_blocklists")
    op.execute("DROP TYPE IF EXISTS dns_blocklist_action")
