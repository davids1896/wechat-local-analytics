param(
  [Parameter(Mandatory = $true)]
  [string]$SourceAccountRoot,

  [Parameter(Mandatory = $true)]
  [string]$DestinationRoot,

  [switch]$Incremental,

  [switch]$SetUserEnvironment
)

$ErrorActionPreference = "Stop"

$completeMarkerName = ".wetrace-offline-copy.json"
$updatingMarkerName = ".wetrace-offline-copy.updating.json"
$previousMarkerName = ".wetrace-offline-copy.previous.json"

function Resolve-FullPath {
  param([Parameter(Mandatory = $true)][string]$Path)
  return [IO.Path]::GetFullPath($ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Path))
}

function Assert-WeChatStopped {
  param([Parameter(Mandatory = $true)][string]$Phase)

  $runningWeChat = Get-Process Weixin, WeChat -ErrorAction SilentlyContinue
  if ($runningWeChat) {
    $ids = ($runningWeChat | Select-Object -ExpandProperty Id | Sort-Object) -join ", "
    throw "WeChat is running during $Phase (PID: $ids). Exit WeChat from the system tray and rerun this command."
  }
}

function Read-JsonFile {
  param([Parameter(Mandatory = $true)][string]$Path)

  try {
    return Get-Content -LiteralPath $Path -Raw -Encoding UTF8 | ConvertFrom-Json
  } catch {
    throw "Cannot read JSON marker: $Path"
  }
}

function Write-JsonAtomic {
  param(
    [Parameter(Mandatory = $true)][object]$Value,
    [Parameter(Mandatory = $true)][string]$Path
  )

  $temporaryPath = "$Path.tmp"
  $Value | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $temporaryPath -Encoding UTF8
  Move-Item -LiteralPath $temporaryPath -Destination $Path -Force
}

function Get-FileInventory {
  param([Parameter(Mandatory = $true)][string]$Root)

  $rootFull = Resolve-FullPath $Root
  $prefix = $rootFull.TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
  $items = New-Object "System.Collections.Generic.Dictionary[string,object]" ([StringComparer]::OrdinalIgnoreCase)
  foreach ($file in Get-ChildItem -LiteralPath $rootFull -File -Recurse -Force) {
    $relativePath = $file.FullName.Substring($prefix.Length)
    $items[$relativePath] = [pscustomobject]@{
      RelativePath = $relativePath
      Length = [int64]$file.Length
      LastWriteTimeUtcTicks = [int64]$file.LastWriteTimeUtc.Ticks
    }
  }
  return $items
}

function Get-InventoryBytes {
  param([Parameter(Mandatory = $true)]$Inventory)

  [int64]$total = 0
  foreach ($item in $Inventory.Values) {
    $total += [int64]$item.Length
  }
  return $total
}

function Get-InventoryDelta {
  param(
    [Parameter(Mandatory = $true)]$Source,
    [Parameter(Mandatory = $true)]$Destination
  )

  [int64]$copyBytes = 0
  [int64]$copyFiles = 0
  [int64]$removeFiles = 0
  foreach ($entry in $Source.GetEnumerator()) {
    $destinationItem = $null
    if (
      -not $Destination.TryGetValue($entry.Key, [ref]$destinationItem) -or
      [int64]$destinationItem.Length -ne [int64]$entry.Value.Length -or
      [int64]$destinationItem.LastWriteTimeUtcTicks -ne [int64]$entry.Value.LastWriteTimeUtcTicks
    ) {
      $copyFiles += 1
      $copyBytes += [int64]$entry.Value.Length
    }
  }
  foreach ($entry in $Destination.GetEnumerator()) {
    if (-not $Source.ContainsKey($entry.Key)) {
      $removeFiles += 1
    }
  }
  return [pscustomobject]@{
    CopyFiles = $copyFiles
    CopyBytes = $copyBytes
    RemoveFiles = $removeFiles
  }
}

function Assert-InventoriesMatch {
  param(
    [Parameter(Mandatory = $true)]$Expected,
    [Parameter(Mandatory = $true)]$Actual,
    [Parameter(Mandatory = $true)][string]$Description
  )

  if ($Expected.Count -ne $Actual.Count) {
    throw "$Description file count mismatch: expected=$($Expected.Count) actual=$($Actual.Count)"
  }
  foreach ($entry in $Expected.GetEnumerator()) {
    $actualItem = $null
    if (-not $Actual.TryGetValue($entry.Key, [ref]$actualItem)) {
      throw "$Description is missing file: $($entry.Key)"
    }
    if (
      [int64]$actualItem.Length -ne [int64]$entry.Value.Length -or
      [int64]$actualItem.LastWriteTimeUtcTicks -ne [int64]$entry.Value.LastWriteTimeUtcTicks
    ) {
      throw "$Description metadata mismatch: $($entry.Key)"
    }
  }
}

Assert-WeChatStopped "offline snapshot preparation"

$source = Resolve-FullPath $SourceAccountRoot
$destinationBase = Resolve-FullPath $DestinationRoot
$sourceDbStorage = Join-Path $source "db_storage"
if (-not (Test-Path -LiteralPath $sourceDbStorage -PathType Container)) {
  throw "Source account root does not contain db_storage: $source"
}

$sourceInventoryBefore = Get-FileInventory $sourceDbStorage
if ($sourceInventoryBefore.Count -eq 0) {
  throw "Source db_storage is empty: $sourceDbStorage"
}

$account = Split-Path -Leaf $source
if ($Incremental) {
  $snapshotName = "$account-rolling"
} else {
  $snapshotName = "{0}-{1}" -f $account, (Get-Date -Format "yyyyMMdd-HHmmss")
}
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

$completeMarkerPath = Join-Path $destination $completeMarkerName
$updatingMarkerPath = Join-Path $destination $updatingMarkerName
$previousMarkerPath = Join-Path $destination $previousMarkerName
$previousMarker = $null
$updateState = $null
$resuming = $false

if (Test-Path -LiteralPath $destination) {
  if (-not $Incremental) {
    throw "Destination account directory already exists; refusing to mix snapshots: $destination"
  }

  if (Test-Path -LiteralPath $completeMarkerPath -PathType Leaf) {
    $previousMarker = Read-JsonFile $completeMarkerPath
    if ($previousMarker.status -ne "complete") {
      throw "Existing rolling snapshot marker is not complete: $completeMarkerPath"
    }
  } elseif (Test-Path -LiteralPath $updatingMarkerPath -PathType Leaf) {
    $updateState = Read-JsonFile $updatingMarkerPath
    if ($updateState.status -ne "updating") {
      throw "Existing update marker is invalid: $updatingMarkerPath"
    }
    if (Test-Path -LiteralPath $previousMarkerPath -PathType Leaf) {
      $previousMarker = Read-JsonFile $previousMarkerPath
    }
    $resuming = $true
  } elseif (Test-Path -LiteralPath $previousMarkerPath -PathType Leaf) {
    $previousMarker = Read-JsonFile $previousMarkerPath
    $resuming = $true
  } else {
    $existingFiles = Get-ChildItem -LiteralPath $destination -File -Recurse -Force
    if (@($existingFiles).Count -eq 0) {
      $resuming = $true
    } else {
      throw "Existing rolling destination is not a managed Wetrace snapshot: $destination"
    }
  }

  $knownSource = $null
  if ($previousMarker -and $previousMarker.source_account_root) {
    $knownSource = Resolve-FullPath ([string]$previousMarker.source_account_root)
  } elseif ($updateState -and $updateState.source_account_root) {
    $knownSource = Resolve-FullPath ([string]$updateState.source_account_root)
  }
  if (-not $knownSource -or -not $knownSource.Equals($source, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Rolling snapshot belongs to a different source account: $destination"
  }
} else {
  New-Item -ItemType Directory -Force -Path $destination | Out-Null
}

$destinationDbStorage = Join-Path $destination "db_storage"
if (-not (Test-Path -LiteralPath $destinationDbStorage -PathType Container)) {
  New-Item -ItemType Directory -Force -Path $destinationDbStorage | Out-Null
}

$destinationInventoryBefore = Get-FileInventory $destinationDbStorage
$delta = Get-InventoryDelta $sourceInventoryBefore $destinationInventoryBefore

if (Test-Path -LiteralPath $completeMarkerPath -PathType Leaf) {
  if (Test-Path -LiteralPath $previousMarkerPath -PathType Leaf) {
    Remove-Item -LiteralPath $previousMarkerPath -Force
  }
  Move-Item -LiteralPath $completeMarkerPath -Destination $previousMarkerPath
}

$startedAt = [DateTimeOffset]::UtcNow.ToString("o")
$updateState = [ordered]@{
  format_version = 1
  status = "updating"
  started_at = $startedAt
  source_account_root = $source
  account = $account
  snapshot_mode = $(if ($Incremental) { "rolling-incremental" } else { "timestamped-full" })
  planned_copy_file_count = [int64]$delta.CopyFiles
  planned_copy_bytes = [int64]$delta.CopyBytes
  planned_remove_file_count = [int64]$delta.RemoveFiles
}
Write-JsonAtomic $updateState $updatingMarkerPath

$copyMiB = [math]::Round(([int64]$delta.CopyBytes / 1MB), 1)
if ($resuming) {
  Write-Host "Resuming an interrupted rolling snapshot update."
}
Write-Host "Planned changes: copy $($delta.CopyFiles) file(s), approximately $copyMiB MiB; remove $($delta.RemoveFiles) stale file(s)."
Write-Host "Synchronizing offline database snapshot..."

$robocopyArguments = @(
  $sourceDbStorage,
  $destinationDbStorage
)
if ($Incremental) {
  $robocopyArguments += "/MIR"
} else {
  $robocopyArguments += "/E"
}
$robocopyArguments += @(
  "/COPY:DAT",
  "/DCOPY:DAT",
  "/R:2",
  "/W:1",
  "/XJ",
  "/NFL",
  "/NDL",
  "/NP"
)
& robocopy.exe @robocopyArguments
$robocopyCode = $LASTEXITCODE
if ($robocopyCode -ge 8) {
  throw "robocopy failed with exit code $robocopyCode. The snapshot remains unmarked and Wetrace will refuse to read it: $destination"
}

Assert-WeChatStopped "offline snapshot verification"

$sourceInventoryAfter = Get-FileInventory $sourceDbStorage
Assert-InventoriesMatch $sourceInventoryBefore $sourceInventoryAfter "Source db_storage changed during copy"

$destinationInventoryAfter = Get-FileInventory $destinationDbStorage
Assert-InventoriesMatch $sourceInventoryAfter $destinationInventoryAfter "Offline db_storage"

$now = [DateTimeOffset]::UtcNow.ToString("o")
$createdAt = $now
[int64]$updateCount = 1
if ($previousMarker) {
  if ($previousMarker.created_at) {
    $createdAt = [string]$previousMarker.created_at
  }
  if ($previousMarker.update_count) {
    $updateCount = [int64]$previousMarker.update_count + 1
  } else {
    $updateCount = 2
  }
}
$copyKind = "full"
if ($Incremental -and $destinationInventoryBefore.Count -gt 0) {
  $copyKind = "file-level-incremental"
}
$marker = [ordered]@{
  format_version = 1
  status = "complete"
  snapshot_mode = $(if ($Incremental) { "rolling-incremental" } else { "timestamped-full" })
  created_at = $createdAt
  updated_at = $now
  source_account_root = $source
  account = $account
  update_count = $updateCount
  db_storage_file_count = [int64]$destinationInventoryAfter.Count
  db_storage_total_bytes = [int64](Get-InventoryBytes $destinationInventoryAfter)
  last_copy = [ordered]@{
    kind = $copyKind
    started_at = $startedAt
    completed_at = $now
    planned_copy_file_count = [int64]$delta.CopyFiles
    planned_copy_bytes = [int64]$delta.CopyBytes
    planned_remove_file_count = [int64]$delta.RemoveFiles
    robocopy_exit_code = [int64]$robocopyCode
  }
}
Write-JsonAtomic $marker $completeMarkerPath

if (Test-Path -LiteralPath $updatingMarkerPath -PathType Leaf) {
  Remove-Item -LiteralPath $updatingMarkerPath -Force
}
if (Test-Path -LiteralPath $previousMarkerPath -PathType Leaf) {
  Remove-Item -LiteralPath $previousMarkerPath -Force
}

if ($SetUserEnvironment) {
  [Environment]::SetEnvironmentVariable("WETRACE_OFFLINE_DB_ROOT", $destination, "User")
}

Write-Host ""
if ($copyKind -eq "file-level-incremental") {
  Write-Host "Offline rolling snapshot updated: $destination"
} else {
  Write-Host "Offline snapshot created: $destination"
}
Write-Host "Marker: $completeMarkerPath"
if ($SetUserEnvironment) {
  Write-Host "User environment WETRACE_OFFLINE_DB_ROOT has been set."
} else {
  Write-Host "`$env:WETRACE_OFFLINE_DB_ROOT = '$destination'"
}
Write-Host "You may reopen WeChat now. Wetrace will continue reading this offline snapshot."
