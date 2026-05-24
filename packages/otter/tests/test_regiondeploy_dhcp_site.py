"""Unit tests for the dhcp-site Helm values renderer (PR 72)."""

from __future__ import annotations

from dataclasses import dataclass
from uuid import UUID, uuid4

from dcim.regiondeploy.dhcp_site import render_dhcp_site_values


@dataclass
class _Server:
    id: UUID
    name: str
    fabric_id: UUID


def _server() -> _Server:
    return _Server(id=uuid4(), name="kea-east-1", fabric_id=uuid4())


def test_default_values_omit_dhcpv6_and_tls():
    server = _server()
    v = render_dhcp_site_values(server, anycast_ips=["192.0.2.67"])
    assert v["server"]["dhcpv6"] is False
    assert v["ctrlAgent"]["tls"]["enabled"] is False
    assert v["service"]["anycastIPs"] == ["192.0.2.67"]
    assert v["service"]["labels"]["dcim.io/bgp-advertise"] == "true"
    assert v["service"]["labels"]["dcim.io/dhcp-role"] == "ctrl-agent"


def test_dual_stack_anycast_lands_both_ips():
    server = _server()
    v = render_dhcp_site_values(
        server, anycast_ips=["192.0.2.67", "2001:db8:dhcp::67"], dhcpv6=True,
    )
    assert v["service"]["anycastIPs"] == ["192.0.2.67", "2001:db8:dhcp::67"]
    assert v["server"]["dhcpv6"] is True


def test_tls_secret_flips_tls_enabled():
    server = _server()
    v = render_dhcp_site_values(
        server, anycast_ips=["192.0.2.67"],
        ctrl_agent_tls_secret="kea-ctrl-agent-tls",
    )
    assert v["ctrlAgent"]["tls"]["enabled"] is True
    assert v["ctrlAgent"]["tls"]["serverCertSecret"] == "kea-ctrl-agent-tls"


def test_no_anycast_ips_emits_empty_list_not_none():
    server = _server()
    v = render_dhcp_site_values(server)
    assert v["service"]["anycastIPs"] == []


def test_server_identity_is_string_serialized():
    server = _server()
    v = render_dhcp_site_values(server, anycast_ips=["192.0.2.67"])
    assert v["server"]["id"] == str(server.id)
    assert v["server"]["fabricId"] == str(server.fabric_id)
    assert v["server"]["name"] == "kea-east-1"


def test_ctrl_agent_overrides_propagate():
    server = _server()
    v = render_dhcp_site_values(
        server, anycast_ips=["192.0.2.67"],
        ctrl_agent_port=9000,
        ctrl_agent_configmap="my-kea-cfg",
        ctrl_agent_auth_secret="my-kea-auth",
    )
    assert v["ctrlAgent"]["port"] == 9000
    assert v["ctrlAgent"]["configMapName"] == "my-kea-cfg"
    assert v["ctrlAgent"]["basicAuth"]["secretName"] == "my-kea-auth"
