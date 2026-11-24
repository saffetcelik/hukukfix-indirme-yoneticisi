// HukukFix — the small "download" button shown over a playing video.
//
// It appears only when the extension knows something on the page that can
// really be saved (a media file or an HLS stream), so it never promises a
// download it cannot deliver. Everything lives in a closed shadow root: page
// styles cannot affect it and it does not affect the page.
(() => {
  'use strict';
  if (window.__hfdmButton) return;
  window.__hfdmButton = true;

  const msg = (key) => { try { return chrome.i18n.getMessage(key) || key; } catch { return key; } };
  const MIN_W = 200, MIN_H = 110;

  let host = null, button = null, menu = null, label = null;
  let video = null;        // the video the button currently belongs to
  let items = [];          // what can be downloaded right now
  let title = '';
  let lastAsk = 0;
  let hideTimer = 0;
  let overUI = false;
  let busy = false;

  function build() {
    host = document.createElement('div');
    host.style.cssText = 'position:fixed;left:0;top:0;width:0;height:0;z-index:2147483647;pointer-events:none;';
    const root = host.attachShadow({ mode: 'closed' });
    root.innerHTML = `
      <style>
        :host { all: initial; }
        .wrap { position: fixed; font: 600 12px/1 "Segoe UI", system-ui, sans-serif; pointer-events: auto; }
        button { all: unset; box-sizing: border-box; cursor: pointer; }
        .btn { display: flex; align-items: center; gap: 6px; height: 28px; padding: 0 11px 0 9px; border-radius: 14px;
               background: rgba(20, 22, 28, .86); color: #fff; box-shadow: 0 2px 10px rgba(0, 0, 0, .35); backdrop-filter: blur(6px);
               transition: background .12s; white-space: nowrap; }
        .btn:hover, .btn:focus-visible { background: #2f5bf0; }
        .btn svg { width: 14px; height: 14px; fill: none; stroke: currentColor; stroke-width: 2; stroke-linecap: round; stroke-linejoin: round; }
        .menu { margin-top: 6px; min-width: 150px; max-width: 280px; max-height: 240px; overflow: auto; padding: 4px; border-radius: 10px;
                background: rgba(20, 22, 28, .95); color: #fff; box-shadow: 0 8px 28px rgba(0, 0, 0, .45); font-weight: 500; }
        .menu button { display: flex; justify-content: space-between; gap: 14px; width: 100%; padding: 8px 10px; border-radius: 6px; }
        .menu button:hover, .menu button:focus-visible { background: #2f5bf0; }
        .menu span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
        .menu small { opacity: .7; font-size: 11px; flex: none; }
        [hidden] { display: none !important; }
      </style>
      <div class="wrap" hidden>
        <button class="btn" type="button"><svg viewBox="0 0 16 16"><path d="M8 2.5v8M4.5 7.500 8 11l3.500-3.500M3 13.500h10"/></svg><span></span></button>
        <div class="menu" hidden></div>
      </div>`;
    const wrap = root.querySelector('.wrap');
    button = root.querySelector('.btn');
    label = button.querySelector('span');
    menu = root.querySelector('.menu');
    host._wrap = wrap;
    wrap.addEventListener('mouseenter', () => { overUI = true; clearTimeout(hideTimer); });
    wrap.addEventListener('mouseleave', () => { overUI = false; scheduleHide(); });
    button.addEventListener('click', (e) => { e.preventDefault(); e.stopPropagation(); onClick(); });
    (document.documentElement || document.body).appendChild(host);
  }

  function bytes(n) {
    if (!(n > 0)) return '';
    const u = ['KB', 'MB', 'GB'];
    let v = n / 1024, i = 0;
    while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
    return v.toFixed(v >= 100 ? 0 : 1) + ' ' + u[i];
  }

  function place() {
    if (!video || !host) return;
    const r = video.getBoundingClientRect();
    if (r.width < MIN_W || r.height < MIN_H || r.bottom < 0 || r.top > innerHeight) return hide();
    const wrap = host._wrap;
    wrap.style.top = Math.max(8, r.top + 10) + 'px';
    wrap.style.left = Math.max(8, Math.min(r.left + 10, innerWidth - 180)) + 'px';
  }

  function hide() {
    if (!host) return;
    host._wrap.hidden = true;
    menu.hidden = true;
    video = null;
  }
  function scheduleHide() {
    clearTimeout(hideTimer);
    hideTimer = setTimeout(() => { if (!overUI && !busy) hide(); }, 900);
  }

  function setLabel(text) { label.textContent = text; }

  async function ask(v) {
    lastAsk = Date.now();
    const src = v.currentSrc || v.src || '';
    let res = null;
    try { res = await chrome.runtime.sendMessage({ type: 'page-media', src: /^https?:/i.test(src) ? src : '' }); } catch { /* extension reloaded */ }
    if (v !== video) return;
    items = (res && res.items) || [];
    title = (res && res.title) || document.title;
    if (!items.length) { host._wrap.hidden = true; return; }
    if (!busy) setLabel(msg('pageDownload'));
    host._wrap.hidden = false;
    place();
  }

  function show(v) {
    clearTimeout(hideTimer);
    if (!host) build();
    if (v !== video) {
      video = v;
      items = [];
      menu.hidden = true;
      host._wrap.hidden = true;
      ask(v);
    } else if (host._wrap.hidden && Date.now() - lastAsk > 2500) {
      ask(v); // the stream may only have been requested after the first look
    } else {
      place();
    }
  }

  async function download(item) {
    menu.hidden = true;
    busy = true;
    setLabel(msg('pageSending'));
    let res = null;
    try { res = await chrome.runtime.sendMessage({ type: 'download', item, title, pageUrl: location.href }); } catch { /* ignore */ }
    setLabel(msg(res && res.ok ? 'pageSent' : 'pageFailed'));
    setTimeout(() => { busy = false; setLabel(msg('pageDownload')); if (!overUI) scheduleHide(); }, 2200);
  }

  function onClick() {
    if (busy || !items.length) return;
    if (items.length === 1) return download(items[0]);
    if (!menu.hidden) { menu.hidden = true; return; }
    menu.replaceChildren(...items.map((it) => {
      const b = document.createElement('button');
      b.type = 'button';
      const name = document.createElement('span');
      name.textContent = it.kind === 'hls' ? it.label : it.name;
      const info = document.createElement('small');
      info.textContent = it.kind === 'hls' ? msg('pageStream') : bytes(it.size);
      b.append(name, info);
      b.addEventListener('click', (e) => { e.preventDefault(); e.stopPropagation(); download(it); });
      return b;
    }));
    menu.hidden = false;
  }

  // Players put their own layers over the <video>, so hovering is detected by
  // position rather than by the event target.
  let moveQueued = false;
  function onMove(e) {
    if (moveQueued) return;
    moveQueued = true;
    const x = e.clientX, y = e.clientY;
    setTimeout(() => {
      moveQueued = false;
      if (overUI) return;
      let found = null;
      for (const v of document.querySelectorAll('video')) {
        const r = v.getBoundingClientRect();
        if (r.width >= MIN_W && r.height >= MIN_H && x >= r.left && x <= r.right && y >= r.top && y <= r.bottom) {
          found = v;
          break;
        }
      }
      if (found) show(found); else if (video) scheduleHide();
    }, 150);
  }

  document.addEventListener('mousemove', onMove, { passive: true, capture: true });
  addEventListener('scroll', place, { passive: true, capture: true });
  addEventListener('resize', place, { passive: true });
})();
