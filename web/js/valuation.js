// Valuation tab: a risk-adjusted NPV, sum-of-the-parts model. Assumptions on the left
// recalculate the results on the right as you type. The arithmetic runs on the
// server (valuation.go) and follows the Biotech sum-of-the-parts worksheet.
import { $, $$, api, state, data, emit, toast, esc, fmt, niceDate, relTime, debounce, icon } from './core.js';

let versions = []; // names of this company's saved versions
let root, model = null, result = null, saved = false, dirty = false, price = null, flowsFor = 0, calcSeq = 0;
const open = new Set([0]); // which drug panels are expanded
let noteOpen = null;   // path of the input whose notepad is showing
let logAll = true;     // the list at the bottom shows every assumption, or only edited and noted ones
const FROM_FILINGS = new Set(['cash', 'debt', 'shares']);
const FIN_ROW = { cash: 'cashinv', debt: 'debt', shares: 'shares' }; // the Financials line each comes from
const NOTE_ICON = '<svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d="M3 2.5h7l3 3v8H3z M10 2.5v3h3 M5.5 8.5h5 M5.5 11h5" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" stroke-linecap="round"/></svg>';

const html = `
<div class="val-bar">
  <label class="val-ver" title="This company's saved versions of the model">Version <select class="select" id="val-version"></select></label>
  <span id="val-status" class="muted"></span><span style="flex:1"></span>
  <button class="btn" id="val-add">Add a drug</button>
  <details class="menu" id="val-more"><summary class="btn">More</summary><div class="menu-items">
    <button type="button" id="val-export">Export to Excel</button>
    <button type="button" id="val-file-save">Save this model to a file…</button>
    <button type="button" id="val-file-open">Open a model file…</button>
    <hr>
    <button type="button" id="val-reset" title="Replace what is on screen with the starting assumptions from the filings and pipeline. Nothing saved changes until you save.">Start over from the filings</button>
    <button type="button" id="val-delete">Delete this version…</button>
  </div></details>
  <button class="btn" id="val-saveas" title="Save a copy under a new name, such as Bull case">Save as…</button>
  <button class="btn primary" id="val-save">Save model</button>
</div>
<dialog id="val-ask"><form method="dialog">
  <h3 id="val-ask-title"></h3>
  <p id="val-ask-text" class="muted" style="margin:0"></p>
  <label id="val-ask-field">Name<input class="input" id="val-ask-name" maxlength="60" autocomplete="off"></label>
  <div class="err" id="val-ask-err" hidden></div>
  <div class="acts"><button class="btn">Cancel</button><button type="button" class="btn primary" id="val-ask-ok"></button></div>
</form></dialog>
<div class="val"><div class="val-form" id="val-form"></div><div class="val-out" id="val-out"></div></div>`;

// ------------------------------------------------------------------ form
// A field is [path, label, type, hint]. Types: pct (stored as a fraction), num, year, text.
const GENERAL = [
  ['valuationYear', 'Valuation year', 'year'], ['horizon', 'Years modelled', 'num'],
  ['cash', 'Cash and investments ($mm)', 'num'], ['debt', 'Debt ($mm)', 'num'], ['shares', 'Shares outstanding (millions)', 'num'],
  ['taxRate', 'Tax rate', 'pct'], ['taxLosses', 'Tax credit on losses', 'yes', 'The worksheet books one'],
  ['riskFree', 'Risk-free rate', 'pct'], ['beta', 'Beta', 'num'], ['mrp', 'Market risk premium', 'pct'],
  ['=coe', 'Cost of equity (CAPM)'], ['discountOverride', 'Discount rate set by hand', 'pct', 'Leave at 0 to use CAPM, rounded to 0.5%'], ['=rate', 'Discount rate applied'],
  ['unallocGA', 'Unallocated G&A ($mm a year)', 'num'], ['unallocGAGrowth', 'Unallocated G&A growth', 'pct'],
];
const DRUG = [
  ['name', 'Name', 'text'], ['indication', 'Indication', 'text'],
  ['#', 'Market'],
  ['patients', 'Patient population', 'num'], ['price', 'Price per patient per year ($)', 'num'], ['penetration', 'Peak penetration', 'pct'], ['=peak', 'Peak revenue before risk ($mm)'],
  ['launch', 'Launch year', 'year'], ['yearsToPeak', 'Years to peak sales', 'num', '3 to 10'], ['growthAfterPeak', 'Growth after peak', 'pct'],
  ['patentExpiry', 'Patent expiry', 'year'], ['declineAfterLOE', 'Yearly change after expiry', 'pct', 'Negative, e.g. -50'],
  ['share', 'Economic interest', 'pct', '100 unless partnered'],
  ['#', 'Probability of success'],
  ['pNDA', 'Approval once filed', 'pct'], ['=pos', 'Success to approval'],
];
const PHASE = [['start', 'Start year', 'year'], ['enrollment', 'Patients enrolled', 'num'], ['costPerPatient', 'Cost per patient ($)', 'num'], ['=cost', 'Trial cost ($mm)'], ['months', 'Months to complete', 'num'], ['pSuccess', 'Probability of success', 'pct']];
const COSTS = [
  ['#', 'Costs'],
  ['cogs', 'Cost of goods, % of revenue', 'pct'], ['smPct', 'Selling and marketing, % of revenue', 'pct'], ['gaPct', 'G&A, % of revenue', 'pct'],
  ['preLaunchReps', 'Sales reps before launch', 'num', 'Two years before launch'], ['costPerRep', 'Cost per rep per year ($)', 'num'],
  ['#', 'Capex and working capital'],
  ['capex0', 'First-year capex ($mm)', 'num'], ['capexGrowth', 'Capex growth', 'pct'], ['daPct', 'D&A, % of capex', 'pct'],
  ['nwc0', 'First-year working capital ($mm)', 'num'], ['nwcGrowth', 'Working capital growth', 'pct'],
  ['nwcFixed', 'Working capital grows', 'nwc'],
];

const get = (path) => path.split('.').reduce((o, k) => (o == null ? o : o[k]), model);
function put(path, v) {
  const keys = path.split('.'), last = keys.pop();
  keys.reduce((o, k) => o[k], model)[last] = v;
}
const shown = (v, type) => (type === 'pct' ? +((v || 0) * 100).toFixed(4) : v ?? '');

// ------------------------------------------------------------------ starting values and source notes
// Each input remembers the value it started with (base) and can carry a source note
// (src). Both live beside the values they describe: on the model for company inputs,
// on the drug for its inputs, keyed by the rest of the path ("phases.0.start").
function owner(path) {
  const m = /^products\.(\d+)\.(.+)$/.exec(path);
  return m ? [model.products[+m[1]], m[2]] : [model, path];
}
const baseOf = (path) => { const [o, k] = owner(path); return o && o.base ? o.base[k] : undefined; };
const noteOf = (path) => { const [o, k] = owner(path); return (o && o.src && o.src[k]) || ''; };
function setNote(path, text) {
  const [o, k] = owner(path);
  if (!o) return;
  o.src ||= {};
  if (text.trim()) o.src[k] = text; else delete o.src[k];
}
// status: 'start' (untouched), 'edited', or '' when the starting value isn't known.
function statusOf(path) {
  const b = baseOf(path);
  if (b === undefined) return '';
  return Math.abs(Number(get(path)) - b) < 1e-9 ? 'start' : 'edited';
}
function display(v, type) {
  if (type === 'yes') return v ? 'Yes' : 'No';
  if (type === 'nwc') return v ? 'At a fixed rate' : 'With revenue';
  if (type === 'done') return v ? 'Completed' : 'Ahead or running';
  if (type === 'pct') return shown(v, 'pct') + '%';
  if (type === 'year') return String(v);
  return Number(v).toLocaleString('en-US', { maximumFractionDigits: 4 });
}
function markHtml(path, type) {
  const st = statusOf(path);
  if (!st) return '';
  if (st === 'edited') return `<button type="button" class="mark edited" data-restore="${path}" data-t="${type}" title="Edited. Started as ${esc(display(baseOf(path), type))}. Click to put that back.">edited</button>`;
  if (FROM_FILINGS.has(path)) return `<button type="button" class="mark filed" data-source="${path}" title="Filled in from the latest filings. Click to see the number in the filing.">from filings</button>`;
  return `<span class="mark" title="The starting value. You have not changed it.">default</span>`;
}
const noteBtn = (path) => {
  const n = noteOf(path);
  return `<button type="button" class="note-btn ${n ? 'has' : ''}" data-note="${path}" title="${n ? esc(n) : 'Add a source or reasoning for this assumption'}">${NOTE_ICON}</button>`;
};
const notePad = (path) => (noteOpen === path ? `<div class="span notepad" data-pad="${path}"><textarea placeholder="Where this number comes from: source, page, date, reasoning">${esc(noteOf(path))}</textarea><div class="pad-acts"><span class="muted">Saved with the model</span><button type="button" class="link" data-pad-close>Done</button></div></div>` : '');

// refreshMark updates one input's marker and note button without redrawing the form,
// so typing keeps its place.
function refreshMark(path) {
  const el = root.querySelector(`[data-mark="${path}"]`);
  if (el) el.innerHTML = markHtml(path, el.dataset.t);
  const inp = root.querySelector(`[data-p="${path}"]`);
  if (inp) inp.classList.toggle('untouched', statusOf(path) === 'start');
  const nb = root.querySelector(`[data-note="${path}"]`);
  if (nb) { const n = noteOf(path); nb.classList.toggle('has', !!n); nb.title = n || 'Add a source or reasoning for this assumption'; }
}

// Every input in the model, in form order, for the list at the bottom and the export.
function allFields() {
  const rows = [];
  const push = (section, prefix, [key, label, type]) => {
    if (key === '#' || key[0] === '=' || type === 'text') return;
    const path = prefix + key;
    rows.push({ section, path, label, type, value: get(path), status: statusOf(path), note: noteOf(path) });
  };
  GENERAL.forEach((f) => push('Company', '', f));
  model.products.forEach((p, i) => {
    const pre = `products.${i}.`, sec = p.name || 'Drug ' + (i + 1);
    DRUG.forEach((f) => push(sec, pre, f));
    p.phases.forEach((ph, k) => {
      const pp = `${pre}phases.${k}.`, lab = (l) => `Phase ${k + 1}: ${l}`;
      push(sec, pp, ['done', lab('Status'), 'done']);
      if (ph.done) return;
      PHASE.forEach(([key, label, type]) => push(sec, pp, [key, lab(label), type]));
      if (k === 0 || p.phases[k - 1].done) push(sec, pp, ['pEnroll', lab('Probability the trial runs'), 'pct']);
    });
    COSTS.forEach((f) => push(sec, pre, f));
  });
  return rows;
}
const statusWord = (r) => (r.status === 'edited' ? 'Edited' : r.status === 'start' ? (FROM_FILINGS.has(r.path) ? 'From filings' : 'Default') : '');

function renderLog() {
  const box = $('#val-log', root);
  if (!box) return;
  const rows = allFields();
  const edited = rows.filter((r) => r.status === 'edited').length, noted = rows.filter((r) => r.note).length, untouched = rows.filter((r) => r.status === 'start' && !r.note).length;
  const list = logAll ? rows : rows.filter((r) => r.status === 'edited' || r.note);
  let section = '';
  const body = list.map((r) => {
    const head = r.section !== section ? `<div class="log-sec">${esc(section = r.section)}</div>` : '';
    return `${head}<div class="log-row" data-goto="${r.path}" title="Go to this input"><div class="log-top"><span class="log-lbl">${esc(r.label)}</span><span class="log-val">${esc(display(r.value, r.type))}</span>${r.status ? `<span class="mark ${r.status === 'edited' ? 'edited' : ''}">${statusWord(r).toLowerCase()}</span>` : ''}</div>${r.note ? `<div class="log-note">${esc(r.note)}</div>` : ''}</div>`;
  }).join('');
  box.innerHTML = `<div class="log-sum"><span>${edited} edited · ${noted} with a source · ${untouched} untouched and unsourced</span>
    <label class="log-filter"><input type="checkbox" id="val-log-all" ${logAll ? '' : 'checked'}> Only edited or sourced</label></div>
    ${body || '<p class="muted">Nothing edited or sourced yet.</p>'}`;
}

function field(prefix, [key, label, type, hint]) {
  if (key === '#') return `<h4>${label}</h4>`;
  if (key[0] === '=') return `<label>${label}${hint ? `<div class="hint">${hint}</div>` : ''}</label><div class="calc" data-calc="${prefix}${key.slice(1)}"></div><span></span>`;
  const path = prefix + key, v = get(path);
  if (type === 'text') return `<label>${label}</label><input class="text" data-p="${path}" data-t="text" value="${esc(v || '')}"><span></span>`;
  const lab = `<label>${label} <span data-mark="${path}" data-t="${type}">${markHtml(path, type)}</span>${hint ? `<div class="hint">${hint}</div>` : ''}</label>`;
  const cls = statusOf(path) === 'start' ? 'untouched' : '';
  const opts = { yes: [['1', 'Yes'], ['0', 'No']], nwc: [['0', 'With revenue'], ['1', 'At a fixed rate']], done: [['1', 'Completed'], ['0', 'Ahead or running']] }[type];
  const input = opts
    ? `<select class="${cls}" data-p="${path}" data-t="bool" ${type === 'done' ? 'data-redraw' : ''}>${opts.map(([val, text]) => `<option value="${val}" ${(v ? '1' : '0') === val ? 'selected' : ''}>${text}</option>`).join('')}</select>`
    : `<input class="${cls}" data-p="${path}" data-t="${type}" value="${shown(v, type)}" inputmode="decimal">`;
  return `${lab}${input}${noteBtn(path)}${notePad(path)}`;
}

function renderForm() {
  const drugs = model.products.map((p, i) => {
    const pre = `products.${i}.`;
    const phases = p.phases.map((ph, k) => {
      const pp = `${pre}phases.${k}.`;
      // "Probability the trial runs" is an input only when the phase before it is done.
      const runs = k === 0 || p.phases[k - 1].done ? field(pp, ['pEnroll', 'Probability the trial runs', 'pct']) : field(pp, ['=enroll', 'Probability the trial runs', '', 'Follows the phase before']);
      return `<h4>Phase ${k + 1}</h4>${field(pp, ['done', 'Status', 'done'])}
        ${ph.done ? '' : PHASE.map((f) => field(pp, f)).join('') + runs}`;
    }).join('');
    return `<details class="drug" data-i="${i}" ${open.has(i) ? 'open' : ''}><summary><b>${esc(p.name || 'Drug ' + (i + 1))}</b><span class="num" data-calc="${pre}npv"></span></summary>
      <div class="fields">${DRUG.map((f) => field(pre, f)).join('')}${phases}${COSTS.map((f) => field(pre, f)).join('')}
      <div class="span" style="margin-top:8px"><button class="link" data-remove="${i}">Remove this drug</button></div></div></details>`;
  }).join('');
  $('#val-form', root).innerHTML = `<h3>Company</h3><div class="fields">${GENERAL.map((f) => field('', f)).join('')}</div>
    <h3>Drugs</h3>${drugs || '<p class="muted">No drugs yet. Add one to start.</p>'}
    <h3>Assumptions and sources</h3><div id="val-log" class="val-log"></div>
    <h3>General notes</h3><textarea id="val-notes" class="input" style="width:100%;height:90px;padding:8px;resize:vertical" placeholder="Anything that isn't about one input: the thesis, things to check">${esc(model.notes || '')}</textarea>`;
  renderCalcs();
  renderLog();
}

// renderCalcs fills the read-only figures inside the form from the latest result.
function renderCalcs() {
  if (!result) return;
  const pct = (v) => (v * 100).toFixed(2).replace(/\.?0+$/, '') + '%';
  const vals = { coe: pct(result.costOfEquity), rate: pct(result.discountRate) };
  model.products.forEach((p, i) => {
    const pre = `products.${i}.`, r = result.products[i];
    if (!r) return;
    vals[pre + 'peak'] = fmt.mm(r.peakRevenue);
    vals[pre + 'pos'] = pct(r.pos);
    vals[pre + 'npv'] = fmt.mm(r.npv);
    // Chain the phase probabilities the same way the server does.
    let cum = 1;
    p.phases.forEach((ph, k) => {
      const enroll = k > 0 && !p.phases[k - 1].done ? cum : ph.pEnroll;
      cum = ph.done ? 1 : enroll * ph.pSuccess;
      vals[`${pre}phases.${k}.enroll`] = pct(enroll);
      vals[`${pre}phases.${k}.cost`] = fmt.mm((ph.enrollment * ph.costPerPatient) / 1e6);
    });
  });
  $$('[data-calc]', root).forEach((el) => { el.textContent = vals[el.dataset.calc] ?? ''; });
}

// ------------------------------------------------------------------ results
function renderOut() {
  const r = result, out = $('#val-out', root);
  if (!r) { out.innerHTML = ''; return; }
  const ps = r.perShare, upside = price && ps ? (ps / price - 1) * 100 : null;
  const hero = `<div class="val-hero">
    <div><div class="k">${ps ? 'Value per share' : 'Equity value'}</div><div class="big" style="font-size:32px">${ps ? fmt.price(ps) : '$' + fmt.mm(r.equityValue) + 'M'}</div></div>
    ${price ? `<div><div class="k">Share price</div><div class="big">${fmt.price(price)}</div></div>` : ''}
    ${upside !== null ? `<div><div class="k">${upside >= 0 ? 'Upside' : 'Downside'}</div><div class="big ${upside >= 0 ? 'up' : 'down'}">${fmt.pct(upside, 0)}</div></div>` : ''}
    <div><div class="k">Equity value</div><div class="big">$${fmt.mm(r.equityValue)}M</div></div>
    <div><div class="k">Discount rate</div><div class="big">${(r.discountRate * 100).toFixed(1)}%</div></div></div>`;
  const banner = model.placeholder ? `<div class="banner">This is a starting point, not an estimate. Cash, debt and shares come from the latest filings and the drugs from ClinicalTrials.gov, but the market sizes, prices, penetration and probabilities are the worksheet's placeholders. Replace them with your own before reading anything into the result.</div>` : '';

  // Bridge from drug NPVs to equity value.
  const parts = [...r.products.map((p) => [p.name, p.npv]), ['Unallocated costs', r.unallocated.npv], ['Cash and investments', model.cash], ...(model.debt ? [['Debt', -model.debt]] : [])];
  const lo = Math.min(0, ...parts.map((p) => p[1]), r.equityValue), hi = Math.max(0, ...parts.map((p) => p[1]), r.equityValue), span = hi - lo || 1;
  const row = ([label, v], total) => `<div class="bridge-row ${total ? 'total' : ''}"><span class="lbl" title="${esc(label)}">${esc(label)}</span>
    <span class="track"><span class="bar ${v < 0 ? 'neg' : ''}" style="left:${((Math.min(v, 0) - lo) / span) * 100}%;width:${(Math.abs(v) / span) * 100}%"></span><span class="zero" style="left:${(-lo / span) * 100}%"></span></span>
    <span class="num r">${fmt.mm(v)}</span></div>`;
  const bridge = `<h3>From drug values to equity value, $mm</h3><div class="bridge">${parts.map((p) => row(p)).join('')}${row(['Equity value', r.equityValue], true)}</div>`;

  const drugs = `<h3>Drugs</h3><div class="table-card" style="max-width:860px"><table class="grid"><thead><tr><th>Drug</th><th class="r">Launch</th><th class="r">Peak revenue, $mm</th><th class="r">Probability of success</th><th class="r">Risk-adjusted NPV, $mm</th><th class="r">Per share</th></tr></thead><tbody>
    ${r.products.map((p, i) => `<tr><td>${esc(p.name)}${model.products[i].indication ? `<div class="muted" style="font-size:12px">${esc(model.products[i].indication)}</div>` : ''}</td><td class="r">${model.products[i].launch}</td><td class="r">${fmt.mm(p.peakRevenue)}</td><td class="r">${(p.pos * 100).toFixed(1)}%</td><td class="r">${fmt.mm(p.npv)}</td><td class="r">${model.shares > 0 ? fmt.price(p.npv / model.shares) : '—'}</td></tr>`).join('')}
    </tbody></table></div>`;

  const s = r.sensitivity;
  const cellClass = (v, base) => [base ? 'base' : '', price && s.perShare ? (v >= price ? 'above' : 'below') : ''].join(' ');
  const sens = s ? `<h3>${s.perShare ? 'Value per share' : 'Equity value, $mm'} by discount rate and peak sales</h3>
    <table class="sens"><thead><tr><th>Peak sales</th>${s.rates.map((x) => `<th>${(x * 100).toFixed(1)}%</th>`).join('')}</tr></thead><tbody>
    ${s.scales.map((sc, i) => `<tr><th>${sc === 1 ? 'As modelled' : Math.round(sc * 100) + '%'}</th>${s.values[i].map((v, j) => `<td class="${cellClass(v, sc === 1 && Math.abs(s.rates[j] - r.discountRate) < 1e-9)}">${s.perShare ? fmt.price(v) : fmt.mm(v)}</td>`).join('')}</tr>`).join('')}
    </tbody></table>${price && s.perShare ? `<div class="muted" style="font-size:12px;margin-top:4px">Green is above today's ${fmt.price(price)}, red below.</div>` : ''}` : '';

  flowsFor = Math.min(flowsFor, r.products.length - 1);
  const p = r.products[flowsFor];
  const FLOW = [['uptake', 'Uptake, % of peak', 'pct'], ['adjRevenue', 'Risk-adjusted revenue', 'strong'], ['cogs', 'Cost of goods'], ['rd', 'R&D'], ['sm', 'Selling and marketing'], ['ga', 'G&A'], ['opProfit', 'Operating profit', 'strong'], ['tax', 'Taxes'], ['capex', 'Capex'], ['da', 'D&A'], ['nwc', 'Working capital'], ['fcf', 'Free cash flow', 'strong'], ['df', 'Discount factor', 'f'], ['pv', 'Present value', 'strong']];
  const cell = (v, kind) => (kind === 'pct' ? (v * 100).toFixed(0) + '%' : kind === 'f' ? v.toFixed(3) : fmt.mm(v));
  const flows = p ? `<h3>Yearly cash flows, $mm <select id="val-flow" class="select" style="margin-left:8px">${r.products.map((x, i) => `<option value="${i}" ${i === flowsFor ? 'selected' : ''}>${esc(x.name)}</option>`).join('')}</select></h3>
    <div class="table-card"><table class="flows"><thead><tr><th></th>${r.years.map((y) => `<th>${y}</th>`).join('')}</tr></thead><tbody>
    ${FLOW.map(([k, label, kind]) => `<tr class="${kind === 'strong' ? 'strong' : ''}"><td>${label}</td>${p.rows[k].map((v) => `<td class="${v < -0.005 && kind !== 'pct' ? 'neg' : ''}">${cell(v, kind)}</td>`).join('')}</tr>`).join('')}
    </tbody></table></div>` : '';

  out.innerHTML = `${hero}${banner}${bridge}${drugs}${sens}${flows}
    <p class="note">Method: each drug's peak revenue (patients × price × penetration) follows an uptake curve to peak, grows until patent expiry and then declines. Revenue is multiplied by the probability of reaching approval; trial costs by the probability the trial runs. Cash flows are discounted mid-year at the CAPM cost of equity. Equity value is the drugs' NPVs plus unallocated costs and cash, less debt. This follows your Biotech sum-of-the-parts worksheet and reproduces its results from its inputs. Economic interest and switching off tax credits on losses are additions. A model's output is only as good as its assumptions; this is a tool for your analysis, not investment advice.</p>`;
}

function renderStatus() {
  const s = $('#val-status', root);
  if (model.placeholder && !dirty) s.textContent = 'Starting assumptions, not yet saved';
  else if (dirty) s.textContent = 'Unsaved changes';
  else s.textContent = saved && model.updated ? `Saved ${relTime(model.updated)}` : '';
  $('#val-save', root).disabled = !dirty && saved;
}

const recalc = debounce(async () => {
  const mine = ++calcSeq;
  try {
    const r = await api('/api/model/calc', { json: model });
    if (mine !== calcSeq) return;
    result = r;
    renderCalcs();
    renderOut();
  } catch (e) { toast(e.message, true); }
}, 180);

function changed(redraw) {
  dirty = true;
  renderStatus();
  if (redraw) renderForm();
  recalc();
}

function take(m) {
  model = m.model; result = m.result; saved = m.saved; dirty = false;
  if (m.versions) versions = m.versions;
  model.name ||= 'Base case';
  renderVersions(); renderForm(); renderOut(); renderStatus();
}
const has = (name) => versions.some((v) => v.toLowerCase() === name.toLowerCase());
function renderVersions() {
  const names = has(model.name) ? versions : [...versions, model.name];
  $('#val-version', root).innerHTML = names.map((n) => `<option value="${esc(n)}" ${n.toLowerCase() === model.name.toLowerCase() ? 'selected' : ''}>${esc(n)}${has(n) ? '' : ' (not saved)'}</option>`).join('');
  $('#val-delete', root).disabled = !has(model.name);
}
// keepEdits runs next straight away, or after asking about unsaved edits.
const keepEdits = (next, doing) => (dirty && state.askUnsaved ? state.askUnsaved(next, doing) : next());
async function fetchModel(query) {
  const c = state.company;
  try {
    const m = await api(`/api/company/${c.cik}/model?name=${encodeURIComponent(c.name)}${query}`);
    if (state.company !== c) return null;
    return m;
  } catch (e) { toast(e.message, true); return null; }
}
async function switchVersion(name) {
  const m = await fetchModel(`&version=${encodeURIComponent(name)}`);
  if (!m) return renderVersions();
  state.data.model = Promise.resolve(JSON.parse(JSON.stringify(m)));
  open.clear(); open.add(0); noteOpen = null;
  take(m);
}
// ask shows the small question box: a name to type (Save as) or a yes/no (Delete).
function ask({ title, text, name, ok, run }) {
  const d = $('#val-ask', root);
  $('#val-ask-title', root).textContent = title;
  $('#val-ask-text', root).textContent = text;
  $('#val-ask-field', root).hidden = name === undefined;
  $('#val-ask-name', root).value = name || '';
  $('#val-ask-err', root).hidden = true;
  $('#val-ask-ok', root).textContent = ok;
  $('#val-ask-ok', root).onclick = async () => {
    const problem = await run($('#val-ask-name', root).value.trim().replace(/\s+/g, ' '));
    if (!problem) return d.close();
    $('#val-ask-err', root).textContent = problem; $('#val-ask-err', root).hidden = false;
  };
  d.showModal();
  if (name !== undefined) { $('#val-ask-name', root).focus(); $('#val-ask-name', root).select(); }
}

async function load(fresh) {
  const c = state.company;
  $('#val-form', root).innerHTML = '<div class="loading">Reading the balance sheet and pipeline</div>';
  $('#val-out', root).innerHTML = '';
  try {
    const [m, q] = await Promise.all([data('model', fresh), data('quote').catch(() => null)]);
    if (state.company !== c) return;
    price = q ? q.price : null;
    take(JSON.parse(JSON.stringify(m))); // edit a copy; the shared one changes on save
  } catch (e) {
    if (state.company === c) $('#val-form', root).innerHTML = `<div class="err" style="margin-top:12px">${esc(e.message)}</div>`;
  }
}

// saveModel stores the model and reports whether that worked.
async function saveModel() {
  const c = state.company;
  if (!model || !c) return false;
  try {
    const m = await api(`/api/company/${c.cik}/model`, { method: 'PUT', json: model });
    if (state.company !== c) return true;
    state.data.model = Promise.resolve(JSON.parse(JSON.stringify(m)));
    model.placeholder = false; model.updated = m.model.updated; model.name = m.model.name; result = m.result; saved = true; dirty = false;
    versions = m.versions || versions;
    renderVersions(); renderOut(); renderStatus();
    toast(`Saved "${model.name}"`);
    return true;
  } catch (e) { toast(e.message, true); return false; }
}

export default {
  id: 'valuation', label: 'Valuation', html,
  unsaved: () => !!model && dirty, // edits not yet saved; the app asks before closing
  save: saveModel,
  init(el) {
    root = el;
    const form = $('#val-form', root);
    form.addEventListener('input', (e) => {
      const t = e.target;
      if (t.id === 'val-notes') { model.notes = t.value; dirty = true; renderStatus(); return; }
      const pad = t.closest('[data-pad]');
      if (pad) { setNote(pad.dataset.pad, t.value); refreshMark(pad.dataset.pad); renderLog(); dirty = true; renderStatus(); return; }
      if (!t.dataset.p || t.tagName === 'SELECT') return;
      if (t.dataset.t === 'text') {
        put(t.dataset.p, t.value);
        if (t.dataset.p.endsWith('.name')) { t.closest('.drug').querySelector('summary b').textContent = t.value || 'Drug'; renderLog(); }
        return changed(false);
      }
      const v = parseFloat(t.value.replace(/[,$%\s]/g, ''));
      if (Number.isNaN(v)) return; // mid-typing ("-", "")
      put(t.dataset.p, t.dataset.t === 'pct' ? v / 100 : t.dataset.t === 'year' ? Math.round(v) : v);
      refreshMark(t.dataset.p); renderLog();
      changed(false);
    });
    form.addEventListener('change', (e) => {
      const t = e.target;
      if (t.id === 'val-log-all') { logAll = !t.checked; renderLog(); return; }
      if (t.tagName !== 'SELECT' || !t.dataset.p) return;
      put(t.dataset.p, t.value === '1');
      refreshMark(t.dataset.p); renderLog();
      changed(t.hasAttribute('data-redraw'));
    });
    form.addEventListener('toggle', (e) => {
      const d = e.target.closest && e.target.closest('.drug');
      if (d) d.open ? open.add(+d.dataset.i) : open.delete(+d.dataset.i);
    }, true);
    form.addEventListener('click', (e) => {
      const nb = e.target.closest('[data-note]');
      if (nb) { // open this input's notepad, or close it if it is the one showing
        noteOpen = noteOpen === nb.dataset.note ? null : nb.dataset.note;
        renderForm();
        const ta = noteOpen && root.querySelector('[data-pad] textarea');
        if (ta) { ta.focus(); ta.setSelectionRange(ta.value.length, ta.value.length); }
        return;
      }
      if (e.target.closest('[data-pad-close]')) { noteOpen = null; renderForm(); return; }
      const rs = e.target.closest('[data-restore]');
      if (rs) { // put an edited input back to its starting value
        const path = rs.dataset.restore, b = baseOf(path), isBool = typeof get(path) === 'boolean';
        put(path, isBool ? b === 1 : b);
        return changed(true);
      }
      const sb = e.target.closest('[data-source]');
      if (sb) return emit('show-source', { key: FIN_ROW[sb.dataset.source], from: { tab: 'valuation', label: 'Valuation' } });
      const go = e.target.closest('[data-goto]');
      if (go) { // from the list at the bottom to the input, with its notepad open
        const path = go.dataset.goto, m = /^products\.(\d+)\./.exec(path);
        if (m) open.add(+m[1]);
        noteOpen = path;
        renderForm();
        const inp = root.querySelector(`[data-p="${path}"]`);
        if (inp) { inp.scrollIntoView({ block: 'center' }); const ta = root.querySelector('[data-pad] textarea'); if (ta) ta.focus({ preventScroll: true }); }
        return;
      }
      const b = e.target.closest('[data-remove]');
      if (!b) return;
      model.products.splice(+b.dataset.remove, 1);
      open.clear(); noteOpen = null;
      changed(true);
    });
    $('#val-out', root).addEventListener('change', (e) => { if (e.target.id === 'val-flow') { flowsFor = +e.target.value; renderOut(); } });
    $('#val-add', root).addEventListener('click', () => {
      if (!model) return;
      if (model.products.length >= 8) return toast('A model can hold up to 8 drugs.', true);
      const y = model.valuationYear;
      const drug = {
        name: 'Drug ' + (model.products.length + 1), indication: '', launch: y + 4, yearsToPeak: 7, growthAfterPeak: 0.02, patentExpiry: y + 14, declineAfterLOE: -0.5,
        patients: 10000, price: 100000, penetration: 0.2, pNDA: 0.95, share: 1,
        phases: [{ done: false, start: y, enrollment: 50, costPerPatient: 500000, months: 12, pEnroll: 1, pSuccess: 0.7 }, { done: false, start: y + 1, enrollment: 150, costPerPatient: 500000, months: 24, pEnroll: 1, pSuccess: 0.35 }, { done: false, start: y + 3, enrollment: 300, costPerPatient: 750000, months: 36, pEnroll: 1, pSuccess: 0.65 }],
        cogs: 0.1, smPct: 0.25, gaPct: 0.15, preLaunchReps: 40, costPerRep: 250000, capex0: 5, capexGrowth: 0.05, daPct: 0.95, nwc0: 5, nwcGrowth: 0.05, nwcFixed: false,
      };
      drug.base = {};
      const keep = (k, v) => { if (typeof v === 'number' || typeof v === 'boolean') drug.base[k] = Number(v); };
      Object.entries(drug).forEach(([k, v]) => keep(k, v));
      drug.phases.forEach((ph, i) => Object.entries(ph).forEach(([k, v]) => keep(`phases.${i}.${k}`, v)));
      model.products.push(drug);
      open.add(model.products.length - 1);
      changed(true);
    });
    $('#val-save', root).addEventListener('click', saveModel);
    const closeMenu = () => { $('#val-more', root).open = false; };
    $('#val-more', root).addEventListener('click', (e) => { if (e.target.closest('.menu-items button')) closeMenu(); });
    document.addEventListener('click', (e) => { if (!e.target.closest('#val-more')) closeMenu(); });
    $('#val-version', root).addEventListener('change', (e) => {
      const name = e.target.value;
      renderVersions(); // show the current one again until the switch happens
      keepEdits(() => switchVersion(name), 'switching versions');
    });
    $('#val-saveas', root).addEventListener('click', () => ask({
      title: 'Save as a new version', ok: 'Save',
      text: 'Saves what is on screen under a new name and keeps the other versions as they are.',
      name: has(model.name) ? '' : model.name,
      run: async (name) => {
        if (!name) return 'Type a name, such as Bull case.';
        if (has(name)) return `There is already a version called "${name}". Choose another name.`;
        const before = model.name;
        model.name = name;
        if (await saveModel()) return '';
        model.name = before;
        return 'That could not be saved.';
      },
    }));
    $('#val-delete', root).addEventListener('click', () => ask({
      title: `Delete "${model.name}"?`, ok: 'Delete',
      text: versions.length > 1 ? 'This saved version is removed. Your other versions stay.' : 'This is the only saved version. The Valuation tab goes back to the starting assumptions.',
      run: async () => {
        const c = state.company;
        try { await api(`/api/company/${c.cik}/model?version=${encodeURIComponent(model.name)}`, { method: 'DELETE' }); } catch (e) { return e.message; }
        open.clear(); open.add(0); noteOpen = null;
        load(true);
        return '';
      },
    }));
    $('#val-reset', root).addEventListener('click', async () => {
      const m = await fetchModel(`&fresh=1&version=${encodeURIComponent(model.name)}`);
      if (!m) return;
      const wasSaved = has(model.name);
      open.clear(); open.add(0); noteOpen = null;
      take(m);
      if (wasSaved) { dirty = true; renderStatus(); toast('Back to the starting assumptions. The saved version changes only when you save.'); }
    });
    $('#val-file-save', root).addEventListener('click', async () => {
      const c = state.company;
      try {
        const r = await api(`/api/company/${c.cik}/model/file`, { json: { action: 'save', model, ticker: c.ticker, company: c.name } });
        if (!r.canceled) toast(`Model saved to ${r.file}`);
      } catch (e) { toast(e.message, true); }
    });
    $('#val-file-open', root).addEventListener('click', () => keepEdits(async () => {
      const c = state.company;
      try {
        const r = await api(`/api/company/${c.cik}/model/file`, { json: { action: 'open' } });
        if (r.canceled || state.company !== c) return;
        // Shown as a new, unsaved version so it cannot overwrite one of the same name.
        let name = r.model.name || 'From file';
        if (has(name)) { let i = 2; const base = `${name} (from file)`; name = base; while (has(name)) name = `${base} ${i++}`; }
        r.model.name = name;
        open.clear(); open.add(0); noteOpen = null;
        take({ model: r.model, result: r.result, saved: false });
        dirty = true; renderStatus();
        toast(`Opened as "${name}". Save model to keep it in this session.`);
      } catch (e) { toast(e.message, true); }
    }, 'opening a model file'));
    $('#val-export', root).addEventListener('click', async () => {
      const c = state.company;
      try {
        const r = await api(`/api/company/${c.cik}/model/export`, { json: { model, ticker: c.ticker, name: c.name, price: price || 0, sources: allFields().map((r) => ({ section: r.section, label: r.label, value: display(r.value, r.type), start: baseOf(r.path) === undefined ? '' : display(baseOf(r.path), r.type), status: statusWord(r), note: r.note })) } });
        toast('Saved ' + r.file.split(/[\\/]/).pop());
      } catch (e) { toast(e.message, true); }
    });
  },
  reset() { versions = []; model = null; result = null; calcSeq++; open.clear(); open.add(0); flowsFor = 0; noteOpen = null; },
  show() { if (!model) load(false); },
};

// Start over deletes a saved model, so it asks first: a second press within five
// seconds confirms. (Browser confirm() dialogs would freeze the app window.)
let resetArmed = 0;
