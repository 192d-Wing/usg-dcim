"""Unit tests for the Tinkerbell/Rufio CRD generators.

We assert on dict structure rather than diff full YAML text — the
generators emit Python dicts and the YAML round-trip is just
yaml.safe_dump_all, which is well-tested upstream. Locking the
structure here keeps a future refactor from quietly dropping or
renaming a field that Smee/Tink/Rufio depend on.
"""

from types import SimpleNamespace
from uuid import UUID

import yaml

from dcim.regiondeploy.crd import (
    BMC_API_VERSION,
    KIND_BMC_MACHINE,
    KIND_HARDWARE,
    KIND_TEMPLATE,
    KIND_WORKFLOW,
    TINKERBELL_API_VERSION,
    bmc_machine_for_node,
    crds_for_deployment,
    dump_yaml,
    hardware_for_node,
    template_for_deployment,
    workflow_for_node,
)

# Fixed UUIDs so the test name-generators are deterministic.
DEP_ID = UUID("01234567-89ab-cdef-0123-456789abcdef")
SITE_ID = UUID("fedcba98-7654-3210-fedc-ba9876543210")
NODE_ID = UUID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")


def _node(**overrides):
    base = {
        "id": NODE_ID,
        "hostname": "control-1",
        "mac": "02:00:00:00:00:01",
        "primary_ip_v6": "fd00:site:42:0::10",
        "bmc_address": "10.42.99.10",
        "bmc_creds_secret_ref": "tinkerbell/rd-bmc-control-1",
    }
    base.update(overrides)
    return SimpleNamespace(**base)


def _deployment(nodes=None, **overrides):
    base = {
        "id": DEP_ID,
        "site_id": SITE_ID,
        "name": "site42-prod",
        "nodes": nodes if nodes is not None else [_node()],
    }
    base.update(overrides)
    return SimpleNamespace(**base)


def test_hardware_basic_shape():
    hw = hardware_for_node(_deployment(), _node())
    assert hw["apiVersion"] == TINKERBELL_API_VERSION
    assert hw["kind"] == KIND_HARDWARE
    # Name is DNS-1123 compliant (no dots, no uppercase, ≤63 chars).
    name = hw["metadata"]["name"]
    assert name.islower() and "." not in name and len(name) <= 63
    # The MAC must round-trip exactly — Smee keys reservations by it.
    iface = hw["spec"]["interfaces"][0]
    assert iface["dhcp"]["mac"] == "02:00:00:00:00:01"
    assert iface["netboot"]["allowPXE"] is True


def test_hardware_carries_v6_address_as_metadata():
    # The v1alpha1 dhcp.ip block is v4-typed; we stash the real v6
    # mgmt IP in the instance metadata so operators see it via
    # `kubectl get hardware -o yaml`. Locking this prevents a future
    # refactor from silently dropping it.
    hw = hardware_for_node(_deployment(), _node(primary_ip_v6="fd00:site:42:0::10"))
    assert hw["spec"]["metadata"]["instance"]["ipv6_address"] == "fd00:site:42:0::10"


def test_hardware_omits_v6_metadata_when_absent():
    hw = hardware_for_node(_deployment(), _node(primary_ip_v6=None))
    assert "ipv6_address" not in hw["spec"]["metadata"]["instance"]


def test_template_carries_inner_yaml_string():
    # spec.data is Tinkerbell's YAML-in-YAML pattern. Re-parse the
    # inner string and assert on its structure — that's where the
    # actual install plan lives.
    t = template_for_deployment(_deployment())
    assert t["kind"] == KIND_TEMPLATE
    inner = yaml.safe_load(t["spec"]["data"])
    assert inner["version"] == "0.1"
    action_names = [a["name"] for a in inner["tasks"][0]["actions"]]
    assert action_names == ["stream-image", "write-ignition", "reboot"]


def test_workflow_references_hardware_and_template_by_stable_name():
    dep = _deployment()
    node = _node()
    hw = hardware_for_node(dep, node)
    t = template_for_deployment(dep)
    wf = workflow_for_node(dep, node, image_url="http://central/flatcar.img", ignition_json="{}")
    assert wf["spec"]["templateRef"] == t["metadata"]["name"]
    assert wf["spec"]["hardwareRef"] == hw["metadata"]["name"]
    # device_1 placeholder must be the node MAC so Tink picks the
    # right Hardware to fulfill the worker slot.
    assert wf["spec"]["hardwareMap"]["device_1"] == "02:00:00:00:00:01"
    assert wf["spec"]["hardwareMap"]["image_url"] == "http://central/flatcar.img"
    assert wf["spec"]["hardwareMap"]["ignition_json"] == "{}"


def test_bmc_machine_decomposes_secret_ref():
    # bmc_creds_secret_ref is stored as "<ns>/<name>" in our DB; the
    # Rufio CR wants ns and name as separate fields. Lock the split.
    bm = bmc_machine_for_node(_deployment(), _node(bmc_creds_secret_ref="tinkerbell/rd-bmc-control-1"))
    assert bm["apiVersion"] == BMC_API_VERSION
    assert bm["kind"] == KIND_BMC_MACHINE
    ref = bm["spec"]["connection"]["authSecretRef"]
    assert ref["namespace"] == "tinkerbell"
    assert ref["name"] == "rd-bmc-control-1"


def test_bmc_machine_handles_missing_secret_ref():
    # During very-early states the secret might not exist yet; the
    # generator emits empty strings rather than failing so the dict
    # is at least applyable (k8s will reject; that's the correct
    # next-stage signal).
    bm = bmc_machine_for_node(_deployment(), _node(bmc_creds_secret_ref=None))
    ref = bm["spec"]["connection"]["authSecretRef"]
    assert ref["name"] == "" and ref["namespace"] == ""


def test_crds_for_deployment_order_and_count():
    worker = _node(
        hostname="worker-1",
        mac="02:00:00:00:00:02",
        id=UUID("11111111-2222-3333-4444-555555555555"),
    )
    dep = _deployment(nodes=[_node(), worker])
    crds = crds_for_deployment(dep, image_url="http://x/flat.img")
    # Template first (so Workflow refs resolve), then per-node:
    # Hardware → BMC Machine → Workflow.
    assert crds[0]["kind"] == KIND_TEMPLATE
    kinds = [c["kind"] for c in crds[1:]]
    assert kinds == [
        KIND_HARDWARE, KIND_BMC_MACHINE, KIND_WORKFLOW,  # control-1
        KIND_HARDWARE, KIND_BMC_MACHINE, KIND_WORKFLOW,  # worker-1
    ]


def test_dump_yaml_is_multi_document_stream():
    dep = _deployment()
    crds = crds_for_deployment(dep, image_url="http://x/y.img")
    out = dump_yaml(crds)
    # Multi-doc streams have `---` separators except before the first
    # doc. With 4 CRDs (template + 1 each of HW/BMC/WF) we expect 3
    # separators.
    assert out.count("\n---\n") == 3
    # Round-trip cleanly via safe_load_all.
    loaded = list(yaml.safe_load_all(out))
    assert len(loaded) == 4


def test_common_labels_include_deployment_id_and_name():
    hw = hardware_for_node(_deployment(name="site42"), _node())
    labels = hw["metadata"]["labels"]
    assert labels["dcim.region-deployment"] == str(DEP_ID)
    assert labels["dcim.region-deployment-name"] == "site42"
    assert labels["dcim.region-deployment-node"] == str(NODE_ID)


def test_template_label_has_no_node_id():
    # Templates are per-deployment, not per-node. The node label
    # would be misleading.
    t = template_for_deployment(_deployment())
    assert "dcim.region-deployment-node" not in t["metadata"]["labels"]
