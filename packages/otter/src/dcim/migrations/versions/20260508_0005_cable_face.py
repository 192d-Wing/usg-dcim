"""Cable face column.

Cables record which face of the rack they run on (front | rear). Stored as
VARCHAR rather than the asset_face enum so we can extend with values like
`top` (overhead cable trays) without an enum migration.

Revision ID: 20260508_0005
Revises: 20260508_0004
Create Date: 2026-05-08
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260508_0005"
down_revision: str | None = "20260508_0004"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute("ALTER TABLE cables ADD COLUMN IF NOT EXISTS face VARCHAR(8)")


def downgrade() -> None:
    op.execute("ALTER TABLE cables DROP COLUMN IF EXISTS face")
