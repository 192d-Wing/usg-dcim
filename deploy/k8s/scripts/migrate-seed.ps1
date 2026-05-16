#Requires -PSEdition Core
[CmdletBinding()]
param()
$ErrorActionPreference = 'Stop'

Write-Host "Waiting for API pod to be ready..."
kubectl wait --for=condition=ready pod -l app=api -n dcim --timeout=120s
if (-not $?) { throw "API pod did not become ready in time" }

Write-Host "Running database migrations..."
kubectl exec -n dcim deploy/api -- alembic upgrade head
if (-not $?) { throw "Migration failed" }

Write-Host "Seeding demo data..."
kubectl exec -n dcim deploy/api -- python -m dcim.scripts.seed_demo
if (-not $?) { throw "Seeding failed" }

Write-Host ""
Write-Host "Migrations and seeding complete!"
Write-Host ""
Write-Host "Demo sites created:"
Write-Host "  - CONUS-001, CONUS-002"
Write-Host "  - EUCOM-001, EUCOM-002"
Write-Host "  - INDOPACOM-001, INDOPACOM-002"
Write-Host ""
Write-Host "Admin user: admin@dcim.local / changeme"
Write-Host ""
Write-Host "Next: .\infra\k8s\scripts\enroll-site.ps1 CONUS-001"
