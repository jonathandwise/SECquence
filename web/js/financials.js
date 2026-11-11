// Key financials tab: the XBRL table, and a source panel that traces any number to
// the filing it came from and highlights it there.
import { $, $$, api, state, data, on, emit, toast, esc, enc, fmt, niceDate, openUrl, viewUrl, keepJumpsInFrame, loadFrame, filingIndex, icon } from './core.js';

let root, mode = 'quarterly', fin = null, picked = null, locSeq = 0, backTo = null;

const html = `
<div class="toolbar">
  <div class="seg" id="fin-mode"><button data-mode="quarterly" class="on">Quarterly</button><button data-mode="annual">Annual</button></div>
  <span class="muted">$ millions, except per share. Select any number to see where it comes from.</span><span class="grow"></span>
  <button id="fin-xlsx" class="btn">${icon.down}Export to Excel</button><button id="fin-csv" class="btn" title="Values only">CSV</button>
</div>
<div class="fin-layout">
  <div class="fin-main" id="fin-body"></div>
  <aside class="src-panel" id="src" hidden>
    <div class="src-head"><div id="src-title"></div><button class="icon-btn" id="src-close" title="Close">${icon.x}</button></div>
    <div class="src-info" id="src-info"></div>
    <div class="src-status" id="src-status"></div>
    <iframe id="src-frame" class="frame" title="Filing" sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox"></iframe>
  </aside>
</div>`;

const table = () => (mode === 'annual' ? fin.annual : fin.quarterly);

function cellText(v, unit) {
  if (unit === 'USD/shares') { const s = Math.abs(v).toFixed(2); return v < 0 ? `(${s})` : s; }
  const m = v / 1e6;
  if (unit !== 'shares' && Math.abs(m) < 0.05) return '—';
  return unit === 'shares' ? fmt.mm(Math.abs(m)) : fmt.mm(m);
}

function render() {
  const t = table(), r = fin.runway;
  let cards = '';
  if (r) {
    const runway = r.cashFlowPositive
      ? `<div class="k">Cash runway</div><div class="big">Cash-flow positive</div><div class="s">Operating cash flow averaged above zero over the last ${r.burnBasis} quarters</div>`
      : `<div class="k">Implied cash runway</div><div class="big">${r.quarters.toFixed(1)} quarters</div><div class="s">Into ${esc(r.through)} at the current burn, before any financing</div>`;
    cards = `<div class="cards3">
      <div class="card"><div class="k">Cash and investments</div><div class="big">${fmt.big(r.cashInv)}</div><div class="s">As of ${esc(niceDate(r.asOf))}</div></div>
      <div class="card"><div class="k">Average quarterly cash burn</div><div class="big">${r.avgBurn > 0 ? fmt.big(r.avgBurn) : '—'}</div><div class="s">Operating cash flow, last ${r.burnBasis} quarters</div></div>
      <div class="card">${runway}</div></div>`;
  }
  if (!t.columns.length) {
    $('#fin-body', root).innerHTML = cards + '<div class="err">No standard financial statement data was found for this company in its XBRL filings.</div>';
    return;
  }
  const strong = new Set(['netinc', 'opex', 'cashinv', 'ocf']);
  const order = t.columns.map((_, i) => i).reverse(); // newest period first
  let group = '';
  const rows = t.rows.map((row, ri) => {
    let head = '';
    if (row.group !== group) { group = row.group; head = `<tr class="group"><td>${esc(group)}</td><td colspan="${t.columns.length}"></td></tr>`; }
    const cells = order.map((ci) => {
      const c = row.cells[ci];
      if (!c) return '<td class="na">–</td>';
      const cls = [c.v < 0 && row.unit !== 'shares' ? 'neg' : '', c.d ? 'der' : '', picked && picked.ri === ri && picked.ci === ci ? 'on' : ''].join(' ');
      return `<td class="${cls}" data-r="${ri}" data-c="${ci}" title="${c.d ? 'Calculated from year-to-date figures. Select to see how.' : 'Select to see the source filing'}"><span>${cellText(c.v, row.unit)}</span></td>`;
    }).join('');
    return `${head}<tr class="${strong.has(row.key) ? 'strong' : ''}"><td title="${esc(row.concept)}">${esc(row.label)}</td>${cells}</tr>`;
  }).join('');
  $('#fin-body', root).innerHTML = `${cards}<div class="table-card"><table class="fin">
    <thead><tr><th>${mode === 'annual' ? 'Fiscal year ended' : 'Quarter ended'}</th>${order.map((ci) => t.columns[ci]).map((c) => `<th title="${esc(c.key)}">${esc(c.label.replace(/^(Q\/E|FY) /, ''))}</th>`).join('')}</tr></thead><tbody>${rows}</tbody></table></div>
    <p class="note">From the SEC's XBRL company facts. Values in italics are calculated from year-to-date figures (Q4 is the full year less nine months), because companies report the fourth quarter and most cash-flow lines only cumulatively. Runway is cash and investments divided by average quarterly operating cash burn; it ignores financing, milestones and guidance.</p>`;
}

// ------------------------------------------------------------------ source panel
const period = (s) => (s.start ? `${niceDate(s.start)} to ${niceDate(s.end)}` : `As of ${niceDate(s.end)}`);

function closeSource() {
  picked = null; backTo = null;
  if (!root) return;
  $('#src', root).hidden = true;
  loadFrame($('#src-frame', root), 'about:blank');
  $$('.fin td.on', root).forEach((x) => x.classList.remove('on'));
}

function openSource(ri, ci) {
  const t = table(), row = t.rows[ri], col = t.columns[ci], c = row && row.cells[ci];
  if (!c) return;
  picked = { ri, ci, row, cell: c };
  $$('.fin td.on', root).forEach((x) => x.classList.remove('on'));
  $(`.fin td[data-r="${ri}"][data-c="${ci}"]`, root)?.classList.add('on');
  $('#src', root).hidden = false;
  $('#src-title', root).innerHTML = `<h3>${esc(row.label)}</h3><div class="muted" style="font-size:12px">${mode === 'annual' ? 'Fiscal year ended' : 'Quarter ended'} ${esc(niceDate(col.key))}</div>
    ${backTo ? `<button class="link" id="src-back" style="font-size:12px">← Back to ${esc(backTo.label)}</button>` : ''}`;
  $(`.fin td[data-r="${ri}"][data-c="${ci}"]`, root)?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
  const u = row.unit;
  let how = '';
  if (c.op === 'sub' && c.src.length === 2) {
    const [a, b] = c.src;
    how = `<div class="src-how">Not reported on its own, so it is calculated from two reported figures:
      <div class="formula">${fmt.full(a.val, u)} <small>${esc(a.role)}, ${esc(a.form)}</small><br><span class="op">−</span>${fmt.full(b.val, u)} <small>${esc(b.role)}, ${esc(b.form)}</small><br><span class="op">=</span><b>${fmt.full(c.v, u)}</b></div></div>`;
  } else if (c.op === 'sum') {
    how = `<div class="src-how">The sum of ${c.src.length} balance-sheet line${c.src.length > 1 ? 's' : ''}:
      <div class="formula">${c.src.map((s, i) => `${i ? '<span class="op">+</span>' : ''}${fmt.full(s.val, u)} <small>${esc(s.role)}</small>`).join('<br>')}<br><span class="op">=</span><b>${fmt.full(c.v, u)}</b></div></div>`;
  } else if (c.src.length) {
    how = `<div class="src-how">Reported in the ${esc(c.src[0].form)} filed ${esc(niceDate(c.src[0].filed))}.</div>`;
  }
  const cards = c.src.map((s, i) => `<div class="src-card" data-i="${i}">
      <div class="sc-top"><span>${c.op === 'sub' && i === 1 ? 'Less: ' : ''}${esc(c.op ? s.role : 'Reported value')}</span><span class="num">${fmt.full(s.val, u)}</span></div>
      <div class="sc-meta mono">${esc(s.concept)}</div>
      <div class="sc-meta">${esc(period(s))}, ${esc(s.form)} filed ${esc(niceDate(s.filed))}, ${esc(s.accn)}</div>
      <div class="sc-actions"><button class="btn sm primary" data-show="${i}">Show in filing</button><button class="link" data-index="${i}">Filing index${icon.ext}</button></div></div>`).join('');
  let alts = '';
  if (c.alt && c.alt.length) {
    alts = '<div class="alt-h">Also reported in</div>' + c.alt.map((a, i) => {
      const same = Math.abs(a.val - c.src[0].val) < 0.5;
      return `<div class="alt"><span class="grow">${esc(a.form)} filed ${esc(niceDate(a.filed))}</span><span class="num">${fmt.full(a.val, u)}</span>
        ${same ? '<span class="up">Same</span>' : `<span class="warn" title="The table uses the most recently filed value">Differs by ${fmt.full(a.val - c.src[0].val, u)}</span>`}<button class="link" data-alt="${i}">Show</button></div>`;
    }).join('');
  }
  $('#src-info', root).innerHTML = `<div class="big">${fmt.full(c.v, u)}</div>${how}${cards}${alts}`;
  if (c.src.length) showInFiling(c.src[0], $('.src-card[data-i="0"]', root));
}

async function showInFiling(s, card) {
  const mine = ++locSeq, st = $('#src-status', root), u = picked.row.unit;
  $$('.src-card', root).forEach((x) => x.classList.toggle('on', x === card));
  st.innerHTML = `<span class="busy">Finding this number in the ${esc(s.form)} filed ${esc(niceDate(s.filed))}</span>`;
  loadFrame($('#src-frame', root), 'about:blank');
  try {
    const r = await api(`/api/company/${state.company.cik}/locate?accn=${enc(s.accn)}&concept=${enc(s.concept)}&start=${enc(s.start || '')}&end=${enc(s.end)}&val=${s.val}`);
    if (mine !== locSeq) return;
    keepJumpsInFrame($('#src-frame', root));
    loadFrame($('#src-frame', root), viewUrl(r.url, r.hits));
    const openBtn = `<button class="link" id="src-open">Open in browser${icon.ext}</button>`;
    if (r.hits.length) {
      const scale = { 3: ' (in thousands)', 6: ' (in millions)', 9: ' (in billions)' }[r.scale] || '';
      st.innerHTML = `Highlighted in <b>${esc(r.docName)}</b>, where it is shown as <mark>${esc(r.shown)}</mark>${scale}${r.sign === '-' ? ', tagged as a negative number' : ''}.
        ${r.hits.length > 1 ? `It appears ${r.hits.length} times; the first is shown.` : ''} ${openBtn}
        ${r.valueMatch ? '' : `<br><span class="warn">The document shows ${fmt.full(r.docValue, u)}, which differs from the SEC's data (${fmt.full(s.val, u)}).</span>`}
        ${r.message ? `<br><span class="warn">${esc(r.message)}</span>` : ''}`;
    } else {
      st.innerHTML = `<span class="warn">${esc(r.message || 'Not found in the document.')}</span> ${openBtn}`;
    }
    $('#src-open', root).onclick = () => openUrl(r.url);
  } catch (e) {
    if (mine === locSeq) st.innerHTML = `<span class="warn">${esc(e.message)}</span>`;
  }
}

async function exportAs(format) {
  const c = state.company;
  try {
    const r = await api(`/api/company/${c.cik}/financials/export`, { json: { ticker: c.ticker, mode, format } });
    toast('Saved ' + r.file.split(/[\\/]/).pop());
  } catch (e) { toast(e.message, true); }
}

export default {
  id: 'financials', label: 'Financials', html,
  init(el) {
    root = el;
    $('#fin-mode', root).addEventListener('click', (e) => {
      const b = e.target.closest('[data-mode]');
      if (!b) return;
      mode = b.dataset.mode;
      $$('#fin-mode button', root).forEach((x) => x.classList.toggle('on', x === b));
      closeSource();
      if (fin) render();
    });
    $('#fin-body', root).addEventListener('click', (e) => { const td = e.target.closest('td[data-c]'); if (td) openSource(+td.dataset.r, +td.dataset.c); });
    $('#src-close', root).addEventListener('click', closeSource);
    $('#src-title', root).addEventListener('click', (e) => { if (e.target.id === 'src-back' && backTo) { const tab = backTo.tab; closeSource(); emit('goto', tab); } });
    // Another tab asks to see where the latest value of a line comes from.
    on('show-source', async ({ key, from }) => {
      const c = state.company;
      try {
        if (!fin) fin = await data('fin');
        if (state.company !== c) return;
        mode = 'quarterly';
        $$('#fin-mode button', root).forEach((x) => x.classList.toggle('on', x.dataset.mode === mode));
        const t = fin.quarterly, ri = t.rows.findIndex((r) => r.key === key);
        let ci = -1;
        if (ri >= 0) t.rows[ri].cells.forEach((cell, i) => { if (cell) ci = i; });
        if (ci < 0) return toast('The filings have no tagged figure for this line, so there is nothing to show.', true);
        picked = null;
        render();
        emit('goto', 'financials');
        backTo = from || null;
        openSource(ri, ci);
      } catch (e) { toast(e.message, true); }
    });
    $('#src-info', root).addEventListener('click', (e) => {
      const b = e.target.closest('button'), c = picked && picked.cell;
      if (!b || !c) return;
      if (b.dataset.show !== undefined) showInFiling(c.src[+b.dataset.show], b.closest('.src-card'));
      else if (b.dataset.index !== undefined) openUrl(filingIndex(state.company.cik, c.src[+b.dataset.index].accn));
      else if (b.dataset.alt !== undefined) showInFiling(c.alt[+b.dataset.alt], null);
    });
    $('#fin-xlsx', root).addEventListener('click', () => exportAs('xlsx'));
    $('#fin-csv', root).addEventListener('click', () => exportAs('csv'));
  },
  reset() { fin = null; closeSource(); if (root) $('#fin-body', root).innerHTML = ''; },
  async show() {
    if (fin) return;
    const c = state.company;
    $('#fin-body', root).innerHTML = '<div class="loading">Loading XBRL financial data</div>';
    try {
      const f = await data('fin');
      if (state.company !== c) return;
      fin = f;
      render();
    } catch (e) {
      if (state.company === c) $('#fin-body', root).innerHTML = `<div class="err">${esc(e.message)}</div>`;
    }
  },
};
