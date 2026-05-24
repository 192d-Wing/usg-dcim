"""Operator-authored base config for Kea bundle assembly (PR 76).

The bundle endpoint at `/api/v1/dhcp/servers/{id}/bundle` returns a
complete Kea config the dhcp-site chart's bundle-puller can write to
disk and SIGHUP into Kea. DCIM owns the per-subnet objects
(`subnet4[]` / `subnet6[]` rendered from DhcpScope rows); everything
else Kea needs — `interfaces-config`, `lease-database`,
`hooks-libraries`, `loggers`, global option-data, client-classes —
the operator authors out-of-band and stores here.

Single JSONB column structured as:

    {
      "ctrl-agent": { ... full Kea Control Agent config ... },
      "dhcp4":      { ... Kea Dhcp4 server config sans subnet4 ... },
      "dhcp6":      { ... Kea Dhcp6 server config sans subnet6 ... }
    }

The renderer overlays the DCIM-authored subnet arrays onto each of
the dhcp4 / dhcp6 sections at assembly time. Anything else in those
sections passes through verbatim — DCIM never edits operator state.

Default `'{}'::jsonb` so existing DhcpServer rows don't break;
bundles for empty-base servers come back with empty dhcp4/dhcp6
configs except for the subnet arrays DCIM contributes.
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260524_0054"
down_revision: str | None = "20260524_0053"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        "ALTER TABLE dhcp_servers "
        "ADD COLUMN IF NOT EXISTS base_config JSONB NOT NULL DEFAULT '{}'::jsonb"
    )


def downgrade() -> None:
    op.execute("ALTER TABLE dhcp_servers DROP COLUMN IF EXISTS base_config")
