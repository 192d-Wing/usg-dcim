"""Persist drift state per DhcpScope (PR 80).

PR 75 added on-demand drift detection at GET /dhcp/scopes/{id}/diff
and PR 77 added the bulk variant. Both were stateless — they hit
Kea, computed the delta, returned it. The LIST endpoint couldn't
surface drift without re-checking every row, a cron couldn't track
fleet drift over time, and the "push only what's drifted" pattern
required an out-of-band cache on the client.

This migration adds three columns to dhcp_scopes:

  * last_diff_at        — timestamp of the last diff_scope call
  * last_diff_status    — one of: in_sync, drifted, missing_from_kea,
                          never_pushed, error (mirrors DiffResult.status)
  * last_diff_delta_json — the delta dict on drift; NULL on in_sync /
                          never_pushed / missing_from_kea (where the
                          delta isn't applicable or is implicit)

Companion PR 80 wiring:
  - diff_scope handlers (per-scope + diff-all) write these columns
    after running the diff.
  - push_scope on result.status="ok" resets to in_sync / now / NULL —
    a successful push is by construction a re-sync.
  - New POST /dhcp/servers/{id}/scopes/push-drifted selects only
    scopes where last_diff_status='drifted' and runs push on each,
    reusing the bulk-push summary shape.
  - LIST endpoint gains ?diff_status= filter.
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260524_0057"
down_revision: str | None = "20260524_0056"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        "ALTER TABLE dhcp_scopes "
        "ADD COLUMN IF NOT EXISTS last_diff_at TIMESTAMPTZ"
    )
    op.execute(
        "ALTER TABLE dhcp_scopes "
        "ADD COLUMN IF NOT EXISTS last_diff_status VARCHAR(32)"
    )
    op.execute(
        "ALTER TABLE dhcp_scopes "
        "ADD COLUMN IF NOT EXISTS last_diff_delta_json JSONB"
    )
    # Partial index — push-drifted reads only the drifted rows, so
    # the index doesn't need to cover in_sync entries (which are
    # the steady-state majority).
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_dhcp_scopes_drifted "
        "ON dhcp_scopes (dhcp_server_id) "
        "WHERE last_diff_status = 'drifted'"
    )


def downgrade() -> None:
    op.execute("DROP INDEX IF EXISTS ix_dhcp_scopes_drifted")
    op.execute("ALTER TABLE dhcp_scopes DROP COLUMN IF EXISTS last_diff_delta_json")
    op.execute("ALTER TABLE dhcp_scopes DROP COLUMN IF EXISTS last_diff_status")
    op.execute("ALTER TABLE dhcp_scopes DROP COLUMN IF EXISTS last_diff_at")
