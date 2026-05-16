"""Helm values renderers for the site service stack.

The `apps.*` orchestrator stages each call into a renderer here to
produce Helm values for the chart that owns that stage. Same
render-but-don't-apply pattern as `cilium.py` and `crd.py` — apply
waits on the regional-cluster kubeconfig retrieval workstream.

Apps covered (one stage per app, names matching the orchestrator's
STAGES list):

  * cert-manager   — TLS material for DNS/Hickory/Kea sidecars.
                     Upstream jetstack chart.
  * dns_auth       — CoreDNS authoritative pods.
  * dns_recursive  — Hickory recursive resolver.
  * dhcp           — Kea DHCPv6 + control-agent REST.
  * collector      — usg-dcim go-collector for site telemetry.

Why one module:
  These are small Helm values dicts. Splitting into five modules
  buys nothing but more imports. If any single renderer grows past
  ~80 LOC it's a sign that chart belongs in its own file alongside
  whatever module owns the corresponding deploy stage (Phase 11 in
  the doc covers richer install steps).

Chart sources are placeholders for now: the operator's helm install
command picks the actual chart ref + version, and these renderers
emit values that ride along via `-f -`. When we land the apply path
the chart refs come with it.
"""

from __future__ import annotations

from typing import Any

import yaml


def render_cert_manager_values(deployment: Any) -> dict:
    """Upstream `jetstack/cert-manager`.

    We enable CRD installation by default; per-region clusters don't
    bring their own cert-manager. ACME / Let's Encrypt issuers are
    site-specific and land in PR 11's verify stage (or operator
    follow-up) — not enforced here.
    """
    return {
        "installCRDs": True,
        "global": {
            "leaderElection": {"namespace": "cert-manager"},
        },
        "prometheus": {"enabled": True},
        "dcim": {"deployment_id": str(getattr(deployment, "id", ""))},
    }


def render_dns_auth_values(deployment: Any) -> dict:
    """CoreDNS authoritative.

    Site authority for the region's zone. Bind v6-only — the
    deployment's pod network is v6, north-south v4 clients reach
    via the NAT46 LB pool advertised by Cilium BGP. Replicas
    default to 2 for HA; the operator can override via
    `config.dns_auth_replicas`.
    """
    cfg = (getattr(deployment, "config", None) or {})
    replicas = int(cfg.get("dns_auth_replicas", 2))
    return {
        "replicaCount": replicas,
        "image": {"repository": "coredns/coredns", "tag": "1.12.0"},
        "service": {
            "type": "LoadBalancer",
            "ipFamilies": ["IPv6"],
            "ipFamilyPolicy": "SingleStack",
        },
        "servers": [
            {
                "zones": [{"zone": "."}],
                "port": 53,
                "plugins": [
                    {"name": "errors"},
                    {"name": "health"},
                    {"name": "ready"},
                    {"name": "prometheus", "parameters": "0.0.0.0:9153"},
                    # Authoritative zones come from the usg-dcim
                    # bundle endpoint — same shape Smee uses for
                    # the v4 collector path. Real zone list comes
                    # in via per-fabric `Site` records.
                    {"name": "file", "parameters": "/etc/coredns/zones/db.region"},
                ],
            },
        ],
        "dcim": {"deployment_id": str(getattr(deployment, "id", ""))},
    }


def render_dns_recursive_values(deployment: Any) -> dict:
    """Hickory recursive.

    Uses the existing usg-dcim Hickory chart shape (matches
    `infra/coredns-nsec3sign/` Hickory image build). Optional DNS64
    zone enabled when `config.nat64_enabled` is true.
    """
    cfg = (getattr(deployment, "config", None) or {})
    nat64_enabled = bool(cfg.get("nat64_enabled", False))
    upstreams = list(cfg.get("upstream_dns_v6", []) or [])
    out: dict = {
        "replicaCount": int(cfg.get("dns_recursive_replicas", 2)),
        "image": {"repository": "ghcr.io/usg-dcim/hickory", "tag": "0.26"},
        "service": {
            "type": "LoadBalancer",
            "ipFamilies": ["IPv6"],
            "ipFamilyPolicy": "SingleStack",
        },
        "upstreams": upstreams,
        "dcim": {"deployment_id": str(getattr(deployment, "id", ""))},
    }
    if nat64_enabled:
        # DNS64 synthesises AAAA from A using the well-known prefix.
        # Operators flip this on per site when NAT64 is deployed
        # (the gateway pod itself comes via a separate chart, not
        # rendered here).
        out["dns64"] = {
            "enabled": True,
            "prefix": "64:ff9b::/96",
        }
    return out


def render_dhcp_values(deployment: Any) -> dict:
    """Kea DHCPv6 + control-agent REST.

    Site clients with v6-only stacks use SLAAC for addresses; this
    Kea instance serves stateless DHCPv6 + DDNS to the auth CoreDNS
    when sites enable both. Scopes come from the deployment's
    IPAM (`config.dhcp_scopes`) when present.
    """
    cfg = (getattr(deployment, "config", None) or {})
    scopes = list(cfg.get("dhcp_scopes", []) or [])
    return {
        "replicaCount": int(cfg.get("dhcp_replicas", 2)),
        "image": {"repository": "cloudnativelabs/kea", "tag": "2.6.1"},
        "hostNetwork": True,  # Kea binds the v4 client VLAN directly
        "controlAgent": {
            "enabled": True,
            "port": 8000,
        },
        "dhcp6": {
            "enabled": True,
            "subnets": scopes,
            "stateless": True,
        },
        "dhcp4": {
            "enabled": False,  # site fabric is v6-only at the production VLAN
        },
        "dcim": {"deployment_id": str(getattr(deployment, "id", ""))},
    }


def render_collector_values(deployment: Any) -> dict:
    """usg-dcim go-collector for the site.

    Reuses the existing collector image; the orchestrator's `seed`
    stage (PR 11) will enroll the collector against the central
    backend using the existing enrollment-token flow (cf.
    infra/k8s/scripts/enroll-site.ps1). The Helm values here only
    cover the deploy + the per-site identifier.
    """
    return {
        "replicaCount": 1,
        "image": {"repository": "ghcr.io/usg-dcim/go-collector", "tag": "dev"},
        "site": {
            "id": str(getattr(deployment, "site_id", "")),
            "deployment_id": str(getattr(deployment, "id", "")),
        },
        "central": {
            # Filled in by the `seed` stage post-install — the
            # collector pod needs the central API URL + an
            # enrollment token to bootstrap mTLS. Placeholder here
            # so the rendered values show the slot to operators.
            "api_url": "",
            "enrollment_token": "",
        },
    }


def dump_values(values: dict) -> str:
    """Single-doc YAML — same helper shape as cilium.dump_values."""
    return yaml.safe_dump(values, sort_keys=False)
