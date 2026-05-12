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
from sqlalchemy import delete, func, select, text, update
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
from ..security.scope import (
    enforce_fabric_scope,
    enforce_site_scope,
    scope_filtered_fabric_ids,
    scope_filtered_site_ids,
)
from ..services import dns as dns_svc
from ..settings import get_settings
from ._pagination import paginate

router = APIRouter(prefix="/dns", tags=["dns"])

# Capability codes referenced from multiple endpoints. Centralized so a
# rename only happens in one place (and the Sonar duplicate-literal
# check stops complaining about the popular ones).
_CAP_SERVERS_READ = "dns:servers:read"
_CAP_ZONES_READ = "dns:zones:read"
_CAP_ZONES_UPDATE = "dns:zones:update"
_CAP_KEYS_READ = "dns:keys:read"
_CAP_KEYS_ROTATE = "dns:keys:rotate"

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
    principal: Principal = Depends(require_capability("dns:zones:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(DnsZone)
    if fabric_id is not None:
        stmt = stmt.where(DnsZone.fabric_id == fabric_id)
    if site_id is not None:
        stmt = stmt.where(DnsZone.site_id == site_id)
    if kind is not None:
        stmt = stmt.where(DnsZone.kind == kind)
    in_scope = await scope_filtered_fabric_ids(
        db, principal.capabilities, "dns:zones:read",
    )
    if in_scope is not None:
        if not in_scope:
            return Page[DnsZoneOut](items=[], total=0, page=params.page, page_size=params.page_size, has_more=False)
        stmt = stmt.where(DnsZone.fabric_id.in_(in_scope))
    return await paginate(db, stmt, model=DnsZone, params=params, out_model=DnsZoneOut)


@router.post("/zones", response_model=DnsZoneOut, status_code=201)
async def create_zone(
    payload: DnsZoneCreate,
    principal: Principal = Depends(require_capability("dns:zones:create")),
    db: AsyncSession = Depends(get_db),
):
    await enforce_fabric_scope(
        db, principal.capabilities, payload.fabric_id, "dns:zones:create",
    )
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
    principal: Principal = Depends(require_capability("dns:zones:read")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(DnsZone, zone_id)
    if obj is None:
        raise NotFoundError(_ZONE_NOT_FOUND)
    await enforce_fabric_scope(db, principal.capabilities, obj.fabric_id, "dns:zones:read")
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
    await enforce_fabric_scope(db, principal.capabilities, obj.fabric_id, "dns:zones:update")
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
    await enforce_fabric_scope(db, principal.capabilities, obj.fabric_id, "dns:zones:delete")
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
    await enforce_fabric_scope(
        db, principal.capabilities, zone.fabric_id, _CAP_ZONES_UPDATE,
    )
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
    principal: Principal = Depends(require_capability("dns:zones:read")),
    db: AsyncSession = Depends(get_db),
) -> dict:
    """Render the zone as a BIND-format text blob, useful for the UI's
    "preview" button so operators can see exactly what the collector
    will write to disk."""
    zone = await db.get(DnsZone, zone_id)
    if zone is None:
        raise NotFoundError(_ZONE_NOT_FOUND)
    await enforce_fabric_scope(
        db, principal.capabilities, zone.fabric_id, _CAP_ZONES_READ,
    )
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
    await enforce_fabric_scope(
        db, principal.capabilities, zone.fabric_id, _CAP_ZONES_UPDATE,
    )
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
    await enforce_fabric_scope(
        db, principal.capabilities, zone.fabric_id, _CAP_KEYS_ROTATE,
    )
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
    principal: Principal = Depends(require_capability(_CAP_KEYS_READ)),
    db: AsyncSession = Depends(get_db),
):
    zone = await db.get(DnsZone, zone_id)
    if zone is None:
        raise NotFoundError(_ZONE_NOT_FOUND)
    await enforce_fabric_scope(
        db, principal.capabilities, zone.fabric_id, _CAP_KEYS_READ,
    )
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
    principal: Principal = Depends(require_capability(_CAP_KEYS_READ)),
    db: AsyncSession = Depends(get_db),
):
    """DS records for active KSKs — the operator uploads these to the
    parent zone's operator to chain the trust anchor."""
    zone = await db.get(DnsZone, zone_id)
    if zone is None:
        raise NotFoundError(_ZONE_NOT_FOUND)
    await enforce_fabric_scope(
        db, principal.capabilities, zone.fabric_id, _CAP_KEYS_READ,
    )
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
    await enforce_fabric_scope(
        db, principal.capabilities, zone.fabric_id, _CAP_ZONES_UPDATE,
    )
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
    await enforce_fabric_scope(
        db, principal.capabilities, zone.fabric_id, _CAP_ZONES_UPDATE,
    )
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
    await enforce_fabric_scope(
        db, principal.capabilities, zone.fabric_id, _CAP_KEYS_ROTATE,
    )
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
    await enforce_fabric_scope(
        db, principal.capabilities, zone.fabric_id, _CAP_KEYS_ROTATE,
    )
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
    zone = await db.get(DnsZone, obj.zone_id)
    await enforce_fabric_scope(
        db, principal.capabilities,
        zone.fabric_id if zone else None, "dns:keys:delete",
    )
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
    principal: Principal = Depends(require_capability("dns:records:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(DnsRecord)
    if zone_id is not None:
        stmt = stmt.where(DnsRecord.zone_id == zone_id)
    if type_ is not None:
        stmt = stmt.where(DnsRecord.type == type_)
    if source is not None:
        stmt = stmt.where(DnsRecord.source == source)
    # Records have no direct fabric_id — join to DnsZone for the
    # filter. Subquery is cheap (zones table is small).
    in_scope = await scope_filtered_fabric_ids(
        db, principal.capabilities, "dns:records:read",
    )
    if in_scope is not None:
        if not in_scope:
            return Page[DnsRecordOut](items=[], total=0, page=params.page, page_size=params.page_size, has_more=False)
        stmt = stmt.where(DnsRecord.zone_id.in_(
            select(DnsZone.id).where(DnsZone.fabric_id.in_(in_scope))
        ))
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
    await enforce_fabric_scope(
        db, principal.capabilities, zone.fabric_id, "dns:records:create",
    )
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
    zone = await db.get(DnsZone, obj.zone_id)
    await enforce_fabric_scope(
        db, principal.capabilities,
        zone.fabric_id if zone else None,
        "dns:records:update",
    )
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
    zone = await db.get(DnsZone, obj.zone_id)
    await enforce_fabric_scope(
        db, principal.capabilities,
        zone.fabric_id if zone else None,
        "dns:records:delete",
    )
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
    principal: Principal = Depends(require_capability(_CAP_SERVERS_READ)),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(DnsServer)
    if site_id is not None:
        stmt = stmt.where(DnsServer.site_id == site_id)
    if fabric_id is not None:
        stmt = stmt.where(DnsServer.fabric_id == fabric_id)
    if role is not None:
        stmt = stmt.where(DnsServer.role == role)
    in_scope = await scope_filtered_fabric_ids(
        db, principal.capabilities, _CAP_SERVERS_READ,
    )
    if in_scope is not None:
        if not in_scope:
            return Page[DnsServerOut](items=[], total=0, page=params.page, page_size=params.page_size, has_more=False)
        stmt = stmt.where(DnsServer.fabric_id.in_(in_scope))
    return await paginate(db, stmt, model=DnsServer, params=params, out_model=DnsServerOut)


@router.post("/servers", response_model=DnsServerOut, status_code=201)
async def create_server(
    payload: DnsServerCreate,
    principal: Principal = Depends(require_capability("dns:servers:create")),
    db: AsyncSession = Depends(get_db),
):
    await enforce_fabric_scope(
        db, principal.capabilities, payload.fabric_id, "dns:servers:create",
    )
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
    principal: Principal = Depends(require_capability(_CAP_SERVERS_READ)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(DnsServer, server_id)
    if obj is None:
        raise NotFoundError(_SERVER_NOT_FOUND)
    await enforce_fabric_scope(db, principal.capabilities, obj.fabric_id, _CAP_SERVERS_READ)
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
    await enforce_fabric_scope(db, principal.capabilities, obj.fabric_id, "dns:servers:update")
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
    await enforce_fabric_scope(db, principal.capabilities, obj.fabric_id, "dns:servers:delete")
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
        # Top-K per-interval name counts. Older collectors omit this;
        # JSONB column accepts None for backward compat.
        top_names=(
            [e.model_dump() for e in payload.top_names]
            if payload.top_names is not None else None
        ),
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
    _: Principal = Depends(require_capability(_CAP_SERVERS_READ)),
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


# ----------------------- Dashboard -----------------------

class _DashGlobal(BaseModel):
    qps_now: float
    qps_avg: float
    queries_total: int
    nxdomain_pct: float
    servfail_pct: float
    p50_ms: float | None
    p95_ms: float | None
    sites_active: int
    servers_total: int
    zones_total: int
    zones_signed: int
    zones_nsec3: int
    anycast_groups: int
    engines: dict[str, int]


class _DashSeriesPoint(BaseModel):
    observed_at: datetime
    qps: float
    nxdomain_pct: float
    servfail_pct: float
    p50_ms: float | None
    p95_ms: float | None


class _DashSitePanel(BaseModel):
    site_id: UUID
    site_name: str
    qps_now: float
    queries_total: int
    nxdomain_pct: float
    servfail_pct: float
    p95_ms: float | None
    server_count: int


class _DashServerHealth(BaseModel):
    server_id: UUID
    name: str
    role: str
    engine: str
    site_id: UUID | None
    site_name: str | None
    last_render_status: str | None
    last_render_at: datetime | None
    last_render_etag: str | None
    qps_now: float | None


class _DashTopName(BaseModel):
    name: str
    type: str
    count: int


class _DashStorageStats(BaseModel):
    """Operator-facing instrumentation for the metrics-samples table —
    specifically the `top_names` JSONB column, which can balloon if a
    busy server's reservoir runs near its 100-row ship cap every
    interval. The dashboard surfaces these so operators can correlate
    `dns_metrics_retention_days` with actual disk usage and decide
    whether to tighten the window before the cron sweeps."""
    sample_count: int
    samples_with_top_names: int
    top_names_bytes_avg: int | None
    top_names_bytes_total: int


class DnsDashboardOut(BaseModel):
    generated_at: datetime
    window_minutes: int
    overall: _DashGlobal
    series: list[_DashSeriesPoint]
    by_site: list[_DashSitePanel]
    server_health: list[_DashServerHealth]
    # Top-N most-queried names across all servers in the window.
    # `null` (vs an empty list) means no collector in this deployment
    # has shipped a top_names payload yet — the UI uses that to tell
    # "dnstap not wired" apart from "wired but quiet". Populated by
    # aggregating `DnsServerMetricsSample.top_names` per the dnstap
    # plumbing tracked in .claude/plans/dns-top-names.md.
    top_names: list[_DashTopName] | None = None
    storage: _DashStorageStats


def _pct(numerator: int, total: int) -> float:
    return round((numerator / total) * 100.0, 2) if total else 0.0


def _weighted_latency(
    samples: list[DnsServerMetricsSample], attr: str,
) -> float | None:
    """Query-weighted average of a latency field across samples.
    Samples with no queries contribute nothing — a server that scraped
    0 qps in the slice shouldn't drag the percentile to whatever it
    last reported."""
    pairs = [
        (getattr(s, attr), s.queries) for s in samples
        if getattr(s, attr) is not None and s.queries > 0
    ]
    if not pairs:
        return None
    num = sum(v * w for v, w in pairs)
    den = sum(w for _, w in pairs)
    return round(num / den, 2)


def _qps_from_last_sample(sample: DnsServerMetricsSample | None) -> float | None:
    if sample is None or sample.interval_seconds <= 0:
        return None
    return round(sample.queries / sample.interval_seconds, 2)


def _engine_for(server: DnsServer, fabric: Fabric | None) -> str:
    """Resolve the recursive engine for one server. Auth pods are
    always CoreDNS in the hybrid model; only recursive pods honor the
    fabric-level `recursive_engine` knob."""
    if server.role != DnsServerRole.recursive:
        return "coredns"
    if fabric and fabric.recursive_engine:
        return fabric.recursive_engine.value
    return "coredns"


def _bucket_series(
    samples: list[DnsServerMetricsSample], minutes: int, buckets: int = 24,
) -> list[_DashSeriesPoint]:
    """Roll per-server samples into `buckets` equal time slices over
    the window. Each slice sums queries/error counts across servers and
    averages p50/p95 weighted by query volume — matches what an
    operator would compute by eye on a single-line chart."""
    if not samples:
        return []
    window = timedelta(minutes=minutes)
    end = datetime.now(UTC)
    start = end - window
    slice_s = max(int(window.total_seconds() / buckets), 1)
    out: list[_DashSeriesPoint] = []
    for i in range(buckets):
        b_start = start + timedelta(seconds=slice_s * i)
        b_end = b_start + timedelta(seconds=slice_s)
        in_slice = [
            s for s in samples
            if b_start <= s.observed_at < b_end
        ]
        if not in_slice:
            out.append(_DashSeriesPoint(
                observed_at=b_end, qps=0.0,
                nxdomain_pct=0.0, servfail_pct=0.0,
                p50_ms=None, p95_ms=None,
            ))
            continue
        q = sum(s.queries for s in in_slice)
        nx = sum(s.nxdomain for s in in_slice)
        sf = sum(s.servfail for s in in_slice)
        out.append(_DashSeriesPoint(
            observed_at=b_end,
            qps=round(q / slice_s, 2),
            nxdomain_pct=_pct(nx, q),
            servfail_pct=_pct(sf, q),
            p50_ms=_weighted_latency(in_slice, "p50_ms"),
            p95_ms=_weighted_latency(in_slice, "p95_ms"),
        ))
    return out


def _build_global_kpis(
    *,
    samples: list[DnsServerMetricsSample],
    servers: list[DnsServer],
    fabrics: dict[UUID, Fabric],
    zones: list[DnsZone],
    qps_now_per_server: dict[UUID, float | None],
    ag_count: int,
    minutes: int,
) -> _DashGlobal:
    """Roll up the top-strip KPI numbers from the same sample window
    used by the series. Extracted so the route handler stays under the
    cognitive-complexity cap."""
    total_q = sum(s.queries for s in samples)
    total_nx = sum(s.nxdomain for s in samples)
    total_sf = sum(s.servfail for s in samples)
    qps_now = sum(
        (qps_now_per_server.get(srv.id) or 0.0) for srv in servers
    )
    engines: dict[str, int] = {"coredns": 0, "hickory": 0}
    for srv in servers:
        if srv.role != DnsServerRole.recursive:
            continue
        engine = _engine_for(srv, fabrics.get(srv.fabric_id))
        engines[engine] = engines.get(engine, 0) + 1
    return _DashGlobal(
        qps_now=round(qps_now, 2),
        qps_avg=round(total_q / (minutes * 60), 2) if minutes > 0 else 0.0,
        queries_total=total_q,
        nxdomain_pct=_pct(total_nx, total_q),
        servfail_pct=_pct(total_sf, total_q),
        p50_ms=_weighted_latency(samples, "p50_ms"),
        p95_ms=_weighted_latency(samples, "p95_ms"),
        sites_active=len({srv.site_id for srv in servers if srv.site_id}),
        servers_total=len(servers),
        zones_total=len(zones),
        zones_signed=sum(1 for z in zones if getattr(z, "signed", False)),
        zones_nsec3=sum(
            1 for z in zones if getattr(z, "nsec3_salt", None) is not None
        ),
        anycast_groups=ag_count,
        engines=engines,
    )


def _build_by_site(
    *,
    samples: list[DnsServerMetricsSample],
    servers: list[DnsServer],
    sites: dict[UUID, Site],
    qps_now_per_server: dict[UUID, float | None],
) -> list[_DashSitePanel]:
    """One row per site that has at least one DnsServer. Sites with
    zero traffic still show up (server_count > 0) so an operator can
    see them in the table before any qps has accrued."""
    servers_by_site: dict[UUID, list[DnsServer]] = {}
    for srv in servers:
        if srv.site_id:
            servers_by_site.setdefault(srv.site_id, []).append(srv)

    server_to_site = {srv.id: srv.site_id for srv in servers if srv.site_id}
    samples_by_site: dict[UUID, list[DnsServerMetricsSample]] = {}
    for s in samples:
        site_id = server_to_site.get(s.server_id)
        if site_id is not None:
            samples_by_site.setdefault(site_id, []).append(s)

    out: list[_DashSitePanel] = []
    for site_id, srv_list in servers_by_site.items():
        site_samples = samples_by_site.get(site_id, [])
        q = sum(s.queries for s in site_samples)
        out.append(_DashSitePanel(
            site_id=site_id,
            site_name=sites[site_id].name if site_id in sites else str(site_id),
            qps_now=round(
                sum((qps_now_per_server.get(srv.id) or 0.0) for srv in srv_list),
                2,
            ),
            queries_total=q,
            nxdomain_pct=_pct(sum(s.nxdomain for s in site_samples), q),
            servfail_pct=_pct(sum(s.servfail for s in site_samples), q),
            p95_ms=_weighted_latency(site_samples, "p95_ms"),
            server_count=len(srv_list),
        ))
    out.sort(key=lambda r: r.qps_now, reverse=True)
    return out


async def _storage_stats(db: AsyncSession) -> _DashStorageStats:
    """One round-trip through Postgres for the metrics-samples size
    metrics. `pg_column_size` measures the TOAST-compressed footprint
    of the JSONB column on disk, which is what `dns_metrics_retention_days`
    actually trims when the cron sweeps. Null when the column is null
    for every row (dnstap not wired anywhere yet), in which case the
    avg is meaningless and the UI suppresses the bytes line."""
    row = (
        await db.execute(
            text(
                "SELECT count(*) AS total, "
                "count(*) FILTER (WHERE top_names IS NOT NULL) AS with_tn, "
                "COALESCE(avg(pg_column_size(top_names))::bigint, 0) AS avg_b, "
                "COALESCE(sum(pg_column_size(top_names))::bigint, 0) AS sum_b "
                "FROM dns_server_metrics_samples"
            )
        )
    ).first()
    total, with_tn, avg_b, sum_b = (
        (row[0], row[1], row[2], row[3]) if row else (0, 0, 0, 0)
    )
    return _DashStorageStats(
        sample_count=int(total),
        samples_with_top_names=int(with_tn),
        top_names_bytes_avg=int(avg_b) if with_tn else None,
        top_names_bytes_total=int(sum_b),
    )


def _aggregate_top_names(
    samples: list[DnsServerMetricsSample], limit: int = 10,
) -> list[_DashTopName] | None:
    """Sum per-name counts across every sample in the window, then
    return the top `limit` (name, type) pairs sorted by count.

    Returns `None` when no sample in the window carries a `top_names`
    payload — the UI uses null to mean "dnstap isn't wired on any
    server" (placeholder card stays visible). An empty list means
    dnstap is wired but the window saw zero queries (real card with a
    "no traffic in window" state)."""
    if not any(s.top_names is not None for s in samples):
        return None
    totals: dict[tuple[str, str], int] = {}
    for s in samples:
        if not s.top_names:
            continue
        for entry in s.top_names:
            key = (entry.get("name", ""), entry.get("type", ""))
            if not key[0]:
                continue
            totals[key] = totals.get(key, 0) + int(entry.get("count", 0))
    ranked = sorted(totals.items(), key=lambda kv: kv[1], reverse=True)
    return [
        _DashTopName(name=n, type=t, count=c)
        for (n, t), c in ranked[:limit]
    ]


def _build_server_health(
    *,
    servers: list[DnsServer],
    fabrics: dict[UUID, Fabric],
    sites: dict[UUID, Site],
    qps_now_per_server: dict[UUID, float | None],
) -> list[_DashServerHealth]:
    """One row per registered DnsServer. `last_render_status` may be a
    stringy enum or already a value — normalize either to .value so
    the response shape is stable."""
    out: list[_DashServerHealth] = []
    for srv in servers:
        site_obj = sites.get(srv.site_id) if srv.site_id else None
        render_status = (
            srv.last_render_status.value
            if srv.last_render_status is not None
            and hasattr(srv.last_render_status, "value")
            else srv.last_render_status
        )
        out.append(_DashServerHealth(
            server_id=srv.id,
            name=srv.name,
            role=srv.role.value,
            engine=_engine_for(srv, fabrics.get(srv.fabric_id)),
            site_id=srv.site_id,
            site_name=site_obj.name if site_obj else None,
            last_render_status=render_status,
            last_render_at=srv.last_render_at,
            last_render_etag=srv.last_render_etag,
            qps_now=qps_now_per_server.get(srv.id),
        ))
    out.sort(key=lambda r: (r.qps_now or 0.0), reverse=True)
    return out


@router.get("/dashboard", response_model=DnsDashboardOut)
async def dns_dashboard(
    minutes: int = Query(60, ge=5, le=24 * 60),
    fabric_id: UUID | None = Query(
        None, description="Scope all aggregates to a single fabric"
    ),
    _: Principal = Depends(require_capability(_CAP_SERVERS_READ)),
    db: AsyncSession = Depends(get_db),
):
    """One-shot aggregate the DNS Overview page polls. Folded together
    so the dashboard renders in a single round-trip instead of
    fan-out queries.

    Includes:
      - Global KPIs (QPS now/avg, error %, p50/p95, zone + server counts).
      - A bucketed series for the QPS / error-rate / latency chart.
      - Per-site rollup (one row per site that has DNS servers).
      - Server health (one row per server with the most recent sample
        attached, so the UI can show a status chip + qps trend).

    When `fabric_id` is set, every aggregate is scoped to that fabric:
    only servers/zones/anycast-groups bound to it count, and the
    sample window filters down to samples emitted by those servers.
    The series, by-site rollup, and server-health table all derive
    from the same filtered set so the dashboard stays internally
    consistent under scope.
    """
    now = datetime.now(UTC)
    cutoff = now - timedelta(minutes=minutes)

    server_stmt = select(DnsServer)
    zone_stmt = select(DnsZone)
    ag_stmt = select(func.count(AnycastGroup.id))
    if fabric_id is not None:
        server_stmt = server_stmt.where(DnsServer.fabric_id == fabric_id)
        zone_stmt = zone_stmt.where(DnsZone.fabric_id == fabric_id)
        ag_stmt = ag_stmt.where(AnycastGroup.fabric_id == fabric_id)

    servers = (await db.execute(server_stmt)).scalars().all()
    server_ids = {srv.id for srv in servers}

    sample_stmt = (
        select(DnsServerMetricsSample)
        .where(DnsServerMetricsSample.observed_at >= cutoff)
        .order_by(DnsServerMetricsSample.observed_at.asc())
    )
    if fabric_id is not None and server_ids:
        sample_stmt = sample_stmt.where(
            DnsServerMetricsSample.server_id.in_(server_ids)
        )
    elif fabric_id is not None:
        # Fabric scope with zero servers — short-circuit to empty
        # rather than letting `.in_({})` blow up downstream.
        sample_stmt = sample_stmt.where(False)
    samples = (await db.execute(sample_stmt)).scalars().all()
    sites = {s.id: s for s in (await db.execute(select(Site))).scalars().all()}
    fabrics = {f.id: f for f in (await db.execute(select(Fabric))).scalars().all()}
    zones = (await db.execute(zone_stmt)).scalars().all()
    ag_count = int((await db.execute(ag_stmt)).scalar_one())

    # Latest sample per server — walking the sorted list once is cheaper
    # than a per-server LIMIT 1 query for each. The qps_now numbers on
    # both the global KPI strip and the per-site rollup derive from
    # this same map so the dashboard stays internally consistent.
    latest_per_server: dict[UUID, DnsServerMetricsSample] = {}
    for s in samples:
        latest_per_server[s.server_id] = s
    qps_now_per_server: dict[UUID, float | None] = {
        sid: _qps_from_last_sample(latest_per_server.get(sid))
        for sid in {srv.id for srv in servers}
    }

    return DnsDashboardOut(
        generated_at=now,
        window_minutes=minutes,
        overall=_build_global_kpis(
            samples=samples, servers=servers, fabrics=fabrics,
            zones=zones, qps_now_per_server=qps_now_per_server,
            ag_count=ag_count, minutes=minutes,
        ),
        series=_bucket_series(samples, minutes),
        by_site=_build_by_site(
            samples=samples, servers=servers, sites=sites,
            qps_now_per_server=qps_now_per_server,
        ),
        server_health=_build_server_health(
            servers=servers, fabrics=fabrics, sites=sites,
            qps_now_per_server=qps_now_per_server,
        ),
        top_names=_aggregate_top_names(samples),
        storage=await _storage_stats(db),
    )


# ----------------------- Anycast groups -----------------------
@router.get("/anycast-groups", response_model=Page[AnycastGroupOut])
async def list_anycast_groups(
    params: PageParams = Depends(PageParams.from_query),
    fabric_id: UUID | None = Query(None),
    principal: Principal = Depends(require_capability("dns:anycast-groups:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(AnycastGroup)
    if fabric_id is not None:
        stmt = stmt.where(AnycastGroup.fabric_id == fabric_id)
    in_scope = await scope_filtered_fabric_ids(
        db, principal.capabilities, "dns:anycast-groups:read",
    )
    if in_scope is not None:
        if not in_scope:
            return Page[AnycastGroupOut](items=[], total=0, page=params.page, page_size=params.page_size, has_more=False)
        stmt = stmt.where(AnycastGroup.fabric_id.in_(in_scope))
    return await paginate(db, stmt, model=AnycastGroup, params=params, out_model=AnycastGroupOut)


@router.post("/anycast-groups", response_model=AnycastGroupOut, status_code=201)
async def create_anycast_group(
    payload: AnycastGroupCreate,
    principal: Principal = Depends(require_capability("dns:anycast-groups:create")),
    db: AsyncSession = Depends(get_db),
):
    await enforce_fabric_scope(
        db, principal.capabilities, payload.fabric_id, "dns:anycast-groups:create",
    )
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
    await enforce_fabric_scope(db, principal.capabilities, obj.fabric_id, "dns:anycast-groups:update")
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
    await enforce_fabric_scope(db, principal.capabilities, obj.fabric_id, "dns:anycast-groups:delete")
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
    principal: Principal = Depends(require_capability("dns:forwarders:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(DnsForwarder)
    if fabric_id is not None:
        stmt = stmt.where(DnsForwarder.fabric_id == fabric_id)
    in_scope = await scope_filtered_fabric_ids(
        db, principal.capabilities, "dns:forwarders:read",
    )
    if in_scope is not None:
        if not in_scope:
            return Page[DnsForwarderOut](items=[], total=0, page=params.page, page_size=params.page_size, has_more=False)
        stmt = stmt.where(DnsForwarder.fabric_id.in_(in_scope))
    return await paginate(
        db, stmt, model=DnsForwarder, params=params, out_model=DnsForwarderOut,
    )


@router.post("/forwarders", response_model=DnsForwarderOut, status_code=201)
async def create_forwarder(
    payload: DnsForwarderCreate,
    principal: Principal = Depends(require_capability("dns:forwarders:create")),
    db: AsyncSession = Depends(get_db),
):
    await enforce_fabric_scope(
        db, principal.capabilities, payload.fabric_id, "dns:forwarders:create",
    )
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
    await enforce_fabric_scope(db, principal.capabilities, obj.fabric_id, "dns:forwarders:update")
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
    await enforce_fabric_scope(db, principal.capabilities, obj.fabric_id, "dns:forwarders:delete")
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
    principal: Principal = Depends(require_capability("dns:blocklists:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(DnsBlocklist)
    if fabric_id is not None:
        stmt = stmt.where(DnsBlocklist.fabric_id == fabric_id)
    in_scope = await scope_filtered_fabric_ids(
        db, principal.capabilities, "dns:blocklists:read",
    )
    if in_scope is not None:
        if not in_scope:
            return Page[DnsBlocklistOut](items=[], total=0, page=params.page, page_size=params.page_size, has_more=False)
        stmt = stmt.where(DnsBlocklist.fabric_id.in_(in_scope))
    return await paginate(
        db, stmt, model=DnsBlocklist, params=params, out_model=DnsBlocklistOut,
    )


@router.post("/blocklists", response_model=DnsBlocklistOut, status_code=201)
async def create_blocklist(
    payload: DnsBlocklistCreate,
    principal: Principal = Depends(require_capability("dns:blocklists:create")),
    db: AsyncSession = Depends(get_db),
):
    await enforce_fabric_scope(
        db, principal.capabilities, payload.fabric_id, "dns:blocklists:create",
    )
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
    await enforce_fabric_scope(db, principal.capabilities, obj.fabric_id, "dns:blocklists:update")
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
    await enforce_fabric_scope(db, principal.capabilities, obj.fabric_id, "dns:blocklists:delete")
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
    principal: Principal = Depends(require_capability("dns:blocklists:read")),
    db: AsyncSession = Depends(get_db),
):
    blocklist = await db.get(DnsBlocklist, blocklist_id)
    if blocklist is None:
        raise NotFoundError(_BLOCKLIST_NOT_FOUND)
    await enforce_fabric_scope(
        db, principal.capabilities, blocklist.fabric_id, "dns:blocklists:read",
    )
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
    blocklist = await db.get(DnsBlocklist, blocklist_id)
    if blocklist is None:
        raise NotFoundError(_BLOCKLIST_NOT_FOUND)
    await enforce_fabric_scope(
        db, principal.capabilities, blocklist.fabric_id, "dns:blocklists:update",
    )
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
    blocklist = await db.get(DnsBlocklist, blocklist_id)
    if blocklist is None:
        raise NotFoundError(_BLOCKLIST_NOT_FOUND)
    await enforce_fabric_scope(
        db, principal.capabilities, blocklist.fabric_id, "dns:blocklists:update",
    )
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
    blocklist = await db.get(DnsBlocklist, blocklist_id)
    await enforce_fabric_scope(
        db, principal.capabilities,
        blocklist.fabric_id if blocklist else None,
        "dns:blocklists:update",
    )
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
    principal: Principal = Depends(require_capability("dns:views:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(DnsView)
    if fabric_id is not None:
        stmt = stmt.where(DnsView.fabric_id == fabric_id)
    in_scope = await scope_filtered_fabric_ids(
        db, principal.capabilities, "dns:views:read",
    )
    if in_scope is not None:
        if not in_scope:
            return Page[DnsViewOut](items=[], total=0, page=params.page, page_size=params.page_size, has_more=False)
        stmt = stmt.where(DnsView.fabric_id.in_(in_scope))
    return await paginate(
        db, stmt, model=DnsView, params=params, out_model=DnsViewOut,
    )


@router.post("/views", response_model=DnsViewOut, status_code=201)
async def create_view(
    payload: DnsViewCreate,
    principal: Principal = Depends(require_capability("dns:views:create")),
    db: AsyncSession = Depends(get_db),
):
    await enforce_fabric_scope(
        db, principal.capabilities, payload.fabric_id, "dns:views:create",
    )
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
    await enforce_fabric_scope(db, principal.capabilities, obj.fabric_id, "dns:views:update")
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
    await enforce_fabric_scope(db, principal.capabilities, obj.fabric_id, "dns:views:delete")
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
    principal: Principal = Depends(require_capability("dns:health-checks:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(DnsHealthCheck)
    if fabric_id is not None:
        stmt = stmt.where(DnsHealthCheck.fabric_id == fabric_id)
    in_scope = await scope_filtered_fabric_ids(
        db, principal.capabilities, "dns:health-checks:read",
    )
    if in_scope is not None:
        if not in_scope:
            return Page[DnsHealthCheckOut](items=[], total=0, page=params.page, page_size=params.page_size, has_more=False)
        stmt = stmt.where(DnsHealthCheck.fabric_id.in_(in_scope))
    return await paginate(
        db, stmt, model=DnsHealthCheck, params=params, out_model=DnsHealthCheckOut,
    )


@router.post("/health-checks", response_model=DnsHealthCheckOut, status_code=201)
async def create_health_check(
    payload: DnsHealthCheckCreate,
    principal: Principal = Depends(require_capability("dns:health-checks:create")),
    db: AsyncSession = Depends(get_db),
):
    await enforce_fabric_scope(
        db, principal.capabilities, payload.fabric_id, "dns:health-checks:create",
    )
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
    await enforce_fabric_scope(db, principal.capabilities, obj.fabric_id, "dns:health-checks:update")
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
    await enforce_fabric_scope(db, principal.capabilities, obj.fabric_id, "dns:health-checks:delete")
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
    principal: Principal = Depends(require_capability("dns:bgp-peers:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(BgpPeer)
    if site_id is not None:
        stmt = stmt.where(BgpPeer.site_id == site_id)
    in_scope = await scope_filtered_site_ids(
        db, principal.capabilities, "dns:bgp-peers:read",
    )
    if in_scope is not None:
        if not in_scope:
            return Page[BgpPeerOut](
                items=[], total=0, page=params.page,
                page_size=params.page_size, has_more=False,
            )
        stmt = stmt.where(BgpPeer.site_id.in_(in_scope))
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
    await enforce_site_scope(
        db, principal.capabilities, payload.site_id, "dns:bgp-peers:create",
    )
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
    await enforce_site_scope(
        db, principal.capabilities, obj.site_id, "dns:bgp-peers:update",
    )
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
    await enforce_site_scope(
        db, principal.capabilities, obj.site_id, "dns:bgp-peers:delete",
    )
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
    principal: Principal = Depends(require_capability("dns:anycast-bindings:read")),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(AnycastBgpBinding)
    if dns_server_id is not None:
        stmt = stmt.where(AnycastBgpBinding.dns_server_id == dns_server_id)
    if bgp_peer_id is not None:
        stmt = stmt.where(AnycastBgpBinding.bgp_peer_id == bgp_peer_id)
    in_scope = await scope_filtered_site_ids(
        db, principal.capabilities, "dns:anycast-bindings:read",
    )
    if in_scope is not None:
        if not in_scope:
            return Page[AnycastBgpBindingOut](
                items=[], total=0, page=params.page,
                page_size=params.page_size, has_more=False,
            )
        stmt = stmt.where(AnycastBgpBinding.bgp_peer_id.in_(
            select(BgpPeer.id).where(BgpPeer.site_id.in_(in_scope))
        ))
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
    await enforce_site_scope(
        db, principal.capabilities, peer.site_id,
        "dns:anycast-bindings:create",
    )
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
    peer = await db.get(BgpPeer, obj.bgp_peer_id)
    await enforce_site_scope(
        db, principal.capabilities,
        peer.site_id if peer else None,
        "dns:anycast-bindings:delete",
    )
    await db.execute(delete(AnycastBgpBinding).where(AnycastBgpBinding.id == binding_id))
    await audit.record(
        db, principal, action="anycast_bgp_binding.delete",
        target_type="anycast_bgp_binding", target_id=str(binding_id),
    )
    await db.commit()
