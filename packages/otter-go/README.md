# otter-go

Phase-2 Go port of [packages/otter](../otter). Built around **chi** for
routing, **pgx** for Postgres, and **sqlc** for typed query codegen.

> [!WARNING]
> **Auth is a stub.** Every authenticated request is granted `*`
> capabilities. The process **refuses to start** unless
> `OTTER_GO_INSECURE_AUTH_STUB=true` is set, and every request logs an
> `auth_stub_in_use` warning. Do not unset the env-gate until the real
> OIDC + capability middleware lands (Phase 3 in
> [the migration doc](../../docs/dev/otter-go-migration.md)).

**Status: vertical-slice scaffold.** Routes implemented:
- `GET  /healthz`
- `GET  /readyz`
- `GET  /api/v1/sites`         — list with pagination + filters
- `GET  /api/v1/sites/{id}`    — fetch by id

Everything else is still served by the Python otter. See
[docs/dev/otter-go-migration.md](../../docs/dev/otter-go-migration.md)
for the phased plan to port the remaining 20 routers.

## Layout

```
packages/otter-go/
├── cmd/otter-go/main.go      # entrypoint: chi router, pgx pool, signals
├── internal/
│   ├── auth/                 # bearer-token middleware (capability stub)
│   ├── httpx/                # JSON helpers, error mapping
│   └── sites/                # one resource — handler + SQL-backed store
├── db/
│   ├── sqlc.yaml             # generator config
│   ├── queries/              # raw SQL queries (input to sqlc)
│   └── generated/            # sqlc output (DO NOT EDIT; regen with `sqlc generate`)
├── go.mod
└── Containerfile
```

## Schema ownership during migration

**Alembic in [packages/otter](../otter) is the source of truth for the
schema.** otter-go reads the same DB. We do not run goose alongside
Alembic — two migration systems against one database is how data-loss
incidents happen. goose adoption is gated on every router moving to
otter-go (see migration doc).

## Regenerating queries

`db/generated/` was hand-written to match what `sqlc generate` would
emit. Before adding more queries, run sqlc for real **once** so the
canonical output replaces the hand-written version:

```sh
# 1. Dump the live Alembic-managed schema sqlc needs for type inference.
#    Run against any DB that's caught up to `alembic upgrade head`.
pg_dump -s -O -x -d "$DCIM_POSTGRES_DSN_RAW" > packages/otter-go/db/schema.sql

# 2. Regenerate.
cd packages/otter-go/db && sqlc generate

# 3. Commit schema.sql + the regenerated db/generated/.
```

CI has a `sqlc-drift` job that's a no-op until `db/schema.sql` lands;
once committed, the job runs `sqlc generate` and fails the build on
any diff in `db/generated/`. So step 3 is the bit that arms the gate.
