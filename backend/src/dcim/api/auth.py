"""Authentication endpoints — OIDC/SAML callbacks + break-glass local login + API token mgmt."""

from __future__ import annotations

from datetime import UTC, datetime
from uuid import UUID
from urllib.parse import urlencode

import bcrypt
import httpx
from fastapi import APIRouter, Depends, HTTPException, Request, Response, status
from fastapi.responses import RedirectResponse
from jose import jwt as jose_jwt
from sqlalchemy import select, text
from sqlalchemy.ext.asyncio import AsyncSession

from ..db import get_db
from ..errors import AuthError, ForbiddenError
from ..models.auth import ApiToken, User
from ..security import audit
from ..security.rate_limit import SlidingWindowLimiter
from ..schemas.auth import ApiTokenOut, LoginRequest, TokenIssue, TokenOut

from ..security.deps import AuthenticatedUser, Principal, find_matching_capability, require_capability
from ..security.tokens import (
    decrypt_refresh_token,
    encrypt_refresh_token,
    generate_api_token,
    issue_user_jwt,
)
from ..settings import get_settings

router = APIRouter(prefix="/auth", tags=["auth"])

_OIDC_NOT_CONFIGURED = "OIDC not configured"

def _anon_principal(label: str, ip: str | None) -> Principal:
    """A Principal for unauthenticated audit events. Has no caps,
    no user, no token — just an actor label + ip for the trail."""
    return Principal(user=None, token=None, capabilities={}, label=label, ip=ip)


# Module-level singleton so the bucket dict survives across requests.
# Configured lazily from settings at first use; the dict survives
# config reload via lru_cache invalidation if the operator wants
# the new limits to apply mid-run (uncommon).
_login_limiter: SlidingWindowLimiter | None = None


def _get_login_limiter() -> SlidingWindowLimiter | None:
    global _login_limiter
    settings = get_settings()
    if settings.login_rate_limit_max <= 0:
        return None
    if _login_limiter is None:
        _login_limiter = SlidingWindowLimiter(
            max_attempts=settings.login_rate_limit_max,
            window_seconds=settings.login_rate_limit_window_seconds,
        )
    return _login_limiter


async def _audit_login_failure(
    db: AsyncSession, *, ip: str | None, email: str | None, reason: str,
) -> None:
    """Record a failed-login audit row, then commit it. Wrapped so the
    AuthError raised after still surfaces; we don't want a failed audit
    insert to mask the auth error, hence the try/except + best-effort
    rollback."""
    try:
        await audit.record(
            db,
            _anon_principal(label=f"anonymous:{email or 'unknown'}", ip=ip),
            action="login.failed",
            success=False,
            metadata={"email": email, "reason": reason},
        )
        await db.commit()
    except Exception:
        await db.rollback()


@router.post("/login", response_model=TokenOut)
async def login_local(
    payload: LoginRequest,
    request: Request,
    db: AsyncSession = Depends(get_db),
) -> TokenOut:
    """Local password login — break-glass only. Production must use OIDC/SAML."""
    settings = get_settings()
    ip = request.client.host if request.client else None
    if settings.local_login_disabled:
        await _audit_login_failure(db, ip=ip, email=payload.email, reason="local_login_disabled")
        raise HTTPException(
            status.HTTP_403_FORBIDDEN,
            detail={"error": {"code": "local_login_disabled",
                              "message": "local password login is disabled; use SSO"}},
        )

    # Rate limit: throttle credential-stuffing per (ip, email). Counted
    # before any DB work so the bucket doesn't drift on slow lookups.
    limiter = _get_login_limiter()
    rl_key = f"{ip or '?'}:{(payload.email or '').lower()}"
    if limiter is not None:
        allowed, count = limiter.consume(rl_key)
        if not allowed:
            await _audit_login_failure(db, ip=ip, email=payload.email, reason="rate_limited")
            raise HTTPException(
                status.HTTP_429_TOO_MANY_REQUESTS,
                detail={"error": {
                    "code": "rate_limited",
                    "message": f"too many login attempts ({count}); retry in a minute",
                }},
                headers={"Retry-After": str(settings.login_rate_limit_window_seconds)},
            )

    res = await db.execute(select(User).where(User.email == payload.email))
    user = res.scalar_one_or_none()
    if user is None or not user.is_active or not user.password_hash:
        await _audit_login_failure(db, ip=ip, email=payload.email, reason="unknown_user")
        raise AuthError("invalid credentials")
    if not bcrypt.checkpw(payload.password.encode(), user.password_hash.encode()):
        await _audit_login_failure(db, ip=ip, email=payload.email, reason="bad_password")
        raise AuthError("invalid credentials")

    # Reset the limiter on success so a flurry of bad guesses doesn't
    # block the now-authenticated user from hitting login again later.
    if limiter is not None:
        limiter.reset(rl_key)
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
async def oidc_login(
    redirect_uri: str | None = None,
    state: str | None = None,
    nonce: str | None = None,
) -> RedirectResponse:
    """Kick off the OIDC code flow.

    Redirects the browser to the issuer's authorization endpoint. The
    `state` and `nonce` params are forwarded verbatim — the SPA mints
    them before navigating here and re-validates on callback:
      * state: echoed by the IdP on the callback URL, SPA compares
        against sessionStorage to defend against CSRF.
      * nonce: embedded in the id_token by the IdP; SPA hands it back
        to /auth/oidc/callback so the backend can verify it matches
        the id_token's `nonce` claim (defense against id_token
        substitution).
    """
    settings = get_settings()
    if not settings.oidc_issuer or not settings.oidc_client_id:
        raise HTTPException(status.HTTP_400_BAD_REQUEST, _OIDC_NOT_CONFIGURED)
    meta = await _oidc_metadata()
    auth_endpoint = meta["authorization_endpoint"]
    callback = redirect_uri or settings.oidc_redirect_uri
    if not callback:
        raise HTTPException(status.HTTP_400_BAD_REQUEST, "redirect_uri required")
    params: dict[str, str] = {
        "client_id": settings.oidc_client_id,
        "redirect_uri": callback,
        "response_type": "code",
        "scope": "openid profile email",
    }
    if state:
        params["state"] = state
    if nonce:
        params["nonce"] = nonce
    return RedirectResponse(url=f"{auth_endpoint}?{urlencode(params)}", status_code=302)

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
    request: Request,
    code: str,
    redirect_uri: str | None = None,
    nonce: str | None = None,
    db: AsyncSession = Depends(get_db),
) -> TokenOut:
    """Exchange the authorization code for tokens, validate the ID token,
    upsert the User, and return our own JWT so the SPA can carry on.

    ID-token signature validation hits the issuer's JWKS over HTTP. We
    accept the issuer's RS256 alg only — no symmetric algs.

    `nonce` is required: the SPA mints it before the authorize redirect
    and we assert claims["nonce"] == nonce post-id_token-validation.
    Defends against id_token substitution: an attacker who obtains a
    token from a different OIDC flow can't replay it here without
    knowing the victim's session-private nonce.
    """
    settings = get_settings()
    ip = request.client.host if request.client else None
    if not settings.oidc_issuer or not settings.oidc_client_id:
        raise HTTPException(status.HTTP_400_BAD_REQUEST, _OIDC_NOT_CONFIGURED)
    if not nonce:
        await _audit_login_failure(db, ip=ip, email=None, reason="missing_nonce")
        raise AuthError("oidc nonce required")
    try:
        tokens, jwks = await _exchange_oidc_code(
            code, redirect_uri or settings.oidc_redirect_uri,
        )
        claims = _validate_id_token(tokens, jwks)
    except AuthError as exc:
        await _audit_login_failure(db, ip=ip, email=None, reason=f"token_exchange:{exc}")
        raise
    if claims.get("nonce") != nonce:
        await _audit_login_failure(db, ip=ip, email=claims.get("email"), reason="nonce_mismatch")
        raise AuthError("oidc id_token nonce mismatch")
    sub = claims.get("sub")
    email = claims.get("email") or claims.get("preferred_username")
    if not sub or not email:
        await _audit_login_failure(db, ip=ip, email=email, reason="missing_claims")
        raise AuthError("oidc claims missing sub/email")
    user = await _upsert_oidc_user(
        db,
        sub=sub,
        email=email,
        name=claims.get("name"),
        refresh_token=tokens.get("refresh_token"),
    )
    # MFA flag: RFC 8176 amr claim. We treat any of the configured
    # mfa_amr_values as satisfying the policy (Keycloak emits "mfa"
    # when its flow enforced one; "otp"/"hwk" appear with specific
    # second factors). Bare "pwd" with no second factor leaves mfa=False.
    amr_claim = claims.get("amr") or []
    mfa_satisfied = isinstance(amr_claim, list) and any(
        v in settings.mfa_amr_values for v in amr_claim
    )
    # Zero-trust: embed the IdP-asserted role names in our session JWT.
    # No UserRole rows are written from OIDC — caps are re-resolved per
    # request in deps._principal_from_jwt against oidc_role_mappings.
    return TokenOut(
        access_token=issue_user_jwt(
            str(user.id),
            idp_roles=sorted(_extract_idp_roles(claims)),
            mfa=mfa_satisfied,
        ),
        expires_in=settings.jwt_ttl_seconds,
        # Returned so the SPA can stash it for RP-initiated logout
        # (passed as `id_token_hint` to /auth/oidc/logout).
        id_token=tokens.get("id_token"),
    )


@router.get("/oidc/logout")
async def oidc_logout(
    id_token_hint: str | None = None,
    post_logout_redirect_uri: str | None = None,
) -> RedirectResponse:
    """RP-initiated logout: 302 to the IdP's end-session endpoint.

    Browser-initiated. The SPA's logout flow sends the user here with
    the id_token it stashed at callback time; this handler forwards
    the request to the IdP, which terminates the SSO session and
    redirects back to `post_logout_redirect_uri` (must be allowlisted
    in the client's post.logout.redirect.uris).

    No-ops gracefully (302 to /login) if OIDC isn't configured.
    """
    settings = get_settings()
    fallback = post_logout_redirect_uri or "/login"
    if not settings.oidc_issuer:
        return RedirectResponse(url=fallback, status_code=302)
    meta = await _oidc_metadata()
    end_session = meta.get("end_session_endpoint")
    if not end_session:
        return RedirectResponse(url=fallback, status_code=302)
    params: dict[str, str] = {}
    if id_token_hint:
        params["id_token_hint"] = id_token_hint
    if post_logout_redirect_uri:
        params["post_logout_redirect_uri"] = post_logout_redirect_uri
    sep = "&" if "?" in end_session else "?"
    url = f"{end_session}{sep}{urlencode(params)}" if params else end_session
    return RedirectResponse(url=url, status_code=302)

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


async def _upsert_oidc_user(
    db: AsyncSession,
    *,
    sub: str,
    email: str,
    name: str | None,
    refresh_token: str | None = None,
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
    # Stash the IdP refresh_token (encrypted) so /auth/refresh can mint
    # a fresh session JWT without an interactive Keycloak round-trip.
    # Keycloak only emits a refresh_token when offline_access scope was
    # requested OR for confidential clients in standard flow — both apply
    # to our dcim-spa client, so this is normally present.
    if refresh_token:
        user.idp_refresh_token = encrypt_refresh_token(refresh_token)
        user.idp_refresh_token_iat = datetime.now(UTC)  # type: ignore[assignment]
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


@router.post("/refresh", response_model=TokenOut)
async def refresh_session(
    request: Request,
    db: AsyncSession = Depends(get_db),
) -> TokenOut:
    """Mint a fresh session JWT for the caller using their IdP refresh_token.

    Identifies the caller by decoding the bearer token (with expiry
    verification disabled — that's the whole point), then uses the
    encrypted IdP refresh_token stored on User.idp_refresh_token to
    hit Keycloak's token endpoint and obtain new tokens. Updates
    idp_roles + mfa from the fresh id_token.

    Returns 401 if no bearer present, the token is unparseable, the
    user has no stored refresh_token, or the IdP rejects it (the
    refresh_token has its own TTL — typically 30 days for Keycloak's
    default). On IdP rejection we also clear the stored token so the
    next attempt fails fast.
    """
    settings = get_settings()
    if not settings.oidc_issuer or not settings.oidc_client_id:
        raise HTTPException(status.HTTP_400_BAD_REQUEST, _OIDC_NOT_CONFIGURED)
    auth_header = request.headers.get("Authorization", "")
    if not auth_header.lower().startswith("bearer "):
        raise AuthError("missing bearer")
    raw = auth_header[7:]
    try:
        claims = jose_jwt.decode(
            raw,
            settings.jwt_secret,
            algorithms=[settings.jwt_algorithm],
            options={"verify_exp": False},
        )
    except Exception as exc:
        raise AuthError("invalid bearer") from exc
    user = await db.get(User, UUID(claims["sub"]))
    if user is None or not user.is_active:
        raise AuthError("user not found or inactive")
    if not user.idp_refresh_token:
        raise AuthError("no refresh token on file; sign in again")
    try:
        plain_refresh = decrypt_refresh_token(user.idp_refresh_token)
    except Exception as exc:
        raise AuthError("refresh token unreadable") from exc

    meta = await _oidc_metadata()
    async with httpx.AsyncClient(timeout=10.0) as client:
        resp = await client.post(
            meta["token_endpoint"],
            data={
                "grant_type": "refresh_token",
                "refresh_token": plain_refresh,
                "client_id": settings.oidc_client_id,
                "client_secret": settings.oidc_client_secret or "",
            },
        )
        if resp.status_code != 200:
            # Refresh token rejected by IdP — clear it so we don't try
            # again on the next refresh call. Operator must re-auth.
            user.idp_refresh_token = None
            user.idp_refresh_token_iat = None
            await db.commit()
            raise AuthError(f"idp refresh rejected: {resp.text}")
        new_tokens = resp.json()
        new_id_token = new_tokens.get("id_token")
        if not new_id_token:
            raise AuthError("idp refresh response missing id_token")
        jwks_resp = await client.get(meta["jwks_uri"])
        jwks_resp.raise_for_status()
        jwks = jwks_resp.json()

    # Verify signature/aud/iss on the fresh id_token, then re-derive
    # idp_roles + mfa from its claims. Skip nonce check on refresh —
    # the refreshed id_token doesn't carry the original session nonce.
    try:
        new_claims = jose_jwt.decode(
            new_id_token, jwks,
            algorithms=["RS256"],
            audience=settings.oidc_client_id,
            issuer=meta["issuer"],
            access_token=new_tokens.get("access_token"),
            options={"verify_at_hash": bool(new_tokens.get("access_token"))},
        )
    except Exception as exc:
        raise AuthError(f"refreshed id_token invalid: {exc}") from exc

    amr_claim = new_claims.get("amr") or []
    mfa_satisfied = isinstance(amr_claim, list) and any(
        v in settings.mfa_amr_values for v in amr_claim
    )
    # Keycloak rotates the refresh_token by default — stash the new one.
    if new_tokens.get("refresh_token"):
        user.idp_refresh_token = encrypt_refresh_token(new_tokens["refresh_token"])
        user.idp_refresh_token_iat = datetime.now(UTC)  # type: ignore[assignment]
    user.last_login_at = datetime.now(UTC)  # type: ignore[assignment]
    await db.commit()

    return TokenOut(
        access_token=issue_user_jwt(
            str(user.id),
            idp_roles=sorted(_extract_idp_roles(new_claims)),
            mfa=mfa_satisfied,
        ),
        expires_in=settings.jwt_ttl_seconds,
        id_token=new_id_token,
    )


@router.post("/logout", status_code=204)
async def logout(
    request: Request,
    db: AsyncSession = Depends(get_db),
) -> Response:
    """Revoke the caller's current session JWT server-side.

    Reads the bearer token from the Authorization header, decodes it
    without rejecting on expired (so a near-expiry logout still
    revokes), and inserts the jti into revoked_jtis. Subsequent
    requests using this token return 401 from `_principal_from_jwt`.

    No body, no caps required — anyone with a JWT can revoke it.
    Returns 204 unconditionally (idempotent): a malformed token, a
    missing jti, or an already-revoked jti is still a "the caller
    can't use this token any more" success from the caller's view.
    """
    auth_header = request.headers.get("Authorization", "")
    if not auth_header.lower().startswith("bearer "):
        return Response(status_code=204)
    raw = auth_header[7:]
    try:
        claims = jose_jwt.decode(
            raw,
            get_settings().jwt_secret,
            algorithms=[get_settings().jwt_algorithm],
            options={"verify_exp": False},
        )
    except Exception:
        return Response(status_code=204)
    jti = claims.get("jti")
    if not jti:
        return Response(status_code=204)
    exp = claims.get("exp")
    expires_at = datetime.fromtimestamp(int(exp), tz=UTC) if exp else datetime.now(UTC)
    user_id_str = claims.get("sub")
    user_uuid: UUID | None = None
    try:
        if user_id_str:
            user_uuid = UUID(user_id_str)
    except ValueError:
        user_uuid = None
    # Upsert pattern: ignore conflict if already revoked.
    await db.execute(
        text(
            "INSERT INTO revoked_jtis (jti, user_id, revoked_at, reason, expires_at) "
            "VALUES (:jti, :user_id, NOW(), :reason, :expires_at) "
            "ON CONFLICT (jti) DO NOTHING"
        ),
        {"jti": jti, "user_id": user_uuid, "reason": "user_logout", "expires_at": expires_at},
    )
    await db.commit()
    return Response(status_code=204)

@router.get("/tokens", response_model=list[ApiTokenOut])
async def list_tokens(
    principal: Principal = Depends(require_capability("admin:api-tokens:read")),
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
    principal: Principal = Depends(require_capability("admin:api-tokens:create")),
    db: AsyncSession = Depends(get_db),
) -> ApiTokenOut:
    if not principal.user:
        raise ForbiddenError("only users can issue tokens")
    # Disallow escalation: every requested code must be granted by an
    # existing capability the issuer holds (exact or wildcard match).
    extra = sorted(
        c for c in payload.permission_codes
        if find_matching_capability(principal.capabilities, c) is None
    )
    if extra:
        raise ForbiddenError(f"cannot grant capabilities you don't hold: {extra}")

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
    principal: Principal = Depends(require_capability("admin:api-tokens:delete")),
    db: AsyncSession = Depends(get_db),
) -> dict:
    res = await db.execute(select(ApiToken).where(ApiToken.id == token_id))
    tok = res.scalar_one_or_none()
    if tok is None:
        raise HTTPException(404, "token not found")
    tok.revoked = True
    await db.commit()
    return {"revoked": True}
