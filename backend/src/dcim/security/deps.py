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

from fastapi import Depends, HTTPException, Request, status
from fastapi.security import HTTPAuthorizationCredentials, HTTPBearer
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..errors import AuthError, ScopeError
from ..models.auth import ApiToken, RevokedJti, User
from .scope import Scope, caps_from_idp_roles, scope_for_user, site_matches_scope
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
    # JTI revocation: a leaked token can be revoked server-side by
    # inserting its jti into revoked_jtis (cf. /auth/logout). Tokens
    # minted before this commit have no jti — those skip the check
    # and continue to work until their natural expiry.
    jti = claims.get("jti")
    if jti:
        revoked = await db.get(RevokedJti, jti)
        if revoked is not None:
            raise AuthError("token revoked")
    user = await db.get(User, user_id)
    if user is None or not user.is_active:
        raise AuthError("user not found or inactive")

    # Persistent caps (manual UserRole assignments + their RoleScope rows).
    caps = await scope_for_user(db, user)

    # IdP-derived caps — zero-trust: re-resolved from the JWT's idp_roles
    # claim against oidc_role_mappings on every request, never persisted.
    # The JWT TTL bounds how long an IdP revocation can take effect.
    idp_caps = await caps_from_idp_roles(db, claims.get("idp_roles") or [])
    for code, scope in idp_caps.items():
        caps[code] = caps.get(code, Scope()).union(scope)

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
    # The token's effective caps are the requested permission_codes, kept
    # only when the owner still has a cap that grants each one — wildcard
    # owner caps (`*`, `dns:*`, `dns:servers:*`) count, otherwise admin-
    # issued tokens silently end up with zero capabilities because the
    # owner's role bundle only stores the wildcard literally.
    owner_caps = await scope_for_user(db, owner)
    caps: dict[str, Scope] = {}
    for code in token.permission_codes:
        granting = find_matching_capability(owner_caps, code)
        if granting is not None:
            caps[code] = granting
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


def find_matching_capability(caps: dict[str, Scope], code: str) -> Scope | None:
    """Find a capability in `caps` that grants `code`, with `*` glob semantics.

    A held capability `pattern` grants `code` when, after splitting both on
    `:`, the segment counts match and every segment in `pattern` is either
    equal to or `*` (the wildcard) the matching segment in `code`. The
    bare global `*` (single-segment) grants everything.

    Examples:
      "inventory:sites:read" matches itself, "inventory:sites:*",
      "inventory:*:read", "inventory:*", "*".
      "dns:*:read" does NOT match "dns:zones:create" — the action
      segments don't align.

    Returns the matching capability's Scope, or None if nothing grants.
    """
    if code in caps:
        return caps[code]
    if "*" in caps:
        # Bare global wildcard short-circuits any check.
        return caps["*"]
    target = code.split(":")
    for pattern, scope in caps.items():
        parts = pattern.split(":")
        if len(parts) != len(target):
            continue
        if all(p == "*" or p == t for p, t in zip(parts, target)):
            return scope
    return None


def require_capability(code: str):
    """Dependency: ensures the principal has the named capability anywhere in their scope."""

    async def _dep(principal: AuthenticatedUser) -> Principal:
        if find_matching_capability(principal.capabilities, code) is None:
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
        scope = find_matching_capability(principal.capabilities, code)
        if scope is None:
            raise ScopeError(f"capability {code} not granted")
        sid = request.path_params.get(site_id_param) or request.query_params.get(site_id_param)
        if sid is None:
            raise ScopeError("site_id required to evaluate scope")
        try:
            site_uuid = UUID(sid)
        except ValueError as e:
            raise ScopeError("invalid site_id") from e
        if not await site_matches_scope(db, scope, site_uuid):
            raise ScopeError(f"site {site_uuid} outside your scope for {code}")
        return principal

    return _dep
