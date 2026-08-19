[CmdletBinding()]
param(
    [string]$Image = $env:MESGUARD_APP_IMAGE,
    [string]$KnowledgeWorkerImage = $env:MESGUARD_KNOWLEDGE_WORKER_IMAGE
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
$baseContainers = @(
    "mesguard-api",
    "mesguard-outbox-relay",
    "mesguard-diagnosis-worker",
    "mesguard-conversation-worker",
    "mesguard-memory-worker"
)
$knowledgeWorkerContainer = "mesguard-knowledge-worker"
$composeFiles = @("-f", "docker-compose.yml", "-f", "docker-compose.layout.yml")

if ([string]::IsNullOrWhiteSpace($Image)) {
    $Image = "mesguard-app:latest"
}
if ([string]::IsNullOrWhiteSpace($KnowledgeWorkerImage)) {
    $KnowledgeWorkerImage = "mesguard-knowledge-worker:latest"
}
$env:MESGUARD_APP_IMAGE = $Image
$env:MESGUARD_KNOWLEDGE_WORKER_IMAGE = $KnowledgeWorkerImage

Push-Location $repositoryRoot
try {
    & .\scripts\runtime\prepare_knowledge_worker_assets.ps1

    & docker compose @composeFiles build backend
    if ($LASTEXITCODE -ne 0) {
        throw "Docker application image build failed with exit code $LASTEXITCODE"
    }
    & docker compose @composeFiles build knowledge-worker
    if ($LASTEXITCODE -ne 0) {
        throw "Docker Knowledge Worker image build failed with exit code $LASTEXITCODE"
    }

    & docker compose @composeFiles --profile web-search-self-hosted up -d --no-build --force-recreate @services
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

    $baseImageIDs = @($baseContainers | ForEach-Object {
        & docker inspect $_ --format '{{.Image}}'
        if ($LASTEXITCODE -ne 0) {
            throw "Could not inspect container $_"
        }
    } | Select-Object -Unique)
    if ($baseImageIDs.Count -ne 1) {
        throw "Base application containers do not use one image: $($baseImageIDs -join ', ')"
    }
    $knowledgeWorkerImageID = & docker inspect $knowledgeWorkerContainer --format '{{.Image}}'
    if ($LASTEXITCODE -ne 0) {
        throw "Could not inspect container $knowledgeWorkerContainer"
    }

    Write-Host "MESGuard application redeployed with image $Image"
    Write-Host "Base application image ID: $($baseImageIDs[0])"
    Write-Host "Knowledge Worker image: $KnowledgeWorkerImage"
    Write-Host "Knowledge Worker image ID: $knowledgeWorkerImageID"
    Write-Host "API: http://127.0.0.1:9090"
} finally {
    Pop-Location
}
