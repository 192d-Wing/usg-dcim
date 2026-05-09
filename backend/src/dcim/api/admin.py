"""Admin CRUD: users, roles, role assignments + scopes.

These endpoints back the Admin settings UI. They're capability-gated on
USERS_MANAGE / ROLES_MANAGE so non-admin users never see them.
"""

from __future__ import annotations

from uuid import UUID

from fastapi import APIRouter, Depends
from sqlalchemy import delete, select
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..errors import ConflictError, NotFoundError, ValidationError
from ..models.auth import Role, RoleScope, ScopeType, User, UserRole
from ..schemas.auth import (
    AssignmentCreate,
    AssignmentOut,
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
from ..security.capabilities import ROLES_MANAGE, USERS_MANAGE
from ..security.deps import Principal, require_capability
from ._pagination import paginate

router = APIRouter(prefix="/admin", tags=["admin"])

_USER_NOT_FOUND = "user not found"
_ROLE_NOT_FOUND = "role not found"
_ASSIGNMENT_NOT_FOUND = "role assignment not found"


# ----------------------- Users -----------------------
@router.get("/users", response_model=Page[UserOut])
async def list_users(
    params: PageParams = Depends(PageParams.from_query),
    _: Principal = Depends(require_capability(USERS_MANAGE)),
    db: AsyncSession = Depends(get_db),
):
    return await paginate(db, select(User), model=User, params=params, out_model=UserOut)


@router.post("/users", response_model=UserOut, status_code=201)
async def create_user(
    payload: UserCreate,
    principal: Principal = Depends(require_capability(USERS_MANAGE)),
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
    principal: Principal = Depends(require_capability(USERS_MANAGE)),
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
    _: Principal = Depends(require_capability(ROLES_MANAGE)),
    db: AsyncSession = Depends(get_db),
):
    return await paginate(db, select(Role), model=Role, params=params, out_model=RoleOut)


@router.post("/roles", response_model=RoleOut, status_code=201)
async def create_role(
    payload: RoleCreate,
    principal: Principal = Depends(require_capability(ROLES_MANAGE)),
    db: AsyncSession = Depends(get_db),
):
    existing = (await db.execute(select(Role).where(Role.name == payload.name))).scalar_one_or_none()
    if existing is not None:
        raise ConflictError("a role with that name already exists")
    extra = set(payload.permission_codes) - set(principal.capabilities.keys())
    if extra:
        raise ValidationError(
            f"cannot grant capabilities you don't hold: {sorted(extra)}",
            details={"missing": sorted(extra)},
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
    principal: Principal = Depends(require_capability(ROLES_MANAGE)),
    db: AsyncSession = Depends(get_db),
):
    obj = await db.get(Role, role_id)
    if obj is None:
        raise NotFoundError(_ROLE_NOT_FOUND)
    if obj.is_system:
        raise ValidationError("system roles are read-only")
    diff = payload.model_dump(exclude_unset=True)
    if "permission_codes" in diff:
        extra = set(diff["permission_codes"]) - set(principal.capabilities.keys())
        if extra:
            raise ValidationError(
                f"cannot grant capabilities you don't hold: {sorted(extra)}",
                details={"missing": sorted(extra)},
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
    principal: Principal = Depends(require_capability(ROLES_MANAGE)),
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
    _: Principal = Depends(require_capability(USERS_MANAGE)),
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
    principal: Principal = Depends(require_capability(USERS_MANAGE)),
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
    principal: Principal = Depends(require_capability(USERS_MANAGE)),
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
