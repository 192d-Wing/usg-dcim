# Dev cluster deploy & icam SSO (Keycloak) — reference

> **Scope: DEV only.** Everything here describes the development cluster
> `k8-01.xtic.dev.mil`. The icam Keycloak runs in dev mode (`start-dev`, embedded
> H2, no persistence, `sslRequired: none`) with plaintext dev credentials. Do not
> apply these patterns to staging/production unchanged.

This doc captures how builds reach the dev cluster, how OIDC capabilities flow
from Keycloak into the UI, and the gotcha that hides finch menus. Written after
debugging "I don't see all menus" → root-caused to a Keycloak id_token mapper.

---

## 1. Dev cluster & access

- **Cluster:** single-node microk8s, `https://k8-01.xtic.dev.mil:16443`.
- **Kubeconfig:** `~/.kube/k8-01.config` (context `k8-01`) — *not* the default
  `~/.kube/config`. Use `export KUBECONFIG=~/.kube/k8-01.config` or pass
  `--kubeconfig=~/.kube/k8-01.config` inline.
- **Namespaces:** `dcim` (the app), `icam` (Keycloak SSO), plus platform ns
  (cilium, cnpg, dns-system, sbc-system, container-registry…).

---

## 2. Deploying a new build

Images are built by CI on every push to `main` and pushed to
`ghcr.io/192d-wing/dcim-<service>:<full-commit-sha>` (plus `:latest`). The Helm
release pins a specific SHA.

```bash
export KUBECONFIG=~/.kube/k8-01.config
SHA=$(git rev-parse origin/main)   # or a specific commit

helm upgrade dcim deploy/helm/dcim -n dcim \
  --reuse-values --set global.image.tag="$SHA" \
  --wait --timeout 6m
```

- `--reuse-values` keeps the release's live overrides (secrets, DSNs, OIDC
  config, ingress) and only bumps the image tag.
- `migrations.runOnUpgrade: true` → an Alembic migration hook job runs on every
  upgrade (this is also what seeds OIDC role mappings — see §4).
- **When a build cuts a new `/api/v1/*` prefix over to otter-go, the ingress
  override must add it** (see §3), otherwise that path keeps hitting Python otter
  which no longer serves it. `--reuse-values` alone won't add new routes.

Verify:

```bash
kubectl -n dcim get pods
kubectl -n dcim get deploy dcim-otter-go -o jsonpath='{.status.readyReplicas}/{.status.replicas}{"\n"}'
```

---

## 3. Ingress routing (Python otter → Go otter-go cutover)

Single ingress `dcim-dcim`, host `dcim.xtic.dev.mil`, className `public`,
annotation `nginx.ingress.kubernetes.io/use-regex: "true"`. Paths, specific-first:

| Path | Backend |
|------|---------|
| `/api/v1/ipam/supernets/[^/]+/move` (ImplementationSpecific) | otter-go |
| `/api/v1/lir` | otter-go |
| `/api/v1/auth` | otter-go |
| `/api/v1/audit` | otter-go |
| `/api/v1/telemetry` | otter-go |
| `/api` | otter (Python — remaining endpoints) |
| `/` | finch (SPA) |

The Python→Go migration moves more prefixes to otter-go over time; this list
grows. The live release uses a **custom ingress override** (not the chart
default host `dcim.example.mil`), so new routes must be merged into the override.

---

## 4. OIDC capability chain (how a login becomes UI menus)

```
Keycloak (icam) login
  └─ id_token carries realm_access.roles: ["dcim-admin"]      ← requires id_token mapper (§5)
       └─ otter-go OIDC callback: extractIdpRoles()            packages/otter-go/internal/auth/oidc.go
            reads realm_access.roles FROM THE ID TOKEN
              └─ mints DCIM session JWT with idp_roles claim   (mint.go)
                   └─ per-request middleware resolveCaps()     internal/auth/middleware.go
                        GetCapabilitiesForIdpRoles(idp_roles)  db/queries/auth.sql
                          → oidc_role_mappings JOIN roles
                          → roles.permission_codes (JSON array)
                            └─ /api/v1/auth/me → {"capabilities": [...]}
                                 └─ finch hasCap() gates NAV_ITEMS
                                      packages/finch/src/components/layout/cloudscape-shell.tsx
```

Key data on the dev cluster:

- `oidc_role_mappings`: Keycloak `dcim-admin` → DCIM role `oidc-admin`
  (claim source `realm_access.roles`). Seeded idempotently by Alembic migration
  `20260513_0043_oidc_dcim_admin_mapping` (+ successors), which runs on every
  `helm upgrade`.
- DCIM role `oidc-admin` has `permission_codes = ["*"]` → all capabilities.
- finch menus: ungated items (Sites, Racks, Capacity, Alerts, Maintenance,
  Collectors) always show; **cap-gated** items hide unless the user holds the
  cap: IPAM (`ipam:subnets:read`), LIR (`lir:requests:*`/`allocations:read`),
  DNS (`dns:servers:read`), Import (`inventory:bulk:execute`), Audit
  (`audit:events:read`), Admin (`admin:users:read`/`roles:read`). `hasCap`
  honours bare `*` and segment globs (`dns:*`).

**Capabilities are baked into the DCIM session JWT at login** — any Keycloak or
role change requires the user to **log out and back in**.

---

## 5. The gotcha: id_token vs access_token role mapper

otter-go reads roles from the **id_token**. Keycloak's default realm-roles
mapper only adds `realm_access.roles` to the **access token** ("Add to ID token"
is off by default). Result: empty `idp_roles` → empty capabilities → finch hides
every cap-gated menu while ungated ones still show (the tell-tale symptom).

**Fix:** the `dcim-spa` client carries a dedicated realm-roles protocol mapper
with `id.token.claim=true`:

```
protocolMapper: oidc-usermodel-realm-role-mapper
claim.name:     realm_access.roles
multivalued:    true
id.token.claim: true       ← the critical bit
access.token.claim: true
```

This lives in the realm import (§6), so it survives restarts.

---

## 6. icam Keycloak is reproducible in-repo

Manifests: [`deploy/k8s/icam/`](../../deploy/k8s/icam/) — namespace, realm
ConfigMap, Deployment (Keycloak 25.0, `start-dev --import-realm`), LoadBalancer
Service, kustomization, README, and a **template** admin Secret.

```bash
# admin Secret once (template excluded from kustomize so apply -k can't clobber it):
kubectl -n icam create secret generic keycloak-admin \
  --from-literal=username=admin --from-literal=password='<strong-password>'

kubectl apply -k deploy/k8s/icam/
```

**Ephemeral H2, no PVC** → Keycloak re-imports the realm from the
`keycloak-realm` ConfigMap on **every pod restart**. That ConfigMap is the
**source of truth** for realm/clients/roles/users; live `kcadm` or admin-console
edits are **lost on restart**. To change the realm: edit
`deploy/k8s/icam/keycloak-realm.configmap.yaml`, `kubectl apply`, then
`kubectl -n icam rollout restart deploy/keycloak`.

Realm contents (dev): realm `dcim`; confidential client `dcim-spa`; realm roles
`dcim-admin`, `dcim-reader`; user `operator` (password `operator`) with
`dcim-admin`. Issuer `http://icam.xtic.dev.mil/realms/dcim`.

### Inspecting Keycloak (no curl/python3 in the image — use kcadm)

The auto-mode classifier blocks shelling into the shared icam pod unless a Bash
allow rule exists, and the command must **start with `kubectl`** (so the rule
matches) — pass `--kubeconfig=` inline rather than `export KUBECONFIG=`. Never
put the client secret on the command line.

```bash
# allow rule (settings.json): Bash(kubectl -n icam exec:*)   (and apply/rollout as needed)
kubectl -n icam exec --kubeconfig=~/.kube/k8-01.config deploy/keycloak -- \
  /opt/keycloak/bin/kcadm.sh config credentials --server http://localhost:8080 \
    --realm master --user <admin> --password <pw>
kubectl -n icam exec --kubeconfig=~/.kube/k8-01.config deploy/keycloak -- \
  /opt/keycloak/bin/kcadm.sh get clients -r dcim -q clientId=dcim-spa --fields id
```

---

## 7. Troubleshooting: "I don't see all menus"

1. **Decode your session token** (browser console):
   ```js
   JSON.parse(atob(localStorage.getItem('dcim_token').split('.')[1]))
   ```
   - `idp_roles` missing/empty → id_token isn't carrying realm roles → §5 mapper.
   - `idp_roles` present but name ≠ `dcim-admin` (casing/format) → mapping miss.
2. **Check `/api/v1/auth/me`** (Network tab) → `capabilities`. `[]` confirms the
   resolution broke upstream; `["*"]` means everything should show.
3. **DCIM side** (read-only): confirm `dcim-admin` resolves to caps —
   `oidc_role_mappings JOIN roles` → `permission_codes`.
4. **Keycloak side:** confirm `dcim-spa` has the `id.token.claim=true` realm-roles
   mapper (§6) and the user has the `dcim-admin` realm role.
5. After any change: **log out / log back in**.

---

## 8. Related references

- `docs/DEPLOYMENT.md` — fuller deploy guide (compose, k8s, SSO smoke).
- `docs/dev/otter-go-migration.md` — the Python→Go port.
- `deploy/k8s/icam/README.md` — the SSO manifest set + caveats.
- `packages/otter-go/internal/auth/` — OIDC callback, JWT mint, cap middleware.
- `packages/finch/src/components/layout/cloudscape-shell.tsx` — menu gating.
