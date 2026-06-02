"""Stencils HTTP routes are fully on otter-go (PR 19 cutover).

The schemas + DB query for the stencils LIST endpoint live on Go in
packages/otter-go/internal/stencils. This module is kept as a stub
so any rare import-path reference in tooling resolves to a no-op
rather than ImportError. The smoke test at
packages/otter/tests/test_smoke.py negative-asserts that the Python
app never advertises /api/v1/stencils/* routes.
"""
from __future__ import annotations
