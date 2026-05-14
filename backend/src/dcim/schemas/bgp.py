"""Pydantic schemas for the BGP policy + identity surfaces.

Each entity has Base / Create / Update / Out:
  Base   — shared shape; required + optional fields
  Create — Base; allows server-generated fields to be omitted
  Update — every field optional (PATCH semantics)
  Out    — Base + id + timestamps; from_attributes=True so SQLAlchemy
           rows hydrate directly.
"""

from __future__ import annotations

from datetime import datetime
from typing import Annotated, Any
from uuid import UUID

from pydantic import BaseModel, BeforeValidator, ConfigDict, model_validator

from ..models.bgp import (
    AddressFamilyV4V6,
    AsnKind,
    CommunityKind,
    PolicyAction,
    TcpAoAlgorithm,
)

# IANA ASN range buckets — single source of truth for the "kind must
# match number" validator. Public is "anything not in the others".
#
# References:
#   RFC 6996 — private ASNs: 64512-65534 (2-byte), 4200000000-4294967294 (4-byte)
#   RFC 5398 — documentation ASNs: 64496-64511, 65536-65551
#   Reserved: 0, 23456 (AS_TRANS), 65535, 4294967295

_PRIVATE_RANGES = ((64512, 65534), (4_200_000_000, 4_294_967_294))
_DOC_RANGES = ((64496, 64511), (65536, 65551))
_RESERVED_VALUES = {0, 23456, 65535, 4_294_967_295}


def _in_range(asn: int, ranges: tuple[tuple[int, int], ...]) -> bool:
    return any(lo <= asn <= hi for lo, hi in ranges)


def asn_kind_for(asn: int) -> AsnKind:
    """Return the kind an ASN integer falls into. Public is the catch-all."""
    if asn in _RESERVED_VALUES:
        return AsnKind.reserved
    if _in_range(asn, _PRIVATE_RANGES):
        return AsnKind.private
    if _in_range(asn, _DOC_RANGES):
        return AsnKind.documentation
    return AsnKind.public


def _to_str(v: Any) -> Any:
    if v is None or isinstance(v, str):
        return v
    return str(v)


CidrStr = Annotated[str, BeforeValidator(_to_str)]


# ---------- ASN ----------

class AsnBase(BaseModel):
    asn: int
    name: str
    kind: AsnKind = AsnKind.private
    organization_id: UUID | None = None
    description: str | None = None

    @model_validator(mode="after")
    def _validate_range(self) -> AsnBase:
        if not 1 <= self.asn <= 4_294_967_295:
            raise ValueError("asn must be in 1..4294967295")
        expected = asn_kind_for(self.asn)
        if self.kind != expected:
            raise ValueError(
                f"asn {self.asn} is in the {expected.value} range, "
                f"but kind={self.kind.value} was specified",
            )
        return self


class AsnCreate(AsnBase):
    pass


class AsnUpdate(BaseModel):
    name: str | None = None
    kind: AsnKind | None = None
    organization_id: UUID | None = None
    description: str | None = None


class AsnOut(AsnBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime
    # Override the base validator so the existing-row hydration path
    # isn't subject to the create-time range check (the DB is the truth).
    @model_validator(mode="after")
    def _skip(self) -> AsnOut:
        return self


# ---------- TCP AO key chain + keys ----------

class TcpAoKeyChainBase(BaseModel):
    name: str
    description: str | None = None


class TcpAoKeyChainCreate(TcpAoKeyChainBase):
    pass


class TcpAoKeyChainUpdate(BaseModel):
    name: str | None = None
    description: str | None = None


class TcpAoKeyChainOut(TcpAoKeyChainBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


class TcpAoKeyBase(BaseModel):
    key_chain_id: UUID
    key_id: int
    send_id: int
    recv_id: int
    algorithm: TcpAoAlgorithm
    secret: str
    valid_from: datetime | None = None
    valid_to: datetime | None = None
    description: str | None = None

    @model_validator(mode="after")
    def _validate_window(self) -> TcpAoKeyBase:
        if (
            self.valid_from is not None
            and self.valid_to is not None
            and self.valid_to <= self.valid_from
        ):
            raise ValueError("valid_to must be after valid_from")
        return self


class TcpAoKeyCreate(TcpAoKeyBase):
    pass


class TcpAoKeyUpdate(BaseModel):
    # key_chain_id + key_id are the natural key; PATCH only touches
    # the rotation-knob fields.
    send_id: int | None = None
    recv_id: int | None = None
    algorithm: TcpAoAlgorithm | None = None
    secret: str | None = None
    valid_from: datetime | None = None
    valid_to: datetime | None = None
    description: str | None = None


class TcpAoKeyOut(TcpAoKeyBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime
    @model_validator(mode="after")
    def _skip(self) -> TcpAoKeyOut:
        return self


# ---------- Prefix list ----------

class PrefixListBase(BaseModel):
    name: str
    family: AddressFamilyV4V6
    description: str | None = None


class PrefixListCreate(PrefixListBase):
    pass


class PrefixListUpdate(BaseModel):
    name: str | None = None
    family: AddressFamilyV4V6 | None = None
    description: str | None = None


class PrefixListOut(PrefixListBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


class PrefixListEntryBase(BaseModel):
    prefix_list_id: UUID
    seq: int
    action: PolicyAction
    prefix: CidrStr
    ge: int | None = None
    le: int | None = None
    description: str | None = None


class PrefixListEntryCreate(PrefixListEntryBase):
    pass


class PrefixListEntryUpdate(BaseModel):
    seq: int | None = None
    action: PolicyAction | None = None
    prefix: CidrStr | None = None
    ge: int | None = None
    le: int | None = None
    description: str | None = None


class PrefixListEntryOut(PrefixListEntryBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


# ---------- Community list ----------

class CommunityListBase(BaseModel):
    name: str
    kind: CommunityKind = CommunityKind.standard
    description: str | None = None


class CommunityListCreate(CommunityListBase):
    pass


class CommunityListUpdate(BaseModel):
    name: str | None = None
    kind: CommunityKind | None = None
    description: str | None = None


class CommunityListOut(CommunityListBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


class CommunityListEntryBase(BaseModel):
    community_list_id: UUID
    seq: int
    action: PolicyAction
    value: str
    description: str | None = None


class CommunityListEntryCreate(CommunityListEntryBase):
    pass


class CommunityListEntryUpdate(BaseModel):
    seq: int | None = None
    action: PolicyAction | None = None
    value: str | None = None
    description: str | None = None


class CommunityListEntryOut(CommunityListEntryBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


# ---------- Route map ----------

class RouteMapBase(BaseModel):
    name: str
    description: str | None = None


class RouteMapCreate(RouteMapBase):
    pass


class RouteMapUpdate(BaseModel):
    name: str | None = None
    description: str | None = None


class RouteMapOut(RouteMapBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


class RouteMapEntryBase(BaseModel):
    route_map_id: UUID
    seq: int
    action: PolicyAction
    match_prefix_list_id: UUID | None = None
    match_community_list_id: UUID | None = None
    match_as_path_regex: str | None = None
    set_local_pref: int | None = None
    set_med: int | None = None
    set_community: str | None = None
    description: str | None = None


class RouteMapEntryCreate(RouteMapEntryBase):
    pass


class RouteMapEntryUpdate(BaseModel):
    seq: int | None = None
    action: PolicyAction | None = None
    match_prefix_list_id: UUID | None = None
    match_community_list_id: UUID | None = None
    match_as_path_regex: str | None = None
    set_local_pref: int | None = None
    set_med: int | None = None
    set_community: str | None = None
    description: str | None = None


class RouteMapEntryOut(RouteMapEntryBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime
