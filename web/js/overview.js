// Overview tab: one screen with the price, cash position, next catalysts, latest
// filings and news, valuation and insider activity. Each card loads on its own and
// its heading opens the full tab.
import { $, state, data, emit, esc, fmt, niceDate, relTime, change, openUrl, tag, describe, itemsText, valuationOf } from './core.js';
import { sparkline } from './stock.js';
import { timeline } from './catalysts.js';

let root;
const card = (id, title, tab, wide = false) => `<div class="card ${wide ? 'wide' : ''}"><div class="card-h"><h3>${title}</h3><button class="link" data-goto="${tab}">Open ${title.toLowerCase()}</button></div><div class="card-b" id="ov-${id}"><div class="muted busy">Loading</div></div></div>`;
const html = `<div class="scroll"><div class="ov">
  ${card('price', 'Stock', 'stock')}${card('cash', 'Financials', 'financials')}${card('val', 'Valuation', 'valuation')}
  ${card('cat', 'Catalysts', 'catalysts', true)}${card('own', 'Ownership', 'ownership')}
  ${card('filings', 'Filings', 'filings', true)}${card('news', 'News', 'news')}
</div></div>`;

const set = (id, body) => { $('#ov-' + id, root).innerHTML = body; };
const fail = (id) => (e) => set(id, `<span class="muted">${esc(e.message)}</span>`);

// fill runs one card's loader and ignores the answer if another company was opened meanwhile.
function fill(id, promise, draw) {
  const c = state.company;
  promise.then((v) => { if (state.company === c) draw(v); }).catch((e) => { if (state.company === c) fail(id)(e); });
}

function show() {
  const c = state.company;
  for (const id of ['price', 'cash', 'val', 'cat', 'own', 'news']) set(id, '<div class="muted busy">Loading</div>');

  set('filings', `<ul class="ov-list">${state.filings.filter((f) => !/^(3|4|5|144)/.test(f.form)).slice(0, 7).map((f) => `<li data-acc="${f.acc}"><span class="d">${esc(f.filed)}</span>${tag(f.form)}<span class="t">${esc(itemsText(f) || describe(f))}</span>${state.newAccs.has(f.acc) ? '<span class="new-tag">New</span>' : ''}</li>`).join('')}</ul>`);

  const both = Promise.all([data('quote').catch(() => null), data('fin').catch(() => null)]);
  fill('price', Promise.all([data('quote'), data('year').catch(() => null)]), ([q, y]) => {
    if (!q) return set('price', '<span class="muted">No ticker on file, so no price.</span>');
    const yr = y && y.points.length > 1 ? change(q.price - y.points[0].c, y.points[0].c, ' past year') : '';
    set('price', `<div class="big">${fmt.price(q.price)}</div><div>${change(q.price - q.prevClose, q.prevClose, ' today')}</div><div style="font-size:12px">${yr}</div><div class="spark">${y ? sparkline(y.points, 300, 64) : ''}</div>`);
  });
  fill('cash', both, ([q, fin]) => {
    if (!fin) return set('cash', '<span class="muted">No XBRL financial data for this company.</span>');
    const v = valuationOf(q, fin), r = fin.runway;
    const runway = !r ? '—' : r.cashFlowPositive ? 'Cash-flow positive' : `${r.quarters.toFixed(1)} quarters, into ${esc(r.through)}`;
    set('cash', `<dl class="kv"><dt>Cash and investments</dt><dd>${v.cash ? fmt.big(v.cash.v) : '—'}</dd><dt>Quarterly cash burn</dt><dd>${r && r.avgBurn > 0 ? fmt.big(r.avgBurn) : '—'}</dd>
      <dt>Runway</dt><dd>${runway}</dd><dt>Market cap</dt><dd>${fmt.big(v.cap)}</dd><dt>Enterprise value</dt><dd>${fmt.big(v.ev)}</dd></dl>
      <div class="muted" style="font-size:12px;margin-top:8px">${v.cash ? 'Balance sheet as of ' + esc(niceDate(v.cash.asOf)) : ''}</div>`);
  });
  fill('val', Promise.all([data('model'), data('quote').catch(() => null)]), ([m, q]) => {
    if (!m.saved) return set('val', `<p class="muted" style="line-height:1.5;margin-bottom:10px">No valuation saved yet. Start from the pipeline on ClinicalTrials.gov and the latest balance sheet.</p><button class="btn primary" data-goto="valuation">Build a valuation</button>`);
    const ps = m.result.perShare, up = q && ps ? (ps / q.price - 1) * 100 : null;
    set('val', `<div class="big">${ps ? fmt.price(ps) : fmt.big(m.result.equityValue * 1e6)}</div><div class="muted">Risk-adjusted NPV ${ps ? 'per share' : ''}</div>
      ${up !== null ? `<div class="${up >= 0 ? 'up' : 'down'}" style="margin-top:4px">${fmt.pct(up, 0)} against ${fmt.price(q.price)}</div>` : ''}
      <div class="muted" style="font-size:12px;margin-top:8px">${m.model.products.length} drug${m.model.products.length === 1 ? '' : 's'}, discounted at ${(m.result.discountRate * 100).toFixed(1)}%${m.model.updated ? ', saved ' + esc(relTime(m.model.updated)) : ''}</div>`);
  });
  fill('cat', data('catalysts'), (d) => {
    const next = timeline(d).filter((i) => !i.past).sort((a, b) => a.start.localeCompare(b.start)).slice(0, 5);
    emit('count', { tab: 'catalysts', n: timeline(d).filter((i) => !i.past).length });
    set('cat', next.length ? `<ul class="ov-list">${next.map((i) => `<li data-goto="catalysts"><span class="d" style="width:104px;color:var(--ink);font-weight:500">${esc(i.label)}</span><span class="t" title="${esc(i.title)}">${esc(i.title)}</span><span class="s">${esc(i.kind)}</span></li>`).join('')}</ul>` : '<span class="muted">No upcoming catalysts found in recent filings or on ClinicalTrials.gov.</span>');
  });
  fill('own', data('insiders'), (ins) => {
    const last = ins.txns.find((t) => !t.derivative && (t.code === 'P' || t.code === 'S'));
    set('own', `<dl class="kv"><dt>Open-market purchases, 12 months</dt><dd class="${ins.buys.count ? 'up' : ''}">${ins.buys.count ? `${ins.buys.count}, ${fmt.big(ins.buys.value)}` : 'None'}</dd>
      <dt>Open-market sales, 12 months</dt><dd class="${ins.sells.count ? 'down' : ''}">${ins.sells.count ? `${ins.sells.count}, ${fmt.big(ins.sells.value)}` : 'None'}</dd></dl>
      <div class="muted" style="font-size:12px;margin-top:8px">${last ? `Latest: ${esc(last.owner)} ${last.code === 'P' ? 'bought' : 'sold'} ${fmt.int(last.shares)} shares on ${esc(niceDate(last.date))}` : `No open-market trades in the ${ins.scanned} most recent Form 4s`}</div>`);
  });
  fill('news', data('news'), (n) => {
    emit('count', { tab: 'news', n: n.items.length });
    set('news', n.items.length ? `<ul class="ov-list">${n.items.slice(0, 6).map((it) => `<li data-url="${esc(it.url)}"><span class="t" title="${esc(it.title)}">${esc(it.title)}</span><span class="s">${esc(relTime(it.published))}</span></li>`).join('')}</ul>` : '<span class="muted">No recent articles.</span>');
  });
  void c;
}

export default {
  id: 'overview', label: 'Overview', html,
  init(el) {
    root = el;
    root.addEventListener('click', (e) => {
      const li = e.target.closest('[data-acc],[data-url],[data-goto]');
      if (!li) return;
      if (li.dataset.acc) emit('open-filing', { acc: li.dataset.acc });
      else if (li.dataset.url) openUrl(li.dataset.url);
      else emit('goto', li.dataset.goto);
    });
  },
  show,
};
