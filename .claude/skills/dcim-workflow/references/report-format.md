# Report Formats

## Findings file (`findings-<role>.md`)

```markdown
# Findings — <role>

Scope: <base>...<head> (<n> files), plan: <run>/plan.md
Verdict: APPROVE | APPROVE WITH COMMENTS | CHANGES REQUESTED

## <ROLE-PREFIX>-001 [BLOCKER] <one-line title>

- **Location**: `packages/otter-go/internal/x/file.go:123`
- **Reference**: NIST 800-53 AC-3 | CWE-639 | RFC 3416 §4.2 | OpenAPI path
- **Problem**: what is wrong, in one or two sentences.
- **Failure scenario**: concrete request/data/state → concrete bad outcome.
- **Fix**: the smallest change that resolves it.
- **Confidence**: high | medium | low
```

Role prefixes: `SEC` (security), `NIST` (800-53), `DB` (database/migration),
`API` (contract), `PERF` (performance/scale), `PROTO` (device/DNS protocols),
`QA` (testing), `REL` (release).

### Severity

| Severity | Meaning | Gate effect |
|----------|---------|-------------|
| BLOCKER | Authz bypass or ABAC scope leak across site/region/enclave, injection, secret exposure, data loss or irreversible migration on populated tables, broken collector ↔ ingest compatibility, telemetry silently dropped, audit bypass | Must fix |
| MAJOR | Missing required control/test, unbounded query/queue, table-locking migration on a large table, breaking API change without versioning, fail-open path, >10% load regression, sqlc drift, protocol MUST violated | Must fix |
| MINOR | Hardening, missing annotation, weak test, unclear error shape, small perf win | Advisory |
| NIT | Style, naming, comments | Advisory |

A finding without a location and a failure scenario is MINOR at most.

## Fix response (`fixes-round-<n>.md`)

```markdown
| Finding | Action | Change |
|---------|--------|--------|
| SEC-001 | Fixed | `assets.go:120-131` apply site scope in the sqlc query |
| DB-002 | Rebutted | table has <1k rows in every deployment; lock is brief |
```

## Gate summary (`gate.md`)

```markdown
# Gate — <task>

Verdict: PASS | PASS WITH POA&M | FAIL

| Gate | Result | Evidence |
|------|--------|----------|
| go build/vet/test -race | PASS | `go test -race -count=1 ./...` (otter-go) |
| sqlc drift | PASS | ci.yml sqlc-drift steps |
| finch tsc/lint/vitest/build | PASS | ... |
| E2E | PASS (54) | `playwright test` vs `make up` |
| load | PASS (heron p95 +3%) | loadgen |
| security review | APPROVE | findings-security.md |
| NIST 800-53 review | APPROVE WITH COMMENTS | findings-nist.md (2 MINOR) |
| DB review | APPROVE | findings-db.md |
| API contract review | APPROVE | findings-api.md |
| performance review | APPROVE | findings-perf.md |
| protocol review | SKIPPED (no collector/DNS change) | brief.md |
| compliance docs | UPDATED | docs-update.md |
| release | Helm + image checks | release-notes.md |

## Open items
## POA&M
## Dropped findings
```
