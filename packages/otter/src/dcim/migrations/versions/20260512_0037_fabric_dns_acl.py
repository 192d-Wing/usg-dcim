"""Per-fabric DNS allow/deny network ACLs.

Adds two JSON columns on `fabrics` that the recursive Hickory
renderer maps directly to Hickory's top-level `allow_networks` and
`deny_networks` settings. Both are nullable; NULL emits nothing
(open recursive, all clients allowed by default). Operators set
these via the existing Fabric edit form.

Not a per-second rate-limiter — Hickory 0.26 doesn't expose one.
Real QPS protection still wants either an out-of-band host
firewall (nftables hashlimit) or a dnsdist sidecar in front of the
recursive.

Revision ID: 20260512_0037
Revises: 20260512_0036
Create Date: 2026-05-12
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260512_0037"
down_revision: str | None = "20260512_0036"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute("ALTER TABLE fabrics ADD COLUMN dns_deny_networks JSON")
    op.execute("ALTER TABLE fabrics ADD COLUMN dns_allow_networks JSON")


def downgrade() -> None:
    op.execute("ALTER TABLE fabrics DROP COLUMN IF EXISTS dns_allow_networks")
    op.execute("ALTER TABLE fabrics DROP COLUMN IF EXISTS dns_deny_networks")
