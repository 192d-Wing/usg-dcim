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
