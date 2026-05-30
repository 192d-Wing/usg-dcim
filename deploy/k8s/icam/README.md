# icam — dev SSO (Keycloak) for DCIM

Source-controlled manifests for the **icam** Keycloak that acts as the OIDC IdP
for DCIM in the dev cluster (`k8-01.xtic.dev.mil`, issuer
`http://icam.xtic.dev.mil/realms/dcim`). Captured from the live cluster so the
SSO stack is reproducible — previously it was hand-applied and untracked.

## Contents

| File | Resource |
|------|----------|
| `namespace.yaml` | `icam` namespace |
| `keycloak-realm.configmap.yaml` | `keycloak-realm` ConfigMap — the `dcim` realm import (realm, `dcim-spa` client, roles `dcim-admin`/`dcim-reader`, `operator` user) |
| `keycloak.deployment.yaml` | `keycloak` Deployment (Keycloak 25.0, `start-dev --import-realm`) |
| `keycloak.service.yaml` | `keycloak` LoadBalancer Service (:80 → 8080) |
| `keycloak-admin.secret.example.yaml` | **template** for the master-console admin Secret (excluded from kustomize) |
| `kustomization.yaml` | applies everything except the Secret |

## Apply

```bash
export KUBECONFIG=~/.kube/k8-01.config

# 1) Admin Secret (once; not in kustomize so it can't clobber an existing one):
kubectl -n icam create secret generic keycloak-admin \
  --from-literal=username=admin \
  --from-literal=password='<strong-password>'

# 2) Everything else:
kubectl apply -k deploy/k8s/icam/
```

## Important caveats

- **Ephemeral H2, no persistence.** The Deployment runs `start-dev` on the
  embedded H2 DB with **no PVC**. Every pod restart wipes the DB and re-imports
  the realm from `keycloak-realm.configmap.yaml`. That ConfigMap is therefore the
  **source of truth** for realm/clients/roles/users — changes made live via
  `kcadm` or the admin console are **lost on restart**. Edit the ConfigMap, then
  `kubectl -n icam rollout restart deploy/keycloak`.
- **id_token role mapper.** The `dcim-spa` client carries a `realm roles`
  protocol mapper with `id.token.claim=true`. otter-go reads `realm_access.roles`
  from the **id_token**; without this mapper, SSO logins resolve zero
  capabilities and finch hides every cap-gated menu.
- **Dev-only.** Keycloak runs in dev mode (`start-dev`, `sslRequired: none`, H2,
  realm contains plaintext dev credentials). For anything beyond dev, switch to
  `start` with an external DB (`KC_DB`) + persistence and move secrets out of the
  realm import.
- **Re-login required** after any realm/role change — DCIM capabilities are baked
  into the session JWT at login.
