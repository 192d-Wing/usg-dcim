#Requires -PSEdition Core
[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$SiteCode,

    [string]      $AdminEmail    = 'admin@dcim.local',
    [SecureString]$AdminPassword = (ConvertTo-SecureString 'changeme' -AsPlainText -Force),
    [int]         $LocalPort     = 18000
)
$ErrorActionPreference = 'Stop'

# Kind doesn't expose NodePorts to the Windows host — use port-forward.
Write-Host "Starting kubectl port-forward on localhost:$LocalPort..."
$pf = Start-Process -FilePath 'kubectl' `
    -ArgumentList "port-forward svc/api $LocalPort`:8000 -n dcim" `
    -PassThru -NoNewWindow
Start-Sleep -Seconds 3

$base = "http://localhost:$LocalPort/api/v1"

try {
    # Authenticate
    Write-Host "Authenticating as $AdminEmail..."
    $plainPw = [System.Net.NetworkCredential]::new('', $AdminPassword).Password
    $loginResp = Invoke-RestMethod -Method Post -Uri "$base/auth/login" `
        -ContentType 'application/json' `
        -Body (@{ email = $AdminEmail; password = $plainPw } | ConvertTo-Json)
    $token = $loginResp.access_token
    if (-not $token) { throw "Login failed — check admin credentials." }
    $headers = @{ Authorization = "Bearer $token"; Accept = 'application/json' }

    # Look up site
    Write-Host "Looking up site: $SiteCode"
    $resp = Invoke-RestMethod -Uri "$base/inventory/sites?q=$SiteCode" -Headers $headers
    $site = $resp.items | Where-Object { $_.code -eq $SiteCode } | Select-Object -First 1
    if (-not $site) {
        throw "Site '$SiteCode' not found. Run migrate-seed.ps1 first."
    }
    $siteId = $site.id
    Write-Host "Found site: $siteId"

    # Enroll collector
    Write-Host "Enrolling collector at $SiteCode..."
    $enroll = Invoke-RestMethod -Method Post -Uri "$base/collectors/enroll" `
        -Headers $headers -ContentType 'application/json' `
        -Body (@{
            site_id      = $siteId
            name         = "collector-$SiteCode"
            capabilities = @('snmp', 'redfish', 'modbus')
        } | ConvertTo-Json)

    $collectorId = $enroll.collector_id
    $token2      = $enroll.enrollment_token
    $expires     = if ($enroll.expires_in_seconds) { $enroll.expires_in_seconds } else { 3600 }

    if (-not $collectorId -or -not $token2) {
        throw "Enrollment response missing fields: $($enroll | ConvertTo-Json)"
    }

    Write-Host ""
    Write-Host "Enrollment successful!"
    Write-Host ""
    Write-Host "  COLLECTOR_ID:     $collectorId"
    Write-Host "  ENROLLMENT_TOKEN: $token2"
    Write-Host "  SITE_ID:          $siteId"
    Write-Host "  EXPIRES_IN:       ${expires}s"
    Write-Host ""
    Write-Host "Create the site namespace + secret:"
    Write-Host ""
    Write-Host "  kubectl create namespace dcim-site1"
    Write-Host "  kubectl create secret generic collector-enrollment -n dcim-site1 ``"
    Write-Host "    --from-literal=token=$token2 ``"
    Write-Host "    --from-literal=collector_id=$collectorId ``"
    Write-Host "    --from-literal=site_id=$siteId"
    Write-Host ""

} finally {
    Stop-Process -Id $pf.Id -Force -ErrorAction SilentlyContinue
    Write-Host "Port-forward closed."
}
