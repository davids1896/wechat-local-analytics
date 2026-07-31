param(
  [Parameter(ValueFromRemainingArguments = $true)]
  [string[]]$Arguments
)

$ErrorActionPreference = "Stop"
$scriptPath = Join-Path $PSScriptRoot "..\skills\wetrace\scripts\wetrace_api.py"
$scriptPath = (Resolve-Path -LiteralPath $scriptPath).Path

$python = $env:WETRACE_PYTHON
if ([string]::IsNullOrWhiteSpace($python)) {
  $command = Get-Command python -ErrorAction SilentlyContinue
  if (-not $command) {
    throw "Python not found. Install Python 3.10+ or set WETRACE_PYTHON."
  }
  $python = $command.Source
}

$env:PYTHONUTF8 = "1"
$env:PYTHONIOENCODING = "utf-8"
& $python $scriptPath @Arguments
exit $LASTEXITCODE
