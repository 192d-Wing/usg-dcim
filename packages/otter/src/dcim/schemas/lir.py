"""Pydantic schemas for the LIR module — pools, requests, allocations.

Wire format mirrors the model column layout closely; family-vs-prefix
validation lives on the Create schemas so an API client gets a 422 on
bad combinations rather than a 500 from the CHECK constraint. Status
fields read back as the enum string ('pending_approval', 'active',
etc.) so the frontend can switch on them directly.
"""

from __future__ import annotations

from datetime import datetime
from uuid import UUID

from pydantic import BaseModel, ConfigDict, Field, model_validator

from ..models.lir import (
    LirAllocationStatus,
    LirArinStatus,
    LirRequestStatus,
)
from .ipam import CidrStr


def _validate_family_prefix(family: int, prefix_len: int) -> None:
    """Same bounds as ck_lir_pool_prefix_bounds / ck_lir_request_prefix_bounds.

    Raised as ValueError so pydantic surfaces a 422 with the field
    path. Bounded server-side as well by the DB CHECK, but failing in
    pydantic gives the frontend a useful error before the row hits PG.
    """
    if family not in (4, 6):
        raise ValueError(f"ip_family must be 4 or 6, got {family}")
    if prefix_len < 0:
        raise ValueError("prefix length must be non-negative")
    cap = 32 if family == 4 else 128
    if prefix_len > cap:
        raise ValueError(
            f"prefix length {prefix_len} exceeds {cap} bits for IPv{family}",
        )


# ---------- LirPool ----------

class LirPoolBase(BaseModel):
    name: str
    slug: str
    description: str | None = None
    ip_family: int = Field(ge=4, le=6)
    fabric_id: UUID | None = None
    classification: str | None = None
    min_prefix_length: int = Field(ge=0, le=128)
    max_prefix_length: int = Field(ge=0, le=128)
    default_supernet_purpose: str | None = None
    arin_parent_net_handle: str | None = None
    enabled: bool = True

    @model_validator(mode="after")
    def _check_bounds(self) -> LirPoolBase:
        # min ≤ max and both fit inside the family's address width.
        if self.min_prefix_length > self.max_prefix_length:
            raise ValueError(
                "min_prefix_length must be ≤ max_prefix_length",
            )
        _validate_family_prefix(self.ip_family, self.max_prefix_length)
        _validate_family_prefix(self.ip_family, self.min_prefix_length)
        return self


class LirPoolCreate(LirPoolBase):
    pass


class LirPoolUpdate(BaseModel):
    name: str | None = None
    slug: str | None = None
    description: str | None = None
    fabric_id: UUID | None = None
    classification: str | None = None
    # Family is immutable on update — switching a pool from v4 to v6
    # would orphan any allocations under it. Re-create the pool instead.
    min_prefix_length: int | None = Field(default=None, ge=0, le=128)
    max_prefix_length: int | None = Field(default=None, ge=0, le=128)
    default_supernet_purpose: str | None = None
    arin_parent_net_handle: str | None = None
    enabled: bool | None = None


class LirPoolOut(LirPoolBase):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    created_at: datetime
    updated_at: datetime


# ---------- LirRequest ----------

class LirRequestCreate(BaseModel):
    """Tenant-side submission. requester_user_id is taken from the
    authenticated principal at the API layer — clients don't supply
    it. organization_id is checked against the principal's
    lir:requests:create scope."""

    organization_id: UUID
    pool_id: UUID | None = None
    site_id: UUID | None = None
    ip_family: int = Field(ge=4, le=6)
    prefix_length: int = Field(ge=0, le=128)
    purpose: str | None = None
    classification: str | None = None
    justification: str = Field(min_length=1)

    @model_validator(mode="after")
    def _check_bounds(self) -> LirRequestCreate:
        _validate_family_prefix(self.ip_family, self.prefix_length)
        return self


class LirRequestCancel(BaseModel):
    """Body for the requester-side cancel endpoint. Notes are optional
    but recommended — they land in `decision_notes` so the NIC sees
    why the requester pulled the request."""

    notes: str | None = None


class LirRequestApprove(BaseModel):
    """NIC-side approval. `approved_pool_id` overrides the tenant's
    `pool_id` preference; both null = approve into the requested pool
    (the API rejects when neither is set)."""

    approved_pool_id: UUID | None = None
    notes: str | None = None


class LirRequestReject(BaseModel):
    """NIC-side rejection. Reason is required — it goes to the
    requester in the notification and into `decision_notes` for
    audit."""

    reason: str = Field(min_length=1)


class LirRequestOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    organization_id: UUID
    requester_user_id: UUID
    pool_id: UUID | None = None
    site_id: UUID | None = None
    ip_family: int
    prefix_length: int
    purpose: str | None = None
    classification: str | None = None
    justification: str
    status: LirRequestStatus
    submitted_at: datetime
    decided_at: datetime | None = None
    decided_by_user_id: UUID | None = None
    decision_notes: str | None = None
    approved_pool_id: UUID | None = None
    created_at: datetime
    updated_at: datetime


# ---------- LirAllocation ----------

class LirAllocationReturnRequest(BaseModel):
    """Tenant-side return request. Reason flows into `return_reason`
    on the row and into the NIC's confirmation queue."""

    reason: str = Field(min_length=1)


class LirAllocationReturnConfirm(BaseModel):
    """NIC-side return confirmation. Notes are optional."""

    notes: str | None = None


class LirAllocationOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    request_id: UUID
    organization_id: UUID
    pool_id: UUID
    pool_supernet_id: UUID
    tenant_supernet_id: UUID
    prefix: CidrStr
    allocated_at: datetime
    allocated_by_user_id: UUID
    status: LirAllocationStatus
    return_requested_at: datetime | None = None
    return_requested_by_user_id: UUID | None = None
    return_reason: str | None = None
    returned_at: datetime | None = None
    returned_by_user_id: UUID | None = None
    arin_status: LirArinStatus
    arin_net_handle: str | None = None
    arin_last_attempt_at: datetime | None = None
    arin_last_error: str | None = None
    arin_attempts: int
    created_at: datetime
    updated_at: datetime
