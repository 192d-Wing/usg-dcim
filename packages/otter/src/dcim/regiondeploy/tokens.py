"""Per-deployment bootstrap-token helpers for the kubeconfig callback.

The `POST /regiondeploy/{id}/kubeconfig/callback` endpoint cannot use
the normal session/API-token auth path — it is invoked from a freshly
booted node that has no DCIM credential yet. To prevent any caller who
learns a deployment_id from overwriting the central-cluster kubeconfig
Secret, the endpoint requires a per-deployment bootstrap token in the
`Authorization: Bearer …` header.

The token is HMAC-SHA256 derived from a server-side secret
(`settings.regiondeploy_callback_secret`) and the deployment's UUID,
namespaced with a context string so the same secret cannot be replayed
for any other purpose. The orchestrator derives the same token and
embeds it in the Ignition payload at /etc/dcim/callback.token so the
in-cluster Workflow action can include it in the POST.

If `regiondeploy_callback_secret` is unset, derive_callback_token()
returns None. Callers MUST treat that as "fail closed": refuse to embed
the token in Ignition and reject any callback. There is no plaintext
fallback by design.
"""

from __future__ import annotations

import hashlib
import hmac
import secrets
from uuid import UUID

# Domain-separation tag baked into the HMAC so the same callback secret
# cannot be re-used to forge any other deployment artifact.
_CTX = b"dcim/regiondeploy/kubeconfig-callback/v1"


def derive_callback_token(deployment_id: UUID, secret: str | None) -> str | None:
    """Return the bootstrap token for `deployment_id`, or None when the
    server-side secret is unset.

    Output is a hex-encoded HMAC-SHA256 digest (64 chars). The same
    inputs always produce the same token — both the orchestrator
    (writing Ignition) and the callback handler (verifying the request)
    call this function and compare with `compare_callback_token`."""
    if not secret:
        return None
    mac = hmac.new(secret.encode("utf-8"), _CTX, hashlib.sha256)
    mac.update(b":")
    mac.update(str(deployment_id).encode("ascii"))
    return mac.hexdigest()


def compare_callback_token(presented: str | None, expected: str | None) -> bool:
    """Constant-time compare of a bearer token against the expected
    value. Returns False when either input is None or empty."""
    if not presented or not expected:
        return False
    return secrets.compare_digest(presented, expected)
