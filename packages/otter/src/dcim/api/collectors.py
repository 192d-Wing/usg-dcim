"""Collector HTTP routes are fully on otter-go (PR 21 cutover).

The schemas + ORM models still live under packages/otter/src/dcim/
{schemas,models}/collectors.py — they back DB rows the surviving
non-collector Python paths still read (alert evaluation loop reads
collector status; freshness job in arq still touches last_seen_at).

PR 21 ported the previously-deferred enroll + heartbeat write paths.
Enroll generates `"enroll_" + token_urlsafe(32)`, sha256-hashes it,
and stores the digest in `collectors.enrollment_token_hash` —
identical to Python's `security.tokens.hash_api_token` so existing
collectors continue to authenticate after the cutover. Heartbeat
flips collector.status to `degraded` when the agent reports
`last_error`, `healthy` otherwise, and the response echoes the
current config_overrides back to the agent.

This module is kept as a stub so any rare import-path reference in
tooling resolves to a no-op rather than ImportError. The smoke test
at packages/otter/tests/test_smoke.py negative-asserts that the
Python app never advertises /api/v1/collectors/* routes, which
would catch a future re-registration here.
"""
from __future__ import annotations
