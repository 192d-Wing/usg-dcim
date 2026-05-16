# Region Deploy — operator runbook

Companion to [`region-deploy.md`](./region-deploy.md). That doc is
the architectural source of truth; this one is what an on-call
operator reads when they're walking a deployment through the wizard
or triaging a failure at 2am.

If anything here drifts from the code, the code wins — file an
update against this doc and link the PR.

---

## 1. End-to-end walkthrough

### Pre-conditions before clicking **New deployment**

|     | What to confirm                                                        | Where                                                           |
| --- | ---------------------------------------------------------------------- | --------------------------------------------------------------- |
| 1   | Site exists in IPAM with v6 prefixes allocated (pod / svc / LB / mgmt) | `/sites/{id}`                                                   |
| 2   | Hardware inventory is current — every node's MAC + BMC IP recorded     | `/sites/{id}` racks                                             |
| 3   | BMC creds are reachable from central                                   | manual ping; pre-flight tightens this once the BMC client lands |
| 4   | Central cluster is v6-enabled (Phase 0 done)                           | `kubectl get nodes -o wide` — InternalIP is v6                  |
| 5   | Fork images public-pulled                                              | `docker pull ghcr.io/1456055067/tinkerbell:dev-dhcpv6`          |

If item 5 fails: see [§5 Image rebuild](#5-image-rebuild).

### Wizard flow

1. **Site & basics.** Pick the site; name the deployment after
   it (`<site>-<purpose>` e.g. `site42-prod`).
2. **Nodes.** Add a row per bare-metal node. **Hostname** and
   **MAC** are required and must be unique within the deploy.
   **BMC address** is the IP Rufio talks to for power/boot-source
   ops. **Role** is one of `control_plane` / `worker` / `edge`;
   the first control-plane is the one that runs `kubeadm init`.
3. **Network.** JSON textarea. Fill in `pod_cidr_v6`,
   `svc_cidr_v6`, `lb_pool_v6`, `vip_v6`, `bgp_local_asn`,
   `bgp_peers`, `upstream_dns_v6`. Defaults are placeholders —
   they pass the JSON syntax check but won't actually work; the
   pre-flight catches the unset ones.
4. **Services.** Two toggles, both default off:
   - **DSR**: enable only when the site fabric guarantees
     symmetric routing (no strict uRPF, no stateful firewalls in
     the LB → pod return path).
   - **NAT64 + DNS64**: enable only when v6-only pods at this
     site must reach IPv4-only external endpoints. Standard
     workloads (auth/recursive DNS, DHCP, collector) don't need
     this.
5. **Review.** The wizard creates the deployment row in
   `pending` and runs pre-flight. Inspect the per-check results.
   **Start** is disabled until every check passes.
6. **Start.** Wizard navigates to `/region-deploy/{id}`. The
   detail page picks up the SSE event stream.

### Expected event sequence

```
preflight        7 built-in checks pass
secrets          [stub] real impl pending kubeconfig workstream
render           crds_yaml + ignition_for_node payloads emitted
pxe.power        [stub]
pxe.install      [stub]
joining          [stub]
cni              Cilium values_yaml + values dict emitted
cni.bgp          4 CiliumBGP* CRDs emitted
apps.cert-manager  values_yaml for jetstack/cert-manager
apps.dns_auth      values_yaml for CoreDNS auth
apps.dns_recursive values_yaml for Hickory recursive
apps.dhcp          values_yaml for Kea DHCPv6
apps.collector     values_yaml for go-collector
seed             [stub]
verify           "verify: 3 ok, 0 failed, 4 deferred-external"
finalize         status flips to ready
```

A run with all stubs still ticks through and ends in `ready` — the
deploy is conceptually complete; no provisioning happened yet
because the apply path waits on the regional-cluster kubeconfig
workstream. Operators today **copy the rendered payloads from the
event log into a real cluster manually**. See §3 below.

---

## 2. Reading the event log

Each event has a stage, level (info / warn / error), message, and
payload dict. The UI renders message + stage + level; payloads are
inspectable via the API.

### Quick API recipes

```powershell
# List events newest-first via the paginated endpoint.
$id  = "<deployment-id>"
$tok = (Get-Content $env:USERPROFILE\.dcim-token).Trim()
Invoke-RestMethod `
  -Uri  "https://dcim.prod.dev.mil/api/v1/region-deployments/$id/events?limit=200" `
  -Headers @{ Authorization = "Bearer $tok" }
```

### Render-stage payloads to grab

| Stage     | Payload key                    | What to do with it                                                                                                              |
| --------- | ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------- |
| `render`  | `crds_yaml`                    | `kubectl apply -f -` on the **central** cluster (Tinkerbell + Rufio CRs). Once central RBAC is granted, this becomes automatic. |
| `render`  | `ignition_for_node[<node-id>]` | Each node's per-boot Ignition. Already wired into the rendered Workflow CRs.                                                    |
| `cni`     | `values_yaml`                  | `helm install cilium cilium/cilium --version 1.19.3 -n kube-system --create-namespace -f -` on the **regional** cluster.        |
| `cni.bgp` | `crds_yaml`                    | `kubectl apply -f -` on the regional cluster after Cilium is up.                                                                |
| `apps.*`  | `values_yaml`                  | `helm install <chart> -f -` on the regional cluster.                                                                            |

---

## 3. Manual apply (current reality until the kubeconfig workstream

lands)

The orchestrator can't reach the regional cluster yet — there's no
path for it to retrieve the kubeadm-generated kubeconfig. Until
[`(j)` kubeconfig workstream](./region-deploy.md#3a-kubeconfig-workstream)
lands, the operator does the apply by hand from the event payloads.

The flow (rough):

1. **Central**: from the `render` event, save `crds_yaml` and apply
   to central's `tinkerbell` namespace. Smee/Tink/Rufio pick up the
   Hardware / Template / Workflow / Rufio Machine resources.
2. **Power on the nodes** with PXE boot enabled. Rufio reconciles
   the Machine + Job CRs and sets boot order; Smee serves the
   Hook OS via UEFI HTTP Boot v6.
3. **Nodes boot Flatcar** via the Ignition payloads the Workflow
   wrote during the install actions.
4. **`kubeadm init`** runs on the first control-plane via the
   systemd unit Ignition installed. The kubeadm-generated
   kubeconfig lands at `/etc/kubernetes/admin.conf` on that node.
   **Copy it back to your workstation manually** (until the
   callback workstream lands).
5. **Helm install Cilium** + apply BGP CRs (cni / cni.bgp event
   payloads) against the regional cluster.
6. **Helm install each app** (apps.\* event payloads).
7. Operator confirms the verify-pending external checks
   (DNS / DHCP DORA / Hubble / collector) by hand.

---

## 4. Troubleshooting

### Preflight blocks Start

Every failed check has a `fix_hint`. Common ones:

| Check                     | Fix                                                                            |
| ------------------------- | ------------------------------------------------------------------------------ |
| `nodes.distinct_macs`     | Two rows have the same MAC; correct the duplicate.                             |
| `nodes.has_control_plane` | At least one node must be role `control_plane`.                                |
| `site.has_v6_pod_prefix`  | Fill `config.pod_cidr_v6` in the Network step (e.g. `fd00:site:42:1000::/56`). |
| `bgp.peers_configured`    | Add at least one entry to `config.bgp_peers`.                                  |

### Deploy fails mid-flight

The detail page surfaces `last_error` in a top-level Alert with a
**Retry from `<stage>`** button.

1. Read the Alert. Stage name + error message tells you which
   stage handler raised.
2. Inspect the event log around the failed stage for context
   events emitted before the error.
3. Resolve the underlying cause (config drift, network blip,
   external API failure, etc.).
4. Click **Retry from `<stage>`** — the orchestrator resumes at
   the failed stage. Earlier stages are not re-run; their events
   already in the log stand.

### Stream shows "reconnecting…"

The SSE stream auto-reconnects with linear 2s backoff. A
`reconnecting…` badge for more than 30 seconds means either:

- The api pod is down — `kubectl get pods -n dcim`;
- A proxy in the path is dropping long-lived connections — the
  backend sends a 15s heartbeat, anything that breaks idle TCP
  flow at less than 15s would do this;
- Your bearer token expired — refresh the page, the auth layer
  reissues.

Events emitted while the stream is detached aren't lost: the
backend persists every event to `region_deployment_events`, and
on reconnect the UI re-fetches `?since=<last_id>` before
re-attaching to pubsub.

### Verify shows "deferred-external"

Expected today. Four external checks (DNS query, DHCP DORA,
collector check-in, Hubble flow probe) need the regional-cluster
kubeconfig — same blocker as the apply path. They surface as
`pending` outcomes in the verify event payload. They don't fail
the deploy.

### Abort doesn't take effect immediately

The orchestrator polls the row status between stage boundaries,
not mid-stage. Worst case is one stage's duration before the
abort lands. A long-running stage (when the apply path is real,
this'll mostly be `pxe.install`) may sit for a few minutes before
the abort signal is observed. The detail page status flips to
`aborted` as soon as the API call returns; the orchestrator
catches up shortly after.

---

## 5. Image rebuild

When upstream Tinkerbell + Hook update or when our fork branches
move:

```bash
# Smee + Tinkerbell monorepo
cd ~/projects/tinkerbell-monorepo && git checkout feat/dhcpv6
git pull upstream main           # merge upstream changes if needed
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go build -ldflags="-s -w" -o out/tinkerbell-linux-amd64 ./cmd/tinkerbell
podman build -t ghcr.io/1456055067/tinkerbell:dev-dhcpv6 \
  -f Dockerfile.tinkerbell --build-arg TARGETOS=linux \
  --build-arg TARGETARCH=amd64 --platform=linux/amd64 .
podman push ghcr.io/1456055067/tinkerbell:dev-dhcpv6

# Hook OS (LinuxKit; 20–30 min, several GB)
cd ~/projects/tinkerbell-hook && git checkout feat/dhcpv6
git pull upstream main
./build.sh build linuxkit         # produces Hook artifacts
# Publish via your usual HookOS distribution flow.
```

After repushing the Tinkerbell image, the existing Helm install
will pick up the new tag via the `helm upgrade` you trigger next:

```powershell
helm upgrade tinkerbell oci://ghcr.io/tinkerbell/charts/tinkerbell `
  -n tinkerbell `
  --reuse-values
```

---

## 6. Common one-liners

```powershell
# All events for a deployment, plain stream
kubectl logs -n dcim deploy/api `
  | Select-String "regiondeploy.*$DEPLOYMENT_ID"

# Replay preflight server-side (no Start side effects)
Invoke-RestMethod `
  -Uri "https://dcim.prod.dev.mil/api/v1/region-deployments/$id/preflight" `
  -Headers @{ Authorization = "Bearer $tok" }

# Force-abort a deploy (operator override; same as the Abort button)
Invoke-RestMethod `
  -Method Post `
  -Uri "https://dcim.prod.dev.mil/api/v1/region-deployments/$id/abort" `
  -Headers @{ Authorization = "Bearer $tok" }
```

---

## 7. Known gaps (single source of truth)

Maintained in [`region-deploy.md`](./region-deploy.md). High level:

- Apply paths for stages 8/9/10 (PXE / CNI / apps) are pending the
  regional-cluster kubeconfig workstream.
- Seed stage (DNS zone push, DHCP scopes, collector enrolment) is
  stubbed.
- Real external verify probes are pending the same kubeconfig
  workstream.
- Upstream merging of the Smee DHCPv6 + Hook `ipfamily=` work is
  open against the forks at `1456055067/tinkerbell` and
  `1456055067/hook`.
