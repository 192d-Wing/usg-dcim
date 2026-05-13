"""Deployment-wide editable config rows.

Backs the new admin Settings → DNS panel: an operator can override
the env-backed `dns_recursive_upstreams` default at runtime without
a redeploy. Generic key/JSON-value shape so new operator-editable
settings can land without their own migration.

Revision ID: 20260512_0040
Revises: 20260512_0039
Create Date: 2026-05-12
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260512_0040"
down_revision: str | None = "20260512_0039"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        """
        CREATE TABLE system_settings (
            key VARCHAR(64) PRIMARY KEY,
            value JSON,
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
        )
        """
    )


def downgrade() -> None:
    op.execute("DROP TABLE IF EXISTS system_settings")
