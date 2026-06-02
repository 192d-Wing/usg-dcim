"""Telemetry ingest HTTP route is fully on otter-go (PR 22 cutover).

heron remains the canonical mTLS bulk path; this JSON fallback is
served by otter-go's internal/ingest package now. The pipeline
(freshness upsert per asset/metric → ON CONFLICT DO NOTHING insert
into TimescaleDB hypertable → response shape `{accepted, errors,
received_at}`) ported verbatim.

The schemas + service helper still live under packages/otter/src/
dcim/{schemas,services}/telemetry.py — they back the worker's
freshness rollups and the alert evaluation loop (which reads
telemetry_sources directly, not via this endpoint).

Prometheus emission deliberately not reproduced on Go: heron emits
the dcim_telemetry_* counters, and metrics.py:26-38 documents the
DCIM_DISABLE_GO_PORTED_METRICS lever for shutting off the Python
counters once the cutover is verified.

This module is kept as a stub so any rare import-path reference in
tooling resolves to a no-op rather than ImportError. The smoke test
at packages/otter/tests/test_smoke.py negative-asserts that the
Python app never advertises /api/v1/ingest/* routes.
"""
from __future__ import annotations
