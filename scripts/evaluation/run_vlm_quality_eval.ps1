param(
    [Parameter(Mandatory = $true)]
    [string]$InputRoot,
    [switch]$ExecuteProvider,
    [string]$Config = "config/mesguard.toml",
    [string]$Fixture = "testdata/vlm-quality-local-v1.json",
    [string]$Output = "output/evaluation/vlm-quality-local-v1.summary.json",
    [string]$CropOutput = "output/evaluation/vlm-quality-local-v1-crops"
)

$ErrorActionPreference = "Stop"

$arguments = @(
    "run", "./tools/evaluation/mesguard-vlm-quality-eval",
    "-config", $Config,
    "-fixture", $Fixture,
    "-root", $InputRoot,
    "-output", $Output,
    "-crop-output", $CropOutput
)
if ($ExecuteProvider) {
    $arguments += "-execute-provider"
}

& go @arguments
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}
