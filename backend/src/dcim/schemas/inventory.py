"""Pydantic schemas for inventory entities."""

from __future__ import annotations

from datetime import datetime
from uuid import UUID

from pydantic import BaseModel, ConfigDict, Field

from ..models.inventory import AssetKind, LifecycleState


class _Out(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


# --- Region ---
class RegionBase(BaseModel):
    name: str
    code: str
    description: str | None = None


class RegionCreate(RegionBase):
    pass


class RegionUpdate(BaseModel):
    name: str | None = None
    description: str | None = None


class RegionOut(RegionBase, _Out):
    pass


# --- Site ---
class SiteBase(BaseModel):
    region_id: UUID
    name: str
    code: str
    address: str | None = None
    latitude: float | None = None
    longitude: float | None = None
    timezone: str | None = None
    majcom: str | None = None
    organization: str | None = None
    mission_owner: str | None = None
    enclave: str | None = None
    classification: str | None = None
    lifecycle_state: LifecycleState = LifecycleState.active
    metadata_json: dict = Field(default_factory=dict)


class SiteCreate(SiteBase):
    pass


class SiteUpdate(BaseModel):
    name: str | None = None
    address: str | None = None
    majcom: str | None = None
    organization: str | None = None
    mission_owner: str | None = None
    enclave: str | None = None
    lifecycle_state: LifecycleState | None = None
    metadata_json: dict | None = None


class SiteOut(SiteBase, _Out):
    pass


# --- Building ---
class BuildingBase(BaseModel):
    site_id: UUID
    name: str
    code: str


class BuildingCreate(BuildingBase):
    pass


class BuildingUpdate(BaseModel):
    name: str | None = None


class BuildingOut(BuildingBase, _Out):
    pass


# --- Room ---
class RoomBase(BaseModel):
    building_id: UUID
    name: str
    code: str
    floor_area_sqft: int | None = None
    design_kw: float | None = None
    design_cooling_tons: float | None = None


class RoomCreate(RoomBase):
    pass


class RoomUpdate(BaseModel):
    name: str | None = None
    design_kw: float | None = None
    design_cooling_tons: float | None = None


class RoomOut(RoomBase, _Out):
    pass


# --- Row ---
class RowBase(BaseModel):
    room_id: UUID
    name: str
    code: str


class RowCreate(RowBase):
    pass


class RowUpdate(BaseModel):
    name: str | None = None


class RowOut(RowBase, _Out):
    pass


# --- Rack ---
class RackBase(BaseModel):
    site_id: UUID
    row_id: UUID
    name: str
    code: str
    u_height: int = 42
    max_kw: float | None = None
    max_weight_lbs: int | None = None
    serial: str | None = None


class RackCreate(RackBase):
    pass


class RackUpdate(BaseModel):
    name: str | None = None
    u_height: int | None = Field(default=None, ge=1, le=60)
    max_kw: float | None = None
    serial: str | None = None


class RackOut(RackBase, _Out):
    pass


# --- Asset ---
class AssetBase(BaseModel):
    site_id: UUID
    rack_id: UUID | None = None
    parent_asset_id: UUID | None = None
    name: str
    hostname: str | None = None
    kind: AssetKind
    manufacturer: str | None = None
    model: str | None = None
    serial: str | None = None
    firmware: str | None = None
    rack_position_u: int | None = None
    rack_units: int | None = 1
    face: str = "front"           # asset_face enum
    mount: str = "rack"           # asset_mount enum
    pdu_side: str | None = None   # PDUs only
    psu_count: int | None = None  # for redundancy gap detection on devices
    mgmt_ip: str | None = None
    mgmt_protocol: str | None = None
    mgmt_port: int | None = None
    mgmt_credentials_ref: str | None = None
    lifecycle_state: LifecycleState = LifecycleState.active
    metadata_json: dict = Field(default_factory=dict)


class AssetCreate(AssetBase):
    pass


class AssetUpdate(BaseModel):
    name: str | None = None
    hostname: str | None = None
    rack_id: UUID | None = None
    rack_position_u: int | None = None
    rack_units: int | None = None
    face: str | None = None
    mount: str | None = None
    pdu_side: str | None = None
    psu_count: int | None = None
    mgmt_ip: str | None = None
    mgmt_protocol: str | None = None
    mgmt_port: int | None = None
    firmware: str | None = None
    lifecycle_state: LifecycleState | None = None
    metadata_json: dict | None = None


class AssetOut(AssetBase, _Out):
    pass


# --- Cable ---
class CableBase(BaseModel):
    site_id: UUID
    a_asset_id: UUID
    a_port: str | None = None
    b_asset_id: UUID
    b_port: str | None = None
    medium: str | None = None  # cat6|smf|mmf|power-c13|...
    color: str | None = None
    length_m: float | None = None
    label: str | None = None


class CableCreate(CableBase):
    pass


class CableUpdate(BaseModel):
    a_asset_id: UUID | None = None
    a_port: str | None = None
    b_asset_id: UUID | None = None
    b_port: str | None = None
    medium: str | None = None
    color: str | None = None
    length_m: float | None = None
    label: str | None = None


class CableOut(CableBase, _Out):
    pass
