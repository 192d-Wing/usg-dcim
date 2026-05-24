"""Pydantic schemas for the IPAM hierarchy."""

from __future__ import annotations

from datetime import datetime
from typing import Annotated, Any
from uuid import UUID

from pydantic import BaseModel, BeforeValidator, ConfigDict, Field, field_validator

from ..models.ipam import (
    BgpAddressFamily,
    IpAddressRole,
    IpAddressSource,
    IpAddressStatus,
    OverlayKind,
    RecursiveDnsEngine,
    VniKind,
    VtepRole,
)


def _to_str(v: Any) -> Any:
    """asyncpg returns CIDR/INET columns as ipaddress.IPv*Network/Address
    objects. The Out schemas want strings — coerce on the way out while
    still accepting plain strings on the way in."""
    if v is None or isinstance(v, str):
        return v
    return str(v)


CidrStr = Annotated[str, BeforeValidator(_to_str)]
InetStrOpt = Annotated[str | None, BeforeValidator(_to_str)]

# ---------- Fabric ----------

class FabricBase(BaseModel):
    name: str
    slug: str
    description: str | None = None
    enclave: str | None = None
    classification: str | None = None
    # Per-fabric override for the recursive Corefile's catch-all
    # forward. NULL falls back to settings.dns_recursive_upstreams.
    dns_recursive_upstreams: list[str] | None = None
    # CIDR allow/deny lists rendered into the Hickory recursive's
    # top-level `allow_networks` / `deny_networks` settings. Null
    # means "no restriction" — the recursive answers any client
    # that reaches it. Empty list also means no restriction (an
    # explicit `allow_networks = []` would lock everyone out, which
    # is rarely what an operator wants when they fat-finger the form).
    dns_deny_networks: list[str] | None = None
    dns_allow_networks: list[str] | None = None
    # CIDR allowlist for the RFC 9432 catalog zone's AXFR.
    # NULL/empty → no transfers permitted (renderer omits the AXFR
    # directives entirely, so CoreDNS's default closed posture
    # holds). Non-empty → rendered into an `acl { allow type AXFR
    # net <cidrs> }` gate paired with `transfer { to * }`; CoreDNS's
    # `transfer` plugin doesn't accept CIDRs natively, so `acl` does
    # the CIDR filtering and `transfer` is just the on/off switch.
    catalog_transfer_acl: list[str] | None = None
    # Which engine renders this fabric's recursive pod. Authoritative
    # always uses CoreDNS — see hickory-migration plan.
    recursive_engine: RecursiveDnsEngine = RecursiveDnsEngine.coredns


class FabricCreate(FabricBase):
    pass


class FabricUpdate(BaseModel):
    name: str | None = None
    slug: str | None = None
    description: str | None = None
    enclave: str | None = None
    classification: str | None = None
    dns_recursive_upstreams: list[str] | None = None
    dns_deny_networks: list[str] | None = None
    dns_allow_networks: list[str] | None = None
    catalog_transfer_acl: list[str] | None = None
    recursive_engine: RecursiveDnsEngine | None = None


class FabricOut(FabricBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


# ---------- VRF ----------

class VrfBase(BaseModel):
    fabric_id: UUID
    name: str
    # Route Target extended community (e.g. "65000:100"). Imported and
    # exported by every peer advertising this VRF. The Route Distinguisher
    # lives per-binding on VrfBgpPeer.
    route_target: str | None = None
    description: str | None = None
    is_default: bool = False


class VrfCreate(VrfBase):
    pass


class VrfUpdate(BaseModel):
    name: str | None = None
    route_target: str | None = None
    description: str | None = None
    is_default: bool | None = None


class VrfOut(VrfBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


# ---------- VrfBgpPeer (many-to-many between VRF and BgpPeer) ----------

class VrfBgpPeerBase(BaseModel):
    vrf_id: UUID
    bgp_peer_id: UUID
    address_family: BgpAddressFamily
    # Route Distinguisher for this (vrf, peer, AF) binding. Optional —
    # some operators carry the VRF without an explicit RD.
    rd: str | None = None
    enabled: bool = True


class VrfBgpPeerCreate(VrfBgpPeerBase):
    pass


class VrfBgpPeerUpdate(BaseModel):
    # vrf_id, bgp_peer_id, and address_family are immutable post-create
    # (changing them would silently break the unique key). Operators
    # delete + recreate the binding to repoint.
    rd: str | None = None
    enabled: bool | None = None


class VrfBgpPeerOut(VrfBgpPeerBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


# ---------- Supernet ----------

class SupernetBase(BaseModel):
    fabric_id: UUID
    vrf_id: UUID
    parent_supernet_id: UUID | None = None
    site_id: UUID | None = None
    prefix: CidrStr
    name: str | None = None
    description: str | None = None
    purpose: str | None = None


class SupernetCreate(SupernetBase):
    pass


class SupernetUpdate(BaseModel):
    parent_supernet_id: UUID | None = None
    site_id: UUID | None = None
    name: str | None = None
    description: str | None = None
    purpose: str | None = None


class SupernetOut(SupernetBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


# ---------- Subnet ----------

class SubnetBase(BaseModel):
    supernet_id: UUID
    site_id: UUID | None = None
    vni_id: UUID | None = None
    prefix: CidrStr
    name: str | None = None
    description: str | None = None
    purpose: str | None = None
    vlan_id: int | None = None
    gateway: InetStrOpt = None


class SubnetCreate(SubnetBase):
    pass


class SubnetUpdate(BaseModel):
    # Re-parenting a subnet (moving it to a new supernet) goes through the
    # same containment + per-VRF uniqueness checks that creation does.
    # Used by the IPAM tree's drag-and-drop.
    supernet_id: UUID | None = None
    site_id: UUID | None = None
    vni_id: UUID | None = None
    name: str | None = None
    description: str | None = None
    purpose: str | None = None
    vlan_id: int | None = None
    gateway: str | None = None


class SubnetOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    supernet_id: UUID
    fabric_id: UUID
    vrf_id: UUID
    site_id: UUID | None
    vni_id: UUID | None
    prefix: CidrStr
    name: str | None
    description: str | None
    purpose: str | None
    vlan_id: int | None
    gateway: InetStrOpt
    created_at: datetime
    updated_at: datetime


class SubnetUtilization(BaseModel):
    subnet_id: UUID
    prefix: CidrStr
    capacity: int
    allocated: int
    free: int
    percent: float
    next_available: str | None


# ---------- IPAddress ----------

class IPAddressBase(BaseModel):
    subnet_id: UUID
    asset_id: UUID | None = None
    address: CidrStr
    role: IpAddressRole = IpAddressRole.data
    status: IpAddressStatus = IpAddressStatus.active
    source: IpAddressSource = IpAddressSource.static
    dns_name: str | None = None
    description: str | None = None
    dhcp_lease_expires_at: datetime | None = None
    dhcp_mac: str | None = None


class IPAddressCreate(IPAddressBase):
    pass


class IPAddressUpdate(BaseModel):
    asset_id: UUID | None = None
    role: IpAddressRole | None = None
    status: IpAddressStatus | None = None
    dns_name: str | None = None
    description: str | None = None


class IPAddressOut(IPAddressBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


# ---------- DhcpServer ----------

class DhcpServerBase(BaseModel):
    name: str
    fabric_id: UUID
    kea_url: str
    auth_username: str | None = None
    enabled: bool = True
    # PR 76 — operator-authored base for the bundle assembler.
    # Structured as {"ctrl-agent": {...}, "dhcp4": {...}, "dhcp6": {...}};
    # see services/dhcp_bundle.py for the merge contract.
    base_config: dict = Field(default_factory=dict)


class DhcpServerCreate(DhcpServerBase):
    auth_password: str | None = None


class DhcpServerUpdate(BaseModel):
    name: str | None = None
    kea_url: str | None = None
    auth_username: str | None = None
    auth_password: str | None = None
    enabled: bool | None = None
    base_config: dict | None = None


class DhcpServerOut(DhcpServerBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    last_sync_at: datetime | None
    last_sync_status: str | None
    last_sync_error: str | None
    last_sync_lease_count: int | None
    # PR 74 — config-push state (the opposite direction from last_sync_*).
    last_push_at: datetime | None = None
    last_push_status: str | None = None
    last_push_error: str | None = None
    created_at: datetime
    updated_at: datetime


# ---------- DhcpScope (PR 73) ----------
# Kea-shaped DHCPv4/v6 subnet definition. The data model is unified
# (single table with ip_family discriminator) but the schemas guard
# family-specific fields with validators so v4 callers can't send
# pd_pools and v6 callers can't omit preferred_lifetime.

class DhcpPool(BaseModel):
    """One {first, last} address range pool inside a scope."""
    first: str
    last: str


class DhcpPdPool(BaseModel):
    """One DHCPv6 prefix-delegation pool. delegated_len is the prefix
    length handed to clients (e.g. /64); prefix is the supernet the
    pool delegates from (e.g. 2001:db8::/56)."""
    prefix: str
    delegated_len: int


class DhcpOption(BaseModel):
    """Kea option-data entry. Code is the DHCP option number
    (v4: 1-254; v6: separate space). One of code/name must be set;
    when both are, name is informational."""
    code: int | None = None
    name: str | None = None
    data: str
    space: str | None = None  # Kea option space override, rare


class DhcpReservation(BaseModel):
    """One client reservation. v4 uses `mac`; v6 uses `duid`. The API
    layer rejects rows with the wrong identifier for the family."""
    mac: str | None = None
    duid: str | None = None
    ip: str
    hostname: str | None = None


class DhcpScopeBase(BaseModel):
    dhcp_server_id: UUID
    subnet_id: UUID | None = None
    name: str
    ip_family: int  # 4 or 6
    prefix: str
    pools: list[DhcpPool] = Field(default_factory=list)
    pd_pools: list[DhcpPdPool] | None = None
    options: list[DhcpOption] = Field(default_factory=list)
    reservations: list[DhcpReservation] = Field(default_factory=list)
    valid_lifetime_seconds: int = 3600
    renew_timer_seconds: int | None = None
    rebind_timer_seconds: int | None = None
    preferred_lifetime_seconds: int | None = None
    enabled: bool = True
    description: str | None = None

    @field_validator("ip_family")
    @classmethod
    def _family_must_be_4_or_6(cls, v: int) -> int:
        if v not in (4, 6):
            raise ValueError("ip_family must be 4 or 6")
        return v


class DhcpScopeCreate(DhcpScopeBase):
    pass


class DhcpScopeUpdate(BaseModel):
    # ip_family + prefix + dhcp_server_id are immutable post-create.
    name: str | None = None
    subnet_id: UUID | None = None
    pools: list[DhcpPool] | None = None
    pd_pools: list[DhcpPdPool] | None = None
    options: list[DhcpOption] | None = None
    reservations: list[DhcpReservation] | None = None
    valid_lifetime_seconds: int | None = None
    renew_timer_seconds: int | None = None
    rebind_timer_seconds: int | None = None
    preferred_lifetime_seconds: int | None = None
    enabled: bool | None = None
    description: str | None = None


class DhcpScopeOut(DhcpScopeBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    # PR 74 — populated on first push, used as the Kea subnet ID for
    # all subsequent subnet4-update / subnet4-del calls.
    kea_subnet_id: int | None = None
    created_at: datetime
    updated_at: datetime


# ---------- Overlay ----------

class OverlayBase(BaseModel):
    fabric_id: UUID
    name: str
    kind: OverlayKind = OverlayKind.vxlan
    udp_port: int = 4789
    mtu: int | None = None
    underlay_vrf_id: UUID | None = None
    description: str | None = None


class OverlayCreate(OverlayBase):
    pass


class OverlayUpdate(BaseModel):
    name: str | None = None
    kind: OverlayKind | None = None
    udp_port: int | None = None
    mtu: int | None = None
    underlay_vrf_id: UUID | None = None
    description: str | None = None


class OverlayOut(OverlayBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


# ---------- VNI ----------

class VniBase(BaseModel):
    overlay_id: UUID
    vni: int
    kind: VniKind = VniKind.l2
    name: str | None = None
    description: str | None = None
    vlan_id: int | None = None
    evpn_route_target: str | None = None
    vrf_id: UUID | None = None


class VniCreate(VniBase):
    pass


class VniUpdate(BaseModel):
    name: str | None = None
    description: str | None = None
    vlan_id: int | None = None
    evpn_route_target: str | None = None
    # kind/vrf changes can break existing subnet bindings, so they're
    # accepted but re-validated server-side just like a create.
    kind: VniKind | None = None
    vrf_id: UUID | None = None


class VniOut(VniBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


# ---------- VTEP ----------

class VtepBase(BaseModel):
    overlay_id: UUID
    asset_id: UUID
    loopback_ip: InetStrOpt = None
    role: VtepRole = VtepRole.leaf
    description: str | None = None


class VtepCreate(VtepBase):
    pass


class VtepUpdate(BaseModel):
    loopback_ip: str | None = None
    role: VtepRole | None = None
    description: str | None = None


class VtepOut(VtepBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


# ---------- VTEP ↔ VNI membership ----------

class VtepVniMembershipCreate(BaseModel):
    vtep_id: UUID
    vni_id: UUID


class VtepVniMembershipOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    vtep_id: UUID
    vni_id: UUID
    created_at: datetime
    updated_at: datetime
