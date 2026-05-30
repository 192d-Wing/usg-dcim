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
        # /dashboards/forecast/* still on Python (needs the
        # services/forecast port). Used as the smoke needle now that
        # /enterprise + /free-space + /sites/at-risk + /assets/{id} +
        # /sites/{id} + /racks/{id} all moved to otter-go.
        "/api/v1/dashboards/forecast/",
    ]:
        assert any(p.startswith(needle) for p in paths), f"missing route: {needle}"
    # /api/v1/auth/* (PR 179), /api/v1/telemetry/series (PR 178),
    # /api/v1/audit/* (PR 180), /api/v1/admin/* (PR #182 + capabilities/
    # dns follow-up), /api/v1/search (PR #187), /api/v1/dashboards/
    # enterprise (Phase 1) + /api/v1/dashboards/free-space (Phase 2
    # capacity port) all on otter-go. Negative-assert each is gone
    # from Python's OpenAPI so a regression that re-includes any of
    # those routers fails CI.
    for gone in (
        "/api/v1/auth",
        "/api/v1/audit",
        "/api/v1/telemetry/",
        "/api/v1/admin",
        "/api/v1/search",
        "/api/v1/dashboards/enterprise",
        "/api/v1/dashboards/free-space",
        "/api/v1/dashboards/sites/",
        "/api/v1/dashboards/assets/",
        "/api/v1/dashboards/racks/",
    ):
        assert not any(p.startswith(gone) for p in paths), (
            f"Python should not advertise {gone}* — otter-go is canonical"
        )
