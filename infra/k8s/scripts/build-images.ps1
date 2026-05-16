#Requires -PSEdition Core
[CmdletBinding()]
param(
    [string]$KindCluster = 'kind-cluster'
)
$ErrorActionPreference = 'Stop'

$root = Resolve-Path (Join-Path $PSScriptRoot '../../..')
Push-Location $root

$images = @(
    @{ Name = 'dcim-api';          Context = 'backend' },
    @{ Name = 'dcim-frontend';     Context = 'frontend' },
    @{ Name = 'dcim-go-collector'; Context = 'packages/badger' },
    @{ Name = 'dcim-go-ingest';    Context = 'packages/heron' },
    @{ Name = 'dcim-go-alerts';    Context = 'packages/magpie' },
    @{ Name = 'dcim-go-dns-probe'; Context = 'packages/beagle' }
)

try {
    foreach ($img in $images) {
        $tag = "$($img.Name):dev"
        Write-Host "Building $tag from $($img.Context)..."
        podman build -t $tag $img.Context
        if (-not $?) { throw "Failed to build $tag" }
    }

    Write-Host ""
    Write-Host "Loading images into Kind cluster '$KindCluster'..."
    Write-Host "(Podman tags images as localhost/<name>:dev — saving to tar and loading into Kind)"

    $tmpDir = [System.IO.Path]::GetTempPath()
    foreach ($img in $images) {
        $tag  = "$($img.Name):dev"
        $tar  = Join-Path $tmpDir "$($img.Name).tar"
        Write-Host "  Loading $tag..."
        podman save $tag -o $tar
        $env:KIND_EXPERIMENTAL_PROVIDER = 'podman'
        kind load image-archive $tar --name $KindCluster
        Remove-Item $tar -Force
    }

    Write-Host ""
    Write-Host "All images built and loaded into Kind cluster."
    Write-Host ""
    Write-Host "Next: kubectl apply -k infra/k8s/central/"
} finally {
    Pop-Location
}
