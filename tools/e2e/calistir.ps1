# Uçtan uca testi çalıştırır. Program açılışta tarayıcı köprüsünü (NativeMessagingHosts) ve otomatik başlatmayı
# HKCU kayıt defterine yazdığı için önce bu anahtarlar yedeklenir, test bitince (başarısız olsa da) geri yüklenir.
#
#   powershell -ExecutionPolicy Bypass -File tools\e2e\calistir.ps1
#
# Çalışan bir HukukFixDM varsa önce kapatın (tek örnek kilidi testi engeller).

# 'Continue': reg.exe başarı iletisini hata akışına yazar; PowerShell 5.1'de 'Stop' bunu hata sayıp geri yüklemeyi keserdi.
$ErrorActionPreference = 'Continue'
function Invoke-Reg { & reg.exe @args *> $null; if ($LASTEXITCODE -ne 0) { Write-Warning "reg $($args -join ' ') başarısız" } }
$appId = 'HukukFixDM'   # internal/shared/shared.go AppID (otomatik başlatma değerinin adı)
$hostAdi = 'com.hukukfix.downloadmanager'   # internal/shared/shared.go NativeHostName
$tarayicilar = 'Google\Chrome', 'Microsoft\Edge', 'BraveSoftware\Brave-Browser', 'Chromium', 'Vivaldi'
$runYolu = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'

Set-Location (Join-Path $PSScriptRoot '..\..')
if (-not (Get-Command go -ErrorAction SilentlyContinue)) { $local = Join-Path $env:LOCALAPPDATA 'go-toolchain\go\bin'; if (Test-Path (Join-Path $local 'go.exe')) { $env:Path = "$local;$env:Path" } }
if (Get-Process HukukFixDM, HukukFixDM-e2e -ErrorAction SilentlyContinue) { Write-Error 'HukukFixDM açık; önce tepsi simgesinden Çıkış yapın.'; exit 2 }

$yedek = Join-Path ([IO.Path]::GetTempPath()) ('hfdm-e2e-reg-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory $yedek | Out-Null
$i = 0
foreach ($t in $tarayicilar) {
  $i++
  if (Test-Path "Registry::HKEY_CURRENT_USER\Software\$t\NativeMessagingHosts\$hostAdi") {
    Invoke-Reg export "HKCU\Software\$t\NativeMessagingHosts\$hostAdi" (Join-Path $yedek "nmh$i.reg") /y
  }
}
$runOnce = (Get-ItemProperty $runYolu -ErrorAction SilentlyContinue).$appId

$kod = 1
try {
  # Test sürümü: e2e_hook.go yalnız -tags e2e ile derlenir (build.ps1'in yayın sürümünde yoktur).
  $gobin = Join-Path (go env GOPATH) 'bin'; $env:Path = "$gobin;$env:Path"
  wails build -tags e2e -trimpath -o HukukFixDM-e2e.exe *> $null
  if ($LASTEXITCODE -ne 0) { throw 'test sürümü derlenemedi (wails build -tags e2e)' }
  go build -trimpath -o build\bin\HukukFixDM-host.exe ./cmd/hukukfix-host
  if ($LASTEXITCODE -ne 0) { throw 'HukukFixDM-host.exe derlenemedi' }
  node tools\e2e\e2e.mjs
  $kod = $LASTEXITCODE
} finally {
  Get-Process HukukFixDM, HukukFixDM-e2e -ErrorAction SilentlyContinue | Stop-Process -Force
  $i = 0
  foreach ($t in $tarayicilar) {
    $i++
    $anahtar = "Registry::HKEY_CURRENT_USER\Software\$t\NativeMessagingHosts\$hostAdi"
    $dosya = Join-Path $yedek "nmh$i.reg"
    if (Test-Path $dosya) { Invoke-Reg import $dosya }
    elseif (Test-Path $anahtar) { Remove-Item $anahtar -Recurse -Force }
  }
  if ($null -eq $runOnce) { Remove-ItemProperty $runYolu -Name $appId -ErrorAction SilentlyContinue }
  else { Set-ItemProperty $runYolu -Name $appId -Value $runOnce }
  Remove-Item $yedek -Recurse -Force
  Write-Host 'Kayıt defteri eski hâline getirildi.'
}
exit $kod
