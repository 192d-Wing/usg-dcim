from __future__ import annotations

from datetime import datetime
from uuid import UUID

from pydantic import BaseModel, ConfigDict


class OutletOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    pdu_asset_id: UUID
    position: int
    label: str | None = None
    phase: str | None = None
    max_amps: int | None = None
    receptacle: str | None = None
    connected: dict | None = None  # populated by endpoints when joining the connection row


class OutletCreate(BaseModel):
    pdu_asset_id: UUID
    position: int
    label: str | None = None
    phase: str | None = None
    max_amps: int | None = None
    receptacle: str | None = None


class PowerConnectionCreate(BaseModel):
    asset_id: UUID
    psu_index: int = 1
    cord_color: str | None = None
    cord_length_m: float | None = None


class PowerConnectionOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    outlet_id: UUID
    asset_id: UUID
    psu_index: int
    cord_color: str | None = None
    cord_length_m: float | None = None
    created_at: datetime
