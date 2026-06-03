# Orchestrator Port Plan — `run_region_deploy` Python → Go

## Goal

Retire the last Python service in production (`otter-worker` running
`arq` against `run_region_deploy`) by porting the orchestrator to Go.
After this lands, `packages/otter/` can be deleted entirely and the
`otter` Containerfile + the `otter-worker` helm subchart go with it.

## What's already done

The session that landed PRs #261-#271 ported every API route, every
cron job, and the arq enqueuer. The only thing still served from
Python is the arq job *consumer* for `run_region_deploy`.

Already on otter-go from prior PRs:

- `tokens.py` → `internal/regiondeploy/callback_token.go` (PR #264)
- `preflight.py` → `internal/regiondeploy/preflight.go` (PR #263)
- `events.py` row writer → `internal/regiondeploy/sse.go` (PR #265,
  reads the table for SSE backfill; the actual emit still happens
  from Python's `events.emit` during stage transitions)
- arq enqueuer (msgpack) → `internal/regiondeploy/arq.go` (PR #267)
- in-pod K8s client (Secret CRUD only) → `internal/regiondeploy/k8s.go`
  (PR #264)

## What's left to port

| Module | LOC | Purpose | External deps |
|---|---|---|---|
| `ignition.py` | 369 | Per-node Flatcar Ignition 3.4 JSON (systemd units + storage files) | None — pure text |
| `dns_site.py` | 85 | Helm values for the dns-site chart | None |
| `dhcp_site.py` | 72 | Helm values for the dhcp-site chart | None |
| `apps.py` | 188 | Helm values for cert-manager / dns_auth / dns_recursive / dhcp / collector | None |
| `cilium.py` | 279 | Cilium Helm values + 4 BGP CRD manifests | Cilium CRDs |
| `crd.py` | 331 | Tinkerbell + Rufio CRD manifests (Hardware, Template, Workflow, BMCMachine) | Tinkerbell + Rufio CRDs |
| `k8s.py` | 312 | Server-side-apply for CRDs (extends existing in-pod client) | k8s API |
| `verify.py` | 283 | Post-deploy verification checklist | None for the four built-in checks; the four deferred-external checks need cluster access |
| `events.py` emit | 96 | Append to `region_deployment_events` + `XADD` to `dcim:deploy:<id>` | Redis |
| `orchestrator.py` | 563 | 16-stage state machine + arq job handler | All of the above |

Total: ~2600 lines of Python.

## Architecture decisions

**Where does the orchestrator run?**

Each deploy run takes ~30 min wall-clock with long blocking calls
(Redfish power-cycle, Tinkerbell workflow polling, kubeconfig retry
loop). That's not cron-shaped — a new dedicated worker binary is
cleaner than cramming it into `otter-go-scheduler`.

Recommendation: **new `otter-go-orchestrator` binary** with its own
helm subchart. Consumes arq jobs from Redis via the same msgpack
wire format PR #267 established. Runs alongside the scheduler.

**Helm apply path?**

Python's orchestrator emits Helm values + Cilium CRD YAML as event
log entries but doesn't actually `helm install`. Operators currently
run `helm install -f -` manually until the regional-kubeconfig
workstream lands. Keep that contract on the Go side — the
orchestrator stays a renderer, not an applier. Reduces blast radius
and keeps the port testable without standing up a real regional
cluster.

**Tinkerbell + Rufio CRD types?**

Options:
- Import `github.com/tinkerbell/tink` + `github.com/tinkerbell/rufio`
  for typed CRs.
- Hand-define minimal struct types matching the YAML shape Python
  produces.

Hand-defined structs are simpler and avoid version-skew risk against
the operator's cluster. The Python code is already YAML-shape-only,
not API-typed.

## PR breakdown (6 PRs)

### PR 1 — Pure-Go Ignition renderer

- `internal/regiondeploy/ignition/` package
- Port `ignition.py` to Go's `text/template` + JSON marshal
- Three systemd units: `kubeadm-init` (first CP), `kubeadm-join`
  (workers/edge/secondary CPs), `kubeconfig-callback`
- Three storage files: `/etc/hostname`, `/etc/dcim/cluster.env`,
  `/etc/dcim/callback.token`
- **Golden-byte tests** per node role (matches the pattern from the
  DNS bundle stack). Pull Python's `ignition.py` test fixtures into
  the Go test directory and assert exact-byte parity.
- Standalone — no other PRs depend on it
- ~3-4 days

### PR 2 — Helm values renderers (apps / dns_site / dhcp_site)

- `internal/regiondeploy/helmvalues/`
- Port `apps.py` (5 renderers), `dns_site.py`, `dhcp_site.py`
- Pure dict-to-YAML transforms
- Golden tests per chart
- Standalone
- ~3-4 days

### PR 3 — Tinkerbell + Rufio CRD generators

- `internal/regiondeploy/tinkerbell/`
- Port `crd.py`: Hardware (per node), Template (per deployment,
  3-action stream-image / write-ignition / reboot), Workflow (per
  node, binds Hardware + Template + Ignition), BMCMachine (Rufio,
  per node)
- Hand-defined struct types (NOT imports from upstream Tinkerbell)
- `sigs.k8s.io/yaml` for serialization
- Golden tests per CRD kind
- Depends on PR 1 (Workflow embeds per-node Ignition)
- ~3-4 days

### PR 4 — Cilium values + BGP CRDs

- `internal/regiondeploy/cilium/`
- Port `cilium.py`: Helm values + 4 CRDs (BGPPeerConfig,
  BGPClusterConfig, BGPAdvertisement, LoadBalancerIPPool)
- Skip-BGP-when-unset branch matches Python's warn-and-continue
- Golden tests
- Standalone
- ~3-4 days

### PR 5 — K8s SSA client extension + verify runner + events emit

- Extend `internal/regiondeploy/k8s.go` (PR #264) with
  server-side-apply for arbitrary CRs (`PATCH ?fieldManager=...&force=true`
  with `Content-Type: application/apply-patch+yaml`)
- New `internal/regiondeploy/verify/` package — 4 built-in checks
  (preflight_no_drift, render_chain_complete, no_error_events,
  pending-external placeholder)
- New `internal/regiondeploy/events/emit.go` — append to
  `region_deployment_events` + Redis `XADD` to `dcim:deploy:<id>`.
  Existing SSE reader (PR #265) already drains the right channel
  so no SSE changes needed.
- Tests with fake K8s + fake Redis
- ~3-4 days

### PR 6 — Orchestrator state machine + arq consumer + cutover

- New `cmd/otter-go-orchestrator/main.go` binary
- arq job consumer: msgpack decode from `arq:queue` ZSET (mirror of
  PR #267's encoder side), same job name `run_region_deploy`
- `internal/regiondeploy/orchestrator/` — 16-stage state machine
  (preflight → secrets → render → pxe.power → pxe.install → joining
  → cni → cni.bgp → apps.{cert-manager,dns_auth,dns_recursive,dhcp,
  collector} → seed → verify → finalize)
- Stage dispatch table calls PRs 1-5 helpers
- Retry-resume: read `current_stage` from row, restart from there
- Abort: poll row.status between stages, exit cleanly if `aborted`
- New helm subchart `deploy/helm/dcim/charts/otter-go-orchestrator/`
- Helm umbrella `Chart.yaml` adds the new subchart; `otter-worker`
  subchart drops to disabled-default (kept around for one release
  cycle as the rollback path, then deleted in a follow-up)
- Integration tests with a fake K8s API + fake Tinkerbell that walks
  a full happy-path deploy through every stage
- ~1 week — possibly split into two PRs if the test surface grows

### PR 7 (follow-up, separate session) — Final deletion

After 1-2 release cycles of the Go orchestrator running clean:

- Delete `packages/otter/` entirely
- Delete the `dcim-otter` image build matrix entry from CI
- Delete `otter-worker` subchart
- Delete the `otter` CI ruff+pytest job
- Update the python-to-go-migration memory file with the final state

## Risks

**Tinkerbell CRD version skew.** The CRD struct shapes change
between Tinkerbell releases. Mitigation: hand-define structs as
YAML-shape-only (no upstream imports), pin the shape against the
deployed operator version, and document the version in the package
docstring. A CRD shape drift becomes a one-line code change instead
of a dep-graph problem.

**Long-running stage handlers blocking SIGTERM.** Each stage handler
must respect `ctx.Done()`. Tinkerbell workflow polling and
kubeconfig retry loops are the obvious offenders — the helm chart's
`terminationGracePeriodSeconds` may need to be tuned past the
default 30s. Document the deploy-completion latency budget in the
package docstring.

**Replay-on-restart semantics.** If the orchestrator pod restarts
mid-deploy, the arq job is reclaimed by another worker. The state
machine must read `current_stage` from the DB row and resume from
there — never restart from `preflight`. PR 6 must include a test
that simulates a SIGTERM mid-stage and verifies the next consumer
resumes correctly.

**Helm-values wire-shape drift.** The five chart `values.yaml`
schemas (cert-manager, coredns-auth, hickory-recursive, kea-dhcp,
go-collector) live in `deploy/helm/charts/*/` and evolve. The Go
renderers must stay in lockstep. Mitigation: golden-byte tests
per renderer **plus** a CI check that `helm template` against the
shipped charts with the Go-generated values succeeds.

**Tinkerbell Worker action shapes.** Python's Template generates a
3-action stream-image/write-ignition/reboot plan. If the Tinkerbell
Worker version on the cluster changes the action interface, the
generated YAML won't apply. Pin the Tinkerbell version in the
operator-runbook doc and gate any future Tinkerbell upgrade behind
a Go renderer-update PR.

## What's NOT in scope

- **`evaluate_alerts` / `sweep_collectors` / `dns_health_checks`** —
  already on Go services (services/go-alerts + services/go-dns-probe).
  No port work needed.
- **Helm apply path** — orchestrator stays a renderer; `helm install`
  remains operator-driven until the regional-kubeconfig workstream
  ships separately.
- **Four deferred-external verify checks** (DNS query, DHCPv6 DORA,
  collector check-in, Hubble flows) — pending cluster-access
  workstream. PR 5 lands a placeholder that records `pending` and
  PR 6 doesn't fail the deploy on them.
- **Redfish BMC power-cycle (pxe.power stage)** — already a stub in
  Python. PR 6 ports the stub. Real Redfish integration is its own
  workstream.
- **Migrating the existing in-flight deploys at cutover time** — the
  state machine resumes from `current_stage` regardless of which
  binary writes the row, so a long-running deploy started on Python
  finishes on the Go orchestrator after pod rollover. No data
  migration needed.

## Suggested order

1. PR 1 (Ignition) — standalone, no deps, easy first PR
2. PR 2 (Helm values) — standalone, no deps
3. PR 3 (Tinkerbell CRDs) — depends on PR 1
4. PR 4 (Cilium) — standalone
5. PR 5 (k8s SSA + verify + events) — standalone
6. PR 6 (orchestrator + cutover) — depends on PR 1-5

PRs 1-2 can land in parallel as immediate next session work. PRs 3-5
follow. PR 6 needs a dedicated session given the integration test
surface.

## Open questions for the operator

- Is the regional-kubeconfig workstream blocking PR 6, or can it
  ship behind a feature flag that defaults to the renderer-only
  contract?
- Confirm the Tinkerbell + Rufio CRD versions deployed against the
  target clusters so the hand-defined struct shapes match.
- Is `otter-go-orchestrator` an acceptable subchart name? It
  matches the existing `otter-go-scheduler` / `otter-go` family but
  the name is long.
