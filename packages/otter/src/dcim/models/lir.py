"""LIR (Local Internet Registry) — pools, requests, allocations.

DoW NIC carves sub-allocations out of registered ARIN aggregates and
hands them to internal organizations. The flow:

  tenant user submits LirRequest
  → NIC approves / rejects via the LIR module
  → on approve, allocation engine finds a free range inside a pool
    Supernet, creates a tenant-owned Supernet in the landing fabric
    `lir-unassigned`, and writes a LirAllocation record
  → tenant later moves the tenant Supernet from the landing fabric to
    their operational fabric via the IPAM module's move endpoint
  → async worker pushes a Reg-RWS reassignment to ARIN; status lives
    on the allocation row

A pool source Supernet (`Supernet.lir_pool_id` set) and a tenant
Supernet (`Supernet.owner_organization_id` set) are mutually
exclusive — DB-level CHECK lives in migration 0065. The link from
pool side to tenant side is a `LirAllocation` row; the two Supernet
trees stay independent so the IPAM 'move' endpoint can relocate a
tenant Supernet without rewriting parent FKs.
"""

from __future__ import annotations

import enum
from datetime import datetime
from uuid import UUID

from sqlalchemy import (
    Boolean,
    DateTime,
    ForeignKey,
    Index,
    Integer,
    SmallInteger,
    String,
    Text,
    UniqueConstraint,
    func,
)
from sqlalchemy.dialects.postgresql import CIDR
from sqlalchemy.dialects.postgresql import UUID as PgUUID
from sqlalchemy.orm import Mapped, mapped_column

from ..db import Base
from ._mixins import Timestamped, UUIDPrimaryKey


class LirRequestStatus(str, enum.Enum):
    """Lifecycle of a tenant's request for IP space.

    pending_approval is the only state the requester can `cancel`
    from. approved triggers allocation creation; rejected/cancelled
    are terminal with no allocation. failed is the safety valve when
    approval ran but no free range could be carved — NIC can
    redirect to a different pool and retry without re-soliciting the
    tenant.
    """

    pending_approval = "pending_approval"
    approved = "approved"
    rejected = "rejected"
    cancelled = "cancelled"
    failed = "failed"


class LirAllocationStatus(str, enum.Enum):
    """Lifecycle of an issued allocation.

    return_requested = tenant asked to give it back; NIC has not yet
    confirmed. returned = NIC confirmed; the tenant Supernet is
    expected to be detached/deleted out-of-band before ARIN
    deassignment runs.
    """

    active = "active"
    return_requested = "return_requested"
    returned = "returned"


class LirArinStatus(str, enum.Enum):
    """Reg-RWS feed-up state.

    `none` covers two cases: the pool has no `arin_parent_net_handle`
    (LIR-internal only) and the deployment-wide `arin.regrws.enabled`
    setting is off. The worker only acts on `pending` / `failed` /
    `removing` rows (see ix_lir_allocations_arin_worker partial
    index).
    """

    none = "none"
    pending = "pending"
    registered = "registered"
    failed = "failed"
    removing = "removing"
    removed = "removed"


class LirPool(UUIDPrimaryKey, Timestamped, Base):
    """A named bucket of supernet space DoW will sub-allocate from.

    Pinned to a single IP family — v4 and v6 pools never share a row
    because the allocation algorithm and the ARIN parent handle differ
    by family. `fabric_id` records where the pool supernets live
    operationally; it's informational here since allocation always
    lands in the system landing fabric and the tenant relocates from
    there.

    `arin_parent_net_handle` is the ARIN net handle a successful
    approval POSTs reassignments under (e.g. 'NET-198-51-100-0-1').
    NULL means the pool is LIR-internal — local approvals only, no
    ARIN feed-up. The worker treats `arin_status='none'` and a NULL
    handle the same way: do nothing.
    """

    __tablename__ = "lir_pools"
    __table_args__ = (
        UniqueConstraint("name", name="uq_lir_pool_name"),
        UniqueConstraint("slug", name="uq_lir_pool_slug"),
        Index("ix_lir_pools_family", "ip_family"),
        Index("ix_lir_pools_enabled", "enabled"),
    )

    name: Mapped[str] = mapped_column(String(128), nullable=False)
    slug: Mapped[str] = mapped_column(String(64), nullable=False)
    description: Mapped[str | None] = mapped_column(String(512))
    ip_family: Mapped[int] = mapped_column(SmallInteger, nullable=False)
    fabric_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("fabrics.id"),
    )
    classification: Mapped[str | None] = mapped_column(String(32))
    # Smallest (broadest) prefix length the pool will issue — e.g.
    # 24 means "no allocation can be wider than /24". Together with
    # max_prefix_length bounds the carve sizes a single pool services.
    min_prefix_length: Mapped[int] = mapped_column(SmallInteger, nullable=False)
    # Largest (narrowest) prefix length the pool will issue.
    max_prefix_length: Mapped[int] = mapped_column(SmallInteger, nullable=False)
    default_supernet_purpose: Mapped[str | None] = mapped_column(String(32))
    arin_parent_net_handle: Mapped[str | None] = mapped_column(String(64))
    enabled: Mapped[bool] = mapped_column(Boolean, default=True, nullable=False)


class LirRequest(UUIDPrimaryKey, Timestamped, Base):
    """Tenant-submitted request for IP space.

    `organization_id` is the tenant the allocation will belong to.
    `requester_user_id` is the natural person who filed it. The
    submission flow rejects an org_id the requester's
    `lir:requests:create` capability doesn't cover (via
    `Scope.organization_ids`), so the FK pair is checked against ABAC
    at the API layer.

    `pool_id` is the tenant's preference (or null = "any matching
    pool"); `approved_pool_id` is what the NIC actually approved
    into. When they differ the audit trail captures why via
    `decision_notes`.

    Allocation creation is 1:1 with approval — `LirAllocation` rows
    point back here via `request_id`. The request-side does not carry
    an allocation FK to avoid a bidirectional cycle.
    """

    __tablename__ = "lir_requests"
    __table_args__ = (
        Index("ix_lir_requests_org", "organization_id"),
        Index("ix_lir_requests_requester", "requester_user_id"),
        Index("ix_lir_requests_status", "status"),
        Index("ix_lir_requests_submitted_at", "submitted_at"),
    )

    organization_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("organizations.id"), nullable=False,
    )
    requester_user_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("users.id"), nullable=False,
    )
    pool_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("lir_pools.id"),
    )
    site_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("sites.id"),
    )
    ip_family: Mapped[int] = mapped_column(SmallInteger, nullable=False)
    prefix_length: Mapped[int] = mapped_column(SmallInteger, nullable=False)
    purpose: Mapped[str | None] = mapped_column(String(32))
    classification: Mapped[str | None] = mapped_column(String(32))
    justification: Mapped[str] = mapped_column(Text, nullable=False)
    # Status is a free-form VARCHAR at the DB level (CHECK constraint
    # in migration 0065 pins the set). Stored as the enum's value
    # string; the API layer round-trips through LirRequestStatus.
    status: Mapped[str] = mapped_column(
        String(32),
        default=LirRequestStatus.pending_approval.value,
        nullable=False,
    )
    submitted_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), server_default=func.now(), nullable=False,
    )
    decided_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    decided_by_user_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("users.id"),
    )
    decision_notes: Mapped[str | None] = mapped_column(String(2048))
    approved_pool_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("lir_pools.id"),
    )


class LirAllocation(UUIDPrimaryKey, Timestamped, Base):
    """An issued allocation tied 1:1 to an approved LirRequest.

    `pool_supernet_id` records which pool supernet got carved (so the
    allocation engine can reconstruct used ranges per pool supernet
    without walking the tenant Supernet tree). `tenant_supernet_id`
    points at the tenant-owned Supernet row created at approve time;
    it starts life in the landing fabric `lir-unassigned` and the
    tenant moves it later via the IPAM module.

    `prefix` denormalizes the carved range so the allocation list can
    show the CIDR without a join. It should equal
    `Supernet.prefix WHERE id = tenant_supernet_id` — kept in sync at
    write time only (no trigger).

    ARIN columns are the worker's contract: it reads
    `arin_status IN ('pending', 'failed', 'removing')` to find work,
    bumps `arin_attempts`, and writes back `arin_status`,
    `arin_net_handle`, `arin_last_attempt_at`, `arin_last_error`.
    """

    __tablename__ = "lir_allocations"
    __table_args__ = (
        UniqueConstraint("request_id", name="uq_lir_allocation_request"),
        UniqueConstraint(
            "tenant_supernet_id", name="uq_lir_allocation_tenant_supernet",
        ),
        Index("ix_lir_allocations_org", "organization_id"),
        Index("ix_lir_allocations_pool", "pool_id"),
        Index("ix_lir_allocations_pool_supernet", "pool_supernet_id"),
        Index("ix_lir_allocations_status", "status"),
    )

    request_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("lir_requests.id"), nullable=False,
    )
    organization_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("organizations.id"), nullable=False,
    )
    pool_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("lir_pools.id"), nullable=False,
    )
    pool_supernet_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("supernets.id"), nullable=False,
    )
    tenant_supernet_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("supernets.id"), nullable=False,
    )
    prefix: Mapped[str] = mapped_column(CIDR, nullable=False)
    allocated_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), server_default=func.now(), nullable=False,
    )
    allocated_by_user_id: Mapped[UUID] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("users.id"), nullable=False,
    )
    status: Mapped[str] = mapped_column(
        String(32), default=LirAllocationStatus.active.value, nullable=False,
    )
    return_requested_at: Mapped[datetime | None] = mapped_column(
        DateTime(timezone=True),
    )
    return_requested_by_user_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("users.id"),
    )
    return_reason: Mapped[str | None] = mapped_column(String(2048))
    returned_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    returned_by_user_id: Mapped[UUID | None] = mapped_column(
        PgUUID(as_uuid=True), ForeignKey("users.id"),
    )
    arin_status: Mapped[str] = mapped_column(
        String(32), default=LirArinStatus.none.value, nullable=False,
    )
    arin_net_handle: Mapped[str | None] = mapped_column(String(64))
    arin_last_attempt_at: Mapped[datetime | None] = mapped_column(
        DateTime(timezone=True),
    )
    arin_last_error: Mapped[str | None] = mapped_column(String(2048))
    arin_attempts: Mapped[int] = mapped_column(
        Integer, default=0, nullable=False,
    )
