[CmdletBinding()]
param(
    [string]$CorpusRoot = "output/evaluation/layout-routing-corpus",
    [string]$AssetRoot = "output/docker/knowledge-worker-assets",
    [string]$OutputRoot = "output/evaluation/linux-layout-routing",
    [string]$Image = "mesguard/knowledge-worker-eval:local",
    [double]$CPUs = 2,
    [string]$Memory = "2g"
)

$ErrorActionPreference = "Stop"

$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot "../..")).Path
$evaluationRoot = [IO.Path]::GetFullPath((Join-Path $repositoryRoot "output/evaluation"))

function Resolve-RepositoryPath([string]$Value) {
    if ([IO.Path]::IsPathRooted($Value)) {
        return [IO.Path]::GetFullPath($Value)
    }
    return [IO.Path]::GetFullPath((Join-Path $repositoryRoot $Value))
}

$corpusRootPath = Resolve-RepositoryPath $CorpusRoot
$assetRootPath = Resolve-RepositoryPath $AssetRoot
$outputRootPath = Resolve-RepositoryPath $OutputRoot
if (-not $outputRootPath.StartsWith($evaluationRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
    throw "OutputRoot must stay under $evaluationRoot"
}
if ($CPUs -le 0 -or [string]::IsNullOrWhiteSpace($Memory)) {
    throw "container CPU and memory limits are required"
}

New-Item -ItemType Directory -Force -Path $outputRootPath | Out-Null
foreach ($name in @("time-v.txt", "observations.jsonl", "summary.json", "resources.json")) {
    $generatedPath = Join-Path $outputRootPath $name
    if (Test-Path -LiteralPath $generatedPath -PathType Leaf) {
        Remove-Item -Force -LiteralPath $generatedPath
    }
}

$buildArguments = @(
    "build",
    "--file", "Dockerfile.knowledge-worker",
    "--target", "evaluation",
    "--build-context", "knowledge_assets=$assetRootPath",
    "--tag", $Image,
    "."
)
& docker @buildArguments
if ($LASTEXITCODE -ne 0) {
    throw "failed to build the Knowledge Worker Linux evaluation image"
}

$timePath = Join-Path $outputRootPath "time-v.txt"
$runArguments = @(
    "run", "--rm",
    "--network", "none",
    "--read-only",
    "--tmpfs", "/tmp:rw,noexec,nosuid,size=256m",
    "--cap-drop", "ALL",
    "--security-opt", "no-new-privileges",
    "--pids-limit", "256",
    "--cpus", $CPUs,
    "--memory", $Memory,
    "--mount", "type=bind,source=$corpusRootPath,target=/corpus,readonly",
    "--mount", "type=bind,source=$outputRootPath,target=/output",
    $Image,
    "/usr/bin/time", "-v", "-o", "/output/time-v.txt",
    "/app/mesguard-layout-routing-eval",
    "-config", "/app/config/mesguard.docker.toml",
    "-corpus", "/app/testdata/layout-routing-public-v1.corpus.json",
    "-cases", "/app/testdata/layout-routing-public-v1.jsonl",
    "-root", "/corpus",
    "-model", "/app/models/pp-doclayout-m.onnx",
    "-manifest", "/app/config/models/pp-doclayout-m.json",
    "-runtime", "/app/runtime/libonnxruntime.so",
    "-output", "/output/observations.jsonl",
    "-summary", "/output/summary.json",
    "-max-raster-pixels", "8000000",
    "-intra-op-threads", "2",
    "-inter-op-threads", "1",
    "-timeout", "10m"
)
$stopwatch = [Diagnostics.Stopwatch]::StartNew()
& docker @runArguments
$exitCode = $LASTEXITCODE
$stopwatch.Stop()
if ($exitCode -ne 0) {
    throw "Linux layout routing evaluation failed with exit code $exitCode"
}

$timeLines = Get-Content -LiteralPath $timePath
function Read-TimeMetric([string]$Label) {
    $line = $timeLines | Where-Object { $_ -match "^\s*$([regex]::Escape($Label)):\s*(.+)$" } | Select-Object -First 1
    if (-not $line) {
        throw "missing GNU time metric: $Label"
    }
    return ([regex]::Match($line, ":\s*(.+)$")).Groups[1].Value.Trim()
}

$imageSize = [int64](& docker image inspect $Image --format "{{.Size}}")
if ($LASTEXITCODE -ne 0) {
    throw "failed to inspect Linux evaluation image"
}
$resourceRecord = [ordered]@{
    evaluator = "layout-routing-eval-v2"
    recordedAt = (Get-Date).ToUniversalTime().ToString("o")
    operatingSystem = "linux"
    architecture = "amd64"
    containerCPULimit = $CPUs
    containerMemoryLimit = $Memory
    networkMode = "none"
    readOnlyRootFilesystem = $true
    durationSeconds = $stopwatch.Elapsed.TotalSeconds
    userCPUSeconds = [double](Read-TimeMetric "User time (seconds)")
    systemCPUSeconds = [double](Read-TimeMetric "System time (seconds)")
    averageProcessCPUPercent = [double](Read-TimeMetric "Percent of CPU this job got").TrimEnd('%')
    peakResidentSetBytes = [int64](Read-TimeMetric "Maximum resident set size (kbytes)") * 1024
    majorPageFaults = [int64](Read-TimeMetric "Major (requiring I/O) page faults")
    minorPageFaults = [int64](Read-TimeMetric "Minor (reclaiming a frame) page faults")
    swaps = [int64](Read-TimeMetric "Swaps")
    evaluationImageSizeBytes = $imageSize
    exitCode = $exitCode
}
$resourceRecord | ConvertTo-Json -Depth 3 | Set-Content -LiteralPath (Join-Path $outputRootPath "resources.json") -Encoding utf8

Write-Host "Linux layout summary: $(Join-Path $outputRootPath 'summary.json')"
Write-Host "Linux resource metrics: $(Join-Path $outputRootPath 'resources.json')"
