"""Per-fabric DNS recursive upstreams.

NULL means "use the system-wide dns_recursive_upstreams setting"
(unchanged behavior). When set, the recursive Corefile renderer
prefers the per-fabric list for that fabric's recursive server.

Revision ID: 20260511_0026
Revises: 20260511_0025
Create Date: 2026-05-11
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260511_0026"
down_revision: str | None = "20260511_0025"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute("ALTER TABLE fabrics ADD COLUMN dns_recursive_upstreams JSON")


def downgrade() -> None:
    op.execute("ALTER TABLE fabrics DROP COLUMN IF EXISTS dns_recursive_upstreams")
