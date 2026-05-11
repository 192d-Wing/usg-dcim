"""CRUD over the BGP policy + identity surfaces.

Endpoint layout (all under /bgp):

  /asns                            GET list (filter by kind), POST
  /asns/{id}                       GET, PATCH, DELETE
  /tcp-ao-key-chains               GET, POST
  /tcp-ao-key-chains/{id}          GET, PATCH, DELETE
  /tcp-ao-keys                     GET (filter by key_chain_id), POST
  /tcp-ao-keys/{id}                GET, PATCH, DELETE
  /prefix-lists                    GET (filter by family), POST
  /prefix-lists/{id}               GET, PATCH, DELETE
  /prefix-list-entries             GET (filter by prefix_list_id), POST
  /prefix-list-entries/{id}        GET, PATCH, DELETE
  /community-lists                 GET (filter by kind), POST
  /community-lists/{id}            GET, PATCH, DELETE
  /community-list-entries          GET (filter by community_list_id), POST
  /community-list-entries/{id}     GET, PATCH, DELETE
  /route-maps                      GET, POST
  /route-maps/{id}                 GET, PATCH, DELETE
  /route-map-entries               GET (filter by route_map_id), POST
  /route-map-entries/{id}          GET, PATCH, DELETE

DELETE on a parent cascades by guarding its children: if entries exist,
the parent delete returns 409 so the operator clears entries first.
"""

from __future__ import annotations

import secrets
from datetime import UTC, datetime, timedelta
from uuid import UUID

from fastapi import APIRouter, Depends, Query
from pydantic import BaseModel
from sqlalchemy import delete, func, select
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..errors import ConflictError, NotFoundError
from ..models.bgp import (
    AddressFamilyV4V6,
    Asn,
    AsnKind,
    CommunityKind,
    CommunityList,
    CommunityListEntry,
    PrefixList,
    PrefixListEntry,
    RouteMap,
    RouteMapEntry,
    TcpAoAlgorithm,
    TcpAoKey,
    TcpAoKeyChain,
)
from ..models.organization import Organization
from ..schemas.bgp import (
    AsnCreate,
    AsnOut,
    AsnUpdate,
    CommunityListCreate,
    CommunityListEntryCreate,
    CommunityListEntryOut,
    CommunityListEntryUpdate,
    CommunityListOut,
    CommunityListUpdate,
    PrefixListCreate,
    PrefixListEntryCreate,
    PrefixListEntryOut,
    PrefixListEntryUpdate,
    PrefixListOut,
    PrefixListUpdate,
    RouteMapCreate,
    RouteMapEntryCreate,
    RouteMapEntryOut,
    RouteMapEntryUpdate,
    RouteMapOut,
    RouteMapUpdate,
    TcpAoKeyChainCreate,
    TcpAoKeyChainOut,
    TcpAoKeyChainUpdate,
    TcpAoKeyCreate,
    TcpAoKeyOut,
    TcpAoKeyUpdate,
)
from ..schemas.common import Page, PageParams
from ..security import audit
from ..security.capabilities import INVENTORY_READ, INVENTORY_WRITE
from ..security.deps import Principal, require_capability
from ._pagination import paginate

router = APIRouter(prefix="/bgp", tags=["bgp"])


# ----------------------- ASNs -----------------------

_ASN_NOT_FOUND = "asn not found"


@router.get("/asns", response_model=Page[AsnOut])
async def list_asns(
    params: PageParams = Depends(PageParams.from_query),
    kind: AsnKind | None = Query(None),
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(Asn)
    if kind is not None:
        stmt = stmt.where(Asn.kind == kind)
    return await paginate(db, stmt, model=Asn, params=params, out_model=AsnOut)


@router.post("/asns", response_model=AsnOut, status_code=201)
async def create_asn(
    payload: AsnCreate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    if payload.organization_id is not None:
        if await db.get(Organization, payload.organization_id) is None:
            raise NotFoundError("organization not found")
    obj = Asn(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="asn.create", target_type="asn", target_id=str(obj.id),
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/asns/{asn_id}", response_model=AsnOut)
async def update_asn(
    asn_id: UUID,
    payload: AsnUpdate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Asn, asn_id)
    if obj is None:
        raise NotFoundError(_ASN_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    if (
        "organization_id" in diff
        and diff["organization_id"] is not None
        and await db.get(Organization, diff["organization_id"]) is None
    ):
        raise NotFoundError("organization not found")
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="asn.update", target_type="asn",
        target_id=str(asn_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/asns/{asn_id}", status_code=204)
async def delete_asn(
    asn_id: UUID,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Asn, asn_id)
    if obj is None:
        raise NotFoundError(_ASN_NOT_FOUND)
    await db.execute(delete(Asn).where(Asn.id == asn_id))
    await audit.record(
        db, principal, action="asn.delete", target_type="asn", target_id=str(asn_id),
    )
    await db.commit()


# ----------------------- TCP AO key chains -----------------------

_TCP_AO_CHAIN_NOT_FOUND = "tcp ao key chain not found"
_TCP_AO_KEY_NOT_FOUND = "tcp ao key not found"


@router.get("/tcp-ao-key-chains", response_model=Page[TcpAoKeyChainOut])
async def list_tcp_ao_chains(
    params: PageParams = Depends(PageParams.from_query),
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    return await paginate(
        db, select(TcpAoKeyChain), model=TcpAoKeyChain, params=params, out_model=TcpAoKeyChainOut,
    )


@router.post("/tcp-ao-key-chains", response_model=TcpAoKeyChainOut, status_code=201)
async def create_tcp_ao_chain(
    payload: TcpAoKeyChainCreate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = TcpAoKeyChain(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="tcp_ao_key_chain.create",
        target_type="tcp_ao_key_chain", target_id=str(obj.id),
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/tcp-ao-key-chains/{chain_id}", response_model=TcpAoKeyChainOut)
async def update_tcp_ao_chain(
    chain_id: UUID,
    payload: TcpAoKeyChainUpdate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(TcpAoKeyChain, chain_id)
    if obj is None:
        raise NotFoundError(_TCP_AO_CHAIN_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="tcp_ao_key_chain.update",
        target_type="tcp_ao_key_chain", target_id=str(chain_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/tcp-ao-key-chains/{chain_id}", status_code=204)
async def delete_tcp_ao_chain(
    chain_id: UUID,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(TcpAoKeyChain, chain_id)
    if obj is None:
        raise NotFoundError(_TCP_AO_CHAIN_NOT_FOUND)
    # Guard: refuse to drop a chain that still has keys; operator clears
    # entries first so secret rotation history is intentional.
    in_use = (
        await db.execute(
            select(TcpAoKey.id).where(TcpAoKey.key_chain_id == chain_id).limit(1),
        )
    ).scalar_one_or_none()
    if in_use is not None:
        raise ConflictError("key chain still has keys; remove them first")
    await db.execute(delete(TcpAoKeyChain).where(TcpAoKeyChain.id == chain_id))
    await audit.record(
        db, principal, action="tcp_ao_key_chain.delete",
        target_type="tcp_ao_key_chain", target_id=str(chain_id),
    )
    await db.commit()


@router.get("/tcp-ao-keys", response_model=Page[TcpAoKeyOut])
async def list_tcp_ao_keys(
    params: PageParams = Depends(PageParams.from_query),
    key_chain_id: UUID | None = Query(None),
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(TcpAoKey)
    if key_chain_id is not None:
        stmt = stmt.where(TcpAoKey.key_chain_id == key_chain_id)
    return await paginate(db, stmt, model=TcpAoKey, params=params, out_model=TcpAoKeyOut)


@router.post("/tcp-ao-keys", response_model=TcpAoKeyOut, status_code=201)
async def create_tcp_ao_key(
    payload: TcpAoKeyCreate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    chain = await db.get(TcpAoKeyChain, payload.key_chain_id)
    if chain is None:
        raise NotFoundError(_TCP_AO_CHAIN_NOT_FOUND)
    obj = TcpAoKey(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="tcp_ao_key.create",
        target_type="tcp_ao_key", target_id=str(obj.id),
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/tcp-ao-keys/{key_id_pk}", response_model=TcpAoKeyOut)
async def update_tcp_ao_key(
    key_id_pk: UUID,
    payload: TcpAoKeyUpdate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(TcpAoKey, key_id_pk)
    if obj is None:
        raise NotFoundError(_TCP_AO_KEY_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="tcp_ao_key.update",
        target_type="tcp_ao_key", target_id=str(key_id_pk), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/tcp-ao-keys/{key_id_pk}", status_code=204)
async def delete_tcp_ao_key(
    key_id_pk: UUID,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(TcpAoKey, key_id_pk)
    if obj is None:
        raise NotFoundError(_TCP_AO_KEY_NOT_FOUND)
    await db.execute(delete(TcpAoKey).where(TcpAoKey.id == key_id_pk))
    await audit.record(
        db, principal, action="tcp_ao_key.delete",
        target_type="tcp_ao_key", target_id=str(key_id_pk),
    )
    await db.commit()


# ---- TCP AO key chain rotation: generate a year's worth at once ----

class TcpAoRotationRequest(BaseModel):
    """Generate `count` keys with consecutive `days_per_key`-day windows
    starting at `start`. Each key gets a server-side 256-bit random
    secret (32 bytes / 64 hex chars). Defaults match the operator's
    usual ask: 12 keys × 30 days = ~1 year."""

    start: datetime | None = None
    count: int = 12
    days_per_key: int = 30
    algorithm: TcpAoAlgorithm = TcpAoAlgorithm.hmac_sha1_96


@router.post(
    "/tcp-ao-key-chains/{chain_id}/rotate-batch",
    response_model=list[TcpAoKeyOut],
    status_code=201,
)
async def rotate_tcp_ao_chain(
    chain_id: UUID,
    payload: TcpAoRotationRequest,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    chain = await db.get(TcpAoKeyChain, chain_id)
    if chain is None:
        raise NotFoundError(_TCP_AO_CHAIN_NOT_FOUND)
    if payload.count < 1 or payload.count > 365:
        raise ValidationError("count must be 1..365")
    if payload.days_per_key < 1 or payload.days_per_key > 366:
        raise ValidationError("days_per_key must be 1..366")

    # Resume from max(key_id) so a follow-up rotation extends the chain
    # rather than colliding with existing keys.
    next_id = (
        await db.execute(
            select(func.max(TcpAoKey.key_id)).where(TcpAoKey.key_chain_id == chain_id),
        )
    ).scalar_one_or_none()
    next_key_id = (next_id or 0) + 1

    start = payload.start or datetime.now(tz=UTC)
    window = timedelta(days=payload.days_per_key)

    created: list[TcpAoKey] = []
    for i in range(payload.count):
        valid_from = start + window * i
        valid_to = valid_from + window
        key_id = next_key_id + i
        # 32 bytes = 256 bits of entropy. secrets.token_hex generates a
        # cryptographically-secure hex string (64 chars).
        secret_hex = secrets.token_hex(32)
        obj = TcpAoKey(
            key_chain_id=chain_id,
            key_id=key_id,
            send_id=key_id,
            recv_id=key_id,
            algorithm=payload.algorithm,
            secret=secret_hex,
            valid_from=valid_from,
            valid_to=valid_to,
            description=f"Auto-generated rotation #{i + 1}/{payload.count}",
        )
        db.add(obj)
        created.append(obj)

    await db.flush()
    await audit.record(
        db, principal, action="tcp_ao_key.rotate_batch",
        target_type="tcp_ao_key_chain", target_id=str(chain_id),
        diff={"count": payload.count, "days_per_key": payload.days_per_key,
              "start": start.isoformat(), "first_key_id": next_key_id},
    )
    await db.commit()
    for obj in created:
        await db.refresh(obj)
    return created


# ----------------------- Prefix lists -----------------------

_PREFIX_LIST_NOT_FOUND = "prefix list not found"
_PREFIX_LIST_ENTRY_NOT_FOUND = "prefix list entry not found"


@router.get("/prefix-lists", response_model=Page[PrefixListOut])
async def list_prefix_lists(
    params: PageParams = Depends(PageParams.from_query),
    family: AddressFamilyV4V6 | None = Query(None),
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(PrefixList)
    if family is not None:
        stmt = stmt.where(PrefixList.family == family)
    return await paginate(db, stmt, model=PrefixList, params=params, out_model=PrefixListOut)


@router.post("/prefix-lists", response_model=PrefixListOut, status_code=201)
async def create_prefix_list(
    payload: PrefixListCreate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = PrefixList(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="prefix_list.create",
        target_type="prefix_list", target_id=str(obj.id),
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/prefix-lists/{list_id}", response_model=PrefixListOut)
async def update_prefix_list(
    list_id: UUID,
    payload: PrefixListUpdate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(PrefixList, list_id)
    if obj is None:
        raise NotFoundError(_PREFIX_LIST_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="prefix_list.update",
        target_type="prefix_list", target_id=str(list_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/prefix-lists/{list_id}", status_code=204)
async def delete_prefix_list(
    list_id: UUID,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(PrefixList, list_id)
    if obj is None:
        raise NotFoundError(_PREFIX_LIST_NOT_FOUND)
    in_use = (
        await db.execute(
            select(PrefixListEntry.id).where(PrefixListEntry.prefix_list_id == list_id).limit(1),
        )
    ).scalar_one_or_none()
    if in_use is not None:
        raise ConflictError("prefix list still has entries; remove them first")
    await db.execute(delete(PrefixList).where(PrefixList.id == list_id))
    await audit.record(
        db, principal, action="prefix_list.delete",
        target_type="prefix_list", target_id=str(list_id),
    )
    await db.commit()


@router.get("/prefix-list-entries", response_model=Page[PrefixListEntryOut])
async def list_prefix_list_entries(
    params: PageParams = Depends(PageParams.from_query),
    prefix_list_id: UUID | None = Query(None),
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(PrefixListEntry)
    if prefix_list_id is not None:
        stmt = stmt.where(PrefixListEntry.prefix_list_id == prefix_list_id)
    return await paginate(
        db, stmt, model=PrefixListEntry, params=params, out_model=PrefixListEntryOut,
    )


@router.post("/prefix-list-entries", response_model=PrefixListEntryOut, status_code=201)
async def create_prefix_list_entry(
    payload: PrefixListEntryCreate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    parent = await db.get(PrefixList, payload.prefix_list_id)
    if parent is None:
        raise NotFoundError(_PREFIX_LIST_NOT_FOUND)
    obj = PrefixListEntry(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="prefix_list_entry.create",
        target_type="prefix_list_entry", target_id=str(obj.id),
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/prefix-list-entries/{entry_id}", response_model=PrefixListEntryOut)
async def update_prefix_list_entry(
    entry_id: UUID,
    payload: PrefixListEntryUpdate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(PrefixListEntry, entry_id)
    if obj is None:
        raise NotFoundError(_PREFIX_LIST_ENTRY_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="prefix_list_entry.update",
        target_type="prefix_list_entry", target_id=str(entry_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/prefix-list-entries/{entry_id}", status_code=204)
async def delete_prefix_list_entry(
    entry_id: UUID,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(PrefixListEntry, entry_id)
    if obj is None:
        raise NotFoundError(_PREFIX_LIST_ENTRY_NOT_FOUND)
    await db.execute(delete(PrefixListEntry).where(PrefixListEntry.id == entry_id))
    await audit.record(
        db, principal, action="prefix_list_entry.delete",
        target_type="prefix_list_entry", target_id=str(entry_id),
    )
    await db.commit()


# ----------------------- Community lists -----------------------

_COMMUNITY_LIST_NOT_FOUND = "community list not found"
_COMMUNITY_LIST_ENTRY_NOT_FOUND = "community list entry not found"


@router.get("/community-lists", response_model=Page[CommunityListOut])
async def list_community_lists(
    params: PageParams = Depends(PageParams.from_query),
    kind: CommunityKind | None = Query(None),
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(CommunityList)
    if kind is not None:
        stmt = stmt.where(CommunityList.kind == kind)
    return await paginate(
        db, stmt, model=CommunityList, params=params, out_model=CommunityListOut,
    )


@router.post("/community-lists", response_model=CommunityListOut, status_code=201)
async def create_community_list(
    payload: CommunityListCreate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = CommunityList(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="community_list.create",
        target_type="community_list", target_id=str(obj.id),
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/community-lists/{list_id}", response_model=CommunityListOut)
async def update_community_list(
    list_id: UUID,
    payload: CommunityListUpdate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(CommunityList, list_id)
    if obj is None:
        raise NotFoundError(_COMMUNITY_LIST_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="community_list.update",
        target_type="community_list", target_id=str(list_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/community-lists/{list_id}", status_code=204)
async def delete_community_list(
    list_id: UUID,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(CommunityList, list_id)
    if obj is None:
        raise NotFoundError(_COMMUNITY_LIST_NOT_FOUND)
    in_use = (
        await db.execute(
            select(CommunityListEntry.id).where(
                CommunityListEntry.community_list_id == list_id,
            ).limit(1),
        )
    ).scalar_one_or_none()
    if in_use is not None:
        raise ConflictError("community list still has entries; remove them first")
    await db.execute(delete(CommunityList).where(CommunityList.id == list_id))
    await audit.record(
        db, principal, action="community_list.delete",
        target_type="community_list", target_id=str(list_id),
    )
    await db.commit()


@router.get("/community-list-entries", response_model=Page[CommunityListEntryOut])
async def list_community_list_entries(
    params: PageParams = Depends(PageParams.from_query),
    community_list_id: UUID | None = Query(None),
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(CommunityListEntry)
    if community_list_id is not None:
        stmt = stmt.where(CommunityListEntry.community_list_id == community_list_id)
    return await paginate(
        db, stmt, model=CommunityListEntry, params=params, out_model=CommunityListEntryOut,
    )


@router.post("/community-list-entries", response_model=CommunityListEntryOut, status_code=201)
async def create_community_list_entry(
    payload: CommunityListEntryCreate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    parent = await db.get(CommunityList, payload.community_list_id)
    if parent is None:
        raise NotFoundError(_COMMUNITY_LIST_NOT_FOUND)
    obj = CommunityListEntry(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="community_list_entry.create",
        target_type="community_list_entry", target_id=str(obj.id),
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/community-list-entries/{entry_id}", response_model=CommunityListEntryOut)
async def update_community_list_entry(
    entry_id: UUID,
    payload: CommunityListEntryUpdate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(CommunityListEntry, entry_id)
    if obj is None:
        raise NotFoundError(_COMMUNITY_LIST_ENTRY_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="community_list_entry.update",
        target_type="community_list_entry", target_id=str(entry_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/community-list-entries/{entry_id}", status_code=204)
async def delete_community_list_entry(
    entry_id: UUID,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(CommunityListEntry, entry_id)
    if obj is None:
        raise NotFoundError(_COMMUNITY_LIST_ENTRY_NOT_FOUND)
    await db.execute(delete(CommunityListEntry).where(CommunityListEntry.id == entry_id))
    await audit.record(
        db, principal, action="community_list_entry.delete",
        target_type="community_list_entry", target_id=str(entry_id),
    )
    await db.commit()


# ----------------------- Route maps -----------------------

_ROUTE_MAP_NOT_FOUND = "route map not found"
_ROUTE_MAP_ENTRY_NOT_FOUND = "route map entry not found"


@router.get("/route-maps", response_model=Page[RouteMapOut])
async def list_route_maps(
    params: PageParams = Depends(PageParams.from_query),
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    return await paginate(
        db, select(RouteMap), model=RouteMap, params=params, out_model=RouteMapOut,
    )


@router.post("/route-maps", response_model=RouteMapOut, status_code=201)
async def create_route_map(
    payload: RouteMapCreate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = RouteMap(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="route_map.create",
        target_type="route_map", target_id=str(obj.id),
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/route-maps/{map_id}", response_model=RouteMapOut)
async def update_route_map(
    map_id: UUID,
    payload: RouteMapUpdate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(RouteMap, map_id)
    if obj is None:
        raise NotFoundError(_ROUTE_MAP_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="route_map.update",
        target_type="route_map", target_id=str(map_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/route-maps/{map_id}", status_code=204)
async def delete_route_map(
    map_id: UUID,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(RouteMap, map_id)
    if obj is None:
        raise NotFoundError(_ROUTE_MAP_NOT_FOUND)
    in_use = (
        await db.execute(
            select(RouteMapEntry.id).where(RouteMapEntry.route_map_id == map_id).limit(1),
        )
    ).scalar_one_or_none()
    if in_use is not None:
        raise ConflictError("route map still has entries; remove them first")
    await db.execute(delete(RouteMap).where(RouteMap.id == map_id))
    await audit.record(
        db, principal, action="route_map.delete",
        target_type="route_map", target_id=str(map_id),
    )
    await db.commit()


@router.get("/route-map-entries", response_model=Page[RouteMapEntryOut])
async def list_route_map_entries(
    params: PageParams = Depends(PageParams.from_query),
    route_map_id: UUID | None = Query(None),
    _: Principal = Depends(require_capability(INVENTORY_READ)),
    db: AsyncSession = Depends(get_db),
):
    stmt = select(RouteMapEntry)
    if route_map_id is not None:
        stmt = stmt.where(RouteMapEntry.route_map_id == route_map_id)
    return await paginate(
        db, stmt, model=RouteMapEntry, params=params, out_model=RouteMapEntryOut,
    )


@router.post("/route-map-entries", response_model=RouteMapEntryOut, status_code=201)
async def create_route_map_entry(
    payload: RouteMapEntryCreate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    parent = await db.get(RouteMap, payload.route_map_id)
    if parent is None:
        raise NotFoundError(_ROUTE_MAP_NOT_FOUND)
    # Cross-check the match-side FKs so 404 lands at the create site
    # instead of a constraint violation deep in commit.
    if payload.match_prefix_list_id is not None:
        if await db.get(PrefixList, payload.match_prefix_list_id) is None:
            raise NotFoundError(_PREFIX_LIST_NOT_FOUND)
    if payload.match_community_list_id is not None:
        if await db.get(CommunityList, payload.match_community_list_id) is None:
            raise NotFoundError(_COMMUNITY_LIST_NOT_FOUND)
    obj = RouteMapEntry(**payload.model_dump())
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="route_map_entry.create",
        target_type="route_map_entry", target_id=str(obj.id),
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.patch("/route-map-entries/{entry_id}", response_model=RouteMapEntryOut)
async def update_route_map_entry(
    entry_id: UUID,
    payload: RouteMapEntryUpdate,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(RouteMapEntry, entry_id)
    if obj is None:
        raise NotFoundError(_ROUTE_MAP_ENTRY_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="route_map_entry.update",
        target_type="route_map_entry", target_id=str(entry_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj


@router.delete("/route-map-entries/{entry_id}", status_code=204)
async def delete_route_map_entry(
    entry_id: UUID,
    principal: Principal = Depends(require_capability(INVENTORY_WRITE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(RouteMapEntry, entry_id)
    if obj is None:
        raise NotFoundError(_ROUTE_MAP_ENTRY_NOT_FOUND)
    await db.execute(delete(RouteMapEntry).where(RouteMapEntry.id == entry_id))
    await audit.record(
        db, principal, action="route_map_entry.delete",
        target_type="route_map_entry", target_id=str(entry_id),
    )
    await db.commit()
