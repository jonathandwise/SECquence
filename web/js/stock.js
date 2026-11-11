// Stock tab: price chart with range buttons, hover read-out, volume, and valuation stats.
import { $, $$, api, state, data, on, emit, esc, enc, fmt, niceDate, change, debounce, valuationOf } from './core.js';

const RANGES = [['1d', '1D'], ['5d', '5D'], ['1mo', '1M'], ['6mo', '6M'], ['ytd', 'YTD'], ['1y', '1Y'], ['5y', '5Y'], ['max', 'All']];
const RANGE_WORDS = { '1d': 'today', '5d': 'past 5 days', '1mo': 'past month', '6mo': 'past 6 months', ytd: 'year to date', '1y': 'past year', '5y': 'past 5 years', max: 'all time' };
let range = '1d', chart = null, geom = null, seq = 0, root;

const html = `<div class="scroll">
  <div class="stock-head">
    <div><div class="q-line"><span class="q-price" id="q-price">—</span><span class="q-chg" id="q-chg"></span></div><div class="muted" id="q-meta" style="font-size:12px;margin-top:2px"></div></div>
    <div class="seg" id="ranges">${RANGES.map(([k, l]) => `<button data-r="${k}" class="${k === range ? 'on' : ''}">${l}</button>`).join('')}</div>
  </div>
  <div class="chart-box" id="chart-box"><svg id="chart"></svg><div id="chart-tip" class="chart-tip" hidden></div><div id="chart-msg" class="chart-msg" hidden></div></div>
  <div class="stats" id="stats"></div>
  <p class="note" id="stock-note"></p>
</div>`;

// Dates are shown in the exchange's time zone, not the computer's.
function tz(d, opts) {
  try { return new Intl.DateTimeFormat('en-US', { timeZone: d.timeZone || undefined, ...opts }); } catch { return new Intl.DateTimeFormat('en-US', opts); }
}

function niceTicks(lo, hi, count) {
  const span = hi - lo || 1;
  const mag = Math.pow(10, Math.floor(Math.log10(span / count)));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => span / s <= count) || 10 * mag;
  const out = [];
  for (let v = Math.ceil(lo / step) * step; v <= hi + 1e-9; v += step) out.push(+v.toFixed(10));
  return out;
}

// linePath draws a price series into a box; used by the main chart and the overview sparkline.
export function sparkline(points, W, H) {
  if (!points || points.length < 2) return '';
  const cs = points.map((p) => p.c), lo = Math.min(...cs), hi = Math.max(...cs);
  const x = (i) => (i * W) / (points.length - 1), y = (v) => 3 + ((hi - v) * (H - 6)) / (hi - lo || 1);
  const col = cs[cs.length - 1] >= cs[0] ? 'var(--up)' : 'var(--down)';
  const line = points.map((p, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)} ${y(p.c).toFixed(1)}`).join('');
  return `<svg viewBox="0 0 ${W} ${H}" preserveAspectRatio="none"><path d="${line}L${W} ${H}L0 ${H}Z" fill="${col}" opacity=".1"/><path d="${line}" fill="none" stroke="${col}" stroke-width="1.5" vector-effect="non-scaling-stroke"/></svg>`;
}

function geometry(d) {
  const box = $('#chart-box', root);
  const W = Math.max(300, box.clientWidth), H = box.clientHeight || 400;
  const m = { l: 14, r: 62, t: 18, b: 28 }, volH = 54, plotB = H - m.b - volH - 10;
  const pts = d.points, n = pts.length, plotW = W - m.l - m.r;
  // Today's chart spans the whole 6.5-hour session, so a part-day doesn't stretch.
  const t0 = pts[0].t, t1 = Math.max(pts[n - 1].t, t0 + 6.5 * 3600 - 300);
  const x = d.range === '1d' ? (i) => m.l + ((pts[i].t - t0) / (t1 - t0)) * plotW : (i) => m.l + (n === 1 ? plotW / 2 : (i * plotW) / (n - 1));
  const base = d.range === '1d' ? d.prevClose || pts[0].c : pts[0].c;
  let lo = Math.min(...pts.map((p) => p.c)), hi = Math.max(...pts.map((p) => p.c));
  if (d.range === '1d' && d.prevClose) { lo = Math.min(lo, d.prevClose); hi = Math.max(hi, d.prevClose); }
  const pad = (hi - lo) * 0.08 || hi * 0.02 || 1;
  lo -= pad; hi += pad;
  const y = (v) => m.t + ((hi - v) * (plotB - m.t)) / (hi - lo);
  return { d, W, H, m, volH, plotB, pts, n, x, y, lo, hi, base };
}

function xTicks(g) {
  const { d, pts, n } = g;
  const unit = { '1d': { hour: 'numeric', hour12: false }, '5d': { day: 'numeric' }, '1mo': { day: 'numeric' }, '6mo': { month: 'numeric' }, ytd: { month: 'numeric' }, '1y': { month: 'numeric' } }[d.range] || { year: 'numeric' };
  const f = tz(d, unit);
  const marks = [];
  let prev = null;
  for (let i = 0; i < n; i++) {
    const k = f.format(new Date(pts[i].t * 1000));
    if (prev !== null && k !== prev) marks.push(i);
    prev = k;
  }
  const step = Math.ceil(marks.length / Math.max(2, Math.floor((g.W - 80) / 90))) || 1;
  return marks.filter((_, i) => i % step === 0);
}
function tickLabel(d, t) {
  const date = new Date(t * 1000);
  if (d.range === '1d') return tz(d, { hour: 'numeric' }).format(date);
  if (d.range === '5d' || d.range === '1mo') return tz(d, { month: 'short', day: 'numeric' }).format(date);
  if (d.range === '5y' || d.range === 'max') return tz(d, { year: 'numeric' }).format(date);
  const mo = tz(d, { month: 'short' }).format(date);
  return mo === 'Jan' ? tz(d, { year: 'numeric' }).format(date) : mo;
}
function tipDate(d, t) {
  const date = new Date(t * 1000);
  if (['1d', '5d', '1mo'].includes(d.range)) return tz(d, { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit', timeZoneName: 'short' }).format(date);
  if (d.range === '5y' || d.range === 'max') return 'Week of ' + tz(d, { month: 'short', day: 'numeric', year: 'numeric' }).format(date);
  return tz(d, { weekday: 'short', month: 'short', day: 'numeric', year: 'numeric' }).format(date);
}

function draw() {
  const d = chart, svg = $('#chart', root), msg = $('#chart-msg', root);
  renderHead();
  renderStats();
  if (!d) return;
  if (!d.points || !d.points.length) { svg.innerHTML = ''; msg.hidden = false; msg.textContent = 'No prices for this period.'; return; }
  msg.hidden = true;
  const g = (geom = geometry(d));
  const { W, H, m, volH, plotB, pts, n, x, y, base } = g;
  svg.setAttribute('viewBox', `0 0 ${W} ${H}`);
  const last = pts[n - 1].c, col = last >= base ? 'var(--up)' : 'var(--down)';
  let out = `<defs><linearGradient id="fade" x1="0" x2="0" y1="0" y2="1"><stop offset="0%" stop-color="${col}" stop-opacity=".2"/><stop offset="100%" stop-color="${col}" stop-opacity="0"/></linearGradient></defs>`;
  for (const v of niceTicks(g.lo, g.hi, 5)) {
    const yy = y(v);
    out += `<line x1="${m.l}" x2="${W - m.r}" y1="${yy.toFixed(1)}" y2="${yy.toFixed(1)}" stroke="var(--line-2)"/>`;
    if (Math.abs(yy - y(last)) > 14) out += `<text x="${W - m.r + 8}" y="${(yy + 4).toFixed(1)}">${v.toFixed(v < 10 ? 2 : v < 100 ? 1 : 0)}</text>`;
  }
  for (const i of xTicks(g)) {
    out += `<line x1="${x(i).toFixed(1)}" x2="${x(i).toFixed(1)}" y1="${m.t}" y2="${H - m.b}" stroke="var(--line-2)"/><text x="${x(i).toFixed(1)}" y="${H - 9}" text-anchor="middle">${esc(tickLabel(d, pts[i].t))}</text>`;
  }
  const maxV = Math.max(...pts.map((p) => p.v || 0));
  if (maxV > 0) {
    const bw = Math.max(1, Math.min(8, ((W - m.l - m.r) / n) * 0.7));
    let bars = '';
    for (let i = 0; i < n; i++) {
      const h = ((pts[i].v || 0) / maxV) * volH;
      if (h > 0.3) bars += `M${(x(i) - bw / 2).toFixed(1)} ${H - m.b}h${bw.toFixed(1)}v${(-h).toFixed(1)}h${(-bw).toFixed(1)}z`;
    }
    out += `<path d="${bars}" fill="var(--line)"/>`;
  }
  if (d.range === '1d' && d.prevClose) {
    const yy = y(d.prevClose).toFixed(1);
    out += `<line x1="${m.l}" x2="${W - m.r}" y1="${yy}" y2="${yy}" stroke="var(--muted)" stroke-dasharray="4 4"/><text x="${m.l + 4}" y="${+yy - 5}">Previous close ${d.prevClose.toFixed(2)}</text>`;
  }
  const line = pts.map((p, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)} ${y(p.c).toFixed(1)}`).join('');
  out += `<path d="${line}L${x(n - 1).toFixed(1)} ${plotB}L${x(0).toFixed(1)} ${plotB}Z" fill="url(#fade)"/><path d="${line}" fill="none" stroke="${col}" stroke-width="1.8" stroke-linejoin="round"/>`;
  out += `<rect x="${W - m.r + 2}" y="${(y(last) - 9).toFixed(1)}" width="${m.r - 6}" height="18" rx="3" fill="${col}"/><text x="${W - m.r + 7}" y="${(y(last) + 4).toFixed(1)}" style="fill:var(--surface);font-weight:600">${last.toFixed(2)}</text>`;
  out += `<line id="ch-x" y1="${m.t}" y2="${H - m.b}" stroke="var(--muted)" visibility="hidden"/><circle id="ch-dot" r="4" fill="${col}" stroke="var(--surface)" stroke-width="2" visibility="hidden"/>`;
  svg.innerHTML = out;
}

function renderHead() {
  const live = state.quoteNow || chart;
  if (!live) { $('#q-price', root).textContent = '—'; $('#q-chg', root).innerHTML = ''; $('#q-meta', root).textContent = ''; return; }
  $('#q-price', root).textContent = fmt.price(live.price);
  const pts = (chart && chart.points) || [];
  if (chart && chart.range !== '1d' && pts.length) $('#q-chg', root).innerHTML = change(pts[pts.length - 1].c - pts[0].c, pts[0].c, ' ' + RANGE_WORDS[chart.range]);
  else $('#q-chg', root).innerHTML = change(live.price - live.prevClose, live.prevClose, ' today');
  const when = live.marketTime ? 'As of ' + tz(live, { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit', timeZoneName: 'short' }).format(new Date(live.marketTime * 1000)) : '';
  $('#q-meta', root).innerHTML = [when, live.exchange, live.currency, 'Source: ' + (chart || live).source].filter(Boolean).map((s) => `<span style="margin-right:14px">${esc(s)}</span>`).join('');
}

function renderStats() {
  const q = state.quoteNow || chart, fin = state.finNow;
  if (!q) { $('#stats', root).innerHTML = ''; return; }
  const v = valuationOf(q, fin);
  const stat = (k, val, s = '') => `<div class="stat"><div class="k">${k}</div><div class="v">${val}</div><div class="s">${s}</div></div>`;
  $('#stats', root).innerHTML = [
    stat('Day range', q.dayLow && q.dayHigh ? `${fmt.price(q.dayLow)} to ${fmt.price(q.dayHigh)}` : '—', q.prevClose ? 'Previous close ' + fmt.price(q.prevClose) : ''),
    stat('52-week range', q.low52 && q.high52 ? `${fmt.price(q.low52)} to ${fmt.price(q.high52)}` : '—', q.high52 ? `${(((q.price - q.high52) / q.high52) * 100).toFixed(1)}% from the 52-week high` : ''),
    stat('Volume', fmt.count(q.volume), 'Shares traded today'),
    stat('Market cap', fmt.big(v.cap), v.shares ? `${fmt.count(v.shares.v)} shares at ${fmt.price(q.price)}` : fin ? 'Share count not reported' : ''),
    stat('Shares outstanding', v.shares ? fmt.count(v.shares.v) : '—', v.shares ? `Cover page, as of ${niceDate(v.shares.asOf)}` : ''),
    stat('Cash and investments', v.cash ? fmt.big(v.cash.v) : '—', v.cash ? `As of ${niceDate(v.cash.asOf)}` : ''),
    stat('Enterprise value', fmt.big(v.ev), v.ev !== null ? `Market cap less cash${v.debt && v.debt.v ? `, plus ${fmt.big(v.debt.v)} debt` : ''}` : ''),
    stat('Cash per share', v.cash && v.shares ? fmt.price(v.cash.v / v.shares.v) : '—', v.cash && v.cap ? `${((v.cash.v / v.cap) * 100).toFixed(0)}% of market cap` : ''),
  ].join('');
  $('#stock-note', root).textContent = `Prices come from ${q.source || 'Yahoo Finance'}, an unofficial source that can lag by about 15 minutes. ${(chart && chart.note) || ''} Market cap uses the share count on the cover of the latest 10-Q or 10-K. Enterprise value adds debt only where the company tags notes or loans in its filings; check the balance sheet for leases and other claims.`;
}

async function load() {
  const c = state.company, msg = $('#chart-msg', root), mine = ++seq;
  if (!c.ticker) { msg.hidden = false; msg.textContent = 'This company has no ticker on file with the SEC, so there is no price chart.'; return; }
  if (!chart) { msg.hidden = false; msg.innerHTML = '<span class="busy">Loading prices</span>'; }
  const charts = (state.data.charts ||= {});
  try {
    const d = range === '1d' ? await data('quote', true) : (charts[range] ||= await api(`/api/chart?ticker=${enc(c.ticker)}&range=${range}`));
    if (mine !== seq || state.company !== c) return;
    chart = d;
    if (range === '1d') { state.quoteNow = d; emit('quote'); }
    draw();
  } catch (e) {
    if (mine !== seq) return;
    msg.hidden = false;
    msg.textContent = e.message;
  }
  data('quote').then((q) => { if (state.company === c) { state.quoteNow = q; renderHead(); renderStats(); } }).catch(() => {});
  data('fin').then((f) => { if (state.company === c) { state.finNow = f; renderStats(); } }).catch(() => {});
}

export default {
  id: 'stock', label: 'Stock', html,
  init(el) {
    root = el;
    $('#ranges', root).addEventListener('click', (e) => {
      const b = e.target.closest('[data-r]');
      if (!b) return;
      range = b.dataset.r;
      $$('#ranges button', root).forEach((x) => x.classList.toggle('on', x === b));
      chart = null;
      load();
    });
    const svg = $('#chart', root);
    svg.addEventListener('mousemove', (e) => {
      const g = geom;
      if (!g || !$('#ch-x', root)) return;
      const rect = svg.getBoundingClientRect(), mx = ((e.clientX - rect.left) / rect.width) * g.W;
      let best = 0;
      for (let i = 1; i < g.n; i++) if (Math.abs(g.x(i) - mx) < Math.abs(g.x(best) - mx)) best = i;
      const p = g.pts[best], xx = g.x(best), yy = g.y(p.c), pct = g.base ? ((p.c - g.base) / g.base) * 100 : 0;
      for (const a of ['x1', 'x2']) $('#ch-x', root).setAttribute(a, xx);
      $('#ch-x', root).setAttribute('visibility', 'visible');
      const dot = $('#ch-dot', root);
      dot.setAttribute('cx', xx); dot.setAttribute('cy', yy); dot.setAttribute('visibility', 'visible');
      const tip = $('#chart-tip', root);
      tip.innerHTML = `<div>${esc(tipDate(g.d, p.t))}</div><b>${fmt.price(p.c)}</b> ${fmt.pct(pct, 2)}${p.v ? `<div style="opacity:.75">Volume ${fmt.count(p.v)}</div>` : ''}`;
      tip.hidden = false;
      const sx = (xx / g.W) * rect.width, sy = (yy / g.H) * rect.height, tw = tip.offsetWidth, th = tip.offsetHeight;
      tip.style.left = Math.min(rect.width - tw - 6, Math.max(6, sx + 14 + tw > rect.width ? sx - tw - 14 : sx + 14)) + 'px';
      tip.style.top = Math.min(rect.height - th - 6, Math.max(6, sy - th - 10)) + 'px';
    });
    svg.addEventListener('mouseleave', () => {
      $('#chart-tip', root).hidden = true;
      for (const id of ['#ch-x', '#ch-dot']) { const el2 = $(id, root); if (el2) el2.setAttribute('visibility', 'hidden'); }
    });
    window.addEventListener('resize', debounce(() => { if (state.tab === 'stock' && chart) draw(); }, 150));
    on('theme', () => { if (chart) draw(); });
    // Keep today's chart and the header price current.
    setInterval(() => {
      if (!state.company || state.page !== 'company' || document.hidden || !state.company.ticker) return;
      if (state.tab === 'stock' && range === '1d') load();
      else data('quote', true).then((q) => { state.quoteNow = q; emit('quote'); }).catch(() => {});
    }, 60 * 1000);
  },
  reset() { chart = null; geom = null; seq++; state.quoteNow = null; state.finNow = null; if (root) { $('#chart', root).innerHTML = ''; $('#stats', root).innerHTML = ''; } },
  show() { if (chart) draw(); load(); },
};
