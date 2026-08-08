param(
    [string]$Manifest = "testdata/rag-ingestion-throughput-v1.corpus.json",
    [string]$OutputRoot = "output/evaluation/layout-routing-corpus",
    [switch]$Force
)

$ErrorActionPreference = "Stop"
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot "../..")).Path
$manifestPath = if ([IO.Path]::IsPathRooted($Manifest)) {
    [IO.Path]::GetFullPath($Manifest)
} else {
    [IO.Path]::GetFullPath((Join-Path $repositoryRoot $Manifest))
}
$outputPath = if ([IO.Path]::IsPathRooted($OutputRoot)) {
    [IO.Path]::GetFullPath($OutputRoot)
} else {
    [IO.Path]::GetFullPath((Join-Path $repositoryRoot $OutputRoot))
}
$evaluationRoot = [IO.Path]::GetFullPath((Join-Path $repositoryRoot "output/evaluation"))
if (-not $outputPath.StartsWith($evaluationRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
    throw "OutputRoot must stay under $evaluationRoot"
}

$corpus = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json
if ([string]::IsNullOrWhiteSpace($corpus.datasetVersion) -or $corpus.documents.Count -lt 1) {
    throw "Corpus manifest is empty or invalid"
}
New-Item -ItemType Directory -Force -Path $outputPath | Out-Null

foreach ($document in $corpus.documents) {
    $fileName = [string]$document.fileName
    if ([IO.Path]::GetFileName($fileName) -ne $fileName) {
        throw "Invalid corpus file name: $fileName"
    }
    $downloadUrl = [string]$document.downloadUrl
    if (-not $downloadUrl.StartsWith("https://", [StringComparison]::OrdinalIgnoreCase)) {
        throw "Corpus downloadUrl must use HTTPS: $fileName"
    }
    $target = Join-Path $outputPath $fileName
    $expectedSize = [int64]$document.sizeBytes
    $expectedHash = ([string]$document.sha256).ToLowerInvariant()
    if (Test-Path -LiteralPath $target) {
        $current = Get-Item -LiteralPath $target
        $currentHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $target).Hash.ToLowerInvariant()
        if ($current.Length -eq $expectedSize -and $currentHash -eq $expectedHash) {
            Write-Host "verified $fileName"
            continue
        }
        if (-not $Force) {
            throw "Existing corpus file does not match manifest: $target. Use -Force to replace it."
        }
    }

    $temporary = "$target.download-$PID"
    try {
        Invoke-WebRequest -UseBasicParsing -Uri $downloadUrl -OutFile $temporary
        $download = Get-Item -LiteralPath $temporary
        $downloadHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $temporary).Hash.ToLowerInvariant()
        if ($download.Length -ne $expectedSize -or $downloadHash -ne $expectedHash) {
            throw "Downloaded corpus file does not match manifest: $fileName"
        }
        Move-Item -Force -LiteralPath $temporary -Destination $target
        Write-Host "downloaded $fileName"
    } finally {
        if (Test-Path -LiteralPath $temporary) {
            Remove-Item -Force -LiteralPath $temporary
        }
    }
}
