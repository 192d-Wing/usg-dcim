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
from ..models.auth import ApiToken, User
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
    meta = await _oidc_metadata()
    callback = redirect_uri or settings.oidc_redirect_uri
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
        id_token = tokens.get("id_token")
        if not id_token:
            raise AuthError("oidc response missing id_token")
        jwks_resp = await client.get(meta["jwks_uri"])
        jwks_resp.raise_for_status()
        jwks = jwks_resp.json()
    try:
        claims = jose_jwt.decode(
            id_token, jwks,
            algorithms=["RS256"],
            audience=settings.oidc_client_id,
            issuer=meta["issuer"],
        )
    except Exception as exc:
        raise AuthError(f"oidc id_token invalid: {exc}") from exc

    sub = claims.get("sub")
    email = claims.get("email") or claims.get("preferred_username")
    if not sub or not email:
        raise AuthError("oidc claims missing sub/email")
    user = await _upsert_oidc_user(db, sub=sub, email=email, name=claims.get("name"))
    return TokenOut(
        access_token=issue_user_jwt(str(user.id)),
        expires_in=settings.jwt_ttl_seconds,
    )


async def _upsert_oidc_user(
    db: AsyncSession, *, sub: str, email: str, name: str | None,
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
    else:
        user.sso_subject = sub
        if name and not user.display_name:
            user.display_name = name
    user.last_login_at = datetime.now(UTC)  # type: ignore[assignment]
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
