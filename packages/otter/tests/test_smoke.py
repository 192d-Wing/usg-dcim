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
    # spot-check the key surfaces are mounted. /api/v1/inventory/*
    # (including cables) is fully on otter-go now; the umbrella chart
    # routes the whole prefix via a single rule.
    for needle in [
        "/api/v1/collectors",
        "/api/v1/ingest/telemetry",
    ]:
        assert any(p.startswith(needle) for p in paths), f"missing route: {needle}"
    # /api/v1/auth/* (PR 179), /api/v1/telemetry/series (PR 178),
    # /api/v1/audit/* (PR 180), /api/v1/admin/* (PR #182 + capabilities/
    # dns follow-up), /api/v1/search (PR #187), /api/v1/dashboards/*
    # (PRs #188-#194), /api/v1/inventory/* (PR #195 + cables PATCH
    # follow-up) all on otter-go. Negative-assert each is gone from
    # Python's OpenAPI so a regression that re-includes any of those
    # routers fails CI.
    for gone in (
        "/api/v1/auth",
        "/api/v1/audit",
        "/api/v1/telemetry/",
        "/api/v1/admin",
        "/api/v1/search",
        "/api/v1/dashboards",
        "/api/v1/inventory",
        # BGP module fully on otter-go (TCP-AO in PRs #203/#204; the
        # rest cut over here). URL prefix is /bgp (capability
        # namespace `routing:*` is a separate concept).
        "/api/v1/bgp",
        # /api/v1/alerts/* (this PR — list/ack, rules CRUD,
        # maintenance-windows CRUD). The arq eval loop lives in
        # services/alerts.py which is untouched.
        "/api/v1/alerts",
        # /api/v1/dns/bgp-peers/* moved to otter-go. The rest of
        # /api/v1/dns/* stays Python-canonical until the full DNS
        # module is cut over, so the negative-assert targets the
        # specific subprefix that left.
        "/api/v1/dns/bgp-peers",
        # /api/v1/notifications/* fully moved to otter-go (channels
        # list/create/patch/delete were already there; this PR added
        # the test endpoint). services/notifications.py stays in
        # Python because the alert eval loop still uses it.
        "/api/v1/notifications",
        # /api/v1/ipam/* fully moved to otter-go (PR 17 cutover for
        # DHCP; PR 18 cutover for the rest — fabrics, vrfs,
        # vrf-bgp-peers, supernets, subnets, addresses, overlays,
        # vnis, vteps, vtep-memberships, free-space, utilization,
        # bulk endpoints). The broader prefix supersedes the
        # /api/v1/ipam/dhcp negative-assert that lived here for PR 17.
        "/api/v1/ipam",
        # /api/v1/organizations + /api/v1/stencils fully moved to
        # otter-go (PR 19). Go's internal/organization has CRUD
        # parity; internal/stencils serves the static catalog LIST.
        "/api/v1/organizations",
        "/api/v1/stencils",
    ):
        assert not any(p.startswith(gone) for p in paths), (
            f"Python should not advertise {gone}* — otter-go is canonical"
        )
