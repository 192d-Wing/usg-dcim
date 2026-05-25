"""DUID binding on IPAddress for v6 reconcile (PR 94).

PR 88 added MAC-binding check to reconcile/sync for v4 reservations.
v6 reservations declare DUID instead of MAC; PR 94 stores it on
IPAddress.dhcp_duid (parallel to PR 88's reuse of dhcp_mac) and
extends the reconcile + sync paths to compare.

DUIDs (RFC 8415) are 1-128 octets, hex-encoded with colons. Common
DUID-LL forms run ~14 bytes (~42 hex chars with colons); DUID-EN
or DUID-UUID can reach ~260 chars. Storing as VARCHAR(254) covers
the common case + most uncommon cases; truly oversized DUIDs are
rejected at the normalize step (returns None) and the binding check
falls through to "clean" rather than spurious-mismatch.

Nullable for backward compat — pre-PR-94 IPAddress rows leave it
NULL; rows the v6 Kea lease ingest writes can populate it from
Kea's DUID field (separate PR if/when v6 lease ingest lands).
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260525_0062"
down_revision: str | None = "20260524_0061"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        "ALTER TABLE ip_addresses "
        "ADD COLUMN IF NOT EXISTS dhcp_duid VARCHAR(254)"
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_ip_addresses_dhcp_duid "
        "ON ip_addresses (dhcp_duid) WHERE dhcp_duid IS NOT NULL"
    )


def downgrade() -> None:
    op.execute("DROP INDEX IF EXISTS ix_ip_addresses_dhcp_duid")
    op.execute("ALTER TABLE ip_addresses DROP COLUMN IF EXISTS dhcp_duid")
