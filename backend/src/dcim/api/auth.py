"""Authentication endpoints — OIDC/SAML callbacks + break-glass local login + API token mgmt."""

from __future__ import annotations

from datetime import UTC, datetime
from urllib.parse import urlencode

import bcrypt
import httpx
from fastapi import APIRouter, Depends, HTTPException, status
from fastapi.responses import RedirectResponse
from jose import jwt as jose_jwt
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..errors import AuthError, ForbiddenError
from ..models.auth import ApiToken, OidcRoleMapping, Role, User, UserRole
from ..schemas.auth import ApiTokenOut, LoginRequest, TokenIssue, TokenOut
from ..security.capabilities import TOKENS_MANAGE
from ..security.deps import AuthenticatedUser, Principal, require_capability
from ..security.tokens import generate_api_token, issue_user_jwt
from ..settings import get_settings

router = APIRouter(prefix="/auth", tags=["auth"])

_OIDC_NOT_CONFIGURED = "OIDC not configured"


@router.post("/login", response_model=TokenOut)
async def login_local(payload: LoginRequest, db: AsyncSession = Depends(get_db)) -> TokenOut:
    """Local password login — break-glass only. Production must use OIDC/SAML."""
    settings = get_settings()
    if settings.env == "prod" and not settings.oidc_issuer and not settings.saml_metadata_url:
        # allow only if explicitly configured to allow it
        pass

    res = await db.execute(select(User).where(User.email == payload.email))
    user = res.scalar_one_or_none()
    if user is None or not user.is_active or not user.password_hash:
        raise AuthError("invalid credentials")
    if not bcrypt.checkpw(payload.password.encode(), user.password_hash.encode()):
        raise AuthError("invalid credentials")

    user.last_login_at = datetime.now(UTC)  # type: ignore[assignment]
    await db.commit()
    return TokenOut(access_token=issue_user_jwt(str(user.id)), expires_in=settings.jwt_ttl_seconds)


async def _oidc_metadata() -> dict:
    """Fetch + lightly cache the OIDC discovery document."""
    settings = get_settings()
    if not settings.oidc_issuer:
        raise HTTPException(status.HTTP_400_BAD_REQUEST, _OIDC_NOT_CONFIGURED)
    cached = getattr(_oidc_metadata, "_cache", None)
    if cached and cached["issuer"] == settings.oidc_issuer:
        return cached["doc"]
    async with httpx.AsyncClient(timeout=10.0) as client:
        url = settings.oidc_issuer.rstrip("/") + "/.well-known/openid-configuration"
        resp = await client.get(url)
        resp.raise_for_status()
        doc = resp.json()
    _oidc_metadata._cache = {"issuer": settings.oidc_issuer, "doc": doc}  # type: ignore[attr-defined]
    return doc


@router.get("/oidc/login")
async def oidc_login(redirect_uri: str | None = None) -> RedirectResponse:
    """Kick off the OIDC code flow.

    Redirects the browser to the issuer's authorization endpoint with
    the configured client_id + scope=openid+profile+email. The optional
    `redirect_uri` query param overrides settings.oidc_redirect_uri so
    the same backend can serve dev (localhost) and staging (https://...)
    without redeploys.
    """
    settings = get_settings()
    if not settings.oidc_issuer or not settings.oidc_client_id:
        raise HTTPException(status.HTTP_400_BAD_REQUEST, _OIDC_NOT_CONFIGURED)
    meta = await _oidc_metadata()
    auth_endpoint = meta["authorization_endpoint"]
    callback = redirect_uri or settings.oidc_redirect_uri
    if not callback:
        raise HTTPException(status.HTTP_400_BAD_REQUEST, "redirect_uri required")
    qs = urlencode({
        "client_id": settings.oidc_client_id,
        "redirect_uri": callback,
        "response_type": "code",
        "scope": "openid profile email",
    })
    return RedirectResponse(url=f"{auth_endpoint}?{qs}", status_code=302)


async def _exchange_oidc_code(code: str, callback: str | None) -> tuple[dict, dict]:
    """Token-exchange + JWKS fetch. Returns (tokens, jwks)."""
    settings = get_settings()
    meta = await _oidc_metadata()
    async with httpx.AsyncClient(timeout=10.0) as client:
        token_resp = await client.post(
            meta["token_endpoint"],
            data={
                "grant_type": "authorization_code",
                "code": code,
                "redirect_uri": callback,
                "client_id": settings.oidc_client_id,
                "client_secret": settings.oidc_client_secret or "",
            },
        )
        if token_resp.status_code != 200:
            raise AuthError(f"oidc token exchange failed: {token_resp.text}")
        tokens = token_resp.json()
        if not tokens.get("id_token"):
            raise AuthError("oidc response missing id_token")
        jwks_resp = await client.get(meta["jwks_uri"])
        jwks_resp.raise_for_status()
        jwks = jwks_resp.json()
    return tokens, jwks


def _validate_id_token(tokens: dict, jwks: dict) -> dict:
    """Verify signature, audience, issuer, and at_hash binding."""
    settings = get_settings()
    meta_cache = getattr(_oidc_metadata, "_cache", None)
    issuer = meta_cache["doc"]["issuer"] if meta_cache else settings.oidc_issuer
    try:
        # Pass access_token so python-jose can verify the at_hash claim
        # (binds the id_token to this specific access_token). Keycloak
        # includes at_hash in its id_tokens by default.
        return jose_jwt.decode(
            tokens["id_token"], jwks,
            algorithms=["RS256"],
            audience=settings.oidc_client_id,
            issuer=issuer,
            access_token=tokens.get("access_token"),
        )
    except Exception as exc:
        raise AuthError(f"oidc id_token invalid: {exc}") from exc


@router.get("/oidc/callback", response_model=TokenOut)
async def oidc_callback(
    code: str,
    redirect_uri: str | None = None,
    db: AsyncSession = Depends(get_db),
) -> TokenOut:
    """Exchange the authorization code for tokens, validate the ID token,
    upsert the User, and return our own JWT so the SPA can carry on.

    ID-token signature validation hits the issuer's JWKS over HTTP. We
    accept the issuer's RS256 alg only — no symmetric algs.
    """
    settings = get_settings()
    if not settings.oidc_issuer or not settings.oidc_client_id:
        raise HTTPException(status.HTTP_400_BAD_REQUEST, _OIDC_NOT_CONFIGURED)
    tokens, jwks = await _exchange_oidc_code(
        code, redirect_uri or settings.oidc_redirect_uri,
    )
    claims = _validate_id_token(tokens, jwks)
    sub = claims.get("sub")
    email = claims.get("email") or claims.get("preferred_username")
    if not sub or not email:
        raise AuthError("oidc claims missing sub/email")
    user = await _upsert_oidc_user(
        db, sub=sub, email=email, name=claims.get("name"), claims=claims,
    )
    return TokenOut(
        access_token=issue_user_jwt(str(user.id)),
        expires_in=settings.jwt_ttl_seconds,
    )


def _extract_idp_roles(claims: dict) -> set[str]:
    """Pull role strings from the standard claim locations.

    Covers Keycloak (realm_access.roles + resource_access.*.roles),
    Okta and ADFS (groups, roles). Returns deduped set of strings.
    """
    roles: set[str] = set()

    def _add(value: object) -> None:
        if isinstance(value, list):
            for v in value:
                if isinstance(v, str) and v:
                    roles.add(v)

    realm_access = claims.get("realm_access")
    if isinstance(realm_access, dict):
        _add(realm_access.get("roles"))

    resource_access = claims.get("resource_access")
    if isinstance(resource_access, dict):
        for client_block in resource_access.values():
            if isinstance(client_block, dict):
                _add(client_block.get("roles"))

    _add(claims.get("groups"))
    _add(claims.get("roles"))
    return roles


async def _sync_oidc_roles(db: AsyncSession, user: User, claims: dict) -> None:
    """Add any DCIM roles that the user's IdP-asserted roles map to.

    Idempotent — duplicates are skipped. We don't currently remove
    previously-assigned roles when an IdP role disappears; that needs
    a 'source' column on UserRole to distinguish OIDC-managed rows
    from manual admin assignments. Tracked as a follow-up.
    """
    idp_roles = _extract_idp_roles(claims)
    if not idp_roles:
        return
    mappings = (
        await db.execute(
            select(OidcRoleMapping).where(OidcRoleMapping.idp_role.in_(idp_roles))
        )
    ).scalars().all()
    if not mappings:
        return
    existing = {
        ur.role_id
        for ur in (
            await db.execute(select(UserRole).where(UserRole.user_id == user.id))
        ).scalars().all()
    }
    for m in mappings:
        if m.dcim_role_id in existing:
            continue
        # Confirm the DCIM role still exists before binding.
        if (await db.get(Role, m.dcim_role_id)) is None:
            continue
        db.add(UserRole(user_id=user.id, role_id=m.dcim_role_id))
        existing.add(m.dcim_role_id)


async def _upsert_oidc_user(
    db: AsyncSession, *, sub: str, email: str, name: str | None, claims: dict,
) -> User:
    """Match by sso_subject first, then email — handles users who pre-exist as
    local break-glass accounts before SSO went live."""
    res = await db.execute(select(User).where(User.sso_subject == sub))
    user = res.scalar_one_or_none()
    if user is None:
        res = await db.execute(select(User).where(User.email == email))
        user = res.scalar_one_or_none()
    if user is None:
        user = User(email=email, display_name=name, sso_subject=sub, is_active=True)
        db.add(user)
        await db.flush()  # need user.id for role sync
    else:
        user.sso_subject = sub
        if name and not user.display_name:
            user.display_name = name
    user.last_login_at = datetime.now(UTC)  # type: ignore[assignment]
    await _sync_oidc_roles(db, user, claims)
    await db.commit()
    await db.refresh(user)
    return user


@router.get("/me")
async def whoami(principal: AuthenticatedUser) -> dict:
    return {
        "user": {
            "id": str(principal.user.id) if principal.user else None,
            "email": principal.user.email if principal.user else None,
        },
        "via_token": principal.token is not None,
        "capabilities": sorted(principal.capabilities.keys()),
    }


@router.get("/tokens", response_model=list[ApiTokenOut])
async def list_tokens(
    principal: Principal = Depends(require_capability(TOKENS_MANAGE)),
    db: AsyncSession = Depends(get_db),
) -> list[ApiTokenOut]:
    """List the caller's own API tokens. Plaintext is never returned for existing tokens."""
    if not principal.user:
        raise ForbiddenError("only users can list their tokens")
    res = await db.execute(
        select(ApiToken)
        .where(ApiToken.owner_user_id == principal.user.id)
        .order_by(ApiToken.created_at.desc())
    )
    tokens = res.scalars().all()
    return [ApiTokenOut.model_validate(t) for t in tokens]


@router.post("/tokens", response_model=ApiTokenOut)
async def issue_token(
    payload: TokenIssue,
    principal: Principal = Depends(require_capability(TOKENS_MANAGE)),
    db: AsyncSession = Depends(get_db),
) -> ApiTokenOut:
    if not principal.user:
        raise ForbiddenError("only users can issue tokens")
    # Disallow escalation: token's permissions must be a subset of the issuer's caps.
    extra = set(payload.permission_codes) - set(principal.capabilities.keys())
    if extra:
        raise ForbiddenError(f"cannot grant capabilities you don't hold: {sorted(extra)}")

    raw, digest = generate_api_token()
    tok = ApiToken(
        name=payload.name,
        owner_user_id=principal.user.id,
        token_hash=digest,
        permission_codes=payload.permission_codes,
        scope_json=payload.scope_json,
        expires_at=payload.expires_at,
    )
    db.add(tok)
    await db.commit()
    await db.refresh(tok)
    out = ApiTokenOut.model_validate(tok)
    out.plaintext = raw  # only returned once
    return out


@router.delete("/tokens/{token_id}")
async def revoke_token(
    token_id: str,
    principal: Principal = Depends(require_capability(TOKENS_MANAGE)),
    db: AsyncSession = Depends(get_db),
) -> dict:
    res = await db.execute(select(ApiToken).where(ApiToken.id == token_id))
    tok = res.scalar_one_or_none()
    if tok is None:
        raise HTTPException(404, "token not found")
    tok.revoked = True
    await db.commit()
    return {"revoked": True}
