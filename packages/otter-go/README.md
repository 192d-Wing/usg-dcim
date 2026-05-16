# otter-go

Phase-2 Go port of [packages/otter](../otter). Built around **chi** for
routing, **pgx** for Postgres, and **sqlc** for typed query codegen.

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

```sh
# from packages/otter-go/
sqlc generate
```

The committed `db/generated/` output must match what `sqlc generate`
produces. CI should verify this with a `git diff --exit-code` check
once sqlc is on the runners; left as a TODO for now.
