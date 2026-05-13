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
    BigInteger,
    Boolean,
    CheckConstraint,
    DateTime,
    Enum,
    Float,
    ForeignKey,
    Index,
    Integer,
    String,
    UniqueConstraint,
)
from sqlalchemy.dialects.postgresql import INET, JSONB
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
    # IPv4 /24 (in-addr.arpa) or IPv6 /64 (ip6.arpa) reverse zones, one
    # per (site, classful-or-aligned prefix) combination. Auto-created
    # and populated by the IPAM projector — operators rarely touch
    # them, but they show up in the hosted-zones list so PTR rdata is
    # discoverable.
    reverse = "reverse"


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
    # Operator-managed records — A, CNAME, MX, TXT, etc. that
    # operators add through the UI or API. Survive all sync passes.
    manual = "manual"
    # Statically-allocated IPAddress rows whose dns_name field is set.
    # Replaced on every projector cycle.
    ipam = "ipam"
    # DHCP-driven projections — the parent IPAddress has source=dhcp,
    # so the DNS record's lifetime tracks the lease. Operators can't
    # delete these directly; clearing the lease (or its hostname)
    # makes them disappear on the next sync.
    ddns = "ddns"


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
        # Zone names must be globally unique (uq_dns_zone_name); no
        # other restrictions on count or shape. A fabric may carry any
        # mix of apex + site-kind zones, and a site may host multiple
        # site-kind zones.
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
    soa_refresh: Mapped[int] = mapped_column(Integer, nullable=False, default=900)
    soa_retry: Mapped[int] = mapped_column(Integer, nullable=False, default=900)
    soa_expire: Mapped[int] = mapped_column(Integer, nullable=False, default=1800)
    soa_minimum: Mapped[int] = mapped_column(Integer, nullable=False, default=60)
    default_ttl: Mapped[int] = mapped_column(Integer, nullable=False, default=60)
    # DNSSEC: when true, the renderer emits the dnssec plugin and
    # includes DNSKEY records. Keys live in dns_keys (KSK + ZSK).
    signed: Mapped[bool] = mapped_column(Boolean, default=False, nullable=False)
    # Auto-rotation policy for the ZSK. 0 = off (operator rotates by
    # hand via the UI button). When > 0, the worker rotates the active
    # ZSK every N days. KSK rotation stays manual because it requires
    # the operator to upload the new DS to the parent zone.
    zsk_rotation_days: Mapped[int] = mapped_column(Integer, nullable=False, default=0)
    # NSEC3: when nsec3_salt is non-NULL the renderer emits the
    # `nsec3sign` plugin (custom CoreDNS image required) instead of
    # the upstream `dnssec` plugin. NULL keeps the NSEC path so this
    # migration is a no-op for existing signed zones. RFC 9276 §3.1
    # recommends empty salt + zero iterations; the column enforces
    # the choice but doesn't constrain it.
    nsec3_salt: Mapped[str | None] = mapped_column(String(64))
    nsec3_iterations: Mapped[int] = mapped_column(Integer, nullable=False, default=0)
    nsec3_opt_out: Mapped[bool] = mapped_column(Boolean, nullable=False, default=False)
    # Operator write lock for maintenance windows. When true every
    # mutation against this zone or its records (record CRUD, BIND
    # import, IPAM sync, DNSSEC operations, NSEC3 toggle, key delete,
    # zone PATCH/DELETE) returns 422 with a "zone is frozen" message.
    # Toggled exclusively through POST /freeze + /unfreeze so the
    # state change always lands in audit; PATCH /dns/zones does NOT
    # accept this field.
    frozen: Mapped[bool] = mapped_column(Boolean, nullable=False, default=False)


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
    # Optional split-horizon binding — when set, this record is only
    # served to clients matching the view's CIDR list. NULL = visible
    # to every view (the default fallback answer).
    view_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("dns_views.id", ondelete="SET NULL"),
    )
    # Optional health check — when set and the check is unhealthy, the
    # renderer drops this record from the rendered zone. NULL = always
    # rendered (no health gating).
    health_check_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True),
        ForeignKey("dns_health_checks.id", ondelete="SET NULL"),
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


class DnsKeyRole(str, enum.Enum):
    """KSK signs the DNSKEY rrset (its DS lives in the parent zone);
    ZSK signs everything else. Standard BIND/NIST-recommended split."""

    ksk = "ksk"
    zsk = "zsk"


class DnsKeyAlgorithm(str, enum.Enum):
    """RFC 8624 SHOULD/MAY algorithms. v1 ships ECDSAP256SHA256 only —
    short keys, broad resolver support; we surface the enum so RSA can
    drop in without a schema change."""

    ecdsap256sha256 = "ecdsap256sha256"
    ed25519 = "ed25519"
    rsasha256 = "rsasha256"


class DnsKey(UUIDPrimaryKey, Timestamped, Base):
    """A DNSSEC key bound to exactly one scope: a DnsZone (zone_id)
    or a DnsCatalogZone (catalog_id). Exactly one FK is non-null —
    the CHECK constraint enforces this. Public + private halves live
    in Postgres for v1 (encrypted-at-rest hardening deferred)."""

    __tablename__ = "dns_keys"
    __table_args__ = (
        Index("ix_dns_keys_zone", "zone_id"),
        Index("ix_dns_keys_catalog", "catalog_id"),
        # Enforce exactly one scope: zone XOR catalog.
        CheckConstraint(
            "(zone_id IS NOT NULL) != (catalog_id IS NOT NULL)",
            name="ck_dns_keys_scope",
        ),
    )

    zone_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True),
        ForeignKey("dns_zones.id", ondelete="CASCADE"),
        nullable=True,
    )
    # Nullable FK for catalog-zone keys. Mutually exclusive with
    # zone_id per ck_dns_keys_scope.
    catalog_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True),
        ForeignKey("dns_catalog_zones.id", ondelete="CASCADE"),
        nullable=True,
    )
    role: Mapped[DnsKeyRole] = mapped_column(
        Enum(
            DnsKeyRole, name="dns_key_role",
            values_callable=lambda x: [e.value for e in x],
        ),
        nullable=False,
    )
    algorithm: Mapped[DnsKeyAlgorithm] = mapped_column(
        Enum(
            DnsKeyAlgorithm, name="dns_key_algorithm",
            values_callable=lambda x: [e.value for e in x],
        ),
        nullable=False,
    )
    # PEM-serialized private key (TODO: encrypt-at-rest before any
    # production deployment).
    private_pem: Mapped[str] = mapped_column(String, nullable=False)
    # Base64-encoded public-key bytes per RFC 4034 §2.1.1 — same form
    # as the DNSKEY rdata's "Public Key" field.
    public_key_b64: Mapped[str] = mapped_column(String, nullable=False)
    # DNSSEC key tag (RFC 4034 Appendix B); cached so we don't
    # recompute on every render.
    key_tag: Mapped[int] = mapped_column(Integer, nullable=False)
    active_from: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), nullable=False,
    )
    retired_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))


class DnsHealthCheckProtocol(str, enum.Enum):
    tcp = "tcp"
    http = "http"
    https = "https"
    icmp = "icmp"


class DnsHealthCheckStatus(str, enum.Enum):
    """Latest observed health state. `unknown` is the start condition
    before the first probe has run."""

    unknown = "unknown"
    healthy = "healthy"
    unhealthy = "unhealthy"


class DnsHealthCheck(UUIDPrimaryKey, Timestamped, Base):
    """A liveness probe operators bind to one or more DnsRecord rows.
    The probe runs in the central worker (not the collector), so this
    only works for targets central can reach directly. Records whose
    health-check is `unhealthy` are excluded from the rendered zone."""

    __tablename__ = "dns_health_checks"
    __table_args__ = (
        Index("ix_dns_health_checks_fabric", "fabric_id"),
    )

    name: Mapped[str] = mapped_column(String(128), nullable=False)
    fabric_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("fabrics.id", ondelete="CASCADE"),
        nullable=False,
    )
    target_ip: Mapped[str] = mapped_column(INET, nullable=False)
    protocol: Mapped[DnsHealthCheckProtocol] = mapped_column(
        Enum(
            DnsHealthCheckProtocol, name="dns_health_check_protocol",
            values_callable=lambda x: [e.value for e in x],
        ),
        nullable=False,
    )
    port: Mapped[int | None] = mapped_column(Integer)
    # HTTP path for protocol in {http, https}. Default `/` matches most
    # health-endpoint conventions.
    path: Mapped[str] = mapped_column(String(255), nullable=False, default="/")
    interval_seconds: Mapped[int] = mapped_column(Integer, nullable=False, default=30)
    timeout_seconds: Mapped[int] = mapped_column(Integer, nullable=False, default=5)
    enabled: Mapped[bool] = mapped_column(Boolean, default=True, nullable=False)
    # Observed state — updated by the worker every interval.
    status: Mapped[DnsHealthCheckStatus] = mapped_column(
        Enum(
            DnsHealthCheckStatus, name="dns_health_check_status",
            values_callable=lambda x: [e.value for e in x],
        ),
        default=DnsHealthCheckStatus.unknown,
        nullable=False,
    )
    last_checked_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    last_error: Mapped[str | None] = mapped_column(String(512))


class DnsView(UUIDPrimaryKey, Timestamped, Base):
    """A split-horizon view — same FQDN, different answers depending
    on the client subnet. Records can be bound to a view via
    DnsRecord.view_id; records with view_id IS NULL are the "default
    view" answer served to anyone not matching a specific view."""

    __tablename__ = "dns_views"
    __table_args__ = (
        UniqueConstraint("fabric_id", "name", name="uq_dns_view_fabric_name"),
        Index("ix_dns_views_fabric", "fabric_id"),
    )

    name: Mapped[str] = mapped_column(String(64), nullable=False)
    fabric_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("fabrics.id", ondelete="CASCADE"),
        nullable=False,
    )
    # Stored as JSON array of CIDR strings. Postgres has a CIDR[] type
    # we could use, but JSON keeps the schema portable + lets us add
    # IPv4 and IPv6 entries to the same row without extra plumbing.
    match_cidrs: Mapped[list[str]] = mapped_column(JSON, nullable=False, default=list)
    # When two views could match a client, the higher-priority view
    # wins. Lower numbers = higher priority; defaults to 100 so
    # operators can stack new views without shuffling.
    priority: Mapped[int] = mapped_column(Integer, nullable=False, default=100)
    description: Mapped[str | None] = mapped_column(String(512))


class DnsServerMetricsSample(UUIDPrimaryKey, Timestamped, Base):
    """One scrape of a DnsServer's CoreDNS Prometheus endpoint.

    Stored as per-interval deltas (not raw cumulative counters) so the
    UI can show "what happened in this window" without doing diffs.
    Collector computes the diff locally between successive scrapes.
    """

    __tablename__ = "dns_server_metrics_samples"
    __table_args__ = (
        Index("ix_dns_metrics_server_observed", "server_id", "observed_at"),
    )

    server_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True),
        ForeignKey("dns_servers.id", ondelete="CASCADE"),
        nullable=False,
    )
    observed_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), nullable=False,
    )
    # Size of the interval this sample summarises, in seconds. Lets
    # the UI compute QPS without assuming a fixed scrape cadence.
    interval_seconds: Mapped[int] = mapped_column(Integer, nullable=False)
    # Total request count over the interval, plus per-rcode breakdowns
    # for the ones operators care about most. Anything not enumerated
    # is folded into noerror (NOERROR is the long tail of success).
    queries: Mapped[int] = mapped_column(BigInteger, nullable=False, default=0)
    nxdomain: Mapped[int] = mapped_column(BigInteger, nullable=False, default=0)
    servfail: Mapped[int] = mapped_column(BigInteger, nullable=False, default=0)
    noerror: Mapped[int] = mapped_column(BigInteger, nullable=False, default=0)
    # Response-time percentiles from CoreDNS's duration histogram, in
    # milliseconds. Nullable so the collector can omit them when the
    # histogram has too few samples to be meaningful.
    p50_ms: Mapped[float | None] = mapped_column(Float)
    p95_ms: Mapped[float | None] = mapped_column(Float)
    # Top-K (name, type, count) tuples observed on this server during
    # the interval. Sourced from the resolver's dnstap stream, which
    # the collector reads off a UNIX socket on the shared volume.
    # Null when the collector hasn't yet been wired to dnstap (or the
    # operator opted out via config); the dashboard treats null and
    # an empty list as "no data" interchangeably.
    top_names: Mapped[list[dict] | None] = mapped_column(JSONB)


class DnsCatalogZone(UUIDPrimaryKey, Timestamped, Base):
    """RFC 9432 catalog zone for a fabric. Lets external BIND / Knot /
    PowerDNS primaries auto-provision the set of authoritative zones
    DCIM owns by AXFR-ing this catalog and reading its member-zone
    entries.

    One catalog per fabric (unique constraint on `fabric_id`) —
    matches every other DCIM scoping boundary. The catalog itself is
    a regular DNS zone served by the existing auth CoreDNS pod
    (see `services/dns.py::render_catalog_zone`); consumers point
    their `also-notify` / AXFR config at the auth pod's management
    IP, same as they'd consume any member zone.

    Members are computed at render time from the fabric's
    `DnsZone` rows — apex + site + reverse, with `frozen=true` zones
    elided so a mid-maintenance zone doesn't propagate to consumers
    that would then try to AXFR it from a pod that may be offline.
    """

    __tablename__ = "dns_catalog_zones"
    __table_args__ = (
        # One catalog per fabric. Multi-tenant deployments consume
        # multiple catalogs (one per fabric); cross-fabric catalogs
        # are explicitly out of v1 scope.
        UniqueConstraint("fabric_id", name="uq_dns_catalog_fabric"),
        # Operators with unusual conventions can override the
        # default name; uniqueness guards against two fabrics
        # claiming the same catalog apex.
        UniqueConstraint("name", name="uq_dns_catalog_name"),
        Index("ix_dns_catalog_fabric", "fabric_id"),
    )

    fabric_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True),
        ForeignKey("fabrics.id", ondelete="CASCADE"),
        nullable=False,
    )
    # Catalog apex name. The API/UI default the column to
    # `catalog.<fabric-apex>` when the operator first enables it;
    # the column stores the resolved value so a later fabric-apex
    # change doesn't silently rename the catalog out from under
    # downstream consumers.
    name: Mapped[str] = mapped_column(String(253), nullable=False)
    enabled: Mapped[bool] = mapped_column(
        Boolean, nullable=False, default=True,
    )
    # DNSSEC follows the fabric's NSEC3 default when the catalog
    # is first created — operators can flip it later via the
    # standard zone-signing endpoints once they've staged the DS
    # records at the catalog's parent zone.
    signed: Mapped[bool] = mapped_column(
        Boolean, nullable=False, default=False,
    )


class DnsBlocklistAction(str, enum.Enum):
    """What the recursive does when a query matches a blocklist entry.

    - block:    return NXDOMAIN (request never reaches a real resolver)
    - sinkhole: return a canned A/AAAA pointing at a captive landing IP
    """

    block = "block"
    sinkhole = "sinkhole"


class DnsBlocklist(UUIDPrimaryKey, Timestamped, Base):
    """A named bucket of patterns the recursive should refuse or
    redirect. Patterns roll into one `template` block in the recursive
    Corefile per blocklist. Useful for threat-feed integrations (one
    blocklist per feed) and ad-hoc operator rules."""

    __tablename__ = "dns_blocklists"
    __table_args__ = (
        UniqueConstraint("fabric_id", "name", name="uq_dns_blocklist_fabric_name"),
        Index("ix_dns_blocklists_fabric", "fabric_id"),
    )

    name: Mapped[str] = mapped_column(String(128), nullable=False)
    fabric_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("fabrics.id"), nullable=False,
    )
    action: Mapped[DnsBlocklistAction] = mapped_column(
        Enum(
            DnsBlocklistAction, name="dns_blocklist_action",
            values_callable=lambda x: [e.value for e in x],
        ),
        nullable=False,
    )
    # When action=sinkhole, A queries are answered with sink_ipv4 and
    # AAAA with sink_ipv6. At least one must be set; the renderer skips
    # AFs with no sink.
    sink_ipv4: Mapped[str | None] = mapped_column(INET)
    sink_ipv6: Mapped[str | None] = mapped_column(INET)
    enabled: Mapped[bool] = mapped_column(Boolean, default=True, nullable=False)
    description: Mapped[str | None] = mapped_column(String(512))


class DnsBlocklistEntry(UUIDPrimaryKey, Timestamped, Base):
    """One pattern in a blocklist. Patterns are DNS-name shapes; the
    only wildcard is leading `*.` (matches one-or-more labels). The
    renderer compiles them into a regex alternation for CoreDNS's
    template plugin."""

    __tablename__ = "dns_blocklist_entries"
    __table_args__ = (
        UniqueConstraint(
            "blocklist_id", "pattern",
            name="uq_dns_blocklist_entry_pattern",
        ),
        Index("ix_dns_blocklist_entries_blocklist", "blocklist_id"),
    )

    blocklist_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("dns_blocklists.id", ondelete="CASCADE"),
        nullable=False,
    )
    pattern: Mapped[str] = mapped_column(String(253), nullable=False)
    description: Mapped[str | None] = mapped_column(String(512))


class DnsForwarder(UUIDPrimaryKey, Timestamped, Base):
    """Per-zone conditional forwarder for the recursive CoreDNS. Each
    row emits one extra `<pattern>:53 { forward . <upstreams…> }` block
    in the rendered recursive Corefile, in addition to the global
    upstreams. Useful when a fabric needs to route specific zones (eg.
    `aws.internal.`) to a non-default resolver."""

    __tablename__ = "dns_forwarders"
    __table_args__ = (
        UniqueConstraint(
            "fabric_id", "zone_pattern", name="uq_dns_forwarder_fabric_zone",
        ),
        Index("ix_dns_forwarders_fabric", "fabric_id"),
    )

    name: Mapped[str] = mapped_column(String(128), nullable=False)
    fabric_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("fabrics.id"), nullable=False,
    )
    # Trailing dot is normalized in by the schema layer so the renderer
    # can rely on a canonical form.
    zone_pattern: Mapped[str] = mapped_column(String(253), nullable=False)
    # Stored as a JSON array of "ip" or "ip:port" strings — gives the
    # operator multiple resolvers without forcing a separate table.
    upstreams: Mapped[list[str]] = mapped_column(JSON, nullable=False, default=list)
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
