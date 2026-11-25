// Uçtan uca test: programın test sürümünü (HukukFixDM-e2e.exe, -tags e2e) geçici bir veri klasörüyle açar, arayüzdeki
// düğmelere gerçekten basar (e2e_hook.go: pencerede JavaScript çalıştırır) ve sonucu hem pencerede hem diskte doğrular. İndirmeler yalnız bu
// betiğin 127.0.0.1'de açtığı test sunucusundan yapılır; internete çıkılmaz. Kurulum geçici klasöre yapılır, kısayollar geçici
// klasördeki Masaustu/Programlar klasörlerine düşer; güncelleme testi GitHub yerine bu sunucuyu ve testin ürettiği geçici imza
// anahtarını kullanır, yeni sürüm olarak 9.9.9 numaralı test derlemesini (HukukFixDM-e2e-999.exe) sunar.
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
import { spawn, execFileSync } from 'node:child_process';

const KOK = path.resolve(path.dirname(new URL(import.meta.url).pathname.replace(/^\/(\w:)/, '$1')), '..', '..');
const EXE = process.env.HFDM_EXE || path.join(KOK, 'build', 'bin', 'HukukFixDM-e2e.exe');
const EXE999 = path.join(KOK, 'build', 'bin', 'HukukFixDM-e2e-999.exe'); // aynı kod, sürüm numarası 9.9.9
const HOST_YAPI = path.join(KOK, 'build', 'bin', 'HukukFixDM-host.exe');   // programın içine gömülen köprünün derlenmiş hâli
const SURUM = /var Version = "([^"]+)"/.exec(fs.readFileSync(path.join(KOK, 'internal', 'shared', 'shared.go'), 'utf8'))[1];
const GECICI = fs.mkdtempSync(path.join(os.tmpdir(), 'hfdm-e2e-'));
const VERI = path.join(GECICI, 'veri'), INDIR = path.join(GECICI, 'indirilenler'), KISAYOL = path.join(GECICI, 'kisayol');
const KURULU = path.join(VERI, 'HukukFixDM'), KURULU_EXE = path.join(KURULU, 'HukukFixDM.exe');
const HOST = path.join(KURULU, 'HukukFixDM-host.exe'); // tarayıcıların çalıştırdığı, programdan çıkarılan köprü
const KALDIR = 'HKCU\\Software\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\HukukFixDM';
const NMH_CHROME = 'HKCU\\Software\\Google\\Chrome\\NativeMessagingHosts\\com.hukukfix.downloadmanager';
const PORT = 47231, KANCA = 47232;
const bekle = (ms) => new Promise((r) => setTimeout(r, ms));
let gecen = 0, kalan = 0; const hatalar = [];
const sonuc = (ad, ok, ayrinti = '') => { if (ok) { gecen++; console.log(`  ✓ ${ad}`); } else { kalan++; hatalar.push(ad); console.log(`  ✗ ${ad}  ${ayrinti}`); } };

// ---------- güncelleme yayını (GitHub'ın yerine) ----------
// internal/update ile aynı imza metni; anahtar her test için yeniden üretilir ve programa HFDM_UPDATE_PUBKEY ile verilir.
const ANAHTAR = crypto.generateKeyPairSync('ed25519');
const PUB = Buffer.from(ANAHTAR.publicKey.export({ format: 'jwk' }).x, 'base64url').toString('base64');
function imzala(surum, veri, gizli = ANAHTAR.privateKey) {
  const sha256 = crypto.createHash('sha256').update(veri).digest('hex');
  const m = { version: surum, size: veri.length, sha256 };
  m.signature = crypto.sign(null, Buffer.from(`HukukFixDM update v1\nversion=${surum}\nsize=${veri.length}\nsha256=${sha256}\n`), gizli).toString('base64');
  return m;
}
let YAYIN = { tag: 'v0.0.1', exe: Buffer.alloc(0), sig: {} };
function yayinla(kip) {
  const yeni = fs.readFileSync(EXE999);
  if (kip === 'ayni') YAYIN = { tag: 'v' + SURUM, exe: yeni, sig: imzala(SURUM, yeni) };
  else if (kip === 'yabanci-anahtar') YAYIN = { tag: 'v9.9.9', exe: yeni, sig: imzala('9.9.9', yeni, crypto.generateKeyPairSync('ed25519').privateKey) };
  else if (kip === 'yeniden-etiket') YAYIN = { tag: 'v9.9.9', exe: yeni, sig: { ...imzala('1.0.0', yeni), version: '9.9.9' } };
  else if (kip === 'eski-imza') YAYIN = { tag: 'v9.9.9', exe: yeni, sig: imzala('1.0.0', yeni) };
  else if (kip === 'bozuk-dosya') { const b = Buffer.from(yeni); b[b.length >> 1] ^= 0xff; YAYIN = { tag: 'v9.9.9', exe: b, sig: imzala('9.9.9', yeni) }; }
  else if (kip === 'iyi') YAYIN = { tag: 'v9.9.9', exe: yeni, sig: imzala('9.9.9', yeni) };
  else if (kip === 'kapali') YAYIN = { kapali: true }; // GitHub'a ulaşılamıyor (internet henüz yok)
  else throw new Error('bilinmeyen yayın: ' + kip);
}
function yayinYanit(ad, res) {
  if (YAYIN.kapali) { res.writeHead(503); return res.end(); }
  const adres = (n) => `http://127.0.0.1:${PORT}/guncelleme/${n}`;
  const sig = Buffer.from(JSON.stringify(YAYIN.sig));
  if (ad === 'latest') {
    res.writeHead(200, { 'Content-Type': 'application/json' });
    return res.end(JSON.stringify({ tag_name: YAYIN.tag, html_url: 'https://github.com/saffetcelik/hukukfix-indirme-yoneticisi/releases/tag/' + YAYIN.tag, draft: false, prerelease: false,
      assets: [{ name: 'HukukFixDM.exe', size: YAYIN.exe.length, browser_download_url: adres('HukukFixDM.exe') }, { name: 'HukukFixDM.exe.sig', size: sig.length, browser_download_url: adres('HukukFixDM.exe.sig') }] }));
  }
  const veri = ad === 'HukukFixDM.exe' ? YAYIN.exe : ad === 'HukukFixDM.exe.sig' ? sig : null;
  if (!veri) { res.writeHead(404); return res.end(); }
  res.writeHead(200, { 'Content-Length': veri.length, 'Content-Type': 'application/octet-stream' });
  res.end(veri);
}

// ---------- test sunucusu ----------
// /hizli/<ad>?boy=N  Range destekli, hızlı · /yavas/<ad>?boy=N  bağlantı başına ~400 KB/s · /tek/<ad>?boy=N  Range yok
// /yok/<ad>  404 · içerik belirlenimli: bayt i = (i*31 + 7) & 255, böylece indirilen dosya bayt bayt doğrulanır.
const bayt = (bas, n) => { const b = Buffer.alloc(n); for (let i = 0; i < n; i++) b[i] = ((bas + i) * 31 + 7) & 255; return b; };
const sunucu = http.createServer((req, res) => {
  const u = new URL(req.url, 'http://x'); const [, tur, ad] = u.pathname.split('/'); const boy = +(u.searchParams.get('boy') || 0);
  if (tur === 'guncelleme') return yayinYanit(ad, res);
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
const ORTAM = { ...process.env, LOCALAPPDATA: VERI, HFDM_E2E_PORT: String(KANCA), HFDM_SHORTCUT_DIR: KISAYOL,
  HFDM_UPDATE_URL: `http://127.0.0.1:${PORT}/guncelleme/latest`, HFDM_UPDATE_PUBKEY: PUB,
  HFDM_UPDATE_FIRST: '1000', HFDM_UPDATE_RETRY: '3000' }; // ilk denetim 10 sn yerine 1 sn, yeniden deneme 10 dk yerine 3 sn
let pid = 0, kurucu = null; // pid: pencereyi açan (kurulu) program; kurucu: testin başlattığı, kendini kurup devreden exe
const calisiyor = (p) => { if (!p) return false; try { process.kill(p, 0); return true; } catch { return false; } };
const kopruPid = () => { try { return JSON.parse(fs.readFileSync(path.join(KURULU, 'bridge.json'), 'utf8')).pid; } catch { return 0; } };
function ayarYaz(ek = {}) {
  fs.mkdirSync(path.join(VERI, 'HukukFixDM'), { recursive: true }); fs.mkdirSync(INDIR, { recursive: true });
  for (const k of ['Masaustu', 'Programlar']) fs.mkdirSync(path.join(KISAYOL, k), { recursive: true }); // gerçek masaüstü ve Başlat menüsü gibi hep var
  const s = { downloadDir: INDIR, categoryFolders: false, maxConnections: 8, maxActive: 3, speedLimitKB: 0, capture: true, captureExts: ['zip', 'bin'], captureMinKB: 0,
    showOnCapture: false, notify: false, autostart: false, language: 'tr', theme: 'light', autoUpdate: false, ...ek };
  fs.writeFileSync(path.join(VERI, 'HukukFixDM', 'settings.json'), JSON.stringify(s));
}
// hazirBekle: bridge.json'a yeni bir program yazılana ve penceresi test kancasına yanıt verene kadar bekler.
async function hazirBekle(eski = 0) {
  let hazir = false;
  for (let i = 0; i < 160 && !hazir; i++) {
    await bekle(250);
    const p = kopruPid();
    if (!p || p === eski || !calisiyor(p)) continue;
    // Kısa bekleme: sayfa henüz yüklenmediyse yanıt gelmez; 20 sn beklemek yerine hemen yeniden dene.
    try { hazir = await js(`!!(window.go && document.querySelector('#list') && typeof render === 'function' && Object.keys(settings).length)`, 1500); } catch {}
    if (hazir) pid = p;
  }
  if (!hazir) throw new Error('program açılmadı ya da test kancası yanıt vermedi');
}
// ac: kullanıcının indirdiği exe'yi çalıştırması gibi: test exe'si kendini kurar (ya da kurulu kopyayı bulur) ve ona devreder.
async function ac() {
  const eski = pid;
  kurucu = spawn(EXE, [], { env: ORTAM, stdio: 'ignore' });
  await hazirBekle(eski);
  await bekle(600);
}
async function js(ifade, sure = 20000) {
  const r = await fetch(`http://127.0.0.1:${KANCA}/js?t=${sure}`, { method: 'POST', body: ifade, signal: AbortSignal.timeout(sure + 5000) });
  if (!r.ok) throw new Error(`kanca ${r.status}`);
  const d = await r.json(); if (d.e) throw new Error(d.e); return d.v;
}
async function kapat() {
  const p = pid;
  js(`window.go.main.App.Quit()`).catch(() => {});
  await bekleKosul(() => !calisiyor(p), 15000);
  if (calisiyor(p)) try { process.kill(p); } catch {}
  await bekle(800);
}
// pencereden okumalar
const satirlar = () => js(`[...document.querySelectorAll('#list .row')].map(r => ({ id: r.dataset.id, ad: r.querySelector('.name').textContent, durum: r.dataset.state, meta: r.querySelector('.meta').textContent + ' | ' + r.querySelector('.size').textContent }))`);
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
  // ORTAM: host programı başlatırsa (connect) program da test ortamıyla (veri klasörü, test kancası) açılsın.
  const p = spawn(HOST, ['chrome-extension://e2e/'], { env: ORTAM, stdio: ['pipe', 'pipe', 'ignore'] });
  const parca = []; p.stdout.on('data', (d) => parca.push(d));
  for (const m of mesajlar) { const b = Buffer.from(JSON.stringify(m)); const h = Buffer.alloc(4); h.writeUInt32LE(b.length); p.stdin.write(Buffer.concat([h, b])); }
  p.stdin.end(); await new Promise((r) => p.on('exit', r));
  const buf = Buffer.concat(parca), out = []; let i = 0;
  while (i + 4 <= buf.length) { const n = buf.readUInt32LE(i); out.push(JSON.parse(buf.subarray(i + 4, i + 4 + n).toString('utf8'))); i += 4 + n; }
  return out;
}
const toastVar = (metin) => js(`[...document.querySelectorAll('#toasts .toast')].some(t => t.textContent === ${JSON.stringify(metin)})`);

const parcaVar = (ad) => fs.readdirSync(INDIR).some((f) => f.startsWith(ad) && f.endsWith('.hfdm'));
// kurulum ve Windows tarafı
const ayni = (a, b) => !!a && !!b && path.resolve(a).toLowerCase() === path.resolve(b).toLowerCase();
const esit = (a, b) => { try { return fs.readFileSync(a).equals(fs.readFileSync(b)); } catch { return false; } };
const ps = (komut, ek = {}) => { try { return execFileSync('powershell', ['-NoProfile', '-Command', komut], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'], env: { ...process.env, ...ek } }).trim(); } catch { return ''; } };
const surecYolu = (p) => ps(`(Get-Process -Id ${p}).Path`);
const regOku = (anahtar, ad) => { try { const o = execFileSync('reg', ['query', anahtar, ...(ad ? ['/v', ad] : ['/ve'])], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] }); const m = /REG_\w+\s+(.*)$/m.exec(o); return m ? m[1].trim() : null; } catch { return null; } };
const kisayollar = (klasor) => { try { return fs.readdirSync(path.join(KISAYOL, klasor)).filter((f) => f.endsWith('.lnk')).map((f) => path.join(KISAYOL, klasor, f)); } catch { return []; } };
const kisayolHedef = (lnk) => ps('(New-Object -ComObject WScript.Shell).CreateShortcut($env:HFDM_LNK).TargetPath', { HFDM_LNK: lnk });
const surumYazisi = () => js(`document.querySelector('#about-version').textContent`);
const guncDurum = () => js(`({ s: upd.state, v: upd.version || '', e: upd.error || '', serit: !document.querySelector('#update-bar').hidden, yazi: document.querySelector('#update-status').textContent })`);
const bekleyenGuncelleme = () => fs.existsSync(path.join(KURULU, 'update'));
const baslik = () => ps(`(Get-Process -Id ${pid}).MainWindowTitle`); // PowerShell Türkçe harfleri bozabilir: yalnız baş ve sonu karşılaştırılır
const baslikSurum = async (v) => { let b = ''; await bekleKosul(() => { b = baslik(); return b.startsWith('HukukFix') && b.endsWith(' ' + v); }, 5000); return b; };
const ipucuAcik = () => js(`!document.querySelector('#ext-hint').hidden`);
const kaldirmaKomutu = () => { const u = regOku(KALDIR, 'UninstallString') || ''; return u.endsWith('" --uninstall') ? u.slice(1, u.indexOf('"', 1)) : ''; };

// ---------- testler ----------
async function testler() {
  const K = 1024, MB = 1024 * 1024;
  console.log('Kurulum (tek exe kendini kurar)');
  sonuc('indirilen exe kurulu kopyayı başlatıp kapandı', await bekleKosul(() => kurucu.exitCode === 0, 10000), String(kurucu.exitCode));
  sonuc('program %LOCALAPPDATA%\\HukukFixDM içine kuruldu', esit(KURULU_EXE, EXE));
  sonuc('açık olan program kurulu kopya', ayni(surecYolu(pid), KURULU_EXE), surecYolu(pid));
  sonuc('gömülü tarayıcı köprüsü (HukukFixDM-host.exe) yanına çıkarıldı', esit(HOST, HOST_YAPI));
  const nh = JSON.parse(fs.readFileSync(path.join(KURULU, 'native-host.json'), 'utf8'));
  sonuc('köprü tanımı çıkarılan host\'u gösteriyor', ayni(nh.path, HOST), nh.path);
  sonuc('Chrome kaydı köprü tanımını gösteriyor', ayni(regOku(NMH_CHROME), path.join(KURULU, 'native-host.json')), regOku(NMH_CHROME));
  for (const [ad, klasor] of [['masaüstü', 'Masaustu'], ['Başlat menüsü', 'Programlar']]) {
    const k = kisayollar(klasor);
    sonuc(`${ad} kısayolu oluştu`, k.length === 1, JSON.stringify(k));
    sonuc(`${ad} kısayolu kurulu programı açıyor`, k.length === 1 && ayni(kisayolHedef(k[0]), KURULU_EXE), k[0] && kisayolHedef(k[0]));
  }
  sonuc('"Yüklü uygulamalar" kaydı (sürüm ve Kaldır komutu)', regOku(KALDIR, 'DisplayVersion') === SURUM && ayni(kaldirmaKomutu(), KURULU_EXE), `${regOku(KALDIR, 'DisplayVersion')} ${regOku(KALDIR, 'UninstallString')}`);
  sonuc('install.json kurulu sürümü yazıyor', JSON.parse(fs.readFileSync(path.join(KURULU, 'install.json'), 'utf8')).version === SURUM);
  { const b = await baslikSurum(SURUM); sonuc(`pencere başlığında sürüm (${SURUM})`, b.startsWith('HukukFix') && b.endsWith(' ' + SURUM), b); }
  sonuc('eklenti hiç bağlanmamışken sağ altta eklenti uyarısı çıktı', await bekleKosul(ipucuAcik, 4000));
  await tikla('#ext-hint-go'); await bekle(400);
  sonuc('"Nasıl eklenir?" Ayarlar\'ın Tarayıcı sayfasını açtı', await js(`!document.querySelector('#dlg-settings').hidden && !document.querySelector('.pane[data-pane="browser"]').hidden && document.querySelector('#set-tabs [data-pane="browser"]').classList.contains('on') && document.querySelector('#ext-hint').hidden`));
  await js(`document.querySelector('#dlg-settings [data-close]')?.click()`); await bekle(300);
  sonuc('uyarı kapandıktan sonra geri gelmedi', !(await ipucuAcik()));
  sonuc('ayarlarda sürüm ve "Güncellemeleri denetle"', (await surumYazisi()) === SURUM && await js(`!document.querySelector('#update-check').hidden && document.querySelector('#update-bar').hidden`));

  console.log('Ekleme ve tamamlanma');
  await ekle([url('hizli', 'a.bin', 3 * MB + 123), url('hizli', 'b.bin', 777 * K), url('tek', 'c.bin', 2 * MB + 5), url('yavas', 'd.bin', 120 * MB)]);
  sonuc('4 bağlantı eklenince 4 satır', (await satirlar()).length === 4, JSON.stringify(await satirlar()));
  sonuc('hızlı dosyalar tamamlandı', await bekleKosul(async () => (await satir('a.bin'))?.durum === 'completed' && (await satir('b.bin'))?.durum === 'completed'));
  sonuc('dosya türü rozeti Türkçe arayüzde de doğru ("BIN", "BİN" değil)', await js(`[...document.querySelectorAll('#list .row')].find(r => r.querySelector('.name').textContent === 'a.bin')?.querySelector('.tile').textContent === 'BIN'`));
  sonuc('tamamlanan satırda boyut ve yeşil onay', await js(`(() => { const r = [...document.querySelectorAll('#list .row')].find(r => r.querySelector('.name').textContent === 'a.bin'); return r.querySelector('.size').textContent === '3,00 MB' && r.querySelector('.st.ok svg') !== null && r.querySelector('.prog').hidden; })()`));
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
  for (const k of kisayollar('Masaustu')) fs.rmSync(k);
  const kuruluZaman = fs.statSync(KURULU_EXE).mtimeMs;
  await kapat(); await ac();
  sonuc('indirilen exe yeniden çalışınca kurulu kopyayı açtı (aynı sürüm yeniden kopyalanmadı)', ayni(surecYolu(pid), KURULU_EXE) && fs.statSync(KURULU_EXE).mtimeMs === kuruluZaman);
  sonuc('silinen masaüstü kısayolu yeniden oluştu', kisayollar('Masaustu').length === 1);
  sonuc('eklenti hâlâ bağlanmamışken uyarı yeniden açılışta tekrar çıktı', await bekleKosul(ipucuAcik, 4000));
  await tikla('#ext-hint-later'); await bekle(300);
  sonuc('"Sonra" uyarıyı kapattı', !(await ipucuAcik()));
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

  console.log('Ayarlar sayfaları ve pencere boyutları');
  // Ayarlar dört sayfadır. Her sayfada pencere ekrana sığmalı, içerik kendi içinde sonuna kadar kaymalı (sayfanın son öğesi görünmeli).
  const ayarOlc = (sayfa) => js(`(() => { const sekme = document.querySelector('#set-tabs [data-pane=${JSON.stringify(sayfa)}]'); sekme.click();
    const d = document.querySelector('#dlg-settings .dialog'), s = document.querySelector('#dlg-settings .settings'), p = s.querySelector('.pane[data-pane=${JSON.stringify(sayfa)}]');
    const r = d.getBoundingClientRect(); s.scrollTop = s.scrollHeight; const k = s.getBoundingClientRect(), e = p.lastElementChild.getBoundingClientRect();
    const o = { sayfa: ${JSON.stringify(sayfa)}, secili: sekme.classList.contains('on'), gorunur: !p.hidden, tekSayfa: [...s.querySelectorAll('.pane')].filter((x) => !x.hidden).length === 1,
      pencere: innerWidth + 'x' + innerHeight, ust: Math.round(r.top), alt: Math.round(r.bottom), icerik: s.scrollHeight, gorunen: s.clientHeight, kaydi: s.scrollTop,
      sonGorunur: e.top >= k.top - 1 && e.bottom <= Math.min(k.bottom, innerHeight) + 1 }; s.scrollTop = 0; return o; })()`);
  const pencereBoyu = (w, h) => js(`(async () => { window.runtime.WindowSetSize(${w}, ${h}); await new Promise(r => setTimeout(r, 700)); return innerWidth + 'x' + innerHeight; })()`);
  await tikla('#btn-settings'); await bekle(400);
  for (const [w, h, ad] of [[860, 560, 'açılış boyutu'], [620, 420, 'en küçük pencere']]) {
    await pencereBoyu(w, h);
    for (const sayfa of ['general', 'speed', 'browser', 'about']) {
      const o = await ayarOlc(sayfa);
      sonuc(`ayarlar "${sayfa}" sayfası açılıyor, ekrana sığıyor, sonuna kadar kayıyor (${ad})`,
        o.secili && o.gorunur && o.tekSayfa && o.ust >= 0 && o.alt <= +o.pencere.split('x')[1] && o.sonGorunur && (o.icerik <= o.gorunen || o.kaydi > 0), JSON.stringify(o));
    }
    const ust = await js(`(() => { const t = document.querySelector('.toolbar'), b = document.querySelector('#btn-settings').getBoundingClientRect(); return { tasma: t.scrollWidth > t.clientWidth, sag: Math.round(b.right), w: innerWidth }; })()`);
    sonuc(`üst çubuk taşmıyor, ayarlar düğmesi görünüyor (${ad})`, !ust.tasma && ust.sag <= ust.w, JSON.stringify(ust));
  }
  await js(`document.querySelector('#dlg-settings [data-close]')?.click()`);
  // Öteki pencereler de en küçük boyutta düğmeleriyle birlikte ekranda kalmalı.
  await pencereBoyu(620, 420);
  await tikla('#btn-add'); await bekle(400);
  const ekleOlcu = await js(`(() => { const a = document.querySelector('#dlg-add .dialog-actions').getBoundingClientRect(); return { alt: Math.round(a.bottom), pencere: innerHeight }; })()`);
  sonuc('ekleme penceresinin düğmeleri en küçük pencerede görünüyor', ekleOlcu.alt <= ekleOlcu.pencere, JSON.stringify(ekleOlcu));
  await js(`document.querySelector('#dlg-add [data-close]')?.click()`);
  await pencereBoyu(860, 560);

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

  console.log('Tarayıcı kapalı programı başlatır');
  await kapat();
  const [baglan] = await host([{ action: 'connect' }]);
  sonuc('uzantı kapalı programı başlattı', baglan?.ok === true, JSON.stringify(baglan));
  await hazirBekle(); await bekle(600);
  sonuc('köprünün başlattığı program kurulu kopya', ayni(surecYolu(pid), KURULU_EXE), surecYolu(pid));
  await js(`window.runtime.WindowShow()`); await bekle(2000);
  sonuc('eklenti bir kez bağlanınca bu kayıt kalıcı (extension.json)', fs.existsSync(path.join(KURULU, 'extension.json')));
  sonuc('eklenti bağlanmış kurulumda uyarı çıkmadı', !(await ipucuAcik()));

  console.log('Otomatik güncelleme (imza denetimi)');
  await tikla('#btn-settings'); await bekle(300); await tikla('#set-tabs [data-pane="about"]');
  yayinla('ayni');
  await tikla('#update-check');
  sonuc('yayındaki sürüm aynıyken "Güncel"', await bekleKosul(async () => (await guncDurum()).s === 'latest', 15000), JSON.stringify(await guncDurum()));
  sonuc('ayarlarda "Güncel" yazıyor', (await guncDurum()).yazi === 'Güncel', JSON.stringify(await guncDurum()));
  sonuc('elle denetleyince onaylı "En güncel sürümü kullanıyorsunuz" bildirimi', await bekleKosul(() => toastVar(`✓ En güncel sürümü kullanıyorsunuz (${SURUM})`), 3000));
  sonuc('"Güncel" yeşil onay işaretiyle', await js(`(() => { const l = document.querySelector('#update-line'); return !l.hidden && l.classList.contains('ok') && getComputedStyle(l.querySelector('i')).display !== 'none'; })()`));
  await tikla('#update-check');
  sonuc('denetlerken dönen gösterge ve "Denetleniyor…" görünüyor', await js(`(() => { const l = document.querySelector('#update-line'); return l.classList.contains('busy') && document.querySelector('#update-status').textContent === 'Denetleniyor…' && document.querySelector('#update-check').hidden; })()`));
  await bekleKosul(async () => (await guncDurum()).s === 'latest', 15000); await bekle(300);
  for (const [kip, ad, hata] of [
    ['yabanci-anahtar', 'başka bir anahtarla imzalanmış sürüm reddedildi', 'signature'],
    ['yeniden-etiket', 'imzası bozulmadan sürüm numarası değiştirilmiş paket reddedildi', 'signature'],
    ['eski-imza', 'eski bir sürümün gerçek imzası yeni sürüm diye sunulunca reddedildi', 'signature'],
    ['bozuk-dosya', 'imzalandıktan sonra değiştirilmiş exe reddedildi', 'mismatch'],
  ]) {
    yayinla(kip);
    await tikla('#update-check');
    const ok = await bekleKosul(async () => (await guncDurum()).s === 'error', 20000);
    const d = await guncDurum();
    sonuc(ad, ok && d.e === hata && !d.serit && !bekleyenGuncelleme(), JSON.stringify(d));
  }
  sonuc('reddedilince bildirimle de uyarıldı', await bekleKosul(() => toastVar('Güncelleme doğrulanamadı, kurulmadı'), 3000));
  sonuc('reddedilince "doğrulanamadı, kurulmadı" uyarısı', (await guncDurum()).yazi === 'Güncelleme doğrulanamadı, kurulmadı', JSON.stringify(await guncDurum()));
  sonuc('reddedilen sürümler kurulu exe\'ye dokunmadı', esit(KURULU_EXE, EXE));

  yayinla('iyi');
  await js(`(() => { const e = document.querySelector('[data-set="autoUpdate"]'); e.checked = true; e.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
  sonuc('"Otomatik güncelle" açılınca yeni sürüm indirildi', await bekleKosul(async () => (await guncDurum()).s === 'ready', 40000), JSON.stringify(await guncDurum()));
  sonuc('indirilen dosya imzalı sürümün aynısı', esit(path.join(KURULU, 'update', 'HukukFixDM.exe'), EXE999));
  await js(`document.querySelector('#dlg-settings [data-close]')?.click()`); await bekle(300);
  sonuc('pencerede "Yeni sürüm hazır: 9.9.9" şeridi', await js(`!document.querySelector('#update-bar').hidden && document.querySelector('#update-text').textContent === 'Yeni sürüm hazır: 9.9.9'`));
  const satirOnce = (await satirlar()).length, eskiPid = pid;
  await tikla('#update-apply');
  sonuc('"Yeniden başlat" eski sürümü kapattı', await bekleKosul(() => !calisiyor(eskiPid), 20000));
  await hazirBekle(eskiPid);
  sonuc('"9.9.9 sürümüne güncellendi" bildirimi', await bekleKosul(() => toastVar('9.9.9 sürümüne güncellendi'), 2500));
  sonuc('yeni sürüm açıldı (9.9.9)', (await surumYazisi()) === '9.9.9');
  { const b = await baslikSurum('9.9.9'); sonuc('pencere başlığı yeni sürümü gösteriyor', b.endsWith(' 9.9.9'), b); }
  sonuc('kurulu exe yeni sürümle değişti', esit(KURULU_EXE, EXE999));
  sonuc('install.json ve "Yüklü uygulamalar" 9.9.9', JSON.parse(fs.readFileSync(path.join(KURULU, 'install.json'), 'utf8')).version === '9.9.9' && regOku(KALDIR, 'DisplayVersion') === '9.9.9');
  sonuc('güncelleme klasörü ve eski exe temizlendi', await bekleKosul(() => !bekleyenGuncelleme() && !fs.readdirSync(KURULU).some((f) => f.endsWith('.old')), 5000), fs.readdirSync(KURULU).join(', '));
  sonuc('indirme listesi güncellemeden sonra yerinde', (await satirlar()).length === satirOnce);
  sonuc('yeni sürüm şerit göstermiyor', await js(`document.querySelector('#update-bar').hidden`));

  console.log('Kaldırma (Yüklü uygulamalar → Kaldır)');
  const dosyalar = JSON.stringify(fs.readdirSync(INDIR).sort());
  const kaldirici = spawn(KURULU_EXE, ['--uninstall', '--yes'], { env: ORTAM, stdio: 'ignore' });
  const kod = await new Promise((r) => kaldirici.on('exit', r));
  sonuc('kaldırma komutu bitti', kod === 0, String(kod));
  sonuc('açık program kapatıldı', !calisiyor(pid));
  sonuc('kısayollar silindi', kisayollar('Masaustu').length === 0 && kisayollar('Programlar').length === 0);
  sonuc('"Yüklü uygulamalar" kaydı silindi', regOku(KALDIR, 'DisplayVersion') === null);
  sonuc('tarayıcı köprüsü kaydı silindi', regOku(NMH_CHROME) === null);
  sonuc('program klasörü tümüyle silindi', await bekleKosul(() => !fs.existsSync(KURULU), 20000), fs.existsSync(KURULU) ? fs.readdirSync(KURULU).join(', ') : '');
  sonuc('indirilen dosyalara dokunulmadı', JSON.stringify(fs.readdirSync(INDIR).sort()) === dosyalar);
  pid = 0;

  console.log('Yeniden kurulum, açılışta kendiliğinden denetim ve açılışta güncelleme');
  yayinla('kapali');
  ayarYaz({ autoUpdate: true });
  await ac();
  sonuc('yeniden kuruldu (' + SURUM + ')', esit(KURULU_EXE, EXE) && (await surumYazisi()) === SURUM);
  sonuc('kısayollar geri geldi', kisayollar('Masaustu').length === 1 && kisayollar('Programlar').length === 1);
  sonuc('açılıştan hemen sonra kendiliğinden denetledi (GitHub\'a ulaşılamadı)', await bekleKosul(async () => { const d = await guncDurum(); return d.s === 'error' && d.e === 'network'; }, 8000), JSON.stringify(await guncDurum()));
  yayinla('iyi');
  sonuc('başarısız denetim kısa süre sonra kendiliğinden yinelendi ve yeni sürüm indi', await bekleKosul(async () => (await guncDurum()).s === 'ready', 40000), JSON.stringify(await guncDurum()));
  await tikla('#btn-settings'); await bekle(300); await tikla('#set-tabs [data-pane="about"]');
  sonuc('ayarlarda "9.9.9 hazır" ve "Yeniden başlat"', (await guncDurum()).yazi === '9.9.9 hazır' && await js(`!document.querySelector('#update-apply2').hidden && document.querySelector('#update-check').hidden`));
  await js(`document.querySelector('#dlg-settings [data-close]')?.click()`);
  await kapat();
  sonuc('kapanınca güncelleme kurulmayı bekliyor', bekleyenGuncelleme() && esit(KURULU_EXE, EXE));
  await ac();
  sonuc('açılışta güncelleme kuruldu (9.9.9)', (await surumYazisi()) === '9.9.9' && esit(KURULU_EXE, EXE999));
  sonuc('açılışta kurulunca da "9.9.9 sürümüne güncellendi" bildirimi', await bekleKosul(() => toastVar('9.9.9 sürümüne güncellendi'), 2500));
  sonuc('bekleyen güncelleme temizlendi', !bekleyenGuncelleme());
}

// ---------- çalıştır ----------
await new Promise((r) => sunucu.listen(PORT, '127.0.0.1', r));
console.log(`Geçici klasör: ${GECICI}`);
ayarYaz();
try { await ac(); await testler(); } catch (e) { kalan++; hatalar.push('beklenmeyen hata: ' + e.message); console.log('  ✗ beklenmeyen hata', e.stack); }
finally { if (calisiyor(pid)) await kapat(); sunucu.close(); }
console.log(`\n${gecen} geçti, ${kalan} kaldı${hatalar.length ? '\nKalanlar: ' + hatalar.join(' · ') : ''}`);
if (!process.env.HFDM_TUT) fs.rmSync(GECICI, { recursive: true, force: true });
process.exit(kalan ? 1 : 0);
