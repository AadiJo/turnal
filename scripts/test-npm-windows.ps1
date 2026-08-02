param([string]$ArchivePath)

$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$packageJson = Get-Content -LiteralPath (Join-Path $repoRoot 'package.json') -Raw | ConvertFrom-Json
$executables = @($packageJson.bin.PSObject.Properties.Name)
$npmCommand = (Get-Command npm.cmd -ErrorAction Stop).Source
$testRoot = Join-Path ([IO.Path]::GetTempPath()) "turnal-npm-windows-$([guid]::NewGuid())"
$globalPrefix = Join-Path $testRoot 'global'
$localPrefix = Join-Path $testRoot 'local'
$localBin = Join-Path $localPrefix 'node_modules/.bin'
$archive = $ArchivePath
$removeArchive = -not $ArchivePath

function Invoke-Npm {
  param([Parameter(Mandatory)][string[]]$Arguments)

  & $npmCommand @Arguments
  if ($LASTEXITCODE -ne 0) {
    throw "npm $($Arguments -join ' ') failed with exit code $LASTEXITCODE"
  }
}

function Assert-InstalledLaunchers {
  param([Parameter(Mandatory)][string]$BinDirectory)

  foreach ($executable in $executables) {
    $cmdShim = Join-Path $BinDirectory "$executable.cmd"
    $powerShellShim = Join-Path $BinDirectory "$executable.ps1"
    if (-not (Test-Path -LiteralPath $cmdShim -PathType Leaf)) {
      throw "expected command shim to remain: $cmdShim"
    }
    if (Test-Path -LiteralPath $powerShellShim) {
      throw "expected PowerShell shim to be removed: $powerShellShim"
    }
  }
}

function Assert-LaunchersRemoved {
  param([Parameter(Mandatory)][string]$BinDirectory)

  foreach ($executable in $executables) {
    foreach ($suffix in @('', '.cmd', '.ps1')) {
      $launcher = Join-Path $BinDirectory "$executable$suffix"
      if (Test-Path -LiteralPath $launcher) {
        throw "expected npm uninstall to remove launcher: $launcher"
      }
    }
  }
}

function Assert-TurnalResolution {
  param([Parameter(Mandatory)][string]$BinDirectory)

  $resolved = Get-Command turnal -CommandType Application -ErrorAction Stop | Select-Object -First 1
  if ($resolved.Source -ne (Join-Path $BinDirectory 'turnal.cmd')) {
    throw "expected PowerShell to resolve turnal.cmd, got $($resolved.Source)"
  }
}

function Assert-TurnalVersion {
  $version = (& turnal version --json | ConvertFrom-Json)
  if ($LASTEXITCODE -ne 0 -or $version.version -ne $packageJson.version) {
    throw "installed Turnal did not report version $($packageJson.version)"
  }
}

try {
  New-Item -ItemType Directory -Path $globalPrefix -Force | Out-Null
  if (-not $archive) {
    Push-Location $repoRoot
    try {
      $packOutput = @(& $npmCommand pack --pack-destination $testRoot)
      if ($LASTEXITCODE -ne 0) {
        throw "npm pack failed with exit code $LASTEXITCODE"
      }
      $archive = Join-Path $testRoot $packOutput[-1].Trim()
      if (-not (Test-Path -LiteralPath $archive -PathType Leaf)) {
        throw "npm pack did not create the expected archive: $archive"
      }
    } finally {
      Pop-Location
    }
  }

  $originalPath = $env:PATH
  Set-ExecutionPolicy -Scope Process -ExecutionPolicy Restricted -Force

  Invoke-Npm -Arguments @('install', '--global', '--prefix', $globalPrefix, $archive)
  Assert-InstalledLaunchers -BinDirectory $globalPrefix
  $env:PATH = "$globalPrefix;$originalPath"
  Assert-TurnalResolution -BinDirectory $globalPrefix
  Assert-TurnalVersion

  Invoke-Npm -Arguments @('install', '--global', '--prefix', $globalPrefix, $archive)
  Assert-InstalledLaunchers -BinDirectory $globalPrefix
  Assert-TurnalVersion

  Invoke-Npm -Arguments @('uninstall', '--global', '--prefix', $globalPrefix, $packageJson.name)
  Assert-LaunchersRemoved -BinDirectory $globalPrefix

  Invoke-Npm -Arguments @('install', '--prefix', $localPrefix, $archive)
  Assert-InstalledLaunchers -BinDirectory $localBin
  $env:PATH = "$localBin;$originalPath"
  Assert-TurnalResolution -BinDirectory $localBin
  Assert-TurnalVersion

  Invoke-Npm -Arguments @('install', '--prefix', $localPrefix, $archive)
  Assert-InstalledLaunchers -BinDirectory $localBin
  Assert-TurnalVersion

  Invoke-Npm -Arguments @('uninstall', '--prefix', $localPrefix, $packageJson.name)
  Assert-LaunchersRemoved -BinDirectory $localBin
} finally {
  if ($removeArchive -and $archive -and (Test-Path -LiteralPath $archive)) {
    Remove-Item -LiteralPath $archive -Force
  }
  if (Test-Path -LiteralPath $testRoot) {
    Remove-Item -LiteralPath $testRoot -Recurse -Force
  }
}
