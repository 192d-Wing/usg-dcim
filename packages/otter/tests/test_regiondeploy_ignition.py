"""Unit tests for the Flatcar Ignition renderer.

Like the CRD tests, we assert on dict structure where possible —
the JSON-encoding step is well-tested upstream. Asserting on dicts
keeps the test failures readable when the renderer evolves."""

import json
from types import SimpleNamespace
from uuid import UUID

import pytest

from dcim.regiondeploy.ignition import (
    IGNITION_SPEC,
    build_ignition,
    render_ignition_for_node,
)

DEP_ID = UUID("01234567-89ab-cdef-0123-456789abcdef")


def _deployment(config=None, **overrides):
    base = {
        "id": DEP_ID,
        "name": "site42-prod",
        "config": config if config is not None else {
            "pod_cidr_v6": "fd00:site:42:1000::/56",
            "svc_cidr_v6": "fd00:site:42:2000::/108",
            "vip_v6": "fd00:site:42:0::1",
            "cilium_version": "1.19.3",
        },
    }
    base.update(overrides)
    return SimpleNamespace(**base)


def _node(role="control_plane", **overrides):
    base = {
        "hostname": "control-1",
        "role": role,
        "primary_ip_v6": "fd00:site:42:0::10",
    }
    base.update(overrides)
    return SimpleNamespace(**base)


def test_first_control_plane_uses_init_unit():
    cfg = build_ignition(_deployment(), _node())
    units = cfg["systemd"]["units"]
    assert [u["name"] for u in units] == ["kubeadm-init.service"]
    init = units[0]["contents"]
    # CIDRs and VIP come from the env file rather than baked-in here
    # — keeps the unit static. Assert the env reference exists.
    assert "EnvironmentFile=/etc/dcim/cluster.env" in init
    assert "kubeadm init" in init
    assert "--skip-phases=addon/kube-proxy" in init


def test_worker_uses_join_unit_with_token():
    cfg = build_ignition(
        _deployment(),
        _node(hostname="worker-1", role="worker"),
        kubeadm_join_token="abcdef.1234567890123456",
        control_plane_ep="[fd00:site:42:0::1]:6443",
    )
    units = cfg["systemd"]["units"]
    assert [u["name"] for u in units] == ["kubeadm-join.service"]
    join = units[0]["contents"]
    assert "kubeadm join [fd00:site:42:0::1]:6443" in join
    assert "--token abcdef.1234567890123456" in join
    # Worker doesn't get --control-plane — that flag is reserved for
    # additional CP members.
    assert "--control-plane" not in join


def test_additional_control_plane_gets_control_plane_flag():
    cfg = build_ignition(
        _deployment(),
        _node(hostname="control-2", role="control_plane"),
        kubeadm_join_token="abcdef.1234567890123456",
        control_plane_ep="[fd00:site:42:0::1]:6443",
    )
    join = cfg["systemd"]["units"][0]["contents"]
    assert "--control-plane" in join


def test_non_first_node_without_token_raises():
    # Without a token the worker has no way to join. Render-time
    # failure is preferable to producing an Ignition that bricks at
    # first boot.
    with pytest.raises(ValueError, match="kubeadm_join_token"):
        build_ignition(_deployment(), _node(role="worker"))


def test_hostname_and_cluster_env_written_to_disk():
    cfg = build_ignition(_deployment(), _node(hostname="control-1"))
    files = {f["path"]: f for f in cfg["storage"]["files"]}
    assert files["/etc/hostname"]["contents"]["inline"] == "control-1\n"
    env = files["/etc/dcim/cluster.env"]["contents"]["inline"]
    assert "POD_CIDR_V6=fd00:site:42:1000::/56" in env
    assert "SVC_CIDR_V6=fd00:site:42:2000::/108" in env
    assert "CONTROL_PLANE_EP=fd00:site:42:0::1" in env
    assert "CILIUM_VERSION=1.19.3" in env


def test_cluster_env_uses_explicit_control_plane_ep_when_provided():
    # When the orchestrator passes control_plane_ep explicitly (e.g.
    # for a join), it wins over the deployment's config.vip_v6.
    cfg = build_ignition(
        _deployment(),
        _node(role="worker"),
        kubeadm_join_token="t.xxx",
        control_plane_ep="[fd00:site:42:0::2]:6443",
    )
    env = next(
        f for f in cfg["storage"]["files"]
        if f["path"] == "/etc/dcim/cluster.env"
    )["contents"]["inline"]
    assert "CONTROL_PLANE_EP=[fd00:site:42:0::2]:6443" in env


def test_ssh_keys_only_included_when_provided():
    cfg_without = build_ignition(_deployment(), _node())
    assert "passwd" not in cfg_without

    cfg_with = build_ignition(
        _deployment(), _node(), ssh_keys=["ssh-ed25519 AAAA op1", "ssh-ed25519 AAAB op2"],
    )
    user = cfg_with["passwd"]["users"][0]
    assert user["name"] == "core"
    assert user["sshAuthorizedKeys"] == ["ssh-ed25519 AAAA op1", "ssh-ed25519 AAAB op2"]


def test_render_returns_compact_json_with_pinned_spec():
    out = render_ignition_for_node(_deployment(), _node())
    parsed = json.loads(out)
    assert parsed["ignition"]["version"] == IGNITION_SPEC
    # Compact (no whitespace after , or :) — keeps the Workflow
    # hardwareMap payload small.
    assert ", " not in out and ": " not in out


def test_render_handles_string_role_or_enum():
    # node.role may be a SQLAlchemy enum instance (with .value) or a
    # raw string in tests. The renderer should accept both.
    class _Role:
        value = "control_plane"

    cfg = build_ignition(_deployment(), _node(role=_Role()))
    assert cfg["systemd"]["units"][0]["name"] == "kubeadm-init.service"


def test_missing_config_keys_render_empty_values():
    # A deployment with no config dict should still produce a
    # renderable Ignition — the install will fail later with a
    # clear error, which is better than failing at render time
    # before any nodes have booted.
    cfg = build_ignition(_deployment(config={}), _node())
    env = next(
        f for f in cfg["storage"]["files"]
        if f["path"] == "/etc/dcim/cluster.env"
    )["contents"]["inline"]
    assert "POD_CIDR_V6=\n" in env
    assert "CONTROL_PLANE_EP=\n" in env
