[CmdletBinding()]
param()

Set-StrictMode -Version 2.0
$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
$installerPath = Join-Path $repoRoot 'install.ps1'
$tempRoot = Join-Path ([IO.Path]::GetTempPath()) ("turnal-install-test-" + [Guid]::NewGuid().ToString('N'))
$originalPath = $env:Path
$trackedEnvironment = @(
    'TURNAL_ALLOW_INSECURE_TRANSPORT'
    'TURNAL_INSTALLER_TESTING'
    'TURNAL_INSTALL_DIR'
    'TURNAL_LATEST_RELEASE_URL'
    'TURNAL_RELEASE_BASE_URL'
    'TURNAL_TEST_ARCHITECTURE'
    'TURNAL_TEST_FAIL_INSTALL'
    'TURNAL_TEST_FAIL_RESTORE'
    'TURNAL_TEST_USER_PATH_FILE'
    'TURNAL_VERSION'
)
$originalEnvironment = @{}
foreach ($name in $trackedEnvironment) {
    $originalEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}

function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) {
        throw $Message
    }
}

function Assert-Throws([scriptblock]$Action, [string]$Pattern) {
    try {
        & $Action
    }
    catch {
        if ($_.Exception.Message -notmatch $Pattern) {
            throw "error '$($_.Exception.Message)' does not match '$Pattern'"
        }
        return
    }
    throw "action succeeded; expected error matching '$Pattern'"
}

function Get-TestArchitecture {
    switch ([Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToUpperInvariant()) {
        'X64' { return 'amd64' }
        'ARM64' { return 'arm64' }
        default { throw 'installer test requires x64 or ARM64 Windows' }
    }
}

function Write-Checksum([string]$ArchivePath, [string]$ChecksumsPath) {
    $checksum = (Get-FileHash -LiteralPath $ArchivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    [IO.File]::WriteAllText($ChecksumsPath, "$checksum  $([IO.Path]::GetFileName($ArchivePath))`n")
}

function Write-FixtureArchive([string]$ArchivePath, [switch]$UnexpectedEntry) {
    $payload = Join-Path $tempRoot 'payload'
    if (Test-Path -LiteralPath $payload) {
        Remove-Item -LiteralPath $payload -Recurse -Force
    }
    New-Item -ItemType Directory -Path $payload | Out-Null
    foreach ($name in $script:executables) {
        [IO.File]::WriteAllText((Join-Path $payload $name), "new-$name`n")
    }
    [IO.File]::WriteAllText((Join-Path $payload 'LICENSE'), "license`n")
    [IO.File]::WriteAllText((Join-Path $payload 'NOTICE'), "notice`n")
    $members = @($script:executables) + @('LICENSE', 'NOTICE')
    if ($UnexpectedEntry) {
        [IO.File]::WriteAllText((Join-Path $payload 'unexpected'), "unexpected`n")
        $members += 'unexpected'
    }
    & tar.exe -czf $ArchivePath -C $payload @members
    if ($LASTEXITCODE -ne 0) {
        throw 'could not create installer fixture archive'
    }
    Write-Checksum $ArchivePath (Join-Path (Split-Path -Parent $ArchivePath) 'checksums.txt')
}

function Invoke-Installer([string]$Version, [string]$InstallDir) {
    $scriptText = [IO.File]::ReadAllText($installerPath)
    $installer = [ScriptBlock]::Create($scriptText)
    & $installer -Version $Version -InstallDir $InstallDir
}

try {
    New-Item -ItemType Directory -Path $tempRoot | Out-Null
    # Keep the fixture name distinct from install.ps1's public -Version parameter.
    # Invoke-Expression evaluates that parameter block in this scope on Windows
    # PowerShell, where an existing optimized $Version variable is not writable.
    $fixtureVersion = '9.8.7'
    $architecture = Get-TestArchitecture
    $executables = @(
        'turnal.exe'
        'turnal-adapter-opencode.exe'
        'turnal-adapter-copilot-cli.exe'
        'turnal-adapter-cursor.exe'
        'turnal-adapter-pi.exe'
    )
    $releaseDirectory = Join-Path $tempRoot "releases\v$fixtureVersion"
    New-Item -ItemType Directory -Path $releaseDirectory -Force | Out-Null
    $archiveName = "turnal_${fixtureVersion}_windows_${architecture}.tar.gz"
    $archivePath = Join-Path $releaseDirectory $archiveName
    Write-FixtureArchive $archivePath

    $pathFile = Join-Path $tempRoot 'user-path.txt'
    [IO.File]::WriteAllText($pathFile, 'C:\Existing')
    $env:TURNAL_ALLOW_INSECURE_TRANSPORT = '1'
    $env:TURNAL_INSTALLER_TESTING = '1'
    $env:TURNAL_TEST_ARCHITECTURE = $architecture
    $env:TURNAL_TEST_USER_PATH_FILE = $pathFile
    $env:TURNAL_RELEASE_BASE_URL = ([Uri](Join-Path $tempRoot 'releases')).AbsoluteUri.TrimEnd('/')

    $installDirectory = Join-Path $tempRoot 'install directory with spaces'
    Invoke-Installer $fixtureVersion $installDirectory
    foreach ($name in $executables) {
        $installed = Join-Path $installDirectory $name
        Assert-True (Test-Path -LiteralPath $installed) "missing installed $name"
        Assert-True ([IO.File]::ReadAllText($installed) -eq "new-$name`n") "installed $name bytes changed"
    }
    $pathEntries = @([IO.File]::ReadAllText($pathFile) -split ';')
    Assert-True ($pathEntries.Count -eq 2) "user PATH entries = $($pathEntries -join ', ')"
    Assert-True ($pathEntries[1] -eq $installDirectory) 'install directory was not added to the user PATH'

    Invoke-Installer $fixtureVersion $installDirectory
    $pathEntries = @([IO.File]::ReadAllText($pathFile) -split ';')
    Assert-True ($pathEntries.Count -eq 2) 'installer duplicated its user PATH entry'

    $validChecksum = [IO.File]::ReadAllText((Join-Path $releaseDirectory 'checksums.txt'))
    [IO.File]::WriteAllText(
        (Join-Path $releaseDirectory 'checksums.txt'),
        ('0' * 64) + "  $archiveName`n"
    )
    Assert-Throws { Invoke-Installer $fixtureVersion (Join-Path $tempRoot 'tampered') } 'checksum verification failed'
    [IO.File]::WriteAllText((Join-Path $releaseDirectory 'checksums.txt'), $validChecksum)

    Assert-Throws { Invoke-Installer '..' (Join-Path $tempRoot 'invalid') } 'invalid version'

    Write-FixtureArchive $archivePath -UnexpectedEntry
    Assert-Throws { Invoke-Installer $fixtureVersion (Join-Path $tempRoot 'unexpected') } 'unexpected entry'
    Write-FixtureArchive $archivePath

    $rollbackDirectory = Join-Path $tempRoot 'rollback'
    New-Item -ItemType Directory -Path $rollbackDirectory | Out-Null
    foreach ($name in $executables) {
        [IO.File]::WriteAllText((Join-Path $rollbackDirectory $name), "old-$name`n")
    }
    $env:TURNAL_TEST_FAIL_INSTALL = 'turnal-adapter-copilot-cli.exe'
    Assert-Throws { Invoke-Installer $fixtureVersion $rollbackDirectory } 'injected failure'
    $env:TURNAL_TEST_FAIL_INSTALL = $null
    foreach ($name in $executables) {
        Assert-True ([IO.File]::ReadAllText((Join-Path $rollbackDirectory $name)) -eq "old-$name`n") "rollback did not restore $name"
    }
    Assert-True (@(Get-ChildItem -LiteralPath $rollbackDirectory -Filter '.turnal-install-*').Count -eq 0) 'rollback left a transaction directory'

    $failedRollbackDirectory = Join-Path $tempRoot 'failed-rollback'
    New-Item -ItemType Directory -Path $failedRollbackDirectory | Out-Null
    foreach ($name in $executables) {
        [IO.File]::WriteAllText((Join-Path $failedRollbackDirectory $name), "old-$name`n")
    }
    $env:TURNAL_TEST_FAIL_INSTALL = 'turnal-adapter-copilot-cli.exe'
    $env:TURNAL_TEST_FAIL_RESTORE = 'turnal-adapter-opencode.exe'
    Assert-Throws { Invoke-Installer $fixtureVersion $failedRollbackDirectory } 'rollback incomplete; backups preserved in'
    $env:TURNAL_TEST_FAIL_INSTALL = $null
    $env:TURNAL_TEST_FAIL_RESTORE = $null
    $preservedTransactions = @(Get-ChildItem -LiteralPath $failedRollbackDirectory -Directory -Filter '.turnal-install-*')
    Assert-True ($preservedTransactions.Count -eq 1) 'failed rollback did not preserve exactly one transaction directory'
    Assert-True (
        [IO.File]::ReadAllText((Join-Path $preservedTransactions[0].FullName 'turnal-adapter-opencode.exe.old')) -eq "old-turnal-adapter-opencode.exe`n"
    ) 'failed rollback did not preserve the adapter backup'

    $latestPath = Join-Path $tempRoot 'latest.json'
    [IO.File]::WriteAllText($latestPath, "{`"tag_name`":`"v$fixtureVersion`"}")
    $env:TURNAL_LATEST_RELEASE_URL = ([Uri]$latestPath).AbsoluteUri
    $latestDirectory = Join-Path $tempRoot 'latest'
    Invoke-Installer '' $latestDirectory
    Assert-True (Test-Path -LiteralPath (Join-Path $latestDirectory 'turnal.exe')) 'latest release resolution did not install Turnal'

    $pipedDirectory = Join-Path $tempRoot 'piped'
    $env:TURNAL_VERSION = $fixtureVersion
    $env:TURNAL_INSTALL_DIR = $pipedDirectory
    [IO.File]::ReadAllText($installerPath) | Invoke-Expression
    $pipedTurnal = Join-Path $pipedDirectory 'turnal.exe'
    Assert-True (Test-Path -LiteralPath $pipedTurnal) 'irm-style piped execution did not install Turnal'
    Assert-True ([IO.File]::ReadAllText($pipedTurnal) -eq "new-turnal.exe`n") 'irm-style piped execution installed the wrong release'

    Write-Output 'Windows installer tests passed'
}
finally {
    $env:Path = $originalPath
    foreach ($name in $trackedEnvironment) {
        [Environment]::SetEnvironmentVariable($name, $originalEnvironment[$name], 'Process')
    }
    if (Test-Path -LiteralPath $tempRoot) {
        Remove-Item -LiteralPath $tempRoot -Recurse -Force
    }
}
