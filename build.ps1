# Builds HukukFix into build\bin\HukukFixDM.exe: a single file that installs itself (with desktop and Start menu
# shortcuts), carries the browser bridge (HukukFixDM-host.exe, embedded) and updates itself from signed GitHub releases.
#
#   powershell -ExecutionPolicy Bypass -File build.ps1                   # build
#   powershell -ExecutionPolicy Bypass -File build.ps1 -Test             # run the Go tests first
#   powershell -ExecutionPolicy Bypass -File build.ps1 -Sign <key file>  # also build\release\HukukFixDM.exe + HukukFixDM.exe.sig
#
# The two files in build\release are the release assets. The key file is the private signing key made with
# "go run ./tools/updsign keygen"; it never goes into the repository.
#
# Requires Go 1.24+ (https://go.dev/dl). Nothing else: no Node, no C compiler.
param([switch]$Test, [string]$Sign)

$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

# Use Go from PATH, or the per-user copy under %LOCALAPPDATA%\go-toolchain.
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    $local = Join-Path $env:LOCALAPPDATA 'go-toolchain\go\bin'
    if (Test-Path (Join-Path $local 'go.exe')) { $env:Path = "$local;$env:Path" }
    else { throw 'Go was not found. Install it from https://go.dev/dl and run this script again.' }
}
$gobin = Join-Path (go env GOPATH) 'bin'
$env:Path = "$gobin;$env:Path"

function Run([string]$what, [scriptblock]$cmd) {
    Write-Host "==> $what"
    & $cmd
    if ($LASTEXITCODE -ne 0) { throw "$what failed" }
}

$version = (Select-String -Path internal\shared\shared.go -Pattern 'Version\s*=\s*"([^"]+)"').Matches[0].Groups[1].Value
$product = (Get-Content wails.json -Raw | ConvertFrom-Json).info.productVersion
if ($version -ne $product) { throw "internal\shared\shared.go says $version but wails.json productVersion says $product" }

if ($Test) { Run 'tests' { go test ./internal/... -count=1 } }

Run 'icons' { go run ./tools/genicons }

if (-not (Get-Command wails -ErrorAction SilentlyContinue)) {
    Run 'installing the Wails build tool' { go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.1 }
}

New-Item -ItemType Directory -Force build\bin, hostbin | Out-Null
Run 'HukukFixDM-host.exe' { go build -trimpath -ldflags '-s -w' -o build\bin\HukukFixDM-host.exe ./cmd/hukukfix-host }
Copy-Item build\bin\HukukFixDM-host.exe hostbin\HukukFixDM-host.exe -Force   # embedded by install_windows.go
Run "HukukFixDM.exe $version" { wails build -trimpath -ldflags '-s -w' -o HukukFixDM.exe }

if ($Sign) {
    if (-not (Test-Path $Sign)) { throw "signing key not found: $Sign" }
    New-Item -ItemType Directory -Force build\release | Out-Null
    Copy-Item build\bin\HukukFixDM.exe build\release\HukukFixDM.exe -Force
    Run 'signing' { go run ./tools/updsign sign $Sign $version build\release\HukukFixDM.exe build\release\HukukFixDM.exe.sig }
    Run 'verifying against the key built into the program' { go run ./tools/updsign verify build\release\HukukFixDM.exe build\release\HukukFixDM.exe.sig $version }
    Get-ChildItem build\release | ForEach-Object { '{0,-20} {1,8:N1} KB' -f $_.Name, ($_.Length / 1KB) }
} else {
    Get-Item build\bin\HukukFixDM.exe | ForEach-Object { '{0,-16} {1,6:N1} MB' -f $_.Name, ($_.Length / 1MB) }
}
