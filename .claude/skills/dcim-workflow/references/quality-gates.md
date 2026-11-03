# Quality Gates

Commands mirror `.github/workflows/ci.yml`. Scope to touched packages while
iterating; run every touched package's full form once before the final gate.

| Gate | Command (from package dir) | Required |
|------|----------------------------|----------|
| Go build | `go build ./...` | Any Go package touched |
| Go vet | `go vet ./...` | Any Go package touched |
| Go format | `gofmt -l .` (must print nothing) | Any Go package touched |
| Go tests | `go test -race -count=1 ./...` | Any Go package touched (incl. `packages/wolf/coredns-nsec3sign`) |
| sqlc drift | `packages/otter-go`: migrate up against a scratch TimescaleDB, dump schema, `cd db && go tool sqlc generate`, `git diff --exit-code db/generated` (see ci.yml `sqlc-drift`) | `packages/otter-go/db/**` or migrations touched |
| Migration reversibility | `go run ./cmd/otter-go-migrate -cmd up`, then `down 1`, then `up` | New migration |
| finch install | `npm ci --ignore-scripts --no-audit --no-fund` | finch touched |
| finch typecheck | `./node_modules/.bin/tsc --noEmit -p tsconfig.json` | finch touched |
| finch lint | `npm run lint` | finch touched |
| finch unit | `npm test` (vitest) | finch touched |
| finch build | `npm run build` | finch touched |
| E2E | `make up`, migrate/seed per ci.yml `e2e`, then `cd packages/finch && ./node_modules/.bin/playwright test`; `make down` after | finch or otter-go API touched |
| Load | `packages/loadgen` per `docs/dev/perf-loadgen.md` | heron, magpie, badger, or telemetry queries touched |
| Helm | `helm lint deploy/helm/dcim` and `helm template deploy/helm/dcim >/dev/null` | `deploy/helm/**` touched |
| Flux/kustomize | `kustomize build` on touched `deploy/flux`/`deploy/k8s` paths | Those paths touched |
| Supply chain | `govulncheck ./...` (Go); `npm audit --omit=dev` (finch) | Manifests/lockfiles touched |
| Annotations | `grep -rn "NIST 800-53:" packages \| wc -l` (count should not drop) | Code changes |
| Markdown | `npx markdownlint-cli "docs/**/*.md"` | Docs changed |

## Scale budget

DCIM targets 184+ sites, each with a site collector, feeding one central ingest.

- heron ingest: no per-sample DB round trip; batched `COPY`/multi-row inserts.
- `telemetry_samples` queries must hit time + entity indexes and respect chunk pruning; no unbounded scans of the hypertable.
- List endpoints are paginated and ABAC-filtered in SQL, not in Go after fetch.
- badger: bounded concurrency per site; store-and-forward buffer is bounded on disk.
- magpie: evaluation cost is proportional to changed series, not total series.
- A loadgen throughput or p95 regression > 10% is a MAJOR finding unless justified.

## Rules

- Record every command with exit status and the last relevant output lines in
  `qa-report.md`. "Tests pass" without the command is not evidence.
- Missing environment (no podman, no Postgres, no browsers) → gate is
  **NOT RUN**, never PASS.
- No new `t.Skip`, `test.skip`, `//nolint`, `// @ts-ignore`, or `eslint-disable`
  without a justification comment and a finding entry.
