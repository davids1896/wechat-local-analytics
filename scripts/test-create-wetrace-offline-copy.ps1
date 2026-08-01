$ErrorActionPreference = "Stop"

function Assert-True {
  param(
    [Parameter(Mandatory = $true)][bool]$Condition,
    [Parameter(Mandatory = $true)][string]$Message
  )
  if (-not $Condition) {
    throw $Message
  }
}

# Tests use isolated temporary directories and intentionally hide the real
# WeChat process list from the script under test.
function Get-Process {
  [CmdletBinding()]
  param([Parameter(Position = 0)][string[]]$Name)
  return @()
}

$scriptPath = Join-Path $PSScriptRoot "create-wetrace-offline-copy.ps1"
$testRoot = Join-Path ([IO.Path]::GetTempPath()) ("wetrace-offline-copy-test-" + [Guid]::NewGuid().ToString("N"))
$source = Join-Path $testRoot "live\wxid_test"
$sourceDb = Join-Path $source "db_storage"
$destinationBase = Join-Path $testRoot "offline"
$snapshot = Join-Path $destinationBase "wxid_test-rolling"
$destinationDb = Join-Path $snapshot "db_storage"
$markerPath = Join-Path $snapshot ".wetrace-offline-copy.json"

try {
  New-Item -ItemType Directory -Force -Path (Join-Path $sourceDb "message") | Out-Null
  New-Item -ItemType Directory -Force -Path (Join-Path $sourceDb "session") | Out-Null
  Set-Content -LiteralPath (Join-Path $sourceDb "message\message_0.db") -Value "first" -Encoding ASCII
  Set-Content -LiteralPath (Join-Path $sourceDb "session\session.db") -Value "remove-me" -Encoding ASCII

  & $scriptPath `
    -SourceAccountRoot $source `
    -DestinationRoot $destinationBase `
    -Incremental

  Assert-True (Test-Path -LiteralPath $markerPath -PathType Leaf) "First sync did not create a completion marker."
  $firstMarker = Get-Content -LiteralPath $markerPath -Raw -Encoding UTF8 | ConvertFrom-Json
  Assert-True ($firstMarker.status -eq "complete") "First marker is not complete."
  Assert-True ([int64]$firstMarker.update_count -eq 1) "First update_count is not 1."
  Assert-True ($firstMarker.last_copy.kind -eq "full") "First rolling sync was not recorded as full."

  Start-Sleep -Milliseconds 1100
  Set-Content -LiteralPath (Join-Path $sourceDb "message\message_0.db") -Value "second-version" -Encoding ASCII
  Set-Content -LiteralPath (Join-Path $sourceDb "message\message_1.db") -Value "new-file" -Encoding ASCII
  Remove-Item -LiteralPath (Join-Path $sourceDb "session\session.db") -Force

  & $scriptPath `
    -SourceAccountRoot $source `
    -DestinationRoot $destinationBase `
    -Incremental

  $secondMarker = Get-Content -LiteralPath $markerPath -Raw -Encoding UTF8 | ConvertFrom-Json
  Assert-True ([int64]$secondMarker.update_count -eq 2) "Second update_count is not 2."
  Assert-True ($secondMarker.last_copy.kind -eq "file-level-incremental") "Second sync was not recorded as incremental."
  Assert-True ((Get-Content -LiteralPath (Join-Path $destinationDb "message\message_0.db") -Raw).Trim() -eq "second-version") "Changed file was not updated."
  Assert-True (Test-Path -LiteralPath (Join-Path $destinationDb "message\message_1.db") -PathType Leaf) "New file was not copied."
  Assert-True (-not (Test-Path -LiteralPath (Join-Path $destinationDb "session\session.db"))) "Stale file was not removed."
  Assert-True (-not (Test-Path -LiteralPath (Join-Path $snapshot ".wetrace-offline-copy.updating.json"))) "Updating marker remained after success."

  $previousMarkerPath = Join-Path $snapshot ".wetrace-offline-copy.previous.json"
  $updatingMarkerPath = Join-Path $snapshot ".wetrace-offline-copy.updating.json"
  Move-Item -LiteralPath $markerPath -Destination $previousMarkerPath
  @{
    format_version = 1
    status = "updating"
    source_account_root = $source
  } | ConvertTo-Json | Set-Content -LiteralPath $updatingMarkerPath -Encoding UTF8
  Set-Content -LiteralPath (Join-Path $sourceDb "message\message_2.db") -Value "resume-file" -Encoding ASCII

  & $scriptPath `
    -SourceAccountRoot $source `
    -DestinationRoot $destinationBase `
    -Incremental

  $resumedMarker = Get-Content -LiteralPath $markerPath -Raw -Encoding UTF8 | ConvertFrom-Json
  Assert-True ([int64]$resumedMarker.update_count -eq 3) "Resumed update_count is not 3."
  Assert-True (Test-Path -LiteralPath (Join-Path $destinationDb "message\message_2.db") -PathType Leaf) "Interrupted update did not resume."
  Assert-True (-not (Test-Path -LiteralPath $updatingMarkerPath)) "Updating marker remained after resumed success."
  Assert-True (-not (Test-Path -LiteralPath $previousMarkerPath)) "Previous marker remained after resumed success."

  Move-Item -LiteralPath $markerPath -Destination $previousMarkerPath
  Set-Content -LiteralPath (Join-Path $sourceDb "message\message_3.db") -Value "previous-only-resume" -Encoding ASCII

  & $scriptPath `
    -SourceAccountRoot $source `
    -DestinationRoot $destinationBase `
    -Incremental

  $previousOnlyMarker = Get-Content -LiteralPath $markerPath -Raw -Encoding UTF8 | ConvertFrom-Json
  Assert-True ([int64]$previousOnlyMarker.update_count -eq 4) "Previous-only resume update_count is not 4."
  Assert-True (Test-Path -LiteralPath (Join-Path $destinationDb "message\message_3.db") -PathType Leaf) "Previous-only interruption did not resume."

  Write-Host "PASS: rolling snapshot initial, incremental, and resumed sync"
} finally {
  if (Test-Path -LiteralPath $testRoot) {
    Remove-Item -LiteralPath $testRoot -Recurse -Force
  }
}
