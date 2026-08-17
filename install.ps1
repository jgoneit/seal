$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

function Get-SealInstallerException {
    param([string]$Message)
    return [System.InvalidOperationException]::new($Message)
}

function Get-SealFileSHA256 {
    param([string]$Path)

    $sha256 = [System.Security.Cryptography.SHA256]::Create()
    try {
        $stream = [System.IO.File]::OpenRead($Path)
        try {
            $hashBytes = $sha256.ComputeHash($stream)
        } finally {
            $stream.Dispose()
        }
    } finally {
        $sha256.Dispose()
    }
    $builder = [System.Text.StringBuilder]::new(64)
    foreach ($hashByte in $hashBytes) {
        [void]$builder.Append($hashByte.ToString("x2", [System.Globalization.CultureInfo]::InvariantCulture))
    }
    return $builder.ToString()
}

function Get-SealFileOperationException {
    param([System.Exception]$Exception)

    $current = $Exception
    while ($null -ne $current) {
        if ($current -is [System.IO.IOException] -or $current -is [System.UnauthorizedAccessException] -or $current -is [System.ComponentModel.Win32Exception]) {
            return $current
        }
        $current = $current.InnerException
    }
    return $null
}

function Get-SealFileOperationCode {
    param([System.Exception]$Exception)

    if ($null -eq $Exception) {
        return -1
    }
    if ($Exception -is [System.ComponentModel.Win32Exception]) {
        return $Exception.NativeErrorCode
    }
    return $Exception.HResult -band 0xffff
}

if ($args.Count -ne 2 -or $args[0] -cne "-Version") {
    [Console]::Error.WriteLine("Usage: install.ps1 -Version <TAG>")
    exit 2
}

$tag = [string]$args[1]
if ($tag -cnotmatch '^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[1-9][0-9]*)?\z') {
    [Console]::Error.WriteLine("seal installer: TAG must match vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-rc.N.")
    exit 1
}

$version = $tag.Substring(1)
$temporaryDirectory = $null
$destinationStage = $null
$destinationBackup = $null
$target = $null
$hadExistingTarget = $false
$replacementPending = $false
$replacementApplied = $false
$candidateSHA256 = $null
$originalTargetSHA256 = $null
$installerExitCode = 0

try {
    if ($env:OS -cne "Windows_NT") {
        throw (Get-SealInstallerException "this installer supports only Windows.")
    }
    Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
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

public static class SealNativeFile
{
    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, EntryPoint = "ReplaceFileW", ExactSpelling = true, SetLastError = true)]
    private static extern bool ReplaceFile(
        string replacedFileName,
        string replacementFileName,
        string backupFileName,
        uint replaceFlags,
        IntPtr exclude,
        IntPtr reserved);

    public static void ReplaceWithoutBackup(string replacementFileName, string replacedFileName)
    {
        if (!ReplaceFile(replacedFileName, replacementFileName, null, 0, IntPtr.Zero, IntPtr.Zero))
        {
            throw new Win32Exception(Marshal.GetLastWin32Error());
        }
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
    $actualChecksum = Get-SealFileSHA256 $archivePath
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
    $candidateSHA256 = Get-SealFileSHA256 $candidate

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
        $originalTargetSHA256 = Get-SealFileSHA256 $target
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
    $replacementApplied = $true
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
                    $restoreTimer = [System.Diagnostics.Stopwatch]::StartNew()
                    while ($true) {
                        try {
                            if (-not [System.IO.File]::Exists($destinationBackup)) {
                                throw (Get-SealInstallerException "the installer backup disappeared before rollback.")
                            }
                            $backupAttributes = [System.IO.File]::GetAttributes($destinationBackup)
                            if (($backupAttributes -band [System.IO.FileAttributes]::Directory) -ne 0 -or ($backupAttributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
                                throw (Get-SealInstallerException "the installer backup is not a regular file.")
                            }
                            if ((Get-SealFileSHA256 $destinationBackup) -cne $originalTargetSHA256) {
                                throw (Get-SealInstallerException "the installer backup changed before rollback.")
                            }
                            if ([System.IO.Directory]::Exists($target)) {
                                throw (Get-SealInstallerException "the install target became a directory before rollback.")
                            }
                            if ([System.IO.File]::Exists($target)) {
                                $targetAttributes = [System.IO.File]::GetAttributes($target)
                                if (($targetAttributes -band [System.IO.FileAttributes]::Directory) -ne 0 -or ($targetAttributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
                                    throw (Get-SealInstallerException "the install target is not a regular file during rollback.")
                                }
                                if ((Get-SealFileSHA256 $target) -cne $candidateSHA256) {
                                    throw (Get-SealInstallerException "the install target changed before rollback.")
                                }
                                [SealNativeFile]::ReplaceWithoutBackup($destinationBackup, $target)
                            } else {
                                [System.IO.File]::Move($destinationBackup, $target)
                            }
                            break
                        } catch {
                            $restoreError = Get-SealFileOperationException $_.Exception
                            $restoreCode = Get-SealFileOperationCode $restoreError
                            $retryableRestore = $restoreCode -eq 5 -or $restoreCode -eq 32 -or $restoreCode -eq 33 -or $restoreCode -eq 1175 -or $restoreCode -eq 1224
                            $restoreNamesIntact = [System.IO.File]::Exists($destinationBackup) -and [System.IO.File]::Exists($target)
                            if (-not $retryableRestore -or -not $restoreNamesIntact -or $restoreTimer.Elapsed -ge [System.TimeSpan]::FromSeconds(5)) {
                                throw
                            }
                            [System.Threading.Thread]::Sleep(50)
                        }
                    }
                    $restoreTimer.Stop()
                    $destinationBackup = $null
                }
            } else {
                $cleanReplacementApplied = $replacementApplied
                if (-not $cleanReplacementApplied -and $null -ne $destinationStage -and -not [System.IO.File]::Exists($destinationStage) -and $null -ne $target -and [System.IO.File]::Exists($target)) {
                    $targetAttributes = [System.IO.File]::GetAttributes($target)
                    $targetIsRegular = ($targetAttributes -band [System.IO.FileAttributes]::Directory) -eq 0 -and ($targetAttributes -band [System.IO.FileAttributes]::ReparsePoint) -eq 0
                    $cleanReplacementApplied = $targetIsRegular -and (Get-SealFileSHA256 $target) -ceq $candidateSHA256
                }
                if ($cleanReplacementApplied -and $null -ne $target -and [System.IO.File]::Exists($target)) {
                    $deleteTimer = [System.Diagnostics.Stopwatch]::StartNew()
                    while ($true) {
                        try {
                            $targetAttributes = [System.IO.File]::GetAttributes($target)
                            if (($targetAttributes -band [System.IO.FileAttributes]::Directory) -ne 0 -or ($targetAttributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
                                throw (Get-SealInstallerException "the install target is not a regular file during rollback.")
                            }
                            if ((Get-SealFileSHA256 $target) -cne $candidateSHA256) {
                                throw (Get-SealInstallerException "the install target changed before rollback.")
                            }
                            [System.IO.File]::Delete($target)
                            break
                        } catch {
                            $deleteError = Get-SealFileOperationException $_.Exception
                            $deleteCode = Get-SealFileOperationCode $deleteError
                            $retryableDelete = $deleteCode -eq 5 -or $deleteCode -eq 32 -or $deleteCode -eq 33 -or $deleteCode -eq 1224
                            if (-not $retryableDelete -or -not [System.IO.File]::Exists($target) -or $deleteTimer.Elapsed -ge [System.TimeSpan]::FromSeconds(5)) {
                                throw
                            }
                            [System.Threading.Thread]::Sleep(50)
                        }
                    }
                    $deleteTimer.Stop()
                }
            }
        } catch {
            $installerExitCode = 1
            $rollbackError = Get-SealFileOperationException $_.Exception
            if ($null -eq $rollbackError) {
                $rollbackError = $_.Exception
                while ($null -ne $rollbackError.InnerException) {
                    $rollbackError = $rollbackError.InnerException
                }
            }
            $rollbackHResult = "0x{0:x8}" -f $rollbackError.HResult
            $rollbackCode = Get-SealFileOperationCode $rollbackError
            if ($null -ne $destinationBackup -and [System.IO.File]::Exists($destinationBackup)) {
                $preservedBackup = $destinationBackup
                $destinationBackup = $null
                [Console]::Error.WriteLine("seal installer: rollback failed; the prior binary remains at $preservedBackup; $($rollbackError.GetType().FullName) HResult $rollbackHResult, Win32 $rollbackCode`: $($rollbackError.Message)")
            } else {
                [Console]::Error.WriteLine("seal installer: rollback failed; $($rollbackError.GetType().FullName) HResult $rollbackHResult, Win32 $rollbackCode`: $($rollbackError.Message)")
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
