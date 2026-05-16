"""Per-fabric AXFR transfer ACL for the RFC 9432 catalog zone.

Adds `fabrics.catalog_transfer_acl` — a JSON list of CIDR strings
the catalog Corefile renders into an `acl { allow type AXFR net
<cidrs> ; block type AXFR }` rule paired with `transfer { to * }`.
Default NULL (interpreted as "no transfers permitted" by the
renderer, which omits both directives so CoreDNS's default closed
posture holds); operators add their downstream BIND / Knot /
PowerDNS primaries' source CIDRs to permit AXFR.

CoreDNS's `transfer` plugin parses only literal IPs and `*`, so
the CIDR-aware gating is delegated to the `acl` plugin. TSIG-
signed transfers aren't supported by either plugin natively;
either a custom plugin or an alternate auth engine would be
needed (see the catalog-zones plan). IP ACL is the security gate
v1 ships with.

Revision ID: 20260513_0042
Revises: 20260513_0041
Create Date: 2026-05-13
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260513_0042"
down_revision: str | None = "20260513_0041"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        "ALTER TABLE fabrics ADD COLUMN catalog_transfer_acl JSON"
    )


def downgrade() -> None:
    op.execute(
        "ALTER TABLE fabrics DROP COLUMN IF EXISTS catalog_transfer_acl"
    )
