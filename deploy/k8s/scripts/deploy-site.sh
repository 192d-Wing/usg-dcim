#!/usr/bin/env bash
# Deploy a fresh site (DNS + DHCP + bundle puller) to a k8s cluster.
#
# Wraps `kubectl create ns` + Secrets/ConfigMaps + two `helm install`
# calls into one command, so an operator standing up a new site
# doesn't have to remember the per-service config layout.
#
# The site's DnsServer and DhcpServer rows in the dcim DB still need
# to exist (so otter knows what bundle to serve) — this script just
# lands the *pods*. Create the rows via the dcim UI first, then run
# this with the matching --dns-server-id / --dhcp-server-id.
#
# Example:
#   ./deploy-site.sh \
#       --site-code kfk-002 \
#       --otter-base-url https://dcim.xtic.dev.mil \
#       --otter-token "$(cat /tmp/dcim-collector-token)" \
#       --dns-server-id $(uuidgen) \
#       --dns-server-name kfk-002-dns-recursive \
#       --dns-anycast-ip 10.50.2.53 \
#       --dhcp-server-id $(uuidgen) \
#       --dhcp-server-name kfk-002-dhcp \
#       --dhcp-anycast-ip 10.50.2.67
#
# Defaults are sane for k8-01.xtic.dev.mil microk8s but every flag is
# overridable so the same script works against kind/eks/etc.

set -euo pipefail

# ─── defaults ─────────────────────────────────────────────────────────
SITE_CODE=""
NAMESPACE=""
OTTER_BASE_URL=""
OTTER_TOKEN=""
LB_POOL_LABEL_KEY="sbc.usg/site"
LB_POOL_LABEL_VALUE=""           # default: same as --site-code
DNS_SERVER_ID=""
DNS_SERVER_NAME=""
DNS_FABRIC_ID="00000000-0000-0000-0000-000000000000"
DNS_SITE_ID="00000000-0000-0000-0000-000000000000"
DNS_ROLE="recursive"             # recursive | auth
DNS_ANYCAST_IPS=()
DHCP_SERVER_ID=""
DHCP_SERVER_NAME=""
DHCP_FABRIC_ID="00000000-0000-0000-0000-000000000000"
DHCP_ANYCAST_IPS=()
DHCP_V6=false
KEA_BASIC_USER="kea"
KEA_BASIC_PASSWORD=""
# Override image references. Charts' defaults are wrong as of PR 71/72
# (collector default points at a non-existent ghcr org; Kea images
# point at internetsystemsconsortium which only publishes bind9).
DNS_COLLECTOR_REPO=""
DNS_COLLECTOR_TAG=""
DNS_AUTH_REPO=""
DNS_AUTH_TAG=""
DNS_RECURSIVE_REPO=""
DNS_RECURSIVE_TAG=""
KEA_CTRL_AGENT_REPO=""
KEA_CTRL_AGENT_TAG=""
KEA_DHCP6_REPO=""
KEA_DHCP6_TAG=""
HELM_TIMEOUT="3m"
WAIT=true
SKIP_DNS=false
SKIP_DHCP=false
CHART_DIR=""

usage() {
  sed -nE 's/^# ?//;1,/^$/p' "$0" | sed -nE '/^Deploy a/,/^Defaults/p'
  cat <<EOF

Required:
  --site-code <code>         Short site identifier (kebab-case). Drives namespace name.
  --otter-base-url <url>     Base URL of central otter API (e.g. https://dcim.xtic.dev.mil).

One of (or both):
  --dns-server-id <uuid>     Skip with --skip-dns
  --dhcp-server-id <uuid>    Skip with --skip-dhcp

Common:
  --namespace <name>         Default: dcim-site-<site-code>.
  --otter-token <bearer>     Token the bundle puller uses (Authorization: Bearer …).
                             Required unless both --skip-dns and any other puller pieces are off.
  --lb-pool-label-value <v>  Cilium LB-IPAM pool selector value. Default: same as --site-code.

DNS:
  --dns-server-name <name>   Helm release suffix.
  --dns-role recursive|auth  Default: recursive.
  --dns-anycast-ip <ip>      Repeatable. At least one IP required to provision DNS.

DHCP:
  --dhcp-server-name <name>  Helm release suffix.
  --dhcp-anycast-ip <ip>     Repeatable. At least one required.
  --dhcp-v6                  Also expose UDP/547 (DHCPv6).
  --kea-basic-user <name>    Default: kea.
  --kea-basic-password <pw>  Required if DHCP is being deployed; generated if omitted.

Helm:
  --chart-dir <path>         Override charts dir. Default: <repo>/deploy/helm.
  --timeout <duration>       helm --wait timeout. Default: 3m.
  --no-wait                  Skip --wait.

EOF
  exit 1
}

err() { echo "deploy-site: ERROR: $*" >&2; exit 1; }
log() { echo "deploy-site: $*"; }

# ─── arg parse ────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --site-code) SITE_CODE="$2"; shift 2;;
    --namespace) NAMESPACE="$2"; shift 2;;
    --otter-base-url) OTTER_BASE_URL="$2"; shift 2;;
    --otter-token) OTTER_TOKEN="$2"; shift 2;;
    --lb-pool-label-value) LB_POOL_LABEL_VALUE="$2"; shift 2;;
    --dns-server-id) DNS_SERVER_ID="$2"; shift 2;;
    --dns-server-name) DNS_SERVER_NAME="$2"; shift 2;;
    --dns-fabric-id) DNS_FABRIC_ID="$2"; shift 2;;
    --dns-site-id) DNS_SITE_ID="$2"; shift 2;;
    --dns-role) DNS_ROLE="$2"; shift 2;;
    --dns-anycast-ip) DNS_ANYCAST_IPS+=("$2"); shift 2;;
    --dhcp-server-id) DHCP_SERVER_ID="$2"; shift 2;;
    --dhcp-server-name) DHCP_SERVER_NAME="$2"; shift 2;;
    --dhcp-fabric-id) DHCP_FABRIC_ID="$2"; shift 2;;
    --dhcp-anycast-ip) DHCP_ANYCAST_IPS+=("$2"); shift 2;;
    --dhcp-v6) DHCP_V6=true; shift;;
    --kea-basic-user) KEA_BASIC_USER="$2"; shift 2;;
    --kea-basic-password) KEA_BASIC_PASSWORD="$2"; shift 2;;
    --dns-collector-image-repo) DNS_COLLECTOR_REPO="$2"; shift 2;;
    --dns-collector-image-tag)  DNS_COLLECTOR_TAG="$2";  shift 2;;
    --dns-auth-image-repo)      DNS_AUTH_REPO="$2";      shift 2;;
    --dns-auth-image-tag)       DNS_AUTH_TAG="$2";       shift 2;;
    --dns-recursive-image-repo) DNS_RECURSIVE_REPO="$2"; shift 2;;
    --dns-recursive-image-tag)  DNS_RECURSIVE_TAG="$2";  shift 2;;
    --kea-ctrl-agent-image-repo) KEA_CTRL_AGENT_REPO="$2"; shift 2;;
    --kea-ctrl-agent-image-tag)  KEA_CTRL_AGENT_TAG="$2";  shift 2;;
    --kea-dhcp6-image-repo)      KEA_DHCP6_REPO="$2";      shift 2;;
    --kea-dhcp6-image-tag)       KEA_DHCP6_TAG="$2";       shift 2;;
    --chart-dir) CHART_DIR="$2"; shift 2;;
    --timeout) HELM_TIMEOUT="$2"; shift 2;;
    --no-wait) WAIT=false; shift;;
    --skip-dns) SKIP_DNS=true; shift;;
    --skip-dhcp) SKIP_DHCP=true; shift;;
    -h|--help) usage;;
    *) err "unknown flag: $1 (try --help)";;
  esac
done

# ─── validate ─────────────────────────────────────────────────────────
[[ -n "$SITE_CODE" ]]      || err "--site-code is required"
[[ -n "$OTTER_BASE_URL" ]] || err "--otter-base-url is required"
$SKIP_DNS && $SKIP_DHCP    && err "--skip-dns and --skip-dhcp together leaves nothing to deploy"

NAMESPACE="${NAMESPACE:-dcim-site-${SITE_CODE}}"
LB_POOL_LABEL_VALUE="${LB_POOL_LABEL_VALUE:-${SITE_CODE}}"

if ! $SKIP_DNS; then
  [[ -n "$DNS_SERVER_ID" ]]                || err "--dns-server-id is required when DNS is enabled (or pass --skip-dns)"
  [[ -n "$DNS_SERVER_NAME" ]]              || DNS_SERVER_NAME="${SITE_CODE}-dns"
  (( ${#DNS_ANYCAST_IPS[@]} >= 1 ))        || err "at least one --dns-anycast-ip required (or --skip-dns)"
  [[ "$DNS_ROLE" == recursive || "$DNS_ROLE" == auth ]] || err "--dns-role must be recursive|auth"
  [[ -n "$OTTER_TOKEN" ]]                  || err "--otter-token required for the DNS bundle puller (or --skip-dns)"
fi

if ! $SKIP_DHCP; then
  [[ -n "$DHCP_SERVER_ID" ]]          || err "--dhcp-server-id is required when DHCP is enabled (or pass --skip-dhcp)"
  [[ -n "$DHCP_SERVER_NAME" ]]        || DHCP_SERVER_NAME="${SITE_CODE}-dhcp"
  (( ${#DHCP_ANYCAST_IPS[@]} >= 1 )) || err "at least one --dhcp-anycast-ip required (or --skip-dhcp)"
  if [[ -z "$KEA_BASIC_PASSWORD" ]]; then
    KEA_BASIC_PASSWORD="$(openssl rand -base64 24 | tr -d '/+=' | head -c 32)"
    log "generated Kea ctrl-agent basic-auth password"
  fi
fi

if ! command -v kubectl >/dev/null; then err "kubectl not on PATH"; fi
if ! command -v helm    >/dev/null; then err "helm not on PATH";    fi

CHART_DIR="${CHART_DIR:-$(cd "$(dirname "$0")/../../helm" && pwd)}"
[[ -d "$CHART_DIR/dns-site" && -d "$CHART_DIR/dhcp-site" ]] \
  || err "chart dir missing dns-site or dhcp-site: $CHART_DIR"

# ─── execute ──────────────────────────────────────────────────────────
log "namespace: $NAMESPACE"
log "site-code: $SITE_CODE"
log "LB pool selector: ${LB_POOL_LABEL_KEY}=${LB_POOL_LABEL_VALUE}"

kubectl get ns "$NAMESPACE" >/dev/null 2>&1 \
  || kubectl create namespace "$NAMESPACE"

if ! $SKIP_DNS; then
  log "=== DNS ($DNS_ROLE) ==="
  kubectl -n "$NAMESPACE" create secret generic dcim-dns-site-token \
    --from-literal=token="$OTTER_TOKEN" \
    --dry-run=client -o yaml | kubectl apply -f -

  DNS_ANYCAST_SET=""
  for i in "${!DNS_ANYCAST_IPS[@]}"; do
    DNS_ANYCAST_SET+=" --set service.anycastIPs[$i]=${DNS_ANYCAST_IPS[$i]}"
  done

  DNS_IMG_SET=""
  [[ -n "$DNS_COLLECTOR_REPO" ]] && DNS_IMG_SET+=" --set image.collector.repository=$DNS_COLLECTOR_REPO"
  [[ -n "$DNS_COLLECTOR_TAG"  ]] && DNS_IMG_SET+=" --set image.collector.tag=$DNS_COLLECTOR_TAG"
  [[ -n "$DNS_AUTH_REPO"      ]] && DNS_IMG_SET+=" --set image.auth.repository=$DNS_AUTH_REPO"
  [[ -n "$DNS_AUTH_TAG"       ]] && DNS_IMG_SET+=" --set image.auth.tag=$DNS_AUTH_TAG"
  [[ -n "$DNS_RECURSIVE_REPO" ]] && DNS_IMG_SET+=" --set image.recursive.repository=$DNS_RECURSIVE_REPO"
  [[ -n "$DNS_RECURSIVE_TAG"  ]] && DNS_IMG_SET+=" --set image.recursive.tag=$DNS_RECURSIVE_TAG"

  helm upgrade --install "dns-${DNS_SERVER_NAME}" "$CHART_DIR/dns-site" \
    -n "$NAMESPACE" \
    --set server.id="$DNS_SERVER_ID" \
    --set server.name="$DNS_SERVER_NAME" \
    --set server.role="$DNS_ROLE" \
    --set server.fabricId="$DNS_FABRIC_ID" \
    --set server.siteId="$DNS_SITE_ID" \
    --set "service.labels.${LB_POOL_LABEL_KEY//./\\.}=$LB_POOL_LABEL_VALUE" \
    --set service.labels."dcim\\.io/dns-role"="$DNS_ROLE" \
    --set bundle.apiBaseUrl="$OTTER_BASE_URL" \
    $DNS_ANYCAST_SET \
    $DNS_IMG_SET \
    --wait=$WAIT --timeout "$HELM_TIMEOUT"
fi

if ! $SKIP_DHCP; then
  log "=== DHCP ==="
  # Minimal Kea ctrl-agent config so the chart can come up; operator
  # replaces with site-specific subnets via `kubectl edit cm`.
  cat <<KEA_CFG | kubectl -n "$NAMESPACE" apply -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: kea-ctrl-agent-config
data:
  kea-ctrl-agent.conf: |
    {
      "Control-agent": {
        "http-host": "0.0.0.0",
        "http-port": 8000,
        "control-sockets": {
          "dhcp6": {
            "socket-type": "unix",
            "socket-name": "/tmp/kea-dhcp6-ctrl.sock"
          }
        },
        "loggers": [
          {
            "name": "kea-ctrl-agent",
            "severity": "INFO",
            "output_options": [{ "output": "stdout" }]
          }
        ]
      }
    }
KEA_CFG

  # Kea basic-auth file format: user,bcrypt-hash
  # Use stock httpasswd-equivalent via python3's crypt; fall back to
  # bcrypt via openssl if crypt absent.
  KEA_HASH=$(python3 -c "import crypt;print(crypt.crypt('$KEA_BASIC_PASSWORD', crypt.mksalt(crypt.METHOD_SHA512)))" 2>/dev/null \
              || openssl passwd -6 "$KEA_BASIC_PASSWORD")
  kubectl -n "$NAMESPACE" create secret generic kea-ctrl-agent-auth \
    --from-literal=auth.csv="${KEA_BASIC_USER},${KEA_HASH}" \
    --dry-run=client -o yaml | kubectl apply -f -

  DHCP_ANYCAST_SET=""
  for i in "${!DHCP_ANYCAST_IPS[@]}"; do
    DHCP_ANYCAST_SET+=" --set service.anycastIPs[$i]=${DHCP_ANYCAST_IPS[$i]}"
  done

  DHCP_IMG_SET=""
  [[ -n "$KEA_CTRL_AGENT_REPO" ]] && DHCP_IMG_SET+=" --set image.ctrlAgent.repository=$KEA_CTRL_AGENT_REPO"
  [[ -n "$KEA_CTRL_AGENT_TAG"  ]] && DHCP_IMG_SET+=" --set image.ctrlAgent.tag=$KEA_CTRL_AGENT_TAG"
  [[ -n "$KEA_DHCP6_REPO"      ]] && DHCP_IMG_SET+=" --set image.dhcp6.repository=$KEA_DHCP6_REPO"
  [[ -n "$KEA_DHCP6_TAG"       ]] && DHCP_IMG_SET+=" --set image.dhcp6.tag=$KEA_DHCP6_TAG"

  helm upgrade --install "dhcp-${DHCP_SERVER_NAME}" "$CHART_DIR/dhcp-site" \
    -n "$NAMESPACE" \
    --set server.id="$DHCP_SERVER_ID" \
    --set server.name="$DHCP_SERVER_NAME" \
    --set server.fabricId="$DHCP_FABRIC_ID" \
    --set server.dhcpv6="$DHCP_V6" \
    --set "service.labels.${LB_POOL_LABEL_KEY//./\\.}=$LB_POOL_LABEL_VALUE" \
    $DHCP_ANYCAST_SET \
    $DHCP_IMG_SET \
    --wait=$WAIT --timeout "$HELM_TIMEOUT"
fi

log "=== done ==="
kubectl -n "$NAMESPACE" get pods,svc 2>&1
