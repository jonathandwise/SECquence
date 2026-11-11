// Filings tab: the full filing list with filters, a preview pane with each filing's
// documents, and PDF downloads (one document or many filings at once).
import { $, $$, api, state, on, emit, toast, debounce, esc, fmt, niceDate, openUrl, reveal, viewUrl, keepJumpsInFrame, loadFrame, CATS, category, describe, itemsText, tag, byAcc, icon } from './core.js';

let root, cat = 'all', year = '', text = '', current = null, docs = [], activeDoc = null, showAllDocs = false, docSeq = 0;
const selected = new Set();

const html = `
<div class="toolbar">
  <div id="f-chips" class="chips"></div><span class="grow"></span>
  <select id="f-year" class="select" aria-label="Year"></select>
  <input id="f-text" class="input" type="search" placeholder="Filter, e.g. 2.02 or S-3" style="width:210px">
</div>
<div class="split">
  <div class="list" id="f-list" tabindex="0">
    <table class="grid filings"><thead><tr><th style="width:34px;text-align:center"><input type="checkbox" id="f-all" title="Select everything shown"></th><th>Filed</th><th>Form</th><th>Period</th><th>What it is</th></tr></thead><tbody id="f-rows"></tbody></table>
    <div id="f-empty" class="empty" hidden></div>
  </div>
  <div class="divider" id="f-divider" title="Drag to resize"></div>
  <div class="preview">
    <div id="pv-empty" class="pv-empty">Choose a filing to read it here and see its exhibits.</div>
    <div id="pv" hidden style="display:flex;flex-direction:column;min-height:0;flex:1">
      <div class="pv-head"><div class="pv-title" id="pv-title"></div><div class="pv-sub" id="pv-sub"></div></div>
      <div class="docs" id="pv-docs"></div>
      <iframe id="pv-frame" class="frame" title="Document" sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox"></iframe>
      <div id="pv-none" class="no-preview" hidden></div>
    </div>
  </div>
</div>
<div class="actionbar">
  <span><b id="f-count" class="num">0</b> selected</span><button id="f-clear" class="link" hidden>Clear selection</button><span class="grow" style="flex:1"></span>
  <label class="muted">Include <select id="f-ex" class="select" style="margin-left:6px">
    <option value="none">main document only</option><option value="ex99">main document and EX-99</option><option value="all">main document and all exhibits</option></select></label>
  <button id="f-download" class="btn primary" disabled>${icon.down}Save as PDF</button>
  <button id="f-folder" class="btn">Open folder</button>
</div>`;

function visible() {
  return state.filings.filter((f) => (cat === 'all' || category(f.form) === cat) && (!year || f.filed.startsWith(year)) &&
    (!text || `${f.form} ${describe(f)} ${itemsText(f)} ${f.items} ${f.acc} ${f.period}`.toLowerCase().includes(text)));
}

function renderFilters() {
  const counts = { all: state.filings.length };
  for (const f of state.filings) counts[category(f.form)] = (counts[category(f.form)] || 0) + 1;
  $('#f-chips', root).innerHTML = CATS.filter(([k]) => k === 'all' || counts[k]).map(([k, l]) => `<button class="chip ${cat === k ? 'on' : ''}" data-cat="${k}">${l}<span class="n">${counts[k]}</span></button>`).join('');
  const years = [...new Set(state.filings.map((f) => f.filed.slice(0, 4)))].sort().reverse();
  $('#f-year', root).innerHTML = '<option value="">All years</option>' + years.map((y) => `<option ${y === year ? 'selected' : ''}>${y}</option>`).join('');
}

function renderRows() {
  const list = visible(), empty = $('#f-empty', root);
  empty.hidden = list.length > 0;
  empty.textContent = state.filings.length ? 'No filings match these filters.' : 'This company has no filings on EDGAR.';
  $('#f-rows', root).innerHTML = list.map((f) => {
    const items = itemsText(f);
    return `<tr class="click ${selected.has(f.acc) ? 'sel' : ''} ${current && current.acc === f.acc ? 'cur' : ''}" data-acc="${f.acc}">
      <td class="c"><input type="checkbox" ${selected.has(f.acc) ? 'checked' : ''} tabindex="-1"></td><td class="date">${esc(f.filed)}</td><td>${tag(f.form)}</td>
      <td class="date">${f.period && f.period !== f.filed ? esc(f.period) : ''}</td>
      <td>${esc(describe(f))}${state.newAccs.has(f.acc) ? '<span class="new-tag">New</span>' : ''}${items ? `<div class="items">${esc(items)}</div>` : ''}</td></tr>`;
  }).join('');
  const all = list.length > 0 && list.every((f) => selected.has(f.acc));
  $('#f-all', root).checked = all;
  $('#f-all', root).indeterminate = !all && list.some((f) => selected.has(f.acc));
}

function renderSelection() {
  $('#f-count', root).textContent = selected.size;
  $('#f-clear', root).hidden = selected.size === 0;
  $('#f-download', root).disabled = selected.size === 0;
}

// ------------------------------------------------------------------ preview
async function open(f, docUrl) {
  if (!f) return;
  current = f;
  showAllDocs = false;
  docs = [];
  $$('#f-rows tr.cur', root).forEach((r) => r.classList.remove('cur'));
  const tr = $(`#f-rows tr[data-acc="${f.acc}"]`, root);
  if (tr) tr.classList.add('cur');
  $('#pv-empty', root).hidden = true;
  $('#pv', root).hidden = false;
  $('#pv-title', root).innerHTML = `${tag(f.form)}<span>${esc(describe(f))}</span>`;
  $('#pv-sub', root).innerHTML = `${f.period && f.period !== f.filed ? `<span>Period ${esc(niceDate(f.period))}</span>` : ''}<span>Filed ${esc(niceDate(f.filed))}</span><span class="mono">${esc(f.acc)}</span>
    <button class="link" id="pv-index">Filing index${icon.ext}</button><button class="link" id="pv-browser">Open in browser${icon.ext}</button>`;
  $('#pv-docs', root).innerHTML = '<div class="loading" style="padding:12px">Loading documents</div>';
  const mine = ++docSeq;
  if (docUrl) show({ url: docUrl });
  else if (f.url) show({ url: f.url });
  try {
    const list = await api(`/api/company/${state.company.cik}/docs?acc=${f.acc}`);
    if (mine !== docSeq) return;
    docs = list;
    if (!f.url && !docUrl) {
      const main = docs.find((d) => d.kind === 'main') || docs.find((d) => d.kind === 'other');
      if (main) show(main);
    }
    renderDocs();
  } catch (e) {
    if (mine === docSeq) $('#pv-docs', root).innerHTML = `<div class="empty" style="padding:12px">${esc(e.message)}</div>`;
  }
}

const isMain = (d) => d.kind === 'main' || (current.primaryDoc && d.name === current.primaryDoc.split('/').pop());

function renderDocs() {
  if (!docs.length) { $('#pv-docs', root).innerHTML = '<div class="empty" style="padding:12px">No documents are listed for this filing.</div>'; return; }
  const main = docs.filter((d) => ['main', 'exhibit', 'other'].includes(d.kind)), rest = docs.length - main.length;
  $('#pv-docs', root).innerHTML = (showAllDocs ? docs : main).map((d) => {
    const type = isMain(d) ? current.form : d.type || d.kind;
    const printable = /\.(htm|html|txt|pdf)$/i.test(d.name) && d.kind !== 'full';
    return `<div class="doc ${activeDoc === d.url ? 'on' : ''}" data-i="${docs.indexOf(d)}"><span class="d-type" title="${esc(type)}">${esc(type)}</span>
      <span class="d-name" title="${esc(d.name)}">${esc(d.description && d.description.toUpperCase() !== type.toUpperCase() ? d.description : d.name)}</span>
      <span class="d-size">${fmt.size(d.size)}</span>${printable ? '<button class="btn sm" data-pdf title="Save this document as a PDF">PDF</button>' : ''}</div>`;
  }).join('') + (rest ? `<div class="docs-more"><button class="link" id="pv-more">${showAllDocs ? 'Hide' : 'Show'} ${rest} other file${rest > 1 ? 's' : ''} (images, XBRL data)</button></div>` : '');
}

function show(d) {
  activeDoc = d.url;
  $$('#pv-docs .doc', root).forEach((el) => el.classList.toggle('on', docs[+el.dataset.i]?.url === d.url));
  const frame = $('#pv-frame', root), none = $('#pv-none', root);
  const isPdf = /\.pdf$/i.test(d.url);
  frame.hidden = isPdf;
  none.hidden = !isPdf;
  if (isPdf) {
    none.innerHTML = '<span>This exhibit is already a PDF.</span><button class="btn">Open in browser</button>';
    $('button', none).onclick = () => openUrl(d.url);
  } else {
    keepJumpsInFrame(frame);
    loadFrame(frame, viewUrl(d.url));
  }
}

function item(f) {
  const c = state.company;
  return { cik: c.cik, ticker: c.ticker, acc: f.acc, form: f.form, filed: f.filed, primaryDoc: f.primaryDoc };
}

export default {
  id: 'filings', label: 'Filings', html,
  init(el) {
    root = el;
    $('#f-chips', root).addEventListener('click', (e) => {
      const b = e.target.closest('[data-cat]');
      if (!b) return;
      cat = b.dataset.cat;
      $$('.chip', $('#f-chips', root)).forEach((c) => c.classList.toggle('on', c === b));
      renderRows();
    });
    $('#f-year', root).addEventListener('change', (e) => { year = e.target.value; renderRows(); });
    $('#f-text', root).addEventListener('input', debounce((e) => { text = e.target.value.trim().toLowerCase(); renderRows(); }, 120));
    $('#f-rows', root).addEventListener('click', (e) => {
      const tr = e.target.closest('tr[data-acc]');
      if (!tr) return;
      const acc = tr.dataset.acc;
      if (!e.target.closest('td.c')) { open(byAcc(acc)); return; }
      selected.has(acc) ? selected.delete(acc) : selected.add(acc);
      tr.classList.toggle('sel', selected.has(acc));
      $('input', tr).checked = selected.has(acc);
      renderSelection();
    });
    // Arrow keys move through the list.
    $('#f-list', root).addEventListener('keydown', (e) => {
      if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
      const list = visible(), i = current ? list.findIndex((f) => f.acc === current.acc) : -1;
      const next = list[Math.max(0, Math.min(list.length - 1, i + (e.key === 'ArrowDown' ? 1 : -1)))];
      if (!next) return;
      e.preventDefault();
      open(next);
      $(`#f-rows tr[data-acc="${next.acc}"]`, root)?.scrollIntoView({ block: 'nearest' });
    });
    $('#f-all', root).addEventListener('change', (e) => {
      for (const f of visible()) e.target.checked ? selected.add(f.acc) : selected.delete(f.acc);
      renderRows();
      renderSelection();
    });
    $('#f-clear', root).addEventListener('click', () => { selected.clear(); renderRows(); renderSelection(); });
    $('#f-download', root).addEventListener('click', () => {
      const exhibits = $('#f-ex', root).value;
      emit('download', state.filings.filter((f) => selected.has(f.acc)).map((f) => ({ ...item(f), exhibits })));
    });
    $('#f-folder', root).addEventListener('click', () => reveal({ ticker: state.company?.ticker || '' }));
    $('#pv', root).addEventListener('click', (e) => {
      if (e.target.closest('#pv-index')) return openUrl(current.indexUrl);
      if (e.target.closest('#pv-browser')) return openUrl(activeDoc || current.url || current.indexUrl);
      if (e.target.closest('#pv-more')) { showAllDocs = !showAllDocs; return renderDocs(); }
      const row = e.target.closest('.doc');
      if (!row) return;
      const d = docs[+row.dataset.i];
      if (e.target.closest('[data-pdf]')) emit('download', [{ ...item(current), docUrl: d.url, docType: isMain(d) ? '' : d.type || '', docKind: isMain(d) ? 'main' : d.kind }]);
      else show(d);
    });
    // Drag the divider to resize the two panes.
    const div = $('#f-divider', root), left = $('#f-list', root);
    div.addEventListener('mousedown', (e) => {
      const x0 = e.clientX, w0 = left.getBoundingClientRect().width;
      div.classList.add('drag');
      document.body.style.userSelect = 'none';
      $('#pv-frame', root).style.pointerEvents = 'none';
      const move = (ev) => { left.style.flex = `0 0 ${Math.max(360, w0 + ev.clientX - x0)}px`; };
      const up = () => {
        div.classList.remove('drag');
        document.body.style.userSelect = '';
        $('#pv-frame', root).style.pointerEvents = '';
        window.removeEventListener('mousemove', move);
        window.removeEventListener('mouseup', up);
      };
      window.addEventListener('mousemove', move);
      window.addEventListener('mouseup', up);
    });
    on('settings', () => { $('#f-ex', root).value = state.settings.exhibits || 'ex99'; });
    // Other views open a filing (and optionally one of its documents) here.
    on('open-filing', ({ acc, url }) => {
      const f = byAcc(acc);
      if (!f) { if (url) openUrl(url); return; }
      cat = 'all'; year = ''; text = '';
      $('#f-text', root).value = '';
      emit('goto', 'filings');
      open(f, url);
      $(`#f-rows tr[data-acc="${acc}"]`, root)?.scrollIntoView({ block: 'center' });
    });
  },
  reset() {
    cat = 'all'; year = ''; text = ''; current = null; docs = []; activeDoc = null;
    selected.clear();
    if (!root) return;
    $('#f-text', root).value = '';
    $('#f-rows', root).innerHTML = '';
    $('#pv', root).hidden = true;
    $('#pv-empty', root).hidden = false;
    loadFrame($('#pv-frame', root), 'about:blank');
    renderSelection();
  },
  show() { renderFilters(); renderRows(); renderSelection(); },
};
