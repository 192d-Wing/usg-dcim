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
from .dnstap import serve_dnstap

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


def _find_pid_by_comm(comm: str) -> int | None:
    """Walk `/proc/<pid>/comm` looking for a process whose name
    matches `comm`. Used to locate resolvers that don't write a
    pidfile of their own (Hickory) — the site-stack compose runs the
    collector with `pid: host` so it shares the resolver pod's PID
    namespace and can SIGHUP/SIGUSR1 by PID directly.

    Linux truncates `/proc/<pid>/comm` to 15 chars (TASK_COMM_LEN), so
    we compare on the truncated form; `hickory-dns` is 11 chars and
    fits, but the helper handles longer process names gracefully.
    """
    if not comm:
        return None
    target = comm[:15]
    try:
        for entry in os.listdir("/proc"):
            if not entry.isdigit():
                continue
            try:
                with open(f"/proc/{entry}/comm") as fh:
                    if fh.read().strip() == target:
                        return int(entry)
            except OSError:
                continue
    except OSError:
        return None
    return None


def _resolve_resolver_pid(server: DnsServerConfig, engine: str) -> int | None:
    """Pick the right PID-discovery strategy per engine. CoreDNS
    writes its own pidfile (set via `-pidfile`); Hickory doesn't, so
    we fall back to scanning `/proc` for the binary name when the
    pidfile is missing. The fallback is also tolerant of a Hickory
    container that crash-loops mid-cycle — the next poll re-resolves
    against the fresh PID."""
    pid = _read_pid(server.coredns_pidfile)
    if pid is not None:
        return pid
    if engine == "hickory":
        return _find_pid_by_comm("hickory-dns")
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


def _stale_files(target_dir: Path, keep: set[str], suffix: str | None) -> list[Path]:
    """Files in target_dir that aren't in `keep` and (when `suffix`
    is set) match that suffix. Caller is responsible for unlinking."""
    if not target_dir.exists():
        return []
    return [
        f for f in target_dir.iterdir()
        if f.is_file()
        and f.name not in keep
        and (suffix is None or f.suffix == suffix)
    ]


def _sync_dir(target_dir: Path, files: dict[str, str], suffix: str | None = None) -> None:
    """Atomically write each (filename, text) into target_dir and
    delete any pre-existing files that aren't in the new set. When
    `suffix` is given, only files with that suffix are considered for
    cleanup so unrelated siblings stay intact."""
    for f in _stale_files(target_dir, set(files), suffix):
        f.unlink()
    for name, text in files.items():
        _atomic_write(target_dir / name, text)


def _write_zones(out: Path, zones: dict[str, str]) -> None:
    zones_dir = out / "zones"
    files = {f"{name}.zone": text for name, text in zones.items()}
    _sync_dir(zones_dir, files, suffix=".zone")


def _write_key_files(out: Path, key_files: dict[str, str]) -> None:
    """DNSSEC .key + .private pairs. Restrictive permissions on the
    .private half — CoreDNS doesn't enforce them but anyone reviewing
    the bundle should see locked-down files."""
    keys_dir = out / "keys"
    _sync_dir(keys_dir, key_files)
    for name in key_files:
        if name.endswith(".private"):
            try:
                os.chmod(keys_dir / name, 0o600)
            except OSError:
                pass


_COREFILE_NAME = "Corefile"
_HICKORY_CONFIG_NAME = "config.toml"


def _config_filename(engine: str) -> str:
    """CoreDNS reads `Corefile` from cwd; Hickory takes a `-c
    config.toml`. The collector writes whichever the bundle calls
    for so the same site stack can run either engine without
    reshuffling the volume layout."""
    return _HICKORY_CONFIG_NAME if engine == "hickory" else _COREFILE_NAME


def _write_bundle(server: DnsServerConfig, bundle: dict) -> None:
    """Materialize the bundle on disk in the layout each engine
    expects:
      <output_dir>/Corefile        (CoreDNS) | config.toml (Hickory)
      <output_dir>/zones/<name>.zone   (one per zone)
      <output_dir>/keys/<basename>.key   (DNSSEC; CoreDNS auth only)
      <output_dir>/keys/<basename>.private
      <output_dir>/gobgp.yaml      (recursive only)

    Stale engine-swap files (a leftover Corefile when the fabric
    just moved to hickory) get removed so the resolver doesn't pick
    up cached config."""
    out = Path(server.output_dir)
    engine = bundle.get("engine") or "coredns"
    cfg_name = _config_filename(engine)
    _atomic_write(out / cfg_name, bundle.get("corefile", ""))
    # Drop the other engine's config file if it's still on disk —
    # otherwise the resolver might read stale config after a
    # mid-flight engine switch.
    other = _COREFILE_NAME if cfg_name == _HICKORY_CONFIG_NAME else _HICKORY_CONFIG_NAME
    other_path = out / other
    if other_path.exists():
        try:
            other_path.unlink()
        except OSError:
            pass
    _write_zones(out, bundle.get("zones") or {})
    _write_key_files(out, bundle.get("key_files") or {})
    if server.role == "recursive" and bundle.get("gobgp") is not None:
        _atomic_write(out / "gobgp.yaml", yaml.safe_dump(bundle["gobgp"]))


def _signal_reloads(server: DnsServerConfig, engine: str) -> dict:
    """Send the right reload signals. CoreDNS reloads on SIGUSR1;
    Hickory reloads on SIGHUP; GoBGP also uses SIGHUP. The bundle's
    engine hint drives which signal we send to the resolver pid."""
    resolver_pid = _resolve_resolver_pid(server, engine)
    resolver_sig = signal.SIGHUP if engine == "hickory" else signal.SIGUSR1
    gobgp_pid = _read_pid(server.gobgp_pidfile)
    return {
        "resolver_reloaded": _signal_pid(resolver_pid, resolver_sig),
        "gobgp_reloaded": (
            _signal_pid(gobgp_pid, signal.SIGHUP)
            if server.role == "recursive" else None
        ),
    }


async def _apply_bundle(
    server: DnsServerConfig, bundle: dict, client: httpx.AsyncClient,
    api_base: str,
) -> None:
    """Materialize one bundle on disk + signal the resolver + post the
    render-status callback. Pulled out of _server_loop so the loop's
    cycle skeleton stays under the complexity cap."""
    engine = bundle.get("engine") or "coredns"
    _write_bundle(server, bundle)
    sent = _signal_reloads(server, engine)
    etag = bundle.get("etag")
    log.info(
        "dns_bundle_applied",
        server_id=str(server.id), engine=engine, etag=etag, **sent,
    )
    await _post_status(client, api_base, str(server.id), {
        "status": "ok", "etag": etag,
    })


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
                if etag != last_etag:
                    await _apply_bundle(server, bundle, client, api_base)
                    last_etag = etag
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


_PROM_LINE_RE = None  # populated lazily; cheap to inline-parse


def _parse_prom_line(line: str) -> tuple[str, dict[str, str], float] | None:
    """Decode one Prometheus text-format line into (name, labels, value).
    Returns None for comments, blanks, and unparseable lines."""
    line = line.strip()
    if not line or line.startswith("#"):
        return None
    try:
        metric_part, value_part = line.rsplit(" ", 1)
        value = float(value_part)
    except ValueError:
        return None
    name, _, label_blob = metric_part.partition("{")
    labels: dict[str, str] = {}
    if label_blob:
        for item in label_blob.rstrip("}").split(","):
            if "=" not in item:
                continue
            k, _, v = item.partition("=")
            labels[k.strip()] = v.strip().strip('"')
    return name, labels, value


_RCODE_KEYS = {"NOERROR": "noerror", "NXDOMAIN": "nxdomain", "SERVFAIL": "servfail"}


def _absorb_coredns_metric(
    counters: dict, name: str, labels: dict, value: float,
) -> None:
    """CoreDNS prometheus plugin shape: `coredns_dns_*` series with
    rcode + duration histograms. Matches the schema since CoreDNS 1.x."""
    if name == "coredns_dns_requests_total":
        counters["requests_total"] += int(value)
        return
    if name == "coredns_dns_responses_total":
        key = _RCODE_KEYS.get(labels.get("rcode", "").upper())
        if key:
            counters[key] += int(value)
        return
    if name == "coredns_dns_request_duration_seconds_bucket":
        _absorb_bucket(counters, labels, value)
        return
    if name == "coredns_dns_request_duration_seconds_count":
        counters["duration_count"] += int(value)


def _absorb_hickory_metric(
    counters: dict, name: str, labels: dict, value: float,
) -> None:
    """Hickory's metrics schema is fundamentally different from
    CoreDNS's — no per-rcode response counter, no overall
    request-duration histogram. Synthesize the downstream-uniform
    shape from what Hickory does expose:

      - **requests_total** : sum `hickory_request_record_types_total`
        across all record-type labels.
      - **noerror/nxdomain/servfail** : Hickory doesn't expose
        per-rcode counters. We leave nxdomain/servfail at 0 and let
        noerror absorb the whole response volume — the dashboard's
        rcode pie is meaningful for CoreDNS auth pods (where most
        DNSSEC chain math + zone-boundary checks happen) and roughly
        unused for the recursive's forward path.
      - **duration buckets + count** : taken from the
        `hickory_resolver_cache_miss_duration_seconds_*` histogram,
        which captures the time to forward + receive an upstream
        answer. Cache-hit latency is sub-millisecond and would
        otherwise drown the p95 of the actually-interesting tail.
    """
    if name == "hickory_request_record_types_total":
        counters["requests_total"] += int(value)
        return
    if name == "hickory_resolver_cache_miss_duration_seconds_bucket":
        _absorb_bucket(counters, labels, value)
        return
    if name == "hickory_resolver_cache_miss_duration_seconds_count":
        counters["duration_count"] += int(value)


def _absorb_bucket(counters: dict, labels: dict, value: float) -> None:
    """Shared histogram-bucket folder — both engines emit `le` in
    seconds, so the percentile computation below is engine-agnostic
    once we've routed the right series into it."""
    try:
        le = float(labels.get("le", "+Inf"))
    except ValueError:
        return
    counters["duration_buckets"][le] = (
        counters["duration_buckets"].get(le, 0) + int(value)
    )


def _absorb_metric(counters: dict, name: str, labels: dict, value: float) -> None:
    """Dispatch one parsed sample to the right engine-specific
    absorber. Anything that doesn't start with a known engine prefix
    is silently dropped (process_*, build_info, etc.)."""
    if name.startswith("coredns_"):
        _absorb_coredns_metric(counters, name, labels, value)
    elif name.startswith("hickory_"):
        _absorb_hickory_metric(counters, name, labels, value)


def _parse_prom_text(text: str) -> dict:
    """Minimal Prometheus text-format parser. Folds per-engine series
    (CoreDNS's `coredns_dns_*`, Hickory's `hickory_request_*` +
    `hickory_resolver_cache_miss_duration_*`) into a uniform counters
    dict the central API understands. Engine-specific gaps in the
    upstream metric schema (Hickory doesn't expose per-rcode response
    counts) are documented in the absorber comments. Multi-line
    samples for the same series (different label combos) are summed."""
    counters: dict[str, dict] = {
        "requests_total": 0,
        "noerror": 0,
        "nxdomain": 0,
        "servfail": 0,
        "duration_buckets": {},  # {le_seconds: cumulative_count}
        "duration_count": 0,
    }
    for raw in text.splitlines():
        parsed = _parse_prom_line(raw)
        if parsed is None:
            continue
        name, labels, value = parsed
        _absorb_metric(counters, name, labels, value)
    return counters


def _percentile_from_buckets(buckets: dict, total: int, percentile: float) -> float | None:
    """Linearly interpolate a percentile across Prometheus histogram
    buckets (the standard Prometheus approach). `buckets` is
    `{le_seconds: cumulative_count}`. Returns None when there isn't
    enough data to make a meaningful estimate."""
    if total < 5 or not buckets:
        return None
    sorted_buckets = sorted(buckets.items())
    target = total * percentile
    prev_le = 0.0
    prev_count = 0.0
    for le, count in sorted_buckets:
        if count >= target:
            if le == float("inf"):
                return None
            span = count - prev_count
            if span <= 0:
                return le * 1000.0
            frac = (target - prev_count) / span
            return (prev_le + frac * (le - prev_le)) * 1000.0
        prev_le, prev_count = le, count
    return None


# Per-server top-K reservoirs populated by the dnstap loop and
# snapshotted by the metrics loop. Keyed by `str(server.id)` so the
# two loops can be in different asyncio tasks without sharing a
# DnsServerConfig reference. Access is protected by `_TOP_NAMES_LOCK`
# because both loops touch it from independent coroutines.
_TOP_NAMES: dict[str, dict[tuple[str, str], int]] = {}
_TOP_NAMES_LOCK = asyncio.Lock()

# Cap the per-server reservoir to bound memory under load. 5000
# distinct (name, type) tuples covers normal traffic with headroom;
# above that we drop new entries (a low-frequency name doesn't
# matter for top-K anyway). The metrics POST trims further to
# `_TOP_NAMES_SHIP_K` so the wire payload stays small.
_TOP_NAMES_CAP = 5000
_TOP_NAMES_SHIP_K = 100


async def _record_query(server_id: str, name: str, qtype: str) -> None:
    """Increment the per-(name, type) counter for `server_id`. Called
    by the dnstap loop on every decoded query. Drops new entries
    when the reservoir is full so a query storm of unique names
    can't blow memory."""
    async with _TOP_NAMES_LOCK:
        bucket = _TOP_NAMES.setdefault(server_id, {})
        key = (name, qtype)
        if key in bucket:
            bucket[key] += 1
            return
        if len(bucket) >= _TOP_NAMES_CAP:
            return
        bucket[key] = 1


async def _snapshot_top_names(server_id: str) -> list[dict]:
    """Atomically take and reset the per-server reservoir, returning
    the top-K entries by count for the metrics POST. Reset-on-read
    matches the per-interval-delta shape of the rcode counters."""
    async with _TOP_NAMES_LOCK:
        bucket = _TOP_NAMES.pop(server_id, {})
    ranked = sorted(bucket.items(), key=lambda kv: kv[1], reverse=True)
    return [
        {"name": n, "type": t, "count": c}
        for (n, t), c in ranked[:_TOP_NAMES_SHIP_K]
    ]


async def _dnstap_loop(server: DnsServerConfig) -> None:
    """Listen on the resolver's dnstap socket and feed each decoded
    query into the per-server top-K reservoir. CoreDNS retries with
    backoff if our listener isn't up yet, so it's fine for this loop
    to start in parallel with bundle apply."""
    socket_path = server.dnstap_socket
    if not socket_path:
        return
    server_id = str(server.id)

    async def on_query(name: str, qtype: str) -> None:
        await _record_query(server_id, name, qtype)

    log.info(
        "dnstap_loop_start", server_id=server_id, socket=socket_path,
    )
    # Restart on unexpected exceptions — the listener should run
    # forever for the life of the collector. asyncio.CancelledError
    # bubbles up so shutdown works.
    while True:
        try:
            await serve_dnstap(socket_path, on_query)
        except asyncio.CancelledError:
            raise
        except Exception as e:  # noqa: BLE001
            log.warning(
                "dnstap_loop_restart", server_id=server_id, err=str(e),
            )
            await asyncio.sleep(2)


async def _metrics_loop(
    cfg: CollectorConfig, server: DnsServerConfig, token: str | None,
) -> None:
    """Per-server scrape loop. Polls the server's Prometheus endpoint,
    diffs against the previous scrape to get per-interval deltas, then
    posts to central. First cycle establishes the baseline and posts
    nothing."""
    api_base = _api_base(cfg)
    interval = cfg.dns.metrics_interval_seconds
    prev: dict | None = None
    log.info(
        "dns_metrics_loop_start",
        server_id=str(server.id), metrics_url=server.metrics_url,
    )
    # No-auth client for the local scrape so we don't send mTLS certs
    # to localhost. Token client is reused for the central POST.
    while True:
        try:
            async with httpx.AsyncClient(timeout=10) as scrape:
                r = await scrape.get(server.metrics_url)
                r.raise_for_status()
                snap = _parse_prom_text(r.text)
            if prev is None:
                prev = snap
            else:
                # Counter resets (CoreDNS restart) appear as a smaller
                # current value — clamp to 0 instead of going negative.
                delta = {
                    "queries": max(0, snap["requests_total"] - prev["requests_total"]),
                    "noerror": max(0, snap["noerror"] - prev["noerror"]),
                    "nxdomain": max(0, snap["nxdomain"] - prev["nxdomain"]),
                    "servfail": max(0, snap["servfail"] - prev["servfail"]),
                }
                # Latency percentiles are computed from the *current*
                # snapshot's histogram — they're already cumulative
                # and converge quickly under load.
                p50 = _percentile_from_buckets(
                    snap["duration_buckets"], snap["duration_count"], 0.50,
                )
                p95 = _percentile_from_buckets(
                    snap["duration_buckets"], snap["duration_count"], 0.95,
                )
                # Snapshot+reset the dnstap reservoir for this
                # interval. Returns an empty list when dnstap isn't
                # wired on this server — central treats null and []
                # differently (null = "not wired anywhere", [] =
                # "wired but zero queries in window") so we ship null
                # when there's no dnstap socket and a list otherwise.
                top_names = (
                    await _snapshot_top_names(str(server.id))
                    if server.dnstap_socket else None
                )
                payload = {
                    "interval_seconds": interval,
                    **delta,
                    "p50_ms": p50,
                    "p95_ms": p95,
                    "top_names": top_names,
                }
                async with _client(cfg, token) as push:
                    url = f"{api_base}/api/v1/dns/servers/{server.id}/metrics"
                    resp = await push.post(url, json=payload, timeout=10)
                    if resp.status_code >= 400:
                        log.warning(
                            "dns_metrics_push_failed",
                            server_id=str(server.id), status=resp.status_code,
                        )
                prev = snap
        except asyncio.CancelledError:
            raise
        except Exception as e:  # noqa: BLE001
            log.warning(
                "dns_metrics_cycle_failed",
                server_id=str(server.id), err=str(e),
            )
            # Drop the baseline so the next successful scrape doesn't
            # produce a delta across a long failure window.
            prev = None
        await asyncio.sleep(interval)


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
    if cfg.dns.metrics_enabled:
        # Honor the per-server `metrics_enabled` knob too — Hickory
        # recursives ship without a /metrics endpoint, so spawning a
        # scrape loop just produces a steady stream of connection-
        # refused warnings until dnstap-based observability lands.
        tasks.extend(
            asyncio.create_task(_metrics_loop(cfg, s, token))
            for s in cfg.dns.servers if s.metrics_enabled
        )
    # One dnstap listener per server that has the socket configured.
    # CoreDNS auth pods are the only consumers today; Hickory has no
    # dnstap output. The reservoir + the metrics loop coordinate via
    # the module-level `_TOP_NAMES` dict.
    tasks.extend(
        asyncio.create_task(_dnstap_loop(s))
        for s in cfg.dns.servers if s.dnstap_socket
    )
    try:
        await asyncio.gather(*tasks)
    except asyncio.CancelledError:
        for t in tasks:
            t.cancel()
        await asyncio.gather(*tasks, return_exceptions=True)
        raise
