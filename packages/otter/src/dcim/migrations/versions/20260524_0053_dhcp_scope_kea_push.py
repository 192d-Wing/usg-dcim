"""Kea config push: kea_subnet_id on dhcp_scopes + last_push_* on dhcp_servers (PR 74).

PR 73 added the DhcpScope CRUD; PR 74 pushes the data to Kea via the
Control Agent's subnet_cmds hook (subnet4-add / subnet4-update /
subnet4-del and their v6 twins). Two columns are required:

  * dhcp_scopes.kea_subnet_id (INTEGER, nullable) — Kea requires a
    numeric subnet ID. We allocate per-DhcpServer on first push and
    pin it for the scope's life so subsequent updates target the same
    row in Kea. NULL means "not yet pushed."

  * dhcp_servers.last_push_at / last_push_status / last_push_error —
    parallel set to last_sync_* but for config push (the opposite
    direction). Letting these share columns would collapse two
    diagnostics into one and obscure which way a failure points.

The UNIQUE constraint on (dhcp_server_id, kea_subnet_id) is partial:
NULL ids don't collide. Operators can drop+recreate a scope without
holding the old id.
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260524_0053"
down_revision: str | None = "20260524_0052"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        "ALTER TABLE dhcp_scopes "
        "ADD COLUMN IF NOT EXISTS kea_subnet_id INTEGER"
    )
    op.execute(
        "CREATE UNIQUE INDEX IF NOT EXISTS uq_dhcp_scopes_server_kea_id "
        "ON dhcp_scopes (dhcp_server_id, kea_subnet_id) "
        "WHERE kea_subnet_id IS NOT NULL"
    )
    op.execute(
        "ALTER TABLE dhcp_servers "
        "ADD COLUMN IF NOT EXISTS last_push_at TIMESTAMPTZ"
    )
    op.execute(
        "ALTER TABLE dhcp_servers "
        "ADD COLUMN IF NOT EXISTS last_push_status VARCHAR(32)"
    )
    op.execute(
        "ALTER TABLE dhcp_servers "
        "ADD COLUMN IF NOT EXISTS last_push_error VARCHAR(2048)"
    )


def downgrade() -> None:
    op.execute("DROP INDEX IF EXISTS uq_dhcp_scopes_server_kea_id")
    op.execute("ALTER TABLE dhcp_scopes DROP COLUMN IF EXISTS kea_subnet_id")
    op.execute("ALTER TABLE dhcp_servers DROP COLUMN IF EXISTS last_push_error")
    op.execute("ALTER TABLE dhcp_servers DROP COLUMN IF EXISTS last_push_status")
    op.execute("ALTER TABLE dhcp_servers DROP COLUMN IF EXISTS last_push_at")
