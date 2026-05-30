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
        # /dashboards/free-space stays on Python until Phase 2 of the
        # dashboards port lands (needs services.capacity ported first).
        # Spot-checking it instead of /dashboards/enterprise, which
        # moved to otter-go.
        "/api/v1/dashboards/free-space",
    ]:
        assert any(p.startswith(needle) for p in paths), f"missing route: {needle}"
    # /api/v1/auth/* moved to otter-go (PR 179), /api/v1/telemetry/series
    # moved (PR 178), /api/v1/audit/* moved (PR 180), /api/v1/admin/*
    # fully moved (PR #182 + capabilities/dns follow-up), /api/v1/search
    # moved (PR #187), /api/v1/dashboards/enterprise moved (Phase 1 of
    # the dashboards port). Negative-assert each is gone from Python's
    # OpenAPI so a regression that re-includes any of those routers fails CI.
    for gone in (
        "/api/v1/auth",
        "/api/v1/audit",
        "/api/v1/telemetry/",
        "/api/v1/admin",
        "/api/v1/search",
        "/api/v1/dashboards/enterprise",
    ):
        assert not any(p.startswith(gone) for p in paths), (
            f"Python should not advertise {gone}* — otter-go is canonical"
        )
