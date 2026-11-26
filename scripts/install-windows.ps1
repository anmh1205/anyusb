#Requires -RunAsAdministrator
[CmdletBinding()]
param(
    [Parameter(Mandatory)] [string] $BuildDirectory,
    [string] $Python,
    [string[]] $PeerSources = @(),
    [switch] $KeepStockUsbipRule,
    [switch] $DesktopInstaller,
    [switch] $Desktop,
    [switch] $Upgrade
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch {}
$build = (Resolve-Path -LiteralPath $BuildDirectory).Path
foreach ($file in @('anyusbd.exe', 'anyusb.exe')) {
    if (!(Test-Path -LiteralPath (Join-Path $build $file) -PathType Leaf)) { throw "Missing $file" }
}
$existingService = Get-Service -Name anyusbd -ErrorAction SilentlyContinue
if ($existingService -and !$DesktopInstaller -and !$Upgrade) {
    throw 'The anyusbd service already exists. Re-run with -Upgrade to replace it in place, or stop and remove it first.'
}
if ($existingService -and $existingService.Status -ne 'Stopped') {
    Write-Host 'Stopping existing anyusbd service...'
    Stop-Service -Name anyusbd -Force
    (Get-Service -Name anyusbd).WaitForStatus('Stopped', [TimeSpan]::FromSeconds(15))
}
$architecture = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'x64' }
if ($DesktopInstaller -and $PeerSources.Count -eq 0) {
    $PeerSources = @('LocalSubnet', '100.64.0.0/10', 'fd7a:115c:a1e0::/48')
}
$assets = @(
    @{
        Name = "usbipd-win_5.3.0_$architecture.msi"; Repo = 'dorssel/usbipd-win'; Tag = 'v5.3.0'
        Exe = 'usbipd.exe'; Folder = 'usbipd-win'; Version = '5.3.0'; Description = 'VBoxUSB and VBoxUSBMon drivers, usbipd service and default TCP 3240 local-subnet rule'
    },
    @{
        Name = "USBip-0.9.8.1-$architecture.exe"; Repo = 'vadimgrn/usbip-win2'; Tag = 'v.0.9.8.1'
        Exe = 'usbip.exe'; Folder = 'USBip'; Version = '0.9.8.1'; Description = 'usbip2_ude and usbip2_filter drivers, detached-device shutdown task, USB device restart and possible reboot'
    }
)
foreach ($asset in $assets) {
    Write-Host "$($asset.Name) installs $($asset.Description)."
}
if (!$KeepStockUsbipRule) { Write-Host 'Disable the upstream inbound usbipd rule for managed mode.' }
if ($PeerSources.Count -gt 0) {
    Write-Host "Allow inbound HTTPS peer traffic TCP 47443 from $($PeerSources -join ', ')."
} else {
    Write-Host 'Note: No -PeerSources specified; inbound TCP 47443 firewall rule will NOT be added.'
}
Write-Host 'Install the AnyUSB Windows service. Windows installers will display their own UI.'
if (!$DesktopInstaller -and (Read-Host 'Type INSTALL to continue') -cne 'INSTALL') { return }

$download = Join-Path $env:TEMP ('anyusb-install-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $download | Out-Null
$restartNeeded = $false
try {
foreach ($asset in $assets) {
    $installed = Join-Path $env:ProgramFiles (Join-Path $asset.Folder $asset.Exe)
    if (Test-Path -LiteralPath $installed -PathType Leaf) {
        $foundVersion = & $installed --version
        if ($LASTEXITCODE -eq 0 -and ($foundVersion -join ' ').Contains($asset.Version)) {
            Write-Host "Using $($asset.Exe) $($asset.Version)."
            continue
        }
        Write-Host "Installing the validated release $($asset.Version) for $($asset.Exe)."
    }
    Write-Host "Downloading $($asset.Name) from GitHub releases..."
    $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$($asset.Repo)/releases/tags/$($asset.Tag)"
    $metadata = $release.assets | Where-Object name -eq $asset.Name | Select-Object -First 1
    if (!$metadata -or $metadata.digest -notmatch '^sha256:[a-f0-9]{64}$') { throw "Missing release hash for $($asset.Name)" }
    $installer = Join-Path $download $asset.Name
    Invoke-WebRequest -UseBasicParsing -Uri $metadata.browser_download_url -OutFile $installer
    Write-Host "Verifying SHA-256 and Authenticode signature for $($asset.Name)..."
    if (('sha256:' + (Get-FileHash -LiteralPath $installer).Hash) -ne $metadata.digest -or
        (Get-AuthenticodeSignature -LiteralPath $installer).Status -ne 'Valid') { throw "Signature or hash check failed for $installer" }
    Write-Host "Installing $($asset.Name)..."
    if ($installer.EndsWith('.msi')) {
        $switches = if ($DesktopInstaller) { '/qn /norestart' } else { '/norestart' }
        $result = Start-Process -FilePath "$env:SystemRoot\System32\msiexec.exe" -ArgumentList "/i `"$installer`" $switches" -Wait -PassThru
    } else {
        $switches = if ($DesktopInstaller) { '/VERYSILENT /SUPPRESSMSGBOXES /SP- /NORESTART' } else { '/NORESTART' }
        $result = Start-Process -FilePath $installer -ArgumentList $switches -Wait -PassThru
    }
    if ($result.ExitCode -notin @(0, 3010)) { throw "$($asset.Name) returned $($result.ExitCode)" }
    if ($result.ExitCode -eq 3010 -or $installer.EndsWith('.exe')) {
        $restartNeeded = $true
        Write-Host 'Reboot Windows before using the USB/IP driver.'
    }
}
} finally {
    Remove-Item -LiteralPath $download -Recurse -Force -ErrorAction SilentlyContinue
}

if (!$KeepStockUsbipRule) {
    Get-NetFirewallRule -DisplayName usbipd -ErrorAction SilentlyContinue | Disable-NetFirewallRule
}
if ($PeerSources.Count -gt 0) {
    $old = Get-NetFirewallRule -Name 'AnyUSB-Peer' -ErrorAction SilentlyContinue
    if ($old -and !$DesktopInstaller -and !$Upgrade) { throw 'An AnyUSB-Peer firewall rule already exists. Review the existing scope.' }
    if (!$old) {
        New-NetFirewallRule -Name 'AnyUSB-Peer' -DisplayName 'AnyUSB peer HTTPS' -Direction Inbound -Action Allow -Protocol TCP -LocalPort 47443 -RemoteAddress $PeerSources | Out-Null
    } else {
        Write-Host 'Keeping the existing AnyUSB peer firewall scope during upgrade.'
    }
}

$destination = Join-Path $env:ProgramFiles 'AnyUSB'
$data = Join-Path $env:ProgramData 'AnyUSB'
foreach ($directory in @($destination, $data)) {
    New-Item -ItemType Directory -Path $directory -Force | Out-Null
    if ((Get-Item -LiteralPath $directory -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw "Refusing reparse point $directory" }
    $access = @('*S-1-5-18:(OI)(CI)F', '*S-1-5-32-544:(OI)(CI)F')
    if ($directory -eq $destination) { $access += '*S-1-5-32-545:(OI)(CI)RX' }
    & icacls.exe $directory /inheritance:r /grant:r @access | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "Failed to protect $directory" }
}
if ($build.TrimEnd('\') -ne $destination.TrimEnd('\')) {
    Copy-Item -LiteralPath (Join-Path $build 'anyusbd.exe'), (Join-Path $build 'anyusb.exe') -Destination $destination
}
$binary = '"' + (Join-Path $destination 'anyusbd.exe') + '"'
if ($DesktopInstaller) {
    $binary += ' -desktop -usb-only'
} elseif ($Desktop) {
    $binary += ' -desktop'
}
if ($DesktopInstaller -or $Desktop) {
    if (!(Get-NetFirewallRule -Name 'AnyUSB-LAN-Discovery' -ErrorAction SilentlyContinue)) {
        New-NetFirewallRule -Name 'AnyUSB-LAN-Discovery' -DisplayName 'AnyUSB LAN discovery' -Direction Inbound -Action Allow -Protocol UDP -LocalPort 5353 -RemoteAddress LocalSubnet -Program (Join-Path $destination 'anyusbd.exe') | Out-Null
    }
}
if ($Python) {
    Write-Warning '-Python is deprecated and no longer required; AnyUSB includes a built-in ESP engine.'
}
if ($existingService) {
    & "$env:SystemRoot\System32\sc.exe" config anyusbd 'binPath=' $binary 'start=' 'auto' | Out-Host
    if ($LASTEXITCODE -ne 0) { throw 'Failed to update the existing anyusbd service.' }
} else {
    New-Service -Name anyusbd -DisplayName 'AnyUSB Agent' -BinaryPathName $binary -StartupType Automatic | Out-Null
}
& "$env:SystemRoot\System32\sc.exe" failure anyusbd reset= 86400 actions= restart/2000/restart/5000/restart/10000 | Out-Null
Start-Service -Name anyusbd
& (Join-Path $destination 'anyusb.exe') doctor
if ($DesktopInstaller -and $restartNeeded) { exit 3010 }
