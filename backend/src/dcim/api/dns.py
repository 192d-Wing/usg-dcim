"""CRUD over the DNS subsystem, plus the bundle endpoint the collector
polls and the sync-from-IPAM endpoint operators trigger after a big
allocation push.

Layout:
  /dns/zones                      GET POST
  /dns/zones/{id}                 GET PATCH DELETE
  /dns/zones/{id}/sync-from-ipam  POST
  /dns/zones/{id}/preview         GET    (rendered BIND text, for the UI)
  /dns/records                    GET POST
  /dns/records/{id}               GET PATCH DELETE
  /dns/servers                    GET POST
  /dns/servers/{id}               GET PATCH DELETE
  /dns/servers/{id}/bundle        GET    (collector-facing)
  /dns/servers/{id}/render-status POST   (collector-facing)
  /dns/anycast-groups             GET POST
  /dns/anycast-groups/{id}        GET PATCH DELETE
  /dns/bgp-peers                  GET POST
  /dns/bgp-peers/{id}             GET PATCH DELETE
  /dns/bgp-peers/{peer_id}/bind/{server_id}   POST DELETE
"""

from __future__ import annotations

from datetime import UTC, datetime
from uuid import UUID

from fastapi import APIRouter, Depends, Query
from pydantic import ValidationError as PydanticValidationError
from sqlalchemy import delete, func, select, update
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..errors import ConflictError, NotFoundError, ValidationError
from ..models.dns import (
    AnycastBgpBinding,
    AnycastGroup,
    BgpPeer,
    DnsForwarder,
    DnsRecord,
    DnsRecordSource,
    DnsServer,
    DnsServerRole,
    DnsZone,
    DnsZoneKind,
)
from ..models.bgp import Asn, TcpAoKeyChain
from ..models.inventory import Site
from ..models.ipam import Fabric
from ..schemas.common import Page, PageParams
from ..schemas.dns import (
    AnycastBgpBindingCreate,
    AnycastBgpBindingOut,
    AnycastGroupCreate,
    AnycastGroupOut,
    AnycastGroupUpdate,
    BgpPeerCreate,
    BgpPeerOut,
    BgpPeerUpdate,
    DnsBundle,
    DnsForwarderCreate,
    DnsForwarderOut,
    DnsForwarderUpdate,
    DnsRecordCreate,
    DnsRecordOut,
    DnsRecordUpdate,
    DnsRenderStatus,
    DnsServerCreate,
    DnsServerOut,
    DnsServerUpdate,
    DnsZoneCreate,
    DnsZoneOut,
    DnsZoneUpdate,
    validate_record_data,
)
from ..security import audit
from ..security.capabilities import INVENTORY_READ, INVENTORY_WRITE
from ..security.deps import Principal, require_capability
from ..services import dns as dns_svc
from ._pagination import paginate

router = APIRouter(prefix="/dns", tags=["dns"])

_ZONE_NOT_FOUND = "dns zone not found"
_RECORD_NOT_FOUND = "dns record not found"
_SERVER_NOT_FOUND = "dns server not found"
_ANYCAST_NOT_FOUND = "anycast group not found"
_BGP_NOT_FOUND = "bgp peer not found"
_BIND_NOT_FOUND = "anycast/bgp binding not found"
_FORWARDER_NOT_FOUND = "dns forwarder not found"


async def _touch_zone(db: AsyncSession, zone_id: UUID) -> None:
    """Bump the zone's `updated_at` to NOW(). The SOA serial renderer
    derives the serial from this timestamp, so any record change must
    move it forward — otherwise downstream resolvers won't see the new
    zone and the bundle etag won't change."""
    await db.execute(
        update(DnsZone).where(DnsZone.id == zone_id).values(updated_at=func.now())
    )


# ----------------------- Zones -----------------------
@router.get("/zones", response_model=Page[DnsZoneOut])
async def list_zones(
    params: PageParams = Depends(PageParams.from_query),
    fabric_id: UUID | None = Query(None),
    site_id: UUID | None = Query(None),
    kind: str | None = Query(None, regex="^(apex|site)$"),
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(DnsZone)
    if fabric_id is not None:
        stmt = stmt.where(DnsZone.fabric_id == fabric_id)
    if site_id is not None:
        stmt = stmt.where(DnsZone.site_id == site_id)
    if kind is not None:
        stmt = stmt.where(DnsZone.kind == kind)
    return await paginate(db, stmt, model=DnsZone, params=params, out_model=DnsZoneOut)


@router.post("/zones", response_model=DnsZoneOut, status_code=201)
async def create_zone(
    payload: DnsZoneCreate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    fabric = await db.get(Fabric, payload.fabric_id)
    if fabric is None:
        raise ValidationError(f"fabric {payload.fabric_id} not found")
    if payload.kind == DnsZoneKind.site and payload.site_id is None:
        raise ValidationError("site zones require site_id")
    if payload.kind == DnsZoneKind.apex and payload.site_id is not None:
        raise ValidationError("apex zones must not have site_id (one apex per fabric)")
    if payload.site_id is not None:
        site = await db.get(Site, payload.site_id)
        if site is None:
            raise ValidationError(f"site {payload.site_id} not found")
    existing = (
        await db.execute(select(DnsZone).where(DnsZone.name == payload.name))
    ).scalar_one_or_none()
    if existing is not None:
        raise ConflictError("a zone with that name already exists")
    obj = DnsZone(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="dns_zone.create",
        target_type="dns_zone", target_id=str(obj.id),
        site_id=obj.site_id,
        metadata={"name": payload.name, "kind": payload.kind.value},
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.get("/zones/{zone_id}", response_model=DnsZoneOut)
async def get_zone(
    zone_id: UUID,
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(DnsZone, zone_id)
    if obj is None:
        raise NotFoundError(_ZONE_NOT_FOUND)
    return obj


@router.patch("/zones/{zone_id}", response_model=DnsZoneOut)
async def update_zone(
    zone_id: UUID,
    payload: DnsZoneUpdate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(DnsZone, zone_id)
    if obj is None:
        raise NotFoundError(_ZONE_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="dns_zone.update",
        target_type="dns_zone", target_id=str(zone_id),
        site_id=obj.site_id, diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/zones/{zone_id}", status_code=204)
async def delete_zone(
    zone_id: UUID,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(DnsZone, zone_id)
    if obj is None:
        raise NotFoundError(_ZONE_NOT_FOUND)
    has_records = (
        await db.execute(select(DnsRecord.id).where(DnsRecord.zone_id == zone_id).limit(1))
    ).scalar_one_or_none()
    if has_records is not None:
        raise ConflictError("zone still has records; remove them first")
    await db.execute(delete(DnsZone).where(DnsZone.id == zone_id))
    await audit.record(
        db, principal, action="dns_zone.delete",
        target_type="dns_zone", target_id=str(zone_id),
    )
    await db.commit()


@router.post("/zones/{zone_id}/sync-from-ipam")
async def sync_zone_from_ipam(
    zone_id: UUID,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    zone = await db.get(DnsZone, zone_id)
    if zone is None:
        raise NotFoundError(_ZONE_NOT_FOUND)
    added, removed = await dns_svc.sync_ipam_records_for_zone(db, zone)
    await audit.record(
        db, principal, action="dns_zone.sync_ipam",
        target_type="dns_zone", target_id=str(zone_id),
        site_id=zone.site_id,
        metadata={"added": added, "removed": removed},
    )
    await db.commit()
    return {"zone_id": str(zone_id), "added": added, "removed": removed}


@router.get("/zones/{zone_id}/preview")
async def preview_zone(
    zone_id: UUID,
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
) -> dict:
    """Render the zone as a BIND-format text blob, useful for the UI's
    "preview" button so operators can see exactly what the collector
    will write to disk."""
    zone = await db.get(DnsZone, zone_id)
    if zone is None:
        raise NotFoundError(_ZONE_NOT_FOUND)
    records = (
        await db.execute(select(DnsRecord).where(DnsRecord.zone_id == zone_id))
    ).scalars().all()
    return {
        "zone_id": str(zone_id),
        "name": zone.name,
        "text": dns_svc.render_zone_file(zone, records),
        "record_count": len(records),
    }


# ----------------------- Records -----------------------
@router.get("/records", response_model=Page[DnsRecordOut])
async def list_records(
    params: PageParams = Depends(PageParams.from_query),
    zone_id: UUID | None = Query(None),
    type_: str | None = Query(None, alias="type"),
    source: str | None = Query(None, regex="^(ipam|manual)$"),
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(DnsRecord)
    if zone_id is not None:
        stmt = stmt.where(DnsRecord.zone_id == zone_id)
    if type_ is not None:
        stmt = stmt.where(DnsRecord.type == type_)
    if source is not None:
        stmt = stmt.where(DnsRecord.source == source)
    return await paginate(db, stmt, model=DnsRecord, params=params, out_model=DnsRecordOut)


@router.post("/records", response_model=DnsRecordOut, status_code=201)
async def create_record(
    payload: DnsRecordCreate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    zone = await db.get(DnsZone, payload.zone_id)
    if zone is None:
        raise ValidationError(f"zone {payload.zone_id} not found")
    try:
        normalized_data = validate_record_data(payload.type, payload.data)
    except PydanticValidationError as e:
        raise ValidationError(
            f"data payload invalid for {payload.type.value} record",
            details={"errors": e.errors()},
        ) from None
    obj = DnsRecord(
        zone_id=payload.zone_id,
        name=payload.name, type=payload.type, ttl=payload.ttl,
        data=normalized_data,
        source=DnsRecordSource.manual,
        description=payload.description,
    )
    db.add(obj)
    await db.flush()
    await _touch_zone(db, payload.zone_id)
    await audit.record(
        db, principal, action="dns_record.create",
        target_type="dns_record", target_id=str(obj.id),
        metadata={
            "zone_id": str(payload.zone_id),
            "name": payload.name,
            "type": payload.type.value,
            # Snapshot the rdata + TTL at creation so the audit row
            # alone tells you what was written, without having to
            # cross-reference the live record (which may have been
            # edited since or already deleted).
            "ttl": payload.ttl,
            "data": normalized_data,
        },
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/records/{record_id}", response_model=DnsRecordOut)
async def update_record(
    record_id: UUID,
    payload: DnsRecordUpdate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(DnsRecord, record_id)
    if obj is None:
        raise NotFoundError(_RECORD_NOT_FOUND)
    if obj.source == DnsRecordSource.ipam:
        raise ValidationError(
            "ipam-projected records are managed by the sync job; "
            "set the dns_name on the IPAddress instead",
        )
    diff = payload.model_dump(exclude_unset=True)
    if "data" in diff and diff["data"] is not None:
        try:
            diff["data"] = validate_record_data(obj.type, diff["data"])
        except PydanticValidationError as e:
            raise ValidationError(
                f"data payload invalid for {obj.type.value} record",
                details={"errors": e.errors()},
            ) from None
    for k, v in diff.items():
        setattr(obj, k, v)
    await _touch_zone(db, obj.zone_id)
    await audit.record(
        db, principal, action="dns_record.update",
        target_type="dns_record", target_id=str(record_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/records/{record_id}", status_code=204)
async def delete_record(
    record_id: UUID,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(DnsRecord, record_id)
    if obj is None:
        raise NotFoundError(_RECORD_NOT_FOUND)
    if obj.source == DnsRecordSource.ipam:
        raise ValidationError(
            "ipam-projected records can't be deleted directly; "
            "clear the dns_name on the IPAddress and re-sync",
        )
    # Snapshot the rdata + identity before the row goes away — the
    # audit entry needs to stand on its own once the record is gone.
    snapshot = {
        "zone_id": str(obj.zone_id),
        "name": obj.name,
        "type": obj.type.value,
        "ttl": obj.ttl,
        "data": obj.data,
    }
    await db.execute(delete(DnsRecord).where(DnsRecord.id == record_id))
    await _touch_zone(db, obj.zone_id)
    await audit.record(
        db, principal, action="dns_record.delete",
        target_type="dns_record", target_id=str(record_id),
        metadata=snapshot,
    )
    await db.commit()


# ----------------------- Servers -----------------------
@router.get("/servers", response_model=Page[DnsServerOut])
async def list_servers(
    params: PageParams = Depends(PageParams.from_query),
    site_id: UUID | None = Query(None),
    fabric_id: UUID | None = Query(None),
    role: str | None = Query(None, regex="^(auth|recursive)$"),
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(DnsServer)
    if site_id is not None:
        stmt = stmt.where(DnsServer.site_id == site_id)
    if fabric_id is not None:
        stmt = stmt.where(DnsServer.fabric_id == fabric_id)
    if role is not None:
        stmt = stmt.where(DnsServer.role == role)
    return await paginate(db, stmt, model=DnsServer, params=params, out_model=DnsServerOut)


@router.post("/servers", response_model=DnsServerOut, status_code=201)
async def create_server(
    payload: DnsServerCreate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    site = await db.get(Site, payload.site_id)
    if site is None:
        raise ValidationError(f"site {payload.site_id} not found")
    if payload.role == DnsServerRole.recursive and payload.anycast_group_id is None:
        raise ValidationError("recursive servers require an anycast_group_id")
    if payload.role == DnsServerRole.auth and payload.anycast_group_id is not None:
        raise ValidationError("auth servers must not bind an anycast group")
    if payload.anycast_group_id is not None:
        ag = await db.get(AnycastGroup, payload.anycast_group_id)
        if ag is None or ag.fabric_id != payload.fabric_id:
            raise ValidationError("anycast group must live in the server's fabric")
    obj = DnsServer(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="dns_server.create",
        target_type="dns_server", target_id=str(obj.id),
        site_id=obj.site_id,
        metadata={"name": payload.name, "role": payload.role.value},
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.get("/servers/{server_id}", response_model=DnsServerOut)
async def get_server(
    server_id: UUID,
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(DnsServer, server_id)
    if obj is None:
        raise NotFoundError(_SERVER_NOT_FOUND)
    return obj


@router.patch("/servers/{server_id}", response_model=DnsServerOut)
async def update_server(
    server_id: UUID,
    payload: DnsServerUpdate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(DnsServer, server_id)
    if obj is None:
        raise NotFoundError(_SERVER_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="dns_server.update",
        target_type="dns_server", target_id=str(server_id),
        site_id=obj.site_id, diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/servers/{server_id}", status_code=204)
async def delete_server(
    server_id: UUID,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(DnsServer, server_id)
    if obj is None:
        raise NotFoundError(_SERVER_NOT_FOUND)
    # Memberships go with the server.
    await db.execute(delete(AnycastBgpBinding).where(AnycastBgpBinding.dns_server_id == server_id))
    await db.execute(delete(DnsServer).where(DnsServer.id == server_id))
    await audit.record(
        db, principal, action="dns_server.delete",
        target_type="dns_server", target_id=str(server_id),
    )
    await db.commit()


@router.get("/servers/{server_id}/bundle", response_model=DnsBundle)
async def server_bundle(
    server_id: UUID,
    etag: str | None = Query(None, description="If unchanged, response is identical."),
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    """The collector polls this every 30s. If `etag` matches the
    current bundle, the response still includes the same etag and the
    collector can short-circuit before writing files."""
    server = await db.get(DnsServer, server_id)
    if server is None:
        raise NotFoundError(_SERVER_NOT_FOUND)
    bundle = await dns_svc.render_bundle_for_server(db, server)
    # Still return the full bundle even on etag-match — keeps the
    # client logic trivial (compare on the way out, not during the
    # request).
    return DnsBundle(**bundle)


@router.post("/servers/{server_id}/render-status")
async def post_render_status(
    server_id: UUID,
    payload: DnsRenderStatus,
    _: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
) -> dict:
    """Collector callback after every render attempt. We mirror the
    DhcpServer last_sync_* shape on DnsServer."""
    server = await db.get(DnsServer, server_id)
    if server is None:
        raise NotFoundError(_SERVER_NOT_FOUND)
    server.last_render_at = datetime.now(UTC)
    server.last_render_status = payload.status
    server.last_render_error = payload.error
    server.last_render_etag = payload.etag
    if payload.coredns_version:
        server.coredns_version = payload.coredns_version
    await db.commit()
    return {"server_id": str(server_id), "status": payload.status}


# ----------------------- Anycast groups -----------------------
@router.get("/anycast-groups", response_model=Page[AnycastGroupOut])
async def list_anycast_groups(
    params: PageParams = Depends(PageParams.from_query),
    fabric_id: UUID | None = Query(None),
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(AnycastGroup)
    if fabric_id is not None:
        stmt = stmt.where(AnycastGroup.fabric_id == fabric_id)
    return await paginate(db, stmt, model=AnycastGroup, params=params, out_model=AnycastGroupOut)


@router.post("/anycast-groups", response_model=AnycastGroupOut, status_code=201)
async def create_anycast_group(
    payload: AnycastGroupCreate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    fabric = await db.get(Fabric, payload.fabric_id)
    if fabric is None:
        raise ValidationError(f"fabric {payload.fabric_id} not found")
    if payload.anycast_ipv4 is None and payload.anycast_ipv6 is None:
        raise ValidationError("at least one of anycast_ipv4 or anycast_ipv6 must be set")
    obj = AnycastGroup(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="anycast_group.create",
        target_type="anycast_group", target_id=str(obj.id),
        metadata={"name": payload.name, "service": payload.service.value},
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/anycast-groups/{group_id}", response_model=AnycastGroupOut)
async def update_anycast_group(
    group_id: UUID,
    payload: AnycastGroupUpdate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(AnycastGroup, group_id)
    if obj is None:
        raise NotFoundError(_ANYCAST_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="anycast_group.update",
        target_type="anycast_group", target_id=str(group_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/anycast-groups/{group_id}", status_code=204)
async def delete_anycast_group(
    group_id: UUID,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(AnycastGroup, group_id)
    if obj is None:
        raise NotFoundError(_ANYCAST_NOT_FOUND)
    bound = (
        await db.execute(
            select(DnsServer.id).where(DnsServer.anycast_group_id == group_id).limit(1)
        )
    ).scalar_one_or_none()
    if bound is not None:
        raise ConflictError("anycast group still bound to one or more DNS servers; unbind first")
    await db.execute(delete(AnycastGroup).where(AnycastGroup.id == group_id))
    await audit.record(
        db, principal, action="anycast_group.delete",
        target_type="anycast_group", target_id=str(group_id),
    )
    await db.commit()


# ----------------------- Conditional forwarders -----------------------
@router.get("/forwarders", response_model=Page[DnsForwarderOut])
async def list_forwarders(
    params: PageParams = Depends(PageParams.from_query),
    fabric_id: UUID | None = Query(None),
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(DnsForwarder)
    if fabric_id is not None:
        stmt = stmt.where(DnsForwarder.fabric_id == fabric_id)
    return await paginate(
        db, stmt, model=DnsForwarder, params=params, out_model=DnsForwarderOut,
    )


@router.post("/forwarders", response_model=DnsForwarderOut, status_code=201)
async def create_forwarder(
    payload: DnsForwarderCreate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    fabric = await db.get(Fabric, payload.fabric_id)
    if fabric is None:
        raise ValidationError(f"fabric {payload.fabric_id} not found")
    if not payload.upstreams:
        raise ValidationError("at least one upstream is required")
    obj = DnsForwarder(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="dns_forwarder.create",
        target_type="dns_forwarder", target_id=str(obj.id),
        metadata={
            "name": payload.name,
            "zone_pattern": payload.zone_pattern,
            "upstreams": payload.upstreams,
        },
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/forwarders/{forwarder_id}", response_model=DnsForwarderOut)
async def update_forwarder(
    forwarder_id: UUID,
    payload: DnsForwarderUpdate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(DnsForwarder, forwarder_id)
    if obj is None:
        raise NotFoundError(_FORWARDER_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    if "upstreams" in diff and diff["upstreams"] is not None and not diff["upstreams"]:
        raise ValidationError("at least one upstream is required")
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="dns_forwarder.update",
        target_type="dns_forwarder", target_id=str(forwarder_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/forwarders/{forwarder_id}", status_code=204)
async def delete_forwarder(
    forwarder_id: UUID,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(DnsForwarder, forwarder_id)
    if obj is None:
        raise NotFoundError(_FORWARDER_NOT_FOUND)
    # Snapshot before deletion so the audit row stands on its own.
    snapshot = {
        "name": obj.name,
        "zone_pattern": obj.zone_pattern,
        "upstreams": list(obj.upstreams or []),
    }
    await db.execute(delete(DnsForwarder).where(DnsForwarder.id == forwarder_id))
    await audit.record(
        db, principal, action="dns_forwarder.delete",
        target_type="dns_forwarder", target_id=str(forwarder_id),
        metadata=snapshot,
    )
    await db.commit()


# ----------------------- BGP peers -----------------------
@router.get("/bgp-peers", response_model=Page[BgpPeerOut])
async def list_bgp_peers(
    params: PageParams = Depends(PageParams.from_query),
    site_id: UUID | None = Query(None),
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(BgpPeer)
    if site_id is not None:
        stmt = stmt.where(BgpPeer.site_id == site_id)
    return await paginate(db, stmt, model=BgpPeer, params=params, out_model=BgpPeerOut)


@router.post("/bgp-peers", response_model=BgpPeerOut, status_code=201)
async def create_bgp_peer(
    payload: BgpPeerCreate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    site = await db.get(Site, payload.site_id)
    if site is None:
        raise ValidationError(f"site {payload.site_id} not found")
    # Cross-check ASN catalog FKs at the call site so 404s land here
    # instead of as an opaque IntegrityError at commit.
    if await db.get(Asn, payload.local_asn_id) is None:
        raise NotFoundError(f"local ASN {payload.local_asn_id} not found")
    if await db.get(Asn, payload.peer_asn_id) is None:
        raise NotFoundError(f"peer ASN {payload.peer_asn_id} not found")
    if (
        payload.tcp_ao_key_chain_id is not None
        and await db.get(TcpAoKeyChain, payload.tcp_ao_key_chain_id) is None
    ):
        raise NotFoundError(f"tcp ao key chain {payload.tcp_ao_key_chain_id} not found")
    obj = BgpPeer(**payload.model_dump())
    db.add(obj)
    await db.flush()
    metadata = {"name": payload.name, "peer_ip": payload.peer_ip}
    await audit.record(
        db, principal, action="bgp_peer.create",
        target_type="bgp_peer", target_id=str(obj.id),
        site_id=obj.site_id, metadata=metadata,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/bgp-peers/{peer_id}", response_model=BgpPeerOut)
async def update_bgp_peer(
    peer_id: UUID,
    payload: BgpPeerUpdate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(BgpPeer, peer_id)
    if obj is None:
        raise NotFoundError(_BGP_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    if (
        "local_asn_id" in diff
        and diff["local_asn_id"] is not None
        and await db.get(Asn, diff["local_asn_id"]) is None
    ):
        raise NotFoundError(f"local ASN {diff['local_asn_id']} not found")
    if (
        "peer_asn_id" in diff
        and diff["peer_asn_id"] is not None
        and await db.get(Asn, diff["peer_asn_id"]) is None
    ):
        raise NotFoundError(f"peer ASN {diff['peer_asn_id']} not found")
    if (
        "tcp_ao_key_chain_id" in diff
        and diff["tcp_ao_key_chain_id"] is not None
        and await db.get(TcpAoKeyChain, diff["tcp_ao_key_chain_id"]) is None
    ):
        raise NotFoundError(f"tcp ao key chain {diff['tcp_ao_key_chain_id']} not found")
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="bgp_peer.update",
        target_type="bgp_peer", target_id=str(peer_id),
        site_id=obj.site_id, diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/bgp-peers/{peer_id}", status_code=204)
async def delete_bgp_peer(
    peer_id: UUID,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(BgpPeer, peer_id)
    if obj is None:
        raise NotFoundError(_BGP_NOT_FOUND)
    in_use = (
        await db.execute(
            select(AnycastBgpBinding.id).where(AnycastBgpBinding.bgp_peer_id == peer_id).limit(1)
        )
    ).scalar_one_or_none()
    if in_use is not None:
        raise ConflictError("bgp peer still bound to one or more DNS servers; unbind first")
    await db.execute(delete(BgpPeer).where(BgpPeer.id == peer_id))
    await audit.record(
        db, principal, action="bgp_peer.delete",
        target_type="bgp_peer", target_id=str(peer_id),
    )
    await db.commit()


# ----------------------- Anycast/BGP binding -----------------------
@router.get("/anycast-bindings", response_model=Page[AnycastBgpBindingOut])
async def list_bindings(
    params: PageParams = Depends(PageParams.from_query),
    dns_server_id: UUID | None = Query(None),
    bgp_peer_id: UUID | None = Query(None),
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(AnycastBgpBinding)
    if dns_server_id is not None:
        stmt = stmt.where(AnycastBgpBinding.dns_server_id == dns_server_id)
    if bgp_peer_id is not None:
        stmt = stmt.where(AnycastBgpBinding.bgp_peer_id == bgp_peer_id)
    return await paginate(
        db, stmt, model=AnycastBgpBinding, params=params, out_model=AnycastBgpBindingOut,
    )


@router.post("/anycast-bindings", response_model=AnycastBgpBindingOut, status_code=201)
async def create_binding(
    payload: AnycastBgpBindingCreate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    server = await db.get(DnsServer, payload.dns_server_id)
    peer = await db.get(BgpPeer, payload.bgp_peer_id)
    if server is None:
        raise ValidationError(f"dns server {payload.dns_server_id} not found")
    if peer is None:
        raise ValidationError(f"bgp peer {payload.bgp_peer_id} not found")
    if server.role != DnsServerRole.recursive:
        raise ValidationError("only recursive DNS servers advertise anycast IPs")
    if server.site_id != peer.site_id:
        raise ValidationError("dns server and bgp peer must live at the same site")
    existing = (
        await db.execute(
            select(AnycastBgpBinding).where(
                AnycastBgpBinding.dns_server_id == payload.dns_server_id,
                AnycastBgpBinding.bgp_peer_id == payload.bgp_peer_id,
            )
        )
    ).scalar_one_or_none()
    if existing is not None:
        raise ConflictError("dns server already advertises to this peer")
    obj = AnycastBgpBinding(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="anycast_bgp_binding.create",
        target_type="anycast_bgp_binding", target_id=str(obj.id),
        metadata={"dns_server_id": str(payload.dns_server_id), "bgp_peer_id": str(payload.bgp_peer_id)},
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/anycast-bindings/{binding_id}", status_code=204)
async def delete_binding(
    binding_id: UUID,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(AnycastBgpBinding, binding_id)
    if obj is None:
        raise NotFoundError(_BIND_NOT_FOUND)
    await db.execute(delete(AnycastBgpBinding).where(AnycastBgpBinding.id == binding_id))
    await audit.record(
        db, principal, action="anycast_bgp_binding.delete",
        target_type="anycast_bgp_binding", target_id=str(binding_id),
    )
    await db.commit()
