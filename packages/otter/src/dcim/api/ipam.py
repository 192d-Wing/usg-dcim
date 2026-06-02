"""IPAM HTTP routes are fully on otter-go.

PR 17 cutover moved /api/v1/ipam/dhcp/*; PR 18 moved the rest
(fabrics, vrfs, vrf-bgp-peers, supernets, subnets, addresses,
overlays, vnis, vteps, vtep-memberships, free-space, utilization,
bulk endpoints).

The schemas + ORM models still live under packages/otter/src/dcim/
{schemas,models}/ipam.py — they back DB rows the surviving non-IPAM
Python paths still read (DNS sync_from_ipam, alert evaluation
loops, region-deploy preflight, etc.).

This module is kept as a stub so any rare import-path reference in
tooling resolves to a no-op rather than ImportError. The smoke test
at packages/otter/tests/test_smoke.py negative-asserts that the
Python app never advertises /api/v1/ipam/* routes, which would
catch a future re-registration here.
"""
from __future__ import annotations
