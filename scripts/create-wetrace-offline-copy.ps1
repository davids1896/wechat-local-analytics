param(
  [Parameter(Mandatory = $true)]
  [string]$SourceAccountRoot,

  [Parameter(Mandatory = $true)]
  [string]$DestinationRoot,

  [switch]$SetUserEnvironment
)

$ErrorActionPreference = "Stop"

function Resolve-FullPath {
  param([Parameter(Mandatory = $true)][string]$Path)
  return [IO.Path]::GetFullPath($ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Path))
}

$runningWeChat = Get-Process Weixin, WeChat -ErrorAction SilentlyContinue
if ($runningWeChat) {
  $ids = ($runningWeChat | Select-Object -ExpandProperty Id | Sort-Object) -join ", "
  throw "WeChat is still running (PID: $ids). Exit WeChat from the system tray before creating an offline copy."
}

$source = Resolve-FullPath $SourceAccountRoot
$destinationBase = Resolve-FullPath $DestinationRoot
$sourceDbStorage = Join-Path $source "db_storage"
if (-not (Test-Path -LiteralPath $sourceDbStorage -PathType Container)) {
  throw "Source account root does not contain db_storage: $source"
}

$account = Split-Path -Leaf $source
$snapshotName = "{0}-{1}" -f $account, (Get-Date -Format "yyyyMMdd-HHmmss")
$destination = Join-Path $destinationBase $snapshotName
$separator = [IO.Path]::DirectorySeparatorChar
$sourcePrefix = $source.TrimEnd($separator) + $separator
$destinationPrefix = $destination.TrimEnd($separator) + $separator
if (
  $destination.Equals($source, [StringComparison]::OrdinalIgnoreCase) -or
  $destinationPrefix.StartsWith($sourcePrefix, [StringComparison]::OrdinalIgnoreCase) -or
  $sourcePrefix.StartsWith($destinationPrefix, [StringComparison]::OrdinalIgnoreCase)
) {
  throw "The offline copy must be outside the live WeChat account root. source=$source destination=$destination"
}
if (Test-Path -LiteralPath $destination) {
  throw "Destination account directory already exists; refusing to mix snapshots: $destination"
}

New-Item -ItemType Directory -Force -Path $destination | Out-Null
$destinationDbStorage = Join-Path $destination "db_storage"

Write-Host "Copying offline database snapshot..."
& robocopy.exe $sourceDbStorage $destinationDbStorage /E /COPY:DAT /DCOPY:DAT /R:2 /W:1 /XJ /NFL /NDL /NP
$robocopyCode = $LASTEXITCODE
if ($robocopyCode -ge 8) {
  throw "robocopy failed with exit code $robocopyCode. Incomplete destination: $destination"
}

$runningAfterCopy = Get-Process Weixin, WeChat -ErrorAction SilentlyContinue
if ($runningAfterCopy) {
  throw "WeChat started during the copy. The destination is incomplete and will not be marked safe: $destination"
}

$files = Get-ChildItem -LiteralPath $destinationDbStorage -File -Recurse
$totalBytes = ($files | Measure-Object -Property Length -Sum).Sum
if ($null -eq $totalBytes) {
  $totalBytes = 0
}
$marker = [ordered]@{
  format_version = 1
  status = "complete"
  created_at = [DateTimeOffset]::UtcNow.ToString("o")
  source_account_root = $source
  account = $account
  db_storage_file_count = @($files).Count
  db_storage_total_bytes = [int64]$totalBytes
}
$markerPath = Join-Path $destination ".wetrace-offline-copy.json"
$marker | ConvertTo-Json | Set-Content -LiteralPath $markerPath -Encoding UTF8

if ($SetUserEnvironment) {
  [Environment]::SetEnvironmentVariable("WETRACE_OFFLINE_DB_ROOT", $destination, "User")
}

Write-Host ""
Write-Host "Offline copy created: $destination"
Write-Host "Marker: $markerPath"
if ($SetUserEnvironment) {
  Write-Host "User environment WETRACE_OFFLINE_DB_ROOT has been set."
} else {
  Write-Host "`$env:WETRACE_OFFLINE_DB_ROOT = '$destination'"
}
Write-Host "Next: initialize the offline metadata cache, then run Wetrace doctor."
