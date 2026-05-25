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

from ..models.auth import OidcRoleMapping, Role, RoleScope, ScopeType, User, UserRole
from ..models.inventory import Region, Site, SiteGroup, SiteGroupMembership
from ..models.organization import Organization


@dataclass(frozen=True)
class Scope:
    """Per-capability set of allowed targets. Empty set on any dimension means 'unrestricted on it'."""

    is_global: bool = False
    region_ids: frozenset[UUID] = field(default_factory=frozenset)
    site_ids: frozenset[UUID] = field(default_factory=frozenset)
    site_group_ids: frozenset[UUID] = field(default_factory=frozenset)
    enclaves: frozenset[str] = field(default_factory=frozenset)
    # Legacy free-form organization string. PR 69 introduces the FK-keyed
    # twin `organization_ids` below; both are checked in site_matches_scope
    # so existing string-keyed bindings keep working while new bindings
    # can target an organizations.id UUID directly. Retire after the
    # sites.organization string column is dropped.
    organizations: frozenset[str] = field(default_factory=frozenset)
    organization_ids: frozenset[UUID] = field(default_factory=frozenset)
    fabric_ids: frozenset[UUID] = field(default_factory=frozenset)
    # PR 61 — classification scope dimension. Restricts a principal to
    # resources tagged with a matching classification string ("unclassified",
    # "secret", etc.). Enforcement on the Python side is currently advisory
    # only; the Go service (otter-go) is the authoritative gate.
    classifications: frozenset[str] = field(default_factory=frozenset)

    def union(self, other: Scope) -> Scope:
        return Scope(
            is_global=self.is_global or other.is_global,
            region_ids=self.region_ids | other.region_ids,
            site_ids=self.site_ids | other.site_ids,
            site_group_ids=self.site_group_ids | other.site_group_ids,
            enclaves=self.enclaves | other.enclaves,
            organizations=self.organizations | other.organizations,
            organization_ids=self.organization_ids | other.organization_ids,
            fabric_ids=self.fabric_ids | other.fabric_ids,
            classifications=self.classifications | other.classifications,
        )


@dataclass
class ScopeMatch:
    allowed: bool
    reason: str = ""


async def scope_for_user(db: AsyncSession, user: User) -> dict[str, Scope]:
    """Return capability_code -> Scope for a user, unioned across role assignments."""

    rows = (
        await db.execute(
            select(UserRole).where(UserRole.user_id == user.id).options()
        )
    ).scalars().all()

    out: dict[str, Scope] = {}
    for assignment in rows:
        role = await db.get(UserRole, assignment.id)
        if role is None or role.role_id is None:
            continue
        role_obj = await db.get(Role, role.role_id)
        if role_obj is None:
            continue

        scope = await _scope_from_assignment(db, assignment.id)
        for cap in role_obj.permission_codes:
            out[cap] = out.get(cap, Scope()).union(scope)
    return out


async def _resolve_mapping_scope(
    db: AsyncSession, dimension: ScopeType | None, target: str | None,
) -> Scope | None:
    """Build a Scope from an OidcRoleMapping's (dimension, target) pair.

    Returns None when the mapping is bound to a dimension+target but
    the target can't be resolved (deleted region, typo'd code, etc.) —
    fail-closed: the cap will not be granted on this login.
    """
    if dimension is None or not target:
        return Scope(is_global=True)
    if dimension is ScopeType.region:
        row = (await db.execute(select(Region.id).where(Region.code == target))).scalar_one_or_none()
        return Scope(region_ids=frozenset([row])) if row else None
    if dimension is ScopeType.site:
        row = (await db.execute(select(Site.id).where(Site.code == target))).scalar_one_or_none()
        return Scope(site_ids=frozenset([row])) if row else None
    if dimension is ScopeType.site_group:
        # SiteGroup has no `code` column; match by `name`.
        row = (await db.execute(select(SiteGroup.id).where(SiteGroup.name == target))).scalar_one_or_none()
        return Scope(site_group_ids=frozenset([row])) if row else None
    if dimension is ScopeType.enclave:
        return Scope(enclaves=frozenset([target]))
    if dimension is ScopeType.organization:
        # PR 69 — prefer FK binding. Try the target as an
        # organizations.id UUID first; fall back to organizations.name
        # lookup; finally fall back to legacy string-keyed scope
        # against sites.organization. Strict UUID parse → name lookup
        # → string match means existing IdP role mappings that store
        # an org name as scope_target keep working unchanged.
        try:
            org_uuid = UUID(target)
        except (TypeError, ValueError):
            org_uuid = None
        if org_uuid is not None:
            row = (await db.execute(
                select(Organization.id).where(Organization.id == org_uuid)
            )).scalar_one_or_none()
            if row is not None:
                return Scope(organization_ids=frozenset([row]))
            return None
        row = (await db.execute(
            select(Organization.id).where(Organization.name == target)
        )).scalar_one_or_none()
        if row is not None:
            return Scope(organization_ids=frozenset([row]))
        return Scope(organizations=frozenset([target]))
    if dimension is ScopeType.classification:
        return Scope(classifications=frozenset([target]))
    if dimension is ScopeType.fabric:
        # Local import to dodge a cycle: ipam.Fabric -> base -> security.
        from ..models.ipam import Fabric
        row = (await db.execute(select(Fabric.id).where(Fabric.slug == target))).scalar_one_or_none()
        return Scope(fabric_ids=frozenset([row])) if row else None
    if dimension is ScopeType.global_:
        return Scope(is_global=True)
    return None


async def caps_from_idp_roles(
    db: AsyncSession, idp_roles: Iterable[str],
) -> dict[str, Scope]:
    """Materialize capabilities for IdP-asserted role names.

    Each `oidc_role_mappings` row maps an IdP role to a DCIM Role with
    an optional ABAC scope binding (scope_dimension + scope_target).
    Mappings without a scope grant the cap globally; mappings with one
    constrain the cap to that target (region code, site code, etc.).

    Returns an empty dict if `idp_roles` is empty or no mapping matches.
    A mapping whose scope_target can't be resolved is silently skipped
    — fail-closed: the cap is not granted on that login.
    """
    names = [r for r in idp_roles if isinstance(r, str) and r]
    if not names:
        return {}
    mappings = (
        await db.execute(
            select(OidcRoleMapping).where(OidcRoleMapping.idp_role.in_(names))
        )
    ).scalars().all()
    if not mappings:
        return {}
    out: dict[str, Scope] = {}
    for mapping in mappings:
        role = await db.get(Role, mapping.dcim_role_id)
        if role is None:
            continue
        scope = await _resolve_mapping_scope(
            db, mapping.scope_dimension, mapping.scope_target,
        )
        if scope is None:
            continue
        for cap in role.permission_codes:
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
    org_ids: set[UUID] = set()
    fabric_ids: set[UUID] = set()
    classifications: set[str] = set()
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
                # PR 69 — target_id parses as UUID -> FK binding into
                # organization_ids; otherwise fall back to legacy
                # string match in organizations (against sites.organization).
                if r.target_id:
                    try:
                        org_ids.add(UUID(r.target_id))
                    except ValueError:
                        orgs.add(r.target_id)
            case ScopeType.fabric:
                if r.target_id:
                    fabric_ids.add(UUID(r.target_id))
            case ScopeType.classification:
                if r.target_id:
                    classifications.add(r.target_id)
    return Scope(
        is_global=is_global,
        region_ids=frozenset(region_ids),
        site_ids=frozenset(site_ids),
        site_group_ids=frozenset(group_ids),
        enclaves=frozenset(enclaves),
        organizations=frozenset(orgs),
        organization_ids=frozenset(org_ids),
        fabric_ids=frozenset(fabric_ids),
        classifications=frozenset(classifications),
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
    # PR 69 + PR 92 — FK-keyed organization scope check. The legacy
    # `site.organization in scope.organizations` string match was
    # removed when sites.organization (the column) was dropped. Bindings
    # in scope.organizations are now inert; operators migrate them to
    # organization_ids before relying on the dimension.
    if site.organization_id and site.organization_id in scope.organization_ids:
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


def fabric_matches_scope(scope: Scope, fabric_id: UUID) -> bool:
    """Whether a fabric is reachable under `scope`. Pure: no DB query.
    Fabric is a leaf dimension (no hierarchy expansion like Site has
    region/group), so the check is just `fabric_id in scope.fabric_ids`."""
    if scope.is_global:
        return True
    return fabric_id in scope.fabric_ids


async def scope_filtered_fabric_ids(
    db: AsyncSession,
    capabilities: dict[str, Scope],
    cap_code: str,
) -> set[UUID] | None:
    """Fabric equivalent of scope_filtered_site_ids: returns None for
    globally-scoped principals, otherwise the explicit set of in-scope
    fabric UUIDs. List endpoints push this into a `.in_(...)` filter."""
    from .deps import find_matching_capability

    scope = find_matching_capability(capabilities, cap_code)
    if scope is None or scope.is_global:
        return None
    return set(scope.fabric_ids)


async def enforce_fabric_scope(
    db: AsyncSession,
    capabilities: dict[str, Scope],
    fabric_id: UUID | None,
    cap_code: str,
) -> None:
    """Single-resource fabric-ABAC check. Raises ForbiddenError when
    the principal's cap is fabric-scoped and `fabric_id` isn't in scope."""
    if fabric_id is None:
        return
    from .deps import find_matching_capability

    scope = find_matching_capability(capabilities, cap_code)
    if scope is None or scope.is_global:
        return
    if not fabric_matches_scope(scope, fabric_id):
        from ..errors import ForbiddenError
        raise ForbiddenError("resource is outside your fabric scope")


async def enforce_site_scope(
    db: AsyncSession,
    capabilities: dict[str, Scope],
    site_id: UUID | None,
    cap_code: str,
) -> None:
    """Single-resource ABAC: raise ForbiddenError if the principal holds
    `cap_code` but `site_id` is outside their scope. Global / unscoped
    grants and resources with no site (`site_id=None`) pass."""
    if site_id is None:
        return
    # Local import to keep the security package free of api/* dependencies.
    from .deps import find_matching_capability

    scope = find_matching_capability(capabilities, cap_code)
    if scope is None or scope.is_global:
        return
    if not await site_matches_scope(db, scope, site_id):
        from ..errors import ForbiddenError
        raise ForbiddenError("resource is outside your scope")


async def scope_filtered_site_ids(
    db: AsyncSession,
    capabilities: dict[str, Scope],
    cap_code: str,
) -> set[UUID] | None:
    """Return the set of site UUIDs in scope for `cap_code`, or None if
    the principal has global scope (no filter needed).

    Use in list endpoints to push the ABAC filter into SQL:

        in_scope = await scope_filtered_site_ids(db, principal.capabilities, "inventory:racks:read")
        stmt = select(Rack)
        if in_scope is not None:
            stmt = stmt.where(Rack.site_id.in_(in_scope))
        page = await paginate(db, stmt, ...)

    Returns:
      None       — principal has the cap with global scope; caller skips filter.
      empty set  — principal has the cap but no site matches; caller should
                   return an empty page (filter would short-circuit to false).
      non-empty  — restrict the query to these site IDs.
    """
    # Local import to avoid a circular dependency at module load.
    from .deps import find_matching_capability

    scope = find_matching_capability(capabilities, cap_code)
    if scope is None or scope.is_global:
        return None
    all_site_ids = {
        row[0]
        for row in (await db.execute(select(Site.id))).all()
    }
    return await filter_sites_in_scope(db, scope, all_site_ids)
