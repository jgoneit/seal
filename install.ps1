$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

function Get-SealInstallerException {
    param([string]$Message)
    return [System.InvalidOperationException]::new($Message)
}

if ($args.Count -ne 2 -or $args[0] -cne "-Version") {
    [Console]::Error.WriteLine("Usage: install.ps1 -Version <TAG>")
    exit 2
}

$tag = [string]$args[1]
if ($tag -cnotmatch '^v[0-9][0-9A-Za-z._-]*$') {
    [Console]::Error.WriteLine("seal installer: TAG must start with v followed by a digit and contain only release-safe characters.")
    exit 1
}

$version = $tag.Substring(1)
$temporaryDirectory = $null
$destinationStage = $null
$destinationBackup = $null
$target = $null
$hadExistingTarget = $false
$replacementPending = $false
$installerExitCode = 0

try {
    if ($env:OS -cne "Windows_NT") {
        throw (Get-SealInstallerException "this installer supports only Windows.")
    }
    Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;

public static class SealNativeSystemInfo
{
    [StructLayout(LayoutKind.Sequential)]
    private struct SYSTEM_INFO
    {
        internal ushort ProcessorArchitecture;
        internal ushort Reserved;
        internal uint PageSize;
        internal IntPtr MinimumApplicationAddress;
        internal IntPtr MaximumApplicationAddress;
        internal UIntPtr ActiveProcessorMask;
        internal uint NumberOfProcessors;
        internal uint ProcessorType;
        internal uint AllocationGranularity;
        internal ushort ProcessorLevel;
        internal ushort ProcessorRevision;
    }

    [DllImport("kernel32.dll")]
    private static extern void GetNativeSystemInfo(out SYSTEM_INFO systemInfo);

    public static ushort ProcessorArchitecture()
    {
        SYSTEM_INFO systemInfo;
        GetNativeSystemInfo(out systemInfo);
        return systemInfo.ProcessorArchitecture;
    }
}
'@
    if ([SealNativeSystemInfo]::ProcessorArchitecture() -ne 9) {
        throw (Get-SealInstallerException "this installer supports only Windows amd64.")
    }

    # This override exists only so repository integration tests can use a
    # local release server.
    $releaseBase = if ([string]::IsNullOrWhiteSpace($env:SEAL_RELEASE_BASE_URL)) {
        "https://github.com/jgoneit/seal/releases/download"
    } else {
        $env:SEAL_RELEASE_BASE_URL.TrimEnd('/')
    }
    if ([string]::IsNullOrWhiteSpace($env:LOCALAPPDATA)) {
        throw (Get-SealInstallerException "LOCALAPPDATA is required to select the install directory.")
    }
    $installDirectory = Join-Path $env:LOCALAPPDATA "Programs\Seal\bin"
    $isDriveAbsolute = $installDirectory -cmatch '^[A-Za-z]:[\\/]'
    $isUncAbsolute = $installDirectory -cmatch '^\\\\[^\\/]+[\\/][^\\/]+'
    if (-not $isDriveAbsolute -and -not $isUncAbsolute) {
        throw (Get-SealInstallerException "the install directory must be an absolute path.")
    }

    $asset = "seal_${version}_windows_amd64.zip"
    $temporaryDirectory = Join-Path ([System.IO.Path]::GetTempPath()) ("seal-install-" + [Guid]::NewGuid().ToString("N"))
    [System.IO.Directory]::CreateDirectory($temporaryDirectory) | Out-Null
    $archivePath = Join-Path $temporaryDirectory $asset
    $checksumsPath = Join-Path $temporaryDirectory "checksums.txt"

    Invoke-WebRequest -UseBasicParsing -Uri "$releaseBase/$tag/$asset" -OutFile $archivePath
    Invoke-WebRequest -UseBasicParsing -Uri "$releaseBase/$tag/checksums.txt" -OutFile $checksumsPath

    $checksumMatches = [System.Collections.Generic.List[string]]::new()
    foreach ($line in [System.IO.File]::ReadAllLines($checksumsPath)) {
        $fields = @([System.Text.RegularExpressions.Regex]::Split($line.Trim(), '\s+'))
        if ($fields.Count -ge 2 -and $fields[1] -ceq $asset) {
            if ($line -cmatch '^([0-9a-f]{64})  ([^ ]+)$' -and $Matches[2] -ceq $asset) {
                $checksumMatches.Add($Matches[1])
            } else {
                $checksumMatches.Add("INVALID")
            }
        }
    }
    if ($checksumMatches.Count -ne 1) {
        throw (Get-SealInstallerException "checksums.txt must contain exactly one entry for $asset.")
    }
    $expectedChecksum = $checksumMatches[0]
    if ($expectedChecksum -cnotmatch '^[0-9a-f]{64}$') {
        throw (Get-SealInstallerException "checksums.txt has an invalid SHA-256 for $asset.")
    }
    $actualChecksum = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actualChecksum -cne $expectedChecksum) {
        throw (Get-SealInstallerException "SHA-256 mismatch for $asset.")
    }

    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $archive = [System.IO.Compression.ZipFile]::OpenRead($archivePath)
    try {
        if ($archive.Entries.Count -ne 1 -or $archive.Entries[0].FullName -cne "seal.exe") {
            throw (Get-SealInstallerException "$asset must contain exactly one seal.exe binary.")
        }
    } finally {
        $archive.Dispose()
    }
    [System.IO.Compression.ZipFile]::ExtractToDirectory($archivePath, $temporaryDirectory)
    $candidate = Join-Path $temporaryDirectory "seal.exe"
    $candidateItem = Get-Item -LiteralPath $candidate -Force
    if ($candidateItem.PSIsContainer -or (($candidateItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0)) {
        throw (Get-SealInstallerException "$asset does not contain a regular seal.exe binary.")
    }

    $versionErrorPath = Join-Path $temporaryDirectory "version.err"
    function Test-RequestedVersion {
        param([string]$BinaryPath)
        [System.IO.File]::WriteAllBytes($versionErrorPath, [byte[]]::new(0))
        $output = @(& $BinaryPath --version 2> $versionErrorPath)
        $exitCode = $LASTEXITCODE
        if ($exitCode -ne 0 -or (Get-Item -LiteralPath $versionErrorPath).Length -ne 0) {
            return $false
        }
        return $output.Count -eq 1 -and ([string]$output[0]) -ceq $version
    }

    if (-not (Test-RequestedVersion $candidate)) {
        throw (Get-SealInstallerException "$asset does not report requested version $version.")
    }

    [System.IO.Directory]::CreateDirectory($installDirectory) | Out-Null
    $target = Join-Path $installDirectory "seal.exe"
    $hadExistingTarget = [System.IO.File]::Exists($target)
    if (Test-Path -LiteralPath $target) {
        $targetItem = Get-Item -LiteralPath $target -Force
        if ($targetItem.PSIsContainer -or (($targetItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0)) {
            throw (Get-SealInstallerException "$target is not a regular file.")
        }
    }

    $destinationStage = Join-Path $installDirectory (".seal-install-" + [Guid]::NewGuid().ToString("N") + ".tmp")
    [System.IO.File]::Copy($candidate, $destinationStage, $false)

    $replacementPending = $true
    if ($hadExistingTarget) {
        $destinationBackup = Join-Path $installDirectory (".seal-backup-" + [Guid]::NewGuid().ToString("N") + ".tmp")
        [System.IO.File]::Replace($destinationStage, $target, $destinationBackup, $true)
    } else {
        [System.IO.File]::Move($destinationStage, $target)
    }
    $destinationStage = $null

    if (-not (Test-RequestedVersion $target)) {
        throw (Get-SealInstallerException "the installed binary failed its absolute-path version smoke test.")
    }

    # This is the commit point. Until the installed absolute path has passed,
    # the outer finally block owns rollback, including PowerShell interruption.
    $replacementPending = $false

    if ($null -ne $destinationBackup) {
        [System.IO.File]::Delete($destinationBackup)
        $destinationBackup = $null
    }

    Write-Output "Installed Seal $version at $target"
    $pathEntries = @(([string]$env:Path).Split(';', [System.StringSplitOptions]::RemoveEmptyEntries))
    if ($pathEntries -notcontains $installDirectory) {
        Write-Output "Add $installDirectory to PATH to invoke seal by name."
    }
    Write-Output "Git is required when Seal evaluates a repository; this installer does not install Git."
} catch {
    [Console]::Error.WriteLine("seal installer: " + $_.Exception.Message)
    $installerExitCode = 1
} finally {
    if ($replacementPending) {
        try {
            if ($hadExistingTarget) {
                if ($null -ne $destinationBackup -and [System.IO.File]::Exists($destinationBackup)) {
                    if ($null -ne $target -and [System.IO.File]::Exists($target)) {
                        [System.IO.File]::Replace($destinationBackup, $target, $null, $true)
                    } else {
                        [System.IO.File]::Move($destinationBackup, $target)
                    }
                    $destinationBackup = $null
                }
            } elseif ($null -ne $target -and [System.IO.File]::Exists($target)) {
                [System.IO.File]::Delete($target)
            }
        } catch {
            $installerExitCode = 1
            if ($null -ne $destinationBackup -and [System.IO.File]::Exists($destinationBackup)) {
                $preservedBackup = $destinationBackup
                $destinationBackup = $null
                [Console]::Error.WriteLine("seal installer: rollback failed; the prior binary remains at $preservedBackup")
            } else {
                [Console]::Error.WriteLine("seal installer: rollback failed: " + $_.Exception.Message)
            }
        }
    }
    if ($null -ne $destinationStage -and [System.IO.File]::Exists($destinationStage)) {
        try {
            [System.IO.File]::Delete($destinationStage)
        } catch {
            $installerExitCode = 1
            [Console]::Error.WriteLine("seal installer: could not remove staging file $destinationStage")
        }
    }
    if ($null -ne $destinationBackup -and [System.IO.File]::Exists($destinationBackup)) {
        try {
            [System.IO.File]::Delete($destinationBackup)
        } catch {
            $installerExitCode = 1
            [Console]::Error.WriteLine("seal installer: could not remove backup file $destinationBackup")
        }
    }
    if ($null -ne $temporaryDirectory -and [System.IO.Directory]::Exists($temporaryDirectory)) {
        try {
            [System.IO.Directory]::Delete($temporaryDirectory, $true)
        } catch {
            $installerExitCode = 1
            [Console]::Error.WriteLine("seal installer: could not remove temporary directory $temporaryDirectory")
        }
    }
}

if ($installerExitCode -ne 0) {
    exit $installerExitCode
}
