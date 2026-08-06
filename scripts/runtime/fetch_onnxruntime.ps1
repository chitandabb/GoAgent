[CmdletBinding()]
param(
    [ValidateSet("windows-x64", "linux-x64")]
    [string]$Platform = $(if ($env:OS -eq "Windows_NT") { "windows-x64" } else { "linux-x64" }),
    [string]$OutputRoot = "output/runtime",
    [switch]$ForceDownload
)

$ErrorActionPreference = "Stop"

$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot "../..")).Path
$manifestPath = Join-Path $repositoryRoot "config/runtime/onnxruntime.json"
$manifest = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json
$artifact = $manifest.platforms.$Platform
if ($null -eq $artifact) {
    throw "unsupported ONNX Runtime platform: $Platform"
}

$resolvedOutputRoot = [System.IO.Path]::GetFullPath((Join-Path $repositoryRoot $OutputRoot))
$archivePath = Join-Path $resolvedOutputRoot $artifact.archive
$libraryPath = Join-Path $resolvedOutputRoot $artifact.libraryPath
New-Item -ItemType Directory -Force -Path $resolvedOutputRoot | Out-Null

if ($ForceDownload -or -not (Test-Path -LiteralPath $archivePath)) {
    Invoke-WebRequest -UseBasicParsing -Uri $artifact.url -OutFile $archivePath
}
$actualBytes = (Get-Item -LiteralPath $archivePath).Length
$actualSHA256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $archivePath).Hash.ToLowerInvariant()
if ($actualBytes -ne $artifact.bytes -or $actualSHA256 -ne $artifact.sha256) {
    throw "ONNX Runtime archive verification failed for $Platform"
}

if (-not (Test-Path -LiteralPath $libraryPath)) {
    if ($artifact.format -eq "zip") {
        Expand-Archive -LiteralPath $archivePath -DestinationPath $resolvedOutputRoot -Force
    } elseif ($artifact.format -eq "tgz") {
        & tar -xzf $archivePath -C $resolvedOutputRoot $artifact.libraryPath
        if ($LASTEXITCODE -ne 0) {
            throw "failed to extract the ONNX Runtime tgz archive"
        }
    } else {
        throw "unsupported ONNX Runtime archive format: $($artifact.format)"
    }
}

if (-not (Test-Path -LiteralPath $libraryPath)) {
    throw "ONNX Runtime library is missing after extraction: $libraryPath"
}

$actualLibraryBytes = (Get-Item -LiteralPath $libraryPath).Length
$actualLibrarySHA256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $libraryPath).Hash.ToLowerInvariant()
if ($actualLibraryBytes -ne $artifact.libraryBytes -or $actualLibrarySHA256 -ne $artifact.librarySHA256) {
    throw "ONNX Runtime extracted library verification failed for $Platform"
}
foreach ($notice in $artifact.noticeFiles) {
    $noticePath = Join-Path $resolvedOutputRoot $notice.path
    if (-not (Test-Path -LiteralPath $noticePath)) {
        throw "ONNX Runtime notice file is missing: $noticePath"
    }
    $actualNoticeBytes = (Get-Item -LiteralPath $noticePath).Length
    $actualNoticeSHA256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $noticePath).Hash.ToLowerInvariant()
    if ($actualNoticeBytes -ne $notice.bytes -or $actualNoticeSHA256 -ne $notice.sha256) {
        throw "ONNX Runtime notice verification failed for $($notice.path)"
    }
}

Write-Host "ONNX Runtime $($manifest.version) prepared for $Platform"
Write-Host "Library: $libraryPath"
