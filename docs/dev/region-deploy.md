# Region Deploy

Design document for the **Region Deploy** feature: a UI-driven workflow that
provisions a new Kubernetes cluster on bare-metal servers at a site and rolls
out the site service stack (auth DNS, recursive DNS, DHCP, collector).

This is the single source of truth. Update it as decisions evolve.

---

## 1. Architecture overview

```
                ┌─────────────────────────────────────────────────┐
                │                  CENTRAL CLUSTER                │
                │                                                 │
                │  ┌────────────┐   ┌────────────┐   ┌──────────┐ │
                │  │ Frontend   │──▶│  API       │──▶│ Postgres │ │
                │  │ (region-   │   │ (FastAPI)  │   │ (region_ │ │
                │  │  deploy.tsx│   │            │   │  deploy*)│ │
                │  └────────────┘   └─────┬──────┘   └──────────┘ │
                │        ▲                │                       │
                │        │ SSE            │ enqueue               │
                │        │                ▼                       │
                │  ┌─────┴──────┐    ┌────────────┐   ┌──────────┐│
                │  │ Redis      │◀───┤ Worker     │──▶│Tinkerbell││
                │  │ (pubsub:   │    │ (Celery    │   │ (Smee +  ││
                │  │ deploy:id) │    │  orches-   │   │  Tink +  ││
                │  │            │    │  trator)   │   │  Hegel + ││
                │  └────────────┘    └─────┬──────┘   │  Rufio)  ││
                │                          │          └──────────┘│
                │                          │                      │
                └──────────────────────────┼──────────────────────┘
                                           │
                            Rufio (BMC) ───┼─ kubectl/Helm (v6)
                                           │
                ┌──────────────────────────▼──────────────────────┐
                │                  REGION (SITE)                  │
                │                                                 │
                │            Single IPv6 VLAN (end-to-end)        │
                │       UEFI HTTP Boot over IPv6 for provision    │
                │   ┌──────────┐   ┌──────────┐   ┌──────────┐    │
                │   │ Node 1   │   │ Node 2   │   │ Node N   │    │
                │   │ control- │   │ worker   │   │ worker   │    │
                │   │ plane    │   │          │   │          │    │
                │   └──────────┘   └──────────┘   └──────────┘    │
                │                                                 │
                │   Cilium 1.19.3 (eBPF, IPv6-only, BGP)          │
                │   ├─ CoreDNS auth                               │
                │   ├─ Hickory recursive                          │
                │   ├─ Kea DHCP (+ control-agent REST)            │
                │   ├─ go-collector                               │
                │   └─ NAT46 LB at edge nodes (IPv4 ingress)      │
                └─────────────────────────────────────────────────┘
```

### High-level flow

1. Operator opens **Region Deploy** page, selects a site.
2. Wizard collects node inventory, network config, service selection, DHCP
   scopes, DNS zones.
3. Pre-flight checks run server-side (hard gate).
4. On submit, `region_deployment` row is created (`status=pending`); no work
   starts yet.
5. Operator clicks **Start**; Celery task `run_region_deploy(id)` is enqueued.
6. Worker walks the state machine, emits events to Postgres + Redis pubsub.
7. UI subscribes via SSE to `deploy:{id}`; stage tree + live logs update in
   real time.
8. On success, the new region's kubeconfig is stored as a k8s Secret on
   central; the site is marked `region_status=ready`.

---

## 2. Decision log

| Area                  | Decision                                                  |
| --------------------- | --------------------------------------------------------- |
| Cluster bootstrap     | kubeadm + cloud-init via UEFI HTTP Boot over IPv6         |
| PXE serving           | Tinkerbell (Smee/Tink/Hegel/Rufio) on central, IPv6-only  |
| BMC control           | Redfish via Rufio (Tinkerbell-native)                     |
| Orchestration         | Celery worker on central, state machine, SSE event stream |
| Progress UX           | Stage tree + live logs via SSE                            |
| CNI / LB              | Cilium **1.19.3** with native BGP                         |
| LB mode               | SNAT default; DSR opt-in per deployment                   |
| Address family        | IPv6-only pods/services/internal                          |
| Edge model            | NAT46 LB (pattern A); NAT64+DNS64 opt-in                  |
| DHCP                  | Kea DHCP with REST control-agent                          |
| Auth DNS              | CoreDNS (existing chart)                                  |
| Recursive DNS         | Hickory (existing chart); DNS64 zone when pattern B on    |
| Network               | Single IPv6 VLAN end-to-end (provisioning + production)   |
| Central v6 enablement | **Prerequisite** workstream (Phase 0)                     |
| IPAM v6 schema        | v6 columns alongside v4 (no overloading)                  |
| Pre-flight            | Hard gate; all checks must pass to enable Start           |
| Secrets               | k8s Secrets on central; DB stores secret refs             |
| Multi-cluster mgmt    | Kubeconfig-per-region (Secret); CAPI deferred             |

### Rationale (one-line each)

- **kubeadm over k3s/CAPI**: matches existing operator knowledge; k3s edge
  bias not needed; CAPI infra cost not justified at current site count.
- **Tinkerbell**: actively developed (CNCF), k8s-native CRDs (Hardware,
  Template, Workflow), workflow model maps cleanly onto our state machine.
  Replaces Matchbox which is effectively in maintenance-only mode.
- **Rufio for BMC**: Tinkerbell-native Redfish controller; consistent with
  the rest of the Tink stack, avoids a parallel `sushy`-based code path.
- **Single IPv6 VLAN**: simpler operationally — one address family, one
  VLAN, no v4/v6 toggle anywhere in the stack. Requires UEFI HTTP Boot
  over IPv6 on all hardware; we accept this as a hardware-selection
  constraint and enforce via pre-flight.
- **Cilium + native BGP**: aligns with existing site `gobgp` BGP fabric; one
  tool for CNI + LB + policy + observability (Hubble).
- **SNAT default**: stateful firewalls and uRPF at sites are unknown; SNAT
  is the boring choice that won't surprise.
- **IPv6-only internal**: Meta-style — eliminates dual-stack complexity,
  one address family, one mental model. IPv4 stays only where legacy
  requires it (north-south clients, PXE).
- **NAT46 LB at edge**: north-south is the only IPv4 dependency for most
  workloads; NAT64+DNS64 is overkill until pods must call IPv4-only APIs.
- **Hard pre-flight gate**: bad inputs early are cheap; a half-deployed
  cluster is expensive to clean up.
- **k8s Secrets**: better RBAC story than encrypted columns, no new infra
  (unlike Vault), DB still owns the relationships.
- **Kubeconfig-per-region**: simplest model that works; CAPI introduces a
  whole control-plane lifecycle abstraction that isn't justified yet.

---

## 3. Phase 0 — Central cluster v6 enablement (prerequisite)

This must land before the first real region deploy. Tracked separately from
the region-deploy workstream but blocking for production rollout.

### Scope

1. Central cluster nodes get v6 addresses on the mgmt interface.
2. Pod CIDR and service CIDR migrated to v6 (or added as dual-stack
   intermediate step).
3. Cilium installed on central with `ipv6.enabled=true`; existing CNI
   replaced if not already Cilium.
4. Backend (`api`, `worker`, `ingest`, `alerts`, `dns-probe`) listens
   dual-stack; OIDC/Keycloak callback URLs work over both.
5. Ingress (nginx/Traefik) dual-stack; TLS certs include v6 SANs where
   relevant.
6. Postgres and Redis bind v6 (in-cluster, so this is just Service config).
7. NAT46 LB pool configured on central edge nodes for legacy v4 clients.
8. Site→central collector traffic over v6 verified end-to-end.

### Acceptance criteria

- `kubectl get nodes -o wide` shows v6 InternalIP on every node.
- `curl -6 https://dcim.prod.dev.mil/health` returns 200 from outside
  cluster.
- Collector at a test site connects over v6 only.
- All existing v4 client paths still work (NAT46 verified).

### Estimated effort

2–3 weeks focused work. Sequenced before any region-deploy production use.

---

## 4. Data model

New migration adds the following tables. All FKs cascade-delete from
`region_deployment`.

### `region_deployment`

| Column                  | Type      | Notes                                                                                                    |
| ----------------------- | --------- | -------------------------------------------------------------------------------------------------------- |
| `id`                    | UUID PK   |                                                                                                          |
| `site_id`               | UUID FK   | → `site.id`                                                                                              |
| `name`                  | text      | operator-friendly label                                                                                  |
| `status`                | enum      | `pending`, `preflight`, `provisioning`, `joining`, `cni`, `apps`, `verify`, `ready`, `failed`, `aborted` |
| `current_stage`         | text      | machine-readable stage key                                                                               |
| `last_error`            | text      | last failure message; cleared on retry                                                                   |
| `config`                | JSONB     | network config (see schema below)                                                                        |
| `kubeconfig_secret_ref` | text      | `namespace/name` of k8s Secret holding the cluster kubeconfig                                            |
| `created_by`            | UUID      | → `user.id`                                                                                              |
| `created_at`            | timestamp |                                                                                                          |
| `started_at`            | timestamp | nullable                                                                                                 |
| `finished_at`           | timestamp | nullable                                                                                                 |

#### `config` JSONB schema

```json
{
  "pod_cidr_v6": "fd00:site:42:1000::/56",
  "svc_cidr_v6": "fd00:site:42:2000::/108",
  "lb_pool_v6": "fd00:site:42:3000::/112",
  "mgmt_v6": "fd00:site:42:0::/64",
  "provisioning_v6": "fd00:site:42:ff00::/64",
  "provisioning_dhcp_range_v6": [
    "fd00:site:42:ff00::100",
    "fd00:site:42:ff00::1ff"
  ],
  "edge_v4_pool": "203.0.113.16/28",
  "vip_v6": "fd00:site:42:0::1",
  "bgp_local_asn": 65042,
  "bgp_peers": [
    { "address": "fd00:site:42:0::ffff", "asn": 65000, "md5": null }
  ],
  "upstream_dns_v6": ["2001:4860:4860::8888"],
  "cilium_version": "1.19.3",
  "lb_mode": "snat",
  "edge_mode": "nat46",
  "nat64_enabled": false,
  "selected_services": {
    "dns_auth": { "enabled": true, "version": "...", "replicas": 2 },
    "dns_recursive": { "enabled": true, "version": "...", "replicas": 2 },
    "dhcp": { "enabled": true, "version": "...", "replicas": 2 },
    "collector": { "enabled": true, "version": "...", "replicas": 1 }
  }
}
```

### `region_deployment_node`

| Column                 | Type      | Notes                                                                |
| ---------------------- | --------- | -------------------------------------------------------------------- |
| `id`                   | UUID PK   |                                                                      |
| `deployment_id`        | UUID FK   | → `region_deployment.id`                                             |
| `hostname`             | text      |                                                                      |
| `mac`                  | macaddr   | primary NIC                                                          |
| `primary_ip_v6`        | INET      | from mgmt_v6                                                         |
| `provisioning_ip_v6`   | INET      | nullable; set by DHCPv6 (Smee) during UEFI HTTP Boot                 |
| `bmc_address`          | INET      | v6 preferred; v4 allowed if BMC mgmt network isn't v6 yet            |
| `bmc_creds_secret_ref` | text      | `namespace/name` of k8s Secret with bmc-username / bmc-password keys |
| `role`                 | enum      | `control-plane`, `worker`, `edge`                                    |
| `status`               | enum      | `pending`, `pxe-boot`, `installing`, `joining`, `ready`, `failed`    |
| `last_event`           | text      |                                                                      |
| `joined_at`            | timestamp | nullable                                                             |

### `region_deployment_event` (append-only)

| Column          | Type      | Notes                                              |
| --------------- | --------- | -------------------------------------------------- |
| `id`            | bigint PK | (sequence; ordered)                                |
| `deployment_id` | UUID FK   |                                                    |
| `stage`         | text      | matches state-machine stage key                    |
| `level`         | enum      | `info`, `warn`, `error`                            |
| `message`       | text      |                                                    |
| `payload`       | JSONB     | optional structured context (node id, retry count) |
| `created_at`    | timestamp | default now()                                      |

Indexed on `(deployment_id, id)` for efficient SSE catch-up.

### `region_deployment_service`

| Column            | Type    | Notes                                            |
| ----------------- | ------- | ------------------------------------------------ |
| `id`              | UUID PK |                                                  |
| `deployment_id`   | UUID FK |                                                  |
| `service`         | enum    | `dns_auth`, `dns_recursive`, `dhcp`, `collector` |
| `chart_version`   | text    |                                                  |
| `values_override` | JSONB   |                                                  |
| `status`          | enum    | `pending`, `installing`, `ready`, `failed`       |
| `last_error`      | text    |                                                  |

### IPAM v6 column additions

Add to existing prefix/network models:

- `prefix_v6 CIDR` (alongside `prefix_v4 CIDR` if present)
- `gateway_v6 INET`
- `family_capabilities` enum: `v4_only`, `v6_only`, `dual_stack`

Validators enforce that v6 columns only hold v6 values (and vice versa).
Migration backfills `family_capabilities` from existing data.

---

## 5. API surface

All endpoints under `/api/v1/region-deployments`. Authentication via existing
OIDC; authorization via new permissions (see §10).

| Method | Path                                       | Purpose                                       |
| ------ | ------------------------------------------ | --------------------------------------------- |
| POST   | `/`                                        | Create deployment (validates; status=pending) |
| GET    | `/`                                        | List (filterable by site, status)             |
| GET    | `/{id}`                                    | Full status incl. nodes + services            |
| PATCH  | `/{id}`                                    | Edit config while `status=pending` only       |
| DELETE | `/{id}`                                    | Delete if `pending` or `failed`/`aborted`     |
| POST   | `/{id}/start`                              | Enqueue Celery task                           |
| POST   | `/{id}/retry`                              | Retry from last failed stage                  |
| POST   | `/{id}/abort`                              | Cancel + power-off nodes via Redfish          |
| GET    | `/{id}/events?since=<event_id>`            | Paginated event history                       |
| GET    | `/{id}/events/stream`                      | **SSE** stream (Redis pubsub fan-out)         |
| GET    | `/{id}/kubeconfig`                         | Download kubeconfig (requires elevated perm)  |
| GET    | `/sites/{site_id}/region-deploy-preflight` | Run pre-flight checks; returns check list     |

### Pre-flight response shape

```json
{
  "ready": false,
  "checks": [
    {
      "key": "site.has_v6_pod_prefix",
      "label": "Site has IPv6 pod prefix allocated",
      "passed": true,
      "fix_hint": null
    },
    {
      "key": "bmc.reachable",
      "label": "BMC reachable on all nodes via Redfish",
      "passed": false,
      "fix_hint": "Node node-3 (10.42.99.13): Redfish /redfish/v1/ returned 503"
    }
  ]
}
```

### SSE event shape

```
event: stage
data: {"stage":"cni","status":"running","message":"Installing Cilium 1.19.3"}

event: log
data: {"stage":"cni","level":"info","message":"helm upgrade --install cilium ..."}

event: node
data: {"node_id":"...","status":"joining"}

event: done
data: {"status":"ready"}
```

---

## 6. Orchestrator state machine

Lives in `backend/src/dcim/regiondeploy/`. Single Celery task
`run_region_deploy(deployment_id)`; each stage is a method on a
`DeploymentRunner` class.

### Stages (ordered)

| Key                  | Description                                                                                               | Failure recovery                                                 |
| -------------------- | --------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------- |
| `preflight`          | Re-run pre-flight (in case anything changed since the UI ran it)                                          | Hard fail; no nodes powered on yet                               |
| `secrets`            | Create per-deploy Secrets: BMC creds, join token, kubeconfig placeholder                                  | Idempotent; safe to retry                                        |
| `render`             | Apply Tinkerbell `Hardware`, `Template`, `Workflow`, and Rufio `Machine` CRDs to central cluster          | Idempotent (server-side apply); safe to retry                    |
| `pxe.power`          | Rufio sets boot=HTTP-once (v6) and power-cycles. Control-plane first, then workers in parallel            | Power off failed nodes; retry per-node                           |
| `pxe.install`        | Smee serves DHCPv6 + iPXE chain; Tink Worker runs Workflow actions (image stream, ignition write, reboot) | Per-node Workflow restart; on persistent fail mark deploy failed |
| `joining`            | Wait for control-plane via callback; then worker join                                                     | Callback timeout → retry node; quorum-fail → deploy fail         |
| `cni`                | `helm install cilium` with rendered values (v6, BGP, LB pool)                                             | Helm rollback on failure; retry whole stage                      |
| `cni.bgp`            | Apply Cilium BGP CRDs, wait for peer `Established`                                                        | Diagnose via `cilium bgp peers`; retry                           |
| `apps.cert-manager`  | Helm install cert-manager                                                                                 | Standard Helm retry                                              |
| `apps.dns_auth`      | Helm install CoreDNS auth chart                                                                           | Standard Helm retry                                              |
| `apps.dns_recursive` | Helm install Hickory chart (incl. DNS64 zone if NAT64 enabled)                                            | Standard Helm retry                                              |
| `apps.dhcp`          | Helm install Kea chart                                                                                    | Standard Helm retry                                              |
| `apps.collector`     | Helm install collector chart with enrollment from existing flow                                           | Standard Helm retry                                              |
| `seed`               | Push initial DNS zones + DHCP scopes from IPAM via Kea/CoreDNS APIs                                       | Idempotent; retry                                                |
| `verify`             | DNS query, DHCP DORA test, collector check-in, Hubble flow check                                          | Hard fail with diagnostic bundle                                 |
| `finalize`           | Mark `ready`; write kubeconfig to Secret; update site record                                              | n/a                                                              |

### Event emission

Every stage transition and every command invocation calls
`emitter.emit(stage, level, message, payload)` which:

1. Inserts into `region_deployment_event`.
2. Publishes JSON to Redis channel `deploy:{id}`.

SSE handlers in the API subscribe to the Redis channel and forward to the
browser. Catch-up on (re)connect uses `?since=<last_event_id>` to backfill
from Postgres before going live.

### Retry semantics

- `POST /{id}/retry` resumes from `last_successful_stage + 1`.
- Each stage is idempotent: re-running `cni` reconciles the Helm release
  rather than failing on "already installed."
- `pxe.power` retry is per-failed-node (others stay where they are).
- Hard limit: 3 automatic retries within a stage; after that, surfaces as
  failed and waits for operator action.

### Abort semantics

`POST /{id}/abort`:

1. Sets status=`aborted`, signals task via Redis kill switch.
2. Powers off all nodes via Redfish.
3. Deletes per-deploy Secrets (BMC creds, join token).
4. Retains the deployment row + events for audit.
5. Does **not** clean up cluster state (it's powered off; if you want to
   reuse the hardware, run a fresh deploy which re-PXEs).

---

## 7. Pre-flight checklist (hard gate)

Defined as a JSON-schema'd list. Each check is a backend function returning
`{passed: bool, fix_hint: str | null}`.

| Key                               | What it verifies                                           |
| --------------------------------- | ---------------------------------------------------------- |
| `site.has_v6_pod_prefix`          | Site has a v6 prefix tagged for pod CIDR                   |
| `site.has_v6_svc_prefix`          | Site has v6 svc prefix                                     |
| `site.has_v6_lb_pool`             | Site has v6 LB pool                                        |
| `site.has_v6_mgmt`                | Site has v6 mgmt /64 with room for all nodes               |
| `site.has_v6_provisioning_prefix` | v6 provisioning prefix + DHCPv6 range present              |
| `site.has_edge_v4_pool`           | v4 pool for NAT46 LB allocated                             |
| `nodes.uefi_http_boot_v6_capable` | Per-node dry-run: BMC reports UEFI HTTP Boot v6 capability |
| `nodes.bmc_reachable`             | Redfish `/redfish/v1/` 200 on each node                    |
| `nodes.bmc_credentials_valid`     | Auth succeeds on each node                                 |
| `nodes.distinct_macs`             | No duplicate MACs in inventory                             |
| `bgp.peers_configured`            | At least one BGP peer with v6 capability flag              |
| `bgp.peer_reachable`              | TCP/179 over v6 reaches each peer from central (warn-only) |
| `dns.upstream_v6_resolvable`      | Upstream DNS answers AAAA over v6                          |
| `central.v6_ready`                | Central cluster is v6-enabled (Phase 0 done)               |
| `tinkerbell.healthy`              | Smee, Tink, Hegel, Rufio all reporting Ready on central    |
| `tinkerbell.ipxe_v6_artifacts`    | Smee serves v6-capable iPXE binaries for x86_64 UEFI       |
| `images.available`                | Required container images exist in registry                |

UI renders these as a checklist; **Start** is disabled until `ready=true`.

---

## 8. Helm chart inventory

New charts/components added under `infra/`:

| Path                                       | Purpose                                                                                            |
| ------------------------------------------ | -------------------------------------------------------------------------------------------------- |
| `infra/helm/tinkerbell/`                   | Tinkerbell stack: Smee, Tink, Hegel, Rufio (upstream chart + values overrides for v6-only)         |
| `infra/helm/cilium/values.yaml`            | Pinned Cilium 1.19.3 values; chart pulled from upstream                                            |
| `infra/helm/kea/`                          | Kea DHCPv6 chart (host-network on production VLAN; v6 control-agent REST)                          |
| `infra/helm/region-edge/`                  | NAT46 LB config (CiliumLoadBalancerIPPool + Service template)                                      |
| `infra/helm/nat64/`                        | Jool/tayga gateway + CoreDNS DNS64 (opt-in)                                                        |
| `backend/src/dcim/regiondeploy/templates/` | Jinja templates that render Tinkerbell `Hardware`, `Template`, `Workflow` CRDs + ignition payloads |

Existing charts (`coredns-auth`, `hickory-recursive`, `go-collector`) are
reused; orchestrator generates per-deploy values files.

---

## 9. Frontend

### Pages

- `frontend/src/pages/region-deploy.tsx` — wizard (Cloudscape Wizard).
- `frontend/src/pages/region-deploy-list.tsx` — list of deploys per site.
- `frontend/src/pages/region-deploy-show.tsx` — live status detail.

### Wizard steps (`region-deploy.tsx`)

1. **Site & basics** — site picker (filtered to sites without an active
   region deploy), deployment name.
2. **Nodes** — editable table: hostname / MAC / mgmt v6 / BMC address /
   BMC creds / role. Inline validation; add-row button.
3. **Network** — auto-prefilled from site IPAM where possible:
   - Pod / svc / LB / mgmt v6 prefixes
   - Provisioning v4 VLAN + DHCP range
   - Edge v4 pool
   - BGP local ASN + peers (v6 addresses)
   - Upstream DNS (v6)
   - LB mode: SNAT (default) / DSR (advanced toggle with warning)
   - Edge mode: NAT46 (default) / + NAT64+DNS64 (opt-in)
4. **Services** — checkboxes with per-service version + replicas:
   `dns_auth`, `dns_recursive`, `dhcp`, `collector`.
5. **DHCP scopes & DNS zones** — prefilled from IPAM/DNS records;
   editable list.
6. **Review** — full summary + pre-flight result. **Start** button
   disabled until pre-flight is fully green.

### Detail page (`region-deploy-show.tsx`)

Layout:

```
┌─────────────────────────────────────────────────────────────────┐
│ Header: site • name • status badge • elapsed • [Retry] [Abort]  │
├──────────────────────┬──────────────────────────────────────────┤
│ Stage tree           │ Live log pane (SSE)                      │
│ ▼ preflight   ✓      │ [INFO] preflight: all checks passed      │
│ ▼ secrets     ✓      │ [INFO] secrets: created bmc-creds-…      │
│ ▼ render      ✓      │ [INFO] render: 3 ignition profiles       │
│ ▼ pxe         ⟳      │ [INFO] pxe.power: node-1 → PowerOn       │
│   ├ pxe.power ✓      │ [WARN] pxe.install: node-2 slow boot     │
│   └ pxe.install ⟳    │ …                                        │
│ ▷ joining            │ (auto-scroll, pause on hover, filter by  │
│ ▷ cni                │  stage, level color-coded)               │
│ ▷ apps               │                                          │
│ ▷ verify             │                                          │
├──────────────────────┴──────────────────────────────────────────┤
│ Nodes: per-node power / install / join status table             │
└─────────────────────────────────────────────────────────────────┘
```

### SSE client

Hook `useDeploymentEvents(deploymentId)` that:

1. GETs `/events?since=0` to backfill recent history (configurable limit).
2. Opens `EventSource` on `/events/stream` with `Last-Event-ID` header.
3. Reconnects with exponential backoff; falls back to polling on persistent
   failure.

---

## 10. Permissions & audit

New permissions (added to RBAC):

- `region_deployment.read`
- `region_deployment.create`
- `region_deployment.start`
- `region_deployment.abort`
- `region_deployment.download_kubeconfig` (elevated)

Mapped to existing `dcim_admin` OIDC group by default (see
`backend/src/dcim/migrations/versions/20260513_0043_oidc_dcim_admin_mapping.py`).

Every state-changing API call writes an audit log entry via the existing
audit middleware. Audit records include deployment id, stage, actor.

---

## 11. Testing

### Unit

- Redfish client (mock `sushy` responses): power-on, set-boot, error paths.
- Matchbox renderer: golden ignition files committed under
  `backend/tests/regiondeploy/golden/`.
- State machine transitions: each stage advances/fails/retries correctly.
- Pre-flight checks: each check function in isolation with mocked deps.
- SSE event encoder.

### Integration

- `kind` cluster + fake Matchbox HTTP server + fake Redfish HTTP server.
- Full deploy walks every stage against the fake fleet; asserts final
  `status=ready` and that all expected Helm releases exist.

### E2E (manual, gated)

- One bare-metal test rack (3 nodes minimum). Run a real deploy end-to-end
  before declaring GA. Document the runbook in
  `docs/dev/region-deploy-runbook.md` after first successful real run.

---

## 12. PR breakdown

Sized so each PR is reviewable in < ~1 day and independently deployable.

### Phase 0 — Central v6 (separate workstream, prerequisite)

| PR   | Scope                                                         |
| ---- | ------------------------------------------------------------- |
| P0.1 | Cilium on central + v6 pod/svc CIDRs (dual-stack interim)     |
| P0.2 | Backend dual-stack listeners; OIDC v6                         |
| P0.3 | Ingress dual-stack; NAT46 pool on central edge                |
| P0.4 | Cutover: drop v4 pod/svc CIDR; verify all internal traffic v6 |

### Region deploy

| PR  | Scope                                                                                                              |
| --- | ------------------------------------------------------------------------------------------------------------------ |
| 1   | Migrations: `region_deployment*` tables + IPAM v6 columns + `family_capabilities` backfill                         |
| 2   | SQLAlchemy models + Pydantic schemas + read-only API (`GET` endpoints) + empty pages skeleton                      |
| 3   | Tinkerbell stack Helm install on central (Smee/Tink/Hegel/Rufio) tuned for IPv6-only + DHCPv6                      |
| 4   | Tinkerbell CRD generators in `backend/src/dcim/regiondeploy/`: Hardware/Template/Workflow + BMCMachine (Rufio)     |
| 5   | Ignition/cloud-init Jinja templates + golden tests                                                                 |
| 6   | Pre-flight check framework + all checks listed in §7 + API endpoint + wizard step 6 UI                             |
| 7   | Celery task skeleton + state machine + event emitter + SSE endpoint + Redis pubsub fan-out                         |
| 8   | Stages `preflight`/`secrets`/`render`/`pxe.power`/`pxe.install`/`joining` end-to-end with `kind` integration tests |
| 9   | Stages `cni`/`cni.bgp` with Cilium 1.19.3 chart + BGP CRDs + values templating                                     |
| 10  | Stages `apps.*` (cert-manager → CoreDNS → Hickory → Kea → collector) including Kea chart                           |
| 11  | Stages `seed` + `verify` + `finalize`; Hubble + DNS + DHCP DORA checks                                             |
| 12  | Retry + abort handling; per-node retries; deploy-level abort + power-off                                           |
| 13  | Wizard UI (steps 1–5) with Cloudscape components matching `rack-create.tsx`                                        |
| 14  | Detail page (stage tree + SSE log pane + nodes table) + `useDeploymentEvents` hook                                 |
| 15  | RBAC permissions wired; audit log entries on all state changes                                                     |
| 16  | NAT64+DNS64 opt-in: Jool/tayga chart, DNS64 zone in Hickory, wizard toggle                                         |
| 17  | DSR opt-in: wizard advanced toggle, Cilium values switch, warning copy                                             |
| 18  | Docs: runbook, troubleshooting, operator quickstart in `docs/dev/`                                                 |

---

## 13. Open risks & mitigations

| Risk                                                                       | Mitigation                                                                                                                                                                                            |
| -------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Vendor middlebox quirks on IPv6 traffic                                    | First real-site deploy is a discovery exercise; budget time for triage. Hubble + tcpdump.                                                                                                             |
| UEFI HTTP Boot v6 firmware bugs on some hardware                           | Pre-flight `nodes.uefi_http_boot_v6_capable` check is a hard gate; hardware that fails is ineligible. First-site rollout will surface vendor/firmware quirks — budget time. No v4 fallback by design. |
| Operator unfamiliarity with v6 (`ping`, `kubectl get nodes -o wide`, etc.) | Ship runbook + cheat-sheet; consider `dcim` CLI subcommand that hides v6 specifics.                                                                                                                   |
| Site router can't speak v6 MP-BGP                                          | Pre-flight check `bgp.peers_configured` flags this; site is ineligible until fixed.                                                                                                                   |
| Redfish dialect differences across vendors                                 | `sushy` handles most; abstract per-vendor quirks behind `bmc.py` with conformance tests.                                                                                                              |
| BMC creds in k8s Secrets leak via misconfigured RBAC                       | Dedicated namespace per deploy with tight RoleBindings; rotate creds post-deploy.                                                                                                                     |
| Long-running Celery task blocks worker                                     | Dedicate a worker queue (`region-deploy`) with its own pool; deploys never queue behind it.                                                                                                           |
| SSE through proxies dropping long-lived connections                        | Heartbeat events every 15s; UI auto-reconnects with `Last-Event-ID` catch-up.                                                                                                                         |
| Cilium 1.19.3 BGP regressions                                              | Pin patch via values file (one-line bump); upstream upgrade only after central tests pass.                                                                                                            |
| Pre-flight false-positives block legitimate deploys                        | Each check has a `fix_hint`; reviewable list; checks themselves are versioned (`check_v1`).                                                                                                           |

---

## 14. Glossary

- **NAT46** — translation from IPv4 client traffic to IPv6 backend services
  at the edge LB. Pods are oblivious to v4 existence.
- **NAT64** — translation from IPv6-only client traffic to IPv4-only
  destination services. Paired with **DNS64** which synthesizes AAAA from A.
- **DSR (Direct Server Return)** — load balancer mode where reply traffic
  bypasses the ingress LB and goes pod → client directly. Lower latency, but
  requires symmetric routing on the upstream network.
- **SNAT** — load balancer mode where the LB node rewrites the client IP to
  its own before forwarding to the pod. Safe everywhere; pod loses real
  client IP.
- **Tinkerbell** — CNCF bare-metal provisioning stack. Components used:
  - **Smee** — DHCP/DHCPv6 + iPXE + TFTP/HTTP boot server.
  - **Tink** — workflow engine; `Hardware`, `Template`, `Workflow` CRDs.
  - **Tink Worker** — agent that runs Workflow actions on the target node
    via an in-memory OS booted by Smee.
  - **Hegel** — metadata service (cloud-init / ignition userdata source).
  - **Rufio** — Kubernetes-native BMC controller (Redfish); reconciles
    `Machine` and `Job` CRDs to power/boot operations.
- **UEFI HTTP Boot** — modern replacement for legacy PXE. Firmware fetches
  the boot loader over HTTP(S) (IPv4 or IPv6) using DHCP option 59 or
  vendor-class matching.
- **Redfish** — modern HTTP+JSON BMC API (DMTF standard); replaces IPMI on
  current-generation server hardware. Accessed via Rufio in this design.
- **CAPI** — Cluster API; declarative kubernetes-native cluster lifecycle
  management. Deferred for this design.
