"""Versioned API surface — v1.

Empty after the full Python→Go cutover. Every former route lives on
otter-go now; see deploy/helm/dcim/values.yaml for the ingress map.
The FastAPI app still exists in `app.py` so the otter container starts
and serves /healthz, but no business routes are mounted here. The
otter container itself is queued for deletion in a follow-up alongside
the otter helm subchart + the CI ruff+pytest job — the orchestrator
worker (`packages/otter/src/dcim/worker.py`, run as otter-worker) is
the only Python piece still serving real traffic.
"""

from fastapi import APIRouter

router = APIRouter()
