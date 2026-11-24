# Uçtan uca testi çalıştırır. Program açılışta HKCU kayıt defterine tarayıcı köprüsünü (NativeMessagingHosts), otomatik
# başlatmayı ve "Yüklü uygulamalar" kaydını yazar; test kaldırmayı da dener. Bu yüzden önce bu anahtarlar yedeklenir, test
# bitince (başarısız olsa da) geri yüklenir. Masaüstüne ve Başlat menüsüne dokunulmaz: test sürümü kısayolları geçici klasöre koyar.
#
#   powershell -ExecutionPolicy Bypass -File tools\e2e\calistir.ps1
#
# Açık olan HukukFix'i kapatmak gerekmez: test sürümü kendi tek örnek anahtarını, kendi veri klasörünü kullanır.

# 'Continue': reg.exe başarı iletisini hata akışına yazar; PowerShell 5.1'de 'Stop' bunu hata sayıp geri yüklemeyi keserdi.
$ErrorActionPreference = 'Continue'
function Invoke-Reg { & reg.exe @args *> $null; if ($LASTEXITCODE -ne 0) { Write-Warning "reg $($args -join ' ') başarısız" } }
$appId = 'HukukFixDM'   # internal/shared/shared.go AppID (otomatik başlatma değerinin ve kaldırma kaydının adı)
$hostAdi = 'com.hukukfix.downloadmanager'   # internal/shared/shared.go NativeHostName
$tarayicilar = 'Google\Chrome', 'Microsoft\Edge', 'BraveSoftware\Brave-Browser', 'Chromium', 'Vivaldi'
$runYolu = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
$kaldirAnahtar = "Software\Microsoft\Windows\CurrentVersion\Uninstall\$appId"

Set-Location (Join-Path $PSScriptRoot '..\..')
$kok = (Get-Location).Path
if (-not (Get-Command go -ErrorAction SilentlyContinue)) { $local = Join-Path $env:LOCALAPPDATA 'go-toolchain\go\bin'; if (Test-Path (Join-Path $local 'go.exe')) { $env:Path = "$local;$env:Path" } }
# Önceki bir testten kalmış test programları (yalnız geçici klasörden ya da build\bin'den çalışanlar) kapatılır.
Get-Process HukukFixDM* -ErrorAction SilentlyContinue |
  Where-Object { $_.Path -and ($_.Path -like '*\hfdm-e2e-*' -or $_.Path -like "$kok\build\bin\*") } |
  Stop-Process -Force

$yedek = Join-Path ([IO.Path]::GetTempPath()) ('hfdm-e2e-reg-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory $yedek | Out-Null
$i = 0
foreach ($t in $tarayicilar) {
  $i++
  if (Test-Path "Registry::HKEY_CURRENT_USER\Software\$t\NativeMessagingHosts\$hostAdi") {
    Invoke-Reg export "HKCU\Software\$t\NativeMessagingHosts\$hostAdi" (Join-Path $yedek "nmh$i.reg") /y
  }
}
if (Test-Path "Registry::HKEY_CURRENT_USER\$kaldirAnahtar") { Invoke-Reg export "HKCU\$kaldirAnahtar" (Join-Path $yedek 'kaldir.reg') /y }
$runOnce = (Get-ItemProperty $runYolu -ErrorAction SilentlyContinue).$appId

# Gerçek veri klasörünün (%LOCALAPPDATA%\HukukFixDM) dökümü: ne derleme ne test ona dokunmamalı.
$gercek = Join-Path $env:LOCALAPPDATA 'HukukFixDM'
function Get-Doku { if (Test-Path $gercek) { (Get-ChildItem $gercek -Force | ForEach-Object { '{0}|{1}|{2:o}' -f $_.Name, $_.Length, $_.LastWriteTimeUtc }) -join "`n" } else { '(yok)' } }
$gercekOnce = Get-Doku

$kod = 1
try {
  # Test sürümü: e2e_hook.go yalnız -tags e2e ile derlenir (build.ps1'in yayın sürümünde yoktur). Köprü önce derlenip
  # hostbin\ içine konur, çünkü program onu kendi içine gömer. İkinci derleme aynı kodun 9.9.9 numaralı hâlidir: güncelleme
  # testinde "GitHub'daki yeni sürüm" olarak sunulur.
  $gobin = Join-Path (go env GOPATH) 'bin'; $env:Path = "$gobin;$env:Path"
  New-Item -ItemType Directory -Force build\bin, hostbin | Out-Null
  go build -trimpath -o build\bin\HukukFixDM-host.exe ./cmd/hukukfix-host
  if ($LASTEXITCODE -ne 0) { throw 'HukukFixDM-host.exe derlenemedi' }
  Copy-Item build\bin\HukukFixDM-host.exe hostbin\HukukFixDM-host.exe -Force
  wails build -tags e2e -trimpath -o HukukFixDM-e2e.exe *> $null
  if ($LASTEXITCODE -ne 0) { throw 'test sürümü derlenemedi (wails build -tags e2e)' }
  wails build -tags e2e -trimpath -ldflags '-X hukukfixdm/internal/shared.Version=9.9.9' -o HukukFixDM-e2e-999.exe *> $null
  if ($LASTEXITCODE -ne 0) { throw '9.9.9 test sürümü derlenemedi' }
  if ((Get-Doku) -ne $gercekOnce) { throw "derleme gerçek veri klasörüne dokundu: $gercek" }
  node tools\e2e\e2e.mjs
  $kod = $LASTEXITCODE
  if ((Get-Doku) -ne $gercekOnce) { Write-Warning "test gerçek veri klasörüne dokundu: $gercek"; $kod = 1 }
  else { Write-Host 'Gerçek veri klasörüne dokunulmadı.' }
} finally {
  # Yalnız testin programlarını kapat (geçici klasörden ya da build\bin'den çalışanlar); kullanıcının HukukFix'ine dokunma.
  Get-Process HukukFixDM* -ErrorAction SilentlyContinue |
    Where-Object { $_.Path -and ($_.Path -like '*\hfdm-e2e-*' -or $_.Path -like "$kok\build\bin\*") } |
    Stop-Process -Force
  $i = 0
  foreach ($t in $tarayicilar) {
    $i++
    $anahtar = "Registry::HKEY_CURRENT_USER\Software\$t\NativeMessagingHosts\$hostAdi"
    $dosya = Join-Path $yedek "nmh$i.reg"
    if (Test-Path $dosya) { Invoke-Reg import $dosya }
    elseif (Test-Path $anahtar) { Remove-Item $anahtar -Recurse -Force }
  }
  $dosya = Join-Path $yedek 'kaldir.reg'
  if (Test-Path $dosya) { Invoke-Reg import $dosya }
  elseif (Test-Path "Registry::HKEY_CURRENT_USER\$kaldirAnahtar") { Remove-Item "Registry::HKEY_CURRENT_USER\$kaldirAnahtar" -Recurse -Force }
  if ($null -eq $runOnce) { Remove-ItemProperty $runYolu -Name $appId -ErrorAction SilentlyContinue }
  else { Set-ItemProperty $runYolu -Name $appId -Value $runOnce }
  Remove-Item $yedek -Recurse -Force
  Write-Host 'Kayıt defteri eski hâline getirildi.'
}
exit $kod
