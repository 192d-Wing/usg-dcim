"""Render Helm values for the `dns-site` chart from a DnsServer row.

PR 71 — Site DNS goes k8s-native. The existing bundle pipeline
(services/dns.render_bundle_for_server) keeps owning Corefile/zone
authoring; this renderer owns the *packaging* — how a DnsServer maps
to a Helm release in the site cluster.

Output is a plain dict suitable for `helm install -f <values>` or
`helm template`. The dict's shape matches `deploy/helm/dns-site/values.yaml`
1:1; bumping that schema requires bumping this renderer in lockstep.

The renderer does NOT pull the AnycastBgpBinding rows — those drove
per-peer GoBGP config in the old world. In the k8s-native world the
cluster's existing CiliumBGPClusterConfig (rendered by
regiondeploy/cilium.py) owns the peer list; the dns-site Service just
carries a label the umbrella's CiliumBGPAdvertisement matches on. So
bindings remain in the schema as an audit/policy record but stop
driving config generation.
"""

from __future__ import annotations

from typing import Any


def render_dns_site_values(
    server: Any,
    *,
    anycast_group: Any | None = None,
    bundle_api_base_url: str,
    bundle_token_secret_name: str = "dcim-dns-site-token",
    bundle_token_secret_key: str = "token",
    bundle_ca_secret_name: str | None = None,
    poll_seconds: int = 60,
    replica_count: int = 1,
) -> dict:
    """Build the values dict for one DnsServer release.

    `server` is a `models.dns.DnsServer` row (kept untyped here to dodge
    a circular import with services/dns.py — duck-typed on .id, .name,
    .role, .fabric_id, .site_id, .unicast_ip).

    `anycast_group` is the matching `models.dns.AnycastGroup` row when
    server.role == "recursive" and the recursive engine binds to one.
    Pass None for auth servers — auth.role is unicast-only and the
    Service ends up with no anycast IPs (the LB IP comes from a pool
    instead).
    """
    role = str(server.role)
    anycast_ips: list[str] = []
    if anycast_group is not None:
        if getattr(anycast_group, "anycast_ipv4", None):
            anycast_ips.append(str(anycast_group.anycast_ipv4))
        if getattr(anycast_group, "anycast_ipv6", None):
            anycast_ips.append(str(anycast_group.anycast_ipv6))

    values: dict[str, Any] = {
        "server": {
            "id": str(server.id),
            "name": server.name,
            "role": role,
            "fabricId": str(getattr(server, "fabric_id", "") or ""),
            "siteId": str(getattr(server, "site_id", "") or ""),
        },
        "service": {
            "type": "LoadBalancer",
            "port": 53,
            "anycastIPs": anycast_ips,
            "labels": {
                "dcim.io/bgp-advertise": "true",
                "dcim.io/dns-role": role,
            },
            "annotations": {},
        },
        "bundle": {
            "apiBaseUrl": bundle_api_base_url,
            "tokenSecretName": bundle_token_secret_name,
            "tokenSecretKey": bundle_token_secret_key,
            "pollSeconds": int(poll_seconds),
        },
        "replicaCount": int(replica_count),
    }
    if bundle_ca_secret_name:
        values["bundle"]["caBundleSecretName"] = bundle_ca_secret_name
    return values
