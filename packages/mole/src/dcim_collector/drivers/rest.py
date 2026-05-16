"""Generic REST driver — fetches a JSON document and extracts metrics by JSON-path-ish keys."""

from __future__ import annotations

from datetime import UTC, datetime

import httpx
import structlog

from ..config import DeviceConfig

log = structlog.get_logger("collector.rest")


def _resolve(doc, path: str):
    cur = doc
    for part in path.split("."):
        if isinstance(cur, dict):
            cur = cur.get(part)
        else:
            return None
    return cur


class RestDriver:
    name = "rest"

    async def poll(self, device: DeviceConfig) -> list[dict]:
        cfg = device.rest
        if cfg is None:
            return []
        ts = datetime.now(UTC).isoformat()
        out: list[dict] = []
        async with httpx.AsyncClient(verify=cfg.verify_tls, headers=cfg.headers, timeout=10) as client:
            try:
                r = await client.get(cfg.base_url)
                r.raise_for_status()
                doc = r.json()
            except Exception as e:  # pragma: no cover
                log.warning("rest_fetch_failed", err=str(e))
                return []
            for metric, path in cfg.paths.items():
                v = _resolve(doc, path)
                if isinstance(v, int | float):
                    out.append({
                        "asset_id": device.asset_id, "metric": metric, "value": float(v),
                        "unit": None, "ts": ts, "tags": {"source": cfg.base_url},
                    })
        return out
