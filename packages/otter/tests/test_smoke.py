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
    # /api/v1/auth/* moved to otter-go (PR 179), /api/v1/telemetry/series
    # moved (PR 178), /api/v1/audit/* moved (PR 180), most of /api/v1/admin
    # moved (PR-admin). Negative-assert each *moved* prefix is gone from
    # Python's OpenAPI so this test catches a regression where any router
    # gets re-included alongside the otter-go canonical.
    #
    # /api/v1/admin/* is partial: capabilities + system DNS stay on
    # Python so the 4 moved subpaths are asserted individually here.
    for gone in (
        "/api/v1/auth",
        "/api/v1/audit",
        "/api/v1/telemetry/",
        "/api/v1/admin/users",
        "/api/v1/admin/roles",
        "/api/v1/admin/assignments",
        "/api/v1/admin/oidc-role-mappings",
    ):
        assert not any(p.startswith(gone) for p in paths), (
            f"Python should not advertise {gone}* — otter-go is canonical"
        )
    # Conversely, the routes that intentionally stay on Python must
    # still be present — guards against accidentally deleting them
    # along with the moved sections.
    for stays in (
        "/api/v1/admin/capabilities/catalog",
        "/api/v1/admin/system/dns-settings",
    ):
        assert any(p == stays for p in paths), (
            f"{stays} should still be on Python — Go port hasn't landed"
        )
