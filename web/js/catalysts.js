// Upcoming catalysts tab: company guidance, clinical-trial dates and estimated
// corporate events on one dated timeline.
import { $, state, data, emit, esc, debounce, niceDate, openUrl, MONTHS, icon } from './core.js';

let root, d = null, filter = 'all', text = '', past = false, shown = [];

const html = `
<div class="toolbar"><div id="cat-chips" class="chips"></div><span class="grow"></span>
  <label class="check"><input type="checkbox" id="cat-past">Show past</label>
  <input id="cat-text" class="input" type="search" placeholder="Filter, e.g. NDA or a drug name" style="width:230px"></div>
<div class="scroll" style="padding-top:0"><div class="feed" style="max-width:1050px">
  <div id="cat-sources" class="muted" style="font-size:12px;line-height:1.6;margin-bottom:4px"></div><div id="cat-list"></div>
  <p class="note">Guidance is quoted word for word from the company's recent 10-K, 10-Q and press releases; open the source to read it in context. Trial dates are the sponsor's own estimates on ClinicalTrials.gov, which often slip and only approximate when results are announced. Earnings and meeting dates are projected from last year's. Confirm any date against the company's latest disclosures.</p>
</div></div>`;

const KIND = { 'Data readout': 'k-data', Regulatory: 'k-reg', 'Trial start / enrollment': 'k-start', Commercial: 'k-comm', 'Cash runway': 'k-cash', Milestone: 'k-cash', 'Trial completion': 'k-trial', Earnings: 'k-event', 'Annual meeting': 'k-event' };
const phase = (ps) => (ps || []).map((p) => p.replace('EARLY_PHASE1', 'Early Phase 1').replace('PHASE', 'Phase ').replace(/^NA$/, 'No phase')).join('/').replace(/\/Phase /g, '/');
const status = (s) => (s || '').toLowerCase().replace(/_/g, ' ').replace(/^\w/, (c) => c.toUpperCase());

// ClinicalTrials.gov dates are "2027-09" or "2027-09-30".
function trialDate(s) {
  if (/^\d{4}-\d{2}-\d{2}$/.test(s || '')) return { start: s, end: s, label: niceDate(s) };
  if (/^\d{4}-\d{2}$/.test(s || '')) {
    const y = +s.slice(0, 4), m = +s.slice(5, 7);
    return { start: s + '-01', end: new Date(Date.UTC(y, m, 0)).toISOString().slice(0, 10), label: `${MONTHS[m - 1]} ${y}` };
  }
  return null;
}

// timeline flattens the three sources into dated items. Exported for the overview.
export function timeline(c) {
  const out = [];
  for (const g of c.guidance || []) {
    const next = g.periods.find((p) => p.end >= c.today), p = next || g.periods[g.periods.length - 1];
    out.push({ type: 'guidance', kind: g.kind, start: p.start, end: p.end, label: p.label, sub: 'Company guidance', past: !next, g, title: g.text });
  }
  for (const t of c.trials || []) {
    const r = trialDate(t.primaryCompletion);
    if (!r) continue;
    const est = (t.primaryCompletionType || '').toUpperCase() === 'ESTIMATED';
    out.push({ type: 'trial', kind: 'Trial completion', ...r, sub: est ? 'Estimated primary completion' : 'Primary completion', past: r.end < c.today, t, title: (t.acronym ? t.acronym + ': ' : '') + t.title });
  }
  for (const e of c.events || []) {
    out.push({ type: 'event', kind: e.kind, start: e.date, end: e.date, label: 'About ' + niceDate(e.date).replace(/, \d{4}$/, ''), sub: 'Estimated date', past: e.date < c.today, e, title: e.title });
  }
  return out;
}

function mark(textIn, periods) {
  let h = esc(textIn);
  for (const ph of new Set(periods.map((p) => esc(p.phrase)).filter(Boolean))) h = h.split(ph).join(`<mark>${ph}</mark>`);
  return h;
}

function card(it, i) {
  const kind = `<span class="pill ${KIND[it.kind] || 'k-cash'}">${esc(it.kind)}</span>`;
  let body;
  if (it.g) {
    const g = it.g, s = g.source, name = s.doc === s.form ? s.form : `${s.title} (${s.doc}, ${s.form})`;
    body = `<div class="cat-top">${kind}${(g.programs || []).map((p) => `<span class="prog">${esc(p)}</span>`).join('')}</div>
      <div class="quote">“${mark(g.text, g.periods)}”</div>
      <div class="cat-meta"><button class="link" data-src="${i}">${esc(name)} filed ${esc(niceDate(s.filed))}</button>${g.also && g.also.length ? `<span>Also in ${g.also.length} other document${g.also.length > 1 ? 's' : ''}</span>` : ''}${g.periods.length > 1 ? `<span>Timeframes: ${g.periods.map((p) => esc(p.label)).join(', ')}</span>` : ''}</div>`;
  } else if (it.t) {
    const t = it.t;
    const bits = [phase(t.phases), status(t.status), t.enrollment ? `${t.enrollment} participants${(t.enrollmentType || '').toUpperCase() === 'ESTIMATED' ? ' planned' : ''}` : '', (t.conditions || []).slice(0, 2).join(', ')].filter(Boolean);
    const done = trialDate(t.completion);
    body = `<div class="cat-top">${kind}<b>${esc(it.title)}</b></div>
      <div class="cat-detail">${esc(bits.join(', '))}${t.interventions && t.interventions.length ? `<br>Interventions: ${esc(t.interventions.join(', '))}` : ''}</div>
      <div class="cat-meta"><button class="link" data-trial="${i}">${esc(t.nct)} on ClinicalTrials.gov${icon.ext}</button>${done ? `<span>Study completion ${esc(done.label)}</span>` : ''}${t.lastUpdate ? `<span>Record updated ${esc(niceDate(t.lastUpdate))}</span>` : ''}</div>`;
  } else {
    body = `<div class="cat-top">${kind}<b>${esc(it.e.title)}</b></div><div class="cat-meta"><span>${esc(it.e.basis)}</span></div>`;
  }
  return `<div class="cat ${it.past ? 'past' : ''}"><div class="cat-when"><div class="w">${esc(it.label)}</div><div class="ws">${esc(it.sub)}</div></div><div>${body}</div></div>`;
}

function render() {
  const all = (shown = timeline(d));
  const counts = { all: 0, guidance: 0, trial: 0, event: 0 };
  for (const i of all) if (past || !i.past) { counts.all++; counts[i.type]++; }
  $('#cat-chips', root).innerHTML = [['all', 'All'], ['guidance', 'Company guidance'], ['trial', 'Clinical trials'], ['event', 'Earnings and meetings']].map(([k, l]) => `<button class="chip ${filter === k ? 'on' : ''}" data-f="${k}">${l}<span class="n">${counts[k]}</span></button>`).join('');
  emit('count', { tab: 'catalysts', n: all.filter((i) => !i.past).length });
  const read = d.scanned.map((s) => `${esc(s.doc === s.form ? s.form : s.doc + ' in ' + s.form)} filed ${esc(niceDate(s.filed))}`);
  read.push(`ClinicalTrials.gov (${d.trials.length} ${d.trials.length === 1 ? 'study' : 'studies'})`);
  $('#cat-sources', root).innerHTML = `Read for this timeline: ${read.join('; ')}.${d.problems.length ? `<div class="warn">${esc(d.problems.join('. '))}.</div>` : ''}`;
  let list = all.filter((i) => (filter === 'all' || i.type === filter) && (past || !i.past));
  if (text) list = list.filter((i) => `${i.kind} ${i.title} ${i.g ? (i.g.programs || []).join(' ') : ''} ${i.t ? i.t.nct + ' ' + (i.t.interventions || []).join(' ') + ' ' + (i.t.conditions || []).join(' ') : ''}`.toLowerCase().includes(text));
  const up = list.filter((i) => !i.past).sort((a, b) => a.start.localeCompare(b.start) || a.end.localeCompare(b.end));
  const gone = list.filter((i) => i.past).sort((a, b) => b.end.localeCompare(a.end));
  if (!up.length && !gone.length) {
    $('#cat-list', root).innerHTML = `<div class="empty">${all.length ? 'Nothing matches these filters.' : 'No upcoming catalysts were found in recent filings or on ClinicalTrials.gov.'}</div>`;
    return;
  }
  let out = '', year = '';
  for (const it of up) {
    if (it.start.slice(0, 4) !== year) out += `<div class="year-h">${(year = it.start.slice(0, 4))}</div>`;
    out += card(it, all.indexOf(it));
  }
  if (gone.length) out += `<div class="today-h">Today, ${esc(niceDate(d.today))}. Past items follow</div>` + gone.map((it) => card(it, all.indexOf(it))).join('');
  $('#cat-list', root).innerHTML = out;
}

export default {
  id: 'catalysts', label: 'Catalysts', html,
  init(el) {
    root = el;
    $('#cat-chips', root).addEventListener('click', (e) => { const b = e.target.closest('[data-f]'); if (b) { filter = b.dataset.f; render(); } });
    $('#cat-past', root).addEventListener('change', (e) => { past = e.target.checked; if (d) render(); });
    $('#cat-text', root).addEventListener('input', debounce((e) => { text = e.target.value.trim().toLowerCase(); if (d) render(); }, 120));
    $('#cat-list', root).addEventListener('click', (e) => {
      const b = e.target.closest('button');
      if (!b) return;
      if (b.dataset.trial !== undefined) openUrl(shown[+b.dataset.trial].t.url);
      if (b.dataset.src !== undefined) { const s = shown[+b.dataset.src].g.source; emit('open-filing', { acc: s.acc, url: s.url }); }
    });
  },
  reset() { d = null; if (root) { $('#cat-list', root).innerHTML = ''; $('#cat-sources', root).innerHTML = ''; } },
  async show() {
    if (d) return;
    const c = state.company;
    $('#cat-list', root).innerHTML = '<div class="loading">Reading the latest 10-K, 10-Q and press releases, and checking ClinicalTrials.gov</div>';
    try {
      const r = await data('catalysts');
      if (state.company !== c) return;
      d = r;
      render();
    } catch (e) {
      if (state.company === c) $('#cat-list', root).innerHTML = `<div class="err">${esc(e.message)}</div>`;
    }
  },
};
