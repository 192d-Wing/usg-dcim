"""CRUD for the Organization registry."""

from __future__ import annotations

from uuid import UUID

from fastapi import APIRouter, Depends
from sqlalchemy import delete, select
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..errors import ConflictError, NotFoundError
from ..models.bgp import Asn
from ..models.organization import Organization
from ..schemas.common import Page, PageParams
from ..schemas.organization import (
    OrganizationCreate,
    OrganizationOut,
    OrganizationUpdate,
)
from ..security import audit
from ..security.deps import Principal, require_capability
from ._pagination import paginate

router = APIRouter(prefix="/organizations", tags=["organizations"])

_NOT_FOUND = "organization not found"

@router.get("", response_model=Page[OrganizationOut])
async def list_orgs(
    params: PageParams = Depends(PageParams.from_query),
    _: Principal = Depends(require_capability("inventory:organizations:read")),
    db: AsyncSession = Depends(get_db),
):
    return await paginate(
        db, select(Organization), model=Organization, params=params, out_model=OrganizationOut,
    )

@router.get("/{org_id}", response_model=OrganizationOut)
async def get_org(
    org_id: UUID,
    _: Principal = Depends(require_capability("inventory:organizations:read")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Organization, org_id)
    if obj is None:
        raise NotFoundError(_NOT_FOUND)
    return obj

@router.post("", response_model=OrganizationOut, status_code=201)
async def create_org(
    payload: OrganizationCreate,
    principal: Principal = Depends(require_capability("inventory:organizations:create")),
    db: AsyncSession = Depends(get_db),
):
    obj = Organization(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="organization.create",
        target_type="organization", target_id=str(obj.id),
    )
    await db.commit()
    await db.refresh(obj)
    return obj

@router.patch("/{org_id}", response_model=OrganizationOut)
async def update_org(
    org_id: UUID,
    payload: OrganizationUpdate,
    principal: Principal = Depends(require_capability("inventory:organizations:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Organization, org_id)
    if obj is None:
        raise NotFoundError(_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="organization.update",
        target_type="organization", target_id=str(org_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj

@router.delete("/{org_id}", status_code=204)
async def delete_org(
    org_id: UUID,
    principal: Principal = Depends(require_capability("inventory:organizations:delete")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Organization, org_id)
    if obj is None:
        raise NotFoundError(_NOT_FOUND)
    # Refuse to drop an org that still owns ASNs — the operator clears
    # the FK or moves the ASNs first.
    in_use = (
        await db.execute(
            select(Asn.id).where(Asn.organization_id == org_id).limit(1),
        )
    ).scalar_one_or_none()
    if in_use is not None:
        raise ConflictError("organization still owns ASNs; clear the FK first")
    await db.execute(delete(Organization).where(Organization.id == org_id))
    await audit.record(
        db, principal, action="organization.delete",
        target_type="organization", target_id=str(org_id),
    )
    await db.commit()
