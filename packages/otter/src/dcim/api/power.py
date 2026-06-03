"""Power HTTP routes are fully on otter-go (PR 20 cutover).

The schemas + ORM models still live under packages/otter/src/dcim/
{schemas,models}/power.py — they back DB rows the surviving non-power
Python paths still read (rack visualizer, capacity dashboards, etc.).

Wire-shape parity was verified before the ingress flip: Go matches
Python on the capability codes (power:outlets:{read,create,delete}),
audit event shape (action="power.connect"/"power.disconnect",
target_type="outlet", metadata.asset_id + metadata.psu_index), and
the friendly 409 message for double-connects.

This module is kept as a stub so any rare import-path reference in
tooling resolves to a no-op rather than ImportError. The smoke test
at packages/otter/tests/test_smoke.py negative-asserts that the
Python app never advertises /api/v1/power/* routes, which would
catch a future re-registration here.
"""
from __future__ import annotations
