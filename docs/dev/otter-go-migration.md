# otter → otter-go migration plan

Phase-2 port of the Python `otter` (FastAPI + SQLAlchemy + Alembic) to
Go (`chi` + `pgx` + `sqlc`). Estimated effort: **4–8 months of senior
engineering time** assuming one full-time porter plus reviews; not a
single-session task.

## Why we're doing this incrementally

The hot paths (`heron`/`magpie`/`beagle`) were already extracted in
Phase 1. What remains in `otter` is CRUD + auth + orchestration. The
performance argument for porting is weak; the consistency argument
(one toolchain, fewer images, fewer runtimes) is the actual driver.

That means **speed isn't worth correctness risk**. The plan below
keeps `otter` (Python) authoritative until each router is replaced
end-to-end and verified, with traffic shifted incrementally at the
ingress.

## Architecture during the migration

```
            Ingress (nginx/traefik)
                    │
        path-based routing
        ┌───────────┴───────────┐
        │                       │
        │ /api/v1/sites/*       │ everything else
        ▼                       ▼
   otter-go (Go)            otter (Python)
        │                       │
        └─────────┬─────────────┘
                  ▼
            PostgreSQL (Alembic-managed)
            Redis
```

- **Schema authority**: Alembic in `packages/otter` stays the source
  of truth. otter-go reads the schema; it does NOT run migrations.
- **goose adoption**: deferred until every router is on otter-go and
  the Python alembic chain is frozen. Running two migrators against
  one database is a data-loss risk we won't take during transition.
- **OIDC/auth**: re-implementing JWT verify + capability matching +
  scope ABAC is its own work-stream. The vertical-slice
  `internal/auth/Require` middleware is a **stub** — promote nothing
  to production until the real implementation lands.

## Profile first — gate Phase 1 on real data

Before writing more handler code, prove that porting actually helps.
[`packages/otter/src/dcim/metrics.py`](../../packages/otter/src/dcim/metrics.py)
already exposes per-route latency at `/metrics` as
`dcim_http_request_duration_seconds{method,route}`. Let it collect a
**representative two weeks** of production-shaped traffic, then run:

```promql
# Top 10 endpoints by p95 latency (the ones most worth porting)
topk(10,
  histogram_quantile(0.95,
    sum by (route, le) (rate(dcim_http_request_duration_seconds_bucket[1h]))
  )
)

# Top 10 endpoints by total CPU time (latency × volume)
topk(10,
  sum by (route) (
    rate(dcim_http_request_duration_seconds_sum[1h])
  )
)

# Routes that account for >5% of total request volume
sum by (route) (rate(dcim_http_requests_total[1h]))
  / ignoring(route) group_left()
  sum (rate(dcim_http_requests_total[1h])) > 0.05
```

If the topk lists contain mostly endpoints from the same 2–3 routers,
port those routers first regardless of the alphabetical Phase-1 order
below. If the top list is dominated by `/healthz`, `/readyz`, or
already-Go services (heron/magpie/beagle), **stop the migration** and
declare otter Python-permanent — see "Decisions worth revisiting" at
the bottom of this doc.

## Phases

### Phase 0 — Scaffold (DONE)

- [x] `packages/otter-go/` with chi + pgx + structured logging
- [x] sqlc config + canonical generated layout
- [x] Vertical slice: `/healthz`, `/readyz`, `/api/v1/sites` GET list + GET by id
- [x] Containerfile, go.work entry, CI matrix shard, Taskfile

## Current cutover status (snapshot — 2026-05-30)

Most route-level work from Phases 1–3 is done. The remaining work is
either small route-level gaps (single-PR each) or infrastructure-scale
(DHCP stack, scheduler, Alembic→Atlas). Anything not listed here
should be assumed shipped — see `git log packages/otter-go` for the
authoritative record.

### Shipped — module fully on otter-go

| Module | PRs | Notes |
| --- | --- | --- |
| Auth (`/api/v1/auth/*`) | #179 | JWT verify, OIDC, MFA AMR check |
| Audit (`/api/v1/audit/*`) | #180 | read endpoint + SQL-level scope filter |
| Telemetry (`/api/v1/telemetry/series`) | #178 | freshness + range queries |
| Admin (`/api/v1/admin/*`) | #182 + #184 | 17 routes; 256-cap catalog byte-for-byte parity |
| Search (`/api/v1/search`) | #187 | 4 result buckets + IP-parse bulk enrichment |
| Dashboards (`/api/v1/dashboards/*`) | #188 → #194 | 9 endpoints, 3 service helpers (capacity/powerchain/forecast) |
| Inventory (`/api/v1/inventory/*`) | #195, #197, #198, #199 | sites/regions/buildings/rooms/rows/racks/assets/cables; PATCH+DELETE for locations; per-region ABAC |
| LIR (`/api/v1/lir`) | #175 | Go-canonical from day one |
| IPAM `/move` | #175 | nginx regex ingress |

### Cutover queue — small route-level gaps (1–3 PRs each)

- [ ] **Finish alerts** (6 of 12 routes ported). Remaining: arq-driven
      evaluation + delivery loops. Will need the Go scheduler before
      the cron paths can move. ~3–5 PRs.
- [ ] **BGP TCP-AO keychain CRUD**. 37 Python routes total, 8 on Go;
      this is the highest-value gap. ~1–2 PRs.
- [ ] **Notifications channel test action**. Single endpoint that
      triggers a test notification on the configured channel. ~1 PR.

### Cutover queue — infrastructure-scale (multi-PR)

- [x] **Go scheduler — proof of concept (PR #208)**. `robfig/cron/v3`
      picked. `packages/otter-go/internal/scheduler` is the harness
      (Job interface, register/run loop, structured slog wrapper,
      cron.Recover panic guard). First job:
      `internal/scheduler/jobs/dnspurge` (port of Python's
      `dns_purge_metrics`). New binary `otter-go-scheduler` with
      /healthz + /readyz; same shape as otter-go-worker. Helm sub-
      chart deferred to a follow-up. Remaining arq cron entries port
      one at a time: alerts evaluation, collectors sweep, freshness
      sweep, DHCP sync/age-out/drift/tombstone, IPAM utilization,
      DNS sync-from-ipam/rotate-zsks/health-checks, notify-bridge.
- [ ] **DHCP stack**. ~5000 lines across `services/dhcp_*.py` plus the
      `api/ipam.py` `/dhcp/*` endpoints. Push/drift/reconcile/
      bundle-cache/push-history; Kea Control Agent integration via
      JSON-RPC; tombstone purge + bundle re-render are cron jobs that
      need the scheduler first. Operator-accepted risk: unit tests pin
      shapes but don't validate against real Kea — paired-running
      staging validation required before cutover. ~25–30 PRs.

### Infrastructure-level moves

- [ ] **Alembic → Atlas or Goose**. 66 revisions to migrate or
      co-own. Plan: freeze Alembic during the cutover (already in
      the risk register), build Atlas baseline against a clone of
      prod, verify `atlas migrate status` matches `alembic current`,
      then ratchet over.
- [ ] **DHCP bundle rendering**. Compile-time codegen is likely
      faster than template rendering at the scales we're seeing.
      Investigate after the DHCP API port is stable.
- [ ] **Delete `packages/otter/` entirely** once every queue item
      above is green in Go and the dual-deploy soak window expires.

### Established patterns to reuse in remaining work

- New paginated reads should use `httpx.Page[T]` + `httpx.EmptyPage[T]`
  with `httpx.PageBounds`; per-handler page-struct aliases are typedef
  ed to `httpx.Page[T]`.
- Capability gate on every route via `auth.RequireCapability(capCode)`.
  Don't rely on `ScopedSiteFilter` to gate access on its own — without
  the capability check, `FindScope→nil` makes the filter signal
  "global view" and leaks data.
- Per-row ABAC on get-by-id via `auth.EnforceSiteScope` (or
  `EnforceRegionScope` for region-rooted resources). PATCH/DELETE
  scope-check the existing row's site, and if the patch moves the row
  to a new site, scope-check that one too.
- `httpx.Mapped` translates `pgx.ErrNoRows → 404`,
  `auth.ErrOutsideScope → 403`, FK 23503 → 409. Handlers should
  delegate via `writeMapped(w, err)` rather than emit status codes by
  hand.
- Sequential SQL writes are the codebase posture (no pgx tx wrapper
  yet). Partial-failure semantics documented in each handler comment;
  recovery is idempotent re-run.
- Audit on every mutation via `audit.Record(ctx, h.Audit, nil,
  audit.Event{Action, TargetType, TargetID, SiteID, Diff})`.
- Hand-edited generated code lives in `db/generated/*.sql.go` and
  must match `db/queries/*.sql` — CI's `sqlc drift check` job
  enforces this.

### Operational risk notes (user-accepted 2026-05-25)

Unit tests pin shapes + call contracts but **do not** validate behavior
against real Kea / collector / IdP. A "green" DHCP port could still
break in prod when external systems return unexpected error codes.
**Staging soak + paired-running required before any cron-job or
orchestration cutover.**

---

### Phase 1 — Read-side ports (target: 6–8 weeks)

Move all GET endpoints. No state mutations, no audit log writes — so
the blast radius of a bug is "stale or wrong data shown to user," not
"corrupted database." Safe to dual-deploy.

Routers ordered by complexity (port in this order):

1. `inventory` GETs — sites, regions, racks, devices, cables
2. `ipam` GETs — VRFs, prefixes, IPs, leases
3. `power` GETs — power chains, feeds, PDUs
4. `bgp` GETs — peers, ASNs, prefix-lists, route-maps
5. `dns` GETs — zones, records, views, health checks
6. `dashboards` — all (read-only aggregations)
7. `audit` — read endpoint (paginated event stream)
8. `search` — global search
9. `notifications` GETs
10. `alerts` GETs — rules, instances, history

Per-router work item:

- [ ] Add SQL to `db/queries/<router>.sql`
- [ ] `sqlc generate`
- [ ] Handler in `internal/<router>/`
- [ ] Wire into `cmd/otter-go/main.go`
- [ ] Move the ingress path rule to send GETs to otter-go
- [ ] Compare a week of access logs between otter and otter-go for
      `*_count` and `*_total` mismatches before retiring the Python
      route

### Phase 2 — Write-side ports + audit log (target: 6–10 weeks)

POST/PATCH/DELETE for every router above. Audit log is the
infrastructure work that unlocks this — every write must produce an
`audit_events` row in the same transaction. Port `dcim.security.audit`
to a Go equivalent first.

### Phase 3 — Auth + capabilities + scope ABAC (target: 4–6 weeks)

Replace the `auth.Require` stub:

- [ ] OIDC discovery + JWT verification (`go-oidc` + `github.com/coreos/go-oidc/v3`)
- [ ] API token (`dcim_*`) lookup against `api_tokens` table — port
      from `packages/heron`'s `authorize()` (same code) into
      `packages/shared-go/auth/`
- [ ] Capability matching (`capabilityMatches` is already in heron;
      promote it to `shared-go/auth` once we have ≥2 consumers)
- [ ] Scope expansion + SQL filter helpers (port from
      `dcim.security.scope`)
- [ ] Audit log middleware writing on every state-changing request

### Phase 4 — workers (target: 4–6 weeks)

- [ ] Replace `arq` worker with `river` or `asynq`; reroute
      `dcim:notify:bridge` consumer
- [ ] Port notification dispatchers (email/webhook)

### Phase 5 — Cutover (target: 2–3 weeks)

- [ ] Ingress sends 100% of `/api/v1/*` to otter-go for 2 weeks; otter
      Python remains warm for fast rollback
- [ ] Freeze Alembic; rewrite migration history into goose; verify
      `goose status` matches `alembic current` on a clone of prod
- [ ] Decommission otter Python image
- [ ] Rename otter-go → otter (Phase 7-style runtime rename) once the
      Python service is gone

## Risk register

| Risk | Mitigation |
|---|---|
| Two services emit divergent JSON for the same resource | Shared OpenAPI fixture in `docs/openapi/`; CI shape-diff tests against both backends |
| Pagination total drifts between Python ABAC filter and Go ABAC filter | Capacity-test the scope-filter SQL on a prod-shaped clone; lock with golden tests |
| Audit log gaps during dual-deploy | Treat audit emission as load-bearing for SOC compliance; **block** any write port until the Go audit module is integration-tested end-to-end |
| sqlc generated drift | CI job `sqlc generate && git diff --exit-code` once sqlc is on the runners |
| Schema churn during transition | Freeze non-essential Alembic migrations during Phase 2-3; require an "otter-go impact" note on any new revision |

## Decisions worth revisiting if the migration stalls

- **Could we just leave otter in Python forever?** Yes. The Phase-1
  Go extractions already captured the perf wins. If this migration
  burns more than its 8-month budget without payoff, the right call
  is to declare otter Python-permanent and reinvest the time
  elsewhere.

- **Could we go to a different framework (Gin, Fiber, Echo)?** Not
  without a concrete reason to switch from chi — chi is std-lib
  compatible, OTEL-friendly, and matches the rest of the Go services.
  Fiber's fasthttp base would break our OTEL instrumentation.
