// Shared state, server calls and formatting used by every view.

export const T = new URLSearchParams(location.search).get('t') || '';
export const $ = (s, el = document) => el.querySelector(s);
export const $$ = (s, el = document) => [...el.querySelectorAll(s)];
export const enc = encodeURIComponent;
export const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

export const state = {
  settings: null,
  company: null, // { cik, ticker, name, ... } once its filings have loaded
  filings: [],
  newAccs: new Set(),
  watched: false,
  tab: 'overview',
  page: 'welcome', // welcome | company | compare
  data: {}, // per-company results, keyed by loader name
  watchStatus: {},
  job: null,
};

export async function api(path, opts = {}) {
  const init = { method: opts.method || 'GET', headers: { 'X-App-Token': T } };
  if (opts.json !== undefined) {
    init.method = opts.method || 'POST';
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(opts.json);
  }
  let r;
  try {
    r = await fetch(path, init);
  } catch {
    throw new Error('SECquence stopped responding. Close this window and start it again.');
  }
  const body = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(body.error || r.statusText);
  return body;
}

// Per-company data, fetched once and shared between views. A failed load is
// forgotten so the next view to ask tries again.
const loaders = {
  fin: (c) => api(`/api/company/${c.cik}/financials`),
  quote: (c) => (c.ticker ? api(`/api/chart?ticker=${enc(c.ticker)}&range=1d`) : Promise.resolve(null)),
  year: (c) => (c.ticker ? api(`/api/chart?ticker=${enc(c.ticker)}&range=1y`) : Promise.resolve(null)),
  news: (c) => api(`/api/news?ticker=${enc(c.ticker || '')}&name=${enc(c.name)}`),
  catalysts: (c) => api(`/api/company/${c.cik}/catalysts?name=${enc(c.name)}`),
  insiders: (c) => api(`/api/company/${c.cik}/insiders`),
  model: (c) => api(`/api/company/${c.cik}/model?name=${enc(c.name)}`),
};
export function data(key, fresh = false) {
  const c = state.company;
  if (!c) return Promise.reject(new Error('No company is open.'));
  if (fresh) delete state.data[key];
  if (!state.data[key]) {
    const store = state.data;
    store[key] = loaders[key](c).catch((e) => { delete store[key]; throw e; });
  }
  return state.data[key];
}

// A tiny event bus so views can react to each other without importing each other.
const listeners = {};
export const on = (name, fn) => (listeners[name] ||= []).push(fn);
export const emit = (name, detail) => (listeners[name] || []).forEach((fn) => fn(detail));

let toastTimer;
export function toast(msg, bad = false, ms = 4200) {
  const t = $('#toast');
  t.textContent = msg;
  t.className = 'toast' + (bad ? ' bad' : '');
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (t.hidden = true), ms);
}

export function debounce(fn, ms) {
  let h;
  return (...a) => { clearTimeout(h); h = setTimeout(() => fn(...a), ms); };
}

export async function openUrl(url) {
  try { await api('/api/open-url', { json: { url } }); } catch (e) { toast(e.message, true); }
}
export async function reveal(body) {
  try { await api('/api/reveal', { json: body }); } catch (e) { toast(e.message, true); }
}
export const viewUrl = (url, hits) => `/view?t=${T}&url=${enc(url)}${hits && hits.length ? '&hit=' + hits.join(',') + '#secq-hit' : ''}`;

// Links inside a filing that point at another place in the same filing (its table of
// contents) scroll the viewer; without this they would follow the page's sec.gov base
// address into a new window. Call once per viewer frame.
export function keepJumpsInFrame(frame) {
  if (frame.dataset.jumps) return;
  frame.dataset.jumps = '1';
  frame.addEventListener('load', () => {
    let doc;
    try { doc = frame.contentDocument; } catch { return; }
    if (!doc || !doc.documentElement) return;
    let name = '';
    try { name = (new URL(frame.contentWindow.location.href).searchParams.get('url') || '').split('/').pop().toLowerCase(); } catch { /* blank page */ }
    doc.addEventListener('click', (e) => {
      const a = e.target.closest && e.target.closest('a[href]');
      if (!a) return;
      const raw = a.getAttribute('href') || '', i = raw.indexOf('#');
      if (i < 0) return;
      const before = raw.slice(0, i).replace(/^\.\//, '').toLowerCase(), self = frame.contentWindow.location.href.split('#')[0];
      if (before && before !== name && raw.slice(0, i) !== self) return;
      e.preventDefault();
      let id = raw.slice(i + 1);
      try { id = decodeURIComponent(id); } catch { /* keep as written */ }
      const el = id ? doc.getElementById(id) || doc.getElementsByName(id)[0] : doc.documentElement;
      if (el) el.scrollIntoView({ block: 'start' });
    }, true);
  });
}
// loadFrame points a viewer frame at a page without adding a step to the window's
// history. The window may only close itself (Save and exit) while it has no history.
export function loadFrame(frame, url) {
  try { frame.contentWindow.location.replace(url); } catch { frame.src = url; }
}
export const filingIndex = (cik, acc) => `https://www.sec.gov/Archives/edgar/data/${cik}/${acc.replace(/-/g, '')}/${acc}-index.htm`;

// ------------------------------------------------------------------ formatting
export const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
export function niceDate(d) {
  if (!d || d.length < 10) return d || '';
  return `${MONTHS[+d.slice(5, 7) - 1]} ${+d.slice(8, 10)}, ${d.slice(0, 4)}`;
}
export function relTime(iso) {
  if (!iso) return '';
  const t = new Date(iso).getTime();
  const s = (Date.now() - t) / 1000;
  if (s < 60) return 'just now';
  if (s < 3600) return Math.floor(s / 60) + 'm ago';
  if (s < 86400) return Math.floor(s / 3600) + 'h ago';
  if (s < 7 * 86400) return Math.floor(s / 86400) + 'd ago';
  return niceDate(new Date(t).toISOString().slice(0, 10));
}
const n = (v, min, max = min) => v.toLocaleString('en-US', { minimumFractionDigits: min, maximumFractionDigits: max });
const missing = (v) => v === null || v === undefined || Number.isNaN(v);
export const fmt = {
  price: (v) => (missing(v) ? '—' : (v < 0 ? '−' : '') + '$' + n(Math.abs(v), 2)),
  // $1.23B, $456.7M
  big(v) {
    if (missing(v)) return '—';
    const a = Math.abs(v), s = v < 0 ? '−' : '';
    if (a >= 1e12) return s + '$' + (a / 1e12).toFixed(2) + 'T';
    if (a >= 1e9) return s + '$' + (a / 1e9).toFixed(2) + 'B';
    if (a >= 1e6) return s + '$' + (a / 1e6).toFixed(1) + 'M';
    return s + '$' + n(a, 0);
  },
  count(v) {
    if (!v) return '—';
    if (v >= 1e9) return (v / 1e9).toFixed(2) + 'B';
    if (v >= 1e6) return (v / 1e6).toFixed(2) + 'M';
    if (v >= 1e3) return (v / 1e3).toFixed(1) + 'K';
    return String(Math.round(v));
  },
  pct: (v, digits = 1) => (missing(v) ? '—' : (v >= 0 ? '+' : '−') + Math.abs(v).toFixed(digits) + '%'),
  // $ millions with one decimal, negatives in parentheses
  mm(v) {
    if (missing(v)) return '—';
    const s = n(Math.abs(v), 1);
    return v < 0 ? `(${s})` : s;
  },
  // full dollars, per-share or share counts depending on the XBRL unit
  full(v, unit = 'USD') {
    if (missing(v)) return '—';
    const a = Math.abs(v), s = v < 0 ? '−' : '';
    if (unit === 'USD/shares') return s + '$' + a.toFixed(2);
    if (unit === 'shares') return s + n(a, 0) + ' shares';
    return s + '$' + n(a, 0);
  },
  int: (v) => (missing(v) ? '—' : n(v, 0)),
  size(b) {
    if (!b) return '';
    if (b < 1024) return b + ' B';
    if (b < 1048576) return Math.round(b / 1024) + ' KB';
    return (b / 1048576).toFixed(1) + ' MB';
  },
};
export function change(diff, base, suffix = '') {
  if (!base) return '';
  const cls = diff > 1e-5 ? 'up' : diff < -1e-5 ? 'down' : 'muted';
  const arrow = cls === 'up' ? '▲ ' : cls === 'down' ? '▼ ' : '';
  return `<span class="${cls}">${arrow}${diff >= 0 ? '+' : '−'}$${Math.abs(diff).toFixed(2)} (${fmt.pct((diff / base) * 100, 2)})${suffix}</span>`;
}

// ------------------------------------------------------------------ filing vocabulary
export const CATS = [['all', 'All'], ['annual', '10-K'], ['quarterly', '10-Q'], ['current', '8-K'], ['proxy', 'Proxy'], ['offering', 'Offerings'], ['ownership', 'Insiders and 13D/G'], ['other', 'Other']];
export function category(form) {
  const f = (form || '').toUpperCase();
  if (/^(10-K|10-KT|10-K405|20-F|40-F|ARS)(\/A)?$/.test(f)) return 'annual';
  if (/^10-Q(\/A)?$/.test(f)) return 'quarterly';
  if (/^(8-K|8-K12B|6-K)(\/A)?$/.test(f)) return 'current';
  if (/14A|14C/.test(f)) return 'proxy';
  if (/^(S-\d|F-\d|424B|POS AM|POS EX|EFFECT|RW|FWP|D$)/.test(f)) return 'offering';
  if (/^(3|4|5|144)(\/A)?$/.test(f) || /13D|13G|13F/.test(f)) return 'ownership';
  return 'other';
}
const FORM_DESC = {
  '10-K': 'Annual report', '10-K/A': 'Annual report (amended)', '10-Q': 'Quarterly report', '10-Q/A': 'Quarterly report (amended)',
  '8-K': 'Current report', '8-K/A': 'Current report (amended)', '6-K': 'Foreign issuer report', '20-F': 'Annual report (foreign issuer)',
  'DEF 14A': 'Definitive proxy statement', 'DEFA14A': 'Additional proxy materials', 'PRE 14A': 'Preliminary proxy statement', 'ARS': 'Annual report to shareholders',
  'S-1': 'Registration statement (IPO)', 'S-1/A': 'Registration statement (amended)', 'S-3': 'Shelf registration', 'S-3ASR': 'Automatic shelf registration',
  'S-8': 'Employee stock plan registration', '424B1': 'Prospectus', '424B3': 'Prospectus', '424B4': 'Final IPO prospectus', '424B5': 'Prospectus supplement (offering)',
  'FWP': 'Free writing prospectus', 'EFFECT': 'Notice of effectiveness', '3': 'Initial insider ownership', '4': 'Insider transaction', '5': 'Annual insider ownership',
  '144': 'Proposed sale of restricted stock', 'SC 13G': 'Passive holder above 5%', 'SC 13G/A': 'Passive holder above 5% (amended)', 'SC 13D': 'Active holder above 5%',
  'SC 13D/A': 'Active holder above 5% (amended)', 'SCHEDULE 13G': 'Passive holder above 5%', 'SCHEDULE 13G/A': 'Passive holder above 5% (amended)',
  'SCHEDULE 13D': 'Active holder above 5%', 'CORRESP': 'Company letter to the SEC', 'UPLOAD': 'SEC comment letter', 'D': 'Exempt offering notice',
  'NT 10-K': 'Late annual report notice', 'NT 10-Q': 'Late quarterly report notice', '8-A12B': 'Exchange registration',
};
const ITEMS_8K = {
  '1.01': 'Material agreement', '1.02': 'Agreement terminated', '1.05': 'Cybersecurity incident', '2.01': 'Acquisition or disposal', '2.02': 'Results of operations',
  '2.03': 'New financial obligation', '2.05': 'Restructuring costs', '2.06': 'Impairment', '3.01': 'Listing notice', '3.02': 'Unregistered equity sale',
  '3.03': 'Shareholder rights changed', '4.01': 'Auditor change', '4.02': 'Earlier financials unreliable', '5.02': 'Officer or director change',
  '5.03': 'Bylaws amended', '5.07': 'Shareholder vote results', '7.01': 'Reg FD disclosure', '8.01': 'Other events', '9.01': 'Exhibits',
};
export function describe(f) {
  return FORM_DESC[f.form] || (f.primaryDesc && f.primaryDesc.toUpperCase() !== f.form.toUpperCase() ? f.primaryDesc : '') || f.form;
}
export function itemsText(f) {
  if (!f.items) return '';
  return f.items.split(',').map((s) => s.trim()).filter((i) => i && i !== '9.01').map((i) => (ITEMS_8K[i] ? `${i} ${ITEMS_8K[i]}` : i)).join(', ');
}
export const tag = (form) => `<span class="tag c-${category(form)}">${esc(form)}</span>`;
export const byAcc = (acc) => state.filings.find((f) => f.acc === acc);

// Latest value of a key-financials metric: { v, asOf } or null.
export const latest = (fin, key) => (fin && fin.latest && fin.latest[key]) || null;

// Market cap and enterprise value from a quote and the latest filings.
export function valuationOf(quote, fin) {
  const sh = latest(fin, 'shares'), cash = latest(fin, 'cashinv'), debt = latest(fin, 'debt');
  const out = { shares: sh, cash, debt, cap: null, ev: null };
  if (quote && sh) {
    out.cap = quote.price * sh.v;
    out.ev = out.cap - (cash ? cash.v : 0) + (debt ? debt.v : 0);
  }
  return out;
}

export const icon = {
  ext: '<svg viewBox="0 0 16 16" width="11" height="11" style="vertical-align:-1px;margin-left:3px"><path d="M6 3H3v10h10v-3M9 3h4v4M13 3L7.5 8.5" fill="none" stroke="currentColor" stroke-width="1.5"/></svg>',
  x: '<svg viewBox="0 0 24 24" width="16" height="16"><path d="M6 6l12 12M18 6L6 18" stroke="currentColor" stroke-width="2" stroke-linecap="round"/></svg>',
  down: '<svg viewBox="0 0 24 24" width="15" height="15"><path d="M12 4v11m0 0l-4-4m4 4l4-4M5 19h14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>',
};
