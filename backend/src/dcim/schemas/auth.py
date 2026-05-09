from __future__ import annotations

from datetime import datetime
from uuid import UUID

from pydantic import BaseModel, ConfigDict


class LoginRequest(BaseModel):
    email: str
    password: str  # break-glass only; prod auth is OIDC/SAML


class TokenOut(BaseModel):
    access_token: str
    token_type: str = "bearer"
    expires_in: int


class TokenIssue(BaseModel):
    name: str
    permission_codes: list[str]
    scope_json: dict = {}
    expires_at: datetime | None = None


class ApiTokenOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    name: str
    permission_codes: list[str]
    scope_json: dict
    created_at: datetime
    expires_at: datetime | None = None
    last_used_at: datetime | None = None
    revoked: bool
    plaintext: str | None = None  # only set on first creation


# --- Admin: users / roles / assignments ---


class UserCreate(BaseModel):
    email: str
    display_name: str | None = None
    is_active: bool = True


class UserUpdate(BaseModel):
    display_name: str | None = None
    is_active: bool | None = None


class UserOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    email: str
    display_name: str | None
    is_active: bool
    sso_subject: str | None
    last_login_at: datetime | None
    created_at: datetime


class RoleCreate(BaseModel):
    name: str
    description: str | None = None
    permission_codes: list[str]


class RoleUpdate(BaseModel):
    name: str | None = None
    description: str | None = None
    permission_codes: list[str] | None = None


class RoleOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    name: str
    description: str | None
    permission_codes: list[str]
    is_system: bool


class ScopeRowIn(BaseModel):
    scope_type: str  # global | region | site | site_group | enclave | organization
    target_id: str | None = None


class ScopeRowOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    scope_type: str
    target_id: str | None


class AssignmentCreate(BaseModel):
    user_id: UUID
    role_id: UUID
    scopes: list[ScopeRowIn] = []


class AssignmentOut(BaseModel):
    model_config = ConfigDict(from_attributes=True)
    id: UUID
    user_id: UUID
    role_id: UUID
    role_name: str
    scopes: list[ScopeRowOut]
