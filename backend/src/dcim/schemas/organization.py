"""Organization schemas — ARIN-aligned fields for an owning entity."""

from __future__ import annotations

from datetime import datetime
from uuid import UUID

from pydantic import BaseModel, ConfigDict


class OrganizationBase(BaseModel):
    name: str
    arin_org_id: str | None = None

    address_line1: str
    address_line2: str | None = None
    city: str
    state_province: str | None = None
    postal_code: str | None = None
    country: str  # 2-letter ISO

    phone: str | None = None
    email: str | None = None

    admin_poc_name: str
    admin_poc_email: str
    admin_poc_phone: str | None = None

    tech_poc_name: str
    tech_poc_email: str
    tech_poc_phone: str | None = None

    abuse_poc_name: str
    abuse_poc_email: str
    abuse_poc_phone: str | None = None

    noc_poc_name: str | None = None
    noc_poc_email: str | None = None
    noc_poc_phone: str | None = None

    description: str | None = None


class OrganizationCreate(OrganizationBase):
    pass


class OrganizationUpdate(BaseModel):
    name: str | None = None
    arin_org_id: str | None = None

    address_line1: str | None = None
    address_line2: str | None = None
    city: str | None = None
    state_province: str | None = None
    postal_code: str | None = None
    country: str | None = None

    phone: str | None = None
    email: str | None = None

    admin_poc_name: str | None = None
    admin_poc_email: str | None = None
    admin_poc_phone: str | None = None

    tech_poc_name: str | None = None
    tech_poc_email: str | None = None
    tech_poc_phone: str | None = None

    abuse_poc_name: str | None = None
    abuse_poc_email: str | None = None
    abuse_poc_phone: str | None = None

    noc_poc_name: str | None = None
    noc_poc_email: str | None = None
    noc_poc_phone: str | None = None

    description: str | None = None


class OrganizationOut(OrganizationBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime
