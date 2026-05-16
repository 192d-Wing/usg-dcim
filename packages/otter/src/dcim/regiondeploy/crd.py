"""Tinkerbell + Rufio CRD generators.

Pure functions that take a `RegionDeployment` (and its child nodes /
services) and emit the dict structures for the Kubernetes resources
that drive bare-metal provisioning at the target site. The orchestrator
(PR 7+) applies these via the regional cluster's dynamic client.

We target the **v1alpha1** API group (`tinkerbell.org/v1alpha1` and
`bmc.tinkerbell.org/v1alpha1`) because:

  * the upstream chart's stable contracts are still on v1alpha1;
  * Smee's BackendReader reads v1alpha1 Hardware (verified during the
    Phase 0a smoke install — see docs/dev/region-deploy.md §3.0).

v1alpha2 exists as a parallel set but isn't where the active backend
code is wired yet. Migrating to v1alpha2 is a separate workstream
when upstream signals readiness.

Why not Jinja templates?

  The region-deploy doc mentions Jinja for the Ignition rendering
  (PR 5) — that's a *text* job where the output is YAML/JSON the
  template author owns end-to-end. CRDs are structured objects with
  evolving schemas, where Pythons dict/yaml.safe_dump_all is type-
  safer, easier to compose, and easier to test (assert on dict
  structure rather than diff full text). Keep Jinja for Ignition.
"""

from __future__ import annotations

from collections.abc import Iterable
from typing import Any

import yaml

# v1alpha1 API constants — kept as module-level so callers and tests
# don't sprinkle string literals.
TINKERBELL_API_VERSION = "tinkerbell.org/v1alpha1"
BMC_API_VERSION = "bmc.tinkerbell.org/v1alpha1"

KIND_HARDWARE = "Hardware"
KIND_TEMPLATE = "Template"
KIND_WORKFLOW = "Workflow"
KIND_BMC_MACHINE = "Machine"


def hardware_for_node(deployment: Any, node: Any) -> dict:
    """Render a Hardware CR for a single bare-metal node.

    The Hardware CR is Smee's source-of-truth for "should I respond
    to this MAC". When MAC matches, Smee returns boot options based on
    the netboot block (allowPXE + the deployment-wide iPXE URL Smee
    serves on its HTTP endpoint).
    """
    name = _resource_name(deployment, node)
    primary_ip = node.primary_ip_v6
    # v1alpha1's `dhcp.ip` block is typed v4 (Address/Netmask/Gateway/
    # Family). For v6-only sites we still need *something* there or
    # ConvertByMac rejects it. We populate a sentinel v4 entry so the
    # CR validates; v6 reachability is handled by SLAAC + DHCPv6 boot
    # URL (Option 59), not the v1alpha1 dhcp.ip fields. v1alpha2's
    # interface model fixes this; the v1alpha2 cutover is tracked
    # separately.
    spec: dict[str, Any] = {
        "metadata": {
            "facility": {"facility_code": _site_label(deployment)},
            "instance": {
                "hostname": node.hostname,
                "id": str(node.id),
            },
        },
        "interfaces": [
            {
                "dhcp": {
                    "mac": str(node.mac),
                    "hostname": node.hostname,
                    "ip": {
                        "address": "0.0.0.0",
                        "netmask": "255.255.255.255",
                        "gateway": "0.0.0.0",
                        "family": 4,
                    },
                },
                "netboot": {"allowPXE": True},
            }
        ],
    }
    # Attach the v6 management address as metadata so operators
    # browsing `kubectl get hardware -o yaml` can correlate without
    # relying on the v1alpha1 dhcp.ip sentinel above.
    if primary_ip is not None:
        spec["metadata"]["instance"]["ipv6_address"] = str(primary_ip)
    return {
        "apiVersion": TINKERBELL_API_VERSION,
        "kind": KIND_HARDWARE,
        "metadata": {
            "name": name,
            "labels": _labels(deployment, node),
        },
        "spec": spec,
    }


def template_for_deployment(deployment: Any) -> dict:
    """Render a Template CR for the deployment's install flow.

    One template per deployment (rather than per node) because every
    node in a region runs the same install plan — image-write, write
    Ignition, reboot. Node-specific values land via the Workflow CR.

    The action list here is the **minimum viable plan**. A real
    Flatcar-via-Ignition flow needs:

      * stream-image: write the Flatcar production image to the disk;
      * write-ignition: drop the per-node Ignition file on the OEM
        partition so Flatcar consumes it on first boot;
      * reboot: hand control back to the firmware.

    Each action is a container the Tink Worker pulls and runs from
    within the in-memory Hook OS environment.
    """
    name = _template_name(deployment)
    # Tinkerbell's Template uses YAML-in-YAML — the spec.data field is
    # a string holding a Template document. We construct the inner
    # document as a Python dict and round-trip via safe_dump so the
    # outer CR carries a clean canonical form.
    inner = {
        "version": "0.1",
        "name": name,
        "global_timeout": 1800,
        "tasks": [
            {
                "name": "os-install",
                "worker": "{{.device_1}}",
                "actions": [
                    {
                        "name": "stream-image",
                        "image": "quay.io/tinkerbell-actions/image2disk:v1.0.0",
                        "timeout": 600,
                        "environment": {
                            "DEST_DISK": "/dev/sda",
                            "IMG_URL": "{{.image_url}}",
                            "COMPRESSED": "true",
                        },
                    },
                    {
                        "name": "write-ignition",
                        "image": "quay.io/tinkerbell-actions/writefile:v1.0.0",
                        "timeout": 90,
                        "environment": {
                            "DEST_DISK": "/dev/sda6",
                            "FS_TYPE": "ext4",
                            "DEST_PATH": "/ignition.json",
                            "CONTENTS": "{{.ignition_json}}",
                            "UID": "0",
                            "GID": "0",
                            "MODE": "0644",
                            "DIRMODE": "0755",
                        },
                    },
                    {
                        "name": "reboot",
                        "image": "ghcr.io/jacobweinstock/waitdaemon:0.2.0",
                        "timeout": 90,
                        "pid": "host",
                        "command": ["reboot"],
                        "volumes": ["/worker:/worker"],
                    },
                ],
            }
        ],
    }
    return {
        "apiVersion": TINKERBELL_API_VERSION,
        "kind": KIND_TEMPLATE,
        "metadata": {
            "name": name,
            "labels": _labels(deployment, None),
        },
        "spec": {
            "data": yaml.safe_dump(inner, sort_keys=False),
        },
    }


def workflow_for_node(deployment: Any, node: Any, *, image_url: str, ignition_json: str) -> dict:
    """Render a Workflow CR that binds the deployment Template to a
    specific Hardware (by MAC) with per-node Ignition.

    `image_url` and `ignition_json` are passed in rather than derived
    from the node, since both depend on deployment-wide config the
    orchestrator owns: the Flatcar release URL (mirror-cached
    centrally) and the rendered Ignition (PR 5 renderer).
    """
    name = _resource_name(deployment, node)
    return {
        "apiVersion": TINKERBELL_API_VERSION,
        "kind": KIND_WORKFLOW,
        "metadata": {
            "name": name,
            "labels": _labels(deployment, node),
        },
        "spec": {
            "templateRef": _template_name(deployment),
            "hardwareRef": _resource_name(deployment, node),
            "hardwareMap": {
                # `device_1` is the placeholder the Template references
                # in `worker: "{{.device_1}}"`. Mapping MAC → device_N
                # is how Tink picks which Hardware fulfills which
                # worker slot.
                "device_1": str(node.mac),
                "image_url": image_url,
                "ignition_json": ignition_json,
            },
        },
    }


def bmc_machine_for_node(deployment: Any, node: Any) -> dict:
    """Render a Rufio Machine CR — the BMC connection Rufio uses to
    power-cycle and set boot order on the node.

    Credentials live in a Kubernetes Secret created by the orchestrator
    at deploy-start; this CR references it by name. The convention is
    that the Secret stores `username` + `password` keys, matching what
    Rufio's bmclib client expects.
    """
    name = _resource_name(deployment, node)
    secret_ns, _, secret_name = (node.bmc_creds_secret_ref or "/").partition("/")
    return {
        "apiVersion": BMC_API_VERSION,
        "kind": KIND_BMC_MACHINE,
        "metadata": {
            "name": name,
            "labels": _labels(deployment, node),
        },
        "spec": {
            "connection": {
                "host": str(node.bmc_address),
                "port": 443,
                "insecureTLS": True,
                "authSecretRef": {
                    "name": secret_name or "",
                    "namespace": secret_ns or "",
                },
                # Let Rufio pick the right backend (Redfish first,
                # IPMI as fallback) rather than pinning here — keeps
                # us forward-compatible with future BMC types.
                "providerOptions": {},
            },
        },
    }


def crds_for_deployment(
    deployment: Any,
    *,
    image_url: str,
    ignition_for: dict | None = None,
) -> list[dict]:
    """Render the full set of CRDs for a deployment.

    Returns one Template + one Hardware + one Workflow + one BMC
    Machine per node, in the order the orchestrator should apply them
    (Template first so Workflow references resolve).

    `ignition_for` maps node id → rendered Ignition JSON string. When
    a node id is missing the empty string is used, which keeps the
    structure renderable for tests that exercise the CRD plumbing in
    isolation from PR 5's renderer.
    """
    ignition_for = ignition_for or {}
    out: list[dict] = [template_for_deployment(deployment)]
    for node in deployment.nodes:
        out.append(hardware_for_node(deployment, node))
        out.append(bmc_machine_for_node(deployment, node))
        out.append(
            workflow_for_node(
                deployment, node,
                image_url=image_url,
                ignition_json=ignition_for.get(str(node.id), ""),
            )
        )
    return out


def dump_yaml(crds: Iterable[dict]) -> str:
    """Serialise a CRD list as a multi-document YAML stream — the
    format `kubectl apply -f -` expects.
    """
    return yaml.safe_dump_all(crds, sort_keys=False)


# ─── internal helpers ──────────────────────────────────────────────────


def _resource_name(deployment: Any, node: Any | None) -> str:
    """Stable per-(deployment, node) name. Tinkerbell CR names must be
    DNS-1123 labels — lowercase, ≤63 chars. Node hostnames usually
    qualify; deployment ids are UUIDs we shorten to 8 chars to keep
    the combined name under the limit even with long hostnames.
    """
    dep_part = str(deployment.id)[:8]
    if node is None:
        return f"rd-{dep_part}"
    host = str(node.hostname).lower().replace(".", "-")
    name = f"rd-{dep_part}-{host}"
    return name[:63].rstrip("-")


def _template_name(deployment: Any) -> str:
    return f"rd-{str(deployment.id)[:8]}-install"


def _site_label(deployment: Any) -> str:
    # Tinkerbell's Hardware.metadata.facility is just a label string
    # — using the site id keeps it stable + searchable.
    return f"site-{str(deployment.site_id)[:8]}"


def _labels(deployment: Any, node: Any | None) -> dict[str, str]:
    """Common labels applied to every CR we emit. Lets `kubectl get
    -l dcim.region-deployment=<id>` enumerate everything tied to a
    deployment for cleanup / debugging."""
    out = {
        "dcim.region-deployment": str(deployment.id),
        "dcim.region-deployment-name": deployment.name,
    }
    if node is not None:
        out["dcim.region-deployment-node"] = str(node.id)
    return out
