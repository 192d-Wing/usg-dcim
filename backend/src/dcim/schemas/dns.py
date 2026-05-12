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

from pydantic import BaseModel, BeforeValidator, ConfigDict, Field, computed_field

from ..models.dns import (
    AnycastService,
    DnsBlocklistAction,
    DnsHealthCheckProtocol,
    DnsHealthCheckStatus,
    DnsKeyAlgorithm,
    DnsKeyRole,
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
    # Short timers throughout: in a DCIM context the zone is push-driven
    # from central rather than pulled via AXFR/IXFR, so the standard
    # BIND timers don't apply. 900/900/1800/60 lets a stale slave catch
    # up quickly and keeps the negative-cache window short.
    soa_refresh: int = 900
    soa_retry: int = 900
    soa_expire: int = 1800
    soa_minimum: int = 60
    default_ttl: int = 60
    # 0 = manual rotation only (operator clicks Rotate ZSK). Otherwise
    # the worker rotates the active ZSK every N days.
    zsk_rotation_days: int = 0


class DnsZoneCreate(DnsZoneBase):
    pass


class DnsZoneUpdate(BaseModel):
    description: str | None = None
    zsk_rotation_days: int | None = None
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
    signed: bool = False
    created_at: datetime
    updated_at: datetime

    @computed_field
    @property
    def serial(self) -> int:
        """SOA serial number — derived from the zone's last-modified
        timestamp so it always moves forward when records change. Read-
        only: operators don't set this directly. Mirrors the value the
        renderer emits in the zone file's SOA RR."""
        return int(self.updated_at.timestamp())


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
    # When set, this record is only emitted to clients matching the
    # named DnsView's CIDR list (split-horizon). NULL = served as the
    # default fallback to every client.
    view_id: UUID | None = None
    # When set, the renderer drops this record while its health check
    # is `unhealthy`. NULL = always rendered.
    health_check_id: UUID | None = None
    description: str | None = None


class DnsRecordCreate(DnsRecordBase):
    pass


class DnsRecordUpdate(BaseModel):
    name: str | None = None
    ttl: int | None = None
    data: dict | None = None
    view_id: UUID | None = None
    health_check_id: UUID | None = None
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


# ---------- DnsBlocklist ----------

class DnsBlocklistBase(BaseModel):
    name: str
    fabric_id: UUID
    action: DnsBlocklistAction
    sink_ipv4: InetStrOpt = None
    sink_ipv6: InetStrOpt = None
    enabled: bool = True
    description: str | None = None


class DnsBlocklistCreate(DnsBlocklistBase):
    pass


class DnsBlocklistUpdate(BaseModel):
    name: str | None = None
    action: DnsBlocklistAction | None = None
    sink_ipv4: str | None = None
    sink_ipv6: str | None = None
    enabled: bool | None = None
    description: str | None = None


class DnsBlocklistOut(DnsBlocklistBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


class DnsBlocklistEntryBase(BaseModel):
    blocklist_id: UUID
    pattern: str
    description: str | None = None


class DnsBlocklistEntryCreate(BaseModel):
    pattern: str
    description: str | None = None


class DnsBlocklistEntryBulk(BaseModel):
    patterns: list[str] = Field(default_factory=list, min_length=1)


class DnsBlocklistEntryOut(DnsBlocklistEntryBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


# ---------- DnsKey / DNSSEC ----------

class DnsKeyOut(BaseModel):
    """Public-facing key view — never returns private_pem."""
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    zone_id: UUID
    role: DnsKeyRole
    algorithm: DnsKeyAlgorithm
    public_key_b64: str
    key_tag: int
    active_from: datetime
    retired_at: datetime | None
    created_at: datetime


class DnsDsRecordOut(BaseModel):
    """Operator-facing DS record — uploaded to the parent zone's
    operator to chain the trust anchor."""
    key_tag: int
    algorithm: int
    digest_type: int
    digest: str
    rr: str


# ---------- DnsHealthCheck ----------

class DnsHealthCheckBase(BaseModel):
    name: str
    fabric_id: UUID
    target_ip: InetStr
    protocol: DnsHealthCheckProtocol
    port: int | None = None
    path: str = "/"
    interval_seconds: int = Field(default=30, ge=5, le=3600)
    timeout_seconds: int = Field(default=5, ge=1, le=60)
    enabled: bool = True


class DnsHealthCheckCreate(DnsHealthCheckBase):
    pass


class DnsHealthCheckUpdate(BaseModel):
    name: str | None = None
    target_ip: str | None = None
    protocol: DnsHealthCheckProtocol | None = None
    port: int | None = None
    path: str | None = None
    interval_seconds: int | None = None
    timeout_seconds: int | None = None
    enabled: bool | None = None


class DnsHealthCheckOut(DnsHealthCheckBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    status: DnsHealthCheckStatus
    last_checked_at: datetime | None
    last_error: str | None
    created_at: datetime
    updated_at: datetime


class DnsHealthCheckResult(BaseModel):
    """Collector callback after running one probe. The central worker
    falls back to probing checks whose last_checked_at lags, so a
    collector that drops offline doesn't strand a status."""
    status: DnsHealthCheckStatus
    error: str | None = None


# ---------- DnsView ----------

class DnsViewBase(BaseModel):
    name: str
    fabric_id: UUID
    match_cidrs: list[str] = Field(default_factory=list)
    priority: int = 100
    description: str | None = None


class DnsViewCreate(DnsViewBase):
    pass


class DnsViewUpdate(BaseModel):
    name: str | None = None
    match_cidrs: list[str] | None = None
    priority: int | None = None
    description: str | None = None


class DnsViewOut(DnsViewBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


# ---------- DnsServerMetricsSample ----------

class DnsMetricsSampleIn(BaseModel):
    """One scrape from the collector. observed_at defaults to now()
    on the server side if missing — collectors usually omit it."""
    observed_at: datetime | None = None
    interval_seconds: int = Field(ge=1)
    queries: int = Field(ge=0, default=0)
    nxdomain: int = Field(ge=0, default=0)
    servfail: int = Field(ge=0, default=0)
    noerror: int = Field(ge=0, default=0)
    p50_ms: float | None = None
    p95_ms: float | None = None


class DnsMetricsSampleOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    server_id: UUID
    observed_at: datetime
    interval_seconds: int
    queries: int
    nxdomain: int
    servfail: int
    noerror: int
    p50_ms: float | None
    p95_ms: float | None


# ---------- DnsForwarder ----------

class DnsForwarderBase(BaseModel):
    name: str
    fabric_id: UUID
    # Canonicalize with a trailing dot so the recursive Corefile gets a
    # deterministic key — CoreDNS treats `aws.internal` and
    # `aws.internal.` as different zone names.
    zone_pattern: Annotated[str, BeforeValidator(
        lambda v: v if not isinstance(v, str) or v.endswith(".") else f"{v}.",
    )]
    upstreams: list[str] = Field(default_factory=list, min_length=1)
    description: str | None = None


class DnsForwarderCreate(DnsForwarderBase):
    pass


class DnsForwarderUpdate(BaseModel):
    name: str | None = None
    zone_pattern: Annotated[str | None, BeforeValidator(
        lambda v: v if v is None or not isinstance(v, str) or v.endswith(".") else f"{v}.",
    )] = None
    upstreams: list[str] | None = None
    description: str | None = None


class DnsForwarderOut(DnsForwarderBase):
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
    # Which engine renders this server's config. Auth pods are
    # always "coredns"; recursive pods pick up the fabric's
    # recursive_engine. The collector reads this to decide whether
    # corefile contains a Corefile (coredns) or TOML (hickory) and
    # to pick the matching reload signal.
    engine: str = "coredns"
    corefile: str
    zones: dict[str, str]   # filename -> zone-file text
    gobgp: dict | None      # GoBGP YAML (None for auth servers)
    # BIND-format DNSSEC key files (.key + .private pairs) the
    # collector materializes alongside zone files so CoreDNS's dnssec
    # plugin can sign responses. Empty dict for unsigned zones or
    # recursive servers.
    key_files: dict[str, str] = Field(default_factory=dict)


class DnsRenderStatus(BaseModel):
    """What the collector posts back after a render attempt. Mirrors
    the DhcpServer last_sync_* shape."""

    status: Literal["ok", "error"]
    etag: str | None = None
    error: str | None = None
    coredns_version: str | None = None
