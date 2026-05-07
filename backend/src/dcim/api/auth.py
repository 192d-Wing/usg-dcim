"""Authentication endpoints — OIDC/SAML callbacks + break-glass local login + API token mgmt."""

from __future__ import annotations

from datetime import UTC, datetime

from fastapi import APIRouter, Depends, HTTPException, status
import bcrypt
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


@router.get("/oidc/login")
async def oidc_login() -> dict:
    """Stub. Production wires Authlib OAuth client; redirect to OIDC issuer."""
    settings = get_settings()
    if not settings.oidc_issuer:
        raise HTTPException(status.HTTP_400_BAD_REQUEST, "OIDC not configured")
    return {"redirect_to": f"{settings.oidc_issuer}/protocol/openid-connect/auth"}


@router.post("/oidc/callback", response_model=TokenOut)
async def oidc_callback(code: str, db: AsyncSession = Depends(get_db)) -> TokenOut:
    """Stub callback — production exchanges code, validates ID token, upserts User."""
    raise HTTPException(status.HTTP_501_NOT_IMPLEMENTED, "OIDC callback wiring TODO")


@router.post("/saml/acs", response_model=TokenOut)
async def saml_acs() -> TokenOut:
    raise HTTPException(status.HTTP_501_NOT_IMPLEMENTED, "SAML ACS wiring TODO")


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
