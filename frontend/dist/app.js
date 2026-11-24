'use strict';

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));
const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

const ICON = {
  pause: '<path d="M5.5 3.5v9M10.5 3.5v9"/>',
  play: '<path d="M5 3.5v9l7-4.5z"/>',
  retry: '<path d="M13 8a5 5 0 1 1-1.5-3.6M13 3v2.5h-2.5"/>',
  x: '<path d="m4 4 8 8M12 4l-8 8"/>',
  open: '<path d="M9 3h4v4M13 3 7.5 8.5M11 9.5V12a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1h2.5"/>',
  folder: '<path d="M2 4.5a1 1 0 0 1 1-1h3l1.5 1.5H13a1 1 0 0 1 1 1v5.5a1 1 0 0 1-1 1H3a1 1 0 0 1-1-1z"/>',
  link: '<path d="M6.5 9.5a2.5 2.5 0 0 0 3.5 0l2-2a2.5 2.5 0 0 0-3.5-3.5l-.7.7M9.5 6.5a2.5 2.5 0 0 0-3.5 0l-2 2A2.5 2.5 0 0 0 7.500 12l.7-.7"/>',
  trash: '<path d="M3 4.5h10M6.500 4.500V3h3v1.500M4.500 4.500l.5 8h6l.5-8"/>',
  remove: '<path d="M4 8h8"/>',
};
const svg = (name) => `<svg viewBox="0 0 16 16">${ICON[name]}</svg>`;

let api = null;
let T = STRINGS.en;
let lang = 'en';
let sysLang = 'en';
let settings = {};
let defaultExts = [];
let extInfo = { dir: '', hostFound: true, connected: false, browsers: [] };
let upd = { state: 'idle' };
let appVersion = '';
let manualCheckAt = 0; // set while a check started from Settings is running: its result is announced
let hintReady = false, hintClosed = false;
let updDismissed = '';
let stats = { active: 0, speed: 0 };

const items = new Map();
let order = [];
let filter = 'all';
let query = '';
let limit = 300;

function t(key, vars) {
  let s = T[key] ?? STRINGS.en[key] ?? key;
  if (vars) for (const k in vars) s = s.replace('{' + k + '}', vars[k]);
  return s;
}

// ---------- formatting ----------
function num(n, digits) {
  return n.toLocaleString(lang, { minimumFractionDigits: digits, maximumFractionDigits: digits });
}
function fmtBytes(n) {
  if (!(n >= 0)) return '—';
  if (n < 1024) return n + ' B';
  const units = ['KB', 'MB', 'GB', 'TB'];
  let v = n / 1024, i = 0;
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
  return num(v, v >= 100 || i === 0 ? 0 : v >= 10 ? 1 : 2) + ' ' + units[i];
}
const fmtSpeed = (n) => fmtBytes(Math.round(n)) + '/s';
function fmtEta(s) {
  if (!(s >= 0)) return '';
  if (s < 60) return `${Math.max(1, Math.round(s))} ${t('sec')}`;
  if (s < 3600) { const m = Math.floor(s / 60), r = Math.round(s % 60); return m >= 10 || !r ? `${m} ${t('min')}` : `${m} ${t('min')} ${r} ${t('sec')}`; }
  if (s < 86400) { const h = Math.floor(s / 3600), m = Math.round((s % 3600) / 60); return m ? `${h} ${t('hour')} ${m} ${t('min')}` : `${h} ${t('hour')}`; }
  return `${Math.floor(s / 86400)} ${t('day')} ${Math.floor((s % 86400) / 3600)} ${t('hour')}`;
}
function fmtDate(ms) {
  if (!ms) return '';
  const d = new Date(ms), now = new Date();
  const time = d.toLocaleTimeString(lang, { hour: '2-digit', minute: '2-digit' });
  const day = (x) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const diff = Math.round((day(now) - day(d)) / 864e5);
  if (diff === 0) return time;
  if (diff === 1) return `${t('yesterday')} ${time}`;
  return d.toLocaleDateString(lang, { day: 'numeric', month: 'short', year: d.getFullYear() === now.getFullYear() ? undefined : 'numeric' });
}
function errorText(s) {
  switch (s.errorCode) {
    case 'http': return s.errorDetail === '403' ? t('eHttp403') : s.errorDetail === '404' ? t('eHttp404') : t('eHttp', { d: s.errorDetail });
    case 'network': return t('eNetwork');
    case 'dns': return t('eDns');
    case 'disk_full': return t('eDiskFull');
    case 'disk': return t('eDisk');
    case 'html_page': return t('eHtml');
    case 'changed': return t('eChanged');
    case 'live': return t('eLive');
    case 'drm': return t('eDrm');
    case 'hls': return t('eHls');
    case 'hls_audio': return t('eHlsAudio');
    default: return t('eUnknown');
  }
}

// ---------- i18n / theme ----------
function applyI18n() {
  lang = settings.language === 'tr' || settings.language === 'en' ? settings.language : sysLang;
  T = STRINGS[lang] || STRINGS.en;
  document.documentElement.lang = lang;
  for (const el of $$('[data-t]')) el.textContent = t(el.dataset.t);
  for (const el of $$('[data-tp]')) el.placeholder = t(el.dataset.tp);
  for (const el of $$('[data-tt]')) { el.title = t(el.dataset.tt); el.setAttribute('aria-label', t(el.dataset.tt)); }
  for (const el of $$('#list .row')) el._sig = null;
  render();
  renderStatus();
  renderExt();
  renderUpdate();
}
const darkQuery = matchMedia('(prefers-color-scheme: dark)');
function applyTheme() {
  const root = document.documentElement;
  if (settings.theme === 'light' || settings.theme === 'dark') root.dataset.theme = settings.theme;
  else delete root.dataset.theme;
  root.classList.toggle('sys-dark', darkQuery.matches);
}
darkQuery.addEventListener('change', applyTheme);

// ---------- state ----------
function onState(p) {
  if (p.all) {
    order = p.all.map((s) => s.id);
    const keep = new Set(order);
    for (const id of items.keys()) if (!keep.has(id)) items.delete(id);
    for (const s of p.all) items.set(s.id, s);
  }
  for (const s of p.live || []) if (items.has(s.id)) items.set(s.id, s);
  stats = { active: p.active, speed: p.speed };
  scheduleRender();
}
let renderQueued = false;
function scheduleRender() {
  if (renderQueued) return;
  renderQueued = true;
  requestAnimationFrame(() => { renderQueued = false; render(); renderStatus(); });
}

const isActive = (s) => s.state !== 'completed';
function matches(s) {
  if (filter === 'active' && !isActive(s)) return false;
  if (filter === 'done' && isActive(s)) return false;
  if (query && !(s.fileName.toLowerCase().includes(query) || s.host.toLowerCase().includes(query))) return false;
  return true;
}

// ---------- list rendering ----------
const list = $('#list');

function createRow(id) {
  const el = document.createElement('div');
  el.className = 'row';
  el.dataset.id = id;
  el.innerHTML = '<div class="tile"></div><div class="body"><div class="top"><span class="name"></span><span class="pct"></span></div><div class="prog" hidden></div><div class="meta"></div></div><div class="acts"></div>';
  return el;
}

function progressImage(s) {
  if (!(s.total > 0)) return '';
  const segs = s.segments && s.segments.length ? s.segments : [[0, s.total - 1, s.received]];
  const stops = ['transparent 0'];
  for (const [a, e, p] of segs) {
    if (p <= a) continue;
    const x0 = (a / s.total * 100).toFixed(3), x1 = (Math.min(p, e + 1) / s.total * 100).toFixed(3);
    stops.push(`transparent ${x0}%`, `var(--accent) ${x0}%`, `var(--accent) ${x1}%`, `transparent ${x1}%`);
  }
  return stops.length > 1 ? `linear-gradient(90deg,${stops.join(',')})` : '';
}

function actionsFor(s) {
  switch (s.state) {
    case 'completed': return [['open', 'aOpen', 'open'], ['folder', 'aFolder', 'folder'], ['remove', 'aRemove', 'x']];
    case 'failed': return [['resume', 'aRetry', 'retry'], ['remove', 'aRemove', 'x']];
    case 'paused': return [['resume', 'aResume', 'play'], ['cancel', 'aCancel', 'x']];
    default: return [['pause', 'aPause', 'pause'], ['cancel', 'aCancel', 'x']];
  }
}

function metaHTML(s) {
  const sep = '<i>·</i>';
  const size = s.total > 0 ? t('of', { a: fmtBytes(s.received), b: fmtBytes(s.total) }) : fmtBytes(s.received);
  switch (s.state) {
    case 'completed':
      return [fmtBytes(s.total > 0 ? s.total : s.received), esc(s.host), fmtDate(s.finishedAt)].filter(Boolean).join(sep);
    case 'failed':
      return `<span class="s-err">${esc(errorText(s))}</span>`;
    case 'paused':
      return [t('stPaused'), s.received > 0 ? size : ''].filter(Boolean).join(sep);
    case 'queued':
      return [t('stQueued'), s.received > 0 ? size : ''].filter(Boolean).join(sep);
    case 'connecting':
      return [t('stConnecting'), esc(s.host)].join(sep);
    default: {
      const parts = [size, `<span class="s-speed">${fmtSpeed(s.speed)}</span>`];
      if (s.eta >= 0) parts.push(t('left', { t: fmtEta(s.eta) }));
      if (s.conns > 1) parts.push(t('conns', { n: s.conns }));
      if (!s.resumable) parts.push(t('noResume'));
      return parts.join(sep);
    }
  }
}

function updateRow(el, s) {
  const sig = [s.state, s.fileName, s.received, s.total, Math.round(s.speed), s.eta, s.conns, s.errorCode, s.segments ? s.segments.length : 0].join('|');
  if (el._sig === sig) return;
  el._sig = sig;
  const live = s.state === 'downloading' || s.state === 'connecting';
  el.dataset.state = s.state;
  el.classList.toggle('live', live);

  const tile = el.children[0];
  const m = /\.([a-z0-9]{1,8})$/i.exec(s.fileName);
  tile.dataset.c = s.category;
  tile.textContent = m ? m[1].slice(0, 4) : '···';

  const name = $('.name', el);
  if (name.textContent !== s.fileName) { name.textContent = s.fileName; name.title = s.fileName; }

  const prog = $('.prog', el);
  const showBar = s.state !== 'completed' && s.state !== 'failed' || (s.state === 'failed' && s.received > 0 && s.total > 0);
  prog.hidden = !showBar;
  if (showBar) {
    const indet = live && !(s.total > 0);
    prog.classList.toggle('indet', indet);
    prog.style.backgroundImage = indet ? '' : progressImage(s);
  }
  $('.pct', el).textContent = s.state !== 'completed' && s.total > 0 && s.received > 0 ? Math.floor(s.received / s.total * 100) + '%' : '';
  $('.meta', el).innerHTML = metaHTML(s);

  const acts = el.children[2];
  if (acts.dataset.for !== s.state + lang) {
    acts.dataset.for = s.state + lang;
    acts.innerHTML = actionsFor(s).map(([act, label, icon]) =>
      `<button class="btn icon" data-act="${act}" title="${esc(t(label))}" aria-label="${esc(t(label))}">${svg(icon)}</button>`).join('');
  }
}

function render() {
  let nActive = 0, nDone = 0;
  for (const id of order) { const s = items.get(id); if (!s) continue; if (isActive(s)) nActive++; else nDone++; }
  $('#n-all').textContent = order.length;
  $('#n-active').textContent = nActive;
  $('#n-done').textContent = nDone;
  $('#btn-clear').hidden = nDone === 0;

  const ids = order.filter((id) => items.has(id) && matches(items.get(id)));
  const shown = ids.slice(0, limit);

  const existing = new Map();
  for (const el of Array.from(list.children)) {
    if (el.dataset.id) existing.set(el.dataset.id, el); else el.remove();
  }
  let cursor = list.firstElementChild;
  for (const id of shown) {
    let el = existing.get(id);
    if (el) existing.delete(id); else el = createRow(id);
    updateRow(el, items.get(id));
    if (el === cursor) cursor = cursor.nextElementSibling;
    else list.insertBefore(el, cursor);
  }
  for (const el of existing.values()) el.remove();
  if (ids.length > limit) {
    const more = document.createElement('button');
    more.className = 'btn more';
    more.textContent = `+ ${ids.length - limit}`;
    more.onclick = () => { limit += 300; render(); };
    list.appendChild(more);
  }

  const empty = ids.length === 0;
  list.hidden = empty;
  $('#empty').hidden = !empty;
  if (empty) {
    const kind = query ? 'emptySearch' : filter === 'active' && order.length ? 'emptyActive' : filter === 'done' && order.length ? 'emptyDone' : 'empty';
    $('#empty-title').textContent = t(kind + 'Title');
    $('#empty-text').innerHTML = t(kind + 'Text'); // our own static strings
  }
}

function renderStatus() {
  $('#st-speed').textContent = stats.active > 0 ? '↓ ' + fmtSpeed(stats.speed) : t('idle');
  $('#st-active').textContent = stats.active > 0 ? t('active', { n: stats.active }) : '';
  const lim = $('#st-limit');
  lim.hidden = !(settings.speedLimitKB > 0);
  if (!lim.hidden) lim.textContent = t('limitChip', { v: fmtSpeed(settings.speedLimitKB * 1024) });
  const chip = $('#st-ext');
  chip.firstElementChild.className = 'dot' + (extInfo.connected ? ' on' : extInfo.hostFound ? '' : ' warn');
  chip.lastElementChild.textContent = t(extInfo.connected ? 'extChipOn' : 'extChipOff');
}

// ---------- actions ----------
function toast(text, ms = 2600) {
  const el = document.createElement('div');
  el.className = 'toast';
  el.textContent = text;
  $('#toasts').appendChild(el);
  setTimeout(() => { el.classList.add('out'); setTimeout(() => el.remove(), 300); }, ms);
}

function confirmDialog(title, text, okLabel) {
  return new Promise((resolve) => {
    $('#confirm-title').textContent = title;
    $('#confirm-text').textContent = text;
    const ok = $('#confirm-ok');
    ok.textContent = okLabel;
    openDialog('dlg-confirm', () => resolve(false));
    ok.onclick = () => { closeDialog('dlg-confirm', true); resolve(true); };
    ok.focus();
  });
}

async function act(id, action) {
  const s = items.get(id);
  if (!s) return;
  switch (action) {
    case 'pause':
      if (!s.resumable && s.state === 'downloading' && s.received > 0 &&
        !(await confirmDialog(t('cPauseTitle'), t('cPauseText'), t('cPauseOk')))) return;
      api.Pause(id); break;
    case 'resume': api.Resume(id); break;
    case 'cancel':
      if (s.received > 0 && !(await confirmDialog(t('cCancelTitle'), t('cCancelText'), t('cCancelOk')))) return;
      api.Remove(id, false); break;
    case 'remove': api.Remove(id, false); break;
    case 'delete':
      if (!(await confirmDialog(t('cDeleteTitle'), t('cDeleteText', { name: s.fileName }), t('cDeleteOk')))) return;
      api.Remove(id, true); break;
    case 'open': api.OpenFile(id).catch(() => toast(t('fileMissing'))); break;
    case 'folder': api.ShowInFolder(id).catch(() => toast(t('fileMissing'))); break;
    case 'copy': api.CopyText(s.url); toast(t('copied')); break;
  }
}

list.addEventListener('click', (e) => {
  const btn = e.target.closest('[data-act]');
  const row = e.target.closest('.row');
  if (btn && row) act(row.dataset.id, btn.dataset.act);
});
list.addEventListener('dblclick', (e) => {
  const row = e.target.closest('.row');
  if (!row || e.target.closest('[data-act]')) return;
  const s = items.get(row.dataset.id);
  if (!s) return;
  act(s.id, s.state === 'completed' ? 'open' : s.state === 'paused' || s.state === 'failed' ? 'resume' : 'pause');
});

// ---------- context menu ----------
const menu = $('#menu');
function closeMenu() {
  menu.hidden = true;
  $$('.row.menu-open').forEach((r) => r.classList.remove('menu-open'));
}
list.addEventListener('contextmenu', (e) => {
  e.preventDefault();
  const row = e.target.closest('.row');
  closeMenu();
  if (!row) return;
  const s = items.get(row.dataset.id);
  if (!s) return;
  const entries = [];
  if (s.state === 'completed') entries.push(['open', 'aOpen', 'open']);
  else if (s.state === 'paused') entries.push(['resume', 'aResume', 'play']);
  else if (s.state === 'failed') entries.push(['resume', 'aRetry', 'retry']);
  else entries.push(['pause', 'aPause', 'pause']);
  entries.push(['folder', 'aFolder', 'folder'], ['copy', 'aCopyLink', 'link'], null);
  if (s.state === 'completed') entries.push(['remove', 'aRemove', 'remove'], ['delete', 'aDelete', 'trash', true]);
  else if (s.state === 'failed') entries.push(['remove', 'aRemove', 'remove']);
  else entries.push(['cancel', 'aCancel', 'x', true]);
  menu.innerHTML = entries.map((en) => en
    ? `<button data-act="${en[0]}"${en[3] ? ' class="danger"' : ''}>${svg(en[2])}${esc(t(en[1]))}</button>` : '<hr>').join('');
  menu.dataset.id = s.id;
  menu.hidden = false;
  row.classList.add('menu-open');
  const r = menu.getBoundingClientRect();
  menu.style.left = Math.min(e.clientX, innerWidth - r.width - 8) + 'px';
  menu.style.top = Math.min(e.clientY, innerHeight - r.height - 8) + 'px';
});
menu.addEventListener('click', (e) => {
  const b = e.target.closest('[data-act]');
  if (b) act(menu.dataset.id, b.dataset.act);
  closeMenu();
});
addEventListener('mousedown', (e) => { if (!menu.hidden && !menu.contains(e.target)) closeMenu(); });
addEventListener('blur', closeMenu);
list.addEventListener('scroll', closeMenu);
addEventListener('contextmenu', (e) => { if (!e.target.closest('input, textarea')) e.preventDefault(); });

// ---------- dialogs ----------
const dialogCancel = {};
function openDialog(id, onCancel) {
  closeMenu();
  dialogCancel[id] = onCancel;
  $('#' + id).hidden = false;
}
function closeDialog(id, accepted) {
  const el = $('#' + id);
  if (el.hidden) return;
  el.hidden = true;
  const cb = dialogCancel[id];
  dialogCancel[id] = null;
  if (cb && !accepted) cb();
}
const topDialog = () => $$('.overlay').filter((o) => !o.hidden).pop();
for (const ov of $$('.overlay')) {
  ov.addEventListener('mousedown', (e) => { if (e.target === ov) closeDialog(ov.id); });
  for (const b of $$('[data-close]', ov)) b.addEventListener('click', () => closeDialog(ov.id));
}

// ---------- add dialog ----------
let addDir = '';
async function openAdd(prefill) {
  const ta = $('#add-urls');
  $('#add-error').hidden = true;
  addDir = '';
  $('#add-dir').textContent = settings.downloadDir;
  ta.value = prefill || '';
  openDialog('dlg-add');
  ta.focus();
  if (!prefill) {
    const clip = await api.ClipboardURL();
    if (clip && !ta.value) { ta.value = clip; ta.select(); }
  }
}
async function submitAdd() {
  const text = $('#add-urls').value.trim();
  const err = $('#add-error');
  if (!text) { err.textContent = t('badUrl'); err.hidden = false; return; }
  try {
    const bad = await api.Add(text, addDir);
    closeDialog('dlg-add', true);
    const n = text.split('\n').filter((l) => l.trim()).length - bad;
    toast(bad > 0 ? t('someBad', { n: bad }) : n > 1 ? t('addedN', { n }) : t('added'));
    showAll();
  } catch {
    err.textContent = t('badUrl');
    err.hidden = false;
  }
}
function showAll() {
  if (filter === 'done') setFilter('all');
  list.scrollTop = 0;
}
$('#btn-add').onclick = () => openAdd();
$('#add-ok').onclick = submitAdd;
$('#add-urls').addEventListener('keydown', (e) => {
  if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); submitAdd(); }
});
$('#add-dir-btn').onclick = async () => {
  const dir = await api.ChooseFolder(addDir || settings.downloadDir);
  if (dir) { addDir = dir; $('#add-dir').textContent = dir; }
};

function looksLikeLinks(text) {
  const lines = text.split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
  return lines.length > 0 && lines.length <= 200 && lines.every((l) => /^https?:\/\/\S+$/i.test(l)) ? lines.join('\n') : '';
}
addEventListener('paste', (e) => {
  if (e.target.closest('input, textarea') || topDialog()) return;
  const links = looksLikeLinks(e.clipboardData.getData('text') || '');
  if (links) { e.preventDefault(); openAdd(links); }
});

// ---------- drag & drop ----------
let dragDepth = 0;
const hasLink = (e) => Array.from(e.dataTransfer?.types || []).some((x) => x === 'text/uri-list' || x === 'text/plain');
addEventListener('dragenter', (e) => { if (hasLink(e)) { dragDepth++; $('#drop').hidden = false; } });
addEventListener('dragleave', () => { if (--dragDepth <= 0) { dragDepth = 0; $('#drop').hidden = true; } });
addEventListener('dragover', (e) => { if (hasLink(e)) { e.preventDefault(); e.dataTransfer.dropEffect = 'copy'; } });
addEventListener('drop', async (e) => {
  e.preventDefault();
  dragDepth = 0;
  $('#drop').hidden = true;
  const links = looksLikeLinks(e.dataTransfer.getData('text/uri-list') || e.dataTransfer.getData('text/plain') || '');
  if (!links) return;
  try { await api.Add(links, ''); toast(t('added')); showAll(); } catch { toast(t('badUrl')); }
});

// ---------- toolbar / tabs ----------
function setFilter(f) {
  filter = f;
  limit = 300;
  for (const b of $$('#tabs [data-filter]')) b.classList.toggle('on', b.dataset.filter === f);
  render();
}
for (const b of $$('#tabs [data-filter]')) b.onclick = () => setFilter(b.dataset.filter);
$('#search').addEventListener('input', (e) => { query = e.target.value.trim().toLowerCase(); limit = 300; render(); });
$('#btn-pause-all').onclick = () => api.PauseAll();
$('#btn-resume-all').onclick = () => api.ResumeAll();
$('#btn-folder').onclick = () => api.OpenDownloadFolder();
$('#btn-clear').onclick = () => api.ClearFinished();
$('#btn-settings').onclick = () => openSettings();
$('#st-limit').onclick = () => openSettings();
$('#st-ext').onclick = () => openSettings(true);

addEventListener('keydown', (e) => {
  if (e.key === 'Escape') {
    if (!menu.hidden) return closeMenu();
    const d = topDialog();
    if (d) return closeDialog(d.id);
    if ($('#search').value) { $('#search').value = ''; query = ''; render(); }
    return;
  }
  if (!e.ctrlKey || topDialog()) return;
  const k = e.key.toLowerCase();
  if (k === 'n') { e.preventDefault(); openAdd(); }
  else if (k === 'f') { e.preventDefault(); $('#search').focus(); $('#search').select(); }
  else if (k === ',') { e.preventDefault(); openSettings(); }
});

// ---------- settings ----------
let saveTimer = 0;
function saveSettings() {
  clearTimeout(saveTimer);
  saveTimer = setTimeout(async () => {
    settings = await api.SaveSettings(settings);
    renderStatus();
  }, 250);
}
function fillSettings() {
  for (const el of $$('[data-set]')) {
    const v = settings[el.dataset.set];
    if (el.type === 'checkbox') el.checked = !!v; else el.value = v;
    if (el.type === 'range') el.nextElementSibling.textContent = v;
  }
  $('#set-dir').textContent = settings.downloadDir;
  $('#set-exts').value = (settings.captureExts || []).join(' ');
}
for (const el of $$('[data-set]')) {
  el.addEventListener(el.type === 'range' || el.type === 'number' ? 'input' : 'change', () => {
    const key = el.dataset.set;
    if (el.type === 'checkbox') settings[key] = el.checked;
    else if (el.type === 'range' || el.type === 'number') settings[key] = Math.max(0, parseInt(el.value, 10) || 0);
    else settings[key] = el.value;
    if (el.type === 'range') el.nextElementSibling.textContent = el.value;
    if (key === 'theme') applyTheme();
    if (key === 'language') applyI18n();
    saveSettings();
  });
}
$('#set-dir-btn').onclick = async () => {
  const dir = await api.ChooseFolder(settings.downloadDir);
  if (dir) { settings.downloadDir = dir; $('#set-dir').textContent = dir; saveSettings(); }
};
$('#set-exts').addEventListener('change', (e) => {
  settings.captureExts = e.target.value.toLowerCase().split(/[\s,;]+/).map((x) => x.replace(/^\./, '')).filter(Boolean);
  saveSettings();
});
$('#exts-reset').onclick = () => { settings.captureExts = defaultExts.slice(); $('#set-exts').value = defaultExts.join(' '); saveSettings(); };
$('#ext-copy').onclick = () => { api.CopyText(extInfo.dir); toast(t('copied')); };
$('#ext-open').onclick = () => api.OpenExtensionFolder();

const BROWSER_NAMES = { chrome: 'Chrome', edge: 'Edge', brave: 'Brave' };
// The hint shows until the extension has reached this installation once ("Later" hides it until the next start).
function renderHint() {
  $('#ext-hint').hidden = !hintReady || hintClosed || extInfo.known || extInfo.connected;
}
$('#ext-hint-go').onclick = () => { hintClosed = true; renderHint(); openSettings(true); };
$('#ext-hint-later').onclick = () => { hintClosed = true; renderHint(); };

function renderExt() {
  $('#ext-state').textContent = t(extInfo.connected ? 'extConnected' : 'extNotConnected');
  $('#ext-card .dot').className = 'dot' + (extInfo.connected ? ' on' : '');
  $('#ext-dir').textContent = extInfo.dir;
  $('#ext-warn').hidden = extInfo.hostFound;
  renderHint();
  const box = $('#ext-browsers');
  box.innerHTML = '';
  for (const b of extInfo.browsers || []) {
    const btn = document.createElement('button');
    btn.className = 'btn';
    btn.textContent = BROWSER_NAMES[b] || b;
    btn.onclick = () => api.OpenExtensionsPage(b);
    box.appendChild(btn);
  }
}
async function refreshExt() {
  extInfo = await api.Extension();
  renderExt();
  renderStatus();
}
function openSettings(toBrowser) {
  fillSettings();
  refreshExt();
  openDialog('dlg-settings');
  const pane = $('.settings');
  pane.scrollTop = toBrowser ? $('#ext-card').offsetTop : 0;
}
setInterval(refreshExtQuiet, 5000);
function refreshExtQuiet() { if (api && !document.hidden) refreshExt().catch(() => {}); }

// ---------- updates ----------
function renderUpdate() {
  const ready = upd.state === 'ready';
  $('#update-bar').hidden = !ready || updDismissed === upd.version;
  if (ready) $('#update-text').textContent = t('upReady', { v: upd.version });
  $('#update-notes').hidden = !upd.notes;
  const bad = upd.error === 'signature' || upd.error === 'mismatch';
  const text = {
    checking: t('upChecking'), downloading: t('upDownloading', { v: upd.version }), latest: t('upLatest'),
    ready: t('upReadyShort', { v: upd.version }), error: t(bad ? 'upBad' : 'upError'),
  }[upd.state] || '';
  const busy = upd.state === 'checking' || upd.state === 'downloading';
  $('#update-status').textContent = text;
  $('#update-line').hidden = !text;
  $('#update-line').className = 'upd' + (busy ? ' busy' : upd.state === 'latest' ? ' ok' : upd.state === 'error' ? ' bad' : '');
  $('#update-check').hidden = ready || busy;
  $('#update-apply2').hidden = !ready;
}
function onUpdate(info) {
  info = info || { state: 'idle' };
  const final = info.state === 'latest' || info.state === 'error' || info.state === 'ready';
  // A check asked for by hand shows "Checking…" for a moment even when GitHub answers at once, then says how it went.
  const wait = manualCheckAt && final ? 800 - (Date.now() - manualCheckAt) : 0;
  if (wait > 0) { setTimeout(() => onUpdate(info), wait); return; }
  upd = info;
  if (manualCheckAt && final) {
    manualCheckAt = 0;
    if (info.state === 'latest') toast('✓ ' + t('upLatestToast', { v: appVersion }), 4000);
    else if (info.state === 'error') toast(t(info.error === 'signature' || info.error === 'mismatch' ? 'upBad' : 'upErrorToast'), 5000);
  }
  renderUpdate();
}
async function applyUpdate() {
  $('#update-apply').disabled = true;
  try { await api.ApplyUpdate(); } catch { toast(t('upFailed')); $('#update-apply').disabled = false; }
}
$('#update-apply').onclick = applyUpdate;
$('#update-apply2').onclick = applyUpdate;
$('#update-notes').onclick = () => api.OpenReleaseNotes().catch(() => {});
$('#update-close').onclick = () => { updDismissed = upd.version; renderUpdate(); };
$('#update-check').onclick = () => {
  manualCheckAt = Date.now();
  onUpdate({ state: 'checking' });
  api.CheckForUpdates();
  setTimeout(() => { if (manualCheckAt && upd.state === 'checking') onUpdate({ state: 'error', error: 'network' }); }, 60000);
};

// ---------- boot ----------
function mockBackend() {
  // Stand-in used only when the page is opened outside the app (design preview).
  const now = Date.now();
  const mk = (id, fileName, state, total, received, extra = {}) => ({
    id, fileName, state, total, received, url: 'https://example.com/' + fileName, host: 'cdn.example.com', dir: 'C:\\Users\\you\\Downloads',
    path: '', category: 'other', speed: 0, eta: -1, conns: 0, resumable: true, createdAt: now, finishedAt: now - 36e5, ...extra,
  });
  const G = 1 << 30, M = 1 << 20;
  const all = [
    mk('1', 'ubuntu-24.04.1-desktop-amd64.iso', 'downloading', 5.8 * G, 2.1 * G, { category: 'archive', speed: 41.5 * M, eta: 92, conns: 8,
      segments: Array.from({ length: 8 }, (_, i) => [i * 0.725 * G, (i + 1) * 0.725 * G - 1, i * 0.725 * G + (0.15 + 0.12 * ((i * 7) % 5)) * 0.725 * G]) }),
    mk('2', 'Blender Open Movie 4K.mp4', 'paused', 1.4 * G, 0.62 * G, { category: 'video', segments: [[0, 1.4 * G - 1, 0.62 * G]] }),
    mk('3', 'node-v22.11.0-x64.msi', 'queued', -1, 0, { category: 'program' }),
    mk('4', 'yillik-rapor-2026.pdf', 'completed', 4.2 * M, 4.2 * M, { category: 'document' }),
    mk('5', 'podcast-episode-118.mp3', 'completed', 86 * M, 86 * M, { category: 'audio', finishedAt: now - 9e7 }),
    mk('6', 'design-assets.zip', 'failed', 320 * M, 40 * M, { category: 'archive', errorCode: 'http', errorDetail: '403' }),
    mk('7', 'wallpaper-pack.png', 'completed', 12 * M, 12 * M, { category: 'image', finishedAt: now - 4e8 }),
  ];
  const s = { downloadDir: 'C:\\Users\\you\\Downloads', categoryFolders: false, maxConnections: 8, maxActive: 3, speedLimitKB: 0, capture: true,
    captureExts: ['zip', 'exe', 'mp4', 'pdf'], captureMinKB: 0, showOnCapture: true, notify: true, autostart: false, language: 'auto', theme: 'system', autoUpdate: true };
  const noop = async () => {};
  return {
    Init: async () => ({ version: 'preview', lang: (navigator.language || 'en').startsWith('tr') ? 'tr' : 'en', settings: s,
      state: { all, live: all.filter(isActive), active: 1, speed: 41.5 * M },
      extension: { dir: 'C:\\Users\\you\\AppData\\Local\\HukukFixDM\\extension', hostFound: true, connected: false, known: false, browsers: ['chrome', 'edge'] }, defaultCaptureExts: s.captureExts }),
    Extension: async () => extInfo, SaveSettings: async (x) => x, ClipboardURL: async () => '', ChooseFolder: async () => '',
    Add: async () => 0, Pause: noop, Resume: noop, Remove: noop, PauseAll: noop, ResumeAll: noop, ClearFinished: noop, OpenFile: noop,
    ShowInFolder: noop, CopyText: noop, OpenExtensionFolder: noop, OpenExtensionsPage: noop, OpenDownloadFolder: noop,
    CheckForUpdates: noop, ApplyUpdate: noop, OpenReleaseNotes: noop,
  };
}

async function waitForBackend() {
  for (let i = 0; i < 40; i++) {
    if (window.go?.main?.App) return window.go.main.App;
    if (location.protocol === 'file:') break;
    await new Promise((r) => setTimeout(r, 50));
  }
  return null;
}

(async function boot() {
  api = (await waitForBackend()) || mockBackend();
  const init = await api.Init();
  settings = init.settings;
  sysLang = init.lang;
  defaultExts = init.defaultCaptureExts || [];
  extInfo = init.extension;
  $('#about-version').textContent = init.version;
  appVersion = init.version;
  upd = init.update || { state: 'idle' };
  applyTheme();
  onState(init.state);
  applyI18n();
  if (init.updatedFrom) toast(t('updated', { v: init.version }), 8000);
  setTimeout(() => { hintReady = true; renderHint(); }, 1500); // let an open browser's extension check in first
  if (window.runtime?.EventsOn) {
    window.runtime.EventsOn('state', onState);
    window.runtime.EventsOn('update', onUpdate);
    window.runtime.EventsOn('captured', (id) => {
      showAll();
      requestAnimationFrame(() => requestAnimationFrame(() => {
        const row = $(`.row[data-id="${CSS.escape(id)}"]`);
        if (row) { row.classList.remove('flash'); void row.offsetWidth; row.classList.add('flash'); }
      }));
    });
  }
})();
