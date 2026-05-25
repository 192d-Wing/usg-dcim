"""Drop NOT NULL on alerts.site_id (PR 87).

PR 86 dispatched drift events as transient Alert-shaped objects
because Alert.site_id was nullable=False and DhcpScope drift is
fabric-rooted (a fabric can span multiple sites; there's no
single "site that drifted"). PR 87 makes drift alerts real rows
so they participate in ack/resolve/dedupe like every other alert
— but that means site_id has to allow NULL.

Existing rows: every alerts row pre-PR-87 was created by the
metric-rule engine which always sets site_id, so loosening the
constraint doesn't invalidate anything. LIST queries that filter
`WHERE site_id = ?` naturally exclude the drift rows, which is
the desired UX (operators viewing per-site alerts don't want
fabric-rooted drift mixed in; the drift dashboard reads dedupe_key
or labels_json.metric instead).
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260524_0059"
down_revision: str | None = "20260524_0058"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        "ALTER TABLE alerts ALTER COLUMN site_id DROP NOT NULL"
    )


def downgrade() -> None:
    # Re-NOT-NULL: backfill drift rows to a sentinel? No — drift
    # rows have no natural site. Operator clears them before downgrade.
    op.execute(
        "DELETE FROM alerts WHERE site_id IS NULL"
    )
    op.execute(
        "ALTER TABLE alerts ALTER COLUMN site_id SET NOT NULL"
    )
