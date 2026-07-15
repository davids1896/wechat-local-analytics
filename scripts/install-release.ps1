param(
  [switch]$DryRun,
  [switch]$Json,
  [switch]$Update,
  [string]$Repo = $env:WECHAT_CLI_REPO,
  [string]$Tag = $env:WECHAT_CLI_RELEASE_TAG,
  [string]$Asset = $env:WECHAT_CLI_RELEASE_ASSET,
  [string]$InstallDir = $env:WECHAT_CLI_INSTALL_DIR,
  [switch]$All,
  [switch]$WithASR,
  [switch]$BackgroundRefresh,
  [switch]$KeepDownload
)

$ErrorActionPreference = "Stop"

if ([string]::IsNullOrWhiteSpace($Repo)) { $Repo = "https://github.com/r266-tech/wechat-cli" }
if ([string]::IsNullOrWhiteSpace($Tag)) { $Tag = "latest" }
if ([string]::IsNullOrWhiteSpace($Asset)) { $Asset = "wechat-cli-latest-windows-amd64.zip" }
if ($env:WECHAT_CLI_INSTALL_JSON -eq "1") { $Json = $true }
if ($env:WECHAT_CLI_KEEP_DOWNLOAD -eq "1") { $KeepDownload = $true }
if ($env:WECHAT_CLI_WITH_ASR -match '^(1|true|yes|on)$') { $WithASR = $true }

function Write-Step([string]$Text) {
  if ($Json) {
    [Console]::Error.WriteLine($Text)
  } else {
    Write-Host $Text
  }
}

function Get-RepoSlug([string]$RepoValue) {
  $r = $RepoValue -replace '^https?://github.com/', ''
  $r = $r -replace '\.git$', ''
  return $r.TrimEnd('/')
}

function Get-RepoUrl([string]$RepoValue) {
  if ($RepoValue -match '^https?://') {
    return ($RepoValue -replace '\.git$', '').TrimEnd('/')
  }
  return "https://github.com/$($RepoValue -replace '\.git$', '')".TrimEnd('/')
}

function Get-AssetUrl([string]$Base, [string]$TagValue, [string]$AssetName) {
  if ($TagValue -eq "latest") {
    return "$Base/releases/latest/download/$AssetName"
  }
  return "$Base/releases/download/$TagValue/$AssetName"
}

function Get-FallbackAssetUrl([string]$Slug, [string]$TagValue) {
  if ($TagValue -eq "latest") {
    $api = "https://api.github.com/repos/$Slug/releases/latest"
  } else {
    $api = "https://api.github.com/repos/$Slug/releases/tags/$TagValue"
  }
  $release = Invoke-RestMethod -Uri $api -UseBasicParsing
  $match = $release.assets | Where-Object {
    $_.name -like "*windows-amd64.zip" -and $_.name -notlike "*.sha256"
  } | Select-Object -First 1
  if ($null -eq $match) { return "" }
  return $match.browser_download_url
}

function Save-Url([string]$Url, [string]$Path) {
  Invoke-WebRequest -Uri $Url -OutFile $Path -UseBasicParsing
}

function Test-Sha256([string]$ZipPath, [string]$ShaPath) {
  if (-not (Test-Path -LiteralPath $ShaPath)) { throw "checksum file missing after successful download" }
  $tokens = (Get-Content -LiteralPath $ShaPath -Raw) -split '\s+'
  $expected = [string]$tokens[0]
  if ($expected -notmatch '^[0-9a-fA-F]{64}$') {
    throw "invalid sha256 file"
  }
  $expected = $expected.ToLowerInvariant()
  $actual = (Get-FileHash -LiteralPath $ZipPath -Algorithm SHA256).Hash.ToLowerInvariant()
  if ($expected -ne $actual) {
    throw "sha256 mismatch for downloaded release zip"
  }
}

function Confirm-ReleaseChecksum([string]$Url, [string]$ZipPath, [string]$ShaPath) {
  try {
    Save-Url "$Url.sha256" $ShaPath
  } catch {
    Write-Warning "release checksum file not found; continuing without checksum verification."
    return $false
  }

  # A downloaded checksum is an integrity assertion. Any parse error or
  # mismatch must abort instead of being treated like a missing sidecar.
  Test-Sha256 $ZipPath $ShaPath
  return $true
}

function Invoke-ReleaseInstall {
  if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "this installer is for Windows; use scripts/install-release.sh on macOS"
  }
  $osArch = if (-not [string]::IsNullOrWhiteSpace($env:PROCESSOR_ARCHITEW6432)) {
    $env:PROCESSOR_ARCHITEW6432
  } else {
    $env:PROCESSOR_ARCHITECTURE
  }
  if (-not [Environment]::Is64BitOperatingSystem -or $osArch -ne "AMD64") {
    throw "this release installer supports Windows amd64 only"
  }

  $slug = Get-RepoSlug $Repo
  $base = Get-RepoUrl $Repo
  $url = Get-AssetUrl $base $Tag $Asset
  $tmp = Join-Path ([IO.Path]::GetTempPath()) ("wechat-cli-install-" + [Guid]::NewGuid().ToString("N"))
  $extract = Join-Path $tmp "extract"
  New-Item -ItemType Directory -Force -Path $extract | Out-Null

  try {
    $zip = Join-Path $tmp $Asset
    $sha = Join-Path $tmp "$Asset.sha256"

    Write-Step "Downloading wechat-cli release: $url"
    try {
      Save-Url $url $zip
    } catch {
      Write-Warning "stable asset download failed; querying GitHub release metadata."
      $fallback = Get-FallbackAssetUrl $slug $Tag
      if ([string]::IsNullOrWhiteSpace($fallback)) {
        throw "could not find a windows-amd64 release asset for $slug"
      }
      $url = $fallback
      $zip = Join-Path $tmp (Split-Path ([Uri]$fallback).AbsolutePath -Leaf)
      Write-Step "Downloading wechat-cli release: $url"
      Save-Url $url $zip
    }

    if (Confirm-ReleaseChecksum $url $zip $sha) {
      Write-Step "Verified release checksum."
    }

    Expand-Archive -LiteralPath $zip -DestinationPath $extract -Force
    $installer = Get-ChildItem -LiteralPath $extract -Filter install.ps1 -Recurse | Select-Object -First 1
    if ($null -eq $installer) {
      throw "install.ps1 not found inside release zip"
    }

    $installerArgs = @()
    if ($Update) {
      $installerArgs += "-Update"
    }
    if ($All) {
      $installerArgs += "-All"
    }
    if ($WithASR) {
      $installerArgs += "-WithASR"
    }
    $installerArgs += "-Yes"
    if ($DryRun) { $installerArgs += "-DryRun" }
    if ($Json) { $installerArgs += "-Json" }
    if (-not [string]::IsNullOrWhiteSpace($InstallDir)) {
      $installerArgs += @("-InstallDir", $InstallDir)
    }
    if ($BackgroundRefresh) { $installerArgs += "-BackgroundRefresh" }

    Write-Step "Running bundled installer from $($installer.DirectoryName)"
    & powershell -NoProfile -ExecutionPolicy Bypass -File $installer.FullName @installerArgs
    if ($LASTEXITCODE -ne 0) {
      exit $LASTEXITCODE
    }
  } finally {
    if ($KeepDownload) {
      Write-Step "Keeping download directory: $tmp"
    } elseif (Test-Path -LiteralPath $tmp) {
      Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }
  }
}

if ($MyInvocation.InvocationName -ne ".") {
  Invoke-ReleaseInstall
}
