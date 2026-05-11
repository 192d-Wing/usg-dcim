"""DNS subsystem: zones, records, on-site CoreDNS deployments, anycast.

The hierarchy mirrors how the operator thinks about it:

  Fabric → DnsZone (apex)               → owned by DCIM, NS-delegates to sites
         ↳ DnsZone (per-site)           → owned by DCIM, holds A/AAAA/PTR
         ↳ AnycastGroup (per-fabric)    → the per-fabric anycast IP for recursive

  Site   → DnsServer (auth)             → CoreDNS container, mgmt-IP, loads
                                          fabric-wide zones for resilience
         ↳ DnsServer (recursive)        → CoreDNS container, anycast IP, forwards
                                          everything else upstream
         ↳ BgpPeer                      → reusable BGP neighbor for the GoBGP sidecar

  M:M    AnycastBgpBinding              → which peers each recursive DnsServer
                                          advertises to

DnsRecord rows come in two flavors: `source=ipam` (auto-projected from
IPAddress.dns_name; rebuilt on every sync) and `source=manual` (CNAME,
MX, TXT, etc. — the operator owns these). The JSON `data` column carries
the type-specific shape; the schemas layer enforces a discriminated
union per record type so a malformed payload can't reach the renderer.
"""

from __future__ import annotations

import enum
from datetime import datetime
from uuid import UUID

from sqlalchemy import (
    JSON,
    Boolean,
    DateTime,
    Enum,
    ForeignKey,
    Index,
    Integer,
    String,
    UniqueConstraint,
)
from sqlalchemy.dialects.postgresql import INET
from sqlalchemy.dialects.postgresql import UUID as PgUUID
from sqlalchemy.orm import Mapped, mapped_column

from ..db import Base
from ._mixins import Timestamped, UUIDPrimaryKey


class DnsServerRole(str, enum.Enum):
    auth = "auth"
    recursive = "recursive"


class DnsZoneKind(str, enum.Enum):
    apex = "apex"
    site = "site"


class DnsRecordType(str, enum.Enum):
    A = "A"
    AAAA = "AAAA"
    CNAME = "CNAME"
    MX = "MX"
    TXT = "TXT"
    SRV = "SRV"
    NS = "NS"
    CAA = "CAA"
    PTR = "PTR"


class DnsRecordSource(str, enum.Enum):
    ipam = "ipam"
    manual = "manual"


class AnycastService(str, enum.Enum):
    """Service tag on an AnycastGroup. Reserved for future expansion;
    DNS recursive is the only consumer in v1."""

    dns_recursive = "dns_recursive"
    ntp = "ntp"
    log = "log"


class DnsZone(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "dns_zones"
    __table_args__ = (
        UniqueConstraint("name", name="uq_dns_zone_name"),
        # One apex per fabric. Sites can host any number of site-kind
        # zones; FQDN uniqueness is the only other rule and is covered
        # by uq_dns_zone_name. Enforced by partial unique index
        # uq_dns_zone_one_apex_per_fabric (migration 0015).
        Index("ix_dns_zones_fabric", "fabric_id"),
        Index("ix_dns_zones_site", "site_id"),
    )

    name: Mapped[str] = mapped_column(String(253), nullable=False)
    kind: Mapped[DnsZoneKind] = mapped_column(
        Enum(DnsZoneKind, name="dns_zone_kind", values_callable=lambda x: [e.value for e in x]),
        nullable=False,
    )
    fabric_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("fabrics.id"), nullable=False,
    )
    # Apex zones leave site_id null; site zones must set it.
    site_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("sites.id"),
    )
    description: Mapped[str | None] = mapped_column(String(512))
    # SOA fields. RFC 1035 defaults that work for most internal zones —
    # operators can override per-zone when their replication topology
    # demands different timers.
    soa_mname: Mapped[str] = mapped_column(String(253), nullable=False, default="ns1")
    soa_rname: Mapped[str] = mapped_column(String(253), nullable=False, default="hostmaster")
    soa_refresh: Mapped[int] = mapped_column(Integer, nullable=False, default=3600)
    soa_retry: Mapped[int] = mapped_column(Integer, nullable=False, default=600)
    soa_expire: Mapped[int] = mapped_column(Integer, nullable=False, default=604800)
    soa_minimum: Mapped[int] = mapped_column(Integer, nullable=False, default=300)
    default_ttl: Mapped[int] = mapped_column(Integer, nullable=False, default=300)


class DnsRecord(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "dns_records"
    __table_args__ = (
        Index("ix_dns_records_zone_name_type", "zone_id", "name", "type"),
        Index("ix_dns_records_source", "source"),
        Index("ix_dns_records_ipam_address", "ipam_address_id"),
    )

    zone_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("dns_zones.id"), nullable=False,
    )
    # Left-hand side of the record. "@" means the zone apex.
    name: Mapped[str] = mapped_column(String(253), nullable=False)
    type: Mapped[DnsRecordType] = mapped_column(
        Enum(DnsRecordType, name="dns_record_type", values_callable=lambda x: [e.value for e in x]),
        nullable=False,
    )
    # Per-record TTL; null means inherit from the zone's default_ttl.
    ttl: Mapped[int | None] = mapped_column(Integer)
    # Type-specific payload. Shape validated by a discriminated union in
    # the schemas layer (e.g. {"target":"10.0.0.5"} for A;
    # {"priority":10,"target":"mail.x"} for MX).
    data: Mapped[dict] = mapped_column(JSON, nullable=False)
    source: Mapped[DnsRecordSource] = mapped_column(
        Enum(DnsRecordSource, name="dns_record_source", values_callable=lambda x: [e.value for e in x]),
        default=DnsRecordSource.manual, nullable=False,
    )
    # Back-pointer to the IPAddress an `ipam` row was projected from.
    # The sync job uses this to delete-then-recreate cleanly.
    ipam_address_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("ip_addresses.id"),
    )
    description: Mapped[str | None] = mapped_column(String(512))


class AnycastGroup(UUIDPrimaryKey, Timestamped, Base):
    """Per-fabric anycast service IP. DNS recursive is the v1 consumer;
    the model is reusable for NTP, log aggregators, etc."""

    __tablename__ = "anycast_groups"
    __table_args__ = (
        UniqueConstraint("fabric_id", "service", name="uq_anycast_fabric_service"),
        Index("ix_anycast_groups_fabric", "fabric_id"),
    )

    name: Mapped[str] = mapped_column(String(128), nullable=False)
    fabric_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("fabrics.id"), nullable=False,
    )
    service: Mapped[AnycastService] = mapped_column(
        Enum(AnycastService, name="anycast_service", values_callable=lambda x: [e.value for e in x]),
        nullable=False,
    )
    # Either v4 or v6 (or both) must be set. Enforced in the API layer
    # rather than as a check constraint so the validation message is
    # actionable.
    anycast_ipv4: Mapped[str | None] = mapped_column(INET)
    anycast_ipv6: Mapped[str | None] = mapped_column(INET)
    description: Mapped[str | None] = mapped_column(String(512))


class BgpPeer(UUIDPrimaryKey, Timestamped, Base):
    """A BGP neighbor — typically the leaf or top-of-rack a service's
    anycast sidecar peers with. First-class so anycast services beyond
    DNS can reuse the same row without each one redefining its own
    peer config.

    Both AS numbers are FKs into the ASN catalog (models/bgp.py:Asn) so
    operators can't typo a number that isn't in their inventory. MD5
    authentication is deprecated — RFC 5925 TCP AO superseded it — so
    the peer references a key chain rather than carrying a single
    shared secret inline."""

    __tablename__ = "bgp_peers"
    __table_args__ = (
        UniqueConstraint("site_id", "peer_ip", name="uq_bgp_peer_site_ip"),
        Index("ix_bgp_peers_site", "site_id"),
    )

    name: Mapped[str] = mapped_column(String(128), nullable=False)
    site_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("sites.id"), nullable=False,
    )
    # Replaces the old `local_asn` / `peer_asn` integer columns. The
    # ASN value is reached via Asn.asn through the FK; the API layer
    # surfaces both the id and the resolved integer for convenience.
    local_asn_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("bgp_asns.id"), nullable=False,
    )
    peer_asn_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("bgp_asns.id"), nullable=False,
    )
    peer_ip: Mapped[str] = mapped_column(INET, nullable=False)
    peer_description: Mapped[str | None] = mapped_column(String(512))
    # Optional TCP AO key chain (RFC 5925). Replaces the old
    # md5_password column. Recursive announcements + VRF MP-BGP both
    # default to no-auth when null.
    tcp_ao_key_chain_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("tcp_ao_key_chains.id"),
    )
    enabled: Mapped[bool] = mapped_column(Boolean, default=True, nullable=False)


class DnsServer(UUIDPrimaryKey, Timestamped, Base):
    """A CoreDNS container at a site. Two roles per site (auth +
    recursive); the recursive role binds to an AnycastGroup."""

    __tablename__ = "dns_servers"
    __table_args__ = (
        UniqueConstraint("name", name="uq_dns_server_name"),
        # A site has at most one server per role — auth and recursive
        # never coexist in one container.
        UniqueConstraint("site_id", "role", name="uq_dns_server_site_role"),
        Index("ix_dns_servers_site", "site_id"),
        Index("ix_dns_servers_fabric", "fabric_id"),
    )

    name: Mapped[str] = mapped_column(String(128), nullable=False)
    site_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("sites.id"), nullable=False,
    )
    fabric_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("fabrics.id"), nullable=False,
    )
    role: Mapped[DnsServerRole] = mapped_column(
        Enum(DnsServerRole, name="dns_server_role", values_callable=lambda x: [e.value for e in x]),
        nullable=False,
    )
    # Management IP the auth pod listens on. The recursive pod uses the
    # AnycastGroup's anycast IP instead — but we still record a unicast
    # mgmt IP so the recursive can be probed directly for diagnostics.
    unicast_ip: Mapped[str] = mapped_column(INET, nullable=False)
    enabled: Mapped[bool] = mapped_column(Boolean, default=True, nullable=False)
    # Render-status fields, mirroring DhcpServer's last_sync_* shape.
    # The collector posts back here every time it pulls a new bundle.
    last_render_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    last_render_status: Mapped[str | None] = mapped_column(String(32))   # ok|error
    last_render_error: Mapped[str | None] = mapped_column(String(2048))
    last_render_etag: Mapped[str | None] = mapped_column(String(64))
    coredns_version: Mapped[str | None] = mapped_column(String(32))
    # Only set when role=recursive. Enforced in the API layer.
    anycast_group_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("anycast_groups.id"),
    )


class AnycastBgpBinding(UUIDPrimaryKey, Timestamped, Base):
    """Many-to-many: which BGP peers a recursive DnsServer advertises
    its anycast IP to. Modeled as a row (rather than a plain association
    table) so the binding itself is auditable and timestamped."""

    __tablename__ = "anycast_bgp_bindings"
    __table_args__ = (
        UniqueConstraint("dns_server_id", "bgp_peer_id", name="uq_anycast_binding"),
        Index("ix_anycast_bindings_server", "dns_server_id"),
        Index("ix_anycast_bindings_peer", "bgp_peer_id"),
    )

    dns_server_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("dns_servers.id"), nullable=False,
    )
    bgp_peer_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("bgp_peers.id"), nullable=False,
    )
