"""Per-fabric recursive DNS engine selector (CoreDNS or Hickory).

Step 1 of the Hickory migration plan — only the recursive pod moves;
authoritative stays on CoreDNS because the features we ship there
(views, on-the-fly DNSSEC signing, regex-template RPZ) don't port.
Default coredns so existing fabrics keep their current behavior.

Revision ID: 20260511_0027
Revises: 20260511_0026
Create Date: 2026-05-11
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260511_0027"
down_revision: str | None = "20260511_0026"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        "CREATE TYPE recursive_dns_engine AS ENUM ('coredns', 'hickory')"
    )
    op.execute(
        "ALTER TABLE fabrics ADD COLUMN recursive_engine recursive_dns_engine "
        "NOT NULL DEFAULT 'coredns'"
    )


def downgrade() -> None:
    op.execute("ALTER TABLE fabrics DROP COLUMN IF EXISTS recursive_engine")
    op.execute("DROP TYPE IF EXISTS recursive_dns_engine")
