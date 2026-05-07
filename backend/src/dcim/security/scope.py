"""Authorization scope evaluation.

A Principal has a set of capabilities (e.g. "inventory:read", "power:control") and
a Scope that bounds *where* those capabilities apply: by region, site, site-group,
enclave, or organization. `inventory:read` granted at scope `region=R` means the
user can read inventory for any site whose region_id is R.

Scopes compose by union — a user with multiple role assignments gets the union of
their scopes per capability.
"""

from __future__ import annotations

from collections.abc import Iterable
from dataclasses import dataclass, field
from uuid import UUID

from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from ..models.auth import RoleScope, ScopeType, User, UserRole
from ..models.inventory import Site, SiteGroupMembership


@dataclass(frozen=True)
class Scope:
    """Per-capability set of allowed targets. Empty set on any dimension means 'unrestricted on it'."""

    is_global: bool = False
    region_ids: frozenset[UUID] = field(default_factory=frozenset)
    site_ids: frozenset[UUID] = field(default_factory=frozenset)
    site_group_ids: frozenset[UUID] = field(default_factory=frozenset)
    enclaves: frozenset[str] = field(default_factory=frozenset)
    organizations: frozenset[str] = field(default_factory=frozenset)

    def union(self, other: Scope) -> Scope:
        return Scope(
            is_global=self.is_global or other.is_global,
            region_ids=self.region_ids | other.region_ids,
            site_ids=self.site_ids | other.site_ids,
            site_group_ids=self.site_group_ids | other.site_group_ids,
            enclaves=self.enclaves | other.enclaves,
            organizations=self.organizations | other.organizations,
        )


@dataclass
class ScopeMatch:
    allowed: bool
    reason: str = ""


async def scope_for_user(db: AsyncSession, user: User) -> dict[str, Scope]:
    """Return capability_code -> Scope for a user, unioned across role assignments."""

    rows = (
        await db.execute(
            select(UserRole).where(UserRole.user_id == user.id).options()  # noqa: PIE790
        )
    ).scalars().all()

    out: dict[str, Scope] = {}
    for assignment in rows:
        role = await db.get(UserRole, assignment.id)
        if role is None or role.role_id is None:
            continue
        # Load role capabilities
        from ..models.auth import Role  # local import to avoid cycle at module import time
        role_obj = await db.get(Role, role.role_id)
        if role_obj is None:
            continue

        scope = await _scope_from_assignment(db, assignment.id)
        for cap in role_obj.permission_codes:
            out[cap] = out.get(cap, Scope()).union(scope)
    return out


async def _scope_from_assignment(db: AsyncSession, assignment_id: UUID) -> Scope:
    rows = (
        await db.execute(select(RoleScope).where(RoleScope.assignment_id == assignment_id))
    ).scalars().all()

    if not rows:
        # No restrictions = the role's natural scope. Treat as global; restrict by stacking RoleScope rows.
        return Scope(is_global=True)

    region_ids: set[UUID] = set()
    site_ids: set[UUID] = set()
    group_ids: set[UUID] = set()
    enclaves: set[str] = set()
    orgs: set[str] = set()
    is_global = False
    for r in rows:
        match r.scope_type:
            case ScopeType.global_:
                is_global = True
            case ScopeType.region:
                if r.target_id:
                    region_ids.add(UUID(r.target_id))
            case ScopeType.site:
                if r.target_id:
                    site_ids.add(UUID(r.target_id))
            case ScopeType.site_group:
                if r.target_id:
                    group_ids.add(UUID(r.target_id))
            case ScopeType.enclave:
                if r.target_id:
                    enclaves.add(r.target_id)
            case ScopeType.organization:
                if r.target_id:
                    orgs.add(r.target_id)
    return Scope(
        is_global=is_global,
        region_ids=frozenset(region_ids),
        site_ids=frozenset(site_ids),
        site_group_ids=frozenset(group_ids),
        enclaves=frozenset(enclaves),
        organizations=frozenset(orgs),
    )


async def site_matches_scope(db: AsyncSession, scope: Scope, site_id: UUID) -> bool:
    """Resolve whether a given site_id falls inside a Scope, expanding region/group/enclave dimensions."""

    if scope.is_global:
        return True
    if site_id in scope.site_ids:
        return True
    site = await db.get(Site, site_id)
    if site is None:
        return False
    if site.region_id in scope.region_ids:
        return True
    if site.enclave and site.enclave in scope.enclaves:
        return True
    if site.organization and site.organization in scope.organizations:
        return True
    if scope.site_group_ids:
        memberships = (
            await db.execute(
                select(SiteGroupMembership.group_id).where(SiteGroupMembership.site_id == site_id)
            )
        ).scalars().all()
        if scope.site_group_ids.intersection(memberships):
            return True
    return False


async def filter_sites_in_scope(db: AsyncSession, scope: Scope, candidate_ids: Iterable[UUID]) -> set[UUID]:
    """Return the subset of candidate site IDs visible under the given scope."""

    candidates = list(candidate_ids)
    if scope.is_global or not candidates:
        return set(candidates) if scope.is_global else set()
    out: set[UUID] = set()
    for sid in candidates:
        if await site_matches_scope(db, scope, sid):
            out.add(sid)
    return out
