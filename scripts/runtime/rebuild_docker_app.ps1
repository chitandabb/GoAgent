[CmdletBinding()]
param(
    [string]$Image = $env:MESGUARD_APP_IMAGE
)

$ErrorActionPreference = "Stop"
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot "../..")).Path
$services = @(
    "searxng",
    "migrate",
    "backend",
    "outbox-relay",
    "diagnosis-worker",
    "conversation-worker",
    "knowledge-worker",
    "memory-worker"
)
$containers = @(
    "mesguard-api",
    "mesguard-outbox-relay",
    "mesguard-diagnosis-worker",
    "mesguard-conversation-worker",
    "mesguard-knowledge-worker",
    "mesguard-memory-worker"
)

if ([string]::IsNullOrWhiteSpace($Image)) {
    $Image = "mesguard-app:latest"
}
$env:MESGUARD_APP_IMAGE = $Image

Push-Location $repositoryRoot
try {
    & docker compose build backend
    if ($LASTEXITCODE -ne 0) {
        throw "Docker application image build failed with exit code $LASTEXITCODE"
    }

    & docker compose --profile web-search-self-hosted up -d --no-build --force-recreate @services
    if ($LASTEXITCODE -ne 0) {
        throw "Docker application container recreation failed with exit code $LASTEXITCODE"
    }

    $deadline = (Get-Date).AddSeconds(60)
    do {
        Start-Sleep -Seconds 2
        try {
            $health = Invoke-RestMethod -Uri "http://127.0.0.1:9090/healthz" -TimeoutSec 5
        } catch {
            $health = $null
        }
    } while ($health.data.status -ne "ok" -and (Get-Date) -lt $deadline)
    if ($health.data.status -ne "ok") {
        throw "MESGuard API did not become healthy within 60 seconds"
    }

    $imageIDs = @($containers | ForEach-Object {
        & docker inspect $_ --format '{{.Image}}'
        if ($LASTEXITCODE -ne 0) {
            throw "Could not inspect container $_"
        }
    } | Select-Object -Unique)
    if ($imageIDs.Count -ne 1) {
        throw "Application containers do not use one image: $($imageIDs -join ', ')"
    }

    Write-Host "MESGuard application redeployed with image $Image"
    Write-Host "Container image ID: $($imageIDs[0])"
    Write-Host "API: http://127.0.0.1:9090"
} finally {
    Pop-Location
}
