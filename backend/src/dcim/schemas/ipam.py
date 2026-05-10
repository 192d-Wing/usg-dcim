"""Pydantic schemas for the IPAM hierarchy."""

from __future__ import annotations

from datetime import datetime
from typing import Annotated, Any
from uuid import UUID

from pydantic import BaseModel, BeforeValidator, ConfigDict

from ..models.ipam import IpAddressRole, IpAddressSource, IpAddressStatus


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


class FabricCreate(FabricBase):
    pass


class FabricUpdate(BaseModel):
    name: str | None = None
    slug: str | None = None
    description: str | None = None
    enclave: str | None = None
    classification: str | None = None


class FabricOut(FabricBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


# ---------- VRF ----------

class VrfBase(BaseModel):
    fabric_id: UUID
    name: str
    rd: str | None = None
    description: str | None = None
    is_default: bool = False


class VrfCreate(VrfBase):
    pass


class VrfUpdate(BaseModel):
    name: str | None = None
    rd: str | None = None
    description: str | None = None
    is_default: bool | None = None


class VrfOut(VrfBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


# ---------- Supernet ----------

class SupernetBase(BaseModel):
    fabric_id: UUID
    vrf_id: UUID
    prefix: CidrStr
    name: str | None = None
    description: str | None = None
    purpose: str | None = None


class SupernetCreate(SupernetBase):
    pass


class SupernetUpdate(BaseModel):
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
    prefix: CidrStr
    name: str | None = None
    description: str | None = None
    purpose: str | None = None
    vlan_id: int | None = None
    gateway: InetStrOpt = None


class SubnetCreate(SubnetBase):
    pass


class SubnetUpdate(BaseModel):
    site_id: UUID | None = None
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


class DhcpServerCreate(DhcpServerBase):
    auth_password: str | None = None


class DhcpServerUpdate(BaseModel):
    name: str | None = None
    kea_url: str | None = None
    auth_username: str | None = None
    auth_password: str | None = None
    enabled: bool | None = None


class DhcpServerOut(DhcpServerBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    last_sync_at: datetime | None
    last_sync_status: str | None
    last_sync_error: str | None
    last_sync_lease_count: int | None
    created_at: datetime
    updated_at: datetime
