"""Admin CRUD: users, roles, role assignments + scopes, OIDC mappings.

These endpoints back the Admin settings UI. Each route is gated on a
specific granular capability under `admin:<resource>:<action>` — e.g.
`admin:users:create`, `admin:roles:update`, `admin:oidc-mappings:read`.
"""

from __future__ import annotations

from uuid import UUID

from fastapi import APIRouter, Depends
from sqlalchemy import delete, func, select
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..errors import ConflictError, NotFoundError, ValidationError
from ..models.auth import OidcRoleMapping, Role, RoleScope, ScopeType, User, UserRole
from ..schemas.auth import (
    AssignmentCreate,
    AssignmentOut,
    CapabilityCatalogOut,
    OidcRoleMappingCreate,
    OidcRoleMappingOut,
    OidcRoleMappingUpdate,
    RoleCreate,
    RoleOut,
    RoleUpdate,
    ScopeRowOut,
    UserCreate,
    UserOut,
    UserUpdate,
)
from ..schemas.common import Page, PageParams
from ..security import audit
from ..security.capabilities import CAPABILITY_CATALOG, SPECIALTY_CAPABILITIES
from ..security.deps import Principal, find_matching_capability, require_capability
from ._pagination import paginate

router = APIRouter(prefix="/admin", tags=["admin"])

_USER_NOT_FOUND = "user not found"
_ROLE_NOT_FOUND = "role not found"
_ASSIGNMENT_NOT_FOUND = "role assignment not found"
_OIDC_MAPPING_NOT_FOUND = "oidc role mapping not found"

# ----------------------- Users -----------------------
@router.get("/users", response_model=Page[UserOut])
async def list_users(
    params: PageParams = Depends(PageParams.from_query),
    _: Principal = Depends(require_capability("admin:users:read")),
    db: AsyncSession = Depends(get_db),
):
    return await paginate(db, select(User), model=User, params=params, out_model=UserOut)

@router.post("/users", response_model=UserOut, status_code=201)
async def create_user(
    payload: UserCreate,
    principal: Principal = Depends(require_capability("admin:users:create")),
    db: AsyncSession = Depends(get_db),
):
    existing = (await db.execute(select(User).where(User.email == payload.email))).scalar_one_or_none()
    if existing is not None:
        raise ConflictError("a user with that email already exists")
    obj = User(
        email=payload.email,
        display_name=payload.display_name,
        is_active=payload.is_active,
    )
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="user.create", target_type="user", target_id=str(obj.id),
    )
    await db.commit()
    await db.refresh(obj)
    return obj

@router.patch("/users/{user_id}", response_model=UserOut)
async def update_user(
    user_id: UUID,
    payload: UserUpdate,
    principal: Principal = Depends(require_capability("admin:users:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(User, user_id)
    if obj is None:
        raise NotFoundError(_USER_NOT_FOUND)
    diff = payload.model_dump(exclude_unset=True)
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="user.update", target_type="user",
        target_id=str(user_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj

# ----------------------- Roles -----------------------
@router.get("/roles", response_model=Page[RoleOut])
async def list_roles(
    params: PageParams = Depends(PageParams.from_query),
    _: Principal = Depends(require_capability("admin:roles:read")),
    db: AsyncSession = Depends(get_db),
):
    return await paginate(db, select(Role), model=Role, params=params, out_model=RoleOut)

@router.post("/roles", response_model=RoleOut, status_code=201)
async def create_role(
    payload: RoleCreate,
    principal: Principal = Depends(require_capability("admin:roles:create")),
    db: AsyncSession = Depends(get_db),
):
    existing = (await db.execute(select(Role).where(Role.name == payload.name))).scalar_one_or_none()
    if existing is not None:
        raise ConflictError("a role with that name already exists")
    # No-escalation check, wildcard-aware: a code is grantable if the
    # principal has a matching capability (exact, resource-wildcard,
    # domain-wildcard, or `*`).
    extra = sorted(
        c for c in payload.permission_codes
        if find_matching_capability(principal.capabilities, c) is None
    )
    if extra:
        raise ValidationError(
            f"cannot grant capabilities you don't hold: {extra}",
            details={"missing": extra},
        )
    obj = Role(
        name=payload.name,
        description=payload.description,
        permission_codes=payload.permission_codes,
        is_system=False,
    )
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="role.create", target_type="role", target_id=str(obj.id),
    )
    await db.commit()
    await db.refresh(obj)
    return obj

@router.patch("/roles/{role_id}", response_model=RoleOut)
async def update_role(
    role_id: UUID,
    payload: RoleUpdate,
    principal: Principal = Depends(require_capability("admin:roles:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Role, role_id)
    if obj is None:
        raise NotFoundError(_ROLE_NOT_FOUND)
    if obj.is_system:
        raise ValidationError("system roles are read-only")
    diff = payload.model_dump(exclude_unset=True)
    if "permission_codes" in diff:
        extra = sorted(
            c for c in diff["permission_codes"]
            if find_matching_capability(principal.capabilities, c) is None
        )
        if extra:
            raise ValidationError(
                f"cannot grant capabilities you don't hold: {extra}",
                details={"missing": extra},
            )
    for k, v in diff.items():
        setattr(obj, k, v)
    await audit.record(
        db, principal, action="role.update", target_type="role",
        target_id=str(role_id), diff=diff,
    )
    await db.commit()
    await db.refresh(obj)
    return obj

@router.delete("/roles/{role_id}", status_code=204)
async def delete_role(
    role_id: UUID,
    principal: Principal = Depends(require_capability("admin:roles:delete")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Role, role_id)
    if obj is None:
        raise NotFoundError(_ROLE_NOT_FOUND)
    if obj.is_system:
        raise ValidationError("system roles cannot be deleted")
    in_use = (
        await db.execute(select(UserRole.id).where(UserRole.role_id == role_id).limit(1))
    ).scalar_one_or_none()
    if in_use is not None:
        raise ConflictError("role is assigned to one or more users; remove assignments first")
    await db.execute(delete(Role).where(Role.id == role_id))
    await audit.record(
        db, principal, action="role.delete", target_type="role", target_id=str(role_id),
    )
    await db.commit()

# ----------------------- Assignments -----------------------
async def _hydrate_assignment(db: AsyncSession, assignment: UserRole) -> AssignmentOut:
    role = await db.get(Role, assignment.role_id)
    scopes = (
        await db.execute(select(RoleScope).where(RoleScope.assignment_id == assignment.id))
    ).scalars().all()
    return AssignmentOut(
        id=assignment.id,
        user_id=assignment.user_id,
        role_id=assignment.role_id,
        role_name=role.name if role else "(unknown)",
        scopes=[
            ScopeRowOut(
                id=s.id,
                scope_type=s.scope_type.value if hasattr(s.scope_type, "value") else s.scope_type,
                target_id=s.target_id,
            )
            for s in scopes
        ],
    )

@router.get("/users/{user_id}/assignments", response_model=list[AssignmentOut])
async def list_user_assignments(
    user_id: UUID,
    _: Principal = Depends(require_capability("admin:users:read")),
    db: AsyncSession = Depends(get_db),
) -> list[AssignmentOut]:
    user = await db.get(User, user_id)
    if user is None:
        raise NotFoundError(_USER_NOT_FOUND)
    rows = (
        await db.execute(select(UserRole).where(UserRole.user_id == user_id))
    ).scalars().all()
    return [await _hydrate_assignment(db, r) for r in rows]

@router.post("/assignments", response_model=AssignmentOut, status_code=201)
async def create_assignment(
    payload: AssignmentCreate,
    principal: Principal = Depends(require_capability("admin:users:update")),
    db: AsyncSession = Depends(get_db),
):
    user = await db.get(User, payload.user_id)
    if user is None:
        raise NotFoundError(_USER_NOT_FOUND)
    role = await db.get(Role, payload.role_id)
    if role is None:
        raise NotFoundError(_ROLE_NOT_FOUND)
    existing = (
        await db.execute(
            select(UserRole).where(
                UserRole.user_id == payload.user_id,
                UserRole.role_id == payload.role_id,
            )
        )
    ).scalar_one_or_none()
    if existing is not None:
        raise ConflictError("user is already assigned to this role")
    valid_types = {st.value for st in ScopeType}
    for s in payload.scopes:
        if s.scope_type not in valid_types:
            raise ValidationError(
                f"unknown scope_type {s.scope_type!r}",
                details={"valid": sorted(valid_types)},
            )
    assignment = UserRole(user_id=payload.user_id, role_id=payload.role_id)
    db.add(assignment)
    await db.flush()
    for s in payload.scopes:
        db.add(RoleScope(
            assignment_id=assignment.id,
            scope_type=ScopeType(s.scope_type),
            target_id=s.target_id,
        ))
    await audit.record(
        db, principal, action="role_assignment.create", target_type="user_role",
        target_id=str(assignment.id),
        metadata={
            "user_id": str(payload.user_id),
            "role_id": str(payload.role_id),
            "scope_count": len(payload.scopes),
        },
    )
    await db.commit()
    await db.refresh(assignment)
    return await _hydrate_assignment(db, assignment)

@router.delete("/assignments/{assignment_id}", status_code=204)
async def delete_assignment(
    assignment_id: UUID,
    principal: Principal = Depends(require_capability("admin:users:update")),
    db: AsyncSession = Depends(get_db),
):
    assignment = await db.get(UserRole, assignment_id)
    if assignment is None:
        raise NotFoundError(_ASSIGNMENT_NOT_FOUND)
    user_id = assignment.user_id
    role_id = assignment.role_id
    await db.execute(delete(RoleScope).where(RoleScope.assignment_id == assignment_id))
    await db.execute(delete(UserRole).where(UserRole.id == assignment_id))
    await audit.record(
        db, principal, action="role_assignment.delete", target_type="user_role",
        target_id=str(assignment_id),
        metadata={"user_id": str(user_id), "role_id": str(role_id)},
    )
    await db.commit()

# ----------------------- OIDC role mappings -----------------------

async def _hydrate_mapping(db: AsyncSession, m: OidcRoleMapping) -> OidcRoleMappingOut:
    role = await db.get(Role, m.dcim_role_id)
    return OidcRoleMappingOut(
        id=m.id,
        idp_role=m.idp_role,
        claim_source=m.claim_source,
        dcim_role_id=m.dcim_role_id,
        dcim_role_name=role.name if role else "(deleted role)",
        description=m.description,
        scope_dimension=m.scope_dimension.value if m.scope_dimension else None,
        scope_target=m.scope_target,
        created_at=m.created_at,
    )


_VALID_SCOPE_DIMS = {st.value for st in ScopeType if st is not ScopeType.global_}


def _validate_scope_dimension(value: str | None) -> ScopeType | None:
    """Coerce the wire string to a ScopeType. None / empty → unscoped."""
    if value in (None, "", "global"):
        return None
    if value not in _VALID_SCOPE_DIMS:
        raise ValidationError(
            f"unknown scope_dimension {value!r}",
            details={"valid": sorted(_VALID_SCOPE_DIMS)},
        )
    return ScopeType(value)

@router.get("/oidc-role-mappings", response_model=Page[OidcRoleMappingOut])
async def list_oidc_mappings(
    params: PageParams = Depends(PageParams.from_query),
    _: Principal = Depends(require_capability("admin:oidc-mappings:read")),
    db: AsyncSession = Depends(get_db),
):
    # Join Role so we can return the human-readable role name in one
    # roundtrip; the standard paginate() helper assumes a single
    # model + sync model_validate, which doesn't fit this join shape.
    base = (
        select(OidcRoleMapping, Role)
        .join(Role, Role.id == OidcRoleMapping.dcim_role_id)
        .order_by(OidcRoleMapping.idp_role)
    )
    total = (
        await db.execute(
            select(func.count()).select_from(
                select(OidcRoleMapping.id).subquery()
            )
        )
    ).scalar_one()
    rows = (
        await db.execute(
            base.offset((params.page - 1) * params.page_size).limit(params.page_size)
        )
    ).all()
    items = [
        OidcRoleMappingOut(
            id=m.id,
            idp_role=m.idp_role,
            claim_source=m.claim_source,
            dcim_role_id=m.dcim_role_id,
            dcim_role_name=r.name,
            description=m.description,
            scope_dimension=m.scope_dimension.value if m.scope_dimension else None,
            scope_target=m.scope_target,
            created_at=m.created_at,
        )
        for (m, r) in rows
    ]
    return Page[OidcRoleMappingOut](
        items=items,
        page=params.page,
        page_size=params.page_size,
        total=int(total or 0),
        has_more=(params.page * params.page_size) < int(total or 0),
    )

@router.post("/oidc-role-mappings", response_model=OidcRoleMappingOut, status_code=201)
async def create_oidc_mapping(
    payload: OidcRoleMappingCreate,
    principal: Principal = Depends(require_capability("admin:oidc-mappings:create")),
    db: AsyncSession = Depends(get_db),
):
    if (await db.get(Role, payload.dcim_role_id)) is None:
        raise NotFoundError(_ROLE_NOT_FOUND)
    existing = (
        await db.execute(
            select(OidcRoleMapping).where(OidcRoleMapping.idp_role == payload.idp_role)
        )
    ).scalar_one_or_none()
    if existing is not None:
        raise ConflictError("a mapping for that IdP role already exists")
    scope_dim = _validate_scope_dimension(payload.scope_dimension)
    obj = OidcRoleMapping(
        idp_role=payload.idp_role,
        claim_source=payload.claim_source,
        dcim_role_id=payload.dcim_role_id,
        description=payload.description,
        scope_dimension=scope_dim,
        scope_target=payload.scope_target if scope_dim else None,
    )
    db.add(obj)
    await db.flush()
    await audit.record(
        db, principal, action="oidc_role_mapping.create",
        target_type="oidc_role_mapping", target_id=str(obj.id),
        metadata={"idp_role": payload.idp_role, "dcim_role_id": str(payload.dcim_role_id)},
    )
    await db.commit()
    await db.refresh(obj)
    return await _hydrate_mapping(db, obj)

@router.patch("/oidc-role-mappings/{mapping_id}", response_model=OidcRoleMappingOut)
async def update_oidc_mapping(
    mapping_id: UUID,
    payload: OidcRoleMappingUpdate,
    principal: Principal = Depends(require_capability("admin:oidc-mappings:update")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(OidcRoleMapping, mapping_id)
    if obj is None:
        raise NotFoundError(_OIDC_MAPPING_NOT_FOUND)
    if payload.dcim_role_id is not None:
        if (await db.get(Role, payload.dcim_role_id)) is None:
            raise NotFoundError(_ROLE_NOT_FOUND)
        obj.dcim_role_id = payload.dcim_role_id
    if payload.claim_source is not None:
        obj.claim_source = payload.claim_source
    if payload.description is not None:
        obj.description = payload.description
    if payload.scope_dimension is not None:
        scope_dim = _validate_scope_dimension(payload.scope_dimension)
        obj.scope_dimension = scope_dim
        # Clear target when unscoping, otherwise accept the incoming
        # target (None is a valid 'I want to keep the dimension but
        # drop the target' if scope_target also came through).
        if scope_dim is None:
            obj.scope_target = None
        elif payload.scope_target is not None:
            obj.scope_target = payload.scope_target
    elif payload.scope_target is not None:
        # Dimension unchanged but target updated.
        obj.scope_target = payload.scope_target
    await audit.record(
        db, principal, action="oidc_role_mapping.update",
        target_type="oidc_role_mapping", target_id=str(obj.id),
    )
    await db.commit()
    await db.refresh(obj)
    return await _hydrate_mapping(db, obj)

@router.delete("/oidc-role-mappings/{mapping_id}", status_code=204)
async def delete_oidc_mapping(
    mapping_id: UUID,
    principal: Principal = Depends(require_capability("admin:oidc-mappings:delete")),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(OidcRoleMapping, mapping_id)
    if obj is None:
        raise NotFoundError(_OIDC_MAPPING_NOT_FOUND)
    await db.execute(delete(OidcRoleMapping).where(OidcRoleMapping.id == mapping_id))
    await audit.record(
        db, principal, action="oidc_role_mapping.delete",
        target_type="oidc_role_mapping", target_id=str(mapping_id),
        metadata={"idp_role": obj.idp_role},
    )
    await db.commit()


# ----------------------- Capability catalog -----------------------


@router.get("/capabilities/catalog", response_model=CapabilityCatalogOut)
async def get_capabilities_catalog(
    _: Principal = Depends(require_capability("admin:roles:read")),
) -> CapabilityCatalogOut:
    """Return the granular capability catalog so the admin UI can
    render a grouped picker. Static for the lifetime of the process —
    callers can cache aggressively."""
    return CapabilityCatalogOut(
        catalog=CAPABILITY_CATALOG,
        specialties=SPECIALTY_CAPABILITIES,
    )
