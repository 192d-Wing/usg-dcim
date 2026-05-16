"""Cilium values + BGP CRD renderers for the cni / cni.bgp stages.

Same pattern as `crd.py` and `ignition.py` — pure functions that take
a RegionDeployment row and emit dicts. The orchestrator's cni stages
call these and emit the rendered output into event payloads while
the apply path is still pending the regional-cluster kubeconfig
work (see `joining` stage TODO).

References:
  * Cilium 1.19.3 Helm values:
      https://github.com/cilium/cilium/tree/v1.19.3/install/kubernetes/cilium
  * CiliumBGP* CRDs:
      https://docs.cilium.io/en/v1.19/network/bgp-control-plane/
  * Region-deploy design decisions for IPv6-only + native BGP:
      docs/dev/region-deploy.md §2 (CNI / LB rows).

Why two functions:
  * `render_cilium_values` produces Helm values that go into
    `helm install cilium cilium/cilium --version 1.19.3 -f -`.
  * `render_bgp_crds` produces standalone Kubernetes manifests
    applied AFTER Cilium is installed (the CRDs are owned by the
    Cilium operator).

  Splitting them lets the orchestrator emit two distinct events
  (`cni` and `cni.bgp`) with clean payloads, instead of one
  monolithic blob the UI has to slice.
"""

from __future__ import annotations

from collections.abc import Iterable
from typing import Any

import yaml

# Default Cilium version. Pinned in lockstep with the docs/decision
# log; bump in usg-dcim/docs/dev/region-deploy.md and infra/helm/
# tinkerbell/values.yaml together.
DEFAULT_CILIUM_VERSION = "1.19.3"


def render_cilium_values(deployment: Any) -> dict:
    """Render the Helm values overrides for `cilium/cilium`.

    Locked-in shape:
      * IPv6-only on pods + services (Meta-style; doc §2 Address family).
      * Native BGP (no MetalLB; doc §2 CNI / LB).
      * SNAT default, DSR opt-in via config.lb_mode.
      * kubeProxyReplacement: true — kubeadm init runs with
        --skip-phases=addon/kube-proxy (Ignition emits that flag in
        the kubeadm-init.service unit).
      * Hubble enabled for the observability win the doc cites as a
        Cilium-over-Calico reason.

    Per-deployment knobs read from `row.config`:
      pod_cidr_v6, svc_cidr_v6, vip_v6, cilium_version, lb_mode.

    Returns a dict ready to be `yaml.safe_dump`ed and fed to Helm
    via `-f -`.
    """
    config = (getattr(deployment, "config", None) or {})
    lb_mode = config.get("lb_mode", "snat")
    pod_cidr = config.get("pod_cidr_v6", "")
    svc_cidr = config.get("svc_cidr_v6", "")

    values: dict = {
        "kubeProxyReplacement": True,
        "ipv4": {"enabled": False},
        "ipv6": {"enabled": True},
        "routingMode": "native",
        "autoDirectNodeRoutes": True,
        # IPAM: cluster-pool gives each node a /64 carved from the
        # deployment pod CIDR. Per the doc, sites get a /56 → 256
        # node /64s, plenty for any single region.
        "ipam": {
            "mode": "cluster-pool",
            "operator": {
                "clusterPoolIPv6PodCIDRList": [pod_cidr] if pod_cidr else [],
                "clusterPoolIPv6MaskSize": 64,
            },
        },
        # SVC CIDR comes from kubeadm; surfaced here so an operator
        # browsing the rendered values sees the full deploy picture.
        "k8sServiceHost": _bracketed_host(config.get("vip_v6", "")),
        "k8sServicePort": 6443,
        # BGP control plane — Cilium owns the BGP session, not a
        # separate MetalLB. Actual peer config lives in the BGP CRDs
        # rendered by `render_bgp_crds`.
        "bgpControlPlane": {"enabled": True},
        # LB mode. SNAT is the safe default (no symmetric-routing
        # assumption); DSR is opt-in via deployment.config.lb_mode.
        "loadBalancer": {
            "mode": "dsr" if lb_mode == "dsr" else "snat",
        },
        # Hubble — flow visibility, observability.
        "hubble": {
            "enabled": True,
            "relay": {"enabled": True},
            "ui": {"enabled": True},
        },
        # Metadata to help operators correlate this install to a
        # specific deployment row.
        "cluster": {
            "name": getattr(deployment, "name", "") or "region",
        },
    }
    # Echo the svc CIDR back as metadata so the Helm values
    # snapshot in the event log carries it; Cilium itself reads
    # the service CIDR from the API server.
    if svc_cidr:
        values.setdefault("dcim", {})["svc_cidr_v6"] = svc_cidr
    return values


def render_bgp_crds(deployment: Any) -> list[dict]:
    """Render Cilium's BGP CRDs from the deployment's BGP config.

    Emits four CRs (when fully configured):
      * CiliumBGPClusterConfig    — names the BGP "cluster" and
                                    references PeerConfigs.
      * CiliumBGPPeerConfig       — per-peer timers + auth.
      * CiliumBGPAdvertisement    — what to advertise (LB IPs).
      * CiliumLoadBalancerIPPool  — the LB IP pool Cilium hands out
                                    to Services advertised via BGP.

    Per-deployment knobs read from `row.config`:
      bgp_local_asn, bgp_peers (list of {address, asn, md5?}),
      lb_pool_v6.

    Returns the CR list in the order the orchestrator should apply
    them (PeerConfig before ClusterConfig that references it).
    """
    config = (getattr(deployment, "config", None) or {})
    local_asn = int(config.get("bgp_local_asn", 0))
    peers: list[dict] = list(config.get("bgp_peers", []) or [])
    lb_pool = config.get("lb_pool_v6", "")
    base_name = _resource_name(deployment)
    labels = _labels(deployment)

    out: list[dict] = []

    # One PeerConfig — shared across all peers in this deployment.
    # Splitting per-peer is overkill until operators actually need
    # different timers per peer.
    out.append({
        "apiVersion": "cilium.io/v2alpha1",
        "kind": "CiliumBGPPeerConfig",
        "metadata": {
            "name": f"{base_name}-peer-config",
            "labels": labels,
        },
        "spec": {
            "timers": {
                "holdTimeSeconds": 9,
                "keepAliveTimeSeconds": 3,
            },
            "gracefulRestart": {"enabled": True, "restartTimeSeconds": 120},
            "families": [
                {"afi": "ipv6", "safi": "unicast"},
            ],
        },
    })

    # ClusterConfig references the PeerConfig per peer.
    out.append({
        "apiVersion": "cilium.io/v2alpha1",
        "kind": "CiliumBGPClusterConfig",
        "metadata": {
            "name": f"{base_name}-cluster",
            "labels": labels,
        },
        "spec": {
            "nodeSelector": {"matchLabels": {}},  # all nodes
            "bgpInstances": [
                {
                    "name": f"{base_name}-bgp",
                    "localASN": local_asn,
                    "peers": [
                        {
                            "name": f"peer-{i}",
                            "peerASN": int(p.get("asn", 0)),
                            "peerAddress": p.get("address", ""),
                            "peerConfigRef": {
                                "name": f"{base_name}-peer-config",
                            },
                        }
                        for i, p in enumerate(peers)
                    ],
                }
            ],
        },
    })

    # Advertisement: announce service LB IPs only. Per the doc's
    # gobgp-coexistence note, we deliberately don't advertise pod
    # CIDRs — gobgp at sites handles its own /32 health routes.
    out.append({
        "apiVersion": "cilium.io/v2alpha1",
        "kind": "CiliumBGPAdvertisement",
        "metadata": {
            "name": f"{base_name}-advert",
            "labels": {**labels, "advertise": "services"},
        },
        "spec": {
            "advertisements": [
                {"advertisementType": "Service", "service": {"addresses": ["LoadBalancerIP"]}},
            ],
        },
    })

    # LB IP pool — the actual range Cilium hands out for type=LB
    # services. Only emit when the deployment provided one; an
    # operator can re-render after setting it.
    if lb_pool:
        out.append({
            "apiVersion": "cilium.io/v2alpha1",
            "kind": "CiliumLoadBalancerIPPool",
            "metadata": {
                "name": f"{base_name}-lb-pool",
                "labels": labels,
            },
            "spec": {
                "blocks": [{"cidr": lb_pool}],
            },
        })

    return out


def dump_yaml(crds: Iterable[dict]) -> str:
    """Multi-doc YAML stream, same shape as crd.py's helper — kept
    here for symmetry so the orchestrator's cni stage can mirror
    its render stage."""
    return yaml.safe_dump_all(crds, sort_keys=False)


def dump_values(values: dict) -> str:
    """Single-doc YAML for the Helm `-f -` payload."""
    return yaml.safe_dump(values, sort_keys=False)


# ─── internal helpers ──────────────────────────────────────────────────


def _resource_name(deployment: Any) -> str:
    """Stable per-deployment name prefix. Mirrors crd._resource_name's
    DNS-1123 shape: lowercase, no dots, ≤63 chars."""
    dep_part = str(getattr(deployment, "id", ""))[:8]
    return f"rd-{dep_part}"


def _labels(deployment: Any) -> dict[str, str]:
    """Common labels for the BGP CRs — same scheme as crd._labels so
    `kubectl get -l dcim.region-deployment=<id>` finds the BGP
    resources alongside the Tinkerbell ones."""
    return {
        "dcim.region-deployment": str(getattr(deployment, "id", "")),
        "dcim.region-deployment-name": getattr(deployment, "name", "") or "",
    }


def _bracketed_host(addr: str) -> str:
    """Return an address with v6-bracketing if it's a v6 literal.

    Cilium's k8sServiceHost is a bare host (no port). We don't need
    brackets in that field — but if an operator passed in a
    [v6]:port style by accident we strip back to host. Same for v4.
    """
    s = addr.strip()
    if not s:
        return ""
    if s.startswith("["):
        end = s.find("]")
        if end > 0:
            return s[1:end]
    # Strip trailing :port if there's only one colon (v4 + port).
    if s.count(":") == 1:
        return s.split(":", 1)[0]
    return s
