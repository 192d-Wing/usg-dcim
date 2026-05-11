"""BGP policy + identity entities.

The peer registry itself lives in models/dns.py (BgpPeer) because that's
where the table was first introduced for DNS anycast. The entities here
are the policy + identity surfaces that operators reach for once they
have peers in place:

  Asn                — labeled ASN catalog. Cross-references peer rows
                       so operators can hunt down "which peers run AS65000".
  TcpAoKeyChain      — RFC 5925 TCP AO key chains, modern replacement
                       for the BgpPeer.md5_password single-secret field.
  TcpAoKey           — entries inside a key chain, with per-key send/recv
                       IDs, algorithm, and (eventually encrypted) secret.
  PrefixList         — ordered CIDR matchers used by route-map match
                       clauses (or directly by peers as filter-list).
  PrefixListEntry    — sequenced rows inside a PrefixList.
  CommunityList      — named groups of BGP community values (standard
                       or extended) referenced by route-map match/set
                       clauses by name rather than literal value.
  CommunityListEntry — sequenced rows inside a CommunityList.
  RouteMap           — ordered policy rules attached to a peer's import
                       or export direction.
  RouteMapEntry      — one rule: match-* + action (permit/deny) + set-*.

None of these have a hard FK to BgpPeer at this layer — the peer side
gets binding tables in a follow-up so a single key chain / route map
can be reused across multiple peers.
"""

from __future__ import annotations

import enum
from datetime import datetime
from uuid import UUID

from sqlalchemy import (
    BigInteger,
    Boolean,
    DateTime,
    Enum,
    ForeignKey,
    Index,
    Integer,
    String,
    UniqueConstraint,
)
from sqlalchemy.dialects.postgresql import CIDR
from sqlalchemy.dialects.postgresql import UUID as PgUUID
from sqlalchemy.orm import Mapped, mapped_column

from ..db import Base
from ._mixins import Timestamped, UUIDPrimaryKey


# ----------------------- enums -----------------------

class AsnKind(str, enum.Enum):
    """Why this ASN is in the catalog. Operators rarely need the full
    IANA private/documentation boundary table — the four buckets below
    are what fits in a filter dropdown."""

    public = "public"
    private = "private"
    documentation = "documentation"
    reserved = "reserved"


class TcpAoAlgorithm(str, enum.Enum):
    """RFC 5926 — TCP AO supports HMAC-SHA1 and AES-128-CMAC. Most
    modern stacks default to one of these two."""

    hmac_sha1_96 = "hmac-sha1-96"
    aes_128_cmac = "aes-128-cmac"


class PolicyAction(str, enum.Enum):
    """Shared by PrefixListEntry / CommunityListEntry / RouteMapEntry."""

    permit = "permit"
    deny = "deny"


class AddressFamilyV4V6(str, enum.Enum):
    """For PrefixList scope. Separate enum from BgpAddressFamily (the
    VPNv4/VPNv6/EVPN one) because prefix lists are pure
    unicast-by-address-family."""

    v4 = "v4"
    v6 = "v6"


class CommunityKind(str, enum.Enum):
    """Standard 32-bit communities (RFC 1997) vs extended communities
    (RFC 4360 — route-target, route-origin, etc.)."""

    standard = "standard"
    extended = "extended"


# ----------------------- ASN catalog -----------------------

class Asn(UUIDPrimaryKey, Timestamped, Base):
    """A labeled Autonomous System number. The raw `asn` integer is the
    natural key but we keep a UUID id so it survives renumbering and so
    references from other tables stay stable. Cross-reference back to
    peer rows happens by matching BgpPeer.local_asn / peer_asn."""

    __tablename__ = "bgp_asns"
    __table_args__ = (
        UniqueConstraint("asn", name="uq_bgp_asn"),
        Index("ix_bgp_asns_kind", "kind"),
    )

    # 4-byte ASN: 1 .. 4_294_967_295. Stored as BIGINT because the
    # 4-byte private range (4200000000+) overflows INT4 (max 2147483647).
    asn: Mapped[int] = mapped_column(BigInteger, nullable=False)
    name: Mapped[str] = mapped_column(String(128), nullable=False)
    kind: Mapped[AsnKind] = mapped_column(
        Enum(AsnKind, name="bgp_asn_kind", values_callable=lambda x: [e.value for e in x]),
        nullable=False, default=AsnKind.private,
    )
    # Owner organization. Optional because not every ASN in the catalog
    # is one this estate registers — some are upstream peers / providers
    # whose org we may not track in detail.
    organization_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("organizations.id"),
    )
    description: Mapped[str | None] = mapped_column(String(512))


# ----------------------- TCP AO key chains -----------------------

class TcpAoKeyChain(UUIDPrimaryKey, Timestamped, Base):
    """A named group of TCP AO keys, with overlapping send/accept
    lifetimes so a session can rotate keys without dropping. The chain
    itself is what BGP peers will reference (via a future
    BgpPeer.tcp_ao_key_chain_id column once we migrate off MD5)."""

    __tablename__ = "tcp_ao_key_chains"
    __table_args__ = (
        UniqueConstraint("name", name="uq_tcp_ao_key_chain_name"),
    )

    name: Mapped[str] = mapped_column(String(128), nullable=False)
    description: Mapped[str | None] = mapped_column(String(512))


class TcpAoKey(UUIDPrimaryKey, Timestamped, Base):
    """One key inside a key chain. send_id and recv_id are the on-wire
    KeyIDs (different at each end of an asymmetric setup). algorithm
    picks HMAC-SHA-1-96 or AES-128-CMAC. The secret is stored plain
    in v1 — same caveat as BgpPeer.md5_password; encryption-at-rest
    rolls in the same later pass."""

    __tablename__ = "tcp_ao_keys"
    __table_args__ = (
        UniqueConstraint("key_chain_id", "key_id", name="uq_tcp_ao_key_chain_keyid"),
        Index("ix_tcp_ao_keys_chain", "key_chain_id"),
    )

    key_chain_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("tcp_ao_key_chains.id"), nullable=False,
    )
    # Operator-facing identifier within the chain. 0..255 in Cisco
    # parlance; we accept up to 16-bit to be conservative.
    key_id: Mapped[int] = mapped_column(Integer, nullable=False)
    send_id: Mapped[int] = mapped_column(Integer, nullable=False)
    recv_id: Mapped[int] = mapped_column(Integer, nullable=False)
    algorithm: Mapped[TcpAoAlgorithm] = mapped_column(
        Enum(
            TcpAoAlgorithm,
            name="tcp_ao_algorithm",
            values_callable=lambda x: [e.value for e in x],
        ),
        nullable=False,
    )
    secret: Mapped[str] = mapped_column(String(512), nullable=False)
    # Optional lifetime window for the key. Null on either side means
    # "no boundary": null/null = always valid, null/X = valid until X.
    # RFC 5926 talks about distinct send + accept lifetimes; we collapse
    # to a single window here because (a) overlapping windows are
    # represented by overlapping keys in the same chain, and (b) most
    # operator-facing configs only expose one window per key.
    valid_from: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    valid_to: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    description: Mapped[str | None] = mapped_column(String(512))


# ----------------------- Prefix lists -----------------------

class PrefixList(UUIDPrimaryKey, Timestamped, Base):
    """Named ordered list of CIDR matchers. Scoped to a single address
    family so the entries can use Postgres CIDR safely."""

    __tablename__ = "bgp_prefix_lists"
    __table_args__ = (
        UniqueConstraint("name", "family", name="uq_prefix_list_name_family"),
    )

    name: Mapped[str] = mapped_column(String(128), nullable=False)
    family: Mapped[AddressFamilyV4V6] = mapped_column(
        Enum(
            AddressFamilyV4V6,
            name="address_family_v4v6",
            values_callable=lambda x: [e.value for e in x],
        ),
        nullable=False,
    )
    description: Mapped[str | None] = mapped_column(String(512))


class PrefixListEntry(UUIDPrimaryKey, Timestamped, Base):
    """One row inside a PrefixList. `seq` controls match order; the
    first matching entry wins. `ge` and `le` bound the matched prefix
    length, mirroring Cisco/Juniper prefix-list semantics."""

    __tablename__ = "bgp_prefix_list_entries"
    __table_args__ = (
        UniqueConstraint("prefix_list_id", "seq", name="uq_prefix_list_seq"),
        Index("ix_prefix_list_entries_list", "prefix_list_id"),
    )

    prefix_list_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("bgp_prefix_lists.id"), nullable=False,
    )
    seq: Mapped[int] = mapped_column(Integer, nullable=False)
    action: Mapped[PolicyAction] = mapped_column(
        Enum(PolicyAction, name="bgp_policy_action", values_callable=lambda x: [e.value for e in x]),
        nullable=False,
    )
    prefix: Mapped[str] = mapped_column(CIDR, nullable=False)
    # ge/le are optional: leaving both null means "exact-match this prefix".
    ge: Mapped[int | None] = mapped_column(Integer)
    le: Mapped[int | None] = mapped_column(Integer)
    description: Mapped[str | None] = mapped_column(String(512))


# ----------------------- Community lists -----------------------

class CommunityList(UUIDPrimaryKey, Timestamped, Base):
    """Named set of BGP community values (standard or extended) that
    route maps reference by name."""

    __tablename__ = "bgp_community_lists"
    __table_args__ = (
        UniqueConstraint("name", name="uq_community_list_name"),
    )

    name: Mapped[str] = mapped_column(String(128), nullable=False)
    kind: Mapped[CommunityKind] = mapped_column(
        Enum(
            CommunityKind,
            name="bgp_community_kind",
            values_callable=lambda x: [e.value for e in x],
        ),
        nullable=False, default=CommunityKind.standard,
    )
    description: Mapped[str | None] = mapped_column(String(512))


class CommunityListEntry(UUIDPrimaryKey, Timestamped, Base):
    """One value inside a CommunityList. Stored as a free string because
    extended communities have several syntaxes (e.g. "target:65000:100",
    "origin:10.0.0.1:42") and we don't want to fragment storage. The
    schemas layer can tighten validation per `kind`."""

    __tablename__ = "bgp_community_list_entries"
    __table_args__ = (
        UniqueConstraint("community_list_id", "seq", name="uq_community_list_seq"),
        Index("ix_community_list_entries_list", "community_list_id"),
    )

    community_list_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("bgp_community_lists.id"), nullable=False,
    )
    seq: Mapped[int] = mapped_column(Integer, nullable=False)
    action: Mapped[PolicyAction] = mapped_column(
        Enum(PolicyAction, name="bgp_policy_action", values_callable=lambda x: [e.value for e in x]),
        nullable=False,
    )
    # e.g. "65000:100" (standard) or "target:65000:100" (extended).
    value: Mapped[str] = mapped_column(String(128), nullable=False)
    description: Mapped[str | None] = mapped_column(String(512))


# ----------------------- Route maps -----------------------

class RouteMap(UUIDPrimaryKey, Timestamped, Base):
    """Named ordered policy. Entries reference PrefixList / CommunityList
    by id for match clauses; set-* clauses live inline on the entry."""

    __tablename__ = "bgp_route_maps"
    __table_args__ = (
        UniqueConstraint("name", name="uq_route_map_name"),
    )

    name: Mapped[str] = mapped_column(String(128), nullable=False)
    description: Mapped[str | None] = mapped_column(String(512))


class RouteMapEntry(UUIDPrimaryKey, Timestamped, Base):
    """One sequenced rule inside a RouteMap.

    Match clauses are a closed set of optional FK / regex columns rather
    than a JSON blob so the migration / query layer stays type-safe and
    operators can filter by "every route-map matching prefix-list X".

    Set clauses follow the same closed-set pattern. The set-community
    side is a free string column rather than an FK so operators can
    attach a literal community value without first creating a list."""

    __tablename__ = "bgp_route_map_entries"
    __table_args__ = (
        UniqueConstraint("route_map_id", "seq", name="uq_route_map_seq"),
        Index("ix_route_map_entries_map", "route_map_id"),
    )

    route_map_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("bgp_route_maps.id"), nullable=False,
    )
    seq: Mapped[int] = mapped_column(Integer, nullable=False)
    action: Mapped[PolicyAction] = mapped_column(
        Enum(PolicyAction, name="bgp_policy_action", values_callable=lambda x: [e.value for e in x]),
        nullable=False,
    )

    # --- match clauses (all optional; nulls mean "no constraint") ---
    match_prefix_list_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("bgp_prefix_lists.id"),
    )
    match_community_list_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("bgp_community_lists.id"),
    )
    # AS-path regex e.g. "_65000_" — kept as text rather than a separate
    # AS-path-list entity to avoid a fourth side-table for v1.
    match_as_path_regex: Mapped[str | None] = mapped_column(String(256))

    # --- set clauses ---
    set_local_pref: Mapped[int | None] = mapped_column(Integer)
    set_med: Mapped[int | None] = mapped_column(Integer)
    # Free-form so the operator can add either a single literal community
    # ("65000:100") or a space-separated list to be applied as a set.
    set_community: Mapped[str | None] = mapped_column(String(256))

    description: Mapped[str | None] = mapped_column(String(512))
