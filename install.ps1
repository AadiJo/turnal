[CmdletBinding()]
param(
    [string]$Version = $env:TURNAL_VERSION,
    [string]$InstallDir = $env:TURNAL_INSTALL_DIR,
    [switch]$NoPathUpdate,
    [switch]$Help
)

& {
    [CmdletBinding()]
    param(
        [string]$RequestedVersion,
        [string]$RequestedInstallDir,
        [bool]$SkipPathUpdate,
        [bool]$ShowHelp
    )

    Set-StrictMode -Version 2.0
    $ErrorActionPreference = 'Stop'
    $previousProgressPreference = $ProgressPreference
    $ProgressPreference = 'SilentlyContinue'

    $executables = @(
        'turnal.exe'
        'turnal-adapter-opencode.exe'
        'turnal-adapter-gemini-cli.exe'
        'turnal-adapter-copilot-cli.exe'
    )
    $documentation = @('LICENSE', 'NOTICE')
    $archiveMembers = @($executables) + $documentation

    function Write-Usage {
        @'
Install Turnal on Windows.

Usage: install.ps1 [-Version VERSION] [-InstallDir DIRECTORY] [-NoPathUpdate]

Options:
  -Version VERSION       Install a specific version instead of the latest stable release.
  -InstallDir DIRECTORY  Install executables into DIRECTORY.
                         Default: %LOCALAPPDATA%\Programs\Turnal\bin
  -NoPathUpdate          Do not add the installation directory to the user PATH.
  -Help                  Show this help.
'@ | Write-Output
    }

    function Fail([string]$Message) {
        throw "turnal installer: $Message"
    }

    function Warn([string]$Message) {
        Write-Warning "turnal installer: $Message"
    }

    function Test-InstallerMode {
        return $env:TURNAL_INSTALLER_TESTING -eq '1'
    }

    function Invoke-Download([string]$Url, [string]$Destination) {
        $uri = [Uri]$Url
        $allowInsecure = $env:TURNAL_ALLOW_INSECURE_TRANSPORT -eq '1'
        if ($uri.Scheme -ne 'https' -and -not $allowInsecure) {
            Fail "refusing non-HTTPS download URL: $Url"
        }
        if ($uri.IsFile) {
            if (-not (Test-InstallerMode) -or -not $allowInsecure) {
                Fail 'file downloads are only available to installer tests'
            }
            Copy-Item -LiteralPath $uri.LocalPath -Destination $Destination
            return
        }
        Invoke-WebRequest -UseBasicParsing -Uri $uri -OutFile $Destination
    }

    function Resolve-Architecture {
        if ((Test-InstallerMode) -and $env:TURNAL_TEST_ARCHITECTURE) {
            $architecture = $env:TURNAL_TEST_ARCHITECTURE
        }
        else {
            try {
                $architecture = [Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
            }
            catch {
                $architecture = $env:PROCESSOR_ARCHITECTURE
            }
        }
        switch ($architecture.ToUpperInvariant()) {
            { $_ -in @('X64', 'AMD64') } { return 'amd64' }
            'ARM64' { return 'arm64' }
            default { Fail "unsupported Windows architecture: $architecture" }
        }
    }

    function Get-DefaultInstallDir {
        if ($env:LOCALAPPDATA) {
            return Join-Path $env:LOCALAPPDATA 'Programs\Turnal\bin'
        }
        return Join-Path $HOME '.local\bin'
    }

    function Get-UserPath {
        if ((Test-InstallerMode) -and $env:TURNAL_TEST_USER_PATH_FILE) {
            if (Test-Path -LiteralPath $env:TURNAL_TEST_USER_PATH_FILE) {
                return [IO.File]::ReadAllText($env:TURNAL_TEST_USER_PATH_FILE)
            }
            return ''
        }
        return [Environment]::GetEnvironmentVariable('Path', 'User')
    }

    function Set-UserPath([string]$Value) {
        if ((Test-InstallerMode) -and $env:TURNAL_TEST_USER_PATH_FILE) {
            [IO.File]::WriteAllText($env:TURNAL_TEST_USER_PATH_FILE, $Value)
            return
        }
        [Environment]::SetEnvironmentVariable('Path', $Value, 'User')
    }

    function Normalize-PathEntry([string]$Value) {
        if ([string]::IsNullOrWhiteSpace($Value)) {
            return ''
        }
        try {
            return [IO.Path]::GetFullPath($Value.Trim()).TrimEnd('\', '/')
        }
        catch {
            return $Value.Trim().TrimEnd('\', '/')
        }
    }

    function Add-InstallDirToPath([string]$Directory) {
        $normalizedDirectory = Normalize-PathEntry $Directory
        $userPath = Get-UserPath
        $userEntries = @($userPath -split ';' | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
        $alreadyPresent = $false
        foreach ($entry in $userEntries) {
            if ([string]::Equals((Normalize-PathEntry $entry), $normalizedDirectory, [StringComparison]::OrdinalIgnoreCase)) {
                $alreadyPresent = $true
                break
            }
        }
        if (-not $alreadyPresent) {
            $updatedUserPath = (@($userEntries) + $Directory) -join ';'
            Set-UserPath $updatedUserPath
        }

        $processEntries = @($env:Path -split ';' | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
        $processContainsDirectory = $false
        foreach ($entry in $processEntries) {
            if ([string]::Equals((Normalize-PathEntry $entry), $normalizedDirectory, [StringComparison]::OrdinalIgnoreCase)) {
                $processContainsDirectory = $true
                break
            }
        }
        if (-not $processContainsDirectory) {
            $env:Path = (@($processEntries) + $Directory) -join ';'
        }
        return -not $alreadyPresent
    }

    function Restore-Installation(
        [string]$Directory,
        [string]$TransactionDirectory,
        [string[]]$Touched,
        [hashtable]$HadOriginal
    ) {
        $rollbackErrors = [Collections.Generic.List[string]]::new()
        for ($index = $Touched.Count - 1; $index -ge 0; $index--) {
            $name = $Touched[$index]
            $target = Join-Path $Directory $name
            $backup = Join-Path $TransactionDirectory "$name.old"
            try {
                if (Test-Path -LiteralPath $target) {
                    Remove-Item -LiteralPath $target -Force
                }
                if ($HadOriginal[$name]) {
                    if ((Test-InstallerMode) -and $env:TURNAL_TEST_FAIL_RESTORE -eq $name) {
                        throw 'injected restore failure'
                    }
                    if (-not (Test-Path -LiteralPath $backup)) {
                        throw "backup $backup is missing"
                    }
                    Move-Item -LiteralPath $backup -Destination $target
                }
            }
            catch {
                $rollbackErrors.Add("restore ${name}: $($_.Exception.Message)")
            }
        }
        return $rollbackErrors.ToArray()
    }

    if ($ShowHelp) {
        Write-Usage
        return
    }

    $repo = if ($env:TURNAL_REPOSITORY) { $env:TURNAL_REPOSITORY } else { 'AadiJo/turnal' }
    $releaseBase = if ($env:TURNAL_RELEASE_BASE_URL) {
        $env:TURNAL_RELEASE_BASE_URL.TrimEnd('/')
    }
    else {
        "https://github.com/$repo/releases/download"
    }
    $installDirectory = if ([string]::IsNullOrWhiteSpace($RequestedInstallDir)) {
        Get-DefaultInstallDir
    }
    else {
        $RequestedInstallDir
    }
    $resolvedVersion = $RequestedVersion.Trim()
    $tempDirectory = Join-Path ([IO.Path]::GetTempPath()) ("turnal-install-" + [Guid]::NewGuid().ToString('N'))
    $transactionDirectory = $null
    $completed = $false
    $preserveTransaction = $false
    $failure = $null
    $touched = [Collections.Generic.List[string]]::new()
    $hadOriginal = @{}
    $previousSecurityProtocol = [Net.ServicePointManager]::SecurityProtocol

    try {
        [Net.ServicePointManager]::SecurityProtocol = $previousSecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
        New-Item -ItemType Directory -Path $tempDirectory | Out-Null

        if ([string]::IsNullOrWhiteSpace($resolvedVersion)) {
            $latestUrl = if ($env:TURNAL_LATEST_RELEASE_URL) {
                $env:TURNAL_LATEST_RELEASE_URL
            }
            else {
                "https://api.github.com/repos/$repo/releases/latest"
            }
            $latestPath = Join-Path $tempDirectory 'latest.json'
            try {
                Invoke-Download $latestUrl $latestPath
                $latest = [IO.File]::ReadAllText($latestPath) | ConvertFrom-Json
                $resolvedVersion = [string]$latest.tag_name
            }
            catch {
                Fail "could not resolve the latest stable release: $($_.Exception.Message)"
            }
        }
        $resolvedVersion = $resolvedVersion.TrimStart('v')
        if ($resolvedVersion -notmatch '^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$') {
            Fail "invalid version: $resolvedVersion"
        }

        $architecture = Resolve-Architecture
        $archive = "turnal_${resolvedVersion}_windows_${architecture}.tar.gz"
        $assetRoot = "$releaseBase/v$resolvedVersion"
        $archivePath = Join-Path $tempDirectory $archive
        $checksumsPath = Join-Path $tempDirectory 'checksums.txt'
        $stageDirectory = Join-Path $tempDirectory 'stage'
        New-Item -ItemType Directory -Path $stageDirectory | Out-Null

        Write-Output "Downloading Turnal $resolvedVersion for windows/$architecture..."
        try {
            Invoke-Download "$assetRoot/$archive" $archivePath
        }
        catch {
            Fail "could not download ${archive}: $($_.Exception.Message)"
        }
        try {
            Invoke-Download "$assetRoot/checksums.txt" $checksumsPath
        }
        catch {
            Fail "could not download checksums.txt: $($_.Exception.Message)"
        }

        $checksumMatches = [Collections.Generic.List[string]]::new()
        foreach ($line in [IO.File]::ReadAllLines($checksumsPath)) {
            if ($line -match '^([0-9A-Fa-f]{64})\s+\*?(.+)$' -and $Matches[2] -eq $archive) {
                $checksumMatches.Add($Matches[1])
            }
        }
        if ($checksumMatches.Count -ne 1) {
            Fail "checksums.txt must contain exactly one valid checksum for $archive"
        }
        $actualChecksum = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash
        if (-not [string]::Equals($checksumMatches[0], $actualChecksum, [StringComparison]::OrdinalIgnoreCase)) {
            Fail "checksum verification failed for $archive"
        }

        $tar = Get-Command tar.exe -ErrorAction SilentlyContinue
        if (-not $tar) {
            $tar = Get-Command tar -ErrorAction SilentlyContinue
        }
        if (-not $tar) {
            Fail 'tar is required'
        }
        $listedMembers = @(& $tar.Source -tzf $archivePath 2>&1)
        if ($LASTEXITCODE -ne 0) {
            Fail "could not inspect $archive"
        }
        $memberCounts = @{}
        foreach ($member in $listedMembers) {
            $normalized = ([string]$member).Trim()
            if ($normalized.StartsWith('./')) {
                $normalized = $normalized.Substring(2)
            }
            if ($normalized -notin $archiveMembers) {
                Fail "archive contains unexpected entry: $member"
            }
            if (-not $memberCounts.ContainsKey($normalized)) {
                $memberCounts[$normalized] = 0
            }
            $memberCounts[$normalized]++
        }
        foreach ($name in $executables) {
            if (-not $memberCounts.ContainsKey($name) -or $memberCounts[$name] -ne 1) {
                Fail "archive must contain exactly one regular candidate named $name"
            }
        }
        foreach ($name in $documentation) {
            if ($memberCounts.ContainsKey($name) -and $memberCounts[$name] -gt 1) {
                Fail "archive contains duplicate entry named $name"
            }
        }

        & $tar.Source -xzf $archivePath -C $stageDirectory
        if ($LASTEXITCODE -ne 0) {
            Fail "could not extract $archive"
        }
        foreach ($name in $archiveMembers) {
            $candidate = Join-Path $stageDirectory $name
            if (-not (Test-Path -LiteralPath $candidate)) {
                if ($name -in $documentation) {
                    continue
                }
                Fail "archive is missing $name"
            }
            $item = Get-Item -LiteralPath $candidate -Force
            if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
                Fail "archive entry $name is not a regular file"
            }
        }

        New-Item -ItemType Directory -Path $installDirectory -Force | Out-Null
        $installDirectory = (Resolve-Path -LiteralPath $installDirectory).Path
        $transactionDirectory = Join-Path $installDirectory ('.turnal-install-' + [Guid]::NewGuid().ToString('N'))
        New-Item -ItemType Directory -Path $transactionDirectory | Out-Null

        foreach ($name in $executables) {
            $candidate = Join-Path $transactionDirectory "$name.new"
            $target = Join-Path $installDirectory $name
            $backup = Join-Path $transactionDirectory "$name.old"
            Copy-Item -LiteralPath (Join-Path $stageDirectory $name) -Destination $candidate

            $originalExists = Test-Path -LiteralPath $target
            $hadOriginal[$name] = $originalExists
            if ($originalExists) {
                Move-Item -LiteralPath $target -Destination $backup
            }
            $touched.Add($name)

            if ((Test-InstallerMode) -and $env:TURNAL_TEST_FAIL_INSTALL -eq $name) {
                Fail "could not install $name (injected failure)"
            }
            try {
                Move-Item -LiteralPath $candidate -Destination $target
            }
            catch {
                Fail "could not install ${name}: $($_.Exception.Message)"
            }
        }
        $completed = $true
    }
    catch {
        $failure = $_
    }
    finally {
        if (-not $completed -and $transactionDirectory) {
            $rollbackErrors = @(Restore-Installation $installDirectory $transactionDirectory $touched.ToArray() $hadOriginal)
            if ($rollbackErrors.Count -gt 0) {
                $preserveTransaction = $true
                $message = "$($failure.Exception.Message); rollback incomplete; backups preserved in ${transactionDirectory}: $($rollbackErrors -join '; ')"
                $failure = [Management.Automation.ErrorRecord]::new(
                    [InvalidOperationException]::new($message),
                    'TurnalInstallerRollbackFailed',
                    [Management.Automation.ErrorCategory]::InvalidOperation,
                    $transactionDirectory
                )
            }
        }
        if ($transactionDirectory -and -not $preserveTransaction) {
            try {
                Remove-Item -LiteralPath $transactionDirectory -Recurse -Force
            }
            catch {
                Warn "could not remove transaction directory $transactionDirectory"
            }
        }
        if (Test-Path -LiteralPath $tempDirectory) {
            Remove-Item -LiteralPath $tempDirectory -Recurse -Force
        }
        $ProgressPreference = $previousProgressPreference
        [Net.ServicePointManager]::SecurityProtocol = $previousSecurityProtocol
    }

    if ($failure) {
        throw $failure
    }

    Write-Output "Turnal $resolvedVersion installed in $installDirectory"
    if (-not $SkipPathUpdate -and $env:TURNAL_NO_PATH_UPDATE -ne '1') {
        try {
            if (Add-InstallDirToPath $installDirectory) {
                Write-Output 'Added the installation directory to your user PATH.'
            }
        }
        catch {
            Warn "could not update the user PATH: $($_.Exception.Message)"
        }
    }
    $resolvedCommand = Get-Command turnal -ErrorAction SilentlyContinue
    if ($resolvedCommand -and $resolvedCommand.Source) {
        $expectedCommand = Normalize-PathEntry (Join-Path $installDirectory 'turnal.exe')
        if (-not [string]::Equals((Normalize-PathEntry $resolvedCommand.Source), $expectedCommand, [StringComparison]::OrdinalIgnoreCase)) {
            Warn "$($resolvedCommand.Source) shadows $(Join-Path $installDirectory 'turnal.exe') earlier in PATH"
        }
    }
} $Version $InstallDir $NoPathUpdate.IsPresent $Help.IsPresent
