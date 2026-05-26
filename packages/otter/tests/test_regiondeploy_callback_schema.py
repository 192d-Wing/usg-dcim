"""Schema validator tests for the kubeconfig-callback payload.

The validator on `RegionDeploymentKubeconfigCallback` exists so the
in-cluster bash unit can base64-encode admin.conf rather than escape
the multi-line YAML by hand. We assert here that exactly one of the
two fields is required, and that a supplied `kubeconfig_b64` is
decoded into `kubeconfig` before any handler sees the payload.
"""
from __future__ import annotations

import base64
from uuid import uuid4

import pytest
from pydantic import ValidationError

from dcim.schemas.regiondeploy import RegionDeploymentKubeconfigCallback


def test_kubeconfig_plaintext_accepted():
    p = RegionDeploymentKubeconfigCallback(
        node_id=uuid4(), kubeconfig="apiVersion: v1\nkind: Config\n",
    )
    assert p.kubeconfig.startswith("apiVersion: v1")
    assert p.kubeconfig_b64 is None


def test_kubeconfig_b64_decoded_into_kubeconfig():
    raw = "apiVersion: v1\nkind: Config\n"
    encoded = base64.b64encode(raw.encode("utf-8")).decode("ascii")
    p = RegionDeploymentKubeconfigCallback(
        node_id=uuid4(), kubeconfig_b64=encoded,
    )
    assert p.kubeconfig == raw
    # Validator normalises into kubeconfig only — handlers never see
    # the encoded form.
    assert p.kubeconfig_b64 is None


def test_both_fields_rejected():
    with pytest.raises(ValidationError, match="not both"):
        RegionDeploymentKubeconfigCallback(
            node_id=uuid4(), kubeconfig="x", kubeconfig_b64="eA==",
        )


def test_neither_field_rejected():
    with pytest.raises(ValidationError, match="required"):
        RegionDeploymentKubeconfigCallback(node_id=uuid4())


def test_invalid_base64_rejected():
    with pytest.raises(ValidationError, match="valid base64"):
        RegionDeploymentKubeconfigCallback(
            node_id=uuid4(), kubeconfig_b64="not!!base64",
        )
