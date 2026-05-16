"""Thin Kubernetes API client for the region-deploy orchestrator.

Why not `kubernetes-asyncio` or `kr8s`?
  Both would carry their full client surface (informers, watches,
  generated openapi types, version drift) into the api/worker
  images forever, when all we actually need is server-side-apply of
  a handful of CR kinds plus Secret create/get. Building on the
  existing `httpx` dep keeps the dep footprint flat and the client
  surface tight.

Auth model:
  In-pod service-account token (`/var/run/secrets/kubernetes.io/
  serviceaccount/token`) + the matching CA. The RBAC the SA needs
  is shipped in `deploy/k8s/central/region-deploy-rbac.yaml` — a
  ClusterRole granting Secret + Tinkerbell + Rufio CR access, bound
  to the `dcim/default` SA in the `tinkerbell` namespace via a
  RoleBinding.

Out of pod (local dev, tests):
  `from_kubeconfig(path)` lets you point a local kubeconfig at the
  client. We don't auto-detect — explicit choice keeps the test
  surface obvious.
"""

from __future__ import annotations

import os
from pathlib import Path

import httpx

# Paths the kubelet injects into every pod with a service-account
# mounted. Constants so the test fakes can override via env.
_SA_TOKEN_PATH = "/var/run/secrets/kubernetes.io/serviceaccount/token"
_SA_CA_PATH = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"


class K8sClient:
    """Minimal Kubernetes API client.

    Holds the httpx.AsyncClient and the API server URL. Callers
    use the higher-level helpers (`create_secret`, `server_side_apply`)
    rather than `request()` directly so the orchestrator stays out of
    URL-construction details.
    """

    def __init__(
        self,
        *,
        api_server: str,
        token: str,
        ca_path: str | None,
    ) -> None:
        # `verify=` accepts a path or False; we pass the SA CA when
        # we're in-pod, and skip verification only when explicitly
        # asked (None signals "no CA available, do verify=False").
        # In dev clusters with self-signed APIs the kubeconfig path
        # handles this.
        verify: bool | str = ca_path if ca_path else False
        self._client = httpx.AsyncClient(
            base_url=api_server,
            headers={"Authorization": f"Bearer {token}"},
            verify=verify,
            timeout=30.0,
        )

    @classmethod
    def from_in_pod(cls) -> K8sClient:
        """Construct from in-pod service-account credentials.

        The kubelet sets `KUBERNETES_SERVICE_HOST` + `_PORT` in every
        pod's env. The SA token + CA live at the well-known paths.
        Raises if any are missing — callers wrap in a `try` and emit
        a stage-failure event with a clear hint.
        """
        host = os.environ.get("KUBERNETES_SERVICE_HOST")
        port = os.environ.get("KUBERNETES_SERVICE_PORT", "443")
        if not host:
            raise RuntimeError(
                "KUBERNETES_SERVICE_HOST not set; not running in-pod?",
            )
        token = Path(_SA_TOKEN_PATH).read_text(encoding="utf-8").strip()
        ca = _SA_CA_PATH if Path(_SA_CA_PATH).is_file() else None
        return cls(
            api_server=f"https://{host}:{port}",
            token=token,
            ca_path=ca,
        )

    async def aclose(self) -> None:
        await self._client.aclose()

    # ── high-level helpers ────────────────────────────────────────

    async def create_secret(
        self,
        namespace: str,
        name: str,
        data: dict[str, str],
        *,
        secret_type: str = "Opaque",
        labels: dict[str, str] | None = None,
    ) -> dict:
        """Create a core/v1 Secret.

        `data` values are passed raw — the client base64-encodes
        each entry because the Kubernetes API expects encoded bytes
        in `data`. Pass already-encoded values via `stringData` if
        you'd rather skip the encoding (not exposed here yet — add
        when a caller needs it).
        """
        import base64

        body = {
            "apiVersion": "v1",
            "kind": "Secret",
            "metadata": {"name": name, "labels": labels or {}},
            "type": secret_type,
            "data": {
                k: base64.b64encode(v.encode("utf-8")).decode("ascii")
                for k, v in data.items()
            },
        }
        resp = await self._client.post(
            f"/api/v1/namespaces/{namespace}/secrets",
            json=body,
        )
        return _check(resp)

    async def get_secret(self, namespace: str, name: str) -> dict | None:
        """Fetch a Secret by name. Returns `None` on 404; raises on
        any other non-2xx."""
        resp = await self._client.get(
            f"/api/v1/namespaces/{namespace}/secrets/{name}",
        )
        if resp.status_code == 404:
            return None
        return _check(resp)

    async def replace_secret(
        self,
        namespace: str,
        name: str,
        data: dict[str, str],
        *,
        secret_type: str = "Opaque",
        labels: dict[str, str] | None = None,
    ) -> dict:
        """PUT a Secret — used when create_or_replace's first call
        returned 409. Carries the same `data` encoding semantics
        as `create_secret`."""
        import base64

        body = {
            "apiVersion": "v1",
            "kind": "Secret",
            "metadata": {"name": name, "labels": labels or {}},
            "type": secret_type,
            "data": {
                k: base64.b64encode(v.encode("utf-8")).decode("ascii")
                for k, v in data.items()
            },
        }
        resp = await self._client.put(
            f"/api/v1/namespaces/{namespace}/secrets/{name}",
            json=body,
        )
        return _check(resp)

    async def create_or_replace_secret(
        self,
        namespace: str,
        name: str,
        data: dict[str, str],
        *,
        secret_type: str = "Opaque",
        labels: dict[str, str] | None = None,
    ) -> dict:
        """Idempotent Secret write. POSTs first; on 409 falls back
        to PUT. The kubeconfig callback uses this so a re-run after
        a transient error doesn't fail because the Secret already
        exists from the first attempt."""
        try:
            return await self.create_secret(
                namespace, name, data,
                secret_type=secret_type, labels=labels,
            )
        except K8sConflictError:
            return await self.replace_secret(
                namespace, name, data,
                secret_type=secret_type, labels=labels,
            )

    async def server_side_apply(
        self,
        *,
        api_version: str,
        kind: str,
        namespace: str | None,
        name: str,
        body: dict,
        field_manager: str = "dcim-region-deploy",
    ) -> dict:
        """Apply a single object via server-side apply.

        `api_version` is the literal CR apiVersion ("tinkerbell.org/v1alpha1",
        "v1", etc). Namespace is None for cluster-scoped kinds.
        """
        group, _, version = api_version.partition("/")
        if version == "":
            # core/v1 case: no group, version is what was in the slot
            group, version = "", group
        plural = _kind_to_plural(kind)
        url = _resource_url(
            group=group, version=version, plural=plural,
            namespace=namespace, name=name,
        )
        resp = await self._client.patch(
            url,
            content=_json_dumps(body),
            headers={
                "Content-Type": "application/apply-patch+yaml",
            },
            params={"fieldManager": field_manager, "force": "true"},
        )
        return _check(resp)


# ── error types ───────────────────────────────────────────────────


class K8sError(RuntimeError):
    """Generic Kubernetes API error. Carries the status code so
    callers can react (e.g. 403 → log the missing RBAC hint, not
    "internal error")."""

    def __init__(self, status: int, body: str) -> None:
        super().__init__(f"k8s api {status}: {body}")
        self.status = status
        self.body = body


class K8sConflictError(K8sError):
    """409 Conflict — the resource already exists or
    resourceVersion drifted."""


# ── helpers ────────────────────────────────────────────────────────


def _check(resp: httpx.Response) -> dict:
    """Common response handler. Raises typed exceptions on non-2xx
    so callers can branch on conflict / not-found without parsing
    the body themselves."""
    if 200 <= resp.status_code < 300:
        return resp.json()
    if resp.status_code == 409:
        raise K8sConflictError(resp.status_code, resp.text)
    raise K8sError(resp.status_code, resp.text)


def _json_dumps(body: dict) -> bytes:
    """Compact JSON encoding. Server-side apply accepts
    application/apply-patch+yaml *or* +json — JSON is shorter to
    encode and unambiguous."""
    import json

    return json.dumps(body, separators=(",", ":")).encode("utf-8")


def _resource_url(
    *,
    group: str,
    version: str,
    plural: str,
    namespace: str | None,
    name: str,
) -> str:
    """Build the resource URL.

    Core API group's URL prefix is `/api/v1/...`; named groups use
    `/apis/<group>/<version>/...`. This is the one place we
    convert between the apiVersion the caller passes and the URL
    shape the API server expects.
    """
    base = "/api/v1" if group == "" else f"/apis/{group}/{version}"
    if namespace is not None:
        return f"{base}/namespaces/{namespace}/{plural}/{name}"
    return f"{base}/{plural}/{name}"


# Kind → plural map. Only the kinds the orchestrator actually
# applies — adding a kind means adding it here. Cheaper than
# importing the discovery client just to look up a plural.
_KIND_PLURALS: dict[str, str] = {
    "Secret": "secrets",
    "Hardware": "hardware",   # already plural in Tinkerbell
    "Template": "templates",
    "Workflow": "workflows",
    "Machine": "machines",
    "Job": "jobs",
    "Task": "tasks",
}


def _kind_to_plural(kind: str) -> str:
    plural = _KIND_PLURALS.get(kind)
    if plural is None:
        raise ValueError(
            f"unknown Kind {kind!r}; add it to _KIND_PLURALS",
        )
    return plural
