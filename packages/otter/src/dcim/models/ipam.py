"""IPAM hierarchy: Fabric → VRF → Supernet → Subnet → IPAddress.

A Fabric is a network namespace (typically lining up with an enclave or
classification level — "Production", "Lab", "IL5-SIPR"). VRFs inside a
fabric give isolated routing domains so the same RFC1918 range can
appear in multiple VRFs without colliding. Every fabric ships with a
default VRF auto-created on insert so flat networks don't have to deal
with VRFs explicitly.

Supernets are aggregates (e.g. 10.0.0.0/8). Subnets are allocatable
prefixes inside a supernet (e.g. 10.0.5.0/24). IPAddress rows are
single allocations inside a subnet, optionally bound to an asset and
flagged by source: hand-allocated (`static`), learned from a Kea DHCP
sync (`dhcp`), or held back as a `reservation` (gateway, future use).
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
from sqlalchemy.dialects.postgresql import CIDR, INET
from sqlalchemy.dialects.postgresql import UUID as PgUUID
from sqlalchemy.orm import Mapped, mapped_column, relationship

from ..db import Base
from ._mixins import Timestamped, UUIDPrimaryKey


class IpAddressSource(str, enum.Enum):
    static = "static"
    dhcp = "dhcp"
    reservation = "reservation"


class IpAddressStatus(str, enum.Enum):
    active = "active"
    reserved = "reserved"
    deprecated = "deprecated"


class IpAddressRole(str, enum.Enum):
    mgmt = "mgmt"
    data = "data"
    ipmi = "ipmi"
    vip = "vip"
    storage = "storage"
    other = "other"


class OverlayKind(str, enum.Enum):
    vxlan = "vxlan"
    geneve = "geneve"


class VniKind(str, enum.Enum):
    """L2 VNIs map to a broadcast domain (replaces a VLAN); L3 VNIs map to a
    tenant routing instance (a VRF). EVPN deployments use both."""

    l2 = "l2"
    l3 = "l3"


class VtepRole(str, enum.Enum):
    leaf = "leaf"
    spine = "spine"
    border = "border"
    other = "other"


class BgpAddressFamily(str, enum.Enum):
    """BGP address family for a VRF↔peer binding.

    A single TCP/MP-BGP session typically carries multiple address
    families to advertise the same VRF (VPNv4 for IPv4 unicast, VPNv6
    for IPv6 unicast, EVPN for L2/L3 overlays). Each family can use a
    distinct Route Distinguisher, so the binding is keyed per AF."""

    vpnv4 = "vpnv4"
    vpnv6 = "vpnv6"
    evpn = "evpn"


class RecursiveDnsEngine(str, enum.Enum):
    """Which DNS engine the recursive pod renders for. Authoritative
    pods always render to CoreDNS — see hickory-migration plan for why
    the auth side intentionally doesn't move."""

    coredns = "coredns"
    hickory = "hickory"


class Fabric(UUIDPrimaryKey, Timestamped, Base):
    """Top-level network namespace. Maps roughly to an enclave."""

    __tablename__ = "fabrics"
    __table_args__ = (
        Index("ix_fabrics_slug", "slug", unique=True),
    )

    name: Mapped[str] = mapped_column(String(128), nullable=False, unique=True)
    slug: Mapped[str] = mapped_column(String(64), nullable=False)
    description: Mapped[str | None] = mapped_column(String(512))
    # Cross-reference to Site.enclave so the IPAM page can filter sites
    # belonging to this fabric without a separate join.
    enclave: Mapped[str | None] = mapped_column(String(64))
    classification: Mapped[str | None] = mapped_column(String(32))
    # Per-fabric override for the recursive Corefile's catch-all
    # upstreams. Stored as a JSON array of "ip" or "ip:port" strings;
    # NULL = use the system-wide dns_recursive_upstreams setting.
    # Lets multi-tenant installs point each fabric at its own
    # internal resolver estate without a setting-per-fabric.
    dns_recursive_upstreams: Mapped[list[str] | None] = mapped_column(JSON)
    # Per-fabric CIDR allow/deny lists for the recursive listener.
    # Hickory's `allow_networks` (when set) is an allowlist — any
    # client IP not in it gets rejected; `deny_networks` is a
    # blocklist applied even when allow is empty. Operators use these
    # to keep known abuser ranges off the resolver and to scope the
    # recursive to internal client ranges. NOT per-second
    # rate-limiting — Hickory 0.26 has no native QPS limiter; the
    # answer for real DoS protection is still host-side nftables
    # hashlimit or a dnsdist sidecar.
    dns_deny_networks: Mapped[list[str] | None] = mapped_column(JSON)
    dns_allow_networks: Mapped[list[str] | None] = mapped_column(JSON)
    # CIDR allowlist for AXFR-ing the RFC 9432 catalog zone. NULL or
    # empty list means no transfers permitted — the renderer omits
    # both `acl` and `transfer` directives, so CoreDNS's default of
    # "no transfers" keeps the catalog sealed. A non-empty list
    # turns into `acl { allow type AXFR net <cidrs> ; block type
    # AXFR }` plus `transfer { to * }`: the `acl` plugin gates by
    # CIDR (the `transfer` plugin itself only accepts literal IPs or
    # `*`, so we use it as a transport switch and let `acl` do the
    # network filtering). Operators add the source CIDRs of their
    # downstream BIND / Knot / PowerDNS primaries here. TSIG isn't
    # supported by CoreDNS's `transfer` plugin; IP ACL is the
    # security gate today.
    catalog_transfer_acl: Mapped[list[str] | None] = mapped_column(JSON)
    # Recursive DNS engine for this fabric — CoreDNS (default) or
    # Hickory. Authoritative pods always use CoreDNS; only the
    # recursive side moves. Switch is reversible per-fabric.
    recursive_engine: Mapped[RecursiveDnsEngine] = mapped_column(
        Enum(
            RecursiveDnsEngine, name="recursive_dns_engine",
            values_callable=lambda x: [e.value for e in x],
        ),
        default=RecursiveDnsEngine.coredns,
        nullable=False,
    )

    vrfs: Mapped[list[Vrf]] = relationship(back_populates="fabric")


class Vrf(UUIDPrimaryKey, Timestamped, Base):
    """Isolated routing domain inside a fabric. Same address space can
    appear in multiple VRFs; Subnet uniqueness is per-VRF.

    The Route Distinguisher is *not* on the VRF row — RDs are recorded
    per (VRF, BGP peer, address family) in vrf_bgp_peers since a single
    VRF can be advertised with different RDs on different peers / AFs
    (VPNv4 vs VPNv6 vs EVPN). The VRF carries a Route Target instead
    (the import/export extended community shared across all peers
    advertising this VRF)."""

    __tablename__ = "vrfs"
    __table_args__ = (
        UniqueConstraint("fabric_id", "name", name="uq_vrf_fabric_name"),
        Index("ix_vrfs_fabric", "fabric_id"),
    )

    fabric_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("fabrics.id"), nullable=False,
    )
    name: Mapped[str] = mapped_column(String(64), nullable=False)
    # Route Target extended community e.g. "65000:100". Imported + exported
    # by every peer that advertises this VRF.
    route_target: Mapped[str | None] = mapped_column(String(32))
    description: Mapped[str | None] = mapped_column(String(512))
    is_default: Mapped[bool] = mapped_column(Boolean, default=False, nullable=False)

    fabric: Mapped[Fabric] = relationship(back_populates="vrfs")


class VrfBgpPeer(UUIDPrimaryKey, Timestamped, Base):
    """Many-to-many between a VRF and a BgpPeer, per BGP address family.

    The same TCP session (one BgpPeer row) can carry multiple AFs for
    the same VRF, each with its own Route Distinguisher. The unique
    constraint enforces one row per (vrf, peer, AF) tuple."""

    __tablename__ = "vrf_bgp_peers"
    __table_args__ = (
        UniqueConstraint(
            "vrf_id", "bgp_peer_id", "address_family",
            name="uq_vrf_bgp_peer_af",
        ),
        Index("ix_vrf_bgp_peers_vrf", "vrf_id"),
        Index("ix_vrf_bgp_peers_peer", "bgp_peer_id"),
    )

    vrf_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("vrfs.id"), nullable=False,
    )
    # bgp_peers lives in models/dns.py — string FK target avoids a
    # cross-module import cycle (dns.py imports nothing from ipam.py
    # and we want to keep it that way).
    bgp_peer_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("bgp_peers.id"), nullable=False,
    )
    address_family: Mapped[BgpAddressFamily] = mapped_column(
        Enum(
            BgpAddressFamily,
            name="bgp_address_family",
            values_callable=lambda x: [e.value for e in x],
        ),
        nullable=False,
    )
    # Route Distinguisher for this (VRF, peer, AF) tuple e.g. "65000:100".
    # Per-binding so the same VRF can be advertised under different RDs
    # on different peers (multi-PE deployments).
    rd: Mapped[str | None] = mapped_column(String(32))
    enabled: Mapped[bool] = mapped_column(Boolean, default=True, nullable=False)


class Supernet(UUIDPrimaryKey, Timestamped, Base):
    """Aggregate prefix. Can nest under another supernet so operators can
    model 10.0.0.0/8 → 10.0.0.0/20 (site/role aggregate) → 10.0.0.0/24
    (allocatable subnet). Containment for the parent and for child subnets
    is enforced at write time."""

    __tablename__ = "supernets"
    __table_args__ = (
        Index("ix_supernets_fabric_vrf", "fabric_id", "vrf_id"),
        Index("ix_supernets_parent", "parent_supernet_id"),
    )

    fabric_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("fabrics.id"), nullable=False,
    )
    vrf_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("vrfs.id"), nullable=False,
    )
    # Self-FK for nested supernets. Top-level supernets leave this null.
    parent_supernet_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("supernets.id"),
    )
    # Optional site assignment for sub-supernets that represent a per-site
    # carve-out of a larger aggregate. Top-level supernets are typically
    # site-agnostic and leave this null.
    site_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("sites.id"),
    )
    prefix: Mapped[str] = mapped_column(CIDR, nullable=False)
    name: Mapped[str | None] = mapped_column(String(128))
    description: Mapped[str | None] = mapped_column(String(512))
    purpose: Mapped[str | None] = mapped_column(String(32))


class Subnet(UUIDPrimaryKey, Timestamped, Base):
    """Allocatable prefix. Lives inside a Supernet; ip rows live inside it."""

    __tablename__ = "subnets"
    __table_args__ = (
        Index("ix_subnets_supernet", "supernet_id"),
        Index("ix_subnets_site", "site_id"),
        Index("ix_subnets_vrf", "vrf_id"),
        Index("ix_subnets_vni", "vni_id"),
    )

    supernet_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("supernets.id"), nullable=False,
    )
    # Denormalized for fast filtering — also lets us validate same-VRF
    # uniqueness without joining through Supernet on every write.
    fabric_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("fabrics.id"), nullable=False,
    )
    vrf_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("vrfs.id"), nullable=False,
    )
    site_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("sites.id"),
    )
    prefix: Mapped[str] = mapped_column(CIDR, nullable=False)
    name: Mapped[str | None] = mapped_column(String(128))
    description: Mapped[str | None] = mapped_column(String(512))
    purpose: Mapped[str | None] = mapped_column(String(32))  # mgmt|data|storage|oob|other
    vlan_id: Mapped[int | None] = mapped_column(Integer)
    gateway: Mapped[str | None] = mapped_column(INET)
    # Overlay-aware tenant subnets reference an L2 VNI. The validator on the
    # API layer enforces that the VNI's kind is l2 and lives in the same fabric.
    vni_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("vnis.id"),
    )


class IPAddress(UUIDPrimaryKey, Timestamped, Base):
    __tablename__ = "ip_addresses"
    __table_args__ = (
        UniqueConstraint("subnet_id", "address", name="uq_ip_subnet_address"),
        Index("ix_ip_subnet", "subnet_id"),
        Index("ix_ip_asset", "asset_id"),
        Index("ix_ip_address", "address"),
    )

    subnet_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("subnets.id"), nullable=False,
    )
    asset_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("assets.id"),
    )
    address: Mapped[str] = mapped_column(INET, nullable=False)
    role: Mapped[IpAddressRole] = mapped_column(
        Enum(IpAddressRole, name="ip_role", values_callable=lambda x: [e.value for e in x]),
        default=IpAddressRole.data, nullable=False,
    )
    status: Mapped[IpAddressStatus] = mapped_column(
        Enum(IpAddressStatus, name="ip_status", values_callable=lambda x: [e.value for e in x]),
        default=IpAddressStatus.active, nullable=False,
    )
    source: Mapped[IpAddressSource] = mapped_column(
        Enum(IpAddressSource, name="ip_source", values_callable=lambda x: [e.value for e in x]),
        default=IpAddressSource.static, nullable=False,
    )
    dns_name: Mapped[str | None] = mapped_column(String(255))
    description: Mapped[str | None] = mapped_column(String(512))
    # When source=dhcp, this is the lease expiry from Kea; the sync job
    # ages out IPs whose lease has lapsed without churning static rows.
    dhcp_lease_expires_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    dhcp_mac: Mapped[str | None] = mapped_column(String(32))


class Overlay(UUIDPrimaryKey, Timestamped, Base):
    """A VXLAN/GENEVE overlay anchored in a fabric.

    The underlay VRF is the routing instance that carries VTEP-to-VTEP
    traffic (loopbacks, BGP-EVPN sessions). Tenant VNIs ride on top.
    """

    __tablename__ = "overlays"
    __table_args__ = (
        UniqueConstraint("fabric_id", "name", name="uq_overlay_fabric_name"),
        Index("ix_overlays_fabric", "fabric_id"),
    )

    fabric_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("fabrics.id"), nullable=False,
    )
    name: Mapped[str] = mapped_column(String(128), nullable=False)
    kind: Mapped[OverlayKind] = mapped_column(
        Enum(OverlayKind, name="overlay_kind", values_callable=lambda x: [e.value for e in x]),
        default=OverlayKind.vxlan, nullable=False,
    )
    # Default UDP ports: VXLAN 4789, GENEVE 6081. Stored explicitly so an
    # operator can pin a non-standard port without us second-guessing the
    # data plane.
    udp_port: Mapped[int] = mapped_column(Integer, default=4789, nullable=False)
    mtu: Mapped[int | None] = mapped_column(Integer)
    underlay_vrf_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("vrfs.id"),
    )
    description: Mapped[str | None] = mapped_column(String(512))


class Vni(UUIDPrimaryKey, Timestamped, Base):
    """A 24-bit VNI inside an overlay.

    L2 VNIs replace a VLAN broadcast domain; they typically map to one
    VLAN ID on the access side and an EVPN route-target for the control
    plane. L3 VNIs map to a tenant VRF (the EVPN "L3-VNI"); they require
    `vrf_id` and reject `vlan_id`.
    """

    __tablename__ = "vnis"
    __table_args__ = (
        UniqueConstraint("overlay_id", "vni", name="uq_vni_overlay_vni"),
        Index("ix_vnis_overlay", "overlay_id"),
        Index("ix_vnis_vrf", "vrf_id"),
    )

    overlay_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("overlays.id"), nullable=False,
    )
    vni: Mapped[int] = mapped_column(Integer, nullable=False)
    kind: Mapped[VniKind] = mapped_column(
        Enum(VniKind, name="vni_kind", values_callable=lambda x: [e.value for e in x]),
        default=VniKind.l2, nullable=False,
    )
    name: Mapped[str | None] = mapped_column(String(128))
    description: Mapped[str | None] = mapped_column(String(512))
    # L2 VNI fields. Both optional even for L2 — some deployments don't
    # bother with the access VLAN map (pure overlay) and EVPN RT may live
    # in the control plane config.
    vlan_id: Mapped[int | None] = mapped_column(Integer)
    evpn_route_target: Mapped[str | None] = mapped_column(String(64))
    # L3 VNI field — points at the tenant VRF that this L3-VNI carries.
    vrf_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("vrfs.id"),
    )


class Vtep(UUIDPrimaryKey, Timestamped, Base):
    """A device acting as a VXLAN/GENEVE tunnel endpoint."""

    __tablename__ = "vteps"
    __table_args__ = (
        UniqueConstraint("overlay_id", "asset_id", name="uq_vtep_overlay_asset"),
        Index("ix_vteps_overlay", "overlay_id"),
        Index("ix_vteps_asset", "asset_id"),
    )

    overlay_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("overlays.id"), nullable=False,
    )
    asset_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("assets.id"), nullable=False,
    )
    loopback_ip: Mapped[str | None] = mapped_column(INET)
    role: Mapped[VtepRole] = mapped_column(
        Enum(VtepRole, name="vtep_role", values_callable=lambda x: [e.value for e in x]),
        default=VtepRole.leaf, nullable=False,
    )
    description: Mapped[str | None] = mapped_column(String(512))


class VtepVniMembership(UUIDPrimaryKey, Timestamped, Base):
    """Many-to-many: which VNIs each VTEP carries.

    Modeled as a row (rather than a plain association table) so the
    membership itself can be audited and timestamped — useful for
    answering "when did this VTEP start advertising VNI 10010?".
    """

    __tablename__ = "vtep_vni_memberships"
    __table_args__ = (
        UniqueConstraint("vtep_id", "vni_id", name="uq_vtep_vni_membership"),
        Index("ix_vtep_vni_memberships_vtep", "vtep_id"),
        Index("ix_vtep_vni_memberships_vni", "vni_id"),
    )

    vtep_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("vteps.id"), nullable=False,
    )
    vni_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("vnis.id"), nullable=False,
    )


class DhcpServer(UUIDPrimaryKey, Timestamped, Base):
    """Registered Kea DHCP server we ingest leases from."""

    __tablename__ = "dhcp_servers"
    __table_args__ = (
        UniqueConstraint("name", name="uq_dhcp_server_name"),
        Index("ix_dhcp_servers_fabric", "fabric_id"),
    )

    name: Mapped[str] = mapped_column(String(128), nullable=False)
    fabric_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("fabrics.id"), nullable=False,
    )
    # Kea Control Agent endpoint, e.g. http://kea-ctrl-agent:8000
    kea_url: Mapped[str] = mapped_column(String(512), nullable=False)
    # Optional basic-auth for the Control Agent.
    auth_username: Mapped[str | None] = mapped_column(String(128))
    auth_password: Mapped[str | None] = mapped_column(String(512))
    enabled: Mapped[bool] = mapped_column(Boolean, default=True, nullable=False)
    last_sync_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    last_sync_status: Mapped[str | None] = mapped_column(String(32))   # ok|error
    last_sync_error: Mapped[str | None] = mapped_column(String(2048))
    last_sync_lease_count: Mapped[int | None] = mapped_column(Integer)
    # PR 74 — config-push state. Separate from last_sync_* (which tracks
    # the lease-pull direction); collapsing them would obscure which way
    # a Kea failure points.
    last_push_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    last_push_status: Mapped[str | None] = mapped_column(String(32))   # ok|error
    last_push_error: Mapped[str | None] = mapped_column(String(2048))
    # PR 76 — operator-authored base for the bundle endpoint:
    # {"ctrl-agent": {...}, "dhcp4": {...}, "dhcp6": {...}}.
    # DCIM overlays subnet4[]/subnet6[] at render time; everything
    # else passes through verbatim. See migration 0054 for the full
    # shape contract. Name doesn't carry the `_json` suffix the other
    # JSON columns use because the wire shape is the column shape —
    # no property alias dance needed.
    base_config: Mapped[dict] = mapped_column(JSON, nullable=False, default=dict)
    # PR 79 — opt-in: when TRUE, the API schedules a background
    # push_scope after every DhcpScope create/update on this server.
    # DELETE keeps its inline subnet-del call (no row left to push
    # after the response returns). Default FALSE = explicit-push
    # workflow PR 74 shipped.
    auto_push: Mapped[bool] = mapped_column(Boolean, nullable=False, default=False)
    # PR 83 — bundle pre-render cache. Worker task (rerender_dhcp_bundle)
    # writes these; the bundle endpoint reads from them when present
    # and falls back to live render when null. See migration 0058.
    bundle_cache_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    bundle_cache_etag: Mapped[str | None] = mapped_column(String(128))
    bundle_cache_json: Mapped[dict | None] = mapped_column(JSON)


class DhcpScope(UUIDPrimaryKey, Timestamped, Base):
    """DHCPv4 or DHCPv6 scope/subnet definition for a Kea DhcpServer.

    "Scope" (Microsoft term) = "subnet" (ISC Kea term): the prefix +
    pools + options + reservations a DHCP server hands out on that
    network. PR 73 owns the data; a follow-up will push it to Kea via
    the Control Agent's config-set command and read it back via
    config-get.

    Single table with `ip_family` (4 or 6) and a Postgres CIDR
    `prefix` — same shape as the IPAM Subnet table. v6-only fields
    (pd_pools_json, preferred_lifetime_seconds) are NULLed on v4 rows
    and guarded by a CHECK constraint at the DB layer.

    JSON columns mirror Kea's `subnet4`/`subnet6` object structure so
    the future config builder is a near-direct shape projection.
    """

    __tablename__ = "dhcp_scopes"
    __table_args__ = (
        UniqueConstraint("dhcp_server_id", "prefix", name="uq_dhcp_scope_server_prefix"),
        Index("ix_dhcp_scopes_server", "dhcp_server_id"),
        Index("ix_dhcp_scopes_family", "ip_family"),
    )

    dhcp_server_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True),
        ForeignKey("dhcp_servers.id", ondelete="CASCADE"),
        nullable=False,
    )
    # Optional cross-reference to the IPAM Subnet that backs this scope.
    # Useful for UX ("which IPAM subnet does this scope serve?") and
    # for future reservation reconciliation against IPAddress rows.
    subnet_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True),
        ForeignKey("subnets.id", ondelete="SET NULL"),
    )
    name: Mapped[str] = mapped_column(String(128), nullable=False)
    ip_family: Mapped[int] = mapped_column(Integer, nullable=False)
    prefix: Mapped[str] = mapped_column(CIDR, nullable=False)
    # JSON arrays. Pool entries: {"first": "10.0.0.10", "last": "10.0.0.250"}.
    # Reservation entries (v4): {"mac": "...", "ip": "...", "hostname": "..."}.
    # Reservation entries (v6): {"duid": "...", "ip": "...", "hostname": "..."}.
    pools_json: Mapped[list] = mapped_column(JSON, nullable=False, default=list)
    # v6 prefix-delegation pools — null on v4 (CHECK constraint enforces).
    # Entries: {"prefix": "2001:db8:0:100::/56", "delegated_len": 64}.
    pd_pools_json: Mapped[list | None] = mapped_column(JSON)
    # Kea-shape option-data array. Entries:
    # {"name": "routers", "code": 3, "data": "10.0.0.1"}.
    options_json: Mapped[list] = mapped_column(JSON, nullable=False, default=list)
    reservations_json: Mapped[list] = mapped_column(JSON, nullable=False, default=list)
    # PR 78 — now nullable. NULL = inherit from template_id (if any),
    # otherwise the renderer falls back to a hardcoded default.
    valid_lifetime_seconds: Mapped[int | None] = mapped_column(Integer)
    renew_timer_seconds: Mapped[int | None] = mapped_column(Integer)
    rebind_timer_seconds: Mapped[int | None] = mapped_column(Integer)
    # v6-only — null on v4 (CHECK constraint enforces).
    preferred_lifetime_seconds: Mapped[int | None] = mapped_column(Integer)
    enabled: Mapped[bool] = mapped_column(Boolean, default=True, nullable=False)
    description: Mapped[str | None] = mapped_column(String(512))
    # PR 74 — Kea wants numeric subnet IDs. Allocated per-DhcpServer on
    # first push and pinned for the scope's life. NULL = not yet pushed.
    kea_subnet_id: Mapped[int | None] = mapped_column(Integer)
    # PR 78 — optional FK to a DhcpScopeTemplate. NULL = no template
    # inheritance (the scope's stored values render as-is). When set,
    # the push/diff orchestrators merge template defaults under scope
    # overrides before calling the renderer.
    template_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True),
        ForeignKey("dhcp_scope_templates.id", ondelete="SET NULL"),
    )
    # PR 80 — persisted drift state. Written by diff_scope callers
    # (per-scope + diff-all endpoints); reset to in_sync by
    # push_scope on a successful push. Lets LIST surface drift
    # without re-checking each row and powers the push-drifted
    # endpoint.
    last_diff_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    last_diff_status: Mapped[str | None] = mapped_column(String(32))
    last_diff_delta_json: Mapped[dict | None] = mapped_column(JSON)

    # Pydantic-friendly property aliases. The model_validate path in
    # DhcpScopeOut sees these names; the API layer mutates the *_json
    # columns directly so no setters are needed.
    @property
    def pools(self) -> list:
        return self.pools_json or []

    @property
    def pd_pools(self) -> list | None:
        return self.pd_pools_json

    @property
    def options(self) -> list:
        return self.options_json or []

    @property
    def reservations(self) -> list:
        return self.reservations_json or []

    @property
    def last_diff_delta(self) -> dict | None:
        # PR 80 — column has the _json suffix to match the other JSON
        # columns on this table; wire shape drops it for symmetry with
        # last_diff_at / last_diff_status.
        return self.last_diff_delta_json


class DhcpScopeTemplate(UUIDPrimaryKey, Timestamped, Base):
    """Reusable option-bundle + timer defaults a DhcpScope inherits from.

    Fabric-scoped: templates are FK'd to a single fabric so ABAC's
    fabric-scope enforcement applies the same way it does for the
    parent DhcpServer. Family-typed: ip_family pins the option-data
    code space (v4: 1-254; v6: separate set), and the API enforces
    that a scope's family matches its template's.

    See services/dhcp_push.merge_template_into_scope for the merge
    contract: scope values win on conflict; missing scope values fall
    back to template values; missing both falls back to the
    renderer's hardcoded defaults.
    """

    __tablename__ = "dhcp_scope_templates"
    __table_args__ = (
        UniqueConstraint("fabric_id", "name", name="uq_dhcp_scope_template_fabric_name"),
        Index("ix_dhcp_scope_templates_fabric", "fabric_id"),
        Index("ix_dhcp_scope_templates_family", "ip_family"),
    )

    fabric_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True),
        ForeignKey("fabrics.id", ondelete="CASCADE"),
        nullable=False,
    )
    name: Mapped[str] = mapped_column(String(128), nullable=False)
    ip_family: Mapped[int] = mapped_column(Integer, nullable=False)
    options_json: Mapped[list] = mapped_column(JSON, nullable=False, default=list)
    valid_lifetime_seconds: Mapped[int | None] = mapped_column(Integer)
    renew_timer_seconds: Mapped[int | None] = mapped_column(Integer)
    rebind_timer_seconds: Mapped[int | None] = mapped_column(Integer)
    preferred_lifetime_seconds: Mapped[int | None] = mapped_column(Integer)
    description: Mapped[str | None] = mapped_column(String(512))

    @property
    def options(self) -> list:
        return self.options_json or []
