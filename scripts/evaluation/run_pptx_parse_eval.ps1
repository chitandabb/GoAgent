param(
    [Parameter(Mandatory = $true)]
    [string]$InputRoot,
    [string]$Output = "output/evaluation/pptx-parse-local.summary.json",
    [int]$MaxFiles = 64
)

$ErrorActionPreference = "Stop"
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot "../..")).Path
$resolvedInputRoot = (Resolve-Path -LiteralPath $InputRoot).Path
$evaluationRoot = [IO.Path]::GetFullPath((Join-Path $repositoryRoot "output/evaluation"))
$outputPath = if ([IO.Path]::IsPathRooted($Output)) {
    [IO.Path]::GetFullPath($Output)
} else {
    [IO.Path]::GetFullPath((Join-Path $repositoryRoot $Output))
}
if (-not $outputPath.StartsWith($evaluationRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Output must stay under $evaluationRoot"
}
if ($MaxFiles -lt 1 -or $MaxFiles -gt 256) {
    throw "MaxFiles must be between 1 and 256"
}

$files = @(Get-ChildItem -LiteralPath $resolvedInputRoot -Recurse -File -Filter "*.pptx" |
    Sort-Object FullName)
if ($files.Count -eq 0) {
    throw "No PPTX files were found under $resolvedInputRoot"
}
if ($files.Count -gt $MaxFiles) {
    throw "PPTX file count $($files.Count) exceeds MaxFiles $MaxFiles"
}

$arguments = @("run", "./cmd/mesguard-document-parse-eval", "-output", $outputPath)
foreach ($file in $files) {
    $arguments += @("-input", $file.FullName)
}
& go @arguments
if ($LASTEXITCODE -ne 0) {
    throw "PPTX parse evaluation failed"
}
