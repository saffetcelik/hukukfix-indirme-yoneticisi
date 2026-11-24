// Uçtan uca test: programın test sürümünü (HukukFixDM-e2e.exe, -tags e2e) geçici bir veri klasörüyle açar, arayüzdeki
// düğmelere gerçekten basar (e2e_hook.go: pencerede JavaScript çalıştırır) ve sonucu hem pencerede hem diskte doğrular. İndirmeler yalnız bu
// betiğin 127.0.0.1'de açtığı test sunucusundan yapılır; internete çıkılmaz.
//
//   powershell -ExecutionPolicy Bypass -File tools\e2e\calistir.ps1      (kayıt defterini yedekler ve geri yükler)
//
// Node 22+ gerekir (fetch yerleşik). Doğrudan çalıştırmayın: program açılışta tarayıcı köprüsünü
// HKCU kayıt defterine geçici klasörle yazar; calistir.ps1 bunu yedekleyip sonunda eski hâline getirir.
import http from 'node:http';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import crypto from 'node:crypto';
import { spawn } from 'node:child_process';

const KOK = path.resolve(path.dirname(new URL(import.meta.url).pathname.replace(/^\/(\w:)/, '$1')), '..', '..');
const EXE = process.env.HFDM_EXE || path.join(KOK, 'build', 'bin', 'HukukFixDM-e2e.exe');
const HOST = path.join(KOK, 'build', 'bin', 'HukukFixDM-host.exe');
const GECICI = fs.mkdtempSync(path.join(os.tmpdir(), 'hfdm-e2e-'));
const VERI = path.join(GECICI, 'veri'), INDIR = path.join(GECICI, 'indirilenler');
const PORT = 47231, KANCA = 47232;
const bekle = (ms) => new Promise((r) => setTimeout(r, ms));
let gecen = 0, kalan = 0; const hatalar = [];
const sonuc = (ad, ok, ayrinti = '') => { if (ok) { gecen++; console.log(`  ✓ ${ad}`); } else { kalan++; hatalar.push(ad); console.log(`  ✗ ${ad}  ${ayrinti}`); } };

// ---------- test sunucusu ----------
// /hizli/<ad>?boy=N  Range destekli, hızlı · /yavas/<ad>?boy=N  bağlantı başına ~400 KB/s · /tek/<ad>?boy=N  Range yok
// /yok/<ad>  404 · içerik belirlenimli: bayt i = (i*31 + 7) & 255, böylece indirilen dosya bayt bayt doğrulanır.
const bayt = (bas, n) => { const b = Buffer.alloc(n); for (let i = 0; i < n; i++) b[i] = ((bas + i) * 31 + 7) & 255; return b; };
const sunucu = http.createServer((req, res) => {
  const u = new URL(req.url, 'http://x'); const [, tur, ad] = u.pathname.split('/'); const boy = +(u.searchParams.get('boy') || 0);
  if (tur === 'yok' || !boy) { res.writeHead(404); return res.end(); }
  const range = tur !== 'tek' && /bytes=(\d+)-(\d*)/.exec(req.headers.range || '');
  const bas = range ? +range[1] : 0, son = range && range[2] ? Math.min(+range[2], boy - 1) : boy - 1;
  const h = { 'Content-Length': son - bas + 1, 'Content-Type': 'application/octet-stream', ETag: `"${tur}-${boy}"`, 'Last-Modified': 'Mon, 23 Nov 2026 10:00:00 GMT' };
  if (tur !== 'tek') h['Accept-Ranges'] = 'bytes';
  if (range) h['Content-Range'] = `bytes ${bas}-${son}/${boy}`;
  res.writeHead(range ? 206 : 200, h);
  if (req.method === 'HEAD') return res.end();
  let i = bas; const PARCA = 16384;
  const yaz = () => {
    while (i <= son) {
      const n = Math.min(PARCA, son - i + 1); const ok = res.write(bayt(i, n)); i += n;
      if (tur === 'yavas') return setTimeout(yaz, 40);
      if (!ok) return res.once('drain', yaz);
    }
    res.end();
  };
  req.on('close', () => { i = son + 1; });
  yaz();
});
const url = (tur, ad, boy) => `http://127.0.0.1:${PORT}/${tur}/${ad}?boy=${boy}`;
const dogruMu = (dosya, boy) => { if (!fs.existsSync(dosya)) return 'dosya yok'; const b = fs.readFileSync(dosya); if (b.length !== boy) return `boy ${b.length} ≠ ${boy}`; return crypto.createHash('sha1').update(b).digest('hex') === crypto.createHash('sha1').update(bayt(0, boy)).digest('hex') ? '' : 'içerik farklı'; };

// ---------- program ve test kancası ----------
let app = null;
function ayarYaz(ek = {}) {
  fs.mkdirSync(path.join(VERI, 'HukukFixDM'), { recursive: true }); fs.mkdirSync(INDIR, { recursive: true });
  const s = { downloadDir: INDIR, categoryFolders: false, maxConnections: 8, maxActive: 3, speedLimitKB: 0, capture: true, captureExts: ['zip', 'bin'], captureMinKB: 0,
    showOnCapture: false, notify: false, autostart: false, language: 'tr', theme: 'light', ...ek };
  fs.writeFileSync(path.join(VERI, 'HukukFixDM', 'settings.json'), JSON.stringify(s));
}
async function ac() {
  app = spawn(EXE, [], { env: { ...process.env, LOCALAPPDATA: VERI, HFDM_E2E_PORT: String(KANCA) }, stdio: 'ignore' });
  let hazir = false;
  for (let i = 0; i < 120 && !hazir; i++) { await bekle(250); try { hazir = await js(`!!(window.go && document.querySelector('#list') && typeof render === 'function' && Object.keys(settings).length)`); } catch {} }
  if (!hazir) throw new Error('program açılmadı ya da test kancası yanıt vermedi');
  await bekle(600);
}
async function js(ifade) {
  const r = await fetch(`http://127.0.0.1:${KANCA}/js`, { method: 'POST', body: ifade, signal: AbortSignal.timeout(25000) });
  if (!r.ok) throw new Error(`kanca ${r.status}`);
  const d = await r.json(); if (d.e) throw new Error(d.e); return d.v;
}
async function kapat() {
  js(`window.go.main.App.Quit()`).catch(() => {});
  for (let i = 0; i < 60 && app.exitCode === null; i++) await bekle(250);
  if (app.exitCode === null) app.kill();
  await bekle(500);
}
// pencereden okumalar
const satirlar = () => js(`[...document.querySelectorAll('#list .row')].map(r => ({ id: r.dataset.id, ad: r.querySelector('.name').textContent, durum: r.dataset.state, meta: r.querySelector('.meta').textContent }))`);
const satir = async (ad) => (await satirlar()).find((r) => r.ad === ad);
const tikla = (secici) => js(`(() => { const e = document.querySelector(${JSON.stringify(secici)}); if (!e) return false; e.click(); return true; })()`);
const satirDugme = (ad, act) => js(`(() => { const r = [...document.querySelectorAll('#list .row')].find(r => r.querySelector('.name').textContent === ${JSON.stringify(ad)}); const b = r && r.querySelector('[data-act="${act}"]'); if (!b) return false; b.click(); return true; })()`);
const sagTikMenu = (ad, act) => js(`(() => { const r = [...document.querySelectorAll('#list .row')].find(r => r.querySelector('.name').textContent === ${JSON.stringify(ad)}); if (!r) return false;
  const b = r.getBoundingClientRect(); r.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, clientX: b.left + 40, clientY: b.top + 10 }));
  const m = document.querySelector('#menu [data-act="${act}"]'); if (!m) return false; m.click(); return true; })()`);
const sayilar = () => js(`({ tum: +document.querySelector('#n-all').textContent, aktif: +document.querySelector('#n-active').textContent, tamam: +document.querySelector('#n-done').textContent, temizleGizli: document.querySelector('#btn-clear').hidden, bos: !document.querySelector('#empty').hidden })`);
async function bekleKosul(fn, sure = 20000) { const t0 = Date.now(); while (Date.now() - t0 < sure) { try { if (await fn()) return true; } catch {} await bekle(200); } return false; }
async function ekle(urller) {
  await tikla('#btn-add'); await bekle(300);
  await js(`document.querySelector('#add-urls').value = ${JSON.stringify(urller.join('\n'))}`);
  await tikla('#add-ok'); await bekle(500);
}
const diskte = (ad) => fs.existsSync(path.join(INDIR, ad));
// Tarayıcı uzantısının yolu: uzantı → HukukFixDM-host.exe (native messaging: 4 bayt uzunluk + JSON) → programın köprüsü.
async function host(mesajlar) {
  const p = spawn(HOST, ['chrome-extension://e2e/'], { env: { ...process.env, LOCALAPPDATA: VERI }, stdio: ['pipe', 'pipe', 'ignore'] });
  const parca = []; p.stdout.on('data', (d) => parca.push(d));
  for (const m of mesajlar) { const b = Buffer.from(JSON.stringify(m)); const h = Buffer.alloc(4); h.writeUInt32LE(b.length); p.stdin.write(Buffer.concat([h, b])); }
  p.stdin.end(); await new Promise((r) => p.on('exit', r));
  const buf = Buffer.concat(parca), out = []; let i = 0;
  while (i + 4 <= buf.length) { const n = buf.readUInt32LE(i); out.push(JSON.parse(buf.subarray(i + 4, i + 4 + n).toString('utf8'))); i += 4 + n; }
  return out;
}
const toastVar = (metin) => js(`[...document.querySelectorAll('#toasts .toast')].some(t => t.textContent === ${JSON.stringify(metin)})`);

const parcaVar = (ad) => fs.readdirSync(INDIR).some((f) => f.startsWith(ad) && f.endsWith('.hfdm'));

// ---------- testler ----------
async function testler() {
  const K = 1024, MB = 1024 * 1024;
  console.log('Ekleme ve tamamlanma');
  await ekle([url('hizli', 'a.bin', 3 * MB + 123), url('hizli', 'b.bin', 777 * K), url('tek', 'c.bin', 2 * MB + 5), url('yavas', 'd.bin', 120 * MB)]);
  sonuc('4 bağlantı eklenince 4 satır', (await satirlar()).length === 4, JSON.stringify(await satirlar()));
  sonuc('hızlı dosyalar tamamlandı', await bekleKosul(async () => (await satir('a.bin'))?.durum === 'completed' && (await satir('b.bin'))?.durum === 'completed'));
  sonuc('tek akışlı (Range yok) dosya tamamlandı', await bekleKosul(async () => (await satir('c.bin'))?.durum === 'completed'));
  for (const [ad, boy] of [['a.bin', 3 * MB + 123], ['b.bin', 777 * K], ['c.bin', 2 * MB + 5]]) { const h = dogruMu(path.join(INDIR, ad), boy); sonuc(`${ad} bayt bayt doğru`, !h, h); }

  console.log('Duraklat ve sürdür');
  sonuc('yavaş dosya iniyor', await bekleKosul(async () => (await satir('d.bin'))?.durum === 'downloading'));
  await bekle(1500);
  sonuc('duraklat düğmesi', await satirDugme('d.bin', 'pause'));
  sonuc('duraklatıldı', await bekleKosul(async () => (await satir('d.bin'))?.durum === 'paused', 4000));
  const m1 = (await satir('d.bin')).meta; await bekle(1500); const m2 = (await satir('d.bin')).meta;
  sonuc('duraklatılınca ilerleme durdu', m1 === m2, `${m1} → ${m2}`);
  sonuc('sürdür düğmesi', await satirDugme('d.bin', 'resume'));
  sonuc('sürdürüldü', await bekleKosul(async () => (await satir('d.bin'))?.durum === 'downloading', 6000));

  console.log('Tümünü duraklat ve sürdür');
  await ekle([url('yavas', 'e.bin', 40 * MB)]);
  await bekleKosul(async () => (await satir('e.bin'))?.durum === 'downloading', 6000);
  await tikla('#btn-pause-all');
  sonuc('tümünü duraklat', await bekleKosul(async () => (await satirlar()).filter((r) => r.durum !== 'completed').every((r) => r.durum === 'paused'), 4000));
  await tikla('#btn-resume-all');
  sonuc('tümünü sürdür', await bekleKosul(async () => (await satirlar()).filter((r) => r.durum !== 'completed').every((r) => r.durum === 'downloading'), 6000));

  console.log('İptal (indirmeyi iptal et)');
  await bekle(1000);
  sonuc('iptal düğmesi', await satirDugme('e.bin', 'cancel'));
  sonuc('onay penceresi açıldı', await bekleKosul(() => js(`!document.querySelector('#dlg-confirm').hidden`), 2000));
  await tikla('#confirm-ok');
  sonuc('iptal edilen satır kalktı', await bekleKosul(async () => !(await satir('e.bin')), 4000));
  sonuc('iptal edilen parça dosyası silindi', await bekleKosul(async () => !parcaVar('e.bin') && !diskte('e.bin'), 4000));

  console.log('Başarısız indirme');
  await ekle([url('yok', 'f.bin', 0)]);
  sonuc('404 başarısız olarak göründü', await bekleKosul(async () => (await satir('f.bin'))?.durum === 'failed', 15000));
  sonuc('yeniden dene', await satirDugme('f.bin', 'resume'));
  // Yeniden deneme hemen başlar: önce satırın 'başarısız'dan çıkmasını bekle, yoksa eski durumu görüp erken geçeriz.
  await bekleKosul(async () => (await satir('f.bin'))?.durum !== 'failed', 3000);
  sonuc('yeniden deneme yine başarısız', await bekleKosul(async () => (await satir('f.bin'))?.durum === 'failed' && !!(await js(`!!document.querySelector('#list .row[data-state="failed"] [data-act="remove"]')`)), 15000));
  sonuc('başarısızı listeden kaldır', await satirDugme('f.bin', 'remove'));
  sonuc('başarısız satır kalktı', await bekleKosul(async () => !(await satir('f.bin')), 4000));

  console.log('Sekmeler ve arama');
  let say = await sayilar();
  sonuc('sayaçlar (tümü 4, aktif 1, tamamlanan 3)', say.tum === 4 && say.aktif === 1 && say.tamam === 3, JSON.stringify(say));
  await tikla('#tabs [data-filter="done"]'); await bekle(200);
  sonuc('Tamamlanan sekmesi yalnız 3 satır', (await satirlar()).length === 3);
  await tikla('#tabs [data-filter="active"]'); await bekle(200);
  sonuc('Aktif sekmesi yalnız 1 satır', (await satirlar()).length === 1);
  await tikla('#tabs [data-filter="all"]'); await bekle(200);
  await js(`(() => { const s = document.querySelector('#search'); s.value = 'b.bin'; s.dispatchEvent(new Event('input', { bubbles: true })); })()`); await bekle(200);
  sonuc('arama süzüyor', (await satirlar()).length === 1 && (await satirlar())[0].ad === 'b.bin');
  await js(`(() => { const s = document.querySelector('#search'); s.value = ''; s.dispatchEvent(new Event('input', { bubbles: true })); })()`); await bekle(200);

  console.log('Listeden kaldır ve dosyayı sil');
  sonuc('tamamlananı listeden kaldır (×)', await satirDugme('a.bin', 'remove'));
  sonuc('kaldırılan satır gitti', await bekleKosul(async () => !(await satir('a.bin')), 3000));
  sonuc('kaldırılan dosya diskte duruyor', diskte('a.bin'));
  sonuc('sağ tık → sil', await sagTikMenu('b.bin', 'delete'));
  sonuc('silme onayı açıldı', await bekleKosul(() => js(`!document.querySelector('#dlg-confirm').hidden`), 2000));
  await tikla('#confirm-ok');
  sonuc('silinen satır gitti', await bekleKosul(async () => !(await satir('b.bin')), 3000));
  sonuc('silinen dosya diskten gitti', await bekleKosul(async () => !diskte('b.bin'), 3000));

  console.log('Tamamlananları temizle (karışık liste)');
  await ekle([url('hizli', 'g.bin', MB)]);
  await bekleKosul(async () => (await satir('g.bin'))?.durum === 'completed');
  say = await sayilar();
  sonuc('temizle düğmesi görünüyor', !say.temizleGizli, JSON.stringify(say));
  await tikla('#btn-clear');
  sonuc('tamamlananlar kalktı, süren kaldı', await bekleKosul(async () => { const r = await satirlar(); return r.length === 1 && r[0].ad === 'd.bin'; }, 3000), JSON.stringify(await satirlar()));
  sonuc('tamamlanan dosyalar diskte duruyor', diskte('c.bin') && diskte('g.bin'));

  console.log('Kalıcılık: kapat, aç, kaldığı yerden sürdür');
  await satirDugme('d.bin', 'pause'); await bekleKosul(async () => (await satir('d.bin'))?.durum === 'paused', 4000);
  await kapat(); await ac();
  sonuc('yeniden açılınca duraklatılmış indirme duruyor', (await satir('d.bin'))?.durum === 'paused', JSON.stringify(await satirlar()));
  await js(`window.go.main.App.SaveSettings({ ...settings, maxConnections: 16 })`); // hızlı bitmesi için hız sınırı yok, bağlantı çok
  sonuc('sürdür (yeniden açılıştan sonra)', await satirDugme('d.bin', 'resume'));
  sonuc('kaldığı yerden tamamlandı', await bekleKosul(async () => (await satir('d.bin'))?.durum === 'completed', 240000));
  { const h = dogruMu(path.join(INDIR, 'd.bin'), 120 * MB); sonuc('duraklat + yeniden açılış + sürdür sonrası dosya bayt bayt doğru', !h, h); }

  console.log('Tamamlananları temizle (hepsi tamamlanmış) ve son öğe');
  say = await sayilar();
  sonuc('liste yalnız tamamlanmışlardan oluşuyor', say.aktif === 0 && say.tum >= 1, JSON.stringify(say));
  await tikla('#btn-clear');
  sonuc('liste tamamen boşaldı', await bekleKosul(async () => (await satirlar()).length === 0, 3000), JSON.stringify(await satirlar()));
  say = await sayilar();
  sonuc('boş liste ekranı ve sıfır sayaç', say.bos && say.tum === 0 && say.temizleGizli, JSON.stringify(say));
  await ekle([url('hizli', 'h.bin', 512 * K)]);
  await bekleKosul(async () => (await satir('h.bin'))?.durum === 'completed');
  await satirDugme('h.bin', 'remove');
  sonuc('son kalan öğeyi kaldırınca liste boşaldı', await bekleKosul(async () => (await satirlar()).length === 0, 3000), JSON.stringify(await satirlar()));

  console.log('Ayarlar ve yapıştırma');
  await tikla('#btn-settings'); await bekle(300);
  await js(`(() => { const e = document.querySelector('[data-set="maxActive"]'); e.value = 5; e.dispatchEvent(new Event(e.type === 'range' || e.type === 'number' ? 'input' : 'change', { bubbles: true })); })()`);
  await bekle(1200);
  const ayar = JSON.parse(fs.readFileSync(path.join(VERI, 'HukukFixDM', 'settings.json'), 'utf8'));
  sonuc('ayar değişikliği diske yazıldı', ayar.maxActive === 5, `maxActive=${ayar.maxActive}`);
  await js(`document.querySelector('#dlg-settings [data-close]')?.click()`); await bekle(200);
  await js(`(() => { const dt = new DataTransfer(); dt.setData('text/plain', ${JSON.stringify(url('hizli', 'i.bin', 4096))}); document.body.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true })); })()`);
  sonuc('Ctrl+V ile bağlantı yapıştırınca ekleme penceresi dolu açıldı', await bekleKosul(() => js(`!document.querySelector('#dlg-add').hidden && document.querySelector('#add-urls').value.includes('i.bin')`), 2000));
  await js(`document.querySelector('#dlg-add [data-close]')?.click()`);

  console.log('Tarayıcı köprüsü (uzantı → host → program)');
  const [ping, durum] = await host([{ action: 'ping' }, { action: 'call', method: 'GET', path: '/v1/status' }]);
  sonuc('host ping', ping?.ok === true, JSON.stringify(ping));
  sonuc('host üzerinden programın durumu', durum?.ok === true && durum.data?.app === 'HukukFixDM', JSON.stringify(durum));
  const [yakala] = await host([{ action: 'call', method: 'POST', path: '/v1/download', body: { url: url('yavas', 'j.zip', 60 * MB), filename: 'j.zip', auto: true, size: 60 * MB } }]);
  sonuc('tarayıcıdan gelen .zip devralındı', yakala?.ok === true && yakala.data?.ok === true, JSON.stringify(yakala));
  sonuc('devralınan indirme pencerede göründü', await bekleKosul(async () => !!(await satir('j.zip')), 4000));
  const [ret] = await host([{ action: 'call', method: 'POST', path: '/v1/download', body: { url: url('hizli', 'k.txt', 100), filename: 'k.txt', auto: true, size: 100 } }]);
  sonuc('yakalanmayan tür (.txt) tarayıcıda bırakıldı', ret?.data?.declined === true, JSON.stringify(ret));
  sonuc('reddedilen indirme listeye girmedi', !(await satir('k.txt')));
  const jid = yakala?.data?.id;
  await bekleKosul(async () => (await satir('j.zip'))?.durum === 'downloading', 6000);
  await host([{ action: 'call', method: 'POST', path: '/v1/action', body: { id: jid, action: 'pause' } }]);
  sonuc('uzantıdan duraklat', await bekleKosul(async () => (await satir('j.zip'))?.durum === 'paused', 4000));
  const [liste] = await host([{ action: 'call', method: 'GET', path: '/v1/downloads' }]);
  sonuc('uzantının listesi doğru', Array.isArray(liste?.data?.items) && liste.data.items.some((d) => d.id === jid && d.state === 'paused'), JSON.stringify(liste).slice(0, 200));
  await host([{ action: 'call', method: 'POST', path: '/v1/action', body: { id: jid, action: 'resume' } }]);
  sonuc('uzantıdan sürdür', await bekleKosul(async () => (await satir('j.zip'))?.durum === 'downloading', 6000));

  console.log('Çift tıklama ve kısayollar');
  const cift = () => js(`(() => { const r = [...document.querySelectorAll('#list .row')].find(r => r.querySelector('.name').textContent === 'j.zip'); r.querySelector('.name').dispatchEvent(new MouseEvent('dblclick', { bubbles: true })); return true; })()`);
  await cift();
  sonuc('çift tıklama duraklattı', await bekleKosul(async () => (await satir('j.zip'))?.durum === 'paused', 4000));
  await cift();
  sonuc('çift tıklama sürdürdü', await bekleKosul(async () => (await satir('j.zip'))?.durum === 'downloading', 6000));
  await host([{ action: 'call', method: 'POST', path: '/v1/action', body: { id: jid, action: 'remove' } }]);
  sonuc('uzantıdan kaldır', await bekleKosul(async () => !(await satir('j.zip')), 4000));
  await js(`document.dispatchEvent(new KeyboardEvent('keydown', { key: 'n', ctrlKey: true, bubbles: true }))`);
  sonuc('Ctrl+N ekleme penceresini açtı', await bekleKosul(() => js(`!document.querySelector('#dlg-add').hidden`), 2000));
  await js(`document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))`);
  sonuc('Esc pencereyi kapattı', await bekleKosul(() => js(`document.querySelector('#dlg-add').hidden`), 2000));

  console.log('Sürükle-bırak ve eksik dosya');
  await js(`(() => { const dt = new DataTransfer(); dt.setData('text/uri-list', ${JSON.stringify(url('hizli', 'm.bin', 300 * K))}); document.body.dispatchEvent(new DragEvent('drop', { dataTransfer: dt, bubbles: true, cancelable: true })); })()`);
  sonuc('bağlantıyı pencereye bırakınca indirme eklendi', await bekleKosul(async () => (await satir('m.bin'))?.durum === 'completed', 15000));
  if (diskte('m.bin')) fs.rmSync(path.join(INDIR, 'm.bin'));
  await satirDugme('m.bin', 'open');
  sonuc('diskten silinmiş dosyayı açınca uyarı çıktı', await bekleKosul(() => toastVar('Dosya bulunamadı; taşınmış ya da silinmiş olabilir.'), 3000));
  await satirDugme('m.bin', 'remove');

  console.log('Ayarlar penceresi kaydırma');
  // Ayarlar penceresi ekrana sığmalı, içerik kendi içinde sonuna kadar kaymalı (en alttaki sürüm satırı görünür olmalı).
  const ayarOlc = () => js(`(() => { const d = document.querySelector('#dlg-settings .dialog'), s = document.querySelector('#dlg-settings .settings'); const r = d.getBoundingClientRect();
    s.scrollTop = s.scrollHeight; const k = s.getBoundingClientRect(), e = s.querySelector('.about').getBoundingClientRect();
    const o = { pencere: innerWidth + 'x' + innerHeight, ust: Math.round(r.top), alt: Math.round(r.bottom), icerik: s.scrollHeight, gorunen: s.clientHeight, kaydi: s.scrollTop,
      surumGorunur: e.top >= k.top - 1 && e.bottom <= Math.min(k.bottom, innerHeight) + 1 }; s.scrollTop = 0; return o; })()`);
  const pencereBoyu = (w, h) => js(`(async () => { window.runtime.WindowSetSize(${w}, ${h}); await new Promise(r => setTimeout(r, 700)); return innerWidth + 'x' + innerHeight; })()`);
  await tikla('#btn-settings'); await bekle(400);
  for (const [w, h, ad] of [[980, 660, 'normal boyut'], [620, 420, 'en küçük pencere']]) {
    await pencereBoyu(w, h);
    const o = await ayarOlc();
    sonuc(`ayarlar penceresi ekrana sığıyor (${ad})`, o.ust >= 0 && o.alt <= +o.pencere.split('x')[1], JSON.stringify(o));
    sonuc(`ayarlar sonuna kadar kayıyor (${ad})`, o.surumGorunur && (o.icerik <= o.gorunen || o.kaydi > 0), JSON.stringify(o));
  }
  await js(`document.querySelector('#dlg-settings [data-close]')?.click()`);
  // Öteki pencereler de en küçük boyutta düğmeleriyle birlikte ekranda kalmalı.
  await pencereBoyu(620, 420);
  await tikla('#btn-add'); await bekle(400);
  const ekleOlcu = await js(`(() => { const a = document.querySelector('#dlg-add .dialog-actions').getBoundingClientRect(); return { alt: Math.round(a.bottom), pencere: innerHeight }; })()`);
  sonuc('ekleme penceresinin düğmeleri en küçük pencerede görünüyor', ekleOlcu.alt <= ekleOlcu.pencere, JSON.stringify(ekleOlcu));
  await js(`document.querySelector('#dlg-add [data-close]')?.click()`);
  await pencereBoyu(980, 660);

  console.log('Tema ve dil');
  const ayarSec = (k, v) => js(`(() => { const e = document.querySelector('[data-set="${k}"]'); e.value = ${JSON.stringify(v)}; e.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
  await tikla('#btn-settings'); await bekle(300);
  await ayarSec('theme', 'dark'); await bekle(500);
  sonuc('koyu tema uygulandı', await js(`document.documentElement.dataset.theme === 'dark'`));
  await ayarSec('theme', 'light'); await bekle(500);
  sonuc('açık tema uygulandı', await js(`document.documentElement.dataset.theme === 'light'`));
  await ayarSec('language', 'en'); await bekle(500);
  sonuc('İngilizce arayüz', await js(`document.querySelector('#btn-add span').textContent === 'New download'`));
  await ayarSec('language', 'tr'); await bekle(800);
  sonuc('Türkçe arayüz', await js(`document.querySelector('#btn-add span').textContent === 'Yeni indirme'`));
  const ayar2 = JSON.parse(fs.readFileSync(path.join(VERI, 'HukukFixDM', 'settings.json'), 'utf8'));
  sonuc('tema ve dil diske yazıldı', ayar2.theme === 'light' && ayar2.language === 'tr', JSON.stringify({ theme: ayar2.theme, language: ayar2.language }));
  await js(`document.querySelector('#dlg-settings [data-close]')?.click()`);
}

// ---------- çalıştır ----------
await new Promise((r) => sunucu.listen(PORT, '127.0.0.1', r));
console.log(`Geçici klasör: ${GECICI}`);
ayarYaz();
try { await ac(); await testler(); } catch (e) { kalan++; hatalar.push('beklenmeyen hata: ' + e.message); console.log('  ✗ beklenmeyen hata', e.stack); }
finally { if (app && app.exitCode === null) await kapat(); sunucu.close(); }
console.log(`\n${gecen} geçti, ${kalan} kaldı${hatalar.length ? '\nKalanlar: ' + hatalar.join(' · ') : ''}`);
if (!process.env.HFDM_TUT) fs.rmSync(GECICI, { recursive: true, force: true });
process.exit(kalan ? 1 : 0);
