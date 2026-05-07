"""JWT and API-token helpers."""

from __future__ import annotations

import hashlib
import hmac
import secrets
from datetime import UTC, datetime, timedelta

from jose import jwt

from ..settings import get_settings

_settings = get_settings()


def issue_user_jwt(user_id: str, *, ttl: int | None = None) -> str:
    now = datetime.now(UTC)
    exp = now + timedelta(seconds=ttl or _settings.jwt_ttl_seconds)
    payload = {"sub": user_id, "iat": int(now.timestamp()), "exp": int(exp.timestamp()), "kind": "user"}
    return jwt.encode(payload, _settings.jwt_secret, algorithm=_settings.jwt_algorithm)


def decode_user_jwt(token: str) -> dict:
    return jwt.decode(token, _settings.jwt_secret, algorithms=[_settings.jwt_algorithm])


def generate_api_token() -> tuple[str, str]:
    """Return (plaintext_token, sha256_hash). Hash is what we store."""
    raw = "dcim_" + secrets.token_urlsafe(32)
    digest = hashlib.sha256(raw.encode()).hexdigest()
    return raw, digest


def hash_api_token(plaintext: str) -> str:
    return hashlib.sha256(plaintext.encode()).hexdigest()


def constant_time_eq(a: str, b: str) -> bool:
    return hmac.compare_digest(a, b)
