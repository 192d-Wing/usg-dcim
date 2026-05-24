"""Unit tests for the dns-site Helm values renderer (PR 71).

Pure renderer — no DB, no async. Each test pins one mapping from
DnsServer/AnycastGroup fields onto the chart's values.yaml shape so a
schema change on either side breaks loudly.
"""

from __future__ import annotations

from dataclasses import dataclass
from uuid import UUID, uuid4

from dcim.regiondeploy.dns_site import render_dns_site_values


@dataclass
class _Server:
    id: UUID
    name: str
    role: str
    fabric_id: UUID
    site_id: UUID
    unicast_ip: str | None = None


@dataclass
class _AnycastGroup:
    anycast_ipv4: str | None = None
    anycast_ipv6: str | None = None


def _server(role: str = "recursive") -> _Server:
    return _Server(
        id=uuid4(), name="dns-east-1", role=role,
        fabric_id=uuid4(), site_id=uuid4(),
    )


def test_recursive_server_with_dual_stack_anycast_emits_both_ips():
    server = _server("recursive")
    grp = _AnycastGroup(anycast_ipv4="192.0.2.53", anycast_ipv6="2001:db8:dns::53")
    v = render_dns_site_values(
        server, anycast_group=grp,
        bundle_api_base_url="https://dcim.example.mil",
    )
    assert v["service"]["anycastIPs"] == ["192.0.2.53", "2001:db8:dns::53"]
    assert v["service"]["labels"]["dcim.io/dns-role"] == "recursive"
    assert v["service"]["labels"]["dcim.io/bgp-advertise"] == "true"


def test_v6_only_anycast_group_omits_v4_address():
    server = _server("recursive")
    grp = _AnycastGroup(anycast_ipv4=None, anycast_ipv6="2001:db8:dns::53")
    v = render_dns_site_values(
        server, anycast_group=grp,
        bundle_api_base_url="https://dcim.example.mil",
    )
    assert v["service"]["anycastIPs"] == ["2001:db8:dns::53"]


def test_auth_server_has_no_anycast_ips_and_role_label_says_so():
    # PR 71 — auth role is unicast-only; the Service is still LB-typed
    # but the renderer leaves anycastIPs empty so the IP comes from the
    # cluster's LB pool rather than being pinned by lbipam annotation.
    server = _server("auth")
    v = render_dns_site_values(
        server, anycast_group=None,
        bundle_api_base_url="https://dcim.example.mil",
    )
    assert v["service"]["anycastIPs"] == []
    assert v["service"]["labels"]["dcim.io/dns-role"] == "auth"
    assert v["service"]["type"] == "LoadBalancer"


def test_bundle_section_pulls_url_token_secret_and_poll():
    server = _server("recursive")
    v = render_dns_site_values(
        server, anycast_group=None,
        bundle_api_base_url="https://dcim.example.mil",
        bundle_token_secret_name="my-token",
        bundle_token_secret_key="key",
        poll_seconds=30,
    )
    assert v["bundle"] == {
        "apiBaseUrl": "https://dcim.example.mil",
        "tokenSecretName": "my-token",
        "tokenSecretKey": "key",
        "pollSeconds": 30,
    }


def test_optional_ca_secret_only_appears_when_provided():
    server = _server("recursive")
    v_no_ca = render_dns_site_values(
        server, anycast_group=None,
        bundle_api_base_url="https://dcim.example.mil",
    )
    assert "caBundleSecretName" not in v_no_ca["bundle"]
    v_with_ca = render_dns_site_values(
        server, anycast_group=None,
        bundle_api_base_url="https://dcim.example.mil",
        bundle_ca_secret_name="dcim-ca",
    )
    assert v_with_ca["bundle"]["caBundleSecretName"] == "dcim-ca"


def test_server_identity_carries_through_as_strings():
    server = _server("recursive")
    v = render_dns_site_values(
        server, anycast_group=None,
        bundle_api_base_url="https://dcim.example.mil",
    )
    assert v["server"]["id"] == str(server.id)
    assert v["server"]["name"] == "dns-east-1"
    assert v["server"]["role"] == "recursive"
    assert v["server"]["fabricId"] == str(server.fabric_id)
    assert v["server"]["siteId"] == str(server.site_id)
