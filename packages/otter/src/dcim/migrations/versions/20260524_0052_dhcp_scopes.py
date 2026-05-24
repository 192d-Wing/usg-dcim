"""DhcpScope — DHCPv4/v6 subnet definitions per DhcpServer (PR 73).

A "scope" in Microsoft DHCP terms / "subnet" in ISC Kea terms is the
contract a DHCP server serves on a given prefix: address pools, lease
timers, options, reservations. DCIM today reads leases from Kea but
doesn't author Kea config; this table is the data model for what DCIM
would push (config push itself is deferred to a follow-up).

Single table with `ip_family` discriminator and INET-based CIDR — same
pattern as the IPAM Subnet/Supernet tables, which dodge the v4/v6
split by relying on Postgres's native INET handling.

Most fields are JSON because:
  * Kea's option-data shape is family-specific and operator-extensible.
  * Pools are 1:N inside a scope and small; a side table would add
    joins without changing semantics.
  * Reservations follow the same shape rule (1:N, small, JSON-friendly).

If a scope volume turns out to need per-row queries (e.g. "find
reservation by MAC across all scopes"), the pools/reservations split
to side tables in a follow-up — no schema migration needed for callers
since the API serializes the JSON as embedded arrays today.
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260524_0052"
down_revision: str | None = "20260524_0051"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        """
        CREATE TABLE IF NOT EXISTS dhcp_scopes (
            id UUID PRIMARY KEY,
            dhcp_server_id UUID NOT NULL REFERENCES dhcp_servers(id) ON DELETE CASCADE,
            subnet_id UUID REFERENCES subnets(id) ON DELETE SET NULL,
            name VARCHAR(128) NOT NULL,
            ip_family SMALLINT NOT NULL,
            prefix CIDR NOT NULL,
            pools_json JSONB NOT NULL DEFAULT '[]'::jsonb,
            pd_pools_json JSONB,
            options_json JSONB NOT NULL DEFAULT '[]'::jsonb,
            reservations_json JSONB NOT NULL DEFAULT '[]'::jsonb,
            valid_lifetime_seconds INTEGER NOT NULL DEFAULT 3600,
            renew_timer_seconds INTEGER,
            rebind_timer_seconds INTEGER,
            preferred_lifetime_seconds INTEGER,
            enabled BOOLEAN NOT NULL DEFAULT TRUE,
            description VARCHAR(512),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT ck_dhcp_scope_family CHECK (ip_family IN (4, 6)),
            -- v6-only timers must be NULL on v4 scopes; the API enforces
            -- the inverse too but the DB-level guard catches direct
            -- INSERTs and bad migrations.
            CONSTRAINT ck_dhcp_scope_v6_only CHECK (
                ip_family = 6
                OR (pd_pools_json IS NULL AND preferred_lifetime_seconds IS NULL)
            ),
            CONSTRAINT uq_dhcp_scope_server_prefix UNIQUE (dhcp_server_id, prefix)
        )
        """
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_dhcp_scopes_server "
        "ON dhcp_scopes (dhcp_server_id)"
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_dhcp_scopes_subnet "
        "ON dhcp_scopes (subnet_id) WHERE subnet_id IS NOT NULL"
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_dhcp_scopes_family "
        "ON dhcp_scopes (ip_family)"
    )


def downgrade() -> None:
    op.execute("DROP TABLE IF EXISTS dhcp_scopes")
