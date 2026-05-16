"""Unit tests for the Cilium values + BGP CRD renderers.

Same dict-structure assertion style as the CRD / Ignition tests —
locking the shape keeps a refactor from quietly dropping a field
Cilium reads from Helm or a CR field the operator reads from
`kubectl get`.
"""

from types import SimpleNamespace
from uuid import UUID

import yaml

from dcim.regiondeploy.cilium import (
    DEFAULT_CILIUM_VERSION,
    dump_values,
    dump_yaml,
    render_bgp_crds,
    render_cilium_values,
)

DEP_ID = UUID("01234567-89ab-cdef-0123-456789abcdef")


def _deployment(config=None, **overrides):
    base = {
        "id": DEP_ID,
        "name": "site42-prod",
        "config": config if config is not None else {
            "pod_cidr_v6": "fd00:site:42:1000::/56",
            "svc_cidr_v6": "fd00:site:42:2000::/108",
            "lb_pool_v6":  "fd00:site:42:3000::/112",
            "vip_v6":      "fd00:site:42:0::1",
            "bgp_local_asn": 65042,
            "bgp_peers": [
                {"address": "fd00:site:42:0::ffff", "asn": 65000},
            ],
            "cilium_version": DEFAULT_CILIUM_VERSION,
            "lb_mode": "snat",
        },
    }
    base.update(overrides)
    return SimpleNamespace(**base)


def test_cilium_values_locks_v6_only_and_bgp():
    v = render_cilium_values(_deployment())
    assert v["ipv4"]["enabled"] is False
    assert v["ipv6"]["enabled"] is True
    assert v["kubeProxyReplacement"] is True
    assert v["bgpControlPlane"]["enabled"] is True
    # SNAT default — anything other than "dsr" should produce SNAT.
    assert v["loadBalancer"]["mode"] == "snat"
    # Hubble on by default for observability — the doc names this as
    # one of the Cilium-over-Calico justifications.
    assert v["hubble"]["enabled"] is True


def test_cilium_values_dsr_opt_in():
    v = render_cilium_values(_deployment(config={
        "pod_cidr_v6": "fd00::/56",
        "lb_mode": "dsr",
    }))
    assert v["loadBalancer"]["mode"] == "dsr"


def test_cilium_values_carries_pod_cidr_in_pool():
    v = render_cilium_values(_deployment())
    pool = v["ipam"]["operator"]["clusterPoolIPv6PodCIDRList"]
    assert pool == ["fd00:site:42:1000::/56"]
    assert v["ipam"]["operator"]["clusterPoolIPv6MaskSize"] == 64


def test_cilium_values_unbracket_v6_service_host():
    # A v6 control-plane endpoint stored as `[fd00:...]:6443` in the
    # cluster.env file should be normalised to a bare host before
    # going into Cilium's k8sServiceHost.
    v = render_cilium_values(_deployment(config={
        "vip_v6": "[fd00:site:42:0::1]:6443",
    }))
    assert v["k8sServiceHost"] == "fd00:site:42:0::1"


def test_cilium_values_empty_when_no_config():
    # Renderer must not crash on a barely-filled deployment — the
    # render stage runs before the operator has finished setting up
    # an environment, so partial input is the common case.
    v = render_cilium_values(_deployment(config={}))
    assert v["ipv6"]["enabled"] is True
    assert v["ipam"]["operator"]["clusterPoolIPv6PodCIDRList"] == []


def test_bgp_crds_emit_full_set_with_lb_pool():
    crds = render_bgp_crds(_deployment())
    kinds = [c["kind"] for c in crds]
    # PeerConfig first (referenced by ClusterConfig), then Cluster,
    # then Advertisement, then the LB pool when it's configured.
    assert kinds == [
        "CiliumBGPPeerConfig",
        "CiliumBGPClusterConfig",
        "CiliumBGPAdvertisement",
        "CiliumLoadBalancerIPPool",
    ]


def test_bgp_cluster_config_references_peer_config_by_name():
    crds = render_bgp_crds(_deployment())
    peer_cfg = next(c for c in crds if c["kind"] == "CiliumBGPPeerConfig")
    cluster = next(c for c in crds if c["kind"] == "CiliumBGPClusterConfig")
    inst = cluster["spec"]["bgpInstances"][0]
    assert inst["localASN"] == 65042
    assert inst["peers"][0]["peerASN"] == 65000
    assert inst["peers"][0]["peerAddress"] == "fd00:site:42:0::ffff"
    assert inst["peers"][0]["peerConfigRef"]["name"] == peer_cfg["metadata"]["name"]


def test_bgp_advertisement_announces_lb_ips_only():
    # Doc note: gobgp at sites still advertises its own /32 health
    # routes; Cilium only owns service LB IPs to avoid overlap.
    crds = render_bgp_crds(_deployment())
    adv = next(c for c in crds if c["kind"] == "CiliumBGPAdvertisement")
    types = [a["advertisementType"] for a in adv["spec"]["advertisements"]]
    assert types == ["Service"]
    addrs = adv["spec"]["advertisements"][0]["service"]["addresses"]
    assert addrs == ["LoadBalancerIP"]


def test_bgp_lb_pool_skipped_when_lb_pool_v6_unset():
    crds = render_bgp_crds(_deployment(config={
        "bgp_local_asn": 65042,
        "bgp_peers": [{"address": "fd00::ff", "asn": 65000}],
    }))
    kinds = [c["kind"] for c in crds]
    assert "CiliumLoadBalancerIPPool" not in kinds
    # The other three still emit — partial config is renderable.
    assert "CiliumBGPClusterConfig" in kinds


def test_dump_yaml_is_multi_document_stream():
    crds = render_bgp_crds(_deployment())
    out = dump_yaml(crds)
    # 4 CRDs → 3 separators.
    assert out.count("\n---\n") == 3
    assert len(list(yaml.safe_load_all(out))) == 4


def test_dump_values_is_single_doc():
    v = render_cilium_values(_deployment())
    out = dump_values(v)
    # No multi-doc separator; round-trips through safe_load.
    assert "---" not in out
    assert yaml.safe_load(out)["ipv6"]["enabled"] is True


def test_common_labels_match_other_renderers():
    # The PR 4 CRD generator and PR 9 BGP renderer should share the
    # same `dcim.region-deployment` label so a `kubectl get -l ...`
    # query enumerates everything tied to a deployment regardless
    # of which renderer produced it.
    crds = render_bgp_crds(_deployment())
    for c in crds:
        labels = c["metadata"]["labels"]
        assert labels["dcim.region-deployment"] == str(DEP_ID)
        assert labels["dcim.region-deployment-name"] == "site42-prod"
