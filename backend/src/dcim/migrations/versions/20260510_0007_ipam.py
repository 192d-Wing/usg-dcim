"""IPAM hierarchy + DHCP servers.

Adds Fabric → VRF → Supernet → Subnet → IPAddress, plus DhcpServer for
the Kea sync. CIDR / INET use the native Postgres types so containment
+ overlap checks are index-backed rather than string comparisons.

Revision ID: 20260510_0007
Revises: 20260509_0006
Create Date: 2026-05-10
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260510_0007"
down_revision: str | None = "20260509_0006"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    # Enums first — re-runnable with DO blocks so partial failures during
    # development don't wedge the migration.
    op.execute(
        """
        DO $$ BEGIN
            CREATE TYPE ip_role AS ENUM ('mgmt','data','ipmi','vip','storage','other');
        EXCEPTION WHEN duplicate_object THEN NULL;
        END $$;
        DO $$ BEGIN
            CREATE TYPE ip_status AS ENUM ('active','reserved','deprecated');
        EXCEPTION WHEN duplicate_object THEN NULL;
        END $$;
        DO $$ BEGIN
            CREATE TYPE ip_source AS ENUM ('static','dhcp','reservation');
        EXCEPTION WHEN duplicate_object THEN NULL;
        END $$;
        """
    )

    op.execute(
        """
        CREATE TABLE IF NOT EXISTS fabrics (
            id UUID PRIMARY KEY,
            name VARCHAR(128) NOT NULL UNIQUE,
            slug VARCHAR(64) NOT NULL,
            description VARCHAR(512),
            enclave VARCHAR(64),
            classification VARCHAR(32),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
        );
        CREATE UNIQUE INDEX IF NOT EXISTS ix_fabrics_slug ON fabrics (slug);
        """
    )

    op.execute(
        """
        CREATE TABLE IF NOT EXISTS vrfs (
            id UUID PRIMARY KEY,
            fabric_id UUID NOT NULL REFERENCES fabrics(id),
            name VARCHAR(64) NOT NULL,
            rd VARCHAR(32),
            description VARCHAR(512),
            is_default BOOLEAN NOT NULL DEFAULT FALSE,
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_vrf_fabric_name UNIQUE (fabric_id, name),
            CONSTRAINT uq_vrf_fabric_rd UNIQUE (fabric_id, rd)
        );
        CREATE INDEX IF NOT EXISTS ix_vrfs_fabric ON vrfs (fabric_id);
        """
    )

    op.execute(
        """
        CREATE TABLE IF NOT EXISTS supernets (
            id UUID PRIMARY KEY,
            fabric_id UUID NOT NULL REFERENCES fabrics(id),
            vrf_id UUID NOT NULL REFERENCES vrfs(id),
            prefix CIDR NOT NULL,
            name VARCHAR(128),
            description VARCHAR(512),
            purpose VARCHAR(32),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
        );
        CREATE INDEX IF NOT EXISTS ix_supernets_fabric_vrf ON supernets (fabric_id, vrf_id);
        """
    )

    op.execute(
        """
        CREATE TABLE IF NOT EXISTS subnets (
            id UUID PRIMARY KEY,
            supernet_id UUID NOT NULL REFERENCES supernets(id),
            fabric_id UUID NOT NULL REFERENCES fabrics(id),
            vrf_id UUID NOT NULL REFERENCES vrfs(id),
            site_id UUID REFERENCES sites(id),
            prefix CIDR NOT NULL,
            name VARCHAR(128),
            description VARCHAR(512),
            purpose VARCHAR(32),
            vlan_id INTEGER,
            gateway INET,
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
        );
        CREATE INDEX IF NOT EXISTS ix_subnets_supernet ON subnets (supernet_id);
        CREATE INDEX IF NOT EXISTS ix_subnets_site ON subnets (site_id);
        CREATE INDEX IF NOT EXISTS ix_subnets_vrf ON subnets (vrf_id);
        """
    )

    op.execute(
        """
        CREATE TABLE IF NOT EXISTS ip_addresses (
            id UUID PRIMARY KEY,
            subnet_id UUID NOT NULL REFERENCES subnets(id),
            asset_id UUID REFERENCES assets(id),
            address INET NOT NULL,
            role ip_role NOT NULL DEFAULT 'data',
            status ip_status NOT NULL DEFAULT 'active',
            source ip_source NOT NULL DEFAULT 'static',
            dns_name VARCHAR(255),
            description VARCHAR(512),
            dhcp_lease_expires_at TIMESTAMPTZ,
            dhcp_mac VARCHAR(32),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_ip_subnet_address UNIQUE (subnet_id, address)
        );
        CREATE INDEX IF NOT EXISTS ix_ip_subnet ON ip_addresses (subnet_id);
        CREATE INDEX IF NOT EXISTS ix_ip_asset ON ip_addresses (asset_id);
        CREATE INDEX IF NOT EXISTS ix_ip_address ON ip_addresses (address);
        """
    )

    op.execute(
        """
        CREATE TABLE IF NOT EXISTS dhcp_servers (
            id UUID PRIMARY KEY,
            name VARCHAR(128) NOT NULL,
            fabric_id UUID NOT NULL REFERENCES fabrics(id),
            kea_url VARCHAR(512) NOT NULL,
            auth_username VARCHAR(128),
            auth_password VARCHAR(512),
            enabled BOOLEAN NOT NULL DEFAULT TRUE,
            last_sync_at TIMESTAMPTZ,
            last_sync_status VARCHAR(32),
            last_sync_error VARCHAR(2048),
            last_sync_lease_count INTEGER,
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_dhcp_server_name UNIQUE (name)
        );
        CREATE INDEX IF NOT EXISTS ix_dhcp_servers_fabric ON dhcp_servers (fabric_id);
        """
    )


def downgrade() -> None:
    op.execute("DROP TABLE IF EXISTS dhcp_servers")
    op.execute("DROP TABLE IF EXISTS ip_addresses")
    op.execute("DROP TABLE IF EXISTS subnets")
    op.execute("DROP TABLE IF EXISTS supernets")
    op.execute("DROP TABLE IF EXISTS vrfs")
    op.execute("DROP TABLE IF EXISTS fabrics")
    op.execute("DROP TYPE IF EXISTS ip_source")
    op.execute("DROP TYPE IF EXISTS ip_status")
    op.execute("DROP TYPE IF EXISTS ip_role")
