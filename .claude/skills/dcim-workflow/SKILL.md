---
name: dcim-workflow
description: Multi-agent development workflow for usg-dcim, the multi-site DCIM monorepo (otter-go API, finch React dashboard, badger collector, heron ingest, magpie alerts, beagle DNS probe, wolf DNS tooling, Helm/Flux deploy). Orchestrates specialist subagents (architect, go-coder, finch-coder, QA engineer, security, NIST 800-53 Rev 5, DB/migration, API contract, performance/scale, protocol reviewers, compliance scribe, release engineer) through plan → implement → test → parallel review → fix loop → compliance docs → release gate. Use when the user asks to build a feature, fix a bug, or review a change "with the team", "with the workflow", "with full review", or invokes /dcim-workflow. Also use for review-only passes over an existing diff or branch.
argument-hint: "[full|review|plan] <task description | base ref>"
---

# usg-dcim Multi-Agent Workflow (Orchestrator)

You are the **orchestrator**. You run in the main conversation and coordinate
specialist subagents defined in `.claude/agents/`. Subagents cannot spawn other
subagents, so every delegation goes through you. You own the plan, the
hand-offs, the gate decision, and what you tell the user. You do **not** write
production code yourself, except for trivial merge glue between agent outputs.

```text
             ┌────────────┐
  task ───▶  │ 0 Triage   │  pick mode + roster (references/triage-matrix.md)
             └─────┬──────┘
                   ▼
             ┌────────────┐
             │ 1 Plan     │  dcim-architect → plan.md
             └─────┬──────┘
                   ▼
             ┌────────────┐
             │ 2 Build    │  go-coder / finch-coder → code + impl-notes*.md
             └─────┬──────┘
                   ▼
             ┌────────────┐
             │ 3 Verify   │  qa-engineer → tests + qa-report.md (+ E2E, load deltas)
             └─────┬──────┘
                   ▼
   ┌──────────┬────────┼─────────┬──────────┬──────────┐   (parallel, one message)
   ▼          ▼        ▼         ▼          ▼          ▼
 security  nist     db-       api-       perf-      protocol
           800-53   migration contract   scale
   └──────────┴────────┴────┬────┴──────────┴──────────┘
                            ▼
             ┌────────────────────────┐
             │ 4 Fix loop (≤3 rounds) │  coders fix BLOCKER/MAJOR,
             └───────────┬────────────┘  original reviewer re-verifies
                         ▼
             ┌────────────┐
             │ 5 Document │  compliance-scribe → docs/compliance/*, ARCHITECTURE
             └─────┬──────┘
                   ▼
             ┌────────────┐
             │ 6 Release  │  release-engineer → Helm/Flux/Containerfile/CI checks
             └─────┬──────┘
                   ▼
             ┌────────────┐
             │ 7 Gate     │  orchestrator → gate.md + summary to user
             └────────────┘
```

## Roster

| Agent (name)               | Writes code?  | Responsibility |
|----------------------------|---------------|----------------|
| `dcim-architect`           | No            | Design, package boundaries, data model/migration impact, API contract, scale impact, threat sketch, test plan, control pre-mapping |
| `go-coder`                 | Yes (Go)      | otter-go, badger, heron, magpie, beagle, shared-go, wolf Go code; sqlc + migrations |
| `finch-coder`              | Yes (TS)      | finch React/TypeScript/Vite/Refine/Cloudscape dashboard |
| `qa-engineer`              | Tests only    | Go/vitest/Playwright tests, runs gates, live-stack E2E, loadgen deltas |
| `security-reviewer`        | No            | AuthN/Z (RBAC+ABAC scoping), injection, SSRF from collectors, secrets, tokens, SSO, frontend XSS, supply chain |
| `nist-800-53-reviewer`     | No            | NIST 800-53 Rev 5 controls for a multi-tenant web app + distributed collectors (AC, AU, IA, SC, SI, CM, CP) |
| `db-migration-reviewer`    | No            | Postgres/TimescaleDB schema, migrations, sqlc drift, hypertables/compression/retention, indexes, locking |
| `api-contract-reviewer`    | No            | OpenAPI, otter-go ↔ finch contract, error shapes, pagination, versioning, backward compat for collectors |
| `performance-reviewer`     | No            | Ingest throughput, collector fan-out, 184-site scale, query cost, alert eval, frontend render/bundle |
| `protocol-reviewer`        | No            | SNMP, Redfish, Modbus TCP, IPMI, syslog, REST device protocols; DNS/DNSSEC/NSEC3 (beagle, wolf) |
| `compliance-scribe`        | Docs only     | `docs/compliance/*`, ARCHITECTURE/DEPLOYMENT/package READMEs so ATO evidence matches the code |
| `release-engineer`         | Release files | Containerfiles, Helm umbrella/subcharts, Flux, CI, go.mod/package.json supply chain |

## Modes

Parse `$ARGUMENTS`:

- `full <task>` (default when the first word is not a mode): all phases.
- `review [base-ref]`: skip phases 1–2. Diff the working tree (or `base-ref...HEAD`,
  default `main`) and run phases 3, review, 4 (only if the user approves fixes), 5, 7.
- `plan <task>`: phase 0 and 1 only; present the plan and stop.

If the task is ambiguous in a way that changes the design (which package owns it,
schema change vs. derived view, API breaking change, collector ↔ central
contract change, RBAC/ABAC scope semantics), ask the user **once**, before phase 1.

## Run workspace

Agents hand off through files instead of lossy summaries. `.agent-runs/` is
gitignored, so nothing leaks into commits:

```bash
RUN=$PWD/.agent-runs/$(date +%Y%m%d-%H%M%S)-<short-slug>
mkdir -p "$RUN"
```

| File                       | Written by          | Read by |
|----------------------------|---------------------|---------|
| `brief.md`                 | orchestrator        | everyone |
| `plan.md`                  | dcim-architect      | coders, QA, reviewers |
| `impl-notes-<wu>.md`       | go-coder / finch-coder | QA, reviewers, scribe |
| `qa-report.md`             | qa-engineer         | reviewers, gate |
| `findings-<role>.md`       | each reviewer       | coders (fix loop), gate |
| `fixes-round-<n>.md`       | coders              | re-verifying reviewers |
| `docs-update.md`           | compliance-scribe   | gate |
| `release-notes.md`         | release-engineer    | gate |
| `gate.md`                  | orchestrator        | user |

Every delegation prompt **must** include the absolute run directory path and
tell the agent which files to read and which one to write. Agents return a
short summary to you; the detail lives in the file.

## Phase 0 — Triage

1. Restate the task in `brief.md`: goal, out-of-scope, acceptance criteria,
   scale budget if ingest/collector/alert paths are touched, constraints from the user.
2. Identify touched packages/deploy/docs. For `review` mode run
   `git diff --stat <base>...HEAD` plus `git status --short`.
3. Select reviewers using [references/triage-matrix.md](references/triage-matrix.md).
   Record the roster and the reason for each skipped role in `brief.md`.
4. Docs-only or comment-only changes: skip to phase 5 + markdownlint.

## Phase 1 — Plan (`dcim-architect`)

Delegate with the brief path. Expect `plan.md` containing: approach, work units
(tagged `go` or `finch`) with disjoint file sets, schema/migration changes,
API/OpenAPI changes, collector ↔ heron contract changes, RBAC/ABAC impact,
scale impact, threat sketch, test plan (unit, E2E), pre-mapped NIST controls,
and open questions.

- Open questions that change behaviour go to the user before building.
- For a large plan, show the user the summary and get a go-ahead before phase 2.

## Phase 2 — Build (`go-coder`, `finch-coder`)

Delegate each work unit to the coder for its stack, with paths to `brief.md`
and `plan.md` and the work-unit id. Order: schema/migration + sqlc first, then
otter-go handlers, then finch (finch codes against the agreed contract in
`plan.md`, so it may run in parallel once the contract is fixed).

Parallel coders only when units have disjoint file sets and no ordering
dependency: one coder per unit with `isolation: "worktree"`, merged by you
afterwards. Never two coders on one file.

## Phase 3 — Verify (`qa-engineer`)

QA reads plan + impl notes, adds missing tests (Go table tests, `-race`,
negative/authz-denied, ABAC scope leakage, migration up/down, vitest, Playwright
E2E for user-visible flows), runs the gates in
[references/quality-gates.md](references/quality-gates.md), brings up the live
stack for E2E when finch or the API changed, and runs `packages/loadgen` when an
ingest/alert path changed. Expect `qa-report.md`. A failing build goes straight
to the fix loop.

## Review — parallel

Launch every selected reviewer **in a single message**. Each gets: run
directory, base ref, and the instruction to write `findings-<role>.md` in the
format from [references/report-format.md](references/report-format.md). Do not
paste your own opinion of the change into reviewer prompts.

## Phase 4 — Fix loop

1. Collect and de-duplicate findings (same file+line+issue → highest severity).
2. Must-fix = BLOCKER + MAJOR; advisory = MINOR + NIT.
3. Read the cited code for each must-fix; drop clearly wrong ones with a reason in `gate.md`.
4. Route must-fix findings to the coder for that file's stack (round *n*) → `fixes-round-<n>.md`.
5. Re-run QA gates, then only the reviewers whose findings were touched.
6. Stop after 3 rounds; anything open goes to the user (POA&M, change approach, or stop).

## Phase 5 — Document (`compliance-scribe`)

Mandatory for any code change. Updates `docs/compliance/*.md` (bootstrapping it
on first run), `docs/ARCHITECTURE.md`, `docs/DEPLOYMENT.md`, and touched package
READMEs from the plan, impl notes, findings and QA report; writes
`docs-update.md`. Never inflate implementation status beyond what code and tests show.

## Phase 6 — Release (`release-engineer`)

Run when the change touches `go.mod`/`go.sum`, `package.json`/lockfile,
a `Containerfile`, `deploy/` (Helm, Flux, k8s, compose), CI, `Taskfile.yaml`/
`Makefile`, env/config keys, a DB migration, or the collector ↔ heron wire
contract (site collectors upgrade on their own schedule; old collectors must
keep working).

## Phase 7 — Gate

Write `gate.md` (format in report-format.md) and decide:

- **PASS** — all gates green, no open BLOCKER/MAJOR, compliance docs updated.
- **PASS WITH POA&M** — only items the user explicitly accepted as `// POAM:`.
- **FAIL** — anything else. Say exactly what is open.

Tell the user: verdict, files changed, test/E2E results with the actual
commands, open advisories, POA&M items. Do **not** commit, push, or open a PR
unless the user asks. Remember: merging to `main` auto-deploys to the windep dev
cluster via Flux.

## Orchestrator rules

- Self-contained subagent prompts: goal, run dir, files to read, file to write, scope.
- Reviewers are read-only; route any code change through `go-coder` / `finch-coder`.
- Treat agent reports as claims; re-run the key gate commands yourself before PASS.
- Never weaken a security, compliance, data-integrity, or scale-regression finding to reach PASS.
- Keep the user's task list current: one plan step per phase that runs.
- Long-running commands (live stack, Playwright, loadgen, full `go test -race`) run in the background.
- Tear down any stack QA started (`make down`) before the gate.
