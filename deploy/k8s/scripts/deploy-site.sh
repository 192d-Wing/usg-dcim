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

Config file (optional):
  --config <path>            YAML file with the variables below. See
                             deploy/k8s/scripts/sites/example.yaml.
                             CLI flags override values in the file.

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

# ─── config file (first pass: pick up --config so flags can override) ─
CONFIG_FILE=""
KUBECONFIG_FROM_CONFIG=""
KUBECTL_CONTEXT=""
CLUSTER_HOSTNAME=""
for ((i=1; i<=$#; i++)); do
  if [[ "${!i}" == "--config" ]]; then
    j=$((i+1))
    [[ $j -gt $# ]] && err "--config requires a path"
    CONFIG_FILE="${!j}"
    break
  fi
done
if [[ -n "$CONFIG_FILE" ]]; then
  [[ -f "$CONFIG_FILE" ]] || err "config file not found: $CONFIG_FILE"
  command -v python3 >/dev/null || err "python3 required to parse --config YAML"
  # Python emits shell-safe `KEY=value` lines for scalars and
  # bash array literals for repeating fields. Anything blank/null is
  # skipped so subsequent flag-parsing keeps the script's defaults.
  CONFIG_ENV="$(python3 - "$CONFIG_FILE" <<'PY' 2>&1
import sys, shlex, yaml
with open(sys.argv[1]) as f:
    d = yaml.safe_load(f) or {}
def q(v): return shlex.quote(str(v))
def out(k, v):
    if v is None: return
    if isinstance(v, bool):
        v = "true" if v else "false"
    if isinstance(v, str) and v == "":
        return
    print(f"{k}={q(v)}")
def arr(k, v):
    if not v: return
    items = " ".join(q(str(x)) for x in v)
    print(f"{k}=({items})")
c = d.get("cluster", {}) or {}
out("CLUSTER_HOSTNAME",     c.get("hostname"))
out("KUBECONFIG_FROM_CONFIG", c.get("kubeconfig"))
out("KUBECTL_CONTEXT",      c.get("context"))
s = d.get("site", {}) or {}
out("SITE_CODE",            s.get("code"))
out("NAMESPACE",            s.get("namespace"))
lb = s.get("lbPoolLabel", {}) or {}
out("LB_POOL_LABEL_KEY",    lb.get("key"))
out("LB_POOL_LABEL_VALUE",  lb.get("value"))
o = d.get("otter", {}) or {}
out("OTTER_BASE_URL",       o.get("baseUrl"))
out("OTTER_TOKEN",          o.get("token"))
out("OTTER_TOKEN_FILE",     o.get("tokenFile"))
dns = d.get("dns", {}) or {}
if dns.get("enabled") is False:
    print("SKIP_DNS=true")
out("DNS_SERVER_ID",        dns.get("serverId"))
out("DNS_SERVER_NAME",      dns.get("serverName"))
out("DNS_ROLE",             dns.get("role"))
out("DNS_FABRIC_ID",        dns.get("fabricId"))
out("DNS_SITE_ID",          dns.get("siteId"))
arr("DNS_ANYCAST_IPS",      dns.get("anycastIps"))
img = dns.get("image", {}) or {}
for sub, prefix in (("collector","DNS_COLLECTOR"), ("auth","DNS_AUTH"), ("recursive","DNS_RECURSIVE")):
    si = img.get(sub, {}) or {}
    out(f"{prefix}_REPO", si.get("repository"))
    out(f"{prefix}_TAG",  si.get("tag"))
dhcp = d.get("dhcp", {}) or {}
if dhcp.get("enabled") is False:
    print("SKIP_DHCP=true")
out("DHCP_SERVER_ID",       dhcp.get("serverId"))
out("DHCP_SERVER_NAME",     dhcp.get("serverName"))
out("DHCP_FABRIC_ID",       dhcp.get("fabricId"))
arr("DHCP_ANYCAST_IPS",     dhcp.get("anycastIps"))
if dhcp.get("v6") is True:
    print("DHCP_V6=true")
kba = dhcp.get("keaBasicAuth", {}) or {}
out("KEA_BASIC_USER",       kba.get("username"))
out("KEA_BASIC_PASSWORD",   kba.get("password"))
img = dhcp.get("image", {}) or {}
for sub, prefix in (("ctrlAgent","KEA_CTRL_AGENT"), ("dhcp6","KEA_DHCP6")):
    si = img.get(sub, {}) or {}
    out(f"{prefix}_REPO", si.get("repository"))
    out(f"{prefix}_TAG",  si.get("tag"))
h = d.get("helm", {}) or {}
out("HELM_TIMEOUT",         h.get("timeout"))
if h.get("wait") is False:
    print("WAIT=false")
PY
)"
  if echo "$CONFIG_ENV" | grep -q "^Traceback\|YAMLError"; then
    err "failed to parse config: $CONFIG_ENV"
  fi
  eval "$CONFIG_ENV"
  # Resolve tokenFile if no inline token was given
  if [[ -z "${OTTER_TOKEN:-}" && -n "${OTTER_TOKEN_FILE:-}" ]]; then
    [[ -r "$OTTER_TOKEN_FILE" ]] || err "otter.tokenFile unreadable: $OTTER_TOKEN_FILE"
    OTTER_TOKEN="$(head -n1 "$OTTER_TOKEN_FILE")"
  fi
  # Pick up kubeconfig from the config file (CLI --kubeconfig wins later if added).
  if [[ -n "$KUBECONFIG_FROM_CONFIG" ]]; then
    # Expand leading ~ to $HOME (yaml-loaded value is literal).
    KUBECONFIG_FROM_CONFIG="${KUBECONFIG_FROM_CONFIG/#~/$HOME}"
    export KUBECONFIG="$KUBECONFIG_FROM_CONFIG"
  fi
fi

# ─── arg parse (flags win over --config) ──────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --config) shift 2;;   # already consumed above
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
if [[ -n "$KUBECTL_CONTEXT" ]]; then
  kubectl config use-context "$KUBECTL_CONTEXT" >/dev/null \
    || err "kubectl context not found: $KUBECTL_CONTEXT"
fi
log "namespace: $NAMESPACE"
log "site-code: $SITE_CODE"
[[ -n "$CLUSTER_HOSTNAME" ]] && log "target cluster: $CLUSTER_HOSTNAME"
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
