[CmdletBinding()]
param(
    [switch]$SkipTests
)

$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

if (-not $SkipTests) {
    Write-Host "Running tests..."
    go test ./...
}

# The icon and DPI manifest are compiled into rsrc_windows_amd64.syso. To
# regenerate it after changing assets/icon.ico or app.manifest:
#   go run github.com/akavel/rsrc@latest -manifest app.manifest -ico assets/icon.ico -o rsrc_windows_amd64.syso

Write-Host "Building desktop app..."
go build -trimpath -tags desktopapp -ldflags "-H windowsgui -s -w" -o Tokenizer.exe .

Write-Host "Building browser server..."
go build -trimpath -ldflags "-s -w" -o TokenizerServer.exe .

Write-Host ""
Write-Host "Built:"
Write-Host "  Tokenizer.exe        desktop application (double-click to run)"
Write-Host "  TokenizerServer.exe  local web server -> http://127.0.0.1:8080"
