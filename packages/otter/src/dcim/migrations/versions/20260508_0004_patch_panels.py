"""Patch-panel asset kind and port_count column.

Adds `patch_panel` to the asset_kind enum and an optional integer port_count
column on assets. port_count is generic (any asset that has ports can declare
a count), but the immediate driver is structured patch-panel cabling so that
cables can pin to a specific port slot rather than a free-text string.

Revision ID: 20260508_0004
Revises: 20260507_0003
Create Date: 2026-05-08
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260508_0004"
down_revision: str | None = "20260507_0003"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    # Postgres 12+ allows ADD VALUE inside a transaction as long as the new
    # value isn't referenced in the same transaction (it isn't here).
    op.execute("ALTER TYPE asset_kind ADD VALUE IF NOT EXISTS 'patch_panel'")
    op.execute("ALTER TABLE assets ADD COLUMN IF NOT EXISTS port_count INTEGER")


def downgrade() -> None:
    op.execute("ALTER TABLE assets DROP COLUMN IF EXISTS port_count")
    # Postgres can't remove an enum value without recreating the type and
    # rewriting every dependent column. Leaving the value in place is the
    # standard alembic pattern for enum additions.
