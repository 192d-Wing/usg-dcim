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

## Phases

### Phase 0 — Scaffold (DONE)

- [x] `packages/otter-go/` with chi + pgx + structured logging
- [x] sqlc config + canonical generated layout
- [x] Vertical slice: `/healthz`, `/readyz`, `/api/v1/sites` GET list + GET by id
- [x] Containerfile, go.work entry, CI matrix shard, Taskfile

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

### Phase 4 — region-deploy + workers (target: 4–6 weeks)

- [ ] region-deploy orchestrator → Go (the k8s client is canonically
      Go, this gets cleaner not messier)
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
