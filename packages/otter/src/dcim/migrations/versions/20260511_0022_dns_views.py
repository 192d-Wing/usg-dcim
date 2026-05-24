"""Split-horizon DNS views.

dns_views per fabric + DnsRecord.view_id FK. Records bound to a view
are only emitted in that view's zone block at render time; records
with view_id IS NULL are the fallback served to clients that don't
match any view's CIDR list.

Revision ID: 20260511_0022
Revises: 20260511_0021
Create Date: 2026-05-11
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260511_0022"
down_revision: str | None = "20260511_0021"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        """
        CREATE TABLE dns_views (
            id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
            created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            name         VARCHAR(64) NOT NULL,
            fabric_id    UUID NOT NULL REFERENCES fabrics(id) ON DELETE CASCADE,
            match_cidrs  JSON NOT NULL DEFAULT '[]'::json,
            priority     INTEGER NOT NULL DEFAULT 100,
            description  VARCHAR(512),
            CONSTRAINT uq_dns_view_fabric_name UNIQUE (fabric_id, name)
        )
        """
    )
    op.execute(
        "CREATE INDEX ix_dns_views_fabric ON dns_views (fabric_id)"
    )
    op.execute(
        "ALTER TABLE dns_records ADD COLUMN view_id UUID "
        "REFERENCES dns_views(id) ON DELETE SET NULL"
    )
    op.execute(
        "CREATE INDEX ix_dns_records_view ON dns_records (view_id)"
    )


def downgrade() -> None:
    op.execute("DROP INDEX IF EXISTS ix_dns_records_view")
    op.execute("ALTER TABLE dns_records DROP COLUMN IF EXISTS view_id")
    op.execute("DROP TABLE IF EXISTS dns_views")
