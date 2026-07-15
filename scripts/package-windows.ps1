param(
  [string]$Version = "",
  [string]$WcdbLib = $env:WECHAT_CLI_WCDB_LIB
)

$ErrorActionPreference = "Stop"
$SourceDir = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Set-Location $SourceDir

if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
  throw "Windows release packages must be built on Windows"
}
$osArch = if (-not [string]::IsNullOrWhiteSpace($env:PROCESSOR_ARCHITEW6432)) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
if (-not [Environment]::Is64BitOperatingSystem -or $osArch -ne "AMD64") {
  throw "Windows release packages must be built on Windows amd64"
}
$productText = Get-Content -Raw -LiteralPath (Join-Path $SourceDir "cmd\wechat-cli\product.go")
$versionMatch = [regex]::Match($productText, 'appVersion\s*=\s*"([^"]+)"')
if (-not $versionMatch.Success) {
  throw "could not read appVersion from cmd\wechat-cli\product.go"
}
$sourceVersion = $versionMatch.Groups[1].Value
if ([string]::IsNullOrWhiteSpace($Version)) { $Version = $sourceVersion }
if ($Version -notmatch '^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$') {
  throw "Version must be semantic numeric form such as 1.6.20 or 1.6.20-rc.1"
}
if ($sourceVersion -ne $Version) {
  throw "package version $Version does not match appVersion $sourceVersion"
}
if ($env:WECHAT_CLI_ALLOW_UNTAGGED_PACKAGE -ne "1") {
  if (-not (Get-Command git -ErrorAction SilentlyContinue)) {
    throw "git is required to verify the release source tag"
  }
  & git rev-parse --git-dir 2>$null | Out-Null
  if ($LASTEXITCODE -ne 0) { throw "release packaging requires a git checkout" }
  $tag = (& git describe --tags --exact-match HEAD 2>$null | Select-Object -First 1)
  if ($LASTEXITCODE -ne 0 -or $tag -ne "v$Version") {
    throw "release packaging requires HEAD at tag v$Version"
  }
  $dirty = (& git status --porcelain --untracked-files=normal) -join "`n"
  if (-not [string]::IsNullOrWhiteSpace($dirty)) { throw "release packaging requires a clean worktree" }
}

$RequiredWcdbExports = @(
  "sqlite3_open_v2",
  "sqlite3_close_v2",
  "sqlite3_key_v2",
  "sqlite3_exec",
  "sqlite3_prepare_v2",
  "sqlite3_step",
  "sqlite3_finalize",
  "sqlite3_column_count",
  "sqlite3_column_name",
  "sqlite3_column_text",
  "sqlite3_column_int64",
  "sqlite3_column_bytes",
  "sqlite3_column_blob",
  "sqlite3_column_type",
  "sqlite3_bind_text",
  "sqlite3_bind_blob",
  "sqlite3_bind_int64",
  "sqlite3_bind_null",
  "sqlite3_reset",
  "sqlite3_clear_bindings",
  "sqlite3_errmsg",
  "sqlite3_backup_init",
  "sqlite3_backup_step",
  "sqlite3_backup_finish"
)

function Assert-WcdbDllExports {
  param([string]$Path)

  Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;

public static class WxMcpNative {
  [DllImport("kernel32", SetLastError=true, CharSet=CharSet.Unicode)]
  public static extern IntPtr LoadLibrary(string lpFileName);

  [DllImport("kernel32", SetLastError=true, CharSet=CharSet.Ansi)]
  public static extern IntPtr GetProcAddress(IntPtr hModule, string procName);
}
"@ -ErrorAction SilentlyContinue

  $handle = [WxMcpNative]::LoadLibrary((Resolve-Path -LiteralPath $Path).Path)
  if ($handle -eq [IntPtr]::Zero) {
    $lastError = [Runtime.InteropServices.Marshal]::GetLastWin32Error()
    throw "WCDB DLL failed to load: $Path (Win32 error $lastError)"
  }

  $missing = @()
  foreach ($name in $RequiredWcdbExports) {
    if ([WxMcpNative]::GetProcAddress($handle, $name) -eq [IntPtr]::Zero) {
      $missing += $name
    }
  }
  if ($missing.Count -gt 0) {
    throw "WCDB DLL missing required exports: $($missing -join ', ')"
  }
}

function Assert-PeAmd64 {
  param([string]$Path)

  $stream = [IO.File]::OpenRead((Resolve-Path -LiteralPath $Path).Path)
  try {
    $reader = New-Object System.IO.BinaryReader($stream)
    if ($reader.ReadUInt16() -ne 0x5A4D) { throw "not a PE file: $Path" }
    $stream.Position = 0x3c
    $peOffset = $reader.ReadInt32()
    $stream.Position = $peOffset
    if ($reader.ReadUInt32() -ne 0x00004550) { throw "invalid PE signature: $Path" }
    $machine = $reader.ReadUInt16()
    if ($machine -ne 0x8664) { throw ("PE machine is 0x{0:x4}, expected amd64: {1}" -f $machine, $Path) }
  } finally {
    $stream.Dispose()
  }
}

if ([string]::IsNullOrWhiteSpace($WcdbLib)) {
  foreach ($cand in @(
    (Join-Path $SourceDir "lib\libWCDB.dll"),
    (Join-Path $SourceDir "lib\WCDB.dll"),
    (Join-Path $SourceDir "libWCDB.dll"),
    (Join-Path $SourceDir "WCDB.dll")
  )) {
    if (Test-Path $cand) {
      $WcdbLib = $cand
      break
    }
  }
}
if ([string]::IsNullOrWhiteSpace($WcdbLib) -or -not (Test-Path $WcdbLib)) {
  throw "WCDB DLL missing. Set WECHAT_CLI_WCDB_LIB or place libWCDB.dll/WCDB.dll under .\lib."
}
Assert-PeAmd64 $WcdbLib
Assert-WcdbDllExports $WcdbLib
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
  throw "Go is required to build wechat-cli.exe"
}

$distRoot = Join-Path $SourceDir "dist"
$dist = Join-Path $distRoot "wechat-cli-v$Version-windows-amd64"
if (Test-Path $dist) { Remove-Item -LiteralPath $dist -Recurse -Force }
New-Item -ItemType Directory -Force -Path $dist | Out-Null

$oldCgo = $env:CGO_ENABLED
$oldGoos = $env:GOOS
$oldGoarch = $env:GOARCH
try {
  $env:CGO_ENABLED = "0"
  $env:GOOS = "windows"
  $env:GOARCH = "amd64"
  & go build -trimpath -ldflags="-s -w" -o (Join-Path $dist "wechat-cli.exe") ./cmd/wechat-cli
  if ($LASTEXITCODE -ne 0) { throw "go build failed" }
} finally {
  if ($null -eq $oldCgo) {
    Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue
  } else {
    $env:CGO_ENABLED = $oldCgo
  }
  if ($null -eq $oldGoos) { Remove-Item Env:GOOS -ErrorAction SilentlyContinue } else { $env:GOOS = $oldGoos }
  if ($null -eq $oldGoarch) { Remove-Item Env:GOARCH -ErrorAction SilentlyContinue } else { $env:GOARCH = $oldGoarch }
}
Assert-PeAmd64 (Join-Path $dist "wechat-cli.exe")
$versionEnvelope = & (Join-Path $dist "wechat-cli.exe") --version | ConvertFrom-Json
if ($versionEnvelope.data.version -ne $Version) { throw "built CLI version does not match $Version" }

Copy-Item -LiteralPath $WcdbLib -Destination (Join-Path $dist "libWCDB.dll") -Force
Copy-Item README.md, llms.txt, LICENSE, SECURITY.md, THIRD_PARTY_NOTICES.md, AGENTS.md, install.ps1 -Destination $dist -Force
New-Item -ItemType Directory -Force -Path (Join-Path $dist "scripts") | Out-Null
Copy-Item -LiteralPath (Join-Path $SourceDir "scripts\install-release.ps1") -Destination (Join-Path $dist "scripts\install-release.ps1") -Force
Copy-Item -LiteralPath (Join-Path $SourceDir "scripts\wechat-read-regression.sh") -Destination (Join-Path $dist "scripts\wechat-read-regression.sh") -Force
if (Test-Path (Join-Path $SourceDir "docs\WINDOWS_USER_GUIDE.md")) {
  New-Item -ItemType Directory -Force -Path (Join-Path $dist "docs") | Out-Null
  Copy-Item -LiteralPath (Join-Path $SourceDir "docs\WINDOWS_USER_GUIDE.md") -Destination (Join-Path $dist "docs\WINDOWS_USER_GUIDE.md") -Force
}

$zip = Join-Path $distRoot "wechat-cli-v$Version-windows-amd64.zip"
$latest = Join-Path $distRoot "wechat-cli-latest-windows-amd64.zip"
if (Test-Path $zip) { Remove-Item -LiteralPath $zip -Force }
if (Test-Path $latest) { Remove-Item -LiteralPath $latest -Force }
Compress-Archive -Path $dist -DestinationPath $zip -Force
Copy-Item -LiteralPath $zip -Destination $latest -Force
Get-FileHash -LiteralPath $zip -Algorithm SHA256 | ForEach-Object { "$($_.Hash.ToLowerInvariant())  $(Split-Path $zip -Leaf)" } | Set-Content -LiteralPath "$zip.sha256" -Encoding ASCII
Get-FileHash -LiteralPath $latest -Algorithm SHA256 | ForEach-Object { "$($_.Hash.ToLowerInvariant())  $(Split-Path $latest -Leaf)" } | Set-Content -LiteralPath "$latest.sha256" -Encoding ASCII

$bootstrapAssets = [ordered]@{}
foreach ($name in @("install-release.sh", "install-release.ps1")) {
  $source = Join-Path $SourceDir "scripts\$name"
  $destination = Join-Path $distRoot $name
  Copy-Item -LiteralPath $source -Destination $destination -Force
  Get-FileHash -LiteralPath $destination -Algorithm SHA256 |
    ForEach-Object { "$($_.Hash.ToLowerInvariant())  $name" } |
    Set-Content -LiteralPath "$destination.sha256" -Encoding ASCII
  $bootstrapAssets[$name] = $destination
  $bootstrapAssets["$name.sha256"] = "$destination.sha256"
}

[ordered]@{
  zip = $zip
  latest = $latest
  sha256 = "$zip.sha256"
  bootstraps = $bootstrapAssets
} | ConvertTo-Json -Depth 3
