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
        "/api/v1/auth/me",
    ]:
        assert any(p.startswith(needle) for p in paths), f"missing route: {needle}"
