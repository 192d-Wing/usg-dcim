"""FastAPI dependencies for authn/authz.

A request principal is one of:
  - a User authenticated via OIDC/SAML/local-fallback JWT
  - an ApiToken (Authorization: Bearer dcim_<token>)
  - a Collector mTLS client (via X-Client-Fingerprint header proxied from ingress)

Capabilities are checked declaratively via require_capability(); per-site checks
go through require_capability_for_site(), which evaluates ABAC scope.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Annotated
from uuid import UUID

from fastapi import Depends, Header, HTTPException, Request, status
from fastapi.security import HTTPAuthorizationCredentials, HTTPBearer
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..errors import AuthError, ScopeError
from ..models.auth import ApiToken, User
from .scope import Scope, scope_for_user, site_matches_scope
from .tokens import decode_user_jwt, hash_api_token

bearer = HTTPBearer(auto_error=False)


@dataclass
class Principal:
    """Either a user or a token; both expose capabilities and scope."""

    user: User | None
    token: ApiToken | None
    capabilities: dict[str, Scope]  # capability_code -> Scope
    label: str  # for audit
    ip: str | None = None

    @property
    def is_user(self) -> bool:
        return self.user is not None


AuthenticatedUser = Annotated[Principal, Depends(lambda: None)]  # rebound below


async def _principal_from_jwt(
    creds: HTTPAuthorizationCredentials, db: AsyncSession, ip: str | None
) -> Principal:
    try:
        claims = decode_user_jwt(creds.credentials)
    except Exception as e:
        raise AuthError("invalid token") from e
    user_id = UUID(claims["sub"])
    user = await db.get(User, user_id)
    if user is None or not user.is_active:
        raise AuthError("user not found or inactive")
    caps = await scope_for_user(db, user)
    return Principal(user=user, token=None, capabilities=caps, label=user.email, ip=ip)


async def _principal_from_api_token(
    raw: str, db: AsyncSession, ip: str | None
) -> Principal:
    digest = hash_api_token(raw)
    res = await db.execute(select(ApiToken).where(ApiToken.token_hash == digest))
    token = res.scalar_one_or_none()
    if token is None or token.revoked:
        raise AuthError("invalid api token")
    owner = await db.get(User, token.owner_user_id)
    if owner is None or not owner.is_active:
        raise AuthError("owner inactive")
    # API token scope is whatever was baked into scope_json at issue time.
    # For simplicity here we hand back the owner's scope and intersect by capability codes.
    owner_caps = await scope_for_user(db, owner)
    caps = {c: owner_caps[c] for c in token.permission_codes if c in owner_caps}
    return Principal(user=owner, token=token, capabilities=caps, label=f"token:{token.name}", ip=ip)


async def get_principal(
    request: Request,
    creds: Annotated[HTTPAuthorizationCredentials | None, Depends(bearer)] = None,
    db: AsyncSession = Depends(get_db),
) -> Principal:
    if creds is None:
        raise AuthError("missing credentials")
    ip = request.client.host if request.client else None
    raw = creds.credentials
    if raw.startswith("dcim_"):
        return await _principal_from_api_token(raw, db, ip)
    return await _principal_from_jwt(creds, db, ip)


# Reassign the type alias to the real dependency
AuthenticatedUser = Annotated[Principal, Depends(get_principal)]  # type: ignore[misc]


def require_capability(code: str):
    """Dependency: ensures the principal has the named capability anywhere in their scope."""

    async def _dep(principal: AuthenticatedUser) -> Principal:
        if code not in principal.capabilities:
            raise HTTPException(
                status_code=status.HTTP_403_FORBIDDEN,
                detail={"error": {"code": "missing_capability", "message": code}},
            )
        return principal

    return _dep


def require_capability_for_site(code: str, site_id_param: str = "site_id"):
    """Dependency: ensures the principal has `code` AND it covers the requested site."""

    async def _dep(
        request: Request,
        principal: AuthenticatedUser,
        db: AsyncSession = Depends(get_db),
    ) -> Principal:
        if code not in principal.capabilities:
            raise ScopeError(f"capability {code} not granted")
        sid = request.path_params.get(site_id_param) or request.query_params.get(site_id_param)
        if sid is None:
            raise ScopeError("site_id required to evaluate scope")
        try:
            site_uuid = UUID(sid)
        except ValueError as e:
            raise ScopeError("invalid site_id") from e
        if not await site_matches_scope(db, principal.capabilities[code], site_uuid):
            raise ScopeError(f"site {site_uuid} outside your scope for {code}")
        return principal

    return _dep
