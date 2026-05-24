#Requires -PSEdition Core
[CmdletBinding()]
param(
    [string]$ProjectRoot = (Resolve-Path (Join-Path $PSScriptRoot '../../..')).Path
)
$ErrorActionPreference = 'Stop'

$tmpCrt = Join-Path $env:TEMP 'dcim-proxy.crt'
$tmpKey = Join-Path $env:TEMP 'dcim-proxy.key'

# ── 1. Self-signed wildcard cert for *.prod.dev.mil ──────────────────────────
Write-Host "Generating self-signed wildcard cert for *.prod.dev.mil..."

$san = "subjectAltName=DNS:*.prod.dev.mil," +
       "DNS:dcim.prod.dev.mil," +
       "DNS:keycloak.prod.dev.mil," +
       "DNS:dns.prod.dev.mil," +
       "IP:10.0.100.35"

openssl req -x509 -nodes -days 825 -newkey rsa:2048 `
    -keyout $tmpKey -out $tmpCrt `
    -subj "/CN=*.prod.dev.mil/O=USG DCIM Dev" `
    -addext $san 2>&1
if (-not $?) { throw "openssl failed — ensure openssl is on PATH" }

Write-Host "Creating TLS secret nginx-proxy-tls in namespace dcim..."
kubectl delete secret nginx-proxy-tls -n dcim --ignore-not-found 2>&1 | Out-Null
kubectl create secret tls nginx-proxy-tls -n dcim --cert=$tmpCrt --key=$tmpKey
if (-not $?) { throw "Failed to create nginx-proxy-tls secret" }

# ── 2. Collector API token secret for dcim-site42 ────────────────────────────
$tokenPath = Join-Path $ProjectRoot 'deploy/docker/site-dns/token'
if (-not (Test-Path $tokenPath)) {
    Write-Warning "Token file not found at $tokenPath — skipping collector-token secret."
    Write-Warning "Run enroll-site.ps1 and copy the token to deploy/docker/site-dns/token first."
} else {
    Write-Host "Creating collector-token secret in namespace dcim-site42..."
    kubectl create namespace dcim-site42 --dry-run=client -o yaml | kubectl apply -f - 2>&1 | Out-Null
    kubectl delete secret collector-token -n dcim-site42 --ignore-not-found 2>&1 | Out-Null
    kubectl create secret generic collector-token -n dcim-site42 --from-file=token=$tokenPath
    if (-not $?) { throw "Failed to create collector-token secret" }
}

# ── 3. Hickory TLS cert secret for dcim-site42 ───────────────────────────────
$site42Crt = Join-Path $ProjectRoot 'deploy/docker/site-dns/tls/tls.crt.pem'
$site42Key = Join-Path $ProjectRoot 'deploy/docker/site-dns/tls/tls.key.pem'
if (-not (Test-Path $site42Crt) -or -not (Test-Path $site42Key)) {
    Write-Warning "Site42 TLS certs not found — skipping hickory-tls secret."
    Write-Warning "Expected: deploy/docker/site-dns/tls/tls.crt.pem + tls.key.pem"
} else {
    Write-Host "Creating hickory-tls secret in namespace dcim-site42..."
    kubectl delete secret hickory-tls -n dcim-site42 --ignore-not-found 2>&1 | Out-Null
    kubectl create secret generic hickory-tls -n dcim-site42 `
        --from-file=tls.crt.pem=$site42Crt `
        --from-file=tls.key.pem=$site42Key
    if (-not $?) { throw "Failed to create hickory-tls secret" }
}

# ── Done ─────────────────────────────────────────────────────────────────────
Write-Host ""
Write-Host "All secrets created."
Write-Host ""
Write-Host "To trust the self-signed cert on Windows (run as Admin):"
Write-Host "  certutil -addstore Root $tmpCrt"
Write-Host ""
Write-Host "The cert file is at: $tmpCrt"
Write-Host "(Import it before rebooting — TEMP files may be cleaned up)"
Write-Host ""
Write-Host "Next steps:"
Write-Host "  1. kubectl apply -k deploy/k8s/central/"
Write-Host "  2. Stop Docker Compose site42 (docker compose ... down)"
Write-Host "  3. kubectl apply -k deploy/k8s/site42/"
Write-Host "  4. .\infra\k8s\scripts\port-forward.ps1"
