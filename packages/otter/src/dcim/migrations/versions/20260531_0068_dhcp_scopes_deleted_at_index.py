"""dhcp_scopes: index deleted_at for the tombstone-purge cron.

Background: PR #210/#211 land cron jobs on the Go scheduler; this PR
adds dhcp_scope_tombstone_purge — daily DELETE on dhcp_scopes
WHERE deleted_at IS NOT NULL AND deleted_at < $cutoff. The existing
ix_dhcp_scopes_live_per_server (migration 0063) is a partial index
WHERE deleted_at IS NULL — built for live-row lookups, opposite of
the predicate the cron needs. Without a complementary index the
nightly DELETE seq-scans dhcp_scopes.

This migration adds:

    CREATE INDEX ix_dhcp_scopes_tombstones
    ON dhcp_scopes (deleted_at)
    WHERE deleted_at IS NOT NULL;

The partial form keeps the index tiny — only soft-deleted rows index,
which is by definition the long-tail minority. Mirrors the rationale
of migration 0067 (dns_server_metrics_samples observed_at index) for
the dns_purge_metrics retention DELETE.

Downgrade drops the index (no schema change).
"""
from __future__ import annotations

from alembic import op

revision = "20260531_0068"
down_revision = "20260531_0067"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.execute(
        "CREATE INDEX ix_dhcp_scopes_tombstones "
        "ON dhcp_scopes (deleted_at) "
        "WHERE deleted_at IS NOT NULL"
    )


def downgrade() -> None:
    op.execute("DROP INDEX IF EXISTS ix_dhcp_scopes_tombstones")
