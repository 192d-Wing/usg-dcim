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

import time
from contextlib import contextmanager
from dataclasses import dataclass, field
from datetime import UTC, datetime
from types import SimpleNamespace
from typing import Any

import structlog
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from .. import metrics
from ..models.ipam import DhcpScope, DhcpScopePushHistory, DhcpScopeTemplate, DhcpServer
from .kea import KeaClient


@contextmanager
def _kea_call_timer(server_id: str, operation: str):
    """Record Kea RPC duration into the histogram (PR 98).

    Yields a small holder; the caller stamps `holder.status` with the
    interpreted result ("ok" / "error" / "unsupported") before the
    block exits. The status is the same taxonomy push_scope and
    diff_scope return so dashboards can slice latency by outcome.

    On exception, status defaults to "error" — the operator still
    gets the duration even when the RPC blew up.
    """
    holder = SimpleNamespace(status="error")
    start = time.perf_counter()
    try:
        yield holder
    finally:
        elapsed = time.perf_counter() - start
        metrics.dhcp_kea_call_seconds.labels(
            server_id=server_id, operation=operation, status=holder.status,
        ).observe(elapsed)

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


# Renderer fallback when both scope and template leave valid-lifetime
# unset. Kea defaults to 7200; we pick 3600 to match the column default
# DhcpScope used pre-PR 78.
_DEFAULT_VALID_LIFETIME = 3600


def render_kea_subnet4(scope, kea_id: int) -> dict:
    """Project a v4 DhcpScope (or template-merged effective scope)
    onto Kea's subnet4 object. Duck-typed: accepts a real DhcpScope
    row or the SimpleNamespace `merge_template_into_scope` returns."""
    vl = scope.valid_lifetime_seconds
    out: dict = {
        "id": int(kea_id),
        "subnet": str(scope.prefix),
        "pools": _render_pools(scope.pools_json or []),
        "option-data": _render_options(scope.options_json or []),
        "reservations": _render_reservations_v4(scope.reservations_json or []),
        "valid-lifetime": int(vl) if vl is not None else _DEFAULT_VALID_LIFETIME,
    }
    if scope.renew_timer_seconds is not None:
        out["renew-timer"] = int(scope.renew_timer_seconds)
    if scope.rebind_timer_seconds is not None:
        out["rebind-timer"] = int(scope.rebind_timer_seconds)
    return out


def render_kea_subnet6(scope, kea_id: int) -> dict:
    """Project a v6 DhcpScope (or template-merged effective scope)
    onto Kea's subnet6 object."""
    vl = scope.valid_lifetime_seconds
    out: dict = {
        "id": int(kea_id),
        "subnet": str(scope.prefix),
        "pools": _render_pools(scope.pools_json or []),
        "option-data": _render_options(scope.options_json or []),
        "reservations": _render_reservations_v6(scope.reservations_json or []),
        "valid-lifetime": int(vl) if vl is not None else _DEFAULT_VALID_LIFETIME,
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


# ---------- template merge (PR 78) ----------


def _option_key(opt: dict) -> Any:
    """Stable identity for a Kea option-data entry. Code wins when
    present (it's the wire ID Kea actually keys on); name is the
    fallback. (code, space) pair handles vendor-options with
    separate code spaces."""
    code = opt.get("code")
    if code is not None:
        return ("code", int(code), opt.get("space") or "")
    return ("name", opt.get("name") or "", opt.get("space") or "")


def _merge_options(template_opts: list[dict], scope_opts: list[dict]) -> list[dict]:
    """Scope entries win on conflict; new scope entries append.

    Stable order: template entries first (in template order), then
    scope-only entries (in scope order). Operators see template
    defaults at the top of the option-data array in Kea config
    dumps, which makes review easier."""
    by_key: dict[Any, dict] = {}
    order: list[Any] = []
    for o in template_opts or []:
        k = _option_key(o)
        if k not in by_key:
            order.append(k)
        by_key[k] = dict(o)
    for o in scope_opts or []:
        k = _option_key(o)
        if k not in by_key:
            order.append(k)
        by_key[k] = dict(o)  # scope overrides
    return [by_key[k] for k in order]


def merge_template_into_scope(
    scope: DhcpScope, template: DhcpScopeTemplate | None,
) -> Any:
    """Build the effective scope the renderer should consume.

    With `template=None` this is a near-identity (returns a
    SimpleNamespace with the scope's own values) so callers don't
    need to branch.

    Merge rules:
      * Timers (valid_lifetime / renew / rebind / preferred_lifetime):
        scope value wins when not None; otherwise inherit template.
      * options_json: merged by (code|name, space) — scope entries
        override template entries with the same key, new ones append.
      * Everything else (prefix, pools, pd_pools, reservations,
        ip_family, kea_subnet_id, id, dhcp_server_id): from scope.

    Returns a SimpleNamespace so the existing pure renderers can
    duck-type on attribute access without instantiating a real
    DhcpScope row.
    """
    if template is None:
        # Identity pass-through. Still returns a SimpleNamespace so
        # the renderer's attribute access is uniform across paths.
        return SimpleNamespace(
            id=scope.id,
            dhcp_server_id=scope.dhcp_server_id,
            ip_family=scope.ip_family,
            prefix=scope.prefix,
            pools_json=scope.pools_json,
            pd_pools_json=scope.pd_pools_json,
            options_json=scope.options_json,
            reservations_json=scope.reservations_json,
            valid_lifetime_seconds=scope.valid_lifetime_seconds,
            renew_timer_seconds=scope.renew_timer_seconds,
            rebind_timer_seconds=scope.rebind_timer_seconds,
            preferred_lifetime_seconds=scope.preferred_lifetime_seconds,
            kea_subnet_id=getattr(scope, "kea_subnet_id", None),
            enabled=getattr(scope, "enabled", True),
        )
    return SimpleNamespace(
        id=scope.id,
        dhcp_server_id=scope.dhcp_server_id,
        ip_family=scope.ip_family,
        prefix=scope.prefix,
        pools_json=scope.pools_json,
        pd_pools_json=scope.pd_pools_json,
        options_json=_merge_options(
            template.options_json or [], scope.options_json or [],
        ),
        reservations_json=scope.reservations_json,
        valid_lifetime_seconds=(
            scope.valid_lifetime_seconds
            if scope.valid_lifetime_seconds is not None
            else template.valid_lifetime_seconds
        ),
        renew_timer_seconds=(
            scope.renew_timer_seconds
            if scope.renew_timer_seconds is not None
            else template.renew_timer_seconds
        ),
        rebind_timer_seconds=(
            scope.rebind_timer_seconds
            if scope.rebind_timer_seconds is not None
            else template.rebind_timer_seconds
        ),
        preferred_lifetime_seconds=(
            scope.preferred_lifetime_seconds
            if scope.preferred_lifetime_seconds is not None
            else template.preferred_lifetime_seconds
        ),
        kea_subnet_id=getattr(scope, "kea_subnet_id", None),
        enabled=getattr(scope, "enabled", True),
    )


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
    operation = "update" if is_update else "add"
    if not is_update:
        scope.kea_subnet_id = await _allocate_kea_subnet_id(
            db, server_id=server.id,
        )
    # PR 104 — capture wall-clock so the history row records how long
    # the attempt actually took. Starts before rendering so the row
    # reflects total push latency, not just the Kea RPC.
    push_start = time.perf_counter()

    # PR 78 — merge template defaults under scope overrides before
    # rendering. The renderer stays pure; the merge happens here so
    # diff_scope and the bundle renderer can do the same load and
    # share the contract.
    template = (
        await db.get(DhcpScopeTemplate, scope.template_id)
        if scope.template_id else None
    )
    effective = merge_template_into_scope(scope, template)

    client = KeaClient(
        server.kea_url,
        username=server.auth_username,
        password=server.auth_password,
    )

    # PR 98 — wrap the Kea RPC + config_write in one timer span.
    # The timer's holder.status is stamped from the interpreted
    # response below so dashboards can slice latency by outcome.
    with _kea_call_timer(str(server.id), "push") as timer:
        try:
            if scope.ip_family == 4:
                subnet_def = render_kea_subnet4(effective, scope.kea_subnet_id)
                resp = await (
                    client.subnet4_update(subnet_def) if is_update
                    else client.subnet4_add(subnet_def)
                )
                await client.config_write(["dhcp4"])
            else:
                subnet_def = render_kea_subnet6(effective, scope.kea_subnet_id)
                resp = await (
                    client.subnet6_update(subnet_def) if is_update
                    else client.subnet6_add(subnet_def)
                )
                await client.config_write(["dhcp6"])
        except Exception as e:  # noqa: BLE001 — surface any transport error verbatim
            status, err = "error", f"{type(e).__name__}: {e}"
            timer.status = status  # noqa: B010 — context manager holder
            log.error("dhcp_push.transport_error", scope_id=str(scope.id), error=err)
            await _record_push_status(db, server, status, err)
            # PR 104 — record before the id rollback so the history
            # row preserves which kea_subnet_id was being attempted.
            await _record_push_history(
                db, scope, server, operation, status, err,
                int((time.perf_counter() - push_start) * 1000),
            )
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
        timer.status = status  # noqa: B010
    duration_ms = int((time.perf_counter() - push_start) * 1000)
    await _record_push_history(db, scope, server, operation, status, err, duration_ms)
    if status != "ok" and not is_update:
        # Same rollback as the transport-error branch: id wasn't
        # actually claimed in Kea.
        scope.kea_subnet_id = None
    await _record_push_status(db, server, status, err)
    # PR 80 — a successful push is by construction a re-sync. Clear
    # the drift cache so LIST and push-drifted reflect reality. Don't
    # touch it on error — the previous diff result is still the
    # operator's best information.
    if status == "ok":
        scope.last_diff_at = datetime.now(UTC)
        scope.last_diff_status = "in_sync"
        scope.last_diff_delta_json = None
    return PushResult(
        scope_id=str(scope.id),
        kea_subnet_id=scope.kea_subnet_id,
        status=status, error=err, raw=resp,
    )


async def delete_scope_from_kea(
    scope: DhcpScope, server: DhcpServer,
    db: AsyncSession | None = None,
) -> PushResult:
    """Best-effort Kea-side cleanup before a DCIM DELETE. Returns ok
    if the scope was never pushed (kea_subnet_id IS NULL) — nothing
    to clean up.

    Kept separate from push_scope so the DELETE endpoint can pass-or-
    log without entangling the DB write back into Kea state.

    `db` is optional for backwards compatibility with PR 74 callers
    that don't have a session in hand; when present, PR 104 records
    a push-history row for the attempt."""
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
    # PR 98 — same timing pattern as push_scope. delete is its own
    # operation label so dashboards can distinguish add/update RPCs
    # (typically slow on busy servers) from delete RPCs (usually
    # cheap).
    delete_start = time.perf_counter()
    with _kea_call_timer(str(server.id), "delete") as timer:
        try:
            if scope.ip_family == 4:
                resp = await client.subnet4_del(scope.kea_subnet_id)
                await client.config_write(["dhcp4"])
            else:
                resp = await client.subnet6_del(scope.kea_subnet_id)
                await client.config_write(["dhcp6"])
        except Exception as e:  # noqa: BLE001
            timer.status = "error"  # noqa: B010
            err = f"{type(e).__name__}: {e}"
            duration_ms = int((time.perf_counter() - delete_start) * 1000)
            if db is not None:
                await _record_push_history(
                    db, scope, server, "delete", "error", err, duration_ms,
                )
            return PushResult(
                scope_id=str(scope.id), kea_subnet_id=scope.kea_subnet_id,
                status="error", error=err, raw=None,
            )
        status, err = _interpret_kea_response(resp)
        timer.status = status  # noqa: B010
    duration_ms = int((time.perf_counter() - delete_start) * 1000)
    if db is not None:
        await _record_push_history(
            db, scope, server, "delete", status, err, duration_ms,
        )
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


async def _record_push_history(
    db: AsyncSession,
    scope: DhcpScope,
    server: DhcpServer,
    operation: str,
    status: str,
    error: str | None,
    duration_ms: int | None,
) -> None:
    """PR 104 — append one row to dhcp_scope_push_history per attempt.

    Append-only, no commit here: the caller batches the history row
    with its outer transaction so a rolled-back push doesn't leak a
    success row. error is truncated to fit the column.
    """
    db.add(DhcpScopePushHistory(
        scope_id=scope.id,
        server_id=server.id,
        operation=operation,
        kea_subnet_id=scope.kea_subnet_id,
        status=status,
        error=error[:2048] if error else None,
        duration_ms=duration_ms,
    ))
    await db.flush()


# ---------- background auto-push (PR 79) ----------


def should_auto_push(server: DhcpServer | None, scope: DhcpScope | None = None) -> bool:
    """Gate the auto-push decision in one place.

    Returns True iff:
      * server exists, is enabled, and (PR 95) either server.auto_push
        is True OR scope.auto_push_override is True;
      * scope (when provided) is enabled and not soft-deleted;
      * scope.auto_push_override is not False (which suppresses
        regardless of the server-level setting).

    Override matrix (PR 95):
        server.auto_push  scope.auto_push_override   → push?
        F                 NULL                       → no (inherit)
        F                 TRUE                       → yes (force)
        F                 FALSE                      → no (explicit off)
        T                 NULL                       → yes (inherit)
        T                 TRUE                       → yes
        T                 FALSE                      → no (suppress)

    Pure: no DB I/O. Called from the API handlers before scheduling
    a BackgroundTask, and from unit tests directly.
    """
    if server is None or not server.enabled:
        return False
    if scope is not None:
        if not scope.enabled:
            return False
        if getattr(scope, "deleted_at", None) is not None:
            return False
        override = getattr(scope, "auto_push_override", None)
        if override is True:
            return True
        if override is False:
            return False
    # No override (or no scope passed) → defer to server flag.
    return bool(server.auto_push)


async def enqueue_bundle_rerender(server_id) -> bool:
    """PR 83 — enqueue the rerender_dhcp_bundle arq task for `server_id`.

    Best-effort: returns False (and logs a warning) on Redis failure
    so the API handler can proceed without failing the user's
    request. The bundle endpoint serves stale cache or falls back to
    live render when the worker hasn't caught up.

    Lives in this module rather than the api/ layer so every handler
    that mutates DHCP state (scope create/update/delete, template
    update, future server-config edits) shares one call site.
    """
    try:
        # Local imports — arq + settings only needed when this is
        # called, and importing them at module load couples the
        # worker import graph more tightly than necessary.
        from arq import create_pool
        from arq.connections import RedisSettings

        from ..settings import get_settings

        pool = await create_pool(
            RedisSettings.from_dsn(str(get_settings().redis_dsn)),
        )
        try:
            await pool.enqueue_job("rerender_dhcp_bundle", str(server_id))
        finally:
            await pool.close()
        return True
    except Exception as e:  # noqa: BLE001 — handler shouldn't fail on enqueue
        log.warning(
            "dhcp_bundle_rerender.enqueue_failed",
            server_id=str(server_id), error=f"{type(e).__name__}: {e}",
        )
        return False


async def schedule_template_fanout_pushes(
    db: AsyncSession, template_id,
) -> list:
    """PR 82 — list scope ids that should be auto-re-pushed after a
    template update.

    A template edit changes the effective config of every scope that
    references it. For each such scope whose parent DhcpServer has
    auto_push=true (and both the server and scope are enabled),
    return its id; the API handler enqueues an
    auto_push_scope_in_background task per id.

    Returns: list of scope UUIDs to dispatch. Empty list = nothing
    to do (no referencing scopes, no auto_push servers, or all
    disabled).

    Single SQL JOIN, no per-scope round-trips. The auto-push gate
    (should_auto_push) is duplicated in pure-Python form here
    because pushing the gate logic into SQL keeps the index plan
    simple — server.enabled / server.auto_push / scope.enabled are
    boolean filters on the joined row.
    """
    stmt = (
        select(DhcpScope.id)
        .join(DhcpServer, DhcpServer.id == DhcpScope.dhcp_server_id)
        .where(DhcpScope.template_id == template_id)
        .where(DhcpScope.enabled.is_(True))
        .where(DhcpScope.deleted_at.is_(None))  # PR 95 — skip soft-deleted
        .where(DhcpServer.enabled.is_(True))
        .where(DhcpServer.auto_push.is_(True))
    )
    rows = (await db.execute(stmt)).scalars().all()
    return list(rows)


async def auto_push_scope_in_background(scope_id) -> None:
    """Run as a FastAPI BackgroundTask: open a fresh session, reload
    the scope, push to Kea, log on failure.

    A fresh session is required — the request's session has already
    been closed by the time BackgroundTasks fires. Errors are caught
    and logged (and persisted to dhcp_servers.last_push_* by
    push_scope itself); we never re-raise from a background task
    because there's no caller to surface to.
    """
    # Local import dodges a circular import: services/dhcp_push.py is
    # imported by api/ipam.py, which is imported during app create —
    # importing from ..db at module-load time would couple the import
    # graph more tightly than needed.
    from ..db import async_session

    async with async_session() as db:
        scope = await db.get(DhcpScope, scope_id)
        if scope is None:
            log.info("dhcp_auto_push.scope_gone", scope_id=str(scope_id))
            return
        try:
            result = await push_scope(db, scope)
        except Exception as e:  # noqa: BLE001 — background task swallows
            log.error(
                "dhcp_auto_push.unexpected",
                scope_id=str(scope_id), error=f"{type(e).__name__}: {e}",
            )
            await db.rollback()
            return
        if result.status != "ok":
            log.warning(
                "dhcp_auto_push.bad_status",
                scope_id=str(scope_id), status=result.status, error=result.error,
            )
        await db.commit()


# ---------- drift detection (PR 75) ----------


@dataclass
class DiffResult:
    scope_id: str
    kea_subnet_id: int | None
    status: str  # "in_sync" | "drifted" | "missing_from_kea" | "never_pushed" | "error"
    dcim_subnet: dict | None
    kea_subnet: dict | None
    delta: dict  # field-name -> {"dcim": ..., "kea": ...}
    error: str | None


# Field-by-field comparison ignores keys Kea adds but DCIM doesn't
# author. Anything DCIM rendered must match; anything Kea added on
# top is informational. List-shaped fields are compared as multisets
# (operator may have re-ordered without changing semantics).
_LIST_FIELDS = {"pools", "pd-pools", "option-data", "reservations"}


def _normalize_for_diff(value):
    """Recursively normalize dicts/lists for stable comparison.

    Lists become tuples of frozen dicts so set-membership works; dicts
    become tuples of sorted items so order doesn't matter. Bare scalars
    pass through. Used inside _diff_subnet_objects only — never round-
    tripped back to JSON.
    """
    if isinstance(value, dict):
        return tuple(sorted(
            (k, _normalize_for_diff(v)) for k, v in value.items()
        ))
    if isinstance(value, list):
        return tuple(sorted(
            (_normalize_for_diff(item) for item in value),
            key=repr,
        ))
    return value


def _diff_subnet_objects(dcim: dict, kea: dict) -> dict:
    """Return the per-key delta between DCIM's rendered subnet and
    Kea's reported subnet.

    Only keys that DCIM authored show up in the delta — Kea-added
    fields (timestamps, internal counters, defaulted options) are
    ignored. A key present in DCIM but missing in Kea is reported as
    `{"dcim": X, "kea": None}`.

    Empty return dict = no drift.
    """
    delta: dict = {}
    for key, dcim_val in dcim.items():
        kea_val = kea.get(key)
        if key in _LIST_FIELDS:
            if _normalize_for_diff(dcim_val) != _normalize_for_diff(kea_val or []):
                delta[key] = {"dcim": dcim_val, "kea": kea_val}
        elif dcim_val != kea_val:
            delta[key] = {"dcim": dcim_val, "kea": kea_val}
    return delta


def _extract_kea_subnet(resp: Any, ip_family: int) -> dict | None:
    """Pluck the single subnet object out of Kea's subnet4-get /
    subnet6-get response. Returns None if the result code says
    not-found (3) or the shape is malformed."""
    if not isinstance(resp, list) or not resp:
        return None
    entry = resp[0]
    if not isinstance(entry, dict):
        return None
    if entry.get("result") == 3:
        return None
    args = entry.get("arguments")
    if not isinstance(args, dict):
        return None
    list_key = "subnet4" if ip_family == 4 else "subnet6"
    subnets = args.get(list_key)
    if not isinstance(subnets, list) or not subnets:
        return None
    first = subnets[0]
    return first if isinstance(first, dict) else None


async def diff_scope(
    scope: DhcpScope,
    server: DhcpServer,
    template: DhcpScopeTemplate | None = None,
) -> DiffResult:
    """Compare what DCIM would render against what Kea currently has.

    Five terminal states:
      * never_pushed    — scope has no kea_subnet_id; nothing to diff.
      * missing_from_kea — DCIM has an id but Kea returns result=3
                          (operator manually deleted, Kea reloaded
                          without the persisted config, etc.).
      * in_sync         — diff is empty; DCIM == Kea on every authored field.
      * drifted         — diff is non-empty; the delta dict says what.
      * error           — transport failure or unexpected Kea response.

    A successful diff_scope call against a `missing_from_kea` scope
    is the cue to call push_scope; against `drifted` the operator
    decides whether to push (overwrite Kea) or accept what's there.

    Caller passes the parent DhcpServer explicitly — keeps this
    function decoupled from the DB session and mirrors the shape of
    delete_scope_from_kea.
    """
    if scope.kea_subnet_id is None:
        return DiffResult(
            scope_id=str(scope.id), kea_subnet_id=None,
            status="never_pushed", dcim_subnet=None, kea_subnet=None,
            delta={}, error=None,
        )

    # PR 78 — apply template merge before rendering so the DCIM side
    # of the diff reflects the effective config Kea would have been
    # pushed, not the raw scope row.
    effective = merge_template_into_scope(scope, template)
    if scope.ip_family == 4:
        dcim_subnet = render_kea_subnet4(effective, scope.kea_subnet_id)
    else:
        dcim_subnet = render_kea_subnet6(effective, scope.kea_subnet_id)

    client = KeaClient(
        server.kea_url,
        username=server.auth_username,
        password=server.auth_password,
    )

    # PR 98 — wrap the subnetN-get RPC in the histogram. The timer
    # records the RPC duration; the application-level diff outcome
    # (drifted / in_sync / missing) is decided below from the
    # response body. timer.status reflects whether the RPC itself
    # succeeded — local diff computation is fast and not counted.
    with _kea_call_timer(str(server.id), "diff") as timer:
        try:
            if scope.ip_family == 4:
                resp = await client.subnet4_get(scope.kea_subnet_id)
            else:
                resp = await client.subnet6_get(scope.kea_subnet_id)
            timer.status = "ok"  # noqa: B010
        except Exception as e:  # noqa: BLE001
            err = f"{type(e).__name__}: {e}"
            timer.status = "error"  # noqa: B010
            log.error("dhcp_diff.transport_error", scope_id=str(scope.id), error=err)
            return DiffResult(
                scope_id=str(scope.id), kea_subnet_id=scope.kea_subnet_id,
                status="error", dcim_subnet=dcim_subnet, kea_subnet=None,
                delta={}, error=err,
            )

    kea_subnet = _extract_kea_subnet(resp, scope.ip_family)
    if kea_subnet is None:
        # Check whether Kea reported the "not found" code specifically
        # (vs a malformed reply we couldn't parse).
        result_code = (
            resp[0].get("result") if isinstance(resp, list) and resp
            and isinstance(resp[0], dict) else None
        )
        if result_code == 3:
            return DiffResult(
                scope_id=str(scope.id), kea_subnet_id=scope.kea_subnet_id,
                status="missing_from_kea", dcim_subnet=dcim_subnet,
                kea_subnet=None, delta={}, error=None,
            )
        return DiffResult(
            scope_id=str(scope.id), kea_subnet_id=scope.kea_subnet_id,
            status="error", dcim_subnet=dcim_subnet, kea_subnet=None,
            delta={}, error=f"unexpected Kea response: {resp!r}"[:1024],
        )

    delta = _diff_subnet_objects(dcim_subnet, kea_subnet)
    status = "in_sync" if not delta else "drifted"
    return DiffResult(
        scope_id=str(scope.id), kea_subnet_id=scope.kea_subnet_id,
        status=status, dcim_subnet=dcim_subnet, kea_subnet=kea_subnet,
        delta=delta, error=None,
    )


def persist_diff_state(scope: DhcpScope, result: DiffResult) -> None:
    """Mirror a DiffResult into the scope row's last_diff_* columns.

    Called from the API handlers after diff_scope returns (per-scope
    endpoint, diff-all, push-drifted preflight). The session commit
    is the caller's responsibility — this just stamps the columns.

    `last_diff_delta_json` only carries a value on status='drifted'
    (the in-Kea-but-out-of-sync case). in_sync / never_pushed /
    missing_from_kea / error all clear the column — the delta isn't
    meaningful and storing a stale one would mislead operators
    reading the LIST endpoint.
    """
    scope.last_diff_at = datetime.now(UTC)
    scope.last_diff_status = result.status
    scope.last_diff_delta_json = result.delta if result.status == "drifted" else None


# ---------- bulk operations (PR 77) ----------


@dataclass
class BulkPushReport:
    server_id: str
    total: int
    counts: dict[str, int]
    results: list[PushResult]


@dataclass
class BulkDiffReport:
    server_id: str
    total: int
    counts: dict[str, int]
    results: list[DiffResult]
    # PR 86 — status transitions observed during this diff pass.
    # Each entry: {scope_id, prefix, from_status, to_status}. Empty
    # when no scope's status changed since its previous diff (cold
    # start: every scope's prior is None, so every result is a
    # transition None→<status>).
    transitions: list[dict] = field(default_factory=list)


# Status taxonomies are pinned here so the API handler doesn't have
# to enumerate them. Adding a new status (e.g. "skipped") means
# extending these lists + handling in _tally.
_PUSH_STATUSES = ("ok", "error", "unsupported")
_DIFF_STATUSES = ("in_sync", "drifted", "missing_from_kea", "never_pushed", "error")


def _tally(statuses: list[str], known: tuple[str, ...]) -> dict[str, int]:
    """Aggregate observed statuses into a fixed-key count map. Unknown
    statuses (shouldn't happen but guards against future drift) land
    in an `other` bucket so the operator notices them."""
    counts: dict[str, int] = dict.fromkeys(known, 0)
    other = 0
    for s in statuses:
        if s in counts:
            counts[s] += 1
        else:
            other += 1
    if other:
        counts["other"] = other
    return counts


async def push_drifted_scopes(
    db: AsyncSession, server: DhcpServer,
) -> BulkPushReport:
    """Push only scopes whose persisted drift status is 'drifted'.

    Reads the cached state from PR 80 — operator should diff-all
    (or wait for the cron) first so the cache is fresh. A scope
    that was drifted at last check but has since been fixed will
    still push successfully (push is idempotent), just redundantly.

    Returns the same BulkPushReport shape as push_all_scopes; an
    empty drifted set comes back with total=0 and all counts at zero.
    """
    scopes = (
        await db.execute(
            select(DhcpScope)
            .where(DhcpScope.dhcp_server_id == server.id)
            .where(DhcpScope.enabled.is_(True))
            .where(DhcpScope.deleted_at.is_(None))  # PR 95
            .where(DhcpScope.last_diff_status == "drifted")
        )
    ).scalars().all()
    results: list[PushResult] = []
    for scope in scopes:
        results.append(await push_scope(db, scope))
    return BulkPushReport(
        server_id=str(server.id),
        total=len(results),
        counts=_tally([r.status for r in results], _PUSH_STATUSES),
        results=results,
    )


async def push_all_scopes(db: AsyncSession, server: DhcpServer) -> BulkPushReport:
    """Push every enabled scope on `server` serially.

    Serial (not parallel): _allocate_kea_subnet_id reads from the DB
    to pick the next free integer, so two concurrent first-pushes
    could both pick id=1 and conflict on Kea's side. The serial
    loop sidesteps that without needing a lock. For ~tens of scopes
    this is fine; thousands of scopes is a future-PR concern.

    Errors on individual scopes don't abort the batch. Each scope's
    result lands in the returned list; the caller decides what to
    surface to the operator.
    """
    scopes = (
        await db.execute(
            select(DhcpScope)
            .where(DhcpScope.dhcp_server_id == server.id)
            .where(DhcpScope.enabled.is_(True))
            .where(DhcpScope.deleted_at.is_(None))  # PR 95
        )
    ).scalars().all()
    results: list[PushResult] = []
    for scope in scopes:
        results.append(await push_scope(db, scope))
    return BulkPushReport(
        server_id=str(server.id),
        total=len(results),
        counts=_tally([r.status for r in results], _PUSH_STATUSES),
        results=results,
    )


async def diff_all_scopes(db: AsyncSession, server: DhcpServer) -> BulkDiffReport:
    """Drift-check every scope on `server` — including disabled ones,
    since drift on a disabled scope is still informational (operator
    may have flipped enabled=False locally while Kea still serves it).

    Serial like push_all_scopes. Each result carries the full
    DiffResult including delta + dcim_subnet + kea_subnet, so a
    fleet with N scopes returns N*(rendered+kea) bytes — operators
    with many scopes should call the per-scope endpoint instead.
    """
    scopes = (
        await db.execute(
            select(DhcpScope)
            .where(DhcpScope.dhcp_server_id == server.id)
            .where(DhcpScope.deleted_at.is_(None))  # PR 95
        )
    ).scalars().all()
    # PR 78 — preload referenced templates in one round-trip so the
    # per-scope diff loop doesn't issue N+1 selects.
    template_ids = {s.template_id for s in scopes if s.template_id}
    templates_by_id: dict = {}
    if template_ids:
        rows = (
            await db.execute(
                select(DhcpScopeTemplate)
                .where(DhcpScopeTemplate.id.in_(template_ids))
            )
        ).scalars().all()
        templates_by_id = {t.id: t for t in rows}
    results: list[DiffResult] = []
    transitions: list[dict] = []
    for scope in scopes:
        # PR 86 — snapshot the prior status BEFORE persist_diff_state
        # overwrites it. None on cold start; comparing None != "in_sync"
        # surfaces the initial diff as a transition too, which is what
        # the alert dispatcher wants (operator hears about drift it
        # didn't know about, even on first cron run).
        prior_status = scope.last_diff_status
        template = templates_by_id.get(scope.template_id) if scope.template_id else None
        result = await diff_scope(scope, server, template)
        # PR 80 — persist the drift state on each row so LIST and
        # push-drifted see fresh data without a second round-trip.
        persist_diff_state(scope, result)
        if prior_status != result.status:
            transitions.append({
                "scope_id": str(scope.id),
                "prefix": str(scope.prefix),
                "from_status": prior_status,
                "to_status": result.status,
            })
        results.append(result)
    return BulkDiffReport(
        server_id=str(server.id),
        total=len(results),
        counts=_tally([r.status for r in results], _DIFF_STATUSES),
        results=results,
        transitions=transitions,
    )
