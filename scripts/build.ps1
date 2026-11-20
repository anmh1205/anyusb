[CmdletBinding()]
param(
    [string] $Go = 'go',
    [string[]] $Targets = @('windows-amd64', 'windows-arm64', 'linux-amd64', 'linux-arm64')
)
$ErrorActionPreference = 'Stop'
Push-Location (Join-Path $PSScriptRoot '..')
$previousOS = $env:GOOS
$previousArch = $env:GOARCH
$previousCGO = $env:CGO_ENABLED
try {
    foreach ($target in $Targets) {
        if ($target -notmatch '^(windows|linux)-(amd64|arm64)$') { throw "Unsupported target $target" }
        $env:GOOS = $Matches[1]
        $env:GOARCH = $Matches[2]
        $env:CGO_ENABLED = '0'
        $extension = if ($env:GOOS -eq 'windows') { '.exe' } else { '' }
        $destination = Join-Path 'dist' $target
        New-Item -ItemType Directory -Path $destination -Force | Out-Null
        & $Go build -trimpath -o (Join-Path $destination "anyusbd$extension") ./agent/cmd/anyusbd
        if ($LASTEXITCODE -ne 0) { throw "Agent build failed for $target" }
        & $Go build -trimpath -o (Join-Path $destination "anyusb$extension") ./cli
        if ($LASTEXITCODE -ne 0) { throw "CLI build failed for $target" }
    }
} finally {
    $env:GOOS = $previousOS
    $env:GOARCH = $previousArch
    $env:CGO_ENABLED = $previousCGO
    Pop-Location
}
