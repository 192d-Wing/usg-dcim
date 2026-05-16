"""Flatcar Container Linux Ignition renderer.

Produces per-node Ignition JSON (spec 3.4) that Flatcar consumes on
first boot. The Workflow CR's `hardwareMap.ignition_json` field (see
crd.py) takes the output of this renderer as a string and writes it
to the node's OEM partition during the install workflow.

Why direct JSON, not Butane → Ignition?

  Butane is the human-friendly YAML the Flatcar docs use; it
  transpiles to Ignition JSON via the `butane` binary. We could shell
  out to it, but:
   - it adds a binary dep to the central image,
   - the transpile is one-to-one for the fields we use,
   - emitting JSON directly keeps the renderer testable with plain
     Python asserts and lets us evolve schemas without re-vendoring
     the transpiler.

  If we later need rich Butane-only sugar (e.g. `trees`), we'll
  revisit. For now: dicts in → JSON out.

Per-node fields (the renderer's inputs):
  - hostname             from node.hostname
  - role                 control_plane | worker | edge
  - primary_ip_v6        the v6 mgmt address (informational; SLAAC
                         brings it up at runtime)
  - kubeadm_join_token   the bootstrap token issued by the
                         orchestrator after kubeadm init
  - control_plane_ep     "host:port" of the cluster's control-plane
                         endpoint (the VIP advertised by Cilium BGP)
  - ssh_keys             list of authorized SSH pubkeys (operator
                         access; optional)
"""

from __future__ import annotations

import json
from typing import Any

# Spec version pin. Flatcar ≥ 3815.x supports 3.4. Bump in lockstep
# with the pinned Flatcar release.
IGNITION_SPEC = "3.4.0"


def render_ignition_for_node(
    deployment: Any,
    node: Any,
    *,
    kubeadm_join_token: str | None = None,
    control_plane_ep: str | None = None,
    ssh_keys: list[str] | None = None,
) -> str:
    """Render an Ignition JSON string for a single node.

    Control-plane nodes get a `kubeadm-init.service` systemd unit
    (token + endpoint are produced *by* this node, not consumed).
    Worker / edge nodes get a `kubeadm-join.service` that consumes
    the bootstrap token from the first control-plane.

    The first control-plane node passes `kubeadm_join_token=None`
    and `control_plane_ep=None`; the orchestrator collects the
    token after `kubeadm init` succeeds and feeds it to subsequent
    nodes' Ignition.

    Returns the JSON as a string (Workflow CR consumes a string).
    """
    cfg = build_ignition(
        deployment, node,
        kubeadm_join_token=kubeadm_join_token,
        control_plane_ep=control_plane_ep,
        ssh_keys=ssh_keys,
    )
    # separators set for deterministic, compact output — Ignition is
    # never read by humans and the Workflow CR carries the bytes
    # verbatim through Tink.
    return json.dumps(cfg, separators=(",", ":"), sort_keys=True)


def build_ignition(
    deployment: Any,
    node: Any,
    *,
    kubeadm_join_token: str | None = None,
    control_plane_ep: str | None = None,
    ssh_keys: list[str] | None = None,
) -> dict:
    """Build the Ignition config as a dict. Split from
    render_ignition_for_node so tests can assert on structure
    without JSON-roundtripping."""

    role = _node_role(node)
    storage_files: list[dict] = [
        _file(
            "/etc/hostname",
            node.hostname + "\n",
            mode=0o644,
        ),
        # Make the cluster's pod/service CIDR / VIP visible to the
        # post-install systemd unit. Reading from a file rather than
        # baking into the unit's command line keeps the unit text
        # static and lets the orchestrator iterate the config without
        # changing the unit.
        _file(
            "/etc/dcim/cluster.env",
            _cluster_env(deployment, control_plane_ep),
            mode=0o600,
        ),
    ]

    systemd_units: list[dict] = []
    if role == "control_plane" and kubeadm_join_token is None:
        # First control-plane node — runs kubeadm init.
        systemd_units.append(_kubeadm_init_unit(deployment))
    else:
        # Worker / edge / additional control-plane — runs kubeadm join.
        if kubeadm_join_token is None or control_plane_ep is None:
            raise ValueError(
                "non-first-cp node requires kubeadm_join_token + "
                "control_plane_ep",
            )
        systemd_units.append(
            _kubeadm_join_unit(
                role=role,
                token=kubeadm_join_token,
                control_plane_ep=control_plane_ep,
            ),
        )

    cfg: dict = {
        "ignition": {"version": IGNITION_SPEC},
        "storage": {"files": storage_files},
        "systemd": {"units": systemd_units},
    }

    if ssh_keys:
        cfg["passwd"] = {
            "users": [
                {
                    "name": "core",
                    "sshAuthorizedKeys": list(ssh_keys),
                }
            ]
        }

    return cfg


# ─── internal helpers ──────────────────────────────────────────────────


def _node_role(node: Any) -> str:
    """Coerce node.role (an enum or string) to its string value."""
    role = node.role
    return getattr(role, "value", role)


def _cluster_env(deployment: Any, control_plane_ep: str | None) -> str:
    """Render /etc/dcim/cluster.env — key=value lines, shell-safe.

    Read by the kubeadm-{init,join}.service units to source pod CIDR,
    service CIDR, and the control-plane endpoint. Falls back to
    sensible defaults when the deployment config is missing keys,
    so a partial config still produces a renderable Ignition (the
    install will fail later with a clear error rather than at render
    time)."""
    config = getattr(deployment, "config", None) or {}
    lines = [
        f"POD_CIDR_V6={config.get('pod_cidr_v6', '')}",
        f"SVC_CIDR_V6={config.get('svc_cidr_v6', '')}",
        f"CONTROL_PLANE_EP={control_plane_ep or config.get('vip_v6', '')}",
        f"CILIUM_VERSION={config.get('cilium_version', '1.19.3')}",
    ]
    return "\n".join(lines) + "\n"


def _file(path: str, contents: str, *, mode: int) -> dict:
    """Ignition storage.files entry with inline string contents.

    Ignition's `source` field is a data: URI — for arbitrary text we
    use `data:,<urlencoded>` style. The simpler `contents.inline`
    shape Ignition supports since spec 3.0 is what we use here.
    """
    return {
        "path": path,
        "mode": mode,
        "overwrite": True,
        "contents": {"inline": contents},
    }


def _kubeadm_init_unit(deployment: Any) -> dict:
    """First-control-plane install unit.

    Runs `kubeadm init` with the pod/service CIDRs from the cluster
    env file, then writes the kubeconfig + join token to a well-known
    path the orchestrator polls.
    """
    name = "kubeadm-init.service"
    contents = (
        "[Unit]\n"
        "Description=DCIM-managed kubeadm init (first control-plane)\n"
        "After=network-online.target containerd.service\n"
        "Wants=network-online.target containerd.service\n"
        "ConditionPathExists=!/etc/kubernetes/admin.conf\n"
        "\n"
        "[Service]\n"
        "Type=oneshot\n"
        "RemainAfterExit=yes\n"
        "EnvironmentFile=/etc/dcim/cluster.env\n"
        "ExecStart=/usr/local/bin/kubeadm init"
        " --pod-network-cidr=${POD_CIDR_V6}"
        " --service-cidr=${SVC_CIDR_V6}"
        " --control-plane-endpoint=${CONTROL_PLANE_EP}"
        " --skip-phases=addon/kube-proxy\n"
        "\n"
        "[Install]\n"
        "WantedBy=multi-user.target\n"
    )
    return {
        "name": name,
        "enabled": True,
        "contents": contents,
    }


def _kubeadm_join_unit(*, role: str, token: str, control_plane_ep: str) -> dict:
    """Worker / additional-control-plane join unit.

    Role determines whether we pass `--control-plane` to kubeadm
    join. The token + endpoint come from the orchestrator after the
    first control-plane finishes init.
    """
    name = "kubeadm-join.service"
    cp_flag = " --control-plane" if role == "control_plane" else ""
    contents = (
        "[Unit]\n"
        f"Description=DCIM-managed kubeadm join ({role})\n"
        "After=network-online.target containerd.service\n"
        "Wants=network-online.target containerd.service\n"
        "ConditionPathExists=!/etc/kubernetes/kubelet.conf\n"
        "\n"
        "[Service]\n"
        "Type=oneshot\n"
        "RemainAfterExit=yes\n"
        f"ExecStart=/usr/local/bin/kubeadm join {control_plane_ep}"
        f" --token {token}"
        " --discovery-token-unsafe-skip-ca-verification"
        f"{cp_flag}\n"
        "\n"
        "[Install]\n"
        "WantedBy=multi-user.target\n"
    )
    return {
        "name": name,
        "enabled": True,
        "contents": contents,
    }
