'use strict';
const $ = (id) => document.getElementById(id);
const msg = (key, subs) => chrome.i18n.getMessage(key, subs) || key;
const send = (m) => chrome.runtime.sendMessage(m);

const ICON = {
  pause: '<path d="M5.5 3.5v9M10.5 3.5v9"/>',
  play: '<path d="M5 3.5v9l7-4.5z"/>',
  open: '<path d="M9 3h4v4M13 3 7.5 8.5M11 9.5V12a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1h2.5"/>',
  down: '<path d="M8 3v8M4.500 7.500 8 11l3.500-3.500M3.500 13.500h9"/>',
};
const UI_LANG = chrome.i18n.getUILanguage();

function bytes(n) {
  if (!(n >= 0)) return '';
  if (n < 1024) return n + ' B';
  const u = ['KB', 'MB', 'GB', 'TB'];
  let v = n / 1024, i = 0;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return v.toLocaleString(UI_LANG, { maximumFractionDigits: v >= 100 || i === 0 ? 0 : v >= 10 ? 1 : 2 }) + ' ' + u[i];
}

function iconButton(icon, title, onClick) {
  const b = document.createElement('button');
  b.className = 'icon';
  b.title = title;
  b.setAttribute('aria-label', title);
  b.innerHTML = `<svg viewBox="0 0 16 16">${ICON[icon]}</svg>`;
  b.onclick = onClick;
  return b;
}

function row(name, meta, opts = {}) {
  const el = document.createElement('div');
  el.className = 'item';
  const body = document.createElement('div');
  body.className = 'body';
  const n = document.createElement('div');
  n.className = 'name';
  n.textContent = name;
  n.title = name;
  body.appendChild(n);
  if (opts.progress != null) {
    const bar = document.createElement('div');
    bar.className = 'bar';
    const fill = document.createElement('i');
    fill.style.width = Math.max(0, Math.min(100, opts.progress)) + '%';
    bar.appendChild(fill);
    body.appendChild(bar);
  }
  const m = document.createElement('div');
  m.className = 'meta' + (opts.error ? ' err' : '');
  m.textContent = meta;
  body.appendChild(m);
  el.appendChild(body);
  if (opts.button) el.appendChild(opts.button);
  return el;
}

function downloadRow(d) {
  const pct = d.total > 0 ? d.received / d.total * 100 : null;
  switch (d.state) {
    case 'completed':
      return row(d.fileName, bytes(d.total > 0 ? d.total : d.received), { button: iconButton('open', msg('open'), () => send({ type: 'action', id: d.id, action: 'open' })) });
    case 'failed':
      return row(d.fileName, msg('failed'), { error: true, button: iconButton('play', msg('retry'), () => act(d.id, 'resume')) });
    case 'paused':
      return row(d.fileName, msg('paused'), { progress: pct ?? 0, button: iconButton('play', msg('resume'), () => act(d.id, 'resume')) });
    case 'queued':
      return row(d.fileName, msg('queued'), { progress: pct ?? 0, button: iconButton('pause', msg('pause'), () => act(d.id, 'pause')) });
    default: {
      const parts = [];
      if (pct != null) parts.push(Math.floor(pct) + '%');
      parts.push(d.total > 0 ? `${bytes(d.received)} / ${bytes(d.total)}` : bytes(d.received));
      if (d.speed > 0) parts.push(bytes(Math.round(d.speed)) + '/s');
      return row(d.fileName, parts.join(' · '), { progress: pct ?? 0, button: iconButton('pause', msg('pause'), () => act(d.id, 'pause')) });
    }
  }
}

async function act(id, action) {
  await send({ type: 'action', id, action });
  refresh();
}

let tabId = null;
let tabUrl = '';
let tabTitle = '';

async function refresh() {
  const st = await send({ type: 'state', tabId });
  if (!st) return;
  $('state').firstElementChild.className = 'dot' + (st.online ? ' on' : '');
  $('state').lastElementChild.textContent = msg(st.online ? 'connected' : 'notRunning');
  $('capture').checked = !st.paused;
  $('capture-label').title = msg('captureToggle');

  $('offline').hidden = st.online;
  $('list-box').hidden = !st.online;
  $('footer').hidden = !st.online;
  if (!st.online) {
    const noHost = /host not found|forbidden/i.test(st.error || '');
    $('offline-text').textContent = msg(noHost ? 'notInstalled' : 'offlineText');
    $('start').hidden = noHost;
    // Show the technical reason too: it is what tells a missing install from a blocked one.
    $('offline-error').textContent = st.error && st.error !== 'not_running' ? st.error : '';
  }

  const media = st.media || [];
  $('media-box').hidden = media.length === 0;
  $('media').replaceChildren(...media.map((m) => {
    const stream = m.kind === 'hls';
    const name = stream ? (tabTitle || msg('stream')) : m.name;
    const meta = stream ? [msg('stream'), m.label].join(' · ') : [bytes(m.size), m.type].filter(Boolean).join(' · ');
    return row(name, meta, {
      button: iconButton('down', msg('download'), async (e) => {
        e.currentTarget.disabled = true;
        await send({ type: 'download', item: m, title: tabTitle, pageUrl: tabUrl });
        refresh();
      }),
    });
  }));

  const items = (st.items || []).slice(0, 8);
  $('list').replaceChildren(...items.map(downloadRow));
  $('empty').hidden = items.length > 0;
  return st;
}

(async function init() {
  $('start').textContent = msg('start');
  $('open').textContent = msg('openApp');
  $('media-title').textContent = msg('mediaTitle');
  $('list-title').textContent = msg('listTitle');
  $('empty').textContent = msg('empty');

  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  if (tab) { tabId = tab.id; tabUrl = tab.url || ''; tabTitle = tab.title || ''; }

  $('capture').onchange = (e) => send({ type: 'pause-capture', paused: !e.target.checked });
  $('open').onclick = async () => { await send({ type: 'action', action: 'show' }); window.close(); };
  $('start').onclick = async () => {
    $('start').disabled = true;
    $('start').textContent = msg('starting');
    const r = await send({ type: 'start' });
    $('start').disabled = false;
    $('start').textContent = msg('start');
    refresh();
  };

  await refresh();
  setInterval(refresh, 1000);
})();
