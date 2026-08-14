param(
    [string]$ResourceOutput = "output/evaluation/layout-routing-public-v1.resources.json",
    [string[]]$EvaluatorArgs = @()
)

$ErrorActionPreference = "Stop"
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot "../..")).Path
$evaluationRoot = [IO.Path]::GetFullPath((Join-Path $repositoryRoot "output/evaluation"))
$binaryDirectory = Join-Path $evaluationRoot "bin"
$binaryPath = Join-Path $binaryDirectory "mesguard-layout-routing-eval.exe"
$standardOutput = Join-Path $evaluationRoot "layout-routing-public-v1.stdout.log"
$standardError = Join-Path $evaluationRoot "layout-routing-public-v1.stderr.log"
$resourcePath = if ([IO.Path]::IsPathRooted($ResourceOutput)) {
    [IO.Path]::GetFullPath($ResourceOutput)
} else {
    [IO.Path]::GetFullPath((Join-Path $repositoryRoot $ResourceOutput))
}
if (-not $resourcePath.StartsWith($evaluationRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
    throw "ResourceOutput must stay under $evaluationRoot"
}

New-Item -ItemType Directory -Force -Path $binaryDirectory | Out-Null
& go build -o $binaryPath ./tools/evaluation/mesguard-layout-routing-eval
if ($LASTEXITCODE -ne 0) {
    throw "Failed to build mesguard-layout-routing-eval"
}

$startedAt = Get-Date
$startInfo = New-Object Diagnostics.ProcessStartInfo
$startInfo.FileName = $binaryPath
$startInfo.Arguments = (($EvaluatorArgs | ForEach-Object { '"' + ([string]$_).Replace('"', '\"') + '"' }) -join ' ')
$startInfo.UseShellExecute = $false
$startInfo.CreateNoWindow = $true
$startInfo.RedirectStandardOutput = $true
$startInfo.RedirectStandardError = $true
$process = New-Object Diagnostics.Process
$process.StartInfo = $startInfo
if (-not $process.Start()) {
    throw "Failed to start layout routing evaluator"
}
$standardOutputTask = $process.StandardOutput.ReadToEndAsync()
$standardErrorTask = $process.StandardError.ReadToEndAsync()
$peakWorkingSet = [int64]0
$peakPrivateMemory = [int64]0
$cpuSeconds = 0.0
while (-not $process.HasExited) {
    $process.Refresh()
    $peakWorkingSet = [Math]::Max($peakWorkingSet, [int64]$process.WorkingSet64)
    $peakPrivateMemory = [Math]::Max($peakPrivateMemory, [int64]$process.PrivateMemorySize64)
    $cpuSeconds = [Math]::Max($cpuSeconds, $process.TotalProcessorTime.TotalSeconds)
    Start-Sleep -Milliseconds 50
}
$process.WaitForExit()
$process.Refresh()
$exitCode = $process.ExitCode
$capturedStandardOutput = $standardOutputTask.Result
$capturedStandardError = $standardErrorTask.Result
$capturedStandardOutput | Set-Content -LiteralPath $standardOutput -Encoding utf8
$capturedStandardError | Set-Content -LiteralPath $standardError -Encoding utf8
$durationSeconds = ((Get-Date) - $startedAt).TotalSeconds
$logicalProcessors = [Environment]::ProcessorCount
$averageCPUPercent = 0.0
if ($durationSeconds -gt 0 -and $logicalProcessors -gt 0) {
    $averageCPUPercent = $cpuSeconds / ($durationSeconds * $logicalProcessors) * 100
}

$resourceRecord = [ordered]@{
    evaluator = "layout-routing-eval-v2"
    recordedAt = (Get-Date).ToUniversalTime().ToString("o")
    operatingSystem = [Environment]::OSVersion.VersionString
    architecture = [Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
    logicalProcessors = $logicalProcessors
    durationSeconds = $durationSeconds
    processCPUSeconds = $cpuSeconds
    averageProcessCPUPercent = $averageCPUPercent
    peakWorkingSetBytes = $peakWorkingSet
    peakPrivateMemoryBytes = $peakPrivateMemory
    exitCode = $exitCode
}
$resourceRecord | ConvertTo-Json -Depth 3 | Set-Content -LiteralPath $resourcePath -Encoding utf8

$capturedStandardOutput.TrimEnd()
if ($exitCode -ne 0) {
    $capturedStandardError.TrimEnd()
    throw "Layout routing evaluation failed with exit code $exitCode"
}
Write-Host "resource metrics: $resourcePath"
