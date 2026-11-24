// HukukFix browser extension — service worker (Chrome / Edge / Brave, Manifest V3).
//
// Everything goes through the native messaging host (HukukFixDM-host.exe): the
// extension hands it a request, the host forwards it to the app and returns the
// answer, starting the app first when asked to. The browser never connects to
// 127.0.0.1 itself, so local-network restrictions in browsers cannot break it.
'use strict';

const HOST = 'com.hukukfix.downloadmanager';
const msg = (key, subs) => chrome.i18n.getMessage(key, subs) || key;

// ---------- talking to the app ----------
let lastError = '';

// call reaches the app. With launch=true a stopped app is started first;
// otherwise a stopped app simply yields null (see lastError for the reason).
async function call(path, body, launch = false) {
  try {
    const r = await chrome.runtime.sendNativeMessage(HOST, {
      action: 'call', method: body ? 'POST' : 'GET', path, body: body || null, launch,
    });
    if (r && r.ok) {
      lastError = '';
      return r.data;
    }
    // A refusal by the app still carries its answer (e.g. "declined").
    if (r && r.data && typeof r.data === 'object') return r.data;
    lastError = (r && r.error) || 'no answer';
  } catch (e) {
    lastError = String((e && e.message) || e); // e.g. "Specified native messaging host not found."
  }
  return null;
}

// ---------- capture rules (mirrored from the app so decisions are instant) ----------
// Used until the app has been reached for the first time.
const DEFAULT_EXTS = ('zip rar 7z tar gz bz2 xz tgz zst iso img cab exe msi msix appx apk dmg pkg deb rpm jar bin ' +
  'mp4 mkv webm avi mov wmv flv m4v mpg mpeg 3gp mp3 m4a aac flac wav ogg opus wma ' +
  'pdf doc docx xls xlsx ppt pptx epub torrent psd ttf otf').split(' ');
const DEFAULT_RULES = { capture: true, captureExts: DEFAULT_EXTS, captureMinKB: 0 };

async function rules() {
  const s = await chrome.storage.local.get(['rules', 'paused']);
  return { ...DEFAULT_RULES, ...(s.rules || {}), paused: !!s.paused };
}

async function rememberRules(st) {
  if (st && Array.isArray(st.captureExts)) {
    await chrome.storage.local.set({ rules: { capture: !!st.capture, captureExts: st.captureExts, captureMinKB: st.captureMinKB || 0, at: Date.now() } });
  }
}

async function status(launch = false, withItems = false) {
  const st = await call('/v1/status' + (withItems ? '?items=1' : ''), null, launch);
  if (st && st.ok) await rememberRules(st);
  return st && st.ok ? st : null;
}

function extOf(name) {
  const m = /\.([a-z0-9]{1,12})$/i.exec(String(name || '').split(/[?#]/)[0]);
  return m ? m[1].toLowerCase() : '';
}

function wanted(r, item) {
  if (!r.capture || r.paused) return false;
  const url = item.finalUrl || item.url || '';
  if (!/^https?:/i.test(url)) return false; // blob:, data:, file: stay in the browser
  if (item.byExtensionId) return false;
  let path = '';
  try { path = new URL(url).pathname; } catch {}
  const ext = extOf(item.filename) || extOf(path);
  if (!ext || !r.captureExts.includes(ext)) return false;
  if (r.captureMinKB > 0 && item.totalBytes > 0 && item.totalBytes < r.captureMinKB * 1024) return false;
  return true;
}

// ---------- handing a download to the app ----------
async function cookieHeader(url) {
  try {
    const list = await chrome.cookies.getAll({ url });
    return list.map((c) => `${c.name}=${c.value}`).join('; ');
  } catch {
    return '';
  }
}

// send hands a download over. auto=true marks one taken over from the browser
// (the app may decline it under its current settings); a download the user
// asked for explicitly is always accepted.
async function send({ url, filename, referer, mime, size, auto, kind, quality }) {
  const body = {
    url,
    kind: kind || '',
    quality: quality > 0 ? quality : 0,
    filename: filename || '',
    referer: referer || '',
    mime: mime || '',
    size: size > 0 ? size : 0,
    auto: !!auto,
    userAgent: navigator.userAgent,
    cookie: await cookieHeader(url),
  };
  const r = await call('/v1/download', body, true);
  if (r && r.ok) {
    flashBadge('↓', '#2f5bf0');
    return { ok: true };
  }
  if (r && r.declined) {
    await rememberRules(r); // our copy of the rules was out of date
    return { ok: false, declined: true };
  }
  return { ok: false, error: (r && r.error) || lastError || 'not running' };
}

// Chrome asks every extension before it names a download. If the file is one
// HukukFix should take, it is handed over first and only then cancelled here —
// when the app cannot be reached the browser download simply carries on.
chrome.downloads.onDeterminingFilename.addListener((item, suggest) => {
  (async () => {
    let taken = false;
    try {
      let r = await rules();
      // Capturing was switched off in the app: look again now and then, in
      // case it was switched back on (never starts the app).
      if (!r.capture && !r.paused && Date.now() - (r.at || 0) > 15000) {
        await status(false);
        r = await rules();
      }
      if (wanted(r, item)) {
        const url = item.finalUrl || item.url;
        const name = String(item.filename || '').split(/[\\/]/).pop();
        const res = await send({ url, filename: name, referer: item.referrer, mime: item.mime, size: item.totalBytes, auto: true });
        if (res.ok) {
          taken = true;
          try { await chrome.downloads.cancel(item.id); } catch {}
          try { await chrome.downloads.erase({ id: item.id }); } catch {}
        }
      }
    } catch {}
    if (!taken) { try { suggest(); } catch {} }
  })();
  return true; // suggest() is called asynchronously
});

// ---------- badge ----------
let badgeTimer = 0;
function flashBadge(text, color) {
  chrome.action.setBadgeBackgroundColor({ color });
  chrome.action.setBadgeText({ text });
  clearTimeout(badgeTimer);
  badgeTimer = setTimeout(() => chrome.action.setBadgeText({ text: '' }), 3000);
}

// ---------- context menu ----------
function createMenus() {
  chrome.contextMenus.removeAll(() => {
    chrome.contextMenus.create({ id: 'hfdm-link', title: msg('menuDownload'), contexts: ['link', 'video', 'audio', 'image'] });
  });
}
chrome.runtime.onInstalled.addListener(() => { createMenus(); status(); });
chrome.runtime.onStartup.addListener(() => { createMenus(); status(); });
chrome.contextMenus.onClicked.addListener(async (info, tab) => {
  if (info.menuItemId !== 'hfdm-link') return;
  const url = info.linkUrl || info.srcUrl;
  if (!url || !/^https?:/i.test(url)) return;
  const r = await send({ url, referer: info.pageUrl || (tab && tab.url) });
  if (!r.ok) flashBadge('!', '#d93a40');
});

// ---------- media on the page ----------
// Two kinds of media are noticed while a page plays them:
//   file — a progressive audio/video file (what a <video src> loads), downloadable as-is;
//   hls  — an HLS playlist (.m3u8); the app fetches its segments and joins them.
// DRM-protected and live streams cannot be saved and are not offered.
const MEDIA_MIN = 512 * 1024;
const SKIP_SITES = /(^|\.)(youtube\.com|youtube-nocookie\.com|youtu\.be|googlevideo\.com)$/i;
const mediaKey = (tabId) => 'media:' + tabId;

function header(headers, name) {
  const h = (headers || []).find((x) => x.name.toLowerCase() === name);
  return h ? h.value : '';
}
function hostOf(u) {
  try { return new URL(u).hostname; } catch { return ''; }
}

chrome.webRequest.onHeadersReceived.addListener(
  (d) => {
    if (d.tabId < 0 || d.statusCode >= 400) return;
    if (SKIP_SITES.test(hostOf(d.url)) || SKIP_SITES.test(hostOf(d.initiator || ''))) return;
    const type = header(d.responseHeaders, 'content-type').split(';')[0].trim().toLowerCase();
    // Browsers send only the origin as Referer to another site; servers expect exactly that.
    const ref = d.initiator && /^https?:/i.test(d.initiator) ? d.initiator + '/' : '';
    let path = '';
    try { path = new URL(d.url).pathname.toLowerCase(); } catch {}
    if (/mpegurl/.test(type) || path.endsWith('.m3u8')) {
      addMedia(d.tabId, { kind: 'hls', url: d.url, type: 'hls', size: 0, ref });
      return;
    }
    if (d.type !== 'media') return;
    if (!/^(video|audio)\//.test(type) || /mp2t|segment/.test(type)) return;
    let size = parseInt(header(d.responseHeaders, 'content-length'), 10) || 0;
    const range = /\/(\d+)\s*$/.exec(header(d.responseHeaders, 'content-range'));
    if (range) size = parseInt(range[1], 10);
    if (size < MEDIA_MIN) return;
    addMedia(d.tabId, { kind: 'file', url: d.url, type, size, ref });
  },
  { urls: ['http://*/*', 'https://*/*'], types: ['media', 'xmlhttprequest', 'other'] },
  ['responseHeaders']
);

function mediaName(url, type) {
  let name = '';
  try { name = decodeURIComponent(new URL(url).pathname.split('/').filter(Boolean).pop() || ''); } catch {}
  if (!name) name = 'media';
  if (!extOf(name)) {
    const ext = { 'video/mp4': 'mp4', 'video/webm': 'webm', 'video/quicktime': 'mov', 'audio/mpeg': 'mp3', 'audio/mp4': 'm4a', 'audio/ogg': 'ogg', 'audio/webm': 'weba' }[type];
    if (ext) name += '.' + ext;
  }
  return name;
}

// Everything that touches a tab's media list runs through this chain, one at a time.
let mediaChain = Promise.resolve();
function queued(fn) {
  const p = mediaChain.then(fn);
  mediaChain = p.catch(() => {});
  return p;
}

function addMedia(tabId, m) {
  queued(async () => {
    const key = mediaKey(tabId);
    const list = (await chrome.storage.session.get(key))[key] || [];
    const id = m.kind === 'hls' ? m.url.split('#')[0] : m.url.split(/[?#]/)[0];
    if (list.some((x) => x.id === id)) return;
    list.unshift({ ...m, id, name: m.kind === 'hls' ? '' : mediaName(m.url, m.type) });
    if (list.length > 30) list.length = 30;
    await chrome.storage.session.set({ [key]: list });
  }).catch(() => {});
}

// inspectPlaylist reads an HLS playlist to learn what it offers: the available
// heights of a master playlist, or whether a media playlist can be saved at all.
async function inspectPlaylist(url) {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), 6000);
  try {
    const r = await fetch(url, { credentials: 'include', cache: 'no-store', signal: ctrl.signal });
    if (!r.ok) return { bad: true };
    const text = (await r.text()).slice(0, 2 << 20);
    if (!text.trimStart().startsWith('#EXTM3U')) return { bad: true };
    const base = r.url || url;
    const abs = (ref) => { try { return new URL(ref.trim(), base).href.split('#')[0]; } catch { return ''; } };
    if (!text.includes('#EXT-X-STREAM-INF')) {
      if (/METHOD=SAMPLE-AES/i.test(text)) return { bad: true };
      return { live: !/#EXT-X-ENDLIST/.test(text) && !/#EXT-X-PLAYLIST-TYPE:\s*VOD/.test(text) };
    }
    const heights = new Set();
    const children = [];
    let pending = false;
    for (const raw of text.split(/\r?\n/)) {
      const line = raw.trim();
      if (!line) continue;
      if (line.startsWith('#EXT-X-STREAM-INF')) {
        const m = /RESOLUTION=\d+x(\d+)/i.exec(line);
        if (m) heights.add(parseInt(m[1], 10));
        pending = true;
      } else if (line.startsWith('#EXT-X-MEDIA') || line.startsWith('#EXT-X-I-FRAME-STREAM-INF')) {
        const m = /URI="([^"]+)"/.exec(line);
        if (m) children.push(abs(m[1]));
      } else if (!line.startsWith('#') && pending) {
        children.push(abs(line));
        pending = false;
      }
    }
    return { master: true, heights: [...heights].sort((a, b) => b - a), children };
  } catch {
    return { bad: true };
  } finally {
    clearTimeout(timer);
  }
}

// listMedia returns what can be downloaded from a tab, ready to show: one
// entry per file and one per available quality of each stream.
function listMedia(tabId, extraSrc) {
  return queued(async () => {
    const key = mediaKey(tabId);
    const list = (await chrome.storage.session.get(key))[key] || [];
    let changed = false;
    for (const m of list) {
      if (m.kind === 'hls' && !m.info) {
        m.info = await inspectPlaylist(m.url);
        changed = true;
      }
    }
    if (changed) await chrome.storage.session.set({ [key]: list });

    // Playlists that a listed master already points to are not shown twice.
    const children = new Set();
    for (const m of list) for (const c of (m.info && m.info.children) || []) children.add(c);
    const out = [];
    for (const m of list) {
      if (m.kind !== 'hls') {
        out.push({ kind: 'file', url: m.url, name: m.name, type: m.type, size: m.size, ref: m.ref, label: '' });
        continue;
      }
      const i = m.info || { bad: true };
      if (i.bad || i.live || (!i.master && children.has(m.id))) continue;
      const heights = i.master && i.heights.length ? i.heights : [0];
      for (const h of heights) out.push({ kind: 'hls', url: m.url, quality: h, ref: m.ref, label: h ? h + 'p' : 'HLS' });
    }
    // A plain <video src="https://…"> that was not seen on the network yet.
    if (extraSrc && /^https?:/i.test(extraSrc) && !SKIP_SITES.test(hostOf(extraSrc)) &&
      !/\.m3u8($|\?)/i.test(extraSrc) && !list.some((m) => m.url.split(/[?#]/)[0] === extraSrc.split(/[?#]/)[0])) {
      out.push({ kind: 'file', url: extraSrc, name: mediaName(extraSrc, ''), type: '', size: 0, ref: '', label: '' });
    }
    chrome.action.setBadgeBackgroundColor({ tabId, color: '#2f5bf0' });
    chrome.action.setBadgeText({ tabId, text: out.length ? String(out.length) : '' }).catch(() => {});
    return out;
  });
}

function clearMedia(tabId) {
  queued(() => chrome.storage.session.remove(mediaKey(tabId))).catch(() => {});
  chrome.action.setBadgeText({ tabId, text: '' }).catch(() => {});
}
const pageOf = (u) => String(u || '').split('#')[0];
const pageKey = (tabId) => 'page:' + tabId;
chrome.tabs.onUpdated.addListener((tabId, info) => {
  if (!info.url) return;
  // A new page (not just a new #fragment) starts with an empty list. The page
  // is remembered in session storage because this worker does not stay alive.
  queued(async () => {
    const key = pageKey(tabId);
    const page = pageOf(info.url);
    if ((await chrome.storage.session.get(key))[key] === page) return;
    await chrome.storage.session.set({ [key]: page });
    await chrome.storage.session.remove(mediaKey(tabId));
    chrome.action.setBadgeText({ tabId, text: '' }).catch(() => {});
  }).catch(() => {});
});
chrome.tabs.onRemoved.addListener((tabId) => { clearMedia(tabId); chrome.storage.session.remove(pageKey(tabId)); });

// A video's file name comes from the page title; the app adds the extension.
function titleName(title) {
  return String(title || '').replace(/[\\/:*?"<>|\x00-\x1f]+/g, ' ').replace(/\s+/g, ' ').trim().slice(0, 120);
}

function sendMedia(m, title, pageUrl) {
  return send({
    url: m.url,
    kind: m.kind === 'hls' ? 'hls' : '',
    quality: m.quality || 0,
    filename: m.kind === 'hls' ? titleName(title) : m.filename || m.name || '',
    referer: m.ref || pageUrl || '',
    mime: m.kind === 'hls' ? '' : m.type || m.mime || '',
  });
}

// ---------- popup and page button ----------
chrome.runtime.onMessage.addListener((m, sender, reply) => {
  (async () => {
    try {
      switch (m && m.type) {
        case 'state': {
          const st = await status(false, true);
          const r = await rules();
          const media = m.tabId != null ? await listMedia(m.tabId) : [];
          reply({ online: !!st, version: st && st.version, paused: r.paused, capture: r.capture, items: (st && st.items) || [], media, error: st ? '' : lastError });
          break;
        }
        case 'page-media': { // from the button shown over a playing video
          const r = await rules();
          const tab = sender.tab;
          reply({ items: tab && !r.paused ? await listMedia(tab.id, m.src) : [], title: (tab && tab.title) || '' });
          break;
        }
        case 'start': reply({ ok: !!(await status(true)), error: lastError }); break;
        case 'pause-capture': await chrome.storage.local.set({ paused: !!m.paused }); reply({ ok: true }); break;
        case 'action': reply((await call('/v1/action', { id: m.id, action: m.action })) || { ok: false }); break;
        case 'download': reply(await sendMedia(m.item || m, m.title || (sender.tab && sender.tab.title), m.pageUrl || (sender.tab && sender.tab.url))); break;
        default: reply(null);
      }
    } catch (e) {
      reply({ ok: false, error: String(e) });
    }
  })();
  return true;
});
