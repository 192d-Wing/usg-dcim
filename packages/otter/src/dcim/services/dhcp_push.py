"""Render + push DHCP scopes to a Kea Control Agent (PR 74).

Two layers:

  * Pure renderers — `render_kea_subnet4(scope, kea_id)` /
    `render_kea_subnet6(scope, kea_id)`. Take a DhcpScope row and a
    numeric Kea subnet ID, return the dict Kea expects in subnet4/6
    objects. No DB, no HTTP. Unit-tested directly.

  * Orchestrator — `push_scope(db, scope)`. Loads the parent
    DhcpServer, allocates a kea_subnet_id if the scope is unpushed,
    renders, calls KeaClient.subnet4_add/update (or v6), writes the
    push status onto DhcpServer.last_push_*, audits, commits.

The orchestrator deliberately uses Kea's `subnet_cmds` hook library
(subnet4-add / subnet4-update / subnet4-del) rather than `config-set`.
`config-set` replaces the *entire* Kea config — DCIM doesn't own the
whole config (interfaces, lease database, hooks list, etc.), so that
would either trample operator state or require DCIM to store all of
Kea's surface. Per-subnet commands sidestep both.

What this module does NOT do (yet):
  * Drift detection (subnet4-get → diff vs render). Single-scope pull
    is straightforward; full-server reconciliation is the bigger
    follow-up.
  * Bulk push for all scopes in one call. A loop over single pushes
    works for tens of scopes; for thousands we'd batch.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import UTC, datetime
from typing import Any

import structlog
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from ..models.ipam import DhcpScope, DhcpServer
from .kea import KeaClient

log = structlog.get_logger("dcim.dhcp_push")


@dataclass
class PushResult:
    scope_id: str
    kea_subnet_id: int | None
    status: str  # "ok" | "error" | "unsupported"
    error: str | None
    raw: Any | None  # raw Kea response, for debugging


# ---------- pure renderers ----------


def _render_pools(pools: list[dict]) -> list[dict]:
    """Kea pool shape: {"pool": "first - last"} as a single string."""
    out: list[dict] = []
    for p in pools or []:
        first = p.get("first")
        last = p.get("last")
        if first and last:
            out.append({"pool": f"{first} - {last}"})
    return out


def _render_pd_pools(pd_pools: list[dict]) -> list[dict]:
    """Kea pd-pool shape: split the {"prefix": "...", "delegated_len":
    N} input into the prefix + prefix-len + delegated-len fields Kea
    expects."""
    out: list[dict] = []
    for p in pd_pools or []:
        prefix = str(p.get("prefix", ""))
        delegated_len = int(p.get("delegated_len", 0))
        if not prefix or not delegated_len:
            continue
        # Split "2001:db8::/56" → "2001:db8::" + 56.
        addr, _, plen = prefix.partition("/")
        if not plen:
            continue
        out.append({
            "prefix": addr,
            "prefix-len": int(plen),
            "delegated-len": delegated_len,
        })
    return out


def _render_options(options: list[dict]) -> list[dict]:
    """Kea option-data: pass through; the columns Kea cares about
    (name, code, data, space, csv-format) are operator-authored on
    the way in via the DhcpOption schema."""
    out: list[dict] = []
    for o in options or []:
        entry: dict = {"data": str(o.get("data", ""))}
        if o.get("name"):
            entry["name"] = o["name"]
        if o.get("code") is not None:
            entry["code"] = int(o["code"])
        if o.get("space"):
            entry["space"] = o["space"]
        out.append(entry)
    return out


def _render_reservations_v4(reservations: list[dict]) -> list[dict]:
    """v4 reservation: hw-address + ip-address + optional hostname."""
    out: list[dict] = []
    for r in reservations or []:
        if not r.get("mac") or not r.get("ip"):
            continue
        entry: dict = {"hw-address": r["mac"], "ip-address": r["ip"]}
        if r.get("hostname"):
            entry["hostname"] = r["hostname"]
        out.append(entry)
    return out


def _render_reservations_v6(reservations: list[dict]) -> list[dict]:
    """v6 reservation: duid + ip-addresses (Kea v6 takes a list of IPs
    per reservation; we expose a single ip on the DCIM side, wrap on
    the way out)."""
    out: list[dict] = []
    for r in reservations or []:
        if not r.get("duid") or not r.get("ip"):
            continue
        entry: dict = {"duid": r["duid"], "ip-addresses": [r["ip"]]}
        if r.get("hostname"):
            entry["hostname"] = r["hostname"]
        out.append(entry)
    return out


def render_kea_subnet4(scope: DhcpScope, kea_id: int) -> dict:
    """Project a v4 DhcpScope row onto Kea's subnet4 object."""
    out: dict = {
        "id": int(kea_id),
        "subnet": str(scope.prefix),
        "pools": _render_pools(scope.pools_json or []),
        "option-data": _render_options(scope.options_json or []),
        "reservations": _render_reservations_v4(scope.reservations_json or []),
        "valid-lifetime": int(scope.valid_lifetime_seconds),
    }
    if scope.renew_timer_seconds is not None:
        out["renew-timer"] = int(scope.renew_timer_seconds)
    if scope.rebind_timer_seconds is not None:
        out["rebind-timer"] = int(scope.rebind_timer_seconds)
    return out


def render_kea_subnet6(scope: DhcpScope, kea_id: int) -> dict:
    """Project a v6 DhcpScope row onto Kea's subnet6 object."""
    out: dict = {
        "id": int(kea_id),
        "subnet": str(scope.prefix),
        "pools": _render_pools(scope.pools_json or []),
        "option-data": _render_options(scope.options_json or []),
        "reservations": _render_reservations_v6(scope.reservations_json or []),
        "valid-lifetime": int(scope.valid_lifetime_seconds),
    }
    if scope.preferred_lifetime_seconds is not None:
        out["preferred-lifetime"] = int(scope.preferred_lifetime_seconds)
    if scope.renew_timer_seconds is not None:
        out["renew-timer"] = int(scope.renew_timer_seconds)
    if scope.rebind_timer_seconds is not None:
        out["rebind-timer"] = int(scope.rebind_timer_seconds)
    pd = _render_pd_pools(scope.pd_pools_json or [])
    if pd:
        out["pd-pools"] = pd
    return out


# ---------- ID allocation ----------


async def _allocate_kea_subnet_id(db: AsyncSession, server_id) -> int:
    """Find the lowest unused positive integer for this server. Kea
    rejects id=0 (reserved as "unspecified" in some commands), so
    we start at 1.

    A real production stack might persist a per-server sequence;
    O(n) max-scan is fine until a server has thousands of scopes.
    """
    rows = (
        await db.execute(
            select(DhcpScope.kea_subnet_id)
            .where(DhcpScope.dhcp_server_id == server_id)
            .where(DhcpScope.kea_subnet_id.is_not(None))
        )
    ).scalars().all()
    used = {int(x) for x in rows if x is not None}
    candidate = 1
    while candidate in used:
        candidate += 1
    return candidate


# ---------- response parsing ----------


def _interpret_kea_response(resp: Any) -> tuple[str, str | None]:
    """Map Kea's `[{"result": N, "text": "..."}]` shape to (status, error).

    Kea result codes:
      0  success
      1  generic error
      2  unsupported (hook not loaded, command not implemented)
      3  empty / not found
      4  conflict (some commands)

    We treat 0/3 as ok (3 on delete = "wasn't there, that's fine");
    everything else surfaces as an error string. Multi-service
    responses get scanned for the first non-ok entry — partial
    success is an error.
    """
    if not isinstance(resp, list) or not resp:
        return "error", f"unexpected Kea response shape: {resp!r}"
    first_err: str | None = None
    saw_unsupported = False
    for entry in resp:
        if not isinstance(entry, dict):
            continue
        code = entry.get("result")
        text = entry.get("text") or ""
        if code in (0, 3):
            continue
        if code == 2:
            saw_unsupported = True
            first_err = first_err or f"unsupported: {text}"
            continue
        first_err = first_err or f"kea result={code}: {text}"
    if first_err is None:
        return "ok", None
    return ("unsupported" if saw_unsupported else "error"), first_err


# ---------- orchestrator ----------


async def push_scope(db: AsyncSession, scope: DhcpScope) -> PushResult:
    """Push one scope to its parent Kea server.

    Side effects on the DhcpServer row: last_push_at/status/error are
    updated regardless of outcome so operators can see the failure in
    the UI without tail -f'ing logs.
    """
    server = await db.get(DhcpServer, scope.dhcp_server_id)
    if server is None:
        return PushResult(
            scope_id=str(scope.id), kea_subnet_id=None,
            status="error", error="parent dhcp server not found", raw=None,
        )
    if not server.enabled:
        return PushResult(
            scope_id=str(scope.id), kea_subnet_id=scope.kea_subnet_id,
            status="error", error="dhcp server disabled; refusing to push",
            raw=None,
        )

    is_update = scope.kea_subnet_id is not None
    if not is_update:
        scope.kea_subnet_id = await _allocate_kea_subnet_id(
            db, server_id=server.id,
        )

    client = KeaClient(
        server.kea_url,
        username=server.auth_username,
        password=server.auth_password,
    )

    try:
        if scope.ip_family == 4:
            subnet_def = render_kea_subnet4(scope, scope.kea_subnet_id)
            resp = await (
                client.subnet4_update(subnet_def) if is_update
                else client.subnet4_add(subnet_def)
            )
            await client.config_write(["dhcp4"])
        else:
            subnet_def = render_kea_subnet6(scope, scope.kea_subnet_id)
            resp = await (
                client.subnet6_update(subnet_def) if is_update
                else client.subnet6_add(subnet_def)
            )
            await client.config_write(["dhcp6"])
    except Exception as e:  # noqa: BLE001 — surface any transport error verbatim
        status, err = "error", f"{type(e).__name__}: {e}"
        log.error("dhcp_push.transport_error", scope_id=str(scope.id), error=err)
        await _record_push_status(db, server, status, err)
        # Roll back the optimistic id allocation if this was a first
        # push that never made it to Kea — leaves the scope in its
        # pre-push state so a retry doesn't burn an id.
        if not is_update:
            scope.kea_subnet_id = None
        return PushResult(
            scope_id=str(scope.id), kea_subnet_id=scope.kea_subnet_id,
            status=status, error=err, raw=None,
        )

    status, err = _interpret_kea_response(resp)
    if status != "ok" and not is_update:
        # Same rollback as the transport-error branch: id wasn't
        # actually claimed in Kea.
        scope.kea_subnet_id = None
    await _record_push_status(db, server, status, err)
    return PushResult(
        scope_id=str(scope.id),
        kea_subnet_id=scope.kea_subnet_id,
        status=status, error=err, raw=resp,
    )


async def delete_scope_from_kea(scope: DhcpScope, server: DhcpServer) -> PushResult:
    """Best-effort Kea-side cleanup before a DCIM DELETE. Returns ok
    if the scope was never pushed (kea_subnet_id IS NULL) — nothing
    to clean up.

    Kept separate from push_scope so the DELETE endpoint can pass-or-
    log without entangling the DB write back into Kea state."""
    if scope.kea_subnet_id is None:
        return PushResult(
            scope_id=str(scope.id), kea_subnet_id=None,
            status="ok", error=None, raw=None,
        )
    client = KeaClient(
        server.kea_url,
        username=server.auth_username,
        password=server.auth_password,
    )
    try:
        if scope.ip_family == 4:
            resp = await client.subnet4_del(scope.kea_subnet_id)
            await client.config_write(["dhcp4"])
        else:
            resp = await client.subnet6_del(scope.kea_subnet_id)
            await client.config_write(["dhcp6"])
    except Exception as e:  # noqa: BLE001
        return PushResult(
            scope_id=str(scope.id), kea_subnet_id=scope.kea_subnet_id,
            status="error", error=f"{type(e).__name__}: {e}", raw=None,
        )
    status, err = _interpret_kea_response(resp)
    return PushResult(
        scope_id=str(scope.id), kea_subnet_id=scope.kea_subnet_id,
        status=status, error=err, raw=resp,
    )


async def _record_push_status(
    db: AsyncSession, server: DhcpServer, status: str, error: str | None,
) -> None:
    server.last_push_at = datetime.now(UTC)
    server.last_push_status = status
    server.last_push_error = error[:2048] if error else None
    await db.flush()
