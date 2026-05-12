"""NSEC3 parameters on dns_zones.

Adds three columns that control per-zone NSEC3 emission in the
coredns-nsec3sign plugin:

  - nsec3_salt        : hex string ("" or e.g. "aabbccdd") when the
                        zone should be signed with NSEC3; NULL keeps
                        the legacy NSEC behaviour (upstream `dnssec`
                        plugin).
  - nsec3_iterations  : RFC 9276 §3.1 recommends 0 for new
                        deployments; we cap at 150 in the renderer.
  - nsec3_opt_out     : flag that elides insecure delegations from
                        the NSEC3 chain — saves NSEC3 records on
                        delegation-heavy zones.

NULL on nsec3_salt is the deliberate default so this migration is a
no-op for existing signed zones: they keep using the upstream
`dnssec` plugin (NSEC chains) until an operator explicitly opts a
zone into NSEC3 by setting a salt. Switching the rendered Corefile
to nsec3sign requires the custom CoreDNS image, which the operator
also opts into via the site-stack compose.

Revision ID: 20260512_0028
Revises: 20260511_0027
Create Date: 2026-05-12
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260512_0028"
down_revision: str | None = "20260511_0027"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    # One DDL per execute() — asyncpg refuses multi-statement SQL.
    op.execute("ALTER TABLE dns_zones ADD COLUMN nsec3_salt VARCHAR(64)")
    op.execute(
        "ALTER TABLE dns_zones ADD COLUMN nsec3_iterations INTEGER "
        "NOT NULL DEFAULT 0"
    )
    op.execute(
        "ALTER TABLE dns_zones ADD COLUMN nsec3_opt_out BOOLEAN "
        "NOT NULL DEFAULT FALSE"
    )


def downgrade() -> None:
    op.execute("ALTER TABLE dns_zones DROP COLUMN IF EXISTS nsec3_opt_out")
    op.execute("ALTER TABLE dns_zones DROP COLUMN IF EXISTS nsec3_iterations")
    op.execute("ALTER TABLE dns_zones DROP COLUMN IF EXISTS nsec3_salt")
