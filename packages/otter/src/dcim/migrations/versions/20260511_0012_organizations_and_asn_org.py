"""Organizations registry + Asn.organization → organization_id FK +
TCP AO key lifetimes.

Adds:
  - organizations table (ARIN-shaped fields)
  - bgp_asns.organization_id (FK, replaces the free-string `organization`)
  - tcp_ao_keys.valid_from / valid_to (timestamptz, nullable)

The Asn.organization string column is dropped without preservation —
no existing rows on the migration path carry meaningful data here.

Revision ID: 20260511_0012
Revises: 20260511_0011
Create Date: 2026-05-11
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260511_0012"
down_revision: str | None = "20260511_0011"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    # ----- Organizations -----
    op.execute(
        """
        CREATE TABLE IF NOT EXISTS organizations (
            id UUID PRIMARY KEY,
            name VARCHAR(256) NOT NULL,
            arin_org_id VARCHAR(64),

            address_line1 VARCHAR(256) NOT NULL,
            address_line2 VARCHAR(256),
            city VARCHAR(128) NOT NULL,
            state_province VARCHAR(64),
            postal_code VARCHAR(32),
            country VARCHAR(2) NOT NULL,

            phone VARCHAR(64),
            email VARCHAR(256),

            admin_poc_name VARCHAR(128) NOT NULL,
            admin_poc_email VARCHAR(256) NOT NULL,
            admin_poc_phone VARCHAR(64),

            tech_poc_name VARCHAR(128) NOT NULL,
            tech_poc_email VARCHAR(256) NOT NULL,
            tech_poc_phone VARCHAR(64),

            abuse_poc_name VARCHAR(128) NOT NULL,
            abuse_poc_email VARCHAR(256) NOT NULL,
            abuse_poc_phone VARCHAR(64),

            noc_poc_name VARCHAR(128),
            noc_poc_email VARCHAR(256),
            noc_poc_phone VARCHAR(64),

            description VARCHAR(512),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
        )
        """
    )

    # ----- Asn: drop free-string organization, add FK to organizations -----
    op.execute("ALTER TABLE bgp_asns DROP COLUMN IF EXISTS organization")
    op.execute(
        "ALTER TABLE bgp_asns ADD COLUMN IF NOT EXISTS organization_id "
        "UUID REFERENCES organizations(id)"
    )

    # ----- TcpAoKey: add lifetime window -----
    op.execute(
        "ALTER TABLE tcp_ao_keys ADD COLUMN IF NOT EXISTS valid_from TIMESTAMPTZ"
    )
    op.execute(
        "ALTER TABLE tcp_ao_keys ADD COLUMN IF NOT EXISTS valid_to TIMESTAMPTZ"
    )


def downgrade() -> None:
    op.execute("ALTER TABLE tcp_ao_keys DROP COLUMN IF EXISTS valid_to")
    op.execute("ALTER TABLE tcp_ao_keys DROP COLUMN IF EXISTS valid_from")
    op.execute("ALTER TABLE bgp_asns DROP COLUMN IF EXISTS organization_id")
    op.execute("ALTER TABLE bgp_asns ADD COLUMN IF NOT EXISTS organization VARCHAR(256)")
    op.execute("DROP TABLE IF EXISTS organizations")
