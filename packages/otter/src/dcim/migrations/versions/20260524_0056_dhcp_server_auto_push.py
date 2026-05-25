"""Per-server auto-push toggle (PR 79).

`dhcp_servers.auto_push` is an opt-in bool: when TRUE, the API
handlers schedule a background push after a DhcpScope create or
update on that server, so operators don't have to remember
POST /dhcp/scopes/{id}/push. Default FALSE preserves the explicit-
push behavior PR 74 shipped.

DELETE keeps its inline subnet4-del/subnet6-del call from PR 74 —
auto_push doesn't affect deletes (there's no scope row left to
re-push when the delete returns).

Failures of the background push land in dhcp_servers.last_push_*
(PR 74 already wired this); operators see the bad status in the
LIST endpoint and can fix manually.
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260524_0056"
down_revision: str | None = "20260524_0055"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        "ALTER TABLE dhcp_servers "
        "ADD COLUMN IF NOT EXISTS auto_push BOOLEAN NOT NULL DEFAULT FALSE"
    )


def downgrade() -> None:
    op.execute("ALTER TABLE dhcp_servers DROP COLUMN IF EXISTS auto_push")
