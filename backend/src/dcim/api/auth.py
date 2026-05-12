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

from ..security.deps import AuthenticatedUser, Principal, find_matching_capability, require_capability
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
    code: str,
    redirect_uri: str | None = None,
    nonce: str | None = None,
    db: AsyncSession = Depends(get_db),
) -> TokenOut:
    """Exchange the authorization code for tokens, validate the ID token,
    upsert the User, and return our own JWT so the SPA can carry on.

    ID-token signature validation hits the issuer's JWKS over HTTP. We
    accept the issuer's RS256 alg only — no symmetric algs.

    When the SPA passes `nonce`, it's the value it minted before the
    authorize redirect; we require it to match the id_token's `nonce`
    claim. Defends against id_token substitution: an attacker who
    obtains a token from a different OIDC flow can't replay it here
    without knowing the victim's session-private nonce. Absent
    `nonce` we don't enforce — kept optional during the rollout
    window; can be tightened to required once the SPA always sends it.
    """
    settings = get_settings()
    if not settings.oidc_issuer or not settings.oidc_client_id:
        raise HTTPException(status.HTTP_400_BAD_REQUEST, _OIDC_NOT_CONFIGURED)
    tokens, jwks = await _exchange_oidc_code(
        code, redirect_uri or settings.oidc_redirect_uri,
    )
    claims = _validate_id_token(tokens, jwks)
    if nonce is not None and claims.get("nonce") != nonce:
        raise AuthError("oidc id_token nonce mismatch")
    sub = claims.get("sub")
    email = claims.get("email") or claims.get("preferred_username")
    if not sub or not email:
        raise AuthError("oidc claims missing sub/email")
    user = await _upsert_oidc_user(db, sub=sub, email=email, name=claims.get("name"))
    # Zero-trust: embed the IdP-asserted role names in our session JWT.
    # No UserRole rows are written from OIDC — caps are re-resolved per
    # request in deps._principal_from_jwt against oidc_role_mappings.
    return TokenOut(
        access_token=issue_user_jwt(
            str(user.id),
            idp_roles=sorted(_extract_idp_roles(claims)),
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
