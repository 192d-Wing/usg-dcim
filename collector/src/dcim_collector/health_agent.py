"""Per-site DNS health-check probe loop.

Pulls the fabric's DnsHealthCheck list from central, runs probes
locally (tcp/http/https), and posts each result back. Designed to
fill the gap central's worker can't cover when target IPs live on
private site networks central has no route to.

The central worker still runs in fallback mode — it skips checks
whose last_checked_at advanced within `interval_seconds * 1.5`, so
a collector quietly takes over the moment results start arriving.
A collector that drops offline cedes back to central automatically.
"""

from __future__ import annotations

import asyncio
import socket
from typing import Any
from urllib.parse import urlparse

import httpx
import structlog

from .config import CollectorConfig

log = structlog.get_logger("collector.health_agent")


def _api_base(cfg: CollectorConfig) -> str:
    if cfg.dns.api_base:
        return cfg.dns.api_base.rstrip("/")
    parsed = urlparse(cfg.ingest_url)
    return f"{parsed.scheme}://{parsed.netloc}"


def _client(cfg: CollectorConfig, token: str | None) -> httpx.AsyncClient:
    kwargs: dict[str, Any] = {"timeout": 15}
    if cfg.mtls.enabled and cfg.mtls.client_cert and cfg.mtls.client_key:
        kwargs["cert"] = (cfg.mtls.client_cert, cfg.mtls.client_key)
    if cfg.mtls.ca_bundle:
        kwargs["verify"] = cfg.mtls.ca_bundle
    headers = {}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    kwargs["headers"] = headers
    return httpx.AsyncClient(**kwargs)


async def _probe_tcp(target: str, port: int, timeout: int) -> tuple[str, str | None]:
    if port <= 0:
        return "unhealthy", "tcp probe requires a port"
    try:
        fut = asyncio.open_connection(target, port)
        _, writer = await asyncio.wait_for(fut, timeout=timeout)
        writer.close()
        try:
            await writer.wait_closed()
        except Exception:  # noqa: BLE001
            pass
        return "healthy", None
    except Exception as e:  # noqa: BLE001
        return "unhealthy", f"tcp probe failed: {e}"[:512]


async def _probe_http(
    proto: str, target: str, port: int, path: str, timeout: int,
) -> tuple[str, str | None]:
    url = f"{proto}://{target}:{port}{path or '/'}"
    try:
        async with httpx.AsyncClient(
            timeout=timeout,
            verify=True,
            follow_redirects=False,
        ) as client:
            r = await client.get(url)
    except Exception as e:  # noqa: BLE001
        return "unhealthy", f"http probe failed: {e}"[:512]
    if 200 <= r.status_code < 400:
        return "healthy", None
    return "unhealthy", f"http {r.status_code}"


async def _probe(check: dict) -> tuple[str, str | None]:
    """One probe — mirror of services.dns.probe_health_check on the
    central side, but standalone so we don't drag the backend code
    into the collector."""
    proto = check["protocol"]
    target = str(check["target_ip"]).split("/", 1)[0]
    timeout = check.get("timeout_seconds") or 5
    if proto == "icmp":
        return "unknown", "icmp not supported in collector probes"
    if proto == "tcp":
        return await _probe_tcp(target, int(check.get("port") or 0), timeout)
    if proto in ("http", "https"):
        port = int(check.get("port") or (443 if proto == "https" else 80))
        return await _probe_http(proto, target, port, check.get("path") or "/", timeout)
    _ = socket  # keep import alive for a future ICMP impl
    return "unknown", "unsupported protocol"


async def _cycle(cfg: CollectorConfig, token: str | None) -> None:
    api_base = _api_base(cfg)
    fabric_id = cfg.dns.health_check_fabric_id
    if fabric_id is None:
        return
    async with _client(cfg, token) as client:
        r = await client.get(
            f"{api_base}/api/v1/dns/health-checks",
            params={"fabric_id": str(fabric_id), "page_size": 500},
        )
        r.raise_for_status()
        items = (r.json() or {}).get("items") or []
        for check in items:
            if not check.get("enabled"):
                continue
            status, error = await _probe(check)
            try:
                resp = await client.post(
                    f"{api_base}/api/v1/dns/health-checks/{check['id']}/result",
                    json={"status": status, "error": error},
                )
                if resp.status_code >= 400:
                    log.warning(
                        "health_check_post_failed",
                        check_id=check["id"], status=resp.status_code,
                    )
            except httpx.HTTPError as e:
                log.warning(
                    "health_check_post_error",
                    check_id=check["id"], err=str(e),
                )


async def run_health_agent(cfg: CollectorConfig) -> None:
    """Top-level entry — sleeps forever when health checks aren't
    enabled so the parent can still cancel us cleanly."""
    if not cfg.dns.enabled or not cfg.dns.health_checks_enabled:
        log.info("health_agent_disabled")
        await asyncio.Event().wait()
        return
    if cfg.dns.health_check_fabric_id is None:
        log.warning("health_agent_no_fabric — set dns.health_check_fabric_id to enable")
        await asyncio.Event().wait()
        return
    token: str | None = None
    if cfg.api_token_file:
        try:
            token = open(cfg.api_token_file).read().strip()
        except OSError:
            log.warning("health_agent_no_token", path=cfg.api_token_file)
    interval = cfg.dns.health_check_poll_interval_seconds
    log.info(
        "health_agent_start",
        fabric_id=str(cfg.dns.health_check_fabric_id),
        poll_interval_seconds=interval,
    )
    while True:
        try:
            await _cycle(cfg, token)
        except asyncio.CancelledError:
            raise
        except Exception as e:  # noqa: BLE001
            log.warning("health_agent_cycle_failed", err=str(e))
        await asyncio.sleep(interval)
