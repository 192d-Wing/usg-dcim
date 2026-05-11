"""On-site DNS agent.

Polls /api/v1/dns/servers/{id}/bundle every `poll_interval_seconds`
for each configured DnsServer. When the bundle's etag changes, writes
the rendered Corefile + zone files (+ gobgp.yaml when role=recursive)
into the server's `output_dir`, then signals the running CoreDNS and
GoBGP processes to reload via SIGUSR1 / SIGHUP.

Auth piggybacks on the existing collector forwarder client (mTLS or
bearer token from `api_token_file`).
"""

from __future__ import annotations

import asyncio
import os
import signal
from pathlib import Path
from typing import Any
from urllib.parse import urlparse

import httpx
import structlog
import yaml

from .config import CollectorConfig, DnsServerConfig

log = structlog.get_logger("collector.dns_agent")


def _api_base(cfg: CollectorConfig) -> str:
    """Use the operator-provided override if set, otherwise derive
    `https://host:port` from `ingest_url`. The DNS endpoints live
    under the same `/api/v1` prefix as ingest."""
    if cfg.dns.api_base:
        return cfg.dns.api_base.rstrip("/")
    parsed = urlparse(cfg.ingest_url)
    return f"{parsed.scheme}://{parsed.netloc}"


def _client(cfg: CollectorConfig, token: str | None) -> httpx.AsyncClient:
    """Same auth shape as Forwarder._client. Kept separate to avoid
    importing the Forwarder class (and its buffer dependency) into
    this module."""
    kwargs: dict[str, Any] = {"timeout": 30}
    if cfg.mtls.enabled and cfg.mtls.client_cert and cfg.mtls.client_key:
        kwargs["cert"] = (cfg.mtls.client_cert, cfg.mtls.client_key)
    if cfg.mtls.ca_bundle:
        kwargs["verify"] = cfg.mtls.ca_bundle
    headers = {}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    kwargs["headers"] = headers
    return httpx.AsyncClient(**kwargs)


def _read_pid(pidfile: str | None) -> int | None:
    """Best-effort PID read. Missing pidfile is normal during startup
    (CoreDNS hasn't written it yet); we'll catch up on the next poll."""
    if not pidfile:
        return None
    try:
        with open(pidfile) as fh:
            return int(fh.read().strip())
    except (OSError, ValueError):
        return None


def _signal_pid(pid: int | None, sig: signal.Signals) -> bool:
    if pid is None:
        return False
    try:
        os.kill(pid, sig)
        return True
    except (OSError, ProcessLookupError):
        return False


def _atomic_write(path: Path, contents: str) -> None:
    """Write-then-rename so the consumer never sees a half-written
    file. Critical for CoreDNS — a torn zone file will fail to parse
    and stall the reload."""
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(path.suffix + ".tmp")
    with open(tmp, "w", encoding="utf-8") as fh:
        fh.write(contents)
    os.replace(tmp, path)


async def _fetch_bundle(
    client: httpx.AsyncClient, api_base: str, server_id: str, etag: str | None,
) -> dict | None:
    url = f"{api_base}/api/v1/dns/servers/{server_id}/bundle"
    params = {"etag": etag} if etag else None
    r = await client.get(url, params=params)
    if r.status_code == 404:
        log.warning("dns_server_missing", server_id=server_id)
        return None
    r.raise_for_status()
    return r.json()


async def _post_status(
    client: httpx.AsyncClient, api_base: str, server_id: str, payload: dict,
) -> None:
    url = f"{api_base}/api/v1/dns/servers/{server_id}/render-status"
    try:
        await client.post(url, json=payload, timeout=10)
    except httpx.HTTPError as e:
        log.warning("dns_status_post_failed", server_id=server_id, err=str(e))


def _write_bundle(server: DnsServerConfig, bundle: dict) -> None:
    """Materialize the bundle on disk in the layout CoreDNS expects:
      <output_dir>/Corefile
      <output_dir>/zones/<name>.zone   (one per zone)
      <output_dir>/gobgp.yaml          (recursive only)
    """
    out = Path(server.output_dir)
    _atomic_write(out / "Corefile", bundle.get("corefile", ""))
    zones = bundle.get("zones") or {}
    zones_dir = out / "zones"
    if zones_dir.exists():
        # Drop stale zone files before writing the new set; otherwise
        # a deleted zone keeps living in the file plugin until restart.
        for f in zones_dir.iterdir():
            if f.is_file() and f.suffix == ".zone":
                # Only remove if not in the new bundle.
                stem = f.stem
                if stem not in zones:
                    f.unlink()
    for name, text in zones.items():
        _atomic_write(zones_dir / f"{name}.zone", text)
    if server.role == "recursive" and bundle.get("gobgp") is not None:
        _atomic_write(out / "gobgp.yaml", yaml.safe_dump(bundle["gobgp"]))


def _signal_reloads(server: DnsServerConfig) -> dict:
    """Send the right reload signals. CoreDNS reloads on SIGUSR1;
    GoBGP picks up config changes on SIGHUP."""
    coredns_pid = _read_pid(server.coredns_pidfile)
    gobgp_pid = _read_pid(server.gobgp_pidfile)
    sent = {
        "coredns_reloaded": _signal_pid(coredns_pid, signal.SIGUSR1),
        "gobgp_reloaded": (
            _signal_pid(gobgp_pid, signal.SIGHUP)
            if server.role == "recursive" else None
        ),
    }
    return sent


async def _server_loop(
    cfg: CollectorConfig, server: DnsServerConfig, token: str | None,
) -> None:
    api_base = _api_base(cfg)
    last_etag: str | None = None
    log.info(
        "dns_agent_server_start",
        server_id=str(server.id), role=server.role, output=server.output_dir,
    )
    while True:
        try:
            async with _client(cfg, token) as client:
                bundle = await _fetch_bundle(client, api_base, str(server.id), last_etag)
                if bundle is None:
                    await asyncio.sleep(cfg.dns.poll_interval_seconds)
                    continue
                etag = bundle.get("etag")
                if etag and etag == last_etag:
                    # No-op: bundle unchanged since last poll.
                    pass
                else:
                    _write_bundle(server, bundle)
                    sent = _signal_reloads(server)
                    last_etag = etag
                    log.info(
                        "dns_bundle_applied",
                        server_id=str(server.id), etag=etag, **sent,
                    )
                    await _post_status(client, api_base, str(server.id), {
                        "status": "ok", "etag": etag,
                    })
        except asyncio.CancelledError:
            raise
        except Exception as e:  # noqa: BLE001
            log.warning("dns_agent_cycle_failed", server_id=str(server.id), err=str(e))
            try:
                async with _client(cfg, token) as client:
                    await _post_status(client, api_base, str(server.id), {
                        "status": "error", "error": str(e)[:1500],
                    })
            except Exception:  # noqa: BLE001
                pass
        await asyncio.sleep(cfg.dns.poll_interval_seconds)


async def run_dns_agent(cfg: CollectorConfig) -> None:
    """Spawn one polling loop per configured DnsServer. Returns when
    cancelled (typically at collector shutdown)."""
    if not cfg.dns.enabled or not cfg.dns.servers:
        log.info("dns_agent_disabled")
        # Sleep forever so the parent can still cancel us cleanly.
        await asyncio.Event().wait()
        return
    token: str | None = None
    if cfg.api_token_file:
        try:
            token = open(cfg.api_token_file).read().strip()
        except OSError:
            log.warning("dns_agent_no_token", path=cfg.api_token_file)
    tasks = [
        asyncio.create_task(_server_loop(cfg, s, token))
        for s in cfg.dns.servers
    ]
    try:
        await asyncio.gather(*tasks)
    except asyncio.CancelledError:
        for t in tasks:
            t.cancel()
        await asyncio.gather(*tasks, return_exceptions=True)
        raise
