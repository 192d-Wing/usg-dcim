"""Drop the `legacy_permission_codes` snapshot column.

Migration 0030 added this column as a one-release safety net so the
pre-granular-RBAC permission_codes were recoverable without a backup
restore. A release cycle has passed; no live code path reads the
column. Reclaim the space.

Downgrade note: this re-creates the column empty. If an operator
downgrades all the way past 0030, that earlier migration's rollback
path will write NULL into roles.permission_codes (because there's no
snapshot to restore from). Real recovery past 0030 requires a backup.
The granular-RBAC migration's safety window is intentionally closed
here — same shape as any other rollback after a release ship.

Revision ID: 20260512_0038
Revises: 20260512_0037
Create Date: 2026-05-12
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260512_0038"
down_revision: str | None = "20260512_0037"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute("ALTER TABLE roles DROP COLUMN IF EXISTS legacy_permission_codes")


def downgrade() -> None:
    op.execute(
        "ALTER TABLE roles ADD COLUMN IF NOT EXISTS legacy_permission_codes JSON"
    )
