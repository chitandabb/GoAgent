param(
    [Parameter(Mandatory = $true)]
    [string]$InputRoot,
    [string]$Output = "output/evaluation/pptx-element-quality-local-v1.observations.jsonl",
    [string]$Summary = "output/evaluation/pptx-element-quality-local-v1.summary.json"
)

$ErrorActionPreference = "Stop"
$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot "../..")).Path
$resolvedInputRoot = (Resolve-Path -LiteralPath $InputRoot).Path

Push-Location $repositoryRoot
try {
    & go run ./tools/evaluation/mesguard-pptx-element-eval `
        -root $resolvedInputRoot `
        -output $Output `
        -summary $Summary
    if ($LASTEXITCODE -ne 0) {
        throw "PPTX element quality evaluation failed"
    }
}
finally {
    Pop-Location
}
