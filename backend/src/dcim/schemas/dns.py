"""Pydantic schemas for the DNS subsystem.

The interesting bit is `DnsRecord.data` — a JSON column that carries
type-specific shapes (`{"target": "10.0.0.5"}` for A,
`{"priority": 10, "target": "mail.x"}` for MX, etc.). We validate it
with one schema per record type and let the route handler dispatch on
`type` so a malformed payload can never reach the renderer.
"""

from __future__ import annotations

from datetime import datetime
from typing import Annotated, Any, Literal
from uuid import UUID

from pydantic import BaseModel, BeforeValidator, ConfigDict, Field

from ..models.dns import (
    AnycastService,
    DnsRecordSource,
    DnsRecordType,
    DnsServerRole,
    DnsZoneKind,
)
from .ipam import InetStrOpt, _to_str

InetStr = Annotated[str, BeforeValidator(_to_str)]


# ---------- DnsZone ----------

class DnsZoneBase(BaseModel):
    name: str
    kind: DnsZoneKind
    fabric_id: UUID
    site_id: UUID | None = None
    description: str | None = None
    soa_mname: str = "ns1"
    soa_rname: str = "hostmaster"
    soa_refresh: int = 3600
    soa_retry: int = 600
    soa_expire: int = 604800
    soa_minimum: int = 300
    default_ttl: int = 300


class DnsZoneCreate(DnsZoneBase):
    pass


class DnsZoneUpdate(BaseModel):
    description: str | None = None
    soa_mname: str | None = None
    soa_rname: str | None = None
    soa_refresh: int | None = None
    soa_retry: int | None = None
    soa_expire: int | None = None
    soa_minimum: int | None = None
    default_ttl: int | None = None


class DnsZoneOut(DnsZoneBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


# ---------- DnsRecord.data discriminated union ----------
#
# Each record type gets its own data schema so the API rejects malformed
# payloads at the boundary. The renderer downstream can rely on the
# shape without re-validating.

class _ARData(BaseModel):
    target: InetStr  # IPv4 dotted-quad


class _AAAAData(BaseModel):
    target: InetStr  # IPv6 colon-hex


class _CnameData(BaseModel):
    target: str  # FQDN


class _MxData(BaseModel):
    priority: int = Field(ge=0, le=65535)
    target: str  # mail server FQDN


class _TxtData(BaseModel):
    text: str  # raw quoted text without surrounding quotes


class _SrvData(BaseModel):
    priority: int = Field(ge=0, le=65535)
    weight: int = Field(ge=0, le=65535)
    port: int = Field(ge=1, le=65535)
    target: str  # FQDN of the host providing the service


class _NsData(BaseModel):
    target: str  # FQDN of the nameserver


class _CaaData(BaseModel):
    flags: int = Field(ge=0, le=255)
    tag: str  # "issue" | "issuewild" | "iodef"
    value: str


class _PtrData(BaseModel):
    target: str  # FQDN the address points back to


_DATA_SCHEMAS: dict[DnsRecordType, type[BaseModel]] = {
    DnsRecordType.A: _ARData,
    DnsRecordType.AAAA: _AAAAData,
    DnsRecordType.CNAME: _CnameData,
    DnsRecordType.MX: _MxData,
    DnsRecordType.TXT: _TxtData,
    DnsRecordType.SRV: _SrvData,
    DnsRecordType.NS: _NsData,
    DnsRecordType.CAA: _CaaData,
    DnsRecordType.PTR: _PtrData,
}


def validate_record_data(record_type: DnsRecordType | str, data: Any) -> dict:
    """Validate `data` against the schema for `record_type` and return
    the normalized dict. Raised errors propagate up as Pydantic
    ValidationError, which the API layer translates to a 422."""
    rt = DnsRecordType(record_type) if isinstance(record_type, str) else record_type
    schema = _DATA_SCHEMAS[rt]
    return schema(**(data or {})).model_dump()


# ---------- DnsRecord ----------

class DnsRecordBase(BaseModel):
    zone_id: UUID
    name: str
    type: DnsRecordType
    ttl: int | None = None
    data: dict
    description: str | None = None


class DnsRecordCreate(DnsRecordBase):
    pass


class DnsRecordUpdate(BaseModel):
    name: str | None = None
    ttl: int | None = None
    data: dict | None = None
    description: str | None = None


class DnsRecordOut(DnsRecordBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    source: DnsRecordSource
    ipam_address_id: UUID | None
    created_at: datetime
    updated_at: datetime


# ---------- AnycastGroup ----------

class AnycastGroupBase(BaseModel):
    name: str
    fabric_id: UUID
    service: AnycastService
    anycast_ipv4: InetStrOpt = None
    anycast_ipv6: InetStrOpt = None
    description: str | None = None


class AnycastGroupCreate(AnycastGroupBase):
    pass


class AnycastGroupUpdate(BaseModel):
    name: str | None = None
    anycast_ipv4: str | None = None
    anycast_ipv6: str | None = None
    description: str | None = None


class AnycastGroupOut(AnycastGroupBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


# ---------- BgpPeer ----------

class BgpPeerBase(BaseModel):
    name: str
    site_id: UUID
    # AS numbers reference the ASN catalog (bgp_asns). The API layer
    # cross-checks both FKs at create/patch time.
    local_asn_id: UUID
    peer_asn_id: UUID
    peer_ip: InetStr
    peer_description: str | None = None
    # Optional TCP AO key chain reference (RFC 5925). MD5 password is
    # deprecated and no longer surfaced.
    tcp_ao_key_chain_id: UUID | None = None
    enabled: bool = True


class BgpPeerCreate(BgpPeerBase):
    pass


class BgpPeerUpdate(BaseModel):
    name: str | None = None
    local_asn_id: UUID | None = None
    peer_asn_id: UUID | None = None
    peer_ip: str | None = None
    peer_description: str | None = None
    tcp_ao_key_chain_id: UUID | None = None
    enabled: bool | None = None


class BgpPeerOut(BgpPeerBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


# ---------- DnsServer ----------

class DnsServerBase(BaseModel):
    name: str
    site_id: UUID
    fabric_id: UUID
    role: DnsServerRole
    unicast_ip: InetStr
    enabled: bool = True
    anycast_group_id: UUID | None = None


class DnsServerCreate(DnsServerBase):
    pass


class DnsServerUpdate(BaseModel):
    name: str | None = None
    enabled: bool | None = None
    unicast_ip: str | None = None
    anycast_group_id: UUID | None = None


class DnsServerOut(DnsServerBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    last_render_at: datetime | None
    last_render_status: str | None
    last_render_error: str | None
    last_render_etag: str | None
    coredns_version: str | None
    created_at: datetime
    updated_at: datetime


# ---------- AnycastBgpBinding ----------

class AnycastBgpBindingCreate(BaseModel):
    dns_server_id: UUID
    bgp_peer_id: UUID


class AnycastBgpBindingOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    dns_server_id: UUID
    bgp_peer_id: UUID
    created_at: datetime
    updated_at: datetime


# ---------- Render bundle (response from /dns/servers/{id}/bundle) ----------

class DnsBundle(BaseModel):
    """What the collector pulls every poll cycle. Etag lets the
    collector short-circuit no-op renders."""

    etag: str
    corefile: str
    zones: dict[str, str]   # filename -> zone-file text
    gobgp: dict | None      # GoBGP YAML (None for auth servers)


class DnsRenderStatus(BaseModel):
    """What the collector posts back after a render attempt. Mirrors
    the DhcpServer last_sync_* shape."""

    status: Literal["ok", "error"]
    etag: str | None = None
    error: str | None = None
    coredns_version: str | None = None
