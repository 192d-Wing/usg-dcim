#Requires -PSEdition Core
[CmdletBinding()]
param(
    [string]$Address = '0.0.0.0'
)
$ErrorActionPreference = 'Stop'

Write-Host "Starting port-forwards bound to $Address..."
Write-Host ""
Write-Host "  Port 80  → nginx-proxy  HTTP  (→ HTTPS redirect)"
Write-Host "  Port 443 → nginx-proxy  HTTPS (dcim, keycloak, dns DoH)"
Write-Host "  Port 53  → nginx-proxy  DNS TCP (→ Hickory)"
Write-Host ""
Write-Host "Access:"
Write-Host "  https://dcim.prod.dev.mil/"
Write-Host "  https://keycloak.prod.dev.mil/"
Write-Host "  https://dns.prod.dev.mil/      (DoH)"
Write-Host "  dns.prod.dev.mil:53 TCP        (plain DNS)"
Write-Host ""
Write-Host "Note: UDP DNS (port 53) is not proxied — use DoH at port 443."
Write-Host ""
Write-Host "Press Ctrl+C to stop."

$pf = Start-Process kubectl `
    -ArgumentList "port-forward --address $Address svc/nginx-proxy 80:80 443:443 53:53 -n dcim" `
    -PassThru -NoNewWindow

try {
    while ($true) { Start-Sleep -Seconds 5 }
} finally {
    Stop-Process -Id $pf.Id -Force -ErrorAction SilentlyContinue
    Write-Host "Port-forward stopped."
}
