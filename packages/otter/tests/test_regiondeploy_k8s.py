"""Unit tests for the regiondeploy.k8s thin client.

Uses httpx's MockTransport so we never hit a real Kubernetes API.
The point is to lock the URL construction + body shape + error
handling — anything that would surprise an operator who's read
the kube API docs but not our client code."""

import base64
import json

import httpx
import pytest

from dcim.regiondeploy.k8s import (
    K8sClient,
    K8sConflictError,
    K8sError,
    _kind_to_plural,
    _resource_url,
)


def _client(handler):
    """Build a K8sClient pointed at a mock httpx transport.

    The handler signature is `request -> Response`; tests inspect
    the request to assert on URL + body and return a synthetic
    Response. Avoids the K8sClient constructor's verify= /
    SA-token reads."""
    transport = httpx.MockTransport(handler)
    c = K8sClient.__new__(K8sClient)
    c._client = httpx.AsyncClient(
        base_url="https://kube.test",
        headers={"Authorization": "Bearer fake-token"},
        transport=transport,
        timeout=5.0,
    )
    return c


# ── _resource_url ──────────────────────────────────────────────────


@pytest.mark.parametrize(("kind", "expected"), [
    ("Secret", "secrets"),
    ("Hardware", "hardware"),
    ("Workflow", "workflows"),
])
def test_kind_to_plural_known_kinds(kind, expected):
    assert _kind_to_plural(kind) == expected


def test_kind_to_plural_unknown_kind_raises():
    with pytest.raises(ValueError, match="Pod"):
        _kind_to_plural("Pod")


def test_resource_url_core_group_namespaced():
    # core/v1 (group=="") uses /api/v1, not /apis/.../v1.
    u = _resource_url(
        group="", version="v1", plural="secrets",
        namespace="tinkerbell", name="kubeconfig-x",
    )
    assert u == "/api/v1/namespaces/tinkerbell/secrets/kubeconfig-x"


def test_resource_url_named_group_namespaced():
    u = _resource_url(
        group="tinkerbell.org", version="v1alpha1", plural="hardware",
        namespace="tinkerbell", name="rd-abc-node1",
    )
    assert u == "/apis/tinkerbell.org/v1alpha1/namespaces/tinkerbell/hardware/rd-abc-node1"


def test_resource_url_cluster_scoped():
    # namespace=None must produce the cluster-scoped path. Catches
    # the bug where a cluster-scoped CR accidentally gets wrapped
    # in /namespaces/<None>/.
    u = _resource_url(
        group="bmc.tinkerbell.org", version="v1alpha1", plural="machines",
        namespace=None, name="m1",
    )
    assert u == "/apis/bmc.tinkerbell.org/v1alpha1/machines/m1"


# ── create_secret ──────────────────────────────────────────────────


@pytest.mark.asyncio
async def test_create_secret_base64_encodes_values():
    seen = {}

    def handler(req: httpx.Request) -> httpx.Response:
        seen["url"] = str(req.url)
        seen["body"] = json.loads(req.content)
        return httpx.Response(201, json={"ok": True})

    c = _client(handler)
    try:
        await c.create_secret(
            "tinkerbell", "kubeconfig-xyz",
            {"kubeconfig": "apiVersion: v1\nclusters: []"},
            labels={"dcim.region-deployment": "abc"},
        )
    finally:
        await c.aclose()

    assert seen["url"].endswith("/api/v1/namespaces/tinkerbell/secrets")
    body = seen["body"]
    assert body["kind"] == "Secret"
    assert body["metadata"]["labels"]["dcim.region-deployment"] == "abc"
    # Values must be base64 — the API will reject raw bytes in `data`.
    encoded = body["data"]["kubeconfig"]
    assert base64.b64decode(encoded).decode() == "apiVersion: v1\nclusters: []"


@pytest.mark.asyncio
async def test_create_secret_raises_conflict_on_409():
    def handler(_req):
        return httpx.Response(
            409, json={"message": "already exists"}, text="already exists",
        )

    c = _client(handler)
    try:
        with pytest.raises(K8sConflictError):
            await c.create_secret("tinkerbell", "x", {"k": "v"})
    finally:
        await c.aclose()


@pytest.mark.asyncio
async def test_create_or_replace_falls_back_to_put_on_conflict():
    # First call: 409 (already exists). Client should retry via PUT.
    # Verifies the idempotent-write contract the callback endpoint
    # relies on so a kubeadm-init retry doesn't fail.
    calls = []

    def handler(req):
        calls.append((req.method, str(req.url)))
        if req.method == "POST":
            return httpx.Response(409, text="exists")
        return httpx.Response(200, json={"ok": True})

    c = _client(handler)
    try:
        await c.create_or_replace_secret(
            "tinkerbell", "kubeconfig-xyz", {"kubeconfig": "yaml"},
        )
    finally:
        await c.aclose()

    assert [m for m, _ in calls] == ["POST", "PUT"]
    # The PUT URL is the named-resource form, not the collection.
    assert calls[1][1].endswith(
        "/api/v1/namespaces/tinkerbell/secrets/kubeconfig-xyz",
    )


# ── get_secret ─────────────────────────────────────────────────────


@pytest.mark.asyncio
async def test_get_secret_returns_none_on_404():
    def handler(_req):
        return httpx.Response(404, text="not found")

    c = _client(handler)
    try:
        assert await c.get_secret("tinkerbell", "missing") is None
    finally:
        await c.aclose()


@pytest.mark.asyncio
async def test_get_secret_returns_body_on_200():
    def handler(_req):
        return httpx.Response(200, json={"metadata": {"name": "x"}})

    c = _client(handler)
    try:
        got = await c.get_secret("tinkerbell", "x")
        assert got["metadata"]["name"] == "x"
    finally:
        await c.aclose()


@pytest.mark.asyncio
async def test_get_secret_raises_on_other_errors():
    def handler(_req):
        return httpx.Response(500, text="boom")

    c = _client(handler)
    try:
        with pytest.raises(K8sError) as exc_info:
            await c.get_secret("tinkerbell", "x")
        assert exc_info.value.status == 500
    finally:
        await c.aclose()


# ── server_side_apply ──────────────────────────────────────────────


@pytest.mark.asyncio
async def test_server_side_apply_sends_apply_patch_yaml_content_type():
    # SSA requires Content-Type: application/apply-patch+yaml (or
    # +json). httpx wraps PATCH bodies, but K8s rejects PATCH
    # without the right content type — this is exactly the kind of
    # thing that fails silently in production and is easy to test
    # here.
    seen = {}

    def handler(req):
        seen["method"] = req.method
        seen["url"] = str(req.url)
        seen["content_type"] = req.headers.get("content-type")
        seen["params"] = dict(req.url.params)
        return httpx.Response(200, json={"ok": True})

    c = _client(handler)
    try:
        await c.server_side_apply(
            api_version="tinkerbell.org/v1alpha1",
            kind="Hardware",
            namespace="tinkerbell",
            name="rd-abc-node1",
            body={"apiVersion": "tinkerbell.org/v1alpha1", "kind": "Hardware"},
        )
    finally:
        await c.aclose()

    assert seen["method"] == "PATCH"
    assert seen["content_type"] == "application/apply-patch+yaml"
    # URL carries SSA query params; check the path portion only.
    assert (
        "/apis/tinkerbell.org/v1alpha1/namespaces/tinkerbell/"
        "hardware/rd-abc-node1"
    ) in seen["url"]
    # force=true is part of the SSA contract — without it a foreign
    # field-manager owning a field would block our update.
    assert seen["params"]["force"] == "true"
    assert seen["params"]["fieldManager"] == "dcim-region-deploy"


@pytest.mark.asyncio
async def test_server_side_apply_cluster_scoped_skips_namespace():
    seen = {}

    def handler(req):
        seen["url"] = str(req.url)
        return httpx.Response(200, json={"ok": True})

    c = _client(handler)
    try:
        await c.server_side_apply(
            api_version="bmc.tinkerbell.org/v1alpha1",
            kind="Machine",
            namespace=None,
            name="m1",
            body={"apiVersion": "bmc.tinkerbell.org/v1alpha1", "kind": "Machine"},
        )
    finally:
        await c.aclose()

    assert "/namespaces/" not in seen["url"]
    assert (
        "/apis/bmc.tinkerbell.org/v1alpha1/machines/m1"
    ) in seen["url"]
