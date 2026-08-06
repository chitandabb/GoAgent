[CmdletBinding()]
param(
    [string]$ModelRoot = "output/models/pp-doclayout-m",
    [string]$RuntimeRoot = "output/runtime",
    [string]$OutputRoot = "output/docker/knowledge-worker-assets"
)

$ErrorActionPreference = "Stop"

$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot "../..")).Path
$repositoryOutput = [IO.Path]::GetFullPath((Join-Path $repositoryRoot "output"))

function Resolve-RepositoryPath([string]$Value) {
    if ([IO.Path]::IsPathRooted($Value)) {
        return [IO.Path]::GetFullPath($Value)
    }
    return [IO.Path]::GetFullPath((Join-Path $repositoryRoot $Value))
}

function Assert-File([string]$Path, [int64]$Bytes, [string]$SHA256, [string]$Label) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "$Label is missing: $Path"
    }
    $actualBytes = (Get-Item -LiteralPath $Path).Length
    $actualSHA256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash.ToLowerInvariant()
    if ($actualBytes -ne $Bytes -or $actualSHA256 -ne $SHA256) {
        throw "$Label failed byte length or SHA-256 verification"
    }
}

$modelManifest = Get-Content -Raw -LiteralPath (Join-Path $repositoryRoot "config/models/pp-doclayout-m.json") | ConvertFrom-Json
$runtimeManifest = Get-Content -Raw -LiteralPath (Join-Path $repositoryRoot "config/runtime/onnxruntime.json") | ConvertFrom-Json
$linuxRuntime = $runtimeManifest.platforms."linux-x64"
$modelRootPath = Resolve-RepositoryPath $ModelRoot
$runtimeRootPath = Resolve-RepositoryPath $RuntimeRoot
$outputRootPath = Resolve-RepositoryPath $OutputRoot
if (-not $outputRootPath.StartsWith($repositoryOutput + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
    throw "OutputRoot must stay under $repositoryOutput"
}

$modelPath = Join-Path $modelRootPath $modelManifest.conversion.outputFile
$modelReadme = Join-Path (Join-Path $modelRootPath "source") "README.md"
$modelReadmeContract = $modelManifest.source.files | Where-Object { $_.name -eq "README.md" } | Select-Object -First 1
$runtimeLibrary = Join-Path $runtimeRootPath $linuxRuntime.libraryPath
$runtimeLicenseContract = $linuxRuntime.noticeFiles | Where-Object { $_.path -like "*/LICENSE" } | Select-Object -First 1
$runtimeNoticesContract = $linuxRuntime.noticeFiles | Where-Object { $_.path -like "*/ThirdPartyNotices.txt" } | Select-Object -First 1
$runtimeLicense = Join-Path $runtimeRootPath $runtimeLicenseContract.path
$runtimeNotices = Join-Path $runtimeRootPath $runtimeNoticesContract.path

Assert-File $modelPath $modelManifest.conversion.bytes $modelManifest.conversion.sha256 "PP-DocLayout-M ONNX model"
Assert-File $modelReadme $modelReadmeContract.bytes $modelReadmeContract.sha256 "PP-DocLayout-M README/license metadata"
Assert-File $runtimeLibrary $linuxRuntime.libraryBytes $linuxRuntime.librarySHA256 "ONNX Runtime Linux library"
Assert-File $runtimeLicense $runtimeLicenseContract.bytes $runtimeLicenseContract.sha256 "ONNX Runtime license"
Assert-File $runtimeNotices $runtimeNoticesContract.bytes $runtimeNoticesContract.sha256 "ONNX Runtime third-party notices"

New-Item -ItemType Directory -Force -Path $outputRootPath | Out-Null
$allowedNames = @(
    "pp-doclayout-m.onnx",
    "libonnxruntime.so",
    "asset-manifest.json",
    "MODEL_README.md",
    "ONNXRUNTIME_LICENSE",
    "ONNXRUNTIME_THIRD_PARTY_NOTICES"
)
$unexpected = Get-ChildItem -Force -LiteralPath $outputRootPath | Where-Object { $_.Name -notin $allowedNames }
if ($unexpected) {
    throw "OutputRoot contains unexpected files; refusing to package: $($unexpected.Name -join ', ')"
}

Copy-Item -Force -LiteralPath $modelPath -Destination (Join-Path $outputRootPath "pp-doclayout-m.onnx")
Copy-Item -Force -LiteralPath $runtimeLibrary -Destination (Join-Path $outputRootPath "libonnxruntime.so")
Copy-Item -Force -LiteralPath $modelReadme -Destination (Join-Path $outputRootPath "MODEL_README.md")
Copy-Item -Force -LiteralPath $runtimeLicense -Destination (Join-Path $outputRootPath "ONNXRUNTIME_LICENSE")
Copy-Item -Force -LiteralPath $runtimeNotices -Destination (Join-Path $outputRootPath "ONNXRUNTIME_THIRD_PARTY_NOTICES")

$assetManifest = [ordered]@{
    schemaVersion = 1
    model = [ordered]@{
        name = $modelManifest.name
        version = $modelManifest.source.revision
        bytes = $modelManifest.conversion.bytes
        sha256 = $modelManifest.conversion.sha256
        license = $modelManifest.license
    }
    runtime = [ordered]@{
        name = $runtimeManifest.name
        version = $runtimeManifest.version
        platform = "linux-x64"
        bytes = $linuxRuntime.libraryBytes
        sha256 = $linuxRuntime.librarySHA256
    }
}
$assetManifest | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $outputRootPath "asset-manifest.json") -Encoding utf8

Assert-File (Join-Path $outputRootPath "pp-doclayout-m.onnx") $modelManifest.conversion.bytes $modelManifest.conversion.sha256 "staged model"
Assert-File (Join-Path $outputRootPath "libonnxruntime.so") $linuxRuntime.libraryBytes $linuxRuntime.librarySHA256 "staged runtime"

Write-Host "Knowledge Worker assets prepared: $outputRootPath"
Write-Host "Model: $($modelManifest.conversion.bytes) bytes, $($modelManifest.conversion.sha256)"
Write-Host "Runtime: $($linuxRuntime.libraryBytes) bytes, $($linuxRuntime.librarySHA256)"
