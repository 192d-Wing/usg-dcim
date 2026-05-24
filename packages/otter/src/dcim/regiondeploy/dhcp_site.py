"""Render Helm values for the `dhcp-site` chart from a DhcpServer row.

PR 72 — Kea Control Agent (the URL DCIM stores in DhcpServer.kea_url)
gets an anycast IP via Cilium LB-IPAM + BGP. The chart deploys Kea
Control Agent and optionally kea-dhcp6 on the same Service IP; DHCPv4
broadcast stays at the network edge with RFC 1542 relay.

Companion to regiondeploy/dns_site.py — same dimensions: server
identity, role label, anycast IPs from the data model, bundle/config
references via Secrets the operator owns out-of-band.
"""

from __future__ import annotations

from typing import Any


def render_dhcp_site_values(
    server: Any,
    *,
    anycast_ips: list[str] | None = None,
    dhcpv6: bool = False,
    ctrl_agent_port: int = 8000,
    ctrl_agent_configmap: str = "kea-ctrl-agent-config",
    ctrl_agent_auth_secret: str = "kea-ctrl-agent-auth",
    ctrl_agent_tls_secret: str | None = None,
    replica_count: int = 2,
) -> dict:
    """Build the values dict for one DhcpServer release.

    `server` is a `models.ipam.DhcpServer` row (duck-typed on .id,
    .name, .fabric_id).

    `anycast_ips` is the list of IPs the Service should claim through
    Cilium's lbipam annotation. There is no `AnycastGroup` analogue
    for DHCP in the data model today; PR 72 takes the IPs as an
    argument so operators can pick them from their site allocation.
    If the schema later grows a DhcpAnycastGroup, swap this for a row
    fetch — the chart values shape doesn't change.
    """
    values: dict[str, Any] = {
        "server": {
            "id": str(server.id),
            "name": server.name,
            "fabricId": str(getattr(server, "fabric_id", "") or ""),
            "dhcpv6": bool(dhcpv6),
        },
        "service": {
            "type": "LoadBalancer",
            "anycastIPs": list(anycast_ips or []),
            "labels": {
                "dcim.io/bgp-advertise": "true",
                "dcim.io/dhcp-role": "ctrl-agent",
            },
            "annotations": {},
        },
        "ctrlAgent": {
            "port": int(ctrl_agent_port),
            "configMapName": ctrl_agent_configmap,
            "configKey": "kea-ctrl-agent.conf",
            "tls": {
                "enabled": bool(ctrl_agent_tls_secret),
                "serverCertSecret": ctrl_agent_tls_secret or "",
            },
            "basicAuth": {
                "secretName": ctrl_agent_auth_secret,
                "secretKey": "auth.csv",
            },
        },
        "replicaCount": int(replica_count),
    }
    return values
