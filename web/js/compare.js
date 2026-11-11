// Compare page: the watchlist side by side on price, size, cash and runway.
import { $, api, state, emit, esc, fmt, niceDate } from './core.js';

let root, rows = null, sortKey = 'marketCap', sortDir = -1, loading = false;

const html = `<div class="co-head" style="padding-bottom:14px"><div class="co-top"><h2>Compare watchlist</h2><div class="co-actions"><button class="btn" id="cmp-refresh">Refresh</button></div></div>
  <div class="co-meta"><span>Prices from Yahoo Finance; cash, debt and shares from each company's latest 10-Q or 10-K.</span></div></div>
<div class="scroll"><div id="cmp-body"></div>
  <p class="note">Enterprise value is market cap less cash and investments, plus debt where the company tags it. Runway is cash and investments divided by average quarterly operating cash burn over the last four quarters. Select a column heading to sort, or a row to open the company.</p></div>`;

const COLS = [['ticker', 'Company', 0], ['price', 'Price', 1], ['changePct', 'Today', 1], ['pos52', '52-week range', 1], ['marketCap', 'Market cap', 1], ['cashInv', 'Cash and investments', 1], ['ev', 'Enterprise value', 1], ['cashPct', 'Cash as % of market cap', 1], ['burn', 'Quarterly burn', 1], ['runwayQ', 'Runway', 1]];

function render() {
  const body = $('#cmp-body', root);
  if (!(state.settings.watchlist || []).length) { body.innerHTML = '<div class="empty">Your watchlist is empty. Open a company and choose Watch, then compare them here.</div>'; return; }
  if (!rows) { body.innerHTML = '<div class="loading">Loading prices and balance sheets</div>'; return; }
  const list = rows.map((r) => ({ ...r, pos52: r.high52 > r.low52 ? (r.price - r.low52) / (r.high52 - r.low52) : null, cashPct: r.marketCap ? r.cashInv / r.marketCap : null,
    runwayQ: r.cashFlowPositive ? Infinity : r.runwayQ || null }));
  list.sort((a, b) => {
    const x = a[sortKey], y = b[sortKey];
    if (x == null || x === '') return 1;
    if (y == null || y === '') return -1;
    return (typeof x === 'string' ? x.localeCompare(y) : x - y) * sortDir;
  });
  const maxRun = Math.max(1, ...list.map((r) => (Number.isFinite(r.runwayQ) ? r.runwayQ : 0)));
  body.innerHTML = `<div class="table-card"><table class="grid"><thead><tr>${COLS.map(([k, l, right]) => `<th class="sort ${right ? 'r' : ''}" data-k="${k}">${l}${sortKey === k ? (sortDir > 0 ? ' ▲' : ' ▼') : ''}</th>`).join('')}</tr></thead><tbody>
    ${list.map((r) => {
      const run = r.cashFlowPositive ? 'Cash-flow positive' : r.runwayQ ? `<span class="runbar ${r.runwayQ < 4 ? 'short' : r.runwayQ < 8 ? 'mid' : ''}" style="width:${Math.max(4, (r.runwayQ / maxRun) * 70)}px"></span>${r.runwayQ.toFixed(1)} qtrs` : '—';
      return `<tr class="click" data-cik="${r.cik}" data-ticker="${esc(r.ticker)}" data-name="${esc(r.name)}">
        <td><span class="mono">${esc(r.ticker || 'CIK ' + r.cik)}</span><div class="muted" style="font-size:12px">${esc(r.name)}${r.problem ? ` (${esc(r.problem)})` : ''}</div></td>
        <td class="r">${r.hasPrice ? fmt.price(r.price) : '—'}</td><td class="r ${r.changePct > 0 ? 'up' : r.changePct < 0 ? 'down' : ''}">${r.hasPrice ? fmt.pct(r.changePct, 2) : '—'}</td>
        <td class="r">${r.pos52 == null ? '—' : `<span class="range52" title="${fmt.price(r.low52)} to ${fmt.price(r.high52)}"><i style="left:${Math.max(0, Math.min(1, r.pos52)) * 67}px"></i></span>`}</td>
        <td class="r">${r.marketCap ? fmt.big(r.marketCap) : '—'}</td><td class="r" title="${r.asOf ? 'As of ' + niceDate(r.asOf) : ''}">${r.hasFin ? fmt.big(r.cashInv) : '—'}</td>
        <td class="r">${r.marketCap ? fmt.big(r.ev) : '—'}</td><td class="r">${r.cashPct == null ? '—' : Math.round(r.cashPct * 100) + '%'}</td>
        <td class="r">${r.burn > 0 ? fmt.big(r.burn) : '—'}</td><td class="r" style="white-space:nowrap">${run}</td></tr>`;
    }).join('')}</tbody></table></div>`;
}

async function load() {
  if (loading) return;
  loading = true;
  render();
  try { rows = await api('/api/peers'); } catch (e) { rows = []; $('#cmp-body', root).innerHTML = `<div class="err">${esc(e.message)}</div>`; loading = false; return; }
  loading = false;
  if (state.page === 'compare') render();
}

export default {
  html,
  init(el) {
    root = el;
    root.addEventListener('click', (e) => {
      const th = e.target.closest('th[data-k]');
      if (th) { sortDir = sortKey === th.dataset.k ? -sortDir : th.dataset.k === 'ticker' ? 1 : -1; sortKey = th.dataset.k; return render(); }
      if (e.target.closest('#cmp-refresh')) { rows = null; return load(); }
      const tr = e.target.closest('tr[data-cik]');
      if (tr) emit('open-company', { cik: +tr.dataset.cik, ticker: tr.dataset.ticker, name: tr.dataset.name });
    });
  },
  show() { rows = null; load(); },
};
