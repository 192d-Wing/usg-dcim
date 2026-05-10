"""CRUD over the IPAM hierarchy.

Layout:
  /ipam/fabrics              GET POST
  /ipam/fabrics/{id}         GET PATCH DELETE
  /ipam/vrfs                 GET (filter by fabric_id) POST
  /ipam/vrfs/{id}            GET PATCH DELETE
  /ipam/supernets            GET (filter fabric/vrf) POST
  /ipam/supernets/{id}       GET PATCH DELETE
  /ipam/supernets/{id}/utilization
  /ipam/subnets              GET (filter supernet/fabric/vrf/site) POST
  /ipam/subnets/{id}         GET PATCH DELETE
  /ipam/subnets/{id}/utilization
  /ipam/addresses            GET (filter subnet/asset/role/status) POST
  /ipam/addresses/{id}       GET PATCH DELETE

All write paths run through services.ipam.assert_* helpers so
containment + per-VRF uniqueness invariants stay tight regardless of
who's posting (UI, scripts, sync jobs).
"""

from __future__ import annotations

import re
from uuid import UUID

from fastapi import APIRouter, Depends, Query
from sqlalchemy import delete, select
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..errors import ConflictError, NotFoundError, ValidationError
from ..models.ipam import (
    DhcpServer,
    Fabric,
    IPAddress,
    Subnet,
    Supernet,
    Vrf,
)
from ..schemas.common import Page, PageParams
from ..schemas.ipam import (
    FabricCreate,
    FabricOut,
    FabricUpdate,
    IPAddressCreate,
    IPAddressOut,
    IPAddressUpdate,
    SubnetCreate,
    SubnetOut,
    SubnetUpdate,
    SubnetUtilization,
    SupernetCreate,
    SupernetOut,
    SupernetUpdate,
    VrfCreate,
    VrfOut,
    VrfUpdate,
)
from ..security import audit
from ..security.capabilities import INVENTORY_READ, INVENTORY_WRITE
from ..security.deps import Principal, require_capability
from ..services import ipam as ipam_svc
from ._pagination import paginate

router = APIRouter(prefix="/ipam", tags=["ipam"])

_FABRIC_NOT_FOUND = "fabric not found"
_VRF_NOT_FOUND = "vrf not found"
_SUPERNET_NOT_FOUND = "supernet not found"
_SUBNET_NOT_FOUND = "subnet not found"
_IP_NOT_FOUND = "ip address not found"

_SLUG_RE = re.compile(r"^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$")


# ----------------------- Fabrics -----------------------
@router.get("/fabrics", response_model=Page[FabricOut])
async def list_fabrics(
    params: PageParams = Depends(PageParams.from_query),
    enclave: str | None = Query(None),
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(Fabric)
    if enclave is not None:
        stmt = stmt.where(Fabric.enclave == enclave)
    return await paginate(db, stmt, model=Fabric, params=params, out_model=FabricOut)


@router.post("/fabrics", response_model=FabricOut, status_code=201)
async def create_fabric(
    payload: FabricCreate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    if not _SLUG_RE.match(payload.slug):
        raise ValidationError(
            "slug must be lowercase alphanumeric with optional hyphens",
            details={"slug": payload.slug},
        )
    existing = (
        await db.execute(select(Fabric).where(Fabric.slug == payload.slug))
    ).scalar_one_or_none()
    if existing is not None:
        raise ConflictError("a fabric with that slug already exists")
    obj = Fabric(**payload.model_dump())
    db.add(obj)
    await db.flush()
    # Auto-create the default VRF so flat networks don't need to know about VRFs.
    db.add(Vrf(fabric_id=obj.id, name="default", is_default=True))
    await audit.record(
        db, principal, action="fabric.create", target_type="fabric", target_id=str(obj.id),
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.get("/fabrics/{fabric_id}", response_model=FabricOut)
async def get_fabric(
    fabric_id: UUID,
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Fabric, fabric_id)
    if obj is None:
        raise NotFoundError(_FABRIC_NOT_FOUND)
    return obj


@router.patch("/fabrics/{fabric_id}", response_model=FabricOut)
async def update_fabric(
    fabric_id: UUID,
    payload: FabricUpdate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Fabric, fabric_id)
    if obj is None:
        raise NotFoundError(_FABRIC_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    if "slug" in diff and not _SLUG_RE.match(diff["slug"]):
        raise ValidationError(
            "slug must be lowercase alphanumeric with optional hyphens",
            details={"slug": diff["slug"]},
        )
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="fabric.update", target_type="fabric",
        target_id=str(fabric_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/fabrics/{fabric_id}", status_code=204)
async def delete_fabric(
    fabric_id: UUID,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Fabric, fabric_id)
    if obj is None:
        raise NotFoundError(_FABRIC_NOT_FOUND)
    in_use = (
        await db.execute(select(Vrf.id).where(Vrf.fabric_id == fabric_id).limit(1))
    ).scalar_one_or_none()
    if in_use is not None:
        raise ConflictError("fabric still has VRFs; remove them first")
    await db.execute(delete(Fabric).where(Fabric.id == fabric_id))
    await audit.record(
        db, principal, action="fabric.delete", target_type="fabric", target_id=str(fabric_id),
    )
    await db.commit()


# ----------------------- VRFs -----------------------
@router.get("/vrfs", response_model=Page[VrfOut])
async def list_vrfs(
    params: PageParams = Depends(PageParams.from_query),
    fabric_id: UUID | None = Query(None),
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(Vrf)
    if fabric_id is not None:
        stmt = stmt.where(Vrf.fabric_id == fabric_id)
    return await paginate(db, stmt, model=Vrf, params=params, out_model=VrfOut)


@router.post("/vrfs", response_model=VrfOut, status_code=201)
async def create_vrf(
    payload: VrfCreate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    fabric = await db.get(Fabric, payload.fabric_id)
    if fabric is None:
        raise ValidationError(f"fabric {payload.fabric_id} not found")
    obj = Vrf(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="vrf.create", target_type="vrf", target_id=str(obj.id),
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/vrfs/{vrf_id}", response_model=VrfOut)
async def update_vrf(
    vrf_id: UUID,
    payload: VrfUpdate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Vrf, vrf_id)
    if obj is None:
        raise NotFoundError(_VRF_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="vrf.update", target_type="vrf",
        target_id=str(vrf_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/vrfs/{vrf_id}", status_code=204)
async def delete_vrf(
    vrf_id: UUID,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Vrf, vrf_id)
    if obj is None:
        raise NotFoundError(_VRF_NOT_FOUND)
    if obj.is_default:
        raise ValidationError("cannot delete a fabric's default VRF")
    in_use = (
        await db.execute(select(Supernet.id).where(Supernet.vrf_id == vrf_id).limit(1))
    ).scalar_one_or_none()
    if in_use is not None:
        raise ConflictError("vrf still has supernets; remove them first")
    await db.execute(delete(Vrf).where(Vrf.id == vrf_id))
    await audit.record(
        db, principal, action="vrf.delete", target_type="vrf", target_id=str(vrf_id),
    )
    await db.commit()


# ----------------------- Supernets -----------------------
@router.get("/supernets", response_model=Page[SupernetOut])
async def list_supernets(
    params: PageParams = Depends(PageParams.from_query),
    fabric_id: UUID | None = Query(None),
    vrf_id: UUID | None = Query(None),
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(Supernet)
    if fabric_id is not None:
        stmt = stmt.where(Supernet.fabric_id == fabric_id)
    if vrf_id is not None:
        stmt = stmt.where(Supernet.vrf_id == vrf_id)
    return await paginate(db, stmt, model=Supernet, params=params, out_model=SupernetOut)


@router.post("/supernets", response_model=SupernetOut, status_code=201)
async def create_supernet(
    payload: SupernetCreate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    vrf = await db.get(Vrf, payload.vrf_id)
    if vrf is None or vrf.fabric_id != payload.fabric_id:
        raise ValidationError("vrf does not belong to that fabric")
    await ipam_svc.assert_supernet_unique_in_vrf(
        db, fabric_id=payload.fabric_id, vrf_id=payload.vrf_id, prefix=payload.prefix,
    )
    obj = Supernet(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="supernet.create",
        target_type="supernet", target_id=str(obj.id),
        metadata={"prefix": payload.prefix},
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/supernets/{supernet_id}", response_model=SupernetOut)
async def update_supernet(
    supernet_id: UUID,
    payload: SupernetUpdate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Supernet, supernet_id)
    if obj is None:
        raise NotFoundError(_SUPERNET_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="supernet.update", target_type="supernet",
        target_id=str(supernet_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/supernets/{supernet_id}", status_code=204)
async def delete_supernet(
    supernet_id: UUID,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Supernet, supernet_id)
    if obj is None:
        raise NotFoundError(_SUPERNET_NOT_FOUND)
    in_use = (
        await db.execute(select(Subnet.id).where(Subnet.supernet_id == supernet_id).limit(1))
    ).scalar_one_or_none()
    if in_use is not None:
        raise ConflictError("supernet still has subnets; remove them first")
    await db.execute(delete(Supernet).where(Supernet.id == supernet_id))
    await audit.record(
        db, principal, action="supernet.delete",
        target_type="supernet", target_id=str(supernet_id),
    )
    await db.commit()


@router.get("/supernets/{supernet_id}/utilization")
async def supernet_utilization(
    supernet_id: UUID,
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    sn = await db.get(Supernet, supernet_id)
    if sn is None:
        raise NotFoundError(_SUPERNET_NOT_FOUND)
    capacity = ipam_svc.network_capacity(sn.prefix)
    subnets = (
        await db.execute(select(Subnet).where(Subnet.supernet_id == supernet_id))
    ).scalars().all()
    allocated = sum(ipam_svc.network_capacity(s.prefix) for s in subnets)
    free = max(0, capacity - allocated)
    pct = round(100.0 * allocated / capacity, 2) if capacity else 0.0
    return {
        "supernet_id": str(supernet_id),
        "prefix": str(sn.prefix),
        "capacity": capacity,
        "allocated_subnet_addresses": allocated,
        "free": free,
        "percent": pct,
        "subnet_count": len(subnets),
    }


# ----------------------- Subnets -----------------------
@router.get("/subnets", response_model=Page[SubnetOut])
async def list_subnets(
    params: PageParams = Depends(PageParams.from_query),
    fabric_id: UUID | None = Query(None),
    vrf_id: UUID | None = Query(None),
    supernet_id: UUID | None = Query(None),
    site_id: UUID | None = Query(None),
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(Subnet)
    if fabric_id is not None:
        stmt = stmt.where(Subnet.fabric_id == fabric_id)
    if vrf_id is not None:
        stmt = stmt.where(Subnet.vrf_id == vrf_id)
    if supernet_id is not None:
        stmt = stmt.where(Subnet.supernet_id == supernet_id)
    if site_id is not None:
        stmt = stmt.where(Subnet.site_id == site_id)
    return await paginate(db, stmt, model=Subnet, params=params, out_model=SubnetOut)


@router.post("/subnets", response_model=SubnetOut, status_code=201)
async def create_subnet(
    payload: SubnetCreate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    parent = await ipam_svc.assert_subnet_inside_supernet(
        db, supernet_id=payload.supernet_id, prefix=payload.prefix,
    )
    await ipam_svc.assert_subnet_unique_in_vrf(
        db, fabric_id=parent.fabric_id, vrf_id=parent.vrf_id, prefix=payload.prefix,
    )
    data = payload.model_dump()
    data["fabric_id"] = parent.fabric_id
    data["vrf_id"] = parent.vrf_id
    obj = Subnet(**data)
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="subnet.create",
        target_type="subnet", target_id=str(obj.id),
        site_id=obj.site_id, metadata={"prefix": payload.prefix},
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/subnets/{subnet_id}", response_model=SubnetOut)
async def update_subnet(
    subnet_id: UUID,
    payload: SubnetUpdate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Subnet, subnet_id)
    if obj is None:
        raise NotFoundError(_SUBNET_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="subnet.update", target_type="subnet",
        target_id=str(subnet_id), site_id=obj.site_id, diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/subnets/{subnet_id}", status_code=204)
async def delete_subnet(
    subnet_id: UUID,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Subnet, subnet_id)
    if obj is None:
        raise NotFoundError(_SUBNET_NOT_FOUND)
    in_use = (
        await db.execute(select(IPAddress.id).where(IPAddress.subnet_id == subnet_id).limit(1))
    ).scalar_one_or_none()
    if in_use is not None:
        raise ConflictError("subnet still has IP allocations; remove them first")
    await db.execute(delete(Subnet).where(Subnet.id == subnet_id))
    await audit.record(
        db, principal, action="subnet.delete",
        target_type="subnet", target_id=str(subnet_id),
    )
    await db.commit()


@router.get("/subnets/{subnet_id}/utilization", response_model=SubnetUtilization)
async def subnet_utilization(
    subnet_id: UUID,
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    subnet = await db.get(Subnet, subnet_id)
    if subnet is None:
        raise NotFoundError(_SUBNET_NOT_FOUND)
    used = await ipam_svc.used_addresses_in_subnet(db, subnet_id)
    capacity = ipam_svc.network_capacity(subnet.prefix)
    allocated = len(used)
    free = max(0, capacity - allocated)
    pct = round(100.0 * allocated / capacity, 2) if capacity else 0.0
    return SubnetUtilization(
        subnet_id=subnet_id,
        prefix=str(subnet.prefix),
        capacity=capacity,
        allocated=allocated,
        free=free,
        percent=pct,
        next_available=ipam_svc.next_free_address(subnet.prefix, used),
    )


# ----------------------- IP Addresses -----------------------
@router.get("/addresses", response_model=Page[IPAddressOut])
async def list_addresses(
    params: PageParams = Depends(PageParams.from_query),
    subnet_id: UUID | None = Query(None),
    asset_id: UUID | None = Query(None),
    role: str | None = Query(None),
    status_: str | None = Query(None, alias="status"),
    source: str | None = Query(None),
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(IPAddress)
    if subnet_id is not None:
        stmt = stmt.where(IPAddress.subnet_id == subnet_id)
    if asset_id is not None:
        stmt = stmt.where(IPAddress.asset_id == asset_id)
    if role is not None:
        stmt = stmt.where(IPAddress.role == role)
    if status_ is not None:
        stmt = stmt.where(IPAddress.status == status_)
    if source is not None:
        stmt = stmt.where(IPAddress.source == source)
    return await paginate(db, stmt, model=IPAddress, params=params, out_model=IPAddressOut)


@router.post("/addresses", response_model=IPAddressOut, status_code=201)
async def create_address(
    payload: IPAddressCreate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    await ipam_svc.assert_address_in_subnet(
        db, subnet_id=payload.subnet_id, address=payload.address,
    )
    existing = (
        await db.execute(
            select(IPAddress).where(
                IPAddress.subnet_id == payload.subnet_id,
                IPAddress.address == payload.address,
            )
        )
    ).scalar_one_or_none()
    if existing is not None:
        raise ConflictError(
            f"{payload.address} is already allocated in this subnet",
            details={"existing_id": str(existing.id)},
        )
    obj = IPAddress(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="ip_address.create",
        target_type="ip_address", target_id=str(obj.id),
        metadata={"address": payload.address, "subnet_id": str(payload.subnet_id)},
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/addresses/{ip_id}", response_model=IPAddressOut)
async def update_address(
    ip_id: UUID,
    payload: IPAddressUpdate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(IPAddress, ip_id)
    if obj is None:
        raise NotFoundError(_IP_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="ip_address.update",
        target_type="ip_address", target_id=str(ip_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/addresses/{ip_id}", status_code=204)
async def delete_address(
    ip_id: UUID,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(IPAddress, ip_id)
    if obj is None:
        raise NotFoundError(_IP_NOT_FOUND)
    await db.execute(delete(IPAddress).where(IPAddress.id == ip_id))
    await audit.record(
        db, principal, action="ip_address.delete",
        target_type="ip_address", target_id=str(ip_id),
    )
    await db.commit()


# Keep DhcpServer imports re-exported here so the next commit can wire
# the dispatcher without re-loading the module — empty marker.
_ = DhcpServer
