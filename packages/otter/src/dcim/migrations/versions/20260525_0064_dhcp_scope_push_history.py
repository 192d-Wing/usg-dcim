"""Append-only per-scope push history (PR 104).

`DhcpScope.last_push_*` would give the most recent state, but only
the *most recent* — operators trying to answer "is this scope
flaky?" or "what's our 24h rolling success rate per fabric?" need
the full attempt log. Today the only record is Prometheus
histograms (PR 98) which expire and don't carry the per-scope
breakdown.

This migration adds dhcp_scope_push_history: one row per push or
delete attempt, append-only, with the interpreted Kea status and
duration so the UI can show recent activity inline and an
aggregation query can compute rolling success rates per scope /
fabric / server.

Schema notes:
  * server_id is denormalized off scope_id so server-wide history
    queries don't pay the join cost on the hot read path; ON DELETE
    CASCADE for both so a server delete drops its history with it.
  * operation is "add" | "update" | "delete" mirroring the Kea
    subnet_cmds verbs; small enough to fit in VARCHAR(16) but we
    keep it as VARCHAR (not an enum) so adding a new operation in
    a future PR doesn't require a migration.
  * status is the same 3-value taxonomy push_scope returns
    (ok / error / unsupported) — keeping it consistent with
    DhcpServer.last_push_status simplifies the join.
  * kea_subnet_id can be NULL: transport failures before id
    allocation, or delete attempts where the scope was never
    pushed.
  * duration_ms is INTEGER (millis are plenty of resolution for
    the UI; sub-millisecond Kea calls would be sus).
  * No created_at/updated_at: append-only, so attempted_at IS
    the timestamp.

Indexes:
  * (scope_id, attempted_at DESC) — UI "last N pushes for this
    scope".
  * (server_id, attempted_at DESC) — fleet view "what happened
    on server X in the last hour".
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260525_0064"
down_revision: str | None = "20260525_0063"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        """
        CREATE TABLE IF NOT EXISTS dhcp_scope_push_history (
            id UUID PRIMARY KEY,
            scope_id UUID NOT NULL REFERENCES dhcp_scopes(id) ON DELETE CASCADE,
            server_id UUID NOT NULL REFERENCES dhcp_servers(id) ON DELETE CASCADE,
            operation VARCHAR(16) NOT NULL,
            kea_subnet_id INTEGER,
            status VARCHAR(16) NOT NULL,
            error VARCHAR(2048),
            duration_ms INTEGER,
            attempted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT ck_dhcp_push_history_status CHECK (
                status IN ('ok', 'error', 'unsupported')
            ),
            CONSTRAINT ck_dhcp_push_history_operation CHECK (
                operation IN ('add', 'update', 'delete')
            )
        )
        """
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_dhcp_scope_push_history_scope "
        "ON dhcp_scope_push_history (scope_id, attempted_at DESC)"
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_dhcp_scope_push_history_server "
        "ON dhcp_scope_push_history (server_id, attempted_at DESC)"
    )


def downgrade() -> None:
    op.execute("DROP TABLE IF EXISTS dhcp_scope_push_history")
