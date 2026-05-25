"""Per-scope auto_push override + soft-delete (PR 95).

PR 79 added DhcpServer.auto_push (per-server toggle); operators
have asked for finer-grained control to force or suppress auto-push
on a specific scope without flipping the server-wide flag. PR 95
adds `auto_push_override` (nullable bool: TRUE forces, FALSE
suppresses, NULL inherits server.auto_push) to dhcp_scopes.

PR 95 also adds soft-delete. Today's DELETE drops the row + the
Kea-side subnet entirely; a misclick is unrecoverable. The new
`deleted_at` column lets the API soft-delete: row stays in the DB
with a tombstone timestamp, the LIST endpoint hides it by default,
and a POST /dhcp/scopes/{id}/restore brings it back (without
re-pushing to Kea — operator runs that explicitly).

Partial index on deleted_at IS NULL covers the LIST hot path
without storing index entries for tombstoned rows (the majority).
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260525_0063"
down_revision: str | None = "20260525_0062"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        "ALTER TABLE dhcp_scopes "
        "ADD COLUMN IF NOT EXISTS auto_push_override BOOLEAN"
    )
    op.execute(
        "ALTER TABLE dhcp_scopes "
        "ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ"
    )
    # Partial index for the LIST hot path: most rows are live, so we
    # only index those. WHERE deleted_at IS NULL keeps the index
    # small as tombstones accumulate.
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_dhcp_scopes_live_per_server "
        "ON dhcp_scopes (dhcp_server_id) "
        "WHERE deleted_at IS NULL"
    )


def downgrade() -> None:
    op.execute("DROP INDEX IF EXISTS ix_dhcp_scopes_live_per_server")
    op.execute("ALTER TABLE dhcp_scopes DROP COLUMN IF EXISTS deleted_at")
    op.execute("ALTER TABLE dhcp_scopes DROP COLUMN IF EXISTS auto_push_override")
