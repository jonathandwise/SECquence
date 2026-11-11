// Ownership tab: insider transactions from Form 4 filings, and dilution (share count
// over time, money raised, stock compensation, offering documents).
import { $, state, data, emit, esc, fmt, niceDate, category, describe, itemsText, tag } from './core.js';

let root, ins = null, fin = null, onlyMarket = true;

const html = `<div class="scroll"><div class="own">
  <section><h3>Insider transactions</h3><div class="sub" id="ins-sub"></div>
    <div class="cards3" id="ins-cards" style="grid-template-columns:repeat(2,minmax(0,1fr))"></div>
    <div class="toolbar" style="padding:0 0 8px"><div class="seg" id="ins-mode"><button data-m="1" class="on">Open-market trades</button><button data-m="0">Everything reported</button></div></div>
    <div class="table-card" id="ins-table"></div>
    <p class="note">From the Form 4s officers, directors and 10% holders must file within two business days of a trade. Open-market purchases and sales are the informative ones; grants, option exercises and shares withheld for tax are routine. “Plan” marks trades under a pre-arranged Rule 10b5-1 plan.</p>
  </section>
  <section><h3>Dilution</h3><div class="sub">Shares outstanding from the cover page of each 10-Q and 10-K</div>
    <div class="share-chart" id="share-chart"></div>
    <div class="cards3" id="dil-cards" style="margin-top:12px"></div>
    <div class="table-card" id="dil-table" style="margin-bottom:14px"></div>
    <h3>Offering documents</h3><div class="sub">Registration statements, prospectus supplements and unregistered sales</div>
    <div class="table-card" id="dil-offers"></div>
  </section>
</div></div>`;

function renderInsiders() {
  if (!ins) return;
  const stat = (k, t, cls) => `<div class="card"><div class="k">${k}, last 12 months</div><div class="big ${t.count ? cls : ''}">${t.count ? fmt.big(t.value) : 'None'}</div><div class="s">${t.count ? `${t.count} trade${t.count > 1 ? 's' : ''}, ${fmt.int(t.shares)} shares` : 'No trades reported'}</div></div>`;
  $('#ins-cards', root).innerHTML = stat('Open-market purchases', ins.buys, 'up') + stat('Open-market sales', ins.sells, 'down');
  $('#ins-sub', root).textContent = `${ins.scanned} most recent of ${ins.total} Form 4 filings${ins.since ? ', back to ' + niceDate(ins.since) : ''}${ins.problems.length ? '. ' + ins.problems.join('. ') : ''}`;
  const list = ins.txns.filter((t) => !onlyMarket || (!t.derivative && (t.code === 'P' || t.code === 'S')));
  if (!list.length) { $('#ins-table', root).innerHTML = `<div class="empty">${onlyMarket ? 'No open-market purchases or sales in these filings.' : 'No transactions in these filings.'}</div>`; return; }
  $('#ins-table', root).innerHTML = `<table class="grid"><thead><tr><th>Date</th><th>Insider</th><th>Transaction</th><th class="r">Shares</th><th class="r">Price</th><th class="r">Value</th><th class="r">Held after</th></tr></thead><tbody>
    ${list.map((t) => `<tr class="click" data-acc="${t.acc}"><td class="num" style="white-space:nowrap">${esc(t.date)}</td>
      <td>${esc(t.owner)}<div class="muted" style="font-size:12px">${esc(t.role)}</div></td>
      <td><span class="${t.code === 'P' ? 'up' : t.code === 'S' ? 'down' : ''}">${esc(t.kind)}</span>${t.planned ? ' <span class="pill">Plan</span>' : ''}${t.derivative ? `<div class="muted" style="font-size:12px">${esc(t.security)}</div>` : ''}</td>
      <td class="r">${t.acquired ? '' : '−'}${fmt.int(t.shares)}</td><td class="r">${t.price ? fmt.price(t.price) : '—'}</td><td class="r">${t.value ? fmt.big(t.value) : '—'}</td><td class="r">${t.after ? fmt.int(t.after) : '—'}</td></tr>`).join('')}
    </tbody></table>`;
}

function renderDilution() {
  if (!fin) return;
  const h = fin.shareHistory || [], box = $('#share-chart', root);
  if (h.length < 2) box.innerHTML = '<div class="empty">Not enough share-count history in XBRL filings.</div>';
  else {
    const W = box.clientWidth || 520, H = 200, m = { l: 10, r: 50, t: 14, b: 24 };
    const t0 = Date.parse(h[0].date), t1 = Date.parse(h[h.length - 1].date), hi = Math.max(...h.map((p) => p.shares)) * 1.08;
    const x = (d) => m.l + ((Date.parse(d) - t0) / (t1 - t0 || 1)) * (W - m.l - m.r), y = (v) => m.t + ((hi - v) * (H - m.t - m.b)) / hi;
    // Stepped line: a share count holds until the next report.
    let line = `M${x(h[0].date).toFixed(1)} ${y(h[0].shares).toFixed(1)}`;
    for (let i = 1; i < h.length; i++) line += `H${x(h[i].date).toFixed(1)}V${y(h[i].shares).toFixed(1)}`;
    let out = '';
    for (const v of [hi / 1.08 / 2, hi / 1.08]) out += `<line x1="${m.l}" x2="${W - m.r}" y1="${y(v).toFixed(1)}" y2="${y(v).toFixed(1)}" stroke="var(--line-2)"/><text x="${W - m.r + 6}" y="${(y(v) + 4).toFixed(1)}">${fmt.count(v)}</text>`;
    for (let yr = new Date(t0).getUTCFullYear() + 1; yr <= new Date(t1).getUTCFullYear(); yr++) out += `<text x="${x(yr + '-01-01').toFixed(1)}" y="${H - 7}" text-anchor="middle">${yr}</text>`;
    out += `<path d="${line}V${H - m.b}H${x(h[0].date).toFixed(1)}Z" fill="var(--accent)" opacity=".12"/><path d="${line}" fill="none" stroke="var(--accent)" stroke-width="1.8"/>`;
    box.innerHTML = `<svg viewBox="0 0 ${W} ${H}">${out}</svg>`;
  }
  const now = h[h.length - 1];
  const ago = (years) => { // the last report at least this many years before the latest
    if (!now) return null;
    const cut = new Date(Date.parse(now.date) - years * 365.25 * 864e5).toISOString().slice(0, 10);
    return [...h].reverse().find((p) => p.date <= cut) || null;
  };
  const grow = (p) => (p ? fmt.pct((now.shares / p.shares - 1) * 100, 0) : '—');
  const stat = (k, v, s) => `<div class="card"><div class="k">${k}</div><div class="big">${v}</div><div class="s">${s}</div></div>`;
  $('#dil-cards', root).innerHTML = now ? stat('Shares outstanding', fmt.count(now.shares), 'As of ' + niceDate(now.date)) + stat('Change in 1 year', grow(ago(1)), ago(1) ? 'From ' + fmt.count(ago(1).shares) : 'No earlier report') + stat('Change in 3 years', grow(ago(3)), ago(3) ? 'From ' + fmt.count(ago(3).shares) : 'No earlier report') : '';
  const t = fin.annual, rows = ['issuance', 'sbc', 'wshares'].map((k) => t.rows.find((r) => r.key === k)).filter(Boolean);
  $('#dil-table', root).innerHTML = rows.length && t.columns.length ? `<table class="grid"><thead><tr><th>Fiscal year, $mm</th>${t.columns.map((c) => `<th class="r">${esc(c.label.replace('FY ', ''))}</th>`).join('')}</tr></thead><tbody>
    ${rows.map((r) => `<tr><td>${esc(r.key === 'wshares' ? 'Weighted average shares, mm' : r.label)}</td>${r.cells.map((c) => `<td class="r">${c ? fmt.mm(c.v / 1e6) : '–'}</td>`).join('')}</tr>`).join('')}</tbody></table>` : '<div class="empty">No stock-issuance or compensation figures tagged in annual reports.</div>';
  const offers = state.filings.filter((f) => (category(f.form) === 'offering' && !/^(EFFECT|D$)/.test(f.form)) || (/^8-K/.test(f.form) && /3\.02/.test(f.items || ''))).slice(0, 25);
  $('#dil-offers', root).innerHTML = offers.length ? `<table class="grid"><tbody>${offers.map((f) => `<tr class="click" data-acc="${f.acc}"><td class="num" style="width:96px">${esc(f.filed)}</td><td style="width:90px">${tag(f.form)}</td><td>${esc(itemsText(f) || describe(f))}</td></tr>`).join('')}</tbody></table>` : '<div class="empty">No offering documents on EDGAR.</div>';
}

export default {
  id: 'ownership', label: 'Ownership', html,
  init(el) {
    root = el;
    root.addEventListener('click', (e) => {
      const m = e.target.closest('[data-m]');
      if (m) { onlyMarket = m.dataset.m === '1'; [...m.parentNode.children].forEach((b) => b.classList.toggle('on', b === m)); return renderInsiders(); }
      const tr = e.target.closest('tr[data-acc]');
      if (tr) emit('open-filing', { acc: tr.dataset.acc });
    });
  },
  reset() { ins = null; fin = null; },
  show() {
    const c = state.company;
    if (!ins) {
      $('#ins-cards', root).innerHTML = '';
      $('#ins-sub', root).textContent = '';
      $('#ins-table', root).innerHTML = '<div class="loading">Reading recent Form 4 filings</div>';
      data('insiders').then((r) => { if (state.company === c) { ins = r; renderInsiders(); } }).catch((e) => { if (state.company === c) $('#ins-table', root).innerHTML = `<div class="err">${esc(e.message)}</div>`; });
    }
    if (!fin) {
      $('#share-chart', root).innerHTML = '<div class="loading">Loading</div>';
      $('#dil-cards', root).innerHTML = ''; $('#dil-table', root).innerHTML = ''; $('#dil-offers', root).innerHTML = '';
      data('fin').then((r) => { if (state.company === c) { fin = r; renderDilution(); } }).catch((e) => { if (state.company === c) { fin = { shareHistory: [], annual: { rows: [], columns: [] } }; renderDilution(); $('#share-chart', root).innerHTML = `<div class="empty">${esc(e.message)}</div>`; } });
    } else renderDilution();
  },
};
