"""Organization HTTP routes are fully on otter-go (PR 19 cutover).

The schemas + ORM model still live under packages/otter/src/dcim/
{schemas,models}/organization.py — they back DB rows the surviving
non-organization Python paths still read.

This module is kept as a stub so any rare import-path reference in
tooling resolves to a no-op rather than ImportError. The smoke test
at packages/otter/tests/test_smoke.py negative-asserts that the
Python app never advertises /api/v1/organizations/* routes, which
would catch a future re-registration here.
"""
from __future__ import annotations
