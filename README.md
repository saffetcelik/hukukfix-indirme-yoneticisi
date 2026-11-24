# HukukFix İndirme Yöneticisi

Windows için hızlı, sade, **ücretsiz ve açık kaynak** bir indirme yöneticisi ve ona bağlı Chrome uzantısı. Internet Download Manager gibi dosyayı çok sayıda bağlantıyla parça parça indirir; lisans ücreti, deneme süresi ve reklam yoktur.

> **Claude Fable 5.2 ile geliştirildi.** Bu programın kodu Anthropic'in Claude Fable 5.2 modeliyle yazıldı.

<p align="center"><img src="docs/ekran-goruntusu.png" width="760" alt="HukukFix İndirme Yöneticisi ana penceresi: iki indirme sekiz bağlantıyla sürüyor"></p>

## İndir

[Releases](../../releases/latest) sayfasından `HukukFixDM-<sürüm>-win64.zip` dosyasını indirin, bir klasöre açın ve `HukukFixDM.exe`'yi çalıştırın. Kurulum ve yönetici izni gerekmez. Program henüz kod imzalı olmadığı için ilk açılışta Windows SmartScreen "Ek bilgi → Yine de çalıştır" sorabilir.

Tek bir `HukukFixDM.exe` (≈13 MB) ve tarayıcı köprüsü `HukukFixDM-host.exe` (≈3 MB). Kurulum, çalışma zamanı, yönetici izni gerekmez.

## Özellikler

- **Dinamik çok parçalı indirme** — dosya 32'ye kadar bağlantıyla indirilir. Bir bağlantı işini bitirince, kalan en büyük parçayı ikiye bölüp oradan devam eder; hiçbir bağlantı boşta beklemez.
- **Güvenilir duraklat / sürdür** — ilerleme 2 saniyede bir diske işlenir. Uygulama çökse ya da bilgisayar kapansa bile indirme açılışta kaldığı yerden kendiliğinden devam eder.
- **Bozuk dosya yok** — sürdürürken sunucudaki dosyanın değişip değişmediği `ETag` / `Last-Modified` ile doğrulanır; değiştiyse indirme baştan alınır, eski ve yeni veri asla karışmaz.
- **Sunucuya uyum** — sunucu paralel bağlantıyı sınırlıyorsa bağlantı sayısı otomatik düşer; `Range` desteklemeyen sunucularda tek akışa geçilir; kopan bağlantılar artan beklemeyle yeniden denenir.
- **Kuyruk ve hız sınırı** — aynı anda inecek dosya sayısı ve toplam hız sınırı ayarlanabilir.
- **Tarayıcı entegrasyonu** — Chrome, Edge ve Brave indirmeleri (çerezler, `Referer`, `User-Agent` ile birlikte) HukukFix'e aktarılır. HukukFix kapalıysa uzantı onu başlatır; ulaşamazsa indirme tarayıcıda devam eder, hiçbir indirme kaybolmaz.
- **Video yakalama** — sayfada oynayan videonun üzerine gelince **İndir** düğmesi çıkar; birden fazla kalite varsa (1080p, 720p…) seçtirir. Doğrudan video/ses dosyaları ve HLS (`.m3u8`) akışları desteklenir: akışın parçaları paralel indirilir, şifreliyse (AES-128) çözülür ve tek dosyada birleştirilir. Ses ile görüntüsü ayrı gelen MP4 tabanlı akışlar harici araç olmadan tek `.mp4` yapılır; MPEG-TS akışları `.ts` olarak kaydedilir. Duraklatılan akış kaldığı parçadan devam eder. Bir `.m3u8` bağlantısını doğrudan uygulamaya yapıştırmak da yeterlidir.
- **Bilerek desteklenmeyenler** — YouTube, DRM korumalı içerik (Netflix vb.) ve canlı yayınlar. Bunlar için düğme hiç gösterilmez.
- **Windows'a uygun** — sistem tepsisinde yaşar, açık/koyu tema, bildirimler, sistem proxy'si, indirilen dosyalara “internetten indirildi” işareti (SmartScreen çalışmaya devam eder).
- **Türkçe ve İngilizce** arayüz.

## Kullanım

1. `HukukFixDM.exe` ve `HukukFixDM-host.exe` dosyalarını **aynı klasöre** koyup `HukukFixDM.exe`'yi açın.
2. Bağlantı eklemek için: **Yeni indirme**, `Ctrl+V` ile yapıştırma ya da bağlantıyı pencereye sürükleme.
3. Pencereyi kapatmak HukukFix'i kapatmaz; sistem tepsisinde çalışmaya devam eder. Çıkmak için tepsi simgesine sağ tıklayıp **Çıkış**.

### Chrome uzantısını yükleme

Uzantı henüz Chrome Web Mağazası'nda olmadığı için bir kereliğine elle yüklenir:

1. HukukFix'te **Ayarlar → Tarayıcı** bölümünü açın ve **Chrome** düğmesine basın (`chrome://extensions` açılır).
2. Sağ üstten **Geliştirici modu**nu açın.
3. **Paketlenmemiş öğe yükle**'ye tıklayıp Ayarlar'da gösterilen klasörü seçin
   (`%LOCALAPPDATA%\HukukFixDM\extension` — **Kopyala** düğmesiyle yolu alabilirsiniz).

Bağlantı kurulunca HukukFix'in sağ alt köşesinde **Tarayıcı bağlı** yazar. Edge ve Brave için adımlar aynıdır.

## Kaynaktan derleme

Yalnızca [Go](https://go.dev/dl) gerekir (Node ya da C derleyicisi gerekmez).

```powershell
powershell -ExecutionPolicy Bypass -File build.ps1 -Test -Zip
```

Çıktılar `build\bin\` altına, dağıtım arşivi `build\HukukFixDM-<sürüm>-win64.zip` olarak yazılır.

| Klasör | İçerik |
|---|---|
| `internal/engine` | İndirme motoru ve testleri (`go test ./internal/...`) |
| `cmd/hukukfix-host` | Tarayıcının çalıştırdığı native messaging host |
| `frontend/dist` | Arayüz (derleme adımı olmayan düz HTML/CSS/JS) |
| `extension` | Tarayıcı uzantısı (Manifest V3) |
| `main.go`, `app.go`, `bridge.go`, `tray.go` | Pencere, yerel köprü API'si, tepsi simgesi |
| `tools` | İkon üretici, uzantı anahtarı üretici, arayüz önizleme sunucusu, indirme ve uzantı deneme araçları, uçtan uca test (`tools/e2e`) |

### Testler

```powershell
go test ./internal/...                                              # indirme motoru
powershell -ExecutionPolicy Bypass -File tools\e2e\calistir.ps1     # uçtan uca, gerçek pencere
```

Uçtan uca test, programın test sürümünü (`-tags e2e`, yalnız testte derlenen `e2e_hook.go`) geçici bir veri klasörüyle açar ve arayüzdeki düğmelere gerçekten basar: ekleme, duraklat/sürdür, tümünü duraklat/sürdür, iptal, listeden kaldır, dosyayı sil, başarısız indirme ve yeniden deneme, sekmeler ve arama, tamamlananları temizle (liste tamamen boşalınca dahil), kapatıp açınca kaldığı yerden sürdürme, tarayıcı köprüsü (uzantı → `HukukFixDM-host.exe` → program), sürükle-bırak, kısayollar, tema ve dil. İndirmeler yalnız testin 127.0.0.1'de açtığı sunucudan yapılır ve her dosya bayt bayt doğrulanır. Program açılışta tarayıcı köprüsünü kayıt defterine yazdığı için betik ilgili anahtarları önce yedekler, sonra geri yükler.

## Sürüm notları

- **1.1.1** — Hepsi tamamlanmışken "Tamamlananları temizle"ye basınca ya da listedeki son indirme kaldırılınca pencere eski satırları göstermeye devam ediyordu (boş liste pencereye hiç gönderilmiyordu). Düzeltildi. Açık temada pencere açılırken koyu renkte yanıp sönmüyor. Uçtan uca test eklendi (68 adım).
- **1.1.0** — İlk açık kaynak sürüm.

## Güvenlik tasarımı

- Uygulama yalnızca `127.0.0.1` üzerinde dinler. Her istek, rastgele üretilmiş 256 bitlik bir **erişim anahtarı** taşımak zorundadır.
- Tarayıcı uygulamaya hiç doğrudan bağlanmaz. Uzantı her isteğini native messaging host'a (`HukukFixDM-host.exe`) verir; host isteği uygulamaya iletir. Tarayıcı o host'a yalnızca HukukFix uzantısının kimliğiyle erişim verir.
- Erişim anahtarı tarayıcıya hiç ulaşmaz; yalnızca uygulama ile host arasında kalır. Sıradan web sayfaları anahtarı ne okuyabilir ne tahmin edebilir; `Host` denetimi DNS rebinding saldırılarını engeller.
- Bu tasarım, tarayıcıların yerel ağ (127.0.0.1) isteklerine getirdiği kısıtlamalardan da etkilenmez.
- Çerezler yalnızca indirmenin istendiği sunucuya gönderilir, yönlendirilen başka sunuculara gönderilmez ve indirme bitince silinir.
- Sunucunun gönderdiği dosya adları temizlenir; bir sunucu indirme klasörünün dışına dosya yazdıramaz.

### Sorun giderme

Uzantı penceresinde **Çalışmıyor** yazıyorsa altındaki küçük gri satır nedeni söyler. `%LOCALAPPDATA%\HukukFixDM\host.log` dosyası tarayıcıdan gelen her çağrıyı ve sonucunu (adresler ve çerezler hariç) kaydeder; dosya hiç oluşmuyorsa tarayıcı host'u başlatamıyor demektir. `tools\exttest.ps1`, uzantıyı geçici bir tarayıcı profilinde çalıştırıp denemek içindir (Chrome 137 ve sonrası komut satırından uzantı yüklemeyi engellediği için yalnızca buna izin veren Chromium tabanlı tarayıcılarda çalışır).

`tools\dltest` tek bir bağlantıyı (dosya ya da `.m3u8` akışı) uygulama olmadan motorla indirip sonucu yazar:
`go run ./tools/dltest <klasör> <bağlantı> [kalite]`.

## Yol haritası

- Kurulum paketi (setup.exe) ve otomatik güncelleme
- Kod imzalama (imzasız `.exe` ilk açılışta SmartScreen uyarısı gösterir)
- Uzantının Chrome Web Mağazası'nda yayınlanması
- DASH (`.mpd`) akışları; sesi ayrı gelen MPEG-TS akışları; `.ts` çıktısının MP4'e çevrilmesi

## Lisans

MIT — bkz. [LICENSE](LICENSE).

*Internet Download Manager, Tonec Inc.'in ticari markasıdır; bu proje onunla bağlantılı değildir ve onun kodunu içermez.*
