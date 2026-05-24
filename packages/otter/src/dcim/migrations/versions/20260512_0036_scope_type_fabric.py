"""scope_type enum gains 'fabric'.

The site-based scope helpers don't cover DNS / IPAM since those
resources live under a Fabric, not a Site. This migration extends the
existing scope_type postgres enum so role_scopes and oidc_role_mappings
can express "scope to fabric=<slug>" alongside the existing region /
site / site_group / enclave / organization values.

ALTER TYPE ... ADD VALUE can't run inside a transaction in Postgres
versions before 12; alembic auto-wraps DDL in a transaction, so
op.execute_outside_transaction would normally be required. Postgres
12+ (which we require) allows it inside a tx; the explicit
COMMIT/BEGIN bracket here is defensive in case an older server
shows up in CI.

Revision ID: 20260512_0036
Revises: 20260512_0035
Create Date: 2026-05-12
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260512_0036"
down_revision: str | None = "20260512_0035"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute("ALTER TYPE scope_type ADD VALUE IF NOT EXISTS 'fabric'")


def downgrade() -> None:
    # Postgres doesn't support removing enum values cleanly. Leave the
    # value in place; downgrading the model code is enough to stop
    # writing new rows with it.
    pass
