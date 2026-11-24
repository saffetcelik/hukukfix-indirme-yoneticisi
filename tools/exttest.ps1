# Runs the browser extension in a throw-away, headless Chrome/Edge profile and
# evaluates JavaScript inside the extension's own popup page over the DevTools
# protocol. Used to see what really happens in a browser (native messaging,
# requests to the app) without touching the user's own browser profile.
#
#   powershell -ExecutionPolicy Bypass -File tools\exttest.ps1                      # default checks
#   powershell -ExecutionPolicy Bypass -File tools\exttest.ps1 -Js "location.href"  # evaluate an expression
param(
    [string]$Js = '',
    [string]$Browser = 'chrome',
    [int]$Port = 9333,
    [switch]$Headed
)
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$ext = Join-Path $root 'extension'
$id = 'kphedpgokeneghjfepkodjncihcjhjlh'

$exeName = @{ chrome = 'chrome.exe'; edge = 'msedge.exe' }[$Browser]
$exe = $null
foreach ($hive in 'HKCU:', 'HKLM:') {
    $k = "$hive\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\$exeName"
    if (-not $exe -and (Test-Path $k)) { $exe = (Get-ItemProperty $k).'(default)'.Trim('"') }
}
foreach ($p in "${env:ProgramFiles(x86)}\Microsoft\Edge\Application\msedge.exe", "$env:ProgramFiles\Microsoft\Edge\Application\msedge.exe", "$env:ProgramFiles\Google\Chrome\Application\chrome.exe") {
    if (-not $exe -and (Split-Path $p -Leaf) -eq $exeName -and (Test-Path $p)) { $exe = $p }
}
if (-not $exe) { throw "$exeName not found" }
"browser: $exe  ($((Get-Item $exe).VersionInfo.ProductVersion))"

$profileDir = Join-Path $env:TEMP "hfdm-exttest-$Browser"
$browserArgs = @(
    "--user-data-dir=`"$profileDir`"", "--remote-debugging-port=$Port", '--no-first-run', '--no-default-browser-check',
    '--disable-features=DisableLoadExtensionCommandLineSwitch', "--load-extension=`"$ext`"", 'about:blank'
)
if (-not $Headed) { $browserArgs = @('--headless=new') + $browserArgs }
$proc = Start-Process -FilePath $exe -ArgumentList $browserArgs -PassThru

function Invoke-Cdp($wsUrl, [string]$expression) {
    $ws = New-Object System.Net.WebSockets.ClientWebSocket
    $ws.ConnectAsync([Uri]$wsUrl, [Threading.CancellationToken]::None).Wait()
    $msg = @{ id = 1; method = 'Runtime.evaluate'; params = @{ expression = $expression; awaitPromise = $true; returnByValue = $true } } | ConvertTo-Json -Depth 6 -Compress
    $bytes = [Text.Encoding]::UTF8.GetBytes($msg)
    $ws.SendAsync((New-Object System.ArraySegment[byte] -ArgumentList @(, $bytes)), 'Text', $true, [Threading.CancellationToken]::None).Wait()
    $buf = New-Object byte[] 1048576
    $sb = New-Object Text.StringBuilder
    do {
        $seg = New-Object System.ArraySegment[byte] -ArgumentList @(, $buf)
        $r = $ws.ReceiveAsync($seg, [Threading.CancellationToken]::None); $r.Wait()
        [void]$sb.Append([Text.Encoding]::UTF8.GetString($buf, 0, $r.Result.Count))
    } while (-not $r.Result.EndOfMessage)
    $ws.Dispose()
    $res = ($sb.ToString() | ConvertFrom-Json).result
    if ($res.exceptionDetails) { return "EXCEPTION: $($res.exceptionDetails.exception.description)$($res.exceptionDetails.text)" }
    return $res.result.value
}

try {
    $base = "http://127.0.0.1:$Port"
    $ready = $false
    foreach ($i in 1..40) { try { Invoke-RestMethod "$base/json/version" | Out-Null; $ready = $true; break } catch { Start-Sleep -Milliseconds 250 } }
    if (-not $ready) { throw 'DevTools endpoint did not come up' }
    Start-Sleep -Milliseconds 800
    $page = Invoke-RestMethod "$base/json/new?chrome-extension://$id/popup.html" -Method Put
    Start-Sleep -Milliseconds 1500
    "extension page: $($page.url)"

    if ($Js) {
        Invoke-Cdp $page.webSocketDebuggerUrl $Js
    } else {
        $checks = [ordered]@{
            'page loaded'        = "document.readyState + ' / chrome.runtime.id=' + (window.chrome && chrome.runtime && chrome.runtime.id)"
            'native host (info)' = "chrome.runtime.sendNativeMessage('com.hukukfix.downloadmanager', {action:'info'}).then(r => JSON.stringify(Object.assign({}, r, {token: r.token ? '<' + r.token.length + ' chars>' : ''})), e => 'ERROR: ' + e.message)"
            'fetch to app'       = "chrome.runtime.sendNativeMessage('com.hukukfix.downloadmanager', {action:'info'}).then(r => fetch('http://127.0.0.1:' + r.port + '/v1/status', {headers: {'X-HFDM-Token': r.token}})).then(r => 'HTTP ' + r.status, e => 'ERROR: ' + e.message)"
            'popup state'        = "chrome.runtime.sendMessage({type:'state', tabId: null}).then(s => JSON.stringify({online: s.online, version: s.version, error: s.error, items: (s.items || []).length}), e => 'ERROR: ' + e.message)"
            'popup text'         = "document.body.innerText.replace(/\s+/g, ' ').slice(0, 300)"
        }
        foreach ($name in $checks.Keys) { '{0,-20} {1}' -f $name, (Invoke-Cdp $page.webSocketDebuggerUrl $checks[$name]) }
    }
} finally {
    try { Invoke-RestMethod "http://127.0.0.1:$Port/json/close/$($page.id)" | Out-Null } catch {}
    Get-CimInstance Win32_Process -Filter "Name='$exeName'" | Where-Object { $_.CommandLine -like "*hfdm-exttest-$Browser*" } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
}
