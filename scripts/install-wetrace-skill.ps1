param(
  [string]$CodexHome = $env:CODEX_HOME,
  [switch]$Force
)

$ErrorActionPreference = "Stop"

if ([string]::IsNullOrWhiteSpace($CodexHome)) {
  $CodexHome = Join-Path $HOME ".codex"
}

$source = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot "..\skills\wetrace")).Path
$skillsRoot = Join-Path $CodexHome "skills"
$destination = Join-Path $skillsRoot "wetrace"

if (Test-Path -LiteralPath $destination) {
  if (-not $Force) {
    throw "Wetrace skill already exists at $destination. Rerun with -Force to replace it."
  }
  $resolvedSkillsRoot = [IO.Path]::GetFullPath($skillsRoot)
  $resolvedDestination = [IO.Path]::GetFullPath($destination)
  if (-not $resolvedDestination.StartsWith($resolvedSkillsRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Refusing unsafe skill destination: $resolvedDestination"
  }
  Remove-Item -LiteralPath $resolvedDestination -Recurse -Force
}

New-Item -ItemType Directory -Force -Path $skillsRoot | Out-Null
Copy-Item -LiteralPath $source -Destination $destination -Recurse

Write-Host "Installed Wetrace skill to $destination"
Write-Host "Start a new Codex task, then ask Codex to use Wetrace."
