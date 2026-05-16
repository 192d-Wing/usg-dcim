"""Drain the local SQLite buffer to the central ingest endpoint.

- mTLS or signed-token auth
- Idempotent batches with retry/backoff via tenacity
- Exits cleanly on shutdown signal
"""

from __future__ import annotations

import secrets
from typing import Any

import httpx
import structlog
from tenacity import retry, retry_if_exception_type, stop_after_attempt, wait_exponential

from .buffer import Buffer
from .config import CollectorConfig

log = structlog.get_logger("collector.forwarder")


class Forwarder:
    def __init__(self, cfg: CollectorConfig, buffer: Buffer):
        self.cfg = cfg
        self.buffer = buffer
        self._token: str | None = None
        if cfg.api_token_file:
            try:
                self._token = open(cfg.api_token_file).read().strip()
            except OSError:
                log.warning("api_token_unavailable", path=cfg.api_token_file)

    def _client(self) -> httpx.AsyncClient:
        kwargs: dict[str, Any] = {"timeout": 30}
        if self.cfg.mtls.enabled and self.cfg.mtls.client_cert and self.cfg.mtls.client_key:
            kwargs["cert"] = (self.cfg.mtls.client_cert, self.cfg.mtls.client_key)
        if self.cfg.mtls.ca_bundle:
            kwargs["verify"] = self.cfg.mtls.ca_bundle
        headers = {}
        if self._token:
            headers["Authorization"] = f"Bearer {self._token}"
        kwargs["headers"] = headers
        return httpx.AsyncClient(**kwargs)

    @retry(
        stop=stop_after_attempt(5),
        wait=wait_exponential(multiplier=0.5, min=0.5, max=10),
        retry=retry_if_exception_type((httpx.HTTPError,)),
        reraise=True,
    )
    async def _post(self, client: httpx.AsyncClient, payload: dict) -> None:
        base = (self.cfg.telemetry_url or self.cfg.ingest_url).rstrip("/")
        url = f"{base}/api/v1/ingest/telemetry"
        r = await client.post(url, json=payload)
        if r.status_code >= 500:
            r.raise_for_status()
        if r.status_code >= 400:
            log.warning("ingest_4xx", status=r.status_code, body=r.text[:500])
            r.raise_for_status()

    async def drain_once(self, max_batch: int = 1000) -> int:
        ids, samples = await self.buffer.drain_batch(max_batch)
        if not ids:
            return 0
        payload = {
            "batch_id": secrets.token_hex(8),
            "site_id": str(self.cfg.site_id),
            "collector_id": str(self.cfg.collector_id),
            "samples": [
                {
                    "asset_id": str(s["asset_id"]),
                    "metric": s["metric"],
                    "value": s["value"],
                    "unit": s.get("unit"),
                    "ts": s["ts"],
                    "tags": s.get("tags") or {},
                }
                for s in samples
            ],
        }
        async with self._client() as client:
            await self._post(client, payload)
        await self.buffer.ack(ids)
        log.info("ingest_ok", count=len(ids))
        return len(ids)

    async def heartbeat(self) -> None:
        depth = await self.buffer.depth()
        url = f"{self.cfg.ingest_url.rstrip('/')}/api/v1/collectors/{self.cfg.collector_id}/heartbeat"
        async with self._client() as client:
            try:
                await client.post(
                    url,
                    json={
                        "queue_depth": depth,
                        "buffered_samples": depth,
                        "version": "0.1.0",
                        "metrics": {},
                    },
                )
            except httpx.HTTPError as e:
                log.warning("heartbeat_failed", err=str(e))
