"""DNS subsystem: zones, records, servers, anycast, BGP peers.

Adds:
  - dns_zones / dns_records: BIND-able zone data DCIM is authoritative for.
  - dns_servers: per-site CoreDNS deployments (auth + recursive roles).
  - anycast_groups: per-fabric anycast service IPs (DNS recursive in v1).
  - bgp_peers: reusable BGP neighbor definitions for anycast sidecars.
  - anycast_bgp_bindings: M:M between recursive DnsServers and BgpPeers.

Each DDL statement gets its own op.execute() because asyncpg refuses
multi-statement prepared SQL.

Revision ID: 20260510_0009
Revises: 20260510_0008
Create Date: 2026-05-10
"""

from collections.abc import Sequence

from alembic import op

revision: str = "20260510_0009"
down_revision: str | None = "20260510_0008"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    # Enums first — DO blocks so partial failures during development
    # don't wedge the migration.
    op.execute(
        """
        DO $$ BEGIN
            CREATE TYPE dns_server_role AS ENUM ('auth','recursive');
        EXCEPTION WHEN duplicate_object THEN NULL;
        END $$;
        """
    )
    op.execute(
        """
        DO $$ BEGIN
            CREATE TYPE dns_zone_kind AS ENUM ('apex','site');
        EXCEPTION WHEN duplicate_object THEN NULL;
        END $$;
        """
    )
    op.execute(
        """
        DO $$ BEGIN
            CREATE TYPE dns_record_type AS ENUM (
                'A','AAAA','CNAME','MX','TXT','SRV','NS','CAA','PTR'
            );
        EXCEPTION WHEN duplicate_object THEN NULL;
        END $$;
        """
    )
    op.execute(
        """
        DO $$ BEGIN
            CREATE TYPE dns_record_source AS ENUM ('ipam','manual');
        EXCEPTION WHEN duplicate_object THEN NULL;
        END $$;
        """
    )
    op.execute(
        """
        DO $$ BEGIN
            CREATE TYPE anycast_service AS ENUM ('dns_recursive','ntp','log');
        EXCEPTION WHEN duplicate_object THEN NULL;
        END $$;
        """
    )

    # Anycast groups — referenced by dns_servers.anycast_group_id, so create first.
    op.execute(
        """
        CREATE TABLE IF NOT EXISTS anycast_groups (
            id UUID PRIMARY KEY,
            name VARCHAR(128) NOT NULL,
            fabric_id UUID NOT NULL REFERENCES fabrics(id),
            service anycast_service NOT NULL,
            anycast_ipv4 INET,
            anycast_ipv6 INET,
            description VARCHAR(512),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_anycast_fabric_service UNIQUE (fabric_id, service)
        )
        """
    )
    op.execute("CREATE INDEX IF NOT EXISTS ix_anycast_groups_fabric ON anycast_groups (fabric_id)")

    # BGP peers.
    op.execute(
        """
        CREATE TABLE IF NOT EXISTS bgp_peers (
            id UUID PRIMARY KEY,
            name VARCHAR(128) NOT NULL,
            site_id UUID NOT NULL REFERENCES sites(id),
            local_asn INTEGER NOT NULL,
            peer_asn INTEGER NOT NULL,
            peer_ip INET NOT NULL,
            peer_description VARCHAR(512),
            md5_password VARCHAR(128),
            enabled BOOLEAN NOT NULL DEFAULT TRUE,
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_bgp_peer_site_ip UNIQUE (site_id, peer_ip)
        )
        """
    )
    op.execute("CREATE INDEX IF NOT EXISTS ix_bgp_peers_site ON bgp_peers (site_id)")

    # DNS zones — referenced by dns_records.zone_id.
    op.execute(
        """
        CREATE TABLE IF NOT EXISTS dns_zones (
            id UUID PRIMARY KEY,
            name VARCHAR(253) NOT NULL,
            kind dns_zone_kind NOT NULL,
            fabric_id UUID NOT NULL REFERENCES fabrics(id),
            site_id UUID REFERENCES sites(id),
            description VARCHAR(512),
            soa_mname VARCHAR(253) NOT NULL DEFAULT 'ns1',
            soa_rname VARCHAR(253) NOT NULL DEFAULT 'hostmaster',
            soa_refresh INTEGER NOT NULL DEFAULT 3600,
            soa_retry INTEGER NOT NULL DEFAULT 600,
            soa_expire INTEGER NOT NULL DEFAULT 604800,
            soa_minimum INTEGER NOT NULL DEFAULT 300,
            default_ttl INTEGER NOT NULL DEFAULT 300,
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_dns_zone_name UNIQUE (name),
            CONSTRAINT uq_dns_zone_fabric_site_kind UNIQUE (fabric_id, site_id, kind)
        )
        """
    )
    op.execute("CREATE INDEX IF NOT EXISTS ix_dns_zones_fabric ON dns_zones (fabric_id)")
    op.execute("CREATE INDEX IF NOT EXISTS ix_dns_zones_site ON dns_zones (site_id)")

    # DNS records.
    op.execute(
        """
        CREATE TABLE IF NOT EXISTS dns_records (
            id UUID PRIMARY KEY,
            zone_id UUID NOT NULL REFERENCES dns_zones(id),
            name VARCHAR(253) NOT NULL,
            type dns_record_type NOT NULL,
            ttl INTEGER,
            data JSON NOT NULL,
            source dns_record_source NOT NULL DEFAULT 'manual',
            ipam_address_id UUID REFERENCES ip_addresses(id),
            description VARCHAR(512),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
        )
        """
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_dns_records_zone_name_type "
        "ON dns_records (zone_id, name, type)"
    )
    op.execute("CREATE INDEX IF NOT EXISTS ix_dns_records_source ON dns_records (source)")
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_dns_records_ipam_address "
        "ON dns_records (ipam_address_id)"
    )

    # DNS servers.
    op.execute(
        """
        CREATE TABLE IF NOT EXISTS dns_servers (
            id UUID PRIMARY KEY,
            name VARCHAR(128) NOT NULL,
            site_id UUID NOT NULL REFERENCES sites(id),
            fabric_id UUID NOT NULL REFERENCES fabrics(id),
            role dns_server_role NOT NULL,
            unicast_ip INET NOT NULL,
            enabled BOOLEAN NOT NULL DEFAULT TRUE,
            last_render_at TIMESTAMPTZ,
            last_render_status VARCHAR(32),
            last_render_error VARCHAR(2048),
            last_render_etag VARCHAR(64),
            coredns_version VARCHAR(32),
            anycast_group_id UUID REFERENCES anycast_groups(id),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_dns_server_name UNIQUE (name),
            CONSTRAINT uq_dns_server_site_role UNIQUE (site_id, role)
        )
        """
    )
    op.execute("CREATE INDEX IF NOT EXISTS ix_dns_servers_site ON dns_servers (site_id)")
    op.execute("CREATE INDEX IF NOT EXISTS ix_dns_servers_fabric ON dns_servers (fabric_id)")

    # Anycast ↔ BGP M:M.
    op.execute(
        """
        CREATE TABLE IF NOT EXISTS anycast_bgp_bindings (
            id UUID PRIMARY KEY,
            dns_server_id UUID NOT NULL REFERENCES dns_servers(id),
            bgp_peer_id UUID NOT NULL REFERENCES bgp_peers(id),
            created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
            CONSTRAINT uq_anycast_binding UNIQUE (dns_server_id, bgp_peer_id)
        )
        """
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_anycast_bindings_server "
        "ON anycast_bgp_bindings (dns_server_id)"
    )
    op.execute(
        "CREATE INDEX IF NOT EXISTS ix_anycast_bindings_peer "
        "ON anycast_bgp_bindings (bgp_peer_id)"
    )


def downgrade() -> None:
    op.execute("DROP TABLE IF EXISTS anycast_bgp_bindings")
    op.execute("DROP TABLE IF EXISTS dns_servers")
    op.execute("DROP TABLE IF EXISTS dns_records")
    op.execute("DROP TABLE IF EXISTS dns_zones")
    op.execute("DROP TABLE IF EXISTS bgp_peers")
    op.execute("DROP TABLE IF EXISTS anycast_groups")
    op.execute("DROP TYPE IF EXISTS anycast_service")
    op.execute("DROP TYPE IF EXISTS dns_record_source")
    op.execute("DROP TYPE IF EXISTS dns_record_type")
    op.execute("DROP TYPE IF EXISTS dns_zone_kind")
    op.execute("DROP TYPE IF EXISTS dns_server_role")
