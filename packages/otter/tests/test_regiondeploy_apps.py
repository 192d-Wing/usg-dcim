"""Unit tests for the per-app Helm values renderers (PR 10 partial).

Structural assertions only — Helm itself owns "is this a valid
values blob for chart vX"; what we lock here is the deployment-row
→ values shape, so a refactor can't quietly drop a v6-only switch
or a per-site replica count."""

from types import SimpleNamespace

import yaml

from dcim.regiondeploy.apps import (
    dump_values,
    render_cert_manager_values,
    render_collector_values,
    render_dhcp_values,
    render_dns_auth_values,
    render_dns_recursive_values,
)


def _deployment(config=None, **overrides):
    base = {
        "id": "01234567-89ab-cdef-0123-456789abcdef",
        "site_id": "fedcba98-7654-3210-fedc-ba9876543210",
        "name": "site42-prod",
        "config": config if config is not None else {
            "upstream_dns_v6": ["2001:4860:4860::8888"],
            "nat64_enabled": False,
            "dhcp_scopes": [
                {
                    "subnet": "fd00:site:42:0::/64",
                    "pool": "fd00:site:42:0::100-fd00:site:42:0::1ff",
                },
            ],
        },
    }
    base.update(overrides)
    return SimpleNamespace(**base)


def test_cert_manager_installs_crds_by_default():
    # The chart usually leaves installCRDs=false so a cluster-wide
    # cert-manager already installed elsewhere isn't disturbed. For
    # region-deploy the cluster is brand new — we want CRDs.
    v = render_cert_manager_values(_deployment())
    assert v["installCRDs"] is True


def test_dns_auth_v6_only_service():
    v = render_dns_auth_values(_deployment())
    # IPv6 single-stack — the LB stays inside the v6 production VLAN.
    assert v["service"]["ipFamilies"] == ["IPv6"]
    assert v["service"]["ipFamilyPolicy"] == "SingleStack"
    # CoreDNS port 53 is the canonical bind; locking this so a
    # refactor can't silently move it.
    assert v["servers"][0]["port"] == 53


def test_dns_auth_replicas_overridable_via_config():
    v = render_dns_auth_values(_deployment(config={"dns_auth_replicas": 5}))
    assert v["replicaCount"] == 5
    v_default = render_dns_auth_values(_deployment(config={}))
    assert v_default["replicaCount"] == 2  # the documented default


def test_dns_recursive_dns64_opt_in():
    # NAT64 + DNS64 is an opt-in feature per the doc (NAT46 LB
    # default; NAT64+DNS64 only when pods must reach v4-only
    # external endpoints).
    v_off = render_dns_recursive_values(_deployment(config={"nat64_enabled": False}))
    assert "dns64" not in v_off
    v_on = render_dns_recursive_values(_deployment(config={"nat64_enabled": True}))
    assert v_on["dns64"]["enabled"] is True
    assert v_on["dns64"]["prefix"] == "64:ff9b::/96"  # well-known NAT64 prefix


def test_dns_recursive_carries_upstreams():
    v = render_dns_recursive_values(_deployment(config={
        "upstream_dns_v6": ["2001:4860:4860::8888", "2606:4700:4700::1111"],
    }))
    assert v["upstreams"] == ["2001:4860:4860::8888", "2606:4700:4700::1111"]


def test_dhcp_v6_only_kea():
    # Kea on the site fabric serves v6-only — the v4 listener is
    # off because the production VLAN is v6. PXE / provisioning v4
    # is handled by Tinkerbell's Smee on the central cluster, not
    # by this Kea instance.
    v = render_dhcp_values(_deployment())
    assert v["dhcp6"]["enabled"] is True
    assert v["dhcp6"]["stateless"] is True
    assert v["dhcp4"]["enabled"] is False


def test_dhcp_carries_scopes_from_config():
    v = render_dhcp_values(_deployment(config={
        "dhcp_scopes": [
            {"subnet": "fd00:a::/64", "pool": "fd00:a::1-fd00:a::ff"},
            {"subnet": "fd00:b::/64", "pool": "fd00:b::1-fd00:b::ff"},
        ],
    }))
    subnets = v["dhcp6"]["subnets"]
    assert [s["subnet"] for s in subnets] == ["fd00:a::/64", "fd00:b::/64"]


def test_collector_carries_site_and_deployment_ids():
    # The collector identifies its site to the central backend via
    # these fields; the enrollment flow (PR 11 seed stage) uses
    # them as the lookup key.
    v = render_collector_values(_deployment())
    assert v["site"]["id"] == "fedcba98-7654-3210-fedc-ba9876543210"
    assert v["site"]["deployment_id"] == "01234567-89ab-cdef-0123-456789abcdef"
    # Central API URL + enrollment token are placeholders the seed
    # stage fills in. Locking the slots so the chart's expected
    # values shape stays stable.
    assert "api_url" in v["central"]
    assert "enrollment_token" in v["central"]


def test_collector_default_replicas():
    # One collector per site by design — multiple pods would
    # double-enroll against the central API.
    v = render_collector_values(_deployment())
    assert v["replicaCount"] == 1


def test_dump_values_round_trips():
    # Helm consumes the YAML; a non-round-trippable values dict
    # would fail at install time, not at render. Lock the
    # serialisation now.
    v = render_dns_recursive_values(_deployment())
    out = dump_values(v)
    assert "---" not in out
    assert yaml.safe_load(out)["service"]["ipFamilies"] == ["IPv6"]


def test_all_renderers_attach_deployment_id_for_traceability():
    # The render-stage events carry the deployment id, but having
    # it inside the values blob too lets an operator running a
    # helm install grep for `dcim.deployment_id: <uuid>` to
    # confirm which deploy a release came from.
    dep = _deployment()
    expected = "01234567-89ab-cdef-0123-456789abcdef"
    assert render_cert_manager_values(dep)["dcim"]["deployment_id"] == expected
    assert render_dns_auth_values(dep)["dcim"]["deployment_id"] == expected
    assert render_dns_recursive_values(dep)["dcim"]["deployment_id"] == expected
    assert render_dhcp_values(dep)["dcim"]["deployment_id"] == expected
    # collector uses `site.deployment_id` (nested under site/) — see
    # render_collector_values for why; lock that placement.
    assert render_collector_values(dep)["site"]["deployment_id"] == expected
