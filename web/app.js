'use strict';
(() => {
  const HIST = 180; // amostras de 500 ms = 90 s
  const ROW_CAP = 400;

  /* ---------- utilidades ---------- */
  const $ = (s, r = document) => r.querySelector(s);
  const $$ = (s, r = document) => Array.from(r.querySelectorAll(s));

  function h(tag, props, ...kids) {
    const e = document.createElement(tag);
    if (props) {
      for (const k in props) {
        const v = props[k];
        if (v == null || v === false) continue;
        if (k === 'class') e.className = v;
        else if (k === 'text') e.textContent = v;
        else if (k === 'html') e.innerHTML = v;
        else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
        else e.setAttribute(k, v === true ? '' : v);
      }
    }
    for (const c of kids.flat()) {
      if (c == null || c === false) continue;
      e.append(c.nodeType ? c : document.createTextNode(String(c)));
    }
    return e;
  }

  const svg = (b) => `<svg viewBox="0 0 16 16" aria-hidden="true">${b}</svg>`;
  const ICON = {
    chev: svg('<path d="M6 3.5 10.5 8 6 12.5"/>'),
    folder: svg('<path d="M1.8 4.4c0-.6.5-1.1 1.1-1.1h3l1.5 1.6h5.7c.6 0 1.1.5 1.1 1.1v5.8c0 .6-.5 1.1-1.1 1.1H2.9c-.6 0-1.1-.5-1.1-1.1z"/>'),
    video: svg('<rect x="2" y="3" width="12" height="10" rx="1.6"/><path d="M6.7 6v4l3.3-2z"/>'),
    audio: svg('<path d="M3 6.5v3M5.8 4.5v7M8.6 2.8v10.4M11.4 5.2v5.6M14 7v2"/>'),
    image: svg('<rect x="2" y="3" width="12" height="10" rx="1.6"/><circle cx="5.8" cy="6.6" r="1.1"/><path d="m2.6 12 3.6-3.4 2.4 2.2 2-1.8 2.8 2.6"/>'),
    arch: svg('<rect x="2" y="3" width="12" height="3.2" rx="1"/><path d="M3.2 6.2v6a1 1 0 0 0 1 1h7.6a1 1 0 0 0 1-1v-6M6.6 9h2.8"/>'),
    doc: svg('<path d="M4.2 2h4.6L12 5.2v8.3c0 .3-.2.5-.5.5H4.2c-.3 0-.5-.2-.5-.5V2.5c0-.3.2-.5.5-.5z"/><path d="M8.6 2v3.4H12M6 8.4h4M6 10.8h4"/>'),
    sliders: svg('<path d="M2.5 5h6M11.5 5h2M2.5 11h2M7.5 11h6"/><circle cx="10" cy="5" r="1.5"/><circle cx="6" cy="11" r="1.5"/>'),
    search: svg('<circle cx="7" cy="7" r="4"/><path d="m10 10 3.5 3.5"/>'),
    pause: svg('<path d="M5.5 3.5v9M10.5 3.5v9"/>'),
    play: svg('<path d="M5 3.5v9l7-4.5z"/>'),
    retry: svg('<path d="M13 8a5 5 0 1 1-1.8-3.85"/><path d="M13 2.5v3h-3"/>'),
    stop: svg('<rect x="4" y="4" width="8" height="8" rx="1.5"/>'),
    close: svg('<path d="m4 4 8 8M12 4l-8 8"/>'),
    warn: svg('<path d="M8 2.5 14 13H2z"/><path d="M8 6.5v3M8 11.2v.1"/>'),
    plus: svg('<path d="M8 3v10M3 8h10"/>'),
  };
  const ico = (n, cls = '') => h('span', { class: ('ico ' + cls).trim(), html: ICON[n] || '' });
  const CB = '<svg class="on" viewBox="0 0 16 16"><path d="m3.5 8.5 3 3 6-7"/></svg><svg class="mix" viewBox="0 0 16 16"><path d="M4 8h8"/></svg>';

  const nf0 = new Intl.NumberFormat('pt-BR', { maximumFractionDigits: 0 });
  const nf1 = new Intl.NumberFormat('pt-BR', { minimumFractionDigits: 1, maximumFractionDigits: 1 });
  const NB = ' ';

  function fmtBytes(n) {
    if (!(n >= 0)) return '–';
    const u = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0, v = n;
    while (v >= 1000 && i < 4) { v /= 1000; i++; }
    return (i < 2 ? nf0 : nf1).format(v) + NB + u[i];
  }
  function speedParts(bps) {
    if (!(bps >= 1000)) return ['0', 'MB/s'];
    if (bps < 1e6) return [nf0.format(bps / 1e3), 'KB/s'];
    return [(bps >= 1e8 ? nf0 : nf1).format(bps / 1e6), 'MB/s'];
  }
  const fmtSpeed = (b) => speedParts(b).join(NB);
  function fmtETA(s) {
    if (!(s >= 0)) return '';
    if (s < 60) return `faltam ${Math.max(1, Math.round(s))} s`;
    const m = Math.round(s / 60);
    if (m < 60) return `faltam ${m} min`;
    return `faltam ${Math.floor(m / 60)} h ${String(m % 60).padStart(2, '0')} min`;
  }
  const plural = (n, a, b) => `${nf0.format(n)} ${n === 1 ? a : b}`;
  const norm = (s) => s.normalize('NFD').replace(/[̀-ͯ]/g, '').toLowerCase();

  function shortPath(p) {
    if (!p) return '';
    if (S.home && (p === S.home || p.startsWith(S.home + '/'))) p = '~' + p.slice(S.home.length);
    if (p.length > 44) p = p.slice(0, 14) + '…' + p.slice(-28);
    return p;
  }
  const baseName = (p) => p.replace(/\/+$/, '').split('/').pop() || p;

  async function api(path, body, signal) {
    const opt = body === undefined ? { signal } : {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body), signal,
    };
    const r = await fetch('/api/' + path, opt);
    if (r.status === 401 || r.status === 403) throw new Error('Sessão inválida. Feche esta janela e abra o DriveCRTV de novo.');
    return r.json();
  }

  /* ---------- estado ---------- */
  const S = {
    settings: null, home: '', version: '',
    jobs: [], speed: 0, peak: 0, keyBad: false, hist: new Array(HIST).fill(0),
    roots: [], byId: new Map(), sel: new Set(), expanded: new Set(), limit: new Map(),
    fq: '', vis: [], cur: null, rowEl: new Map(),
    scanning: false, scanCtl: null, notes: [], hasKey: false,
    probeQ: [], probing: new Set(), probeBusy: 0,
    dest: '', free: null,
    jobEl: new Map(), open: new Set(), itemTimers: new Map(),
    clipText: '',
  };

  /* ---------- tipos ---------- */
  const EXT = {
    video: 'mov mp4 m4v mxf avi mkv braw r3d webm mts m2ts wmv mpg mpeg 3gp crm insv',
    audio: 'wav mp3 aif aiff m4a flac aac ogg opus wma caf',
    image: 'jpg jpeg png heic heif tif tiff psd raw cr2 cr3 arw nef dng webp gif svg bmp orf raf rw2 exr',
    arch: 'zip rar 7z tar gz tgz bz2 xz dmg iso',
    doc: 'pdf txt doc docx xls xlsx ppt pptx key pages numbers csv md rtf srt vtt xml json html prproj drp fcpxml aep',
  };
  const EXTMAP = {};
  for (const t in EXT) for (const e of EXT[t].split(' ')) EXTMAP[e] = t;
  const EXPORT = { doc: '.docx', sheet: '.xlsx', slide: '.pptx', draw: '.png' };

  function typeOf(n) {
    if (n.kind === 'folder') return 'folder';
    if (EXPORT[n.kind]) return 'doc';
    const m = (n.mime || '').toLowerCase();
    const e = ((n.name.match(/\.([a-z0-9]{1,5})$/i) || [])[1] || '').toLowerCase();
    if (EXTMAP[e]) return EXTMAP[e];
    if (m.startsWith('video/')) return 'video';
    if (m.startsWith('audio/')) return 'audio';
    if (m.startsWith('image/')) return 'image';
    if (/zip|compressed|x-tar|x-7z|rar/.test(m)) return 'arch';
    return 'doc';
  }

  /* ---------- velocidade e gráfico ---------- */
  const NICE = [5, 10, 20, 30, 50, 75, 100, 150, 200, 300, 500, 750, 1000, 1500, 2000].map((v) => v * 1e6);
  const niceCeil = (x) => NICE.find((v) => v >= x * 1.08) || x * 1.1;

  function pushSample(v) {
    S.hist.push(v || 0);
    if (S.hist.length > HIST) S.hist.shift();
    if (v > S.peak) S.peak = v;
  }

  function renderHeader() {
    let active = 0;
    for (const j of S.jobs) if (j.state === 'active') active += j.filesActive;
    const spd = active > 0 ? S.speed : 0;
    const [n, u] = speedParts(spd);
    $('#spd-n').textContent = n;
    $('#spd-u').textContent = u;
    const live = spd >= 1000;
    $('#readout').dataset.live = live ? '1' : '0';
    const pk = S.peak > 0 ? `pico ${fmtSpeed(S.peak)}` : '';
    $('#spd-meta').textContent = active > 0
      ? [plural(active, 'arquivo ativo', 'arquivos ativos'), pk].filter(Boolean).join(' · ')
      : ['Parado', pk].filter(Boolean).join(' · ');
    document.title = live ? `${fmtSpeed(spd)} · DriveCRTV` : 'DriveCRTV';

    const W = 600, H = 52, top = 5;
    const ceil = niceCeil(Math.max(...S.hist, 1));
    const pts = S.hist.map((v, i) => `${(i * W / (HIST - 1)).toFixed(1)},${(H - 2 - (v / ceil) * (H - 2 - top)).toFixed(1)}`);
    const line = 'M' + pts.join(' L');
    $('#t-line').setAttribute('d', line);
    $('#t-area').setAttribute('d', `${line} L${W},${H} L0,${H} Z`);
    $('#t-top').textContent = fmtSpeed(ceil);
    $('#t-mid').textContent = fmtSpeed(ceil / 2);
  }

  /* ---------- análise do link ---------- */
  const linkEl = $('#link');

  function autosize() {
    linkEl.style.height = 'auto';
    linkEl.style.height = Math.min(140, Math.max(44, linkEl.scrollHeight + 2)) + 'px';
  }

  const hasDriveLink = (t) => /https?:\/\/(drive|docs|drive\.usercontent)\.google\.com\//i.test(t);

  function setLeft(state) {
    $('#empty').hidden = state !== 'empty';
    $('#scanning').hidden = state !== 'scanning';
    $('#result').hidden = state !== 'result';
    const b = $('#scan-btn');
    b.textContent = state === 'scanning' ? 'Cancelar' : 'Analisar';
    b.classList.toggle('btn--go', state !== 'scanning');
  }

  function showLinkErr(msg) {
    const e = $('#linkerr');
    e.textContent = msg || '';
    e.hidden = !msg;
  }

  async function scan(text) {
    text = (text || '').trim();
    if (S.scanning) return;
    if (!text) { linkEl.focus(); return; }
    if (!hasDriveLink(text)) { showLinkErr('Não encontrei nenhum link do Google Drive nesse texto.'); return; }
    showLinkErr('');
    S.scanning = true;
    S.scanCtl = new AbortController();
    const had = S.roots.length > 0;
    $('#scan-title').textContent = 'Lendo o link';
    $('#scan-sub').textContent = 'Isso leva alguns segundos.';
    setLeft('scanning');
    try {
      const r = await api('scan', { text }, S.scanCtl.signal);
      if (!r.ok) {
        showLinkErr(r.error || 'Não consegui ler esse link.');
        setLeft(had ? 'result' : 'empty');
      } else {
        loadRoots(r.roots || [], r.warnings || [], !!r.hasKey);
      }
    } catch (e) {
      if (e.name !== 'AbortError') showLinkErr(e.message.includes('Sessão') ? e.message : 'Não consegui falar com o DriveCRTV. Tente de novo.');
      setLeft(had ? 'result' : 'empty');
    } finally {
      S.scanning = false;
      S.scanCtl = null;
      if (!$('#scanning').hidden) setLeft(S.roots.length ? 'result' : 'empty');
    }
  }

  function onScanProgress(ev) {
    if (!S.scanning) return;
    $('#scan-title').textContent = `Lendo “${ev.current || 'pasta'}”`;
    $('#scan-sub').textContent = `${plural(ev.count || 0, 'item encontrado', 'itens encontrados')} até agora.`;
  }

  /* ---------- árvore ---------- */
  const collator = new Intl.Collator('pt-BR', { numeric: true, sensitivity: 'base' });

  function annotate(n, parent, rel, rk, root) {
    n.parent = parent;
    n.root = root || n;
    n.rk = n.resourceKey || rk || '';
    n.type = typeOf(n);
    n.path = rel.concat(n.name).join('/');
    S.byId.set(n.id + '|' + n.root.id, n);
    n.key = n.id + '|' + n.root.id;
    if (n.kind === 'folder') {
      n.children = (n.children || []).slice().sort((a, b) =>
        (a.kind === 'folder') === (b.kind === 'folder') ? collator.compare(a.name, b.name) : a.kind === 'folder' ? -1 : 1);
      const childRel = parent ? rel.concat(n.name) : [];
      n.files = [];
      for (const c of n.children) {
        annotate(c, n, childRel, n.rk, n.root);
        if (c.kind === 'folder') n.files.push(...c.files); else n.files.push(c);
      }
    } else {
      n.files = [n];
    }
  }

  function loadRoots(roots, warnings, hasKey) {
    S.roots = roots;
    S.byId.clear();
    S.sel.clear();
    S.expanded.clear();
    S.limit.clear();
    S.probeQ = [];
    S.hasKey = hasKey;
    S.fq = '';
    $('#filter').value = '';
    for (const r of roots) {
      annotate(r, null, [], '', null);
      for (const f of r.files) S.sel.add(f.key);
      if (r.kind === 'folder') S.expanded.add(r.key);
    }
    // com uma pasta só e poucas subpastas, abre o primeiro nível
    if (roots.length === 1 && roots[0].kind === 'folder') {
      const subs = roots[0].children.filter((c) => c.kind === 'folder');
      if (subs.length && subs.length <= 3 && roots[0].files.length <= 300) subs.forEach((s) => S.expanded.add(s.key));
    }
    S.notes = buildNotes(warnings);
    renderNotes();
    const types = new Set();
    for (const r of roots) for (const f of r.files) types.add(f.type);
    for (const b of $$('#pick [data-pick]')) {
      const t = b.dataset.pick;
      if (t !== 'all' && t !== 'none') b.hidden = !types.has(t);
    }
    S.cur = null;
    setLeft('result');
    applyFilter();
    computeTotals();
    renderTree();
    updateDock();
    startProbe();
    $('#tree').focus({ preventScroll: true });
  }

  function buildNotes(warnings) {
    const out = [];
    for (const w of warnings) out.push({ text: w });
    const trunc = [], warned = [];
    const walk = (n) => {
      if (n.kind !== 'folder') return;
      if (n.truncated && !n.warn) trunc.push(n);
      if (n.warn) warned.push(n);
      n.children.forEach(walk);
    };
    S.roots.forEach(walk);
    if (trunc.length && !S.hasKey) {
      const names = trunc.slice(0, 3).map((n) => `“${n.name}”`).join(', ') + (trunc.length > 3 ? ` e mais ${trunc.length - 3}` : '');
      out.push({ text: `${names} ${trunc.length === 1 ? 'tem' : 'têm'} 50 itens ou mais. Sem chave de API o Google pode mostrar só parte deles.`, action: 'Adicionar chave' });
    }
    for (const n of warned.slice(0, 4)) out.push({ text: `${n.name}: ${n.warn}` });
    if (warned.length > 4) out.push({ text: `Mais ${warned.length - 4} pastas não puderam ser abertas.` });
    return out;
  }

  function renderNotes() {
    const box = $('#notes');
    box.replaceChildren(...S.notes.map((n) => h('div', { class: 'note' }, ico('warn'), h('span', { text: n.text }),
      n.action ? h('button', { type: 'button', text: n.action, onclick: () => openSettings('key') }) : null)));
    box.hidden = S.notes.length === 0;
  }

  function applyFilter() {
    const q = S.fq;
    const walk = (n) => {
      if (n.kind !== 'folder') {
        n.hit = !q || norm(n.name).includes(q);
        n.hits = n.hit ? 1 : 0;
        return;
      }
      n.hits = 0;
      for (const c of n.children) { walk(c); n.hits += c.hits; }
      n.hit = n.hits > 0 || (!q);
    };
    S.roots.forEach(walk);
  }

  function computeTotals() {
    const walk = (n) => {
      if (n.kind !== 'folder') { n.total = n.size >= 0 ? n.size : 0; n.unknown = n.size >= 0 ? 0 : 1; return; }
      n.total = 0; n.unknown = 0;
      for (const c of n.children) { walk(c); n.total += c.total; n.unknown += c.unknown; }
    };
    S.roots.forEach(walk);
  }

  const matchingFiles = (n) => (S.fq ? n.files.filter((f) => f.hit) : n.files);

  function nodeState(n) {
    if (n.kind !== 'folder') return S.sel.has(n.key) ? 'on' : 'off';
    const fs = matchingFiles(n);
    if (!fs.length) return 'off';
    let c = 0;
    for (const f of fs) if (S.sel.has(f.key)) c++;
    return c === 0 ? 'off' : c === fs.length ? 'on' : 'mixed';
  }

  function sizeCell(n) {
    if (n.kind === 'folder') {
      const nf = S.fq ? n.hits : n.files.length;
      const t = n.total > 0 ? fmtBytes(n.total) + (n.unknown ? '+' : '') : (nf ? '…' : '');
      return { text: `${nf} arq.` + (t ? ` · ${t}` : ''), cls: '' };
    }
    if (n.size >= 0) return { text: fmtBytes(n.size), cls: '' };
    if (n.err) return { warn: n.err, cls: 'sz--warn' };
    if (EXPORT[n.kind]) return { text: `vira ${EXPORT[n.kind]}`, cls: 'sz--wait' };
    return { text: '…', cls: 'sz--wait' };
  }

  function fillSize(el, n) {
    const c = sizeCell(n);
    el.className = 'sz ' + c.cls;
    if (c.warn) {
      el.replaceChildren(ico('warn'), ' indisponível');
      el.title = c.warn;
    } else {
      el.textContent = c.text;
      el.removeAttribute('title');
    }
  }

  function isOpen(n) { return n.kind === 'folder' && (S.fq ? n.hits > 0 : S.expanded.has(n.key)); }

  function renderTree() {
    const box = $('#tree');
    const frag = document.createDocumentFragment();
    S.vis = [];
    S.rowEl.clear();
    const walk = (n, d) => {
      if (!n.hit) return;
      S.vis.push(n);
      const row = h('div', {
        class: 'row', role: 'treeitem', id: 'r' + S.vis.length, 'data-key': n.key, 'data-type': n.type,
        'data-state': nodeState(n), style: `--d:${d}`, 'aria-level': d + 1,
      },
      h('span', { class: 'tw' + (n.kind === 'folder' ? '' : ' tw--none'), html: ICON.chev, 'data-act': 'expand' }),
      h('span', { class: 'cb', html: CB, 'data-act': 'toggle' }),
      ico(n.type === 'folder' ? 'folder' : n.type, 'ic'),
      h('span', { class: 'nm', text: n.name, title: n.path || n.name }),
      h('span', { class: 'sz' }));
      fillSize(row.lastChild, n);
      if (n.kind === 'folder') {
        const open = isOpen(n);
        row.dataset.open = open ? '1' : '0';
        row.setAttribute('aria-expanded', open ? 'true' : 'false');
      }
      row.setAttribute('aria-selected', row.dataset.state === 'on' ? 'true' : 'false');
      S.rowEl.set(n.key, row);
      frag.append(row);
      if (n.kind === 'folder' && isOpen(n)) {
        const kids = n.children.filter((c) => c.hit);
        const lim = S.limit.get(n.key) || ROW_CAP;
        kids.slice(0, lim).forEach((c) => walk(c, d + 1));
        if (kids.length > lim) {
          frag.append(h('button', {
            class: 'more', type: 'button', style: `--d:${d + 1}`, text: `Mostrar mais ${nf0.format(kids.length - lim)} itens`,
            onclick: () => { S.limit.set(n.key, lim + ROW_CAP); renderTree(); },
          }));
        }
      }
    };
    S.roots.forEach((r) => walk(r, 0));
    if (!S.vis.length) frag.append(h('p', { class: 'nomatch', text: S.fq ? 'Nenhum arquivo com esse nome.' : 'Esta pasta está vazia ou não é pública.' }));
    box.replaceChildren(frag);
    if (!S.cur || !S.rowEl.has(S.cur)) S.cur = S.vis[0] ? S.vis[0].key : null;
    markCur(false);
  }

  function refreshRows() {
    for (const [key, row] of S.rowEl) {
      const n = S.byId.get(key);
      const st = nodeState(n);
      if (row.dataset.state !== st) {
        row.dataset.state = st;
        row.setAttribute('aria-selected', st === 'on' ? 'true' : 'false');
      }
      fillSize(row.lastChild, n);
    }
    updateDock();
  }

  function toggle(n) {
    const fs = n.kind === 'folder' ? matchingFiles(n) : [n];
    const on = nodeState(n) !== 'on';
    for (const f of fs) on ? S.sel.add(f.key) : S.sel.delete(f.key);
    refreshRows();
  }

  function setExpanded(n, open) {
    if (n.kind !== 'folder' || S.fq) return;
    if (open) { S.expanded.add(n.key); prioritize(n); } else S.expanded.delete(n.key);
    renderTree();
  }

  function markCur(scroll = true) {
    for (const el of $$('.row.is-cur', $('#tree'))) el.classList.remove('is-cur');
    const row = S.cur && S.rowEl.get(S.cur);
    if (!row) { $('#tree').removeAttribute('aria-activedescendant'); return; }
    row.classList.add('is-cur');
    $('#tree').setAttribute('aria-activedescendant', row.id);
    if (scroll) row.scrollIntoView({ block: 'nearest' });
  }

  $('#tree').addEventListener('click', (e) => {
    const row = e.target.closest('.row');
    if (!row) return;
    const n = S.byId.get(row.dataset.key);
    S.cur = n.key;
    markCur(false);
    const act = e.target.closest('[data-act]')?.dataset.act;
    if (act === 'toggle') toggle(n);
    else if (n.kind === 'folder') setExpanded(n, !isOpen(n));
    else toggle(n);
  });

  $('#tree').addEventListener('keydown', (e) => {
    if (!S.vis.length) return;
    let i = S.vis.findIndex((n) => n.key === S.cur);
    if (i < 0) i = 0;
    const n = S.vis[i];
    const go = (j) => { j = Math.max(0, Math.min(S.vis.length - 1, j)); S.cur = S.vis[j].key; markCur(); };
    switch (e.key) {
      case 'ArrowDown': go(i + 1); break;
      case 'ArrowUp': go(i - 1); break;
      case 'Home': go(0); break;
      case 'End': go(S.vis.length - 1); break;
      case 'PageDown': go(i + 12); break;
      case 'PageUp': go(i - 12); break;
      case ' ': toggle(n); break;
      case 'Enter':
      case 'ArrowRight':
        if (n.kind === 'folder' && !isOpen(n)) setExpanded(n, true);
        else if (e.key === 'ArrowRight') go(i + 1);
        else toggle(n);
        break;
      case 'ArrowLeft':
        if (n.kind === 'folder' && isOpen(n) && !S.fq) setExpanded(n, false);
        else if (n.parent) { S.cur = n.parent.key; markCur(); }
        break;
      default: return;
    }
    e.preventDefault();
  });

  let filterT = 0;
  $('#filter').addEventListener('input', (e) => {
    clearTimeout(filterT);
    filterT = setTimeout(() => {
      S.fq = norm(e.target.value.trim());
      applyFilter();
      renderTree();
      updateDock();
    }, 120);
  });

  $('#pick').addEventListener('click', (e) => {
    const b = e.target.closest('[data-pick]');
    if (!b) return;
    const t = b.dataset.pick;
    for (const r of S.roots) {
      for (const f of r.files) {
        if (S.fq && !f.hit) continue;
        const on = t === 'all' || f.type === t;
        on ? S.sel.add(f.key) : S.sel.delete(f.key);
      }
    }
    refreshRows();
  });

  /* ---------- tamanhos sob demanda ---------- */
  function startProbe() {
    S.probeQ = [];
    const seen = new Set();
    const add = (f) => {
      if (f.size >= 0 || EXPORT[f.kind] || seen.has(f.key)) return;
      seen.add(f.key);
      S.probeQ.push(f);
    };
    // primeiro o que está visível, depois o resto
    for (const n of S.vis) if (n.kind !== 'folder') add(n);
    for (const r of S.roots) for (const f of r.files) add(f);
    pumpProbe();
    pumpProbe();
  }

  function prioritize(folder) {
    const want = new Set(folder.children.filter((c) => c.kind !== 'folder').map((c) => c.key));
    if (!want.size) return;
    const front = [], rest = [];
    for (const f of S.probeQ) (want.has(f.key) ? front : rest).push(f);
    S.probeQ = front.concat(rest);
  }

  let refreshQueued = false;
  function scheduleRefresh() {
    if (refreshQueued) return;
    refreshQueued = true;
    requestAnimationFrame(() => {
      refreshQueued = false;
      computeTotals();
      refreshRows();
    });
  }

  async function pumpProbe() {
    if (S.probeBusy >= 2 || !S.probeQ.length) return;
    const batch = [];
    while (S.probeQ.length && batch.length < 30) {
      const f = S.probeQ.shift();
      if (f.size < 0 && !S.probing.has(f.key) && S.byId.get(f.key) === f) batch.push(f);
    }
    if (!batch.length) return;
    S.probeBusy++;
    batch.forEach((f) => S.probing.add(f.key));
    try {
      const r = await api('probe', { items: batch.map((f) => ({ id: f.id, kind: f.kind, resourceKey: f.rk })) });
      const res = (r && r.results) || {};
      for (const f of batch) {
        const x = res[f.id];
        if (!x) continue;
        if (x.size >= 0) f.size = x.size;
        if (x.md5) f.md5 = x.md5;
        if (x.err) f.err = x.err;
      }
    } catch (e) {
      /* tenta de novo na próxima vez que alguém pedir */
    } finally {
      batch.forEach((f) => S.probing.delete(f.key));
      S.probeBusy--;
      scheduleRefresh();
      pumpProbe();
    }
  }

  /* ---------- destino ---------- */
  function setDest(p) {
    if (!p) return;
    S.dest = p;
    $('#dest-text').textContent = shortPath(p);
    $('#destbtn').title = p;
    S.free = null;
    updateDock();
    api('free?path=' + encodeURIComponent(p)).then((r) => {
      if (S.dest === p && r && r.ok && r.free > 0) { S.free = r.free; updateDock(); }
    }).catch(() => {});
  }

  async function pickFolder(initial) {
    try {
      const r = await api('pick-folder', { initial: initial || S.dest || '' });
      return r && r.path ? r.path : '';
    } catch (e) {
      toast(e.message, 'bad');
      return '';
    }
  }

  function closeMenu() {
    $('#destmenu').hidden = true;
    $('#destbtn').setAttribute('aria-expanded', 'false');
  }

  function openMenu() {
    const m = $('#destmenu');
    const st = S.settings || {};
    const item = (name, path) => h('button', {
      type: 'button', role: 'menuitem', 'aria-current': path === S.dest ? 'true' : 'false',
      onclick: () => { setDest(path); closeMenu(); },
    }, h('b', { text: name }), h('small', { text: shortPath(path) }));
    const kids = [];
    const favs = st.destinations || [];
    const favPaths = new Set(favs.map((d) => d.path));
    kids.push(h('div', { class: 'menu__h', text: 'Pasta padrão' }), item(baseName(st.defaultDest || ''), st.defaultDest));
    if (favs.length) {
      kids.push(h('div', { class: 'menu__h', text: 'Favoritas' }));
      favs.forEach((d) => kids.push(item(d.name, d.path)));
    }
    const rec = (st.recentDests || []).filter((p) => p !== st.defaultDest && !favPaths.has(p));
    if (rec.length) {
      kids.push(h('div', { class: 'menu__h', text: 'Recentes' }));
      rec.slice(0, 5).forEach((p) => kids.push(item(baseName(p), p)));
    }
    kids.push(h('hr'));
    kids.push(h('button', {
      type: 'button', role: 'menuitem', onclick: async () => {
        closeMenu();
        const p = await pickFolder();
        if (p) setDest(p);
      },
    }, h('b', { text: 'Escolher outra pasta…' })));
    if (S.dest && !favPaths.has(S.dest) && S.dest !== st.defaultDest) {
      kids.push(h('button', {
        type: 'button', role: 'menuitem', onclick: async () => {
          closeMenu();
          await saveSettings({ destinations: favs.concat({ name: baseName(S.dest), path: S.dest }) });
          toast(`“${baseName(S.dest)}” adicionada às favoritas.`, 'ok');
        },
      }, h('b', { text: 'Adicionar esta pasta às favoritas' })));
    }
    m.replaceChildren(...kids);
    m.hidden = false;
    $('#destbtn').setAttribute('aria-expanded', 'true');
    m.querySelector('button')?.focus();
  }

  $('#destbtn').addEventListener('click', (e) => {
    e.stopPropagation();
    $('#destmenu').hidden ? openMenu() : closeMenu();
  });
  document.addEventListener('click', (e) => { if (!e.target.closest('.menuwrap')) closeMenu(); });
  $('#destmenu').addEventListener('keydown', (e) => {
    const items = $$('button', $('#destmenu'));
    const i = items.indexOf(document.activeElement);
    if (e.key === 'ArrowDown') { items[(i + 1) % items.length].focus(); e.preventDefault(); }
    if (e.key === 'ArrowUp') { items[(i - 1 + items.length) % items.length].focus(); e.preventDefault(); }
    if (e.key === 'Escape') { closeMenu(); $('#destbtn').focus(); }
  });

  $('#subfolder').addEventListener('change', (e) => saveSettings({ subfolder: e.target.checked }));

  /* ---------- totais e download ---------- */
  function selectedFiles() {
    const out = [];
    for (const r of S.roots) for (const f of r.files) if (S.sel.has(f.key)) out.push(f);
    return out;
  }

  function updateDock() {
    const fs = selectedFiles();
    let size = 0, unknown = 0;
    for (const f of fs) f.size >= 0 ? (size += f.size) : unknown++;
    $('#tot-size').textContent = fs.length ? fmtBytes(size) + (unknown ? '+' : '') : '0' + NB + 'B';
    const parts = [];
    let bad = false;
    if (!fs.length) parts.push('Nenhum arquivo selecionado');
    else {
      parts.push(plural(fs.length, 'arquivo selecionado', 'arquivos selecionados'));
      if (unknown) parts.push(`${nf0.format(unknown)} sem tamanho ainda`);
    }
    if (S.free != null) {
      if (fs.length && size > S.free) {
        bad = true;
        parts.push(`faltam ${fmtBytes(size - S.free)} no disco`);
      } else parts.push(`${fmtBytes(S.free)} livres`);
    }
    $('#tot-meta').textContent = parts.join(' · ');
    $('#tot').classList.toggle('is-bad', bad);
    const b = $('#dl-btn');
    b.disabled = !fs.length || !S.dest;
    b.textContent = fs.length ? `Baixar ${plural(fs.length, 'arquivo', 'arquivos')}` : 'Baixar';
  }

  function buildGroups() {
    const groups = [];
    const loose = [];
    const toItem = (f) => ({
      id: f.id, name: f.name, path: f.root.kind === 'folder' ? f.path : f.name, kind: f.kind,
      mime: f.mime || '', resourceKey: f.rk || '', md5: f.md5 || '', size: f.size >= 0 ? f.size : -1,
    });
    for (const r of S.roots) {
      const fs = r.files.filter((f) => S.sel.has(f.key));
      if (!fs.length) continue;
      if (r.kind === 'folder') groups.push({ title: r.name, subfolder: !!(S.settings && S.settings.subfolder), items: fs.map(toItem) });
      else loose.push(...fs.map(toItem));
    }
    if (loose.length) groups.push({ title: '', subfolder: false, items: loose });
    return groups;
  }

  async function startDownload() {
    const groups = buildGroups();
    if (!groups.length || !S.dest) return;
    const b = $('#dl-btn');
    b.disabled = true;
    let n = 0;
    for (const g of groups) {
      try {
        const r = await api('download', { title: g.title, dest: S.dest, subfolder: g.subfolder, items: g.items });
        if (!r.ok) { toast(r.error || 'Não consegui iniciar o download.', 'bad'); updateDock(); return; }
        n += g.items.length;
      } catch (e) {
        toast(e.message, 'bad');
        updateDock();
        return;
      }
    }
    const title = groups.length === 1 && groups[0].title ? `“${groups[0].title}”` : plural(n, 'arquivo', 'arquivos');
    toast(`Baixando ${title} em ${shortPath(S.dest)}.`, 'ok');
    resetLeft();
    if (window.matchMedia('(max-width: 900px)').matches) setView('queue');
  }

  function resetLeft() {
    S.roots = [];
    S.byId.clear();
    S.sel.clear();
    S.probeQ = [];
    S.vis = [];
    S.rowEl.clear();
    $('#tree').replaceChildren();
    linkEl.value = '';
    autosize();
    showLinkErr('');
    setLeft('empty');
    linkEl.focus();
  }

  $('#dl-btn').addEventListener('click', startDownload);

  /* ---------- fila ---------- */
  const STATE_TXT = { active: 'Baixando', queued: 'Na fila', paused: 'Pausado', failed: 'Com erro', done: 'Concluído', canceled: 'Cancelado' };
  const ITEM_TXT = {
    queued: 'Na fila', resolving: 'Preparando', downloading: 'Baixando', verifying: 'Conferindo', waiting: 'Aguardando',
    done: 'Concluído', skipped: 'Já existia', failed: 'Erro', paused: 'Pausado', canceled: 'Cancelado',
  };

  async function jobAction(id, action) {
    try {
      await api(`jobs/${id}/${action}`, {});
    } catch (e) {
      toast(e.message, 'bad');
    }
  }

  function createJobEl(id) {
    const el = h('article', { class: 'job', 'data-id': id });
    el.append(
      h('div', { class: 'job__top' }, h('h3', { class: 'job__title' }), h('span', { class: 'pill' }), h('div', { class: 'job__acts' })),
      h('div', { class: 'bar' }, h('i')),
      h('div', { class: 'job__stats' }, h('span', { class: 'fl' }), h('span', { class: 'sp' })),
      h('p', { class: 'job__err', hidden: true }),
      h('button', { class: 'job__more', type: 'button', text: 'Ver arquivos', onclick: () => toggleItems(id) }),
      h('div', { class: 'items', hidden: true }));
    return el;
  }

  function actBtn(icon, label, fn) {
    return h('button', { class: 'ibtn', type: 'button', title: label, 'aria-label': label, onclick: fn }, ico(icon));
  }

  function updateJobEl(el, j) {
    el._v = j;
    el.dataset.state = j.state;
    const t = $('.job__title', el);
    if (t.textContent !== j.title) { t.textContent = j.title; t.title = `${j.title}\n${j.dest}`; }
    $('.pill', el).textContent = STATE_TXT[j.state] || j.state;
    const akey = j.state === 'queued' ? 'active' : j.state;
    if (el._akey !== akey) {
      el._akey = akey;
      const acts = [];
      if (j.state === 'active' || j.state === 'queued') acts.push(actBtn('pause', 'Pausar', () => jobAction(j.id, 'pause')));
      if (j.state === 'paused') acts.push(actBtn('play', 'Retomar', () => jobAction(j.id, 'resume')));
      if (j.state === 'failed') acts.push(actBtn('retry', 'Tentar de novo', () => jobAction(j.id, 'resume')));
      acts.push(actBtn('folder', 'Mostrar no Finder', () => api('reveal', { path: j.dest }).catch(() => {})));
      if (j.state === 'active' || j.state === 'queued' || j.state === 'paused') {
        acts.push(actBtn('stop', 'Cancelar e apagar o parcial', () => jobAction(j.id, 'cancel')));
      } else {
        acts.push(actBtn('close', 'Remover da lista', () => jobAction(j.id, 'remove')));
      }
      $('.job__acts', el).replaceChildren(...acts);
    }
    let pct = j.total > 0 ? (j.done / j.total) * 100 : 0;
    if (j.state === 'done') pct = 100;
    if (j.total <= 0 && j.files > 0) pct = (j.filesDone / j.files) * 100;
    $('.bar i', el).style.width = Math.min(100, pct).toFixed(2) + '%';

    const sizeTxt = j.state === 'done'
      ? fmtBytes(j.total)
      : `${fmtBytes(j.done)} de ${fmtBytes(j.total)}${j.unknown ? '+' : ''}`;
    const filesTxt = j.state === 'done'
      ? plural(j.files, 'arquivo', 'arquivos')
      : `${nf0.format(j.filesDone)} de ${plural(j.files, 'arquivo', 'arquivos')}`;
    $('.fl', el).textContent = `${sizeTxt} · ${filesTxt}`;
    let sp = '';
    if (j.state === 'active') sp = [j.speed >= 1000 ? fmtSpeed(j.speed) : 'Conectando', fmtETA(j.eta)].filter(Boolean).join(' · ');
    if (j.state === 'failed' && j.filesFailed) sp = plural(j.filesFailed, 'arquivo com erro', 'arquivos com erro');
    $('.sp', el).textContent = sp;
    const err = $('.job__err', el);
    const showErr = (j.state === 'failed' || j.state === 'paused') && j.err;
    err.hidden = !showErr;
    if (showErr) err.textContent = j.err;
    $('.job__more', el).textContent = S.open.has(j.id) ? 'Ocultar arquivos' : 'Ver arquivos';
  }

  function renderQueue() {
    const list = $('#jobs');
    const seen = new Set();
    S.jobs.forEach((j, i) => {
      let el = S.jobEl.get(j.id);
      if (!el) { el = createJobEl(j.id); S.jobEl.set(j.id, el); }
      updateJobEl(el, j);
      seen.add(j.id);
      if (list.children[i] !== el) list.insertBefore(el, list.children[i] || null);
    });
    for (const [id, el] of S.jobEl) {
      if (!seen.has(id)) {
        el.remove();
        S.jobEl.delete(id);
        S.open.delete(id);
        clearInterval(S.itemTimers.get(id));
        S.itemTimers.delete(id);
      }
    }
    const running = S.jobs.filter((j) => j.state === 'active' || j.state === 'queued').length;
    $('#qempty').hidden = S.jobs.length > 0;
    $('#q-count').textContent = S.jobs.length ? nf0.format(S.jobs.length) : '';
    $('#clear-done').hidden = !S.jobs.some((j) => j.state === 'done' || j.state === 'canceled');
    $('#qhint').hidden = running === 0;
    $('#viewsw-queue').textContent = running ? `Transferências (${running})` : 'Transferências';
  }

  function toggleItems(id) {
    const el = S.jobEl.get(id);
    if (!el) return;
    const box = $('.items', el);
    if (S.open.has(id)) {
      S.open.delete(id);
      box.hidden = true;
      clearInterval(S.itemTimers.get(id));
      S.itemTimers.delete(id);
    } else {
      S.open.add(id);
      box.hidden = false;
      loadItems(id);
      S.itemTimers.set(id, setInterval(() => loadItems(id), 1000));
    }
    if (el._v) updateJobEl(el, el._v);
  }

  const ITEM_ORDER = { downloading: 0, verifying: 0, resolving: 0, waiting: 0, failed: 1, queued: 2, paused: 2 };

  async function loadItems(id) {
    const el = S.jobEl.get(id);
    if (!el || !S.open.has(id)) return;
    let r;
    try { r = await api(`jobs/${id}/items`); } catch (e) { return; }
    if (!r || !r.ok || !S.open.has(id)) return;
    const box = $('.items', el);
    const items = r.items.slice().sort((a, b) => (ITEM_ORDER[a.state] ?? 3) - (ITEM_ORDER[b.state] ?? 3));
    const shown = items.slice(0, 300);
    box._rows = box._rows || new Map();
    const keep = new Set();
    shown.forEach((it, i) => {
      let row = box._rows.get(it.id + it.rel);
      if (!row) {
        row = h('div', { class: 'it' },
          h('div', { class: 'it__top' }, h('span', { class: 'it__nm' }), h('span', { class: 'it__st' })),
          h('div', { class: 'bar', hidden: true }, h('i')),
          h('p', { class: 'it__err', hidden: true }));
        box._rows.set(it.id + it.rel, row);
      }
      keep.add(it.id + it.rel);
      row.dataset.state = it.state;
      const nm = $('.it__nm', row);
      if (nm.textContent !== it.rel) { nm.textContent = it.rel; nm.title = it.rel; }
      let st = ITEM_TXT[it.state] || it.state;
      const pct = it.size > 0 ? Math.min(100, (it.done / it.size) * 100) : 0;
      if (it.state === 'downloading') st = it.size > 0 ? `${nf0.format(pct)}% · ${fmtSpeed(it.speed)}` : fmtBytes(it.done);
      else if (it.state === 'verifying' && it.size > 0) st = `Conferindo ${nf0.format(Math.min(100, (it.ver / it.size) * 100))}%`;
      else if ((it.state === 'done' || it.state === 'skipped') && it.size >= 0) st += ' · ' + fmtBytes(it.size);
      else if (it.state === 'waiting' && it.wait) st = it.wait;
      $('.it__st', row).textContent = st;
      const bar = $('.bar', row);
      bar.hidden = it.state !== 'downloading';
      if (!bar.hidden) $('i', bar).style.width = pct.toFixed(1) + '%';
      const er = $('.it__err', row);
      er.hidden = !(it.state === 'failed' && it.err);
      if (!er.hidden) er.textContent = it.err;
      if (box.children[i] !== row) box.insertBefore(row, box.children[i] || null);
    });
    for (const [k, row] of box._rows) if (!keep.has(k)) { row.remove(); box._rows.delete(k); }
    let more = $('.it-more', box);
    if (items.length > shown.length) {
      if (!more) more = h('p', { class: 'it it-more hint' });
      more.textContent = `Mais ${nf0.format(items.length - shown.length)} arquivos não exibidos.`;
      box.append(more);
    } else if (more) more.remove();
  }

  $('#clear-done').addEventListener('click', () => api('jobs/clear', {}).catch((e) => toast(e.message, 'bad')));

  /* ---------- avisos ---------- */
  function toast(msg, kind = 'info', opts = {}) {
    const box = $('#toasts');
    const t = h('div', { class: 'toast', 'data-kind': kind }, h('span', { class: 'toast__dot' }), h('span', { class: 'toast__msg', text: msg }));
    if (opts.action) t.append(h('button', { class: 'btn btn--sm btn--go', type: 'button', text: opts.action, onclick: () => { t.remove(); opts.onAction(); } }));
    t.append(h('button', { class: 'ibtn', type: 'button', 'aria-label': 'Fechar aviso', onclick: () => t.remove() }, ico('close')));
    if (opts.id) { $$(`[data-tid="${opts.id}"]`, box).forEach((x) => x.remove()); t.dataset.tid = opts.id; }
    box.append(t);
    while (box.children.length > 4) box.firstChild.remove();
    if (!opts.sticky) setTimeout(() => t.remove(), kind === 'bad' ? 9000 : 5000);
    return t;
  }

  /* ---------- ajustes ---------- */
  async function saveSettings(patch) {
    try {
      const r = await api('settings', patch);
      if (r && r.ok) { S.settings = r.settings; syncSettingsUI(); }
      return r;
    } catch (e) {
      toast(e.message, 'bad');
      return null;
    }
  }

  const RANGES = [
    ['#s-conns', '#o-conns', 'connsPerFile', (v) => v],
    ['#s-files', '#o-files', 'parallelFiles', (v) => v],
    ['#s-max', '#o-max', 'maxConns', (v) => v],
    ['#s-chunk', '#o-chunk', 'chunkMB', (v) => v + NB + 'MB'],
  ];

  function syncSettingsUI() {
    const st = S.settings;
    if (!st) return;
    for (const [inp, out, key, f] of RANGES) {
      if (document.activeElement !== $(inp)) $(inp).value = st[key];
      $(out).textContent = f(st[key]);
    }
    $('#s-md5').checked = !!st.verifyMD5;
    $('#s-clip').checked = !!st.watchClipboard;
    $('#subfolder').checked = !!st.subfolder;
    $('#s-defdest').textContent = shortPath(st.defaultDest);
    $('#s-defdest').title = st.defaultDest;
    const favs = st.destinations || [];
    $('#favs').replaceChildren(...favs.map((d, i) => h('div', { class: 'fav' },
      h('div', null, h('b', { text: d.name }), h('small', { text: shortPath(d.path), title: d.path })),
      actBtn('close', 'Remover das favoritas', () => saveSettings({ destinations: favs.filter((_, k) => k !== i) })))));
  }

  for (const [inp, out, key, f] of RANGES) {
    $(inp).addEventListener('input', (e) => { $(out).textContent = f(+e.target.value); });
    $(inp).addEventListener('change', (e) => saveSettings({ [key]: +e.target.value }));
  }
  $('#s-md5').addEventListener('change', (e) => saveSettings({ verifyMD5: e.target.checked }));
  $('#s-clip').addEventListener('change', (e) => saveSettings({ watchClipboard: e.target.checked }));
  $('#s-defdest').addEventListener('click', async () => {
    const p = await pickFolder(S.settings && S.settings.defaultDest);
    if (p) saveSettings({ defaultDest: p });
  });
  $('#fav-add').addEventListener('click', async () => {
    const p = await pickFolder();
    if (!p) return;
    const favs = (S.settings.destinations || []).filter((d) => d.path !== p);
    saveSettings({ destinations: favs.concat({ name: baseName(p), path: p }) });
  });

  const keyEl = $('#s-key');
  function keyStatus(msg, kind) {
    const s = $('#key-status');
    s.textContent = msg || '';
    s.dataset.kind = kind || '';
  }
  keyEl.addEventListener('change', async () => {
    const v = keyEl.value.trim();
    await saveSettings({ apiKey: v });
    keyStatus(v ? 'Chave salva. Clique em Testar para conferir.' : 'Sem chave. O DriveCRTV usa o modo sem chave.', '');
  });
  $('#key-show').addEventListener('click', (e) => {
    const show = keyEl.type === 'password';
    keyEl.type = show ? 'text' : 'password';
    e.target.textContent = show ? 'Ocultar' : 'Mostrar';
  });
  $('#key-test').addEventListener('click', async () => {
    const v = keyEl.value.trim();
    if (!v) { keyStatus('Cole a chave primeiro.', 'bad'); return; }
    keyStatus('Testando…', '');
    try {
      const r = await api('test-key', { key: v });
      if (r.ok) {
        if (!S.settings || S.settings.apiKey !== v) await saveSettings({ apiKey: v });
        keyStatus('Chave funcionando. Ela já está salva e em uso.', 'ok');
      } else keyStatus(r.error || 'A chave não funcionou.', 'bad');
    } catch (e) {
      keyStatus(e.message, 'bad');
    }
  });

  $('#open-log').addEventListener('click', () => api('open-log', {}).catch(() => {}));

  let quitArmed = 0;
  $('#quit').addEventListener('click', async (e) => {
    const b = e.currentTarget;
    if (!quitArmed) {
      const running = S.jobs.some((j) => j.state === 'active' || j.state === 'queued');
      b.textContent = running ? 'Confirmar: pausar downloads e sair' : 'Confirmar saída';
      quitArmed = setTimeout(() => { quitArmed = 0; b.textContent = 'Sair do DriveCRTV'; }, 4000);
      return;
    }
    clearTimeout(quitArmed);
    quitArmed = 0;
    try { await api('quit', {}); } catch (_) { /* já saiu */ }
    showGone();
  });

  function openSettings(focus) {
    syncSettingsUI();
    keyEl.value = (S.settings && S.settings.apiKey) || '';
    keyEl.type = 'password';
    $('#key-show').textContent = 'Mostrar';
    keyStatus(S.keyBad ? 'O Google recusou esta chave. Confira se a Google Drive API está ativa no projeto.' : '', S.keyBad ? 'bad' : '');
    const d = $('#settings');
    if (!d.open) d.showModal();
    if (focus === 'key') { $('details.guide').open = true; keyEl.focus(); }
  }
  $('#open-settings').addEventListener('click', () => openSettings());
  $('#close-settings').addEventListener('click', () => $('#settings').close());
  $('#settings').addEventListener('click', (e) => { if (e.target === e.currentTarget) e.currentTarget.close(); });
  $('#keybad-fix').addEventListener('click', () => openSettings('key'));

  /* ---------- colar, arrastar, área de transferência ---------- */
  function takeText(text) {
    if (!text || !hasDriveLink(text)) return false;
    linkEl.value = text.trim();
    autosize();
    setView('new');
    scan(linkEl.value);
    return true;
  }

  linkEl.addEventListener('input', autosize);
  linkEl.addEventListener('paste', () => {
    setTimeout(() => { autosize(); if (hasDriveLink(linkEl.value) && !S.scanning) scan(linkEl.value); }, 0);
  });
  linkEl.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); scan(linkEl.value); }
  });
  $('#linkform').addEventListener('submit', (e) => {
    e.preventDefault();
    if (S.scanning) { S.scanCtl && S.scanCtl.abort(); return; }
    scan(linkEl.value);
  });

  document.addEventListener('paste', (e) => {
    const tag = (e.target.tagName || '').toLowerCase();
    if (tag === 'input' || tag === 'textarea' || $('#settings').open) return;
    const t = e.clipboardData && e.clipboardData.getData('text');
    if (takeText(t)) e.preventDefault();
  });
  document.addEventListener('dragover', (e) => e.preventDefault());
  document.addEventListener('drop', (e) => {
    e.preventDefault();
    const dt = e.dataTransfer;
    takeText(dt && (dt.getData('text/uri-list') || dt.getData('text/plain')));
  });

  function onClipboard(ev) {
    if (!ev.text || ev.text === S.clipText || S.scanning) return;
    S.clipText = ev.text;
    if (linkEl.value.trim() === ev.text.trim()) return;
    const n = ev.count || 1;
    toast(n > 1 ? `Você copiou ${n} links do Drive.` : 'Você copiou um link do Drive.', 'info', {
      id: 'clip', sticky: true, action: 'Analisar', onAction: () => takeText(ev.text),
    });
  }

  /* ---------- painéis em janela estreita ---------- */
  function setView(v) {
    document.body.dataset.view = v;
    for (const b of $$('#viewsw button')) b.setAttribute('aria-pressed', b.dataset.view === v ? 'true' : 'false');
  }
  $('#viewsw').addEventListener('click', (e) => { const b = e.target.closest('[data-view]'); if (b) setView(b.dataset.view); });

  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && !$('#destmenu').hidden) closeMenu();
  });

  /* ---------- eventos do servidor ---------- */
  function showGone() {
    $('#gone').hidden = false;
    try { window.close(); } catch (_) { /* janela comum */ }
  }

  function onState(ev) {
    S.jobs = ev.jobs || [];
    S.speed = ev.speed || 0;
    pushSample(S.speed);
    if (S.keyBad !== !!ev.keyBad) {
      S.keyBad = !!ev.keyBad;
      $('#keybad').hidden = !S.keyBad;
    }
    renderHeader();
    renderQueue();
  }

  let es = null, failures = 0;
  function connect() {
    es = new EventSource('/api/events');
    es.onopen = () => {
      if (failures > 0) refreshState();
      failures = 0;
      $('#offline').hidden = true;
    };
    es.onmessage = (m) => {
      let ev;
      try { ev = JSON.parse(m.data); } catch (_) { return; }
      switch (ev.type) {
        case 'state': onState(ev); break;
        case 'scan': onScanProgress(ev); break;
        case 'clipboard': onClipboard(ev); break;
        case 'jobdone':
          if (ev.state === 'done') toast(`Concluído: ${ev.title}`, 'ok');
          else toast(`Terminou com erros: ${ev.title}. Abra “Ver arquivos” para detalhes.`, 'bad');
          break;
      }
    };
    es.onerror = () => {
      failures++;
      if (failures >= 2) $('#offline').hidden = false;
      if (failures >= 8 && es.readyState === EventSource.CLOSED) { es.close(); showGone(); }
    };
  }

  async function refreshState() {
    const r = await api('state');
    S.settings = r.settings;
    S.home = r.home || '';
    S.version = r.version || '';
    $('#ver').textContent = S.version ? `Versão ${S.version}` : '';
    syncSettingsUI();
    if (!S.dest) {
      const st = r.settings || {};
      setDest((st.recentDests && st.recentDests[0]) || st.defaultDest);
    }
    onState(r);
    return r;
  }

  /* ---------- início ---------- */
  for (const el of $$('[data-icon]')) el.innerHTML = ICON[el.dataset.icon] || '';
  setLeft('empty');
  renderHeader();
  refreshState().then(connect).catch((e) => {
    showLinkErr(e.message || 'Não consegui iniciar. Feche a janela e abra o DriveCRTV de novo.');
    connect();
  });
  linkEl.focus();

  // ganchos para testes automatizados
  window.__dj = { S, scan, toast };
})();
