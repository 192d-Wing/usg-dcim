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
from ..models.dns import BgpPeer
from ..models.ipam import (
    BgpAddressFamily,
    DhcpServer,
    Fabric,
    IPAddress,
    Overlay,
    Subnet,
    Supernet,
    Vni,
    Vrf,
    VrfBgpPeer,
    Vtep,
    VtepVniMembership,
)
from ..schemas.common import Page, PageParams
from ..schemas.ipam import (
    DhcpServerCreate,
    DhcpServerOut,
    DhcpServerUpdate,
    FabricCreate,
    FabricOut,
    FabricUpdate,
    IPAddressCreate,
    IPAddressOut,
    IPAddressUpdate,
    OverlayCreate,
    OverlayOut,
    OverlayUpdate,
    SubnetCreate,
    SubnetOut,
    SubnetUpdate,
    SubnetUtilization,
    SupernetCreate,
    SupernetOut,
    SupernetUpdate,
    VniCreate,
    VniOut,
    VniUpdate,
    VrfBgpPeerCreate,
    VrfBgpPeerOut,
    VrfBgpPeerUpdate,
    VrfCreate,
    VrfOut,
    VrfUpdate,
    VtepCreate,
    VtepOut,
    VtepUpdate,
    VtepVniMembershipCreate,
    VtepVniMembershipOut,
)
from ..security import audit

from ..security.deps import Principal, require_capability
from ..security.scope import enforce_fabric_scope, scope_filtered_fabric_ids
from ..services import ipam as ipam_svc
from ..services import kea as kea_svc
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
    principal: Principal = Depends(require_capability("ipam:fabrics:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(Fabric)
    if enclave is not None:
        stmt = stmt.where(Fabric.enclave == enclave)
    in_scope = await scope_filtered_fabric_ids(
        db, principal.capabilities, "ipam:fabrics:read",
    )
    if in_scope is not None:
        if not in_scope:
            return Page[FabricOut](items=[], total=0, page=params.page, page_size=params.page_size, has_more=False)
        stmt = stmt.where(Fabric.id.in_(in_scope))
    return await paginate(db, stmt, model=Fabric, params=params, out_model=FabricOut)


@router.post("/fabrics", response_model=FabricOut, status_code=201)
async def create_fabric(
    payload: FabricCreate,
    principal: Principal = Depends(require_capability("ipam:fabrics:create")),
    db: AsyncSession = Depends(get_db),
):
    # A fabric-scoped user has, by definition, no authority to create
    # NEW fabrics — the new fabric would necessarily be outside their
    # existing scope set. Only globally-scoped principals can mint
    # fabrics. find_matching_capability returns the matching Scope
    # (or None); is_global True means proceed, otherwise deny.
    from ..errors import ForbiddenError
    from ..security.deps import find_matching_capability
    cap_scope = find_matching_capability(principal.capabilities, "ipam:fabrics:create")
    if cap_scope is not None and not cap_scope.is_global:
        raise ForbiddenError("creating fabrics requires a globally-scoped grant")
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
    principal: Principal = Depends(require_capability("ipam:fabrics:read")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Fabric, fabric_id)
    if obj is None:
        raise NotFoundError(_FABRIC_NOT_FOUND)
    await enforce_fabric_scope(db, principal.capabilities, obj.id, "ipam:fabrics:read")
    return obj


@router.patch("/fabrics/{fabric_id}", response_model=FabricOut)
async def update_fabric(
    fabric_id: UUID,
    payload: FabricUpdate,
    principal: Principal = Depends(require_capability("ipam:fabrics:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Fabric, fabric_id)
    if obj is None:
        raise NotFoundError(_FABRIC_NOT_FOUND)
    await enforce_fabric_scope(db, principal.capabilities, obj.id, "ipam:fabrics:update")
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
    principal: Principal = Depends(require_capability("ipam:fabrics:delete")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Fabric, fabric_id)
    if obj is None:
        raise NotFoundError(_FABRIC_NOT_FOUND)
    await enforce_fabric_scope(db, principal.capabilities, obj.id, "ipam:fabrics:delete")
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
    principal: Principal = Depends(require_capability("ipam:vrfs:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(Vrf)
    if fabric_id is not None:
        stmt = stmt.where(Vrf.fabric_id == fabric_id)
    in_scope = await scope_filtered_fabric_ids(
        db, principal.capabilities, "ipam:vrfs:read",
    )
    if in_scope is not None:
        if not in_scope:
            return Page[VrfOut](items=[], total=0, page=params.page, page_size=params.page_size, has_more=False)
        stmt = stmt.where(Vrf.fabric_id.in_(in_scope))
    return await paginate(db, stmt, model=Vrf, params=params, out_model=VrfOut)


@router.get("/vrfs/{vrf_id}", response_model=VrfOut)
async def get_vrf(
    vrf_id: UUID,
    principal: Principal = Depends(require_capability("ipam:vrfs:read")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Vrf, vrf_id)
    if obj is None:
        raise NotFoundError(_VRF_NOT_FOUND)
    await enforce_fabric_scope(db, principal.capabilities, obj.fabric_id, "ipam:vrfs:read")
    return obj


@router.post("/vrfs", response_model=VrfOut, status_code=201)
async def create_vrf(
    payload: VrfCreate,
    principal: Principal = Depends(require_capability("ipam:vrfs:create")),
    db: AsyncSession = Depends(get_db),
):
    await enforce_fabric_scope(
        db, principal.capabilities, payload.fabric_id, "ipam:vrfs:create",
    )
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
    principal: Principal = Depends(require_capability("ipam:vrfs:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Vrf, vrf_id)
    if obj is None:
        raise NotFoundError(_VRF_NOT_FOUND)
    await enforce_fabric_scope(db, principal.capabilities, obj.fabric_id, "ipam:vrfs:update")
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
    principal: Principal = Depends(require_capability("ipam:vrfs:delete")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Vrf, vrf_id)
    if obj is None:
        raise NotFoundError(_VRF_NOT_FOUND)
    await enforce_fabric_scope(db, principal.capabilities, obj.fabric_id, "ipam:vrfs:delete")
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


# ----------------------- VRF ↔ BGP peer bindings -----------------------
#
# vrf_bgp_peers is a many-to-many: a single BGP peer (one TCP session)
# can carry the same VRF across multiple address families (VPNv4 /
# VPNv6 / EVPN), and each (VRF, peer, AF) tuple has its own Route
# Distinguisher. The unique constraint enforces one row per tuple.

_VRF_BGP_PEER_NOT_FOUND = "vrf bgp peer binding not found"


@router.get("/vrf-bgp-peers", response_model=Page[VrfBgpPeerOut])
async def list_vrf_bgp_peers(
    params: PageParams = Depends(PageParams.from_query),
    vrf_id: UUID | None = Query(None),
    bgp_peer_id: UUID | None = Query(None),
    address_family: BgpAddressFamily | None = Query(None),
    _: Principal = Depends(require_capability("ipam:vrf-bgp-peers:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(VrfBgpPeer)
    if vrf_id is not None:
        stmt = stmt.where(VrfBgpPeer.vrf_id == vrf_id)
    if bgp_peer_id is not None:
        stmt = stmt.where(VrfBgpPeer.bgp_peer_id == bgp_peer_id)
    if address_family is not None:
        stmt = stmt.where(VrfBgpPeer.address_family == address_family)
    return await paginate(
        db, stmt, model=VrfBgpPeer, params=params, out_model=VrfBgpPeerOut,
    )


@router.post("/vrf-bgp-peers", response_model=VrfBgpPeerOut, status_code=201)
async def create_vrf_bgp_peer(
    payload: VrfBgpPeerCreate,
    principal: Principal = Depends(require_capability("ipam:vrf-bgp-peers:create")),
    db: AsyncSession = Depends(get_db),
):
    vrf = await db.get(Vrf, payload.vrf_id)
    if vrf is None:
        raise NotFoundError(_VRF_NOT_FOUND)
    await enforce_fabric_scope(
        db, principal.capabilities, vrf.fabric_id, "ipam:vrf-bgp-peers:create",
    )
    peer = await db.get(BgpPeer, payload.bgp_peer_id)
    if peer is None:
        raise NotFoundError("bgp peer not found")
    # Surface the unique-tuple collision as a 409 instead of letting
    # the DB raise an IntegrityError that the operator can't act on.
    existing = (
        await db.execute(
            select(VrfBgpPeer.id).where(
                VrfBgpPeer.vrf_id == payload.vrf_id,
                VrfBgpPeer.bgp_peer_id == payload.bgp_peer_id,
                VrfBgpPeer.address_family == payload.address_family,
            ),
        )
    ).scalar_one_or_none()
    if existing is not None:
        raise ConflictError(
            "binding already exists for this (vrf, peer, address_family)",
        )
    obj = VrfBgpPeer(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="vrf_bgp_peer.create",
        target_type="vrf_bgp_peer", target_id=str(obj.id),
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/vrf-bgp-peers/{binding_id}", response_model=VrfBgpPeerOut)
async def update_vrf_bgp_peer(
    binding_id: UUID,
    payload: VrfBgpPeerUpdate,
    principal: Principal = Depends(require_capability("ipam:vrf-bgp-peers:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(VrfBgpPeer, binding_id)
    if obj is None:
        raise NotFoundError(_VRF_BGP_PEER_NOT_FOUND)
    vrf = await db.get(Vrf, obj.vrf_id)
    await enforce_fabric_scope(
        db, principal.capabilities,
        vrf.fabric_id if vrf else None,
        "ipam:vrf-bgp-peers:update",
    )
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="vrf_bgp_peer.update",
        target_type="vrf_bgp_peer", target_id=str(binding_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/vrf-bgp-peers/{binding_id}", status_code=204)
async def delete_vrf_bgp_peer(
    binding_id: UUID,
    principal: Principal = Depends(require_capability("ipam:vrf-bgp-peers:delete")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(VrfBgpPeer, binding_id)
    if obj is None:
        raise NotFoundError(_VRF_BGP_PEER_NOT_FOUND)
    vrf = await db.get(Vrf, obj.vrf_id)
    await enforce_fabric_scope(
        db, principal.capabilities,
        vrf.fabric_id if vrf else None,
        "ipam:vrf-bgp-peers:delete",
    )
    await db.execute(delete(VrfBgpPeer).where(VrfBgpPeer.id == binding_id))
    await audit.record(
        db, principal, action="vrf_bgp_peer.delete",
        target_type="vrf_bgp_peer", target_id=str(binding_id),
    )
    await db.commit()


# ----------------------- Supernets -----------------------
@router.get("/supernets", response_model=Page[SupernetOut])
async def list_supernets(
    params: PageParams = Depends(PageParams.from_query),
    fabric_id: UUID | None = Query(None),
    vrf_id: UUID | None = Query(None),
    parent_supernet_id: UUID | None = Query(
        None, description="Filter by parent. Pass the literal string 'null' to fetch top-level supernets only.",
    ),
    top_level: bool = Query(False, description="Shortcut for parent_supernet_id IS NULL."),
    principal: Principal = Depends(require_capability("ipam:supernets:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(Supernet)
    if fabric_id is not None:
        stmt = stmt.where(Supernet.fabric_id == fabric_id)
    if vrf_id is not None:
        stmt = stmt.where(Supernet.vrf_id == vrf_id)
    if top_level:
        stmt = stmt.where(Supernet.parent_supernet_id.is_(None))
    elif parent_supernet_id is not None:
        stmt = stmt.where(Supernet.parent_supernet_id == parent_supernet_id)
    in_scope = await scope_filtered_fabric_ids(
        db, principal.capabilities, "ipam:supernets:read",
    )
    if in_scope is not None:
        if not in_scope:
            return Page[SupernetOut](items=[], total=0, page=params.page, page_size=params.page_size, has_more=False)
        stmt = stmt.where(Supernet.fabric_id.in_(in_scope))
    return await paginate(db, stmt, model=Supernet, params=params, out_model=SupernetOut)


@router.post("/supernets", response_model=SupernetOut, status_code=201)
async def create_supernet(
    payload: SupernetCreate,
    principal: Principal = Depends(require_capability("ipam:supernets:create")),
    db: AsyncSession = Depends(get_db),
):
    await enforce_fabric_scope(
        db, principal.capabilities, payload.fabric_id, "ipam:supernets:create",
    )
    vrf = await db.get(Vrf, payload.vrf_id)
    if vrf is None or vrf.fabric_id != payload.fabric_id:
        raise ValidationError("vrf does not belong to that fabric")
    parent_purpose: str | None = None
    if payload.parent_supernet_id is not None:
        parent = await ipam_svc.assert_supernet_inside_parent(
            db, parent_supernet_id=payload.parent_supernet_id, prefix=payload.prefix,
            fabric_id=payload.fabric_id, vrf_id=payload.vrf_id,
        )
        parent_purpose = parent.purpose
    # Sibling-overlap check at this level (top-level or under a shared parent).
    await ipam_svc.assert_supernet_unique_in_vrf(
        db, fabric_id=payload.fabric_id, vrf_id=payload.vrf_id, prefix=payload.prefix,
        parent_supernet_id=payload.parent_supernet_id,
    )
    # Same purpose-inheritance rule we apply to subnets, applied one level up.
    ipam_svc.assert_purpose_compatible(
        supernet_purpose=parent_purpose, subnet_purpose=payload.purpose,
    )
    obj = Supernet(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="supernet.create",
        target_type="supernet", target_id=str(obj.id),
        site_id=obj.site_id, metadata={"prefix": payload.prefix},
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/supernets/{supernet_id}", response_model=SupernetOut)
async def update_supernet(
    supernet_id: UUID,
    payload: SupernetUpdate,
    principal: Principal = Depends(require_capability("ipam:supernets:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Supernet, supernet_id)
    if obj is None:
        raise NotFoundError(_SUPERNET_NOT_FOUND)
    await enforce_fabric_scope(
        db, principal.capabilities, obj.fabric_id, "ipam:supernets:update",
    )
    diff = payload.model_dump(exclude_unset=True)
    if "parent_supernet_id" in diff and diff["parent_supernet_id"] is not None:
        if diff["parent_supernet_id"] == supernet_id:
            raise ValidationError("a supernet cannot be its own parent")
        new_parent = await ipam_svc.assert_supernet_inside_parent(
            db, parent_supernet_id=diff["parent_supernet_id"], prefix=str(obj.prefix),
            fabric_id=obj.fabric_id, vrf_id=obj.vrf_id,
        )
        # Inherit/check purpose against the new parent before applying.
        ipam_svc.assert_purpose_compatible(
            supernet_purpose=new_parent.purpose,
            subnet_purpose=diff.get("purpose", obj.purpose),
        )
    if "purpose" in diff:
        await ipam_svc.assert_supernet_purpose_change_safe(
            db, supernet_id=supernet_id, new_purpose=diff["purpose"],
        )
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="supernet.update", target_type="supernet",
        target_id=str(supernet_id), site_id=obj.site_id, diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/supernets/{supernet_id}", status_code=204)
async def delete_supernet(
    supernet_id: UUID,
    principal: Principal = Depends(require_capability("ipam:supernets:delete")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Supernet, supernet_id)
    if obj is None:
        raise NotFoundError(_SUPERNET_NOT_FOUND)
    await enforce_fabric_scope(
        db, principal.capabilities, obj.fabric_id, "ipam:supernets:delete",
    )
    in_use = (
        await db.execute(select(Subnet.id).where(Subnet.supernet_id == supernet_id).limit(1))
    ).scalar_one_or_none()
    if in_use is not None:
        raise ConflictError("supernet still has subnets; remove them first")
    child_supernet = (
        await db.execute(
            select(Supernet.id).where(Supernet.parent_supernet_id == supernet_id).limit(1)
        )
    ).scalar_one_or_none()
    if child_supernet is not None:
        raise ConflictError("supernet still has child supernets; remove them first")
    await db.execute(delete(Supernet).where(Supernet.id == supernet_id))
    await audit.record(
        db, principal, action="supernet.delete",
        target_type="supernet", target_id=str(supernet_id),
    )
    await db.commit()


@router.get("/supernets/{supernet_id}/utilization")
async def supernet_utilization(
    supernet_id: UUID,
    _: Principal = Depends(require_capability("ipam:supernets:read")),
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
    principal: Principal = Depends(require_capability("ipam:subnets:read")),
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
    in_scope = await scope_filtered_fabric_ids(
        db, principal.capabilities, "ipam:subnets:read",
    )
    if in_scope is not None:
        if not in_scope:
            return Page[SubnetOut](items=[], total=0, page=params.page, page_size=params.page_size, has_more=False)
        stmt = stmt.where(Subnet.fabric_id.in_(in_scope))
    return await paginate(db, stmt, model=Subnet, params=params, out_model=SubnetOut)


@router.post("/subnets", response_model=SubnetOut, status_code=201)
async def create_subnet(
    payload: SubnetCreate,
    principal: Principal = Depends(require_capability("ipam:subnets:create")),
    db: AsyncSession = Depends(get_db),
):
    parent = await ipam_svc.assert_subnet_inside_supernet(
        db, supernet_id=payload.supernet_id, prefix=payload.prefix,
    )
    # Scope is inherited from the parent supernet's fabric.
    await enforce_fabric_scope(
        db, principal.capabilities, parent.fabric_id, "ipam:subnets:create",
    )
    await ipam_svc.assert_subnet_unique_in_vrf(
        db, fabric_id=parent.fabric_id, vrf_id=parent.vrf_id, prefix=payload.prefix,
    )
    ipam_svc.assert_purpose_compatible(
        supernet_purpose=parent.purpose, subnet_purpose=payload.purpose,
    )
    if payload.vni_id is not None:
        await ipam_svc.assert_subnet_vni_compatible(
            db, vni_id=payload.vni_id, fabric_id=parent.fabric_id,
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
    principal: Principal = Depends(require_capability("ipam:subnets:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Subnet, subnet_id)
    if obj is None:
        raise NotFoundError(_SUBNET_NOT_FOUND)
    await enforce_fabric_scope(
        db, principal.capabilities, obj.fabric_id, "ipam:subnets:update",
    )
    diff = payload.model_dump(exclude_unset=True)

    # Move support: when supernet_id changes, re-validate containment +
    # per-VRF uniqueness against the new parent and re-derive fabric/vrf
    # from it (the subnet must mirror the parent's namespace).
    moving = "supernet_id" in diff and diff["supernet_id"] != obj.supernet_id
    new_parent: Supernet | None = None
    if moving:
        new_parent = await ipam_svc.assert_subnet_inside_supernet(
            db, supernet_id=diff["supernet_id"], prefix=str(obj.prefix),
        )
        # If moving to a different fabric, that fabric must also be in scope.
        await enforce_fabric_scope(
            db, principal.capabilities, new_parent.fabric_id, "ipam:subnets:update",
        )
        await ipam_svc.assert_subnet_unique_in_vrf(
            db, fabric_id=new_parent.fabric_id, vrf_id=new_parent.vrf_id,
            prefix=str(obj.prefix), exclude_id=obj.id,
        )
        diff["fabric_id"] = new_parent.fabric_id
        diff["vrf_id"] = new_parent.vrf_id

    # Effective parent for downstream purpose/vni checks: the new one if
    # we're moving, otherwise the existing one.
    effective_parent = new_parent or await db.get(Supernet, obj.supernet_id)
    if "purpose" in diff or moving:
        ipam_svc.assert_purpose_compatible(
            supernet_purpose=effective_parent.purpose if effective_parent else None,
            subnet_purpose=diff.get("purpose", obj.purpose),
        )
    # VNI binding must live in the post-move fabric.
    target_fabric = (new_parent.fabric_id if new_parent else obj.fabric_id)
    if "vni_id" in diff and diff["vni_id"] is not None:
        await ipam_svc.assert_subnet_vni_compatible(
            db, vni_id=diff["vni_id"], fabric_id=target_fabric,
        )
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
    principal: Principal = Depends(require_capability("ipam:subnets:delete")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Subnet, subnet_id)
    if obj is None:
        raise NotFoundError(_SUBNET_NOT_FOUND)
    await enforce_fabric_scope(
        db, principal.capabilities, obj.fabric_id, "ipam:subnets:delete",
    )
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
    _: Principal = Depends(require_capability("ipam:subnets:read")),
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
    principal: Principal = Depends(require_capability("ipam:addresses:read")),
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
    # IPAddress has no direct fabric_id — join to Subnet.
    in_scope = await scope_filtered_fabric_ids(
        db, principal.capabilities, "ipam:addresses:read",
    )
    if in_scope is not None:
        if not in_scope:
            return Page[IPAddressOut](items=[], total=0, page=params.page, page_size=params.page_size, has_more=False)
        stmt = stmt.where(IPAddress.subnet_id.in_(
            select(Subnet.id).where(Subnet.fabric_id.in_(in_scope))
        ))
    return await paginate(db, stmt, model=IPAddress, params=params, out_model=IPAddressOut)


@router.post("/addresses", response_model=IPAddressOut, status_code=201)
async def create_address(
    payload: IPAddressCreate,
    principal: Principal = Depends(require_capability("ipam:addresses:create")),
    db: AsyncSession = Depends(get_db),
):
    # Fabric scope: derive from the parent subnet.
    subnet = await db.get(Subnet, payload.subnet_id)
    if subnet is None:
        raise ValidationError(f"subnet {payload.subnet_id} not found")
    await enforce_fabric_scope(
        db, principal.capabilities, subnet.fabric_id, "ipam:addresses:create",
    )
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
    principal: Principal = Depends(require_capability("ipam:addresses:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(IPAddress, ip_id)
    if obj is None:
        raise NotFoundError(_IP_NOT_FOUND)
    subnet = await db.get(Subnet, obj.subnet_id)
    await enforce_fabric_scope(
        db, principal.capabilities,
        subnet.fabric_id if subnet else None,
        "ipam:addresses:update",
    )
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
    principal: Principal = Depends(require_capability("ipam:addresses:delete")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(IPAddress, ip_id)
    if obj is None:
        raise NotFoundError(_IP_NOT_FOUND)
    subnet = await db.get(Subnet, obj.subnet_id)
    await enforce_fabric_scope(
        db, principal.capabilities,
        subnet.fabric_id if subnet else None,
        "ipam:addresses:delete",
    )
    await db.execute(delete(IPAddress).where(IPAddress.id == ip_id))
    await audit.record(
        db, principal, action="ip_address.delete",
        target_type="ip_address", target_id=str(ip_id),
    )
    await db.commit()


# ----------------------- Free-space finders -----------------------


@router.get("/free-space/in-subnets")
async def free_space_in_subnets(
    fabric_id: UUID | None = Query(None),
    vrf_id: UUID | None = Query(None),
    family: str | None = Query(None, regex="^(v4|v6)$"),
    min_free: int = Query(1, ge=1, description="Minimum free addresses required."),
    limit: int = Query(50, ge=1, le=500),
    _: Principal = Depends(require_capability("ipam:subnets:read")),
    db: AsyncSession = Depends(get_db),
):
    """Subnets with at least `min_free` addresses available, sorted by
    free count descending. Use this to answer "where can I place 32 more
    hosts?" — narrow by fabric / vrf / address family.

    Counting "contiguous free" exactly would require enumerating every
    address in the prefix; for IPv4 /20 (~4k) that's fine but for IPv6
    /64 it isn't. We return free-count (capacity - allocated) which is a
    fast O(1) per-subnet check and is what operators actually care about
    for capacity planning.
    """
    stmt = select(Subnet)
    if fabric_id is not None:
        stmt = stmt.where(Subnet.fabric_id == fabric_id)
    if vrf_id is not None:
        stmt = stmt.where(Subnet.vrf_id == vrf_id)
    subnets = (await db.execute(stmt)).scalars().all()

    out: list[dict] = []
    for s in subnets:
        prefix = str(s.prefix)
        if family == "v4" and ":" in prefix:
            continue
        if family == "v6" and ":" not in prefix:
            continue
        capacity = ipam_svc.network_capacity(prefix)
        used = await ipam_svc.used_addresses_in_subnet(db, s.id)
        free = max(0, capacity - len(used))
        if free < min_free:
            continue
        out.append({
            "subnet_id": str(s.id),
            "prefix": prefix,
            "name": s.name,
            "site_id": str(s.site_id) if s.site_id else None,
            "fabric_id": str(s.fabric_id),
            "vrf_id": str(s.vrf_id),
            "purpose": s.purpose,
            "capacity": capacity,
            "allocated": len(used),
            "free": free,
            "next_available": ipam_svc.next_free_address(prefix, used),
        })
    out.sort(key=lambda r: -r["free"])
    return {
        "query": {
            "fabric_id": str(fabric_id) if fabric_id else None,
            "vrf_id": str(vrf_id) if vrf_id else None,
            "family": family, "min_free": min_free,
        },
        "subnets": out[:limit],
        "count": len(out[:limit]),
    }


def _supernet_matches_family(sn_prefix: str, family: str | None) -> bool:
    """Cheap inline family filter — colon ⇒ v6, no colon ⇒ v4."""
    is_v4 = ":" not in sn_prefix
    if family == "v4":
        return is_v4
    if family == "v6":
        return not is_v4
    return True


def _carve_supernet(
    sn: Supernet, prefix_size: int, allocated: list[str], limit: int,
) -> dict | None:
    """Run the pure carver against a single supernet, return a result row
    or None if no candidates (caller filters)."""
    sn_prefix = str(sn.prefix)
    max_size = 32 if ":" not in sn_prefix else 128
    if prefix_size > max_size:
        return None
    candidates = ipam_svc.find_free_prefixes_in_supernet(
        sn_prefix, prefix_size, allocated, limit=limit,
    )
    if not candidates:
        return None
    return {
        "supernet_id": str(sn.id),
        "supernet_prefix": sn_prefix,
        "supernet_name": sn.name,
        "fabric_id": str(sn.fabric_id),
        "vrf_id": str(sn.vrf_id),
        "purpose": sn.purpose,
        "candidates": candidates,
        "count": len(candidates),
    }


async def _allocated_prefixes_by_supernet(
    db: AsyncSession, supernet_ids: list[UUID],
) -> dict[UUID, list[str]]:
    """Pull all subnet prefixes in scope in one query and group them so
    the carver doesn't issue N+1 lookups."""
    if not supernet_ids:
        return {}
    rows = (
        await db.execute(
            select(Subnet.supernet_id, Subnet.prefix).where(
                Subnet.supernet_id.in_(supernet_ids)
            )
        )
    ).all()
    out: dict[UUID, list[str]] = {}
    for sn_id, prefix in rows:
        out.setdefault(sn_id, []).append(str(prefix))
    return out


@router.get("/free-space/prefixes")
async def free_space_prefixes(
    prefix_size: int = Query(
        ..., ge=1, le=128,
        description="Target prefix length, e.g. 24 for /24 or 64 for /64.",
    ),
    fabric_id: UUID | None = Query(None),
    vrf_id: UUID | None = Query(None),
    supernet_id: UUID | None = Query(None),
    family: str | None = Query(None, regex="^(v4|v6)$"),
    limit_per_supernet: int = Query(20, ge=1, le=200),
    _: Principal = Depends(require_capability("ipam:supernets:read")),
    db: AsyncSession = Depends(get_db),
):
    """Find unallocated CIDR blocks of a specific size inside supernets.

    Use cases:
      - "Carve a new /24 in fabric=prod": prefix_size=24, fabric_id=prod
      - "Find a free /64 inside the IL5 v6 fabric": prefix_size=64,
        fabric_id=il5, family=v6
      - "Find candidates inside this exact supernet": supernet_id=...

    Returns up to `limit_per_supernet` candidate prefixes per supernet,
    grouped so the operator can pick which supernet to carve from.
    """
    stmt = select(Supernet)
    if supernet_id is not None:
        stmt = stmt.where(Supernet.id == supernet_id)
    if fabric_id is not None:
        stmt = stmt.where(Supernet.fabric_id == fabric_id)
    if vrf_id is not None:
        stmt = stmt.where(Supernet.vrf_id == vrf_id)
    supernets = (await db.execute(stmt)).scalars().all()

    subnets_by_supernet = await _allocated_prefixes_by_supernet(
        db, [s.id for s in supernets],
    )

    out: list[dict] = []
    for sn in supernets:
        if not _supernet_matches_family(str(sn.prefix), family):
            continue
        row = _carve_supernet(
            sn, prefix_size,
            subnets_by_supernet.get(sn.id, []),
            limit_per_supernet,
        )
        if row is not None:
            out.append(row)
    return {
        "query": {
            "prefix_size": prefix_size,
            "fabric_id": str(fabric_id) if fabric_id else None,
            "vrf_id": str(vrf_id) if vrf_id else None,
            "supernet_id": str(supernet_id) if supernet_id else None,
            "family": family,
        },
        "supernets": out,
    }


# ----------------------- Overlays / VNIs / VTEPs -----------------------
_OVERLAY_NOT_FOUND = "overlay not found"
_VNI_NOT_FOUND = "vni not found"
_VTEP_NOT_FOUND = "vtep not found"
_MEMBERSHIP_NOT_FOUND = "vtep/vni membership not found"


@router.get("/overlays", response_model=Page[OverlayOut])
async def list_overlays(
    params: PageParams = Depends(PageParams.from_query),
    fabric_id: UUID | None = Query(None),
    principal: Principal = Depends(require_capability("ipam:overlays:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(Overlay)
    if fabric_id is not None:
        stmt = stmt.where(Overlay.fabric_id == fabric_id)
    in_scope = await scope_filtered_fabric_ids(
        db, principal.capabilities, "ipam:overlays:read",
    )
    if in_scope is not None:
        if not in_scope:
            return Page[OverlayOut](items=[], total=0, page=params.page, page_size=params.page_size, has_more=False)
        stmt = stmt.where(Overlay.fabric_id.in_(in_scope))
    return await paginate(db, stmt, model=Overlay, params=params, out_model=OverlayOut)


@router.post("/overlays", response_model=OverlayOut, status_code=201)
async def create_overlay(
    payload: OverlayCreate,
    principal: Principal = Depends(require_capability("ipam:overlays:create")),
    db: AsyncSession = Depends(get_db),
):
    await enforce_fabric_scope(
        db, principal.capabilities, payload.fabric_id, "ipam:overlays:create",
    )
    fabric = await db.get(Fabric, payload.fabric_id)
    if fabric is None:
        raise ValidationError(f"fabric {payload.fabric_id} not found")
    if payload.underlay_vrf_id is not None:
        vrf = await db.get(Vrf, payload.underlay_vrf_id)
        if vrf is None or vrf.fabric_id != payload.fabric_id:
            raise ValidationError("underlay vrf must live in the same fabric")
    obj = Overlay(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="overlay.create",
        target_type="overlay", target_id=str(obj.id),
        metadata={"name": payload.name, "kind": payload.kind.value},
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/overlays/{overlay_id}", response_model=OverlayOut)
async def update_overlay(
    overlay_id: UUID,
    payload: OverlayUpdate,
    principal: Principal = Depends(require_capability("ipam:overlays:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Overlay, overlay_id)
    if obj is None:
        raise NotFoundError(_OVERLAY_NOT_FOUND)
    await enforce_fabric_scope(
        db, principal.capabilities, obj.fabric_id, "ipam:overlays:update",
    )
    diff = payload.model_dump(exclude_unset=True)
    if "underlay_vrf_id" in diff and diff["underlay_vrf_id"] is not None:
        vrf = await db.get(Vrf, diff["underlay_vrf_id"])
        if vrf is None or vrf.fabric_id != obj.fabric_id:
            raise ValidationError("underlay vrf must live in the same fabric")
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="overlay.update", target_type="overlay",
        target_id=str(overlay_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/overlays/{overlay_id}", status_code=204)
async def delete_overlay(
    overlay_id: UUID,
    principal: Principal = Depends(require_capability("ipam:overlays:delete")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Overlay, overlay_id)
    if obj is None:
        raise NotFoundError(_OVERLAY_NOT_FOUND)
    await enforce_fabric_scope(
        db, principal.capabilities, obj.fabric_id, "ipam:overlays:delete",
    )
    blocked = (
        await db.execute(select(Vni.id).where(Vni.overlay_id == overlay_id).limit(1))
    ).scalar_one_or_none()
    if blocked is not None:
        raise ConflictError("overlay still has VNIs; remove them first")
    blocked_vtep = (
        await db.execute(select(Vtep.id).where(Vtep.overlay_id == overlay_id).limit(1))
    ).scalar_one_or_none()
    if blocked_vtep is not None:
        raise ConflictError("overlay still has VTEPs; remove them first")
    await db.execute(delete(Overlay).where(Overlay.id == overlay_id))
    await audit.record(
        db, principal, action="overlay.delete",
        target_type="overlay", target_id=str(overlay_id),
    )
    await db.commit()


@router.get("/vnis", response_model=Page[VniOut])
async def list_vnis(
    params: PageParams = Depends(PageParams.from_query),
    overlay_id: UUID | None = Query(None),
    fabric_id: UUID | None = Query(None),
    kind: str | None = Query(None, regex="^(l2|l3)$"),
    _: Principal = Depends(require_capability("ipam:vnis:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(Vni)
    if overlay_id is not None:
        stmt = stmt.where(Vni.overlay_id == overlay_id)
    if fabric_id is not None:
        stmt = stmt.where(Vni.overlay_id.in_(
            select(Overlay.id).where(Overlay.fabric_id == fabric_id)
        ))
    if kind is not None:
        stmt = stmt.where(Vni.kind == kind)
    return await paginate(db, stmt, model=Vni, params=params, out_model=VniOut)


@router.post("/vnis", response_model=VniOut, status_code=201)
async def create_vni(
    payload: VniCreate,
    principal: Principal = Depends(require_capability("ipam:vnis:create")),
    db: AsyncSession = Depends(get_db),
):
    overlay = await db.get(Overlay, payload.overlay_id)
    if overlay is None:
        raise ValidationError(f"overlay {payload.overlay_id} not found")
    await enforce_fabric_scope(
        db, principal.capabilities, overlay.fabric_id, "ipam:vnis:create",
    )
    ipam_svc.assert_vni_in_range(payload.vni)
    ipam_svc.assert_vni_kind_consistent(
        kind=payload.kind, vlan_id=payload.vlan_id, vrf_id=payload.vrf_id,
    )
    if payload.vrf_id is not None:
        vrf = await db.get(Vrf, payload.vrf_id)
        if vrf is None or vrf.fabric_id != overlay.fabric_id:
            raise ValidationError("vni vrf must live in the overlay's fabric")
    existing = (
        await db.execute(
            select(Vni).where(Vni.overlay_id == payload.overlay_id, Vni.vni == payload.vni)
        )
    ).scalar_one_or_none()
    if existing is not None:
        raise ConflictError(
            f"vni {payload.vni} already exists in this overlay",
            details={"existing_id": str(existing.id)},
        )
    obj = Vni(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="vni.create",
        target_type="vni", target_id=str(obj.id),
        metadata={"vni": payload.vni, "kind": payload.kind.value},
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/vnis/{vni_id}", response_model=VniOut)
async def update_vni(
    vni_id: UUID,
    payload: VniUpdate,
    principal: Principal = Depends(require_capability("ipam:vnis:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Vni, vni_id)
    if obj is None:
        raise NotFoundError(_VNI_NOT_FOUND)
    overlay = await db.get(Overlay, obj.overlay_id)
    await enforce_fabric_scope(
        db, principal.capabilities,
        overlay.fabric_id if overlay else None,
        "ipam:vnis:update",
    )
    diff = payload.model_dump(exclude_unset=True)
    new_kind = diff.get("kind", obj.kind)
    new_vlan = diff.get("vlan_id", obj.vlan_id)
    new_vrf = diff.get("vrf_id", obj.vrf_id)
    ipam_svc.assert_vni_kind_consistent(kind=new_kind, vlan_id=new_vlan, vrf_id=new_vrf)
    if "vrf_id" in diff and diff["vrf_id"] is not None:
        overlay = await db.get(Overlay, obj.overlay_id)
        vrf = await db.get(Vrf, diff["vrf_id"])
        if vrf is None or (overlay and vrf.fabric_id != overlay.fabric_id):
            raise ValidationError("vni vrf must live in the overlay's fabric")
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="vni.update", target_type="vni",
        target_id=str(vni_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/vnis/{vni_id}", status_code=204)
async def delete_vni(
    vni_id: UUID,
    principal: Principal = Depends(require_capability("ipam:vnis:delete")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Vni, vni_id)
    if obj is None:
        raise NotFoundError(_VNI_NOT_FOUND)
    overlay = await db.get(Overlay, obj.overlay_id)
    await enforce_fabric_scope(
        db, principal.capabilities,
        overlay.fabric_id if overlay else None,
        "ipam:vnis:delete",
    )
    bound_subnet = (
        await db.execute(select(Subnet.id).where(Subnet.vni_id == vni_id).limit(1))
    ).scalar_one_or_none()
    if bound_subnet is not None:
        raise ConflictError("vni is still bound to one or more subnets; unbind first")
    membership = (
        await db.execute(
            select(VtepVniMembership.id).where(VtepVniMembership.vni_id == vni_id).limit(1)
        )
    ).scalar_one_or_none()
    if membership is not None:
        raise ConflictError("vni is still advertised by one or more VTEPs; remove memberships first")
    await db.execute(delete(Vni).where(Vni.id == vni_id))
    await audit.record(
        db, principal, action="vni.delete", target_type="vni", target_id=str(vni_id),
    )
    await db.commit()


@router.get("/vteps", response_model=Page[VtepOut])
async def list_vteps(
    params: PageParams = Depends(PageParams.from_query),
    overlay_id: UUID | None = Query(None),
    asset_id: UUID | None = Query(None),
    _: Principal = Depends(require_capability("ipam:vteps:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(Vtep)
    if overlay_id is not None:
        stmt = stmt.where(Vtep.overlay_id == overlay_id)
    if asset_id is not None:
        stmt = stmt.where(Vtep.asset_id == asset_id)
    return await paginate(db, stmt, model=Vtep, params=params, out_model=VtepOut)


@router.post("/vteps", response_model=VtepOut, status_code=201)
async def create_vtep(
    payload: VtepCreate,
    principal: Principal = Depends(require_capability("ipam:vteps:create")),
    db: AsyncSession = Depends(get_db),
):
    overlay = await db.get(Overlay, payload.overlay_id)
    if overlay is None:
        raise ValidationError(f"overlay {payload.overlay_id} not found")
    await enforce_fabric_scope(
        db, principal.capabilities, overlay.fabric_id, "ipam:vteps:create",
    )
    existing = (
        await db.execute(
            select(Vtep).where(
                Vtep.overlay_id == payload.overlay_id, Vtep.asset_id == payload.asset_id,
            )
        )
    ).scalar_one_or_none()
    if existing is not None:
        raise ConflictError("asset is already registered as a VTEP in this overlay")
    obj = Vtep(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="vtep.create",
        target_type="vtep", target_id=str(obj.id),
        metadata={"asset_id": str(payload.asset_id), "role": payload.role.value},
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/vteps/{vtep_id}", response_model=VtepOut)
async def update_vtep(
    vtep_id: UUID,
    payload: VtepUpdate,
    principal: Principal = Depends(require_capability("ipam:vteps:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Vtep, vtep_id)
    if obj is None:
        raise NotFoundError(_VTEP_NOT_FOUND)
    overlay = await db.get(Overlay, obj.overlay_id)
    await enforce_fabric_scope(
        db, principal.capabilities,
        overlay.fabric_id if overlay else None,
        "ipam:vteps:update",
    )
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="vtep.update", target_type="vtep",
        target_id=str(vtep_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/vteps/{vtep_id}", status_code=204)
async def delete_vtep(
    vtep_id: UUID,
    principal: Principal = Depends(require_capability("ipam:vteps:delete")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Vtep, vtep_id)
    if obj is None:
        raise NotFoundError(_VTEP_NOT_FOUND)
    overlay = await db.get(Overlay, obj.overlay_id)
    await enforce_fabric_scope(
        db, principal.capabilities,
        overlay.fabric_id if overlay else None,
        "ipam:vteps:delete",
    )
    # Memberships cascade away cleanly because the VTEP is going.
    await db.execute(delete(VtepVniMembership).where(VtepVniMembership.vtep_id == vtep_id))
    await db.execute(delete(Vtep).where(Vtep.id == vtep_id))
    await audit.record(
        db, principal, action="vtep.delete", target_type="vtep", target_id=str(vtep_id),
    )
    await db.commit()


@router.get("/vtep-memberships", response_model=Page[VtepVniMembershipOut])
async def list_vtep_memberships(
    params: PageParams = Depends(PageParams.from_query),
    vtep_id: UUID | None = Query(None),
    vni_id: UUID | None = Query(None),
    _: Principal = Depends(require_capability("ipam:vtep-memberships:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(VtepVniMembership)
    if vtep_id is not None:
        stmt = stmt.where(VtepVniMembership.vtep_id == vtep_id)
    if vni_id is not None:
        stmt = stmt.where(VtepVniMembership.vni_id == vni_id)
    return await paginate(
        db, stmt, model=VtepVniMembership, params=params, out_model=VtepVniMembershipOut,
    )


@router.post(
    "/vtep-memberships", response_model=VtepVniMembershipOut, status_code=201,
)
async def create_vtep_membership(
    payload: VtepVniMembershipCreate,
    principal: Principal = Depends(require_capability("ipam:vtep-memberships:create")),
    db: AsyncSession = Depends(get_db),
):
    vtep = await db.get(Vtep, payload.vtep_id)
    vni = await db.get(Vni, payload.vni_id)
    if vtep is None:
        raise ValidationError(f"vtep {payload.vtep_id} not found")
    if vni is None:
        raise ValidationError(f"vni {payload.vni_id} not found")
    # A VTEP can only advertise VNIs from its own overlay — otherwise the
    # row asserts something the data plane can't honor.
    if vtep.overlay_id != vni.overlay_id:
        raise ValidationError("vtep and vni must belong to the same overlay")
    overlay = await db.get(Overlay, vtep.overlay_id)
    await enforce_fabric_scope(
        db, principal.capabilities,
        overlay.fabric_id if overlay else None,
        "ipam:vtep-memberships:create",
    )
    existing = (
        await db.execute(
            select(VtepVniMembership).where(
                VtepVniMembership.vtep_id == payload.vtep_id,
                VtepVniMembership.vni_id == payload.vni_id,
            )
        )
    ).scalar_one_or_none()
    if existing is not None:
        raise ConflictError("vtep already advertises this vni")
    obj = VtepVniMembership(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="vtep_vni_membership.create",
        target_type="vtep_vni_membership", target_id=str(obj.id),
        metadata={"vtep_id": str(payload.vtep_id), "vni_id": str(payload.vni_id)},
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/vtep-memberships/{membership_id}", status_code=204)
async def delete_vtep_membership(
    membership_id: UUID,
    principal: Principal = Depends(require_capability("ipam:vtep-memberships:delete")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(VtepVniMembership, membership_id)
    if obj is None:
        raise NotFoundError(_MEMBERSHIP_NOT_FOUND)
    vtep = await db.get(Vtep, obj.vtep_id)
    overlay = await db.get(Overlay, vtep.overlay_id) if vtep else None
    await enforce_fabric_scope(
        db, principal.capabilities,
        overlay.fabric_id if overlay else None,
        "ipam:vtep-memberships:delete",
    )
    await db.execute(delete(VtepVniMembership).where(VtepVniMembership.id == membership_id))
    await audit.record(
        db, principal, action="vtep_vni_membership.delete",
        target_type="vtep_vni_membership", target_id=str(membership_id),
    )
    await db.commit()


# ----------------------- DHCP servers (Kea) -----------------------
_DHCP_NOT_FOUND = "dhcp server not found"


@router.get("/dhcp/servers", response_model=Page[DhcpServerOut])
async def list_dhcp_servers(
    params: PageParams = Depends(PageParams.from_query),
    fabric_id: UUID | None = Query(None),
    principal: Principal = Depends(require_capability("ipam:dhcp-servers:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(DhcpServer)
    if fabric_id is not None:
        stmt = stmt.where(DhcpServer.fabric_id == fabric_id)
    in_scope = await scope_filtered_fabric_ids(
        db, principal.capabilities, "ipam:dhcp-servers:read",
    )
    if in_scope is not None:
        if not in_scope:
            return Page[DhcpServerOut](items=[], total=0, page=params.page, page_size=params.page_size, has_more=False)
        stmt = stmt.where(DhcpServer.fabric_id.in_(in_scope))
    return await paginate(db, stmt, model=DhcpServer, params=params, out_model=DhcpServerOut)


@router.post("/dhcp/servers", response_model=DhcpServerOut, status_code=201)
async def create_dhcp_server(
    payload: DhcpServerCreate,
    principal: Principal = Depends(require_capability("ipam:dhcp-servers:create")),
    db: AsyncSession = Depends(get_db),
):
    await enforce_fabric_scope(
        db, principal.capabilities, payload.fabric_id, "ipam:dhcp-servers:create",
    )
    fabric = await db.get(Fabric, payload.fabric_id)
    if fabric is None:
        raise ValidationError(f"fabric {payload.fabric_id} not found")
    existing = (
        await db.execute(select(DhcpServer).where(DhcpServer.name == payload.name))
    ).scalar_one_or_none()
    if existing is not None:
        raise ConflictError("a dhcp server with that name already exists")
    obj = DhcpServer(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="dhcp_server.create",
        target_type="dhcp_server", target_id=str(obj.id),
        metadata={"kea_url": payload.kea_url, "fabric_id": str(payload.fabric_id)},
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/dhcp/servers/{server_id}", response_model=DhcpServerOut)
async def update_dhcp_server(
    server_id: UUID,
    payload: DhcpServerUpdate,
    principal: Principal = Depends(require_capability("ipam:dhcp-servers:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(DhcpServer, server_id)
    if obj is None:
        raise NotFoundError(_DHCP_NOT_FOUND)
    await enforce_fabric_scope(
        db, principal.capabilities, obj.fabric_id, "ipam:dhcp-servers:update",
    )
    diff = payload.model_dump(exclude_unset=True)
    # Don't echo the password into the audit diff.
    redacted = {k: ("***" if k == "auth_password" else v) for k, v in diff.items()}
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="dhcp_server.update", target_type="dhcp_server",
        target_id=str(server_id), diff=redacted,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/dhcp/servers/{server_id}", status_code=204)
async def delete_dhcp_server(
    server_id: UUID,
    principal: Principal = Depends(require_capability("ipam:dhcp-servers:delete")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(DhcpServer, server_id)
    if obj is None:
        raise NotFoundError(_DHCP_NOT_FOUND)
    await enforce_fabric_scope(
        db, principal.capabilities, obj.fabric_id, "ipam:dhcp-servers:delete",
    )
    await db.execute(delete(DhcpServer).where(DhcpServer.id == server_id))
    await audit.record(
        db, principal, action="dhcp_server.delete",
        target_type="dhcp_server", target_id=str(server_id),
    )
    await db.commit()


@router.post("/dhcp/servers/{server_id}/sync")
async def sync_dhcp_server_now(
    server_id: UUID,
    principal: Principal = Depends(require_capability("ipam:dhcp-servers:update")),
    db: AsyncSession = Depends(get_db),
) -> dict:
    """Trigger an on-demand sync. The cron job runs the same code path
    every 5 minutes; this endpoint is for the operator who just edited
    a server config and wants to verify it works."""
    obj = await db.get(DhcpServer, server_id)
    if obj is None:
        raise NotFoundError(_DHCP_NOT_FOUND)
    await enforce_fabric_scope(
        db, principal.capabilities, obj.fabric_id, "ipam:dhcp-servers:update",
    )
    result = await kea_svc.sync_dhcp_server(db, obj)
    await audit.record(
        db, principal, action="dhcp_server.sync",
        target_type="dhcp_server", target_id=str(server_id),
        metadata={
            "upserted": result.upserted,
            "skipped_no_subnet": result.skipped_no_subnet,
            "leases_seen": result.leases_seen,
            "error": result.error,
        },
    )
    return {
        "server_id": result.server_id,
        "upserted": result.upserted,
        "skipped_no_subnet": result.skipped_no_subnet,
        "leases_seen": result.leases_seen,
        "error": result.error,
    }
