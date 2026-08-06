[CmdletBinding()]
param(
    [string]$OutputRoot = "output/models/pp-doclayout-m",
    [string]$ConverterImage = "mesguard/pp-doclayout-converter:2.1.0",
    [switch]$SkipImageBuild,
    [switch]$ForceDownload
)

$ErrorActionPreference = "Stop"

$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot "../..")).Path
$manifestPath = Join-Path $repositoryRoot "config/models/pp-doclayout-m.json"
$manifest = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json
$resolvedOutputRoot = [System.IO.Path]::GetFullPath((Join-Path $repositoryRoot $OutputRoot))
$sourceRoot = Join-Path $resolvedOutputRoot "source"
$onnxPath = Join-Path $resolvedOutputRoot $manifest.conversion.outputFile
$metadataPath = Join-Path $resolvedOutputRoot "onnx-metadata.json"
$dockerfilePath = Join-Path $repositoryRoot "scripts/models/Dockerfile.pp-doclayout-converter"
$inspectorPath = Join-Path $repositoryRoot "scripts/models/inspect_onnx.py"

New-Item -ItemType Directory -Force -Path $sourceRoot | Out-Null

if (-not $SkipImageBuild) {
    & docker build --file $dockerfilePath --tag $ConverterImage $repositoryRoot
    if ($LASTEXITCODE -ne 0) {
        throw "failed to build the pinned Paddle2ONNX conversion image"
    }
}

foreach ($file in $manifest.source.files) {
    $target = Join-Path $sourceRoot $file.name
    if ($ForceDownload -or -not (Test-Path -LiteralPath $target)) {
        Invoke-WebRequest -UseBasicParsing -Uri "$($manifest.source.baseUrl)/$($file.name)" -OutFile $target
    }
    $actualBytes = (Get-Item -LiteralPath $target).Length
    $actualSHA256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $target).Hash.ToLowerInvariant()
    if ($actualBytes -ne $file.bytes -or $actualSHA256 -ne $file.sha256) {
        throw "source artifact verification failed: $($file.name)"
    }
}

& docker run --rm `
    --mount "type=bind,source=$resolvedOutputRoot,target=/work" `
    $ConverterImage `
    --model_dir /work/source `
    --model_filename inference.json `
    --params_filename inference.pdiparams `
    --save_file "/work/$($manifest.conversion.outputFile)" `
    --opset_version $manifest.conversion.opset `
    --enable_onnx_checker True `
    --enable_auto_update_opset False `
    --optimize_tool None
if ($LASTEXITCODE -ne 0) {
    throw "Paddle2ONNX conversion failed"
}

& docker run --rm `
    --entrypoint python `
    --mount "type=bind,source=$resolvedOutputRoot,target=/work" `
    --mount "type=bind,source=$inspectorPath,target=/tools/inspect_onnx.py,readonly" `
    $ConverterImage `
    /tools/inspect_onnx.py `
    --model "/work/$($manifest.conversion.outputFile)" `
    --output /work/onnx-metadata.json
if ($LASTEXITCODE -ne 0) {
    throw "ONNX validation failed"
}

$actualONNXSHA256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $onnxPath).Hash.ToLowerInvariant()
if ($manifest.conversion.sha256 -and $actualONNXSHA256 -ne $manifest.conversion.sha256) {
    throw "converted ONNX checksum does not match the pinned manifest"
}

Write-Host "ONNX model: $onnxPath"
Write-Host "SHA-256: $actualONNXSHA256"
Write-Host "Metadata: $metadataPath"
