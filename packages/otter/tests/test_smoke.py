"""Smoke tests — exercise the FastAPI factory wiring without a real DB."""

from fastapi.testclient import TestClient


def test_app_creates() -> None:
    from dcim.main import create_app

    app = create_app()
    client = TestClient(app)
    r = client.get("/healthz")
    assert r.status_code == 200
    assert r.json()["status"] == "ok"


def test_openapi_published() -> None:
    from dcim.main import create_app

    app = create_app()
    client = TestClient(app)
    r = client.get("/openapi.json")
    assert r.status_code == 200
    paths = r.json()["paths"]
    # spot-check the key surfaces are mounted
    for needle in [
        "/api/v1/inventory/sites",
        "/api/v1/inventory/racks",
        "/api/v1/inventory/assets",
        "/api/v1/inventory/cables",
        "/api/v1/collectors",
        "/api/v1/ingest/telemetry",
        "/api/v1/alerts",
        "/api/v1/dashboards/enterprise",
        "/api/v1/search",
    ]:
        assert any(p.startswith(needle) for p in paths), f"missing route: {needle}"
    # /api/v1/auth/* moved to otter-go (PR 179) — assert it's *gone*
    # from Python's OpenAPI so this test catches a regression where
    # the router gets re-included alongside the otter-go canonical.
    assert not any(p.startswith("/api/v1/auth") for p in paths), (
        "Python should not advertise /api/v1/auth/* — otter-go is canonical"
    )
