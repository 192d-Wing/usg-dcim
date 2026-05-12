"""JWT and API-token helpers."""

from __future__ import annotations

import hashlib
import hmac
import secrets
import uuid
from datetime import UTC, datetime, timedelta

from jose import jwt

from ..settings import get_settings

_settings = get_settings()


def issue_user_jwt(
    user_id: str,
    *,
    ttl: int | None = None,
    idp_roles: list[str] | None = None,
    mfa: bool = False,
) -> str:
    """Mint a session JWT for `user_id`.

    Every minted JWT carries a `jti` claim (UUID4); the deps layer
    rejects requests whose jti is in the `revoked_jtis` table. Pair
    that with /auth/logout to actually invalidate a session before
    its natural expiry.

    Pass `idp_roles` to embed the role strings the IdP asserted for
    this user. The Principal builder reads them and resolves caps
    via `oidc_role_mappings` per request — that's the zero-trust
    pivot: no persisted UserRole row carries OIDC-derived authority.

    Pass `mfa=True` when the underlying authentication satisfied the
    deployment's MFA policy (derived from the id_token's `amr` claim
    at OIDC callback time). The flag is checked by require_capability
    for codes listed in settings.mfa_required_caps.
    """
    now = datetime.now(UTC)
    exp = now + timedelta(seconds=ttl or _settings.jwt_ttl_seconds)
    payload: dict = {
        "sub": user_id,
        "iat": int(now.timestamp()),
        "exp": int(exp.timestamp()),
        "jti": uuid.uuid4().hex,
        "kind": "user",
        "mfa": bool(mfa),
    }
    if idp_roles:
        payload["idp_roles"] = sorted(set(idp_roles))
    return jwt.encode(
        payload,
        _settings.jwt_secret,
        algorithm=_settings.jwt_algorithm,
        headers={"kid": _settings.jwt_kid},
    )


def decode_user_jwt(token: str) -> dict:
    """Verify + decode. Walks active key first, then any retired key
    matching the token's `kid` header — that's what lets a rotation
    coexist with active sessions issued under the previous secret."""
    headers = jwt.get_unverified_header(token)
    candidates: list[tuple[str, str]] = [(_settings.jwt_kid, _settings.jwt_secret)]
    kid = headers.get("kid")
    if kid and kid != _settings.jwt_kid:
        retired = _settings.jwt_old_secrets.get(kid)
        if retired:
            candidates.append((kid, retired))
    last_err: Exception | None = None
    for _kid, secret in candidates:
        try:
            return jwt.decode(token, secret, algorithms=[_settings.jwt_algorithm])
        except Exception as exc:  # noqa: BLE001 - try next key
            last_err = exc
    assert last_err is not None
    raise last_err


def generate_api_token() -> tuple[str, str]:
    """Return (plaintext_token, sha256_hash). Hash is what we store."""
    raw = "dcim_" + secrets.token_urlsafe(32)
    digest = hashlib.sha256(raw.encode()).hexdigest()
    return raw, digest


def hash_api_token(plaintext: str) -> str:
    return hashlib.sha256(plaintext.encode()).hexdigest()


def constant_time_eq(a: str, b: str) -> bool:
    return hmac.compare_digest(a, b)
