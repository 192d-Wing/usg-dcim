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

from datetime import UTC, datetime, timedelta
from uuid import UUID

from fastapi import APIRouter, Depends, Query
from pydantic import BaseModel, ValidationError as PydanticValidationError
from sqlalchemy import delete, func, select, update
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..errors import ConflictError, NotFoundError, ValidationError
from ..models.dns import (
    AnycastBgpBinding,
    AnycastGroup,
    BgpPeer,
    DnsBlocklist,
    DnsBlocklistEntry,
    DnsForwarder,
    DnsHealthCheck,
    DnsKey,
    DnsKeyRole,
    DnsRecord,
    DnsRecordSource,
    DnsRecordType,
    DnsServer,
    DnsServerMetricsSample,
    DnsServerRole,
    DnsView,
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
    DnsBlocklistCreate,
    DnsBlocklistEntryBulk,
    DnsBlocklistEntryCreate,
    DnsBlocklistEntryOut,
    DnsBlocklistOut,
    DnsBlocklistUpdate,
    DnsBundle,
    DnsForwarderCreate,
    DnsForwarderOut,
    DnsForwarderUpdate,
    DnsDsRecordOut,
    DnsHealthCheckCreate,
    DnsHealthCheckOut,
    DnsHealthCheckResult,
    DnsHealthCheckUpdate,
    DnsKeyOut,
    DnsMetricsSampleIn,
    DnsMetricsSampleOut,
    DnsRecordCreate,
    DnsRecordOut,
    DnsRecordUpdate,
    DnsRenderStatus,
    DnsViewCreate,
    DnsViewOut,
    DnsViewUpdate,
    DnsServerCreate,
    DnsServerOut,
    DnsServerUpdate,
    DnsZoneCreate,
    DnsZoneNsec3Params,
    DnsZoneOut,
    DnsZoneUpdate,
    validate_record_data,
)
from ..security import audit

from ..security.deps import Principal, require_capability
from ..services import dns as dns_svc
from ..settings import get_settings
from ._pagination import paginate

router = APIRouter(prefix="/dns", tags=["dns"])

_ZONE_NOT_FOUND = "dns zone not found"
_RECORD_NOT_FOUND = "dns record not found"
_SERVER_NOT_FOUND = "dns server not found"
_ANYCAST_NOT_FOUND = "anycast group not found"
_BGP_NOT_FOUND = "bgp peer not found"
_BIND_NOT_FOUND = "anycast/bgp binding not found"
_FORWARDER_NOT_FOUND = "dns forwarder not found"
_BLOCKLIST_NOT_FOUND = "dns blocklist not found"
_BLOCKLIST_ENTRY_NOT_FOUND = "dns blocklist entry not found"
_VIEW_NOT_FOUND = "dns view not found"
_HC_NOT_FOUND = "dns health check not found"


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
    kind: str | None = Query(None, regex="^(apex|site|reverse)$"),
    _: Principal = Depends(require_capability("dns:zones:read")),
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
    principal: Principal = Depends(require_capability("dns:zones:create")),
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
    _: Principal = Depends(require_capability("dns:zones:read")),
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
    principal: Principal = Depends(require_capability("dns:zones:update")),
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
    principal: Principal = Depends(require_capability("dns:zones:delete")),
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
    principal: Principal = Depends(require_capability("dns:zones:update")),
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
    _: Principal = Depends(require_capability("dns:zones:read")),
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


class _ImportPayload(BaseModel):
    text: str
    # Dry-run returns the parsed view without committing. Useful for
    # the UI's import preview / diff.
    dry_run: bool = False
    # When true, also adopt SOA timers from the imported file. Off by
    # default so a paste from a public zone doesn't quietly overwrite
    # our 60s defaults.
    update_soa: bool = False


@router.post("/zones/{zone_id}/import")
async def import_zone_records(
    zone_id: UUID,
    payload: _ImportPayload,
    principal: Principal = Depends(require_capability("dns:zones:update")),
    db: AsyncSession = Depends(get_db),
) -> dict:
    """Parse a BIND-format zone file and bulk-insert its records into
    the target zone, replacing existing `source=manual` rows.

    IPAM-projected rows are left alone — they're owned by the sync job.
    DNSSEC types (DNSKEY/RRSIG/NSEC*) and unsupported directives
    ($INCLUDE, $GENERATE) become warnings rather than errors so the
    operator can still see what didn't import."""
    zone = await db.get(DnsZone, zone_id)
    if zone is None:
        raise NotFoundError(_ZONE_NOT_FOUND)
    try:
        parsed = dns_svc.parse_bind_zone(payload.text, default_zone=zone.name + ".")
    except dns_svc.BindImportError as e:
        raise ValidationError(str(e)) from None

    if payload.dry_run:
        return {
            "zone_id": str(zone_id),
            "would_add": len(parsed["records"]),
            "would_replace_manual": True,
            "warnings": parsed["warnings"],
            "parsed": parsed,
        }

    # Replace existing manual rows; preserve IPAM-projected ones.
    existing = (
        await db.execute(
            select(DnsRecord).where(
                DnsRecord.zone_id == zone_id,
                DnsRecord.source == DnsRecordSource.manual,
            )
        )
    ).scalars().all()
    removed = len(existing)
    for r in existing:
        await db.delete(r)
    await db.flush()

    added = 0
    for r in parsed["records"]:
        try:
            rtype = DnsRecordType(r["type"])
        except ValueError:
            parsed["warnings"].append(f"unknown record type {r['type']} skipped")
            continue
        db.add(DnsRecord(
            zone_id=zone_id, name=r["name"], type=rtype,
            ttl=r["ttl"], data=r["data"],
            source=DnsRecordSource.manual,
        ))
        added += 1

    if payload.update_soa:
        soa = parsed["soa"]
        zone.soa_mname = soa["mname"].rstrip(".").split(".", 1)[0] or zone.soa_mname
        zone.soa_rname = soa["rname"].rstrip(".").split(".", 1)[0] or zone.soa_rname
        zone.soa_refresh = soa["refresh"]
        zone.soa_retry = soa["retry"]
        zone.soa_expire = soa["expire"]
        zone.soa_minimum = soa["minimum"]
        zone.default_ttl = parsed.get("default_ttl") or zone.default_ttl

    await _touch_zone(db, zone_id)
    await audit.record(
        db, principal, action="dns_zone.import_bind",
        target_type="dns_zone", target_id=str(zone_id),
        site_id=zone.site_id,
        metadata={
            "added": added,
            "removed_manual": removed,
            "update_soa": payload.update_soa,
            "warnings": parsed["warnings"],
        },
    )
    await db.commit()
    return {
        "zone_id": str(zone_id),
        "added": added,
        "removed_manual": removed,
        "warnings": parsed["warnings"],
    }


# ----------------------- DNSSEC -----------------------
@router.post("/zones/{zone_id}/enable-dnssec", response_model=list[DnsKeyOut])
async def enable_dnssec(
    zone_id: UUID,
    principal: Principal = Depends(require_capability("dns:keys:rotate")),
    db: AsyncSession = Depends(get_db),
):
    """Generate a KSK + ZSK for the zone, flip `signed=true`, and
    return the new key roster. Idempotent: if keys already exist, the
    response just lists them — operators rotate via a separate
    endpoint (deferred)."""
    zone = await db.get(DnsZone, zone_id)
    if zone is None:
        raise NotFoundError(_ZONE_NOT_FOUND)
    existing = list((
        await db.execute(select(DnsKey).where(DnsKey.zone_id == zone_id))
    ).scalars().all())
    if existing:
        if not zone.signed:
            zone.signed = True
            await db.commit()
        return existing
    now = datetime.now(UTC)
    default_alg = dns_svc.DnsKeyAlgorithm(
        get_settings().dns_dnssec_default_algorithm,
    )
    keys: list[DnsKey] = []
    for role in (DnsKeyRole.ksk, DnsKeyRole.zsk):
        material = dns_svc.generate_dnssec_keypair(
            zone.name, role, algorithm=default_alg,
        )
        keys.append(DnsKey(
            zone_id=zone_id,
            role=material["role"],
            algorithm=material["algorithm"],
            private_pem=material["private_pem"],
            public_key_b64=material["public_key_b64"],
            key_tag=material["key_tag"],
            active_from=now,
        ))
    for k in keys:
        db.add(k)
    zone.signed = True
    await db.flush()
    await audit.record(
        db, principal, action="dns_zone.enable_dnssec",
        target_type="dns_zone", target_id=str(zone_id),
        metadata={
            "ksk_tag": next(k.key_tag for k in keys if k.role == DnsKeyRole.ksk),
            "zsk_tag": next(k.key_tag for k in keys if k.role == DnsKeyRole.zsk),
        },
    )
    await db.commit()
    for k in keys:
        await db.refresh(k)
    return keys


@router.get("/zones/{zone_id}/keys", response_model=list[DnsKeyOut])
async def list_zone_keys(
    zone_id: UUID,
    _: Principal = Depends(require_capability("dns:keys:read")),
    db: AsyncSession = Depends(get_db),
):
    if (await db.get(DnsZone, zone_id)) is None:
        raise NotFoundError(_ZONE_NOT_FOUND)
    rows = (
        await db.execute(
            select(DnsKey).where(DnsKey.zone_id == zone_id)
            .order_by(DnsKey.role.asc(), DnsKey.active_from.desc()),
        )
    ).scalars().all()
    return rows


@router.get("/zones/{zone_id}/ds-records", response_model=list[DnsDsRecordOut])
async def list_ds_records(
    zone_id: UUID,
    _: Principal = Depends(require_capability("dns:keys:read")),
    db: AsyncSession = Depends(get_db),
):
    """DS records for active KSKs — the operator uploads these to the
    parent zone's operator to chain the trust anchor."""
    zone = await db.get(DnsZone, zone_id)
    if zone is None:
        raise NotFoundError(_ZONE_NOT_FOUND)
    keys = (
        await db.execute(select(DnsKey).where(DnsKey.zone_id == zone_id))
    ).scalars().all()
    return dns_svc.render_ds_records(zone, keys)


@router.post("/zones/{zone_id}/nsec3", response_model=DnsZoneOut)
async def set_zone_nsec3(
    zone_id: UUID,
    params: DnsZoneNsec3Params,
    principal: Principal = Depends(require_capability("dns:zones:update")),
    db: AsyncSession = Depends(get_db),
):
    """Set NSEC3 parameters on a signed zone. Flipping a zone into
    NSEC3 mode tells the renderer to emit the `nsec3sign` plugin
    block instead of the upstream `dnssec` block — the deployed
    auth pod has to be running the custom `coredns-nsec3sign` image
    for that block to load.

    Idempotent: re-posting with the same params is a no-op.
    Operators clear NSEC3 (back to NSEC mode) via DELETE on the
    same path.

    A zone must be signed first — NSEC3 parameters on an unsigned
    zone have nothing to chain off of. Trying to set them anyway
    returns 422 so the UI can prompt the operator to enable DNSSEC
    first."""
    zone = await db.get(DnsZone, zone_id)
    if zone is None:
        raise NotFoundError(_ZONE_NOT_FOUND)
    if not zone.signed:
        raise ValidationError("zone is not signed — enable DNSSEC first")
    zone.nsec3_salt = params.salt
    zone.nsec3_iterations = params.iterations
    zone.nsec3_opt_out = params.opt_out
    await _touch_zone(db, zone_id)
    await audit.record(
        db, principal, action="dns_zone.set_nsec3",
        target_type="dns_zone", target_id=str(zone_id),
        metadata={
            "salt": params.salt,
            "iterations": params.iterations,
            "opt_out": params.opt_out,
        },
    )
    await db.commit()
    await db.refresh(zone)
    return zone


@router.delete("/zones/{zone_id}/nsec3", response_model=DnsZoneOut)
async def clear_zone_nsec3(
    zone_id: UUID,
    principal: Principal = Depends(require_capability("dns:zones:update")),
    db: AsyncSession = Depends(get_db),
):
    """Clear NSEC3 parameters on a zone, reverting it to NSEC mode
    (upstream `dnssec` plugin). Safe on a zone that was never in
    NSEC3 mode — returns the unchanged zone. The signed flag and
    keys are left in place; this only touches the denial-of-existence
    profile."""
    zone = await db.get(DnsZone, zone_id)
    if zone is None:
        raise NotFoundError(_ZONE_NOT_FOUND)
    if zone.nsec3_salt is None and zone.nsec3_iterations == 0 and not zone.nsec3_opt_out:
        return zone
    zone.nsec3_salt = None
    zone.nsec3_iterations = 0
    zone.nsec3_opt_out = False
    await _touch_zone(db, zone_id)
    await audit.record(
        db, principal, action="dns_zone.clear_nsec3",
        target_type="dns_zone", target_id=str(zone_id),
    )
    await db.commit()
    await db.refresh(zone)
    return zone


@router.post("/zones/{zone_id}/disable-dnssec", status_code=204)
async def disable_dnssec(
    zone_id: UUID,
    principal: Principal = Depends(require_capability("dns:keys:rotate")),
    db: AsyncSession = Depends(get_db),
):
    """Unsign a zone: delete every DnsKey for it and clear the signed
    flag. Reversible — calling /enable-dnssec again regenerates fresh
    keys, but operators should withdraw the DS from the parent first
    so cached validators don't bog-down on an unsignable RRset."""
    zone = await db.get(DnsZone, zone_id)
    if zone is None:
        raise NotFoundError(_ZONE_NOT_FOUND)
    if not zone.signed:
        return
    keys = list((
        await db.execute(select(DnsKey).where(DnsKey.zone_id == zone_id))
    ).scalars().all())
    retired_tags = [k.key_tag for k in keys]
    await db.execute(delete(DnsKey).where(DnsKey.zone_id == zone_id))
    zone.signed = False
    await _touch_zone(db, zone_id)
    await audit.record(
        db, principal, action="dns_zone.disable_dnssec",
        target_type="dns_zone", target_id=str(zone_id),
        metadata={"retired_key_tags": retired_tags},
    )
    await db.commit()


@router.post(
    "/zones/{zone_id}/rotate-key/{role}",
    response_model=list[DnsKeyOut],
)
async def rotate_zone_key(
    zone_id: UUID,
    role: DnsKeyRole,
    principal: Principal = Depends(require_capability("dns:keys:rotate")),
    db: AsyncSession = Depends(get_db),
):
    """Generate a fresh KSK or ZSK and mark the existing active key of
    that role as retired (retired_at = NOW()). Retired keys stay in
    the table so the renderer can keep them in the DNSKEY rrset
    through the cache-expiry grace window; operators delete them via
    DELETE /dns/keys/{key_id} once they're sure no validator is still
    cached.

    KSK rotation requires the operator to upload the new DS record to
    the parent zone's operator before the old DS can be retired —
    the UI surfaces a reminder on this path. Returns the full key
    roster (active + retired) so the caller can re-render the panel."""
    zone = await db.get(DnsZone, zone_id)
    if zone is None:
        raise NotFoundError(_ZONE_NOT_FOUND)
    if not zone.signed:
        raise ValidationError("zone is not signed — enable DNSSEC first")
    new_key, retired = await dns_svc.rotate_zone_key(db, zone, role)
    await audit.record(
        db, principal, action=f"dns_zone.rotate_{role.value}",
        target_type="dns_zone", target_id=str(zone_id),
        metadata={
            "new_key_tag": new_key.key_tag,
            "retired_key_tags": [k.key_tag for k in retired],
        },
    )
    await db.commit()
    rows = (
        await db.execute(
            select(DnsKey).where(DnsKey.zone_id == zone_id)
            .order_by(DnsKey.role.asc(), DnsKey.active_from.desc()),
        )
    ).scalars().all()
    return rows


@router.delete("/keys/{key_id}", status_code=204)
async def delete_dns_key(
    key_id: UUID,
    principal: Principal = Depends(require_capability("dns:keys:delete")),
    db: AsyncSession = Depends(get_db),
):
    """Purge a retired key. Refuses to delete an active key — operators
    rotate first so a successor exists before removal."""
    obj = await db.get(DnsKey, key_id)
    if obj is None:
        raise NotFoundError("dns key not found")
    if obj.retired_at is None:
        raise ValidationError(
            "active key can't be deleted; rotate it first so the new "
            "key takes over",
        )
    snapshot = {
        "zone_id": str(obj.zone_id),
        "role": obj.role.value,
        "key_tag": obj.key_tag,
    }
    await db.execute(delete(DnsKey).where(DnsKey.id == key_id))
    await audit.record(
        db, principal, action="dns_key.delete",
        target_type="dns_zone", target_id=str(obj.zone_id),
        metadata=snapshot,
    )
    await db.commit()


# ----------------------- Records -----------------------
@router.get("/records", response_model=Page[DnsRecordOut])
async def list_records(
    params: PageParams = Depends(PageParams.from_query),
    zone_id: UUID | None = Query(None),
    type_: str | None = Query(None, alias="type"),
    source: str | None = Query(None, regex="^(ipam|manual)$"),
    _: Principal = Depends(require_capability("dns:records:read")),
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
    principal: Principal = Depends(require_capability("dns:records:create")),
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
        view_id=payload.view_id,
        health_check_id=payload.health_check_id,
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
    principal: Principal = Depends(require_capability("dns:records:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(DnsRecord, record_id)
    if obj is None:
        raise NotFoundError(_RECORD_NOT_FOUND)
    if obj.source != DnsRecordSource.manual:
        raise ValidationError(
            "projector-owned records are managed by the IPAM/DHCP sync; "
            "edit the underlying IPAddress instead",
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
    principal: Principal = Depends(require_capability("dns:records:delete")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(DnsRecord, record_id)
    if obj is None:
        raise NotFoundError(_RECORD_NOT_FOUND)
    if obj.source != DnsRecordSource.manual:
        raise ValidationError(
            "projector-owned records can't be deleted directly; "
            "clear the dns_name on the IPAddress (or release the lease) "
            "and re-sync",
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
    _: Principal = Depends(require_capability("dns:servers:read")),
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
    principal: Principal = Depends(require_capability("dns:servers:create")),
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
    _: Principal = Depends(require_capability("dns:servers:read")),
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
    principal: Principal = Depends(require_capability("dns:servers:update")),
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
    principal: Principal = Depends(require_capability("dns:servers:delete")),
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
    _: Principal = Depends(require_capability("dns:servers:bundle")),
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
    _: Principal = Depends(require_capability("dns:servers:update")),
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


@router.post(
    "/servers/{server_id}/metrics",
    response_model=DnsMetricsSampleOut, status_code=201,
)
async def post_server_metrics(
    server_id: UUID,
    payload: DnsMetricsSampleIn,
    _: Principal = Depends(require_capability("dns:servers:update")),
    db: AsyncSession = Depends(get_db),
):
    """Collector posts one sample (interval delta) per scrape. Skip
    audit on this path — high-volume cron telemetry isn't a meaningful
    audit event."""
    server = await db.get(DnsServer, server_id)
    if server is None:
        raise NotFoundError(_SERVER_NOT_FOUND)
    obj = DnsServerMetricsSample(
        server_id=server_id,
        observed_at=payload.observed_at or datetime.now(UTC),
        interval_seconds=payload.interval_seconds,
        queries=payload.queries,
        nxdomain=payload.nxdomain,
        servfail=payload.servfail,
        noerror=payload.noerror,
        p50_ms=payload.p50_ms,
        p95_ms=payload.p95_ms,
    )
    db.add(obj)
    await db.commit()
    await db.refresh(obj)
    return obj


@router.get(
    "/servers/{server_id}/metrics",
    response_model=list[DnsMetricsSampleOut],
)
async def list_server_metrics(
    server_id: UUID,
    minutes: int = Query(60, ge=1, le=24 * 60),
    _: Principal = Depends(require_capability("dns:servers:read")),
    db: AsyncSession = Depends(get_db),
):
    """Recent metrics samples for one server, oldest-first so the UI
    can chart them directly. Window defaults to one hour."""
    if (await db.get(DnsServer, server_id)) is None:
        raise NotFoundError(_SERVER_NOT_FOUND)
    cutoff = datetime.now(UTC) - timedelta(minutes=minutes)
    rows = (
        await db.execute(
            select(DnsServerMetricsSample)
            .where(
                DnsServerMetricsSample.server_id == server_id,
                DnsServerMetricsSample.observed_at >= cutoff,
            )
            .order_by(DnsServerMetricsSample.observed_at.asc())
        )
    ).scalars().all()
    return rows


# ----------------------- Anycast groups -----------------------
@router.get("/anycast-groups", response_model=Page[AnycastGroupOut])
async def list_anycast_groups(
    params: PageParams = Depends(PageParams.from_query),
    fabric_id: UUID | None = Query(None),
    _: Principal = Depends(require_capability("dns:anycast-groups:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(AnycastGroup)
    if fabric_id is not None:
        stmt = stmt.where(AnycastGroup.fabric_id == fabric_id)
    return await paginate(db, stmt, model=AnycastGroup, params=params, out_model=AnycastGroupOut)


@router.post("/anycast-groups", response_model=AnycastGroupOut, status_code=201)
async def create_anycast_group(
    payload: AnycastGroupCreate,
    principal: Principal = Depends(require_capability("dns:anycast-groups:create")),
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
    principal: Principal = Depends(require_capability("dns:anycast-groups:update")),
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
    principal: Principal = Depends(require_capability("dns:anycast-groups:delete")),
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
    _: Principal = Depends(require_capability("dns:forwarders:read")),
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
    principal: Principal = Depends(require_capability("dns:forwarders:create")),
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
    principal: Principal = Depends(require_capability("dns:forwarders:update")),
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
    principal: Principal = Depends(require_capability("dns:forwarders:delete")),
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


# ----------------------- Blocklists -----------------------
@router.get("/blocklists", response_model=Page[DnsBlocklistOut])
async def list_blocklists(
    params: PageParams = Depends(PageParams.from_query),
    fabric_id: UUID | None = Query(None),
    _: Principal = Depends(require_capability("dns:blocklists:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(DnsBlocklist)
    if fabric_id is not None:
        stmt = stmt.where(DnsBlocklist.fabric_id == fabric_id)
    return await paginate(
        db, stmt, model=DnsBlocklist, params=params, out_model=DnsBlocklistOut,
    )


@router.post("/blocklists", response_model=DnsBlocklistOut, status_code=201)
async def create_blocklist(
    payload: DnsBlocklistCreate,
    principal: Principal = Depends(require_capability("dns:blocklists:create")),
    db: AsyncSession = Depends(get_db),
):
    fabric = await db.get(Fabric, payload.fabric_id)
    if fabric is None:
        raise ValidationError(f"fabric {payload.fabric_id} not found")
    if payload.action.value == "sinkhole" and payload.sink_ipv4 is None and payload.sink_ipv6 is None:
        raise ValidationError("sinkhole blocklist needs at least one sink IP")
    obj = DnsBlocklist(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="dns_blocklist.create",
        target_type="dns_blocklist", target_id=str(obj.id),
        metadata={"name": payload.name, "action": payload.action.value},
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/blocklists/{blocklist_id}", response_model=DnsBlocklistOut)
async def update_blocklist(
    blocklist_id: UUID,
    payload: DnsBlocklistUpdate,
    principal: Principal = Depends(require_capability("dns:blocklists:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(DnsBlocklist, blocklist_id)
    if obj is None:
        raise NotFoundError(_BLOCKLIST_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="dns_blocklist.update",
        target_type="dns_blocklist", target_id=str(blocklist_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/blocklists/{blocklist_id}", status_code=204)
async def delete_blocklist(
    blocklist_id: UUID,
    principal: Principal = Depends(require_capability("dns:blocklists:delete")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(DnsBlocklist, blocklist_id)
    if obj is None:
        raise NotFoundError(_BLOCKLIST_NOT_FOUND)
    snapshot = {"name": obj.name, "action": obj.action.value}
    # ON DELETE CASCADE on dns_blocklist_entries cleans up children.
    await db.execute(delete(DnsBlocklist).where(DnsBlocklist.id == blocklist_id))
    await audit.record(
        db, principal, action="dns_blocklist.delete",
        target_type="dns_blocklist", target_id=str(blocklist_id),
        metadata=snapshot,
    )
    await db.commit()


@router.get("/blocklists/{blocklist_id}/entries", response_model=Page[DnsBlocklistEntryOut])
async def list_blocklist_entries(
    blocklist_id: UUID,
    params: PageParams = Depends(PageParams.from_query),
    _: Principal = Depends(require_capability("dns:blocklists:read")),
    db: AsyncSession = Depends(get_db),
):
    if (await db.get(DnsBlocklist, blocklist_id)) is None:
        raise NotFoundError(_BLOCKLIST_NOT_FOUND)
    stmt = select(DnsBlocklistEntry).where(DnsBlocklistEntry.blocklist_id == blocklist_id)
    return await paginate(
        db, stmt, model=DnsBlocklistEntry,
        params=params, out_model=DnsBlocklistEntryOut,
    )


@router.post(
    "/blocklists/{blocklist_id}/entries",
    response_model=DnsBlocklistEntryOut, status_code=201,
)
async def create_blocklist_entry(
    blocklist_id: UUID,
    payload: DnsBlocklistEntryCreate,
    principal: Principal = Depends(require_capability("dns:blocklists:update")),
    db: AsyncSession = Depends(get_db),
):
    if (await db.get(DnsBlocklist, blocklist_id)) is None:
        raise NotFoundError(_BLOCKLIST_NOT_FOUND)
    obj = DnsBlocklistEntry(blocklist_id=blocklist_id, **payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="dns_blocklist_entry.create",
        target_type="dns_blocklist", target_id=str(blocklist_id),
        metadata={"pattern": payload.pattern},
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.post("/blocklists/{blocklist_id}/entries/bulk")
async def bulk_add_blocklist_entries(
    blocklist_id: UUID,
    payload: DnsBlocklistEntryBulk,
    principal: Principal = Depends(require_capability("dns:blocklists:update")),
    db: AsyncSession = Depends(get_db),
) -> dict:
    """Idempotent bulk insert for threat-feed style imports. Existing
    (blocklist, pattern) pairs are silently skipped via the unique
    constraint; the response reports the net adds."""
    if (await db.get(DnsBlocklist, blocklist_id)) is None:
        raise NotFoundError(_BLOCKLIST_NOT_FOUND)
    # Dedup within the payload first so the audit count is honest.
    incoming = {p.strip().lower() for p in payload.patterns if p.strip()}
    if not incoming:
        return {"added": 0, "skipped": 0}
    existing = (
        await db.execute(
            select(DnsBlocklistEntry.pattern)
            .where(DnsBlocklistEntry.blocklist_id == blocklist_id)
        )
    ).scalars().all()
    existing_set = set(existing)
    to_add = sorted(incoming - existing_set)
    for pat in to_add:
        db.add(DnsBlocklistEntry(blocklist_id=blocklist_id, pattern=pat))
    await db.flush()
    await audit.record(
        db, principal, action="dns_blocklist_entry.bulk_add",
        target_type="dns_blocklist", target_id=str(blocklist_id),
        metadata={"added": len(to_add), "skipped": len(incoming) - len(to_add)},
    )
    await db.commit()
    return {"added": len(to_add), "skipped": len(incoming) - len(to_add)}


@router.delete("/blocklists/{blocklist_id}/entries/{entry_id}", status_code=204)
async def delete_blocklist_entry(
    blocklist_id: UUID,
    entry_id: UUID,
    principal: Principal = Depends(require_capability("dns:blocklists:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(DnsBlocklistEntry, entry_id)
    if obj is None or obj.blocklist_id != blocklist_id:
        raise NotFoundError(_BLOCKLIST_ENTRY_NOT_FOUND)
    snapshot = {"pattern": obj.pattern}
    await db.execute(delete(DnsBlocklistEntry).where(DnsBlocklistEntry.id == entry_id))
    await audit.record(
        db, principal, action="dns_blocklist_entry.delete",
        target_type="dns_blocklist", target_id=str(blocklist_id),
        metadata=snapshot,
    )
    await db.commit()


# ----------------------- Views (split-horizon) -----------------------
@router.get("/views", response_model=Page[DnsViewOut])
async def list_views(
    params: PageParams = Depends(PageParams.from_query),
    fabric_id: UUID | None = Query(None),
    _: Principal = Depends(require_capability("dns:views:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(DnsView)
    if fabric_id is not None:
        stmt = stmt.where(DnsView.fabric_id == fabric_id)
    return await paginate(
        db, stmt, model=DnsView, params=params, out_model=DnsViewOut,
    )


@router.post("/views", response_model=DnsViewOut, status_code=201)
async def create_view(
    payload: DnsViewCreate,
    principal: Principal = Depends(require_capability("dns:views:create")),
    db: AsyncSession = Depends(get_db),
):
    fabric = await db.get(Fabric, payload.fabric_id)
    if fabric is None:
        raise ValidationError(f"fabric {payload.fabric_id} not found")
    obj = DnsView(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="dns_view.create",
        target_type="dns_view", target_id=str(obj.id),
        metadata={"name": payload.name, "match_cidrs": payload.match_cidrs},
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/views/{view_id}", response_model=DnsViewOut)
async def update_view(
    view_id: UUID,
    payload: DnsViewUpdate,
    principal: Principal = Depends(require_capability("dns:views:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(DnsView, view_id)
    if obj is None:
        raise NotFoundError(_VIEW_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="dns_view.update",
        target_type="dns_view", target_id=str(view_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/views/{view_id}", status_code=204)
async def delete_view(
    view_id: UUID,
    principal: Principal = Depends(require_capability("dns:views:delete")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(DnsView, view_id)
    if obj is None:
        raise NotFoundError(_VIEW_NOT_FOUND)
    # ON DELETE SET NULL on dns_records.view_id keeps records around
    # but un-scopes them — they revert to the default-view answer.
    snapshot = {"name": obj.name, "match_cidrs": list(obj.match_cidrs or [])}
    await db.execute(delete(DnsView).where(DnsView.id == view_id))
    await audit.record(
        db, principal, action="dns_view.delete",
        target_type="dns_view", target_id=str(view_id),
        metadata=snapshot,
    )
    await db.commit()


# ----------------------- Health checks -----------------------
@router.get("/health-checks", response_model=Page[DnsHealthCheckOut])
async def list_health_checks(
    params: PageParams = Depends(PageParams.from_query),
    fabric_id: UUID | None = Query(None),
    _: Principal = Depends(require_capability("dns:health-checks:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(DnsHealthCheck)
    if fabric_id is not None:
        stmt = stmt.where(DnsHealthCheck.fabric_id == fabric_id)
    return await paginate(
        db, stmt, model=DnsHealthCheck, params=params, out_model=DnsHealthCheckOut,
    )


@router.post("/health-checks", response_model=DnsHealthCheckOut, status_code=201)
async def create_health_check(
    payload: DnsHealthCheckCreate,
    principal: Principal = Depends(require_capability("dns:health-checks:create")),
    db: AsyncSession = Depends(get_db),
):
    fabric = await db.get(Fabric, payload.fabric_id)
    if fabric is None:
        raise ValidationError(f"fabric {payload.fabric_id} not found")
    obj = DnsHealthCheck(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="dns_health_check.create",
        target_type="dns_health_check", target_id=str(obj.id),
        metadata={
            "name": payload.name,
            "target_ip": payload.target_ip,
            "protocol": payload.protocol.value,
        },
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/health-checks/{check_id}", response_model=DnsHealthCheckOut)
async def update_health_check(
    check_id: UUID,
    payload: DnsHealthCheckUpdate,
    principal: Principal = Depends(require_capability("dns:health-checks:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(DnsHealthCheck, check_id)
    if obj is None:
        raise NotFoundError(_HC_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="dns_health_check.update",
        target_type="dns_health_check", target_id=str(check_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/health-checks/{check_id}", status_code=204)
async def delete_health_check(
    check_id: UUID,
    principal: Principal = Depends(require_capability("dns:health-checks:delete")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(DnsHealthCheck, check_id)
    if obj is None:
        raise NotFoundError(_HC_NOT_FOUND)
    # ON DELETE SET NULL on dns_records.health_check_id keeps records
    # but stops gating them.
    await db.execute(delete(DnsHealthCheck).where(DnsHealthCheck.id == check_id))
    await audit.record(
        db, principal, action="dns_health_check.delete",
        target_type="dns_health_check", target_id=str(check_id),
        metadata={"name": obj.name, "target_ip": str(obj.target_ip)},
    )
    await db.commit()


@router.post("/health-checks/{check_id}/result", status_code=204)
async def post_health_check_result(
    check_id: UUID,
    payload: DnsHealthCheckResult,
    _: Principal = Depends(require_capability("dns:health-checks:update")),
    db: AsyncSession = Depends(get_db),
):
    """Collector callback after running one probe. Skip audit on this
    path — every 30s probe shouldn't generate an audit row; the
    central worker also writes this column on its fallback cycles.

    last_checked_at advances on every callback so the worker can tell
    "already probed recently" from "stale" and skip the redundant
    central probe in the next cron tick."""
    obj = await db.get(DnsHealthCheck, check_id)
    if obj is None:
        raise NotFoundError(_HC_NOT_FOUND)
    obj.status = payload.status
    obj.last_checked_at = datetime.now(UTC)
    obj.last_error = payload.error
    await db.commit()


# ----------------------- BGP peers -----------------------
@router.get("/bgp-peers", response_model=Page[BgpPeerOut])
async def list_bgp_peers(
    params: PageParams = Depends(PageParams.from_query),
    site_id: UUID | None = Query(None),
    _: Principal = Depends(require_capability("dns:bgp-peers:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(BgpPeer)
    if site_id is not None:
        stmt = stmt.where(BgpPeer.site_id == site_id)
    return await paginate(db, stmt, model=BgpPeer, params=params, out_model=BgpPeerOut)


@router.post("/bgp-peers", response_model=BgpPeerOut, status_code=201)
async def create_bgp_peer(
    payload: BgpPeerCreate,
    principal: Principal = Depends(require_capability("dns:bgp-peers:create")),
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
    principal: Principal = Depends(require_capability("dns:bgp-peers:update")),
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
    principal: Principal = Depends(require_capability("dns:bgp-peers:delete")),
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
    _: Principal = Depends(require_capability("dns:anycast-bindings:read")),
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
    principal: Principal = Depends(require_capability("dns:anycast-bindings:create")),
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
    principal: Principal = Depends(require_capability("dns:anycast-bindings:delete")),
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
