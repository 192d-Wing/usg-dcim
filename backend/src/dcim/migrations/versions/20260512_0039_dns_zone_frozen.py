"""Zone freeze flag — operator write lock for maintenance windows.

A frozen zone rejects every mutation (record CRUD, BIND import, IPAM
sync, DNSSEC enable/disable/rotate, NSEC3 toggles, key delete, zone
PATCH/DELETE) with 422. The freeze itself is set + cleared via the
new POST /dns/zones/{id}/freeze and /unfreeze endpoints, which are
the ONLY paths that mutate the column. Both go through audit.

Off by default: any zone created before this migration starts
unfrozen.

Revision ID: 20260512_0039
Revises: 20260512_0038
Create Date: 2026-05-12
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260512_0039"
down_revision: str | None = "20260512_0038"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        "ALTER TABLE dns_zones ADD COLUMN frozen BOOLEAN NOT NULL DEFAULT FALSE"
    )


def downgrade() -> None:
    op.execute("ALTER TABLE dns_zones DROP COLUMN IF EXISTS frozen")
