# Builds HukukFix into build\bin:  HukukFixDM.exe (the app) + HukukFixDM-host.exe (browser bridge).
#
#   powershell -ExecutionPolicy Bypass -File build.ps1          # build
#   powershell -ExecutionPolicy Bypass -File build.ps1 -Test    # run the engine tests first
#   powershell -ExecutionPolicy Bypass -File build.ps1 -Zip     # also create build\HukukFixDM-<version>-win64.zip
#
# Requires Go 1.24+ (https://go.dev/dl). Nothing else: no Node, no C compiler.
param([switch]$Test, [switch]$Zip)

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

if ($Test) { Run 'engine tests' { go test ./internal/... -count=1 } }

Run 'icons' { go run ./tools/genicons }

if (-not (Get-Command wails -ErrorAction SilentlyContinue)) {
    Run 'installing the Wails build tool' { go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.1 }
}

New-Item -ItemType Directory -Force build\bin | Out-Null
Run 'HukukFixDM-host.exe' { go build -trimpath -ldflags '-s -w' -o build\bin\HukukFixDM-host.exe ./cmd/hukukfix-host }
Run 'HukukFixDM.exe' { wails build -trimpath -ldflags '-s -w' -o HukukFixDM.exe }

if ($Zip) {
    $version = (Select-String -Path internal\shared\shared.go -Pattern 'Version\s*=\s*"([^"]+)"').Matches[0].Groups[1].Value
    $out = "build\HukukFixDM-$version-win64.zip"
    Remove-Item $out -ErrorAction SilentlyContinue
    Compress-Archive -Path build\bin\HukukFixDM.exe, build\bin\HukukFixDM-host.exe -DestinationPath $out
    Write-Host "==> $out"
}

Get-ChildItem build\bin\HukukFixDM*.exe | ForEach-Object { '{0,-16} {1,6:N1} MB' -f $_.Name, ($_.Length / 1MB) }
