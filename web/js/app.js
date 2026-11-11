// The app shell: start-up, search, watchlist, settings, the company header with its
// filing strip, tabs, and the download progress card.
import { $, $$, T, api, state, data, on, emit, toast, debounce, esc, enc, fmt, niceDate, MONTHS, openUrl, reveal, category, describe, itemsText, byAcc } from './core.js';
import overview from './overview.js';
import filings from './filings.js';
import financials from './financials.js';
import stock from './stock.js';
import news from './news.js';
import catalysts from './catalysts.js';
import website from './website.js';
import valuation from './valuation.js';
import ownership from './ownership.js';
import compare from './compare.js';

const views = [overview, financials, valuation, filings, ownership, stock, news, catalysts, website];

// The window closing ends the app: the server watches this heartbeat.
const ping = () => fetch('/api/ping', { headers: { 'X-App-Token': T } }).catch(() => {});
ping();
setInterval(ping, 4000);
window.addEventListener('pagehide', () => navigator.sendBeacon('/api/bye?t=' + T));
// Closing the window with unsaved valuation edits: a web page may not word or add
// buttons to the browser's own "Leave?" prompt, so that prompt comes first, and if
// the user stays, the app's own question (with a Save button) follows.
let closing = false;
window.addEventListener('beforeunload', (e) => {
  if (closing) return;
  const unsaved = valuation.unsaved();
  if (unsaved || (state.job && !state.job.finished)) { e.preventDefault(); e.returnValue = ''; }
  if (unsaved) setTimeout(() => { if (valuation.unsaved() && !$('#unsaved').open) askUnsaved(); }, 150); // runs only if they stayed
});
function closeApp() {
  closing = true;
  window.close();
  // If the window would not close itself, ask the program to close it, and failing
  // that say so rather than sit there.
  setTimeout(() => {
    fetch('/api/exit', { method: 'POST', headers: { 'X-App-Token': T } }).catch(() => {});
    setTimeout(() => { closing = false; toast('You can close the window now.'); }, 1500);
  }, 500);
}
// ------------------------------------------------------------------ sessions
function renderSession() {
  const s = state.settings.session || {};
  $('#session-name').textContent = s.file ? s.name : 'This computer';
  $('#session-cur').textContent = s.file ? s.name : "This computer's session";
  $('#session-path').textContent = s.file || 'Kept in your Windows account on this computer. Save it as a file to take it elsewhere.';
  $('#session-local').hidden = !s.file;
  $('#session-note').textContent = s.file
    ? 'Changes to the watchlist and each saved model are written to this file as you make them. Copy or email the file to use it elsewhere, then choose "Open a session file" there. Your SEC contact email and download folder stay with each computer.'
    : '';
  const recent = (s.recent || []).filter((p) => p !== s.file);
  $('#session-recent').innerHTML = recent.length
    ? `<small class="muted">Recent sessions</small><ul class="session-list">${recent.map((p) => `<li><button type="button" class="link" data-open="${esc(p)}" title="${esc(p)}">${esc(p.split(/[\\/]/).pop().replace(/\.secq$/i, ''))}</button><span class="muted">${esc(p)}</span><button type="button" class="icon-btn" data-forget="${esc(p)}" title="Remove from this list">×</button></li>`).join('')}</ul>`
    : '';
}
async function sessionAction(action, path) {
  try {
    const r = await api('/api/session', { json: { action, path: path || '' } });
    if (r.canceled) return;
    if (action === 'forget') { state.settings = r; return renderSession(); }
    closing = true; // the model on screen belongs to the session being left
    location.reload();
  } catch (e) { toast(e.message, true); }
}
$('#nav-session').addEventListener('click', () => { renderSession(); $('#session').showModal(); });
$('#session').addEventListener('click', (e) => {
  const b = e.target.closest('[data-session],[data-open],[data-forget]');
  if (!b) return;
  if (b.dataset.forget) return sessionAction('forget', b.dataset.forget);
  const action = b.dataset.session || 'open', path = b.dataset.open || '';
  const run = () => sessionAction(action, path);
  $('#session').close();
  if (valuation.unsaved()) askUnsaved(run, action === 'saveas' ? 'saving the session' : 'switching sessions'); else run();
});

// ------------------------------------------------------------------ updates
let release = null;
function showUpdate(r) {
  release = r;
  const b = $('#nav-update');
  b.hidden = !(r && r.newer);
  if (r && r.newer) b.textContent = `Update to version ${r.latest}`;
}
async function checkUpdate(fresh) {
  try { showUpdate(await api('/api/update' + (fresh ? '?fresh=1' : ''))); return release; } catch (e) { if (fresh) throw e; return null; }
}
$('#nav-update').addEventListener('click', () => {
  $('#update-q').textContent = `You have version ${release.current}. Version ${release.latest} is on GitHub. SECquence will download it, close, and open again on the new version.`;
  $('#update-notes').textContent = release.notes || '';
  $('#update-notes').hidden = !release.notes;
  $('#update-go').disabled = false; $('#update-go').textContent = 'Update and restart';
  $('#update').showModal();
});
async function applyUpdate() {
  const b = $('#update-go');
  b.disabled = true; b.textContent = 'Downloading…';
  try {
    await api('/api/update', { method: 'POST' });
    closing = true;
    b.textContent = 'Restarting…';
    setTimeout(() => window.close(), 400); // the new version opens its own window
  } catch (e) { $('#update').close(); toast(e.message, true); }
}
$('#update-go').addEventListener('click', () => {
  if (!valuation.unsaved()) return applyUpdate();
  $('#update').close();
  askUnsaved(() => { $('#update').showModal(); applyUpdate(); }, 'updating');
});
$('#s-update').addEventListener('click', async () => {
  const msg = $('#s-update-msg');
  msg.textContent = 'Checking…';
  try {
    const r = await checkUpdate(true);
    msg.textContent = !r.configured ? 'This copy was not built from a GitHub release, so it cannot update itself.'
      : r.newer ? `Version ${r.latest} is available. Use the "Update" button at the bottom left.`
      : r.latest ? 'You have the latest version.' : 'No releases have been published yet.';
  } catch (e) { msg.textContent = e.message; }
});

// askUnsaved shows the question. next is what to do once the edits are saved or
// dropped; without it, the app closes.
let afterUnsaved = null;
state.askUnsaved = (next, doing) => askUnsaved(next, doing); // for tabs that switch models
function askUnsaved(next, doing = 'opening another company') {
  afterUnsaved = next || null;
  $('#unsaved-q').textContent = `You have unsaved changes in this model. Would you like to save before ${next ? doing : 'exiting'}?`;
  $('#unsaved [data-act=discard]').textContent = next ? 'Continue without saving' : 'Exit without saving';
  $('#unsaved [data-act=save]').textContent = next ? 'Save and continue' : 'Save and exit';
  if (!$('#unsaved').open) $('#unsaved').showModal();
}
$('#unsaved').addEventListener('click', async (e) => {
  const act = e.target.dataset && e.target.dataset.act;
  if (!act) return;
  if (act === 'stay') return $('#unsaved').close();
  if (act === 'save' && !(await valuation.save())) return $('#unsaved').close(); // the error is shown; nothing else happens
  $('#unsaved').close();
  if (afterUnsaved) afterUnsaved(); else closeApp();
});

// ------------------------------------------------------------------ pages and tabs
function showPage(page) {
  state.page = page;
  for (const p of ['welcome', 'company', 'compare']) $('#' + p).hidden = p !== page;
  $('#nav-compare').classList.toggle('on', page === 'compare');
  if (page === 'compare') compare.show();
  if (page !== 'company') $$('#watch li').forEach((li) => li.classList.remove('on'));
}

function buildViews() {
  $('#tabs').innerHTML = views.map((v) => `<button class="tab" data-tab="${v.id}">${v.label}<span class="n" id="n-${v.id}"></span></button>`).join('');
  $('#views').innerHTML = views.map((v) => `<div class="view" id="view-${v.id}" hidden>${v.html}</div>`).join('');
  views.forEach((v) => v.init && v.init($('#view-' + v.id)));
  $('#compare').innerHTML = compare.html;
  compare.init($('#compare'));
}

function showTab(id) {
  if (!views.some((v) => v.id === id)) id = 'overview';
  state.tab = id;
  $$('.tab').forEach((b) => b.classList.toggle('on', b.dataset.tab === id));
  for (const v of views) $('#view-' + v.id).hidden = v.id !== id;
  if (state.company) views.find((v) => v.id === id).show();
  rememberView();
}
$('#tabs').addEventListener('click', (e) => { const b = e.target.closest('[data-tab]'); if (b) showTab(b.dataset.tab); });
on('goto', showTab);
on('count', ({ tab, n }) => { const el = $('#n-' + tab); if (el) el.textContent = n ? n : ''; });

const rememberView = debounce(() => {
  const c = state.company;
  if (c) api('/api/last', { json: { cik: c.cik, ticker: c.ticker, name: c.name, tab: state.tab } }).catch(() => {});
}, 800);

// ------------------------------------------------------------------ settings
function applyTheme() {
  const dark = state.settings.theme === 'dark';
  document.documentElement.dataset.theme = dark ? 'dark' : 'light';
  $('#theme-label').textContent = dark ? 'Light mode' : 'Dark mode';
}
$('#nav-theme').addEventListener('click', async () => {
  state.settings = await api('/api/settings', { json: { theme: state.settings.theme === 'dark' ? 'light' : 'dark' } });
  applyTheme();
  emit('theme');
});

function renderRailPath() {
  const p = state.settings.downloadDir || '';
  const parts = p.split(/[\\/]/);
  $('#rail-path').textContent = parts.length > 3 ? '…\\' + parts.slice(-2).join('\\') : p;
  $('#rail-path').title = p;
}

function openSettings(first = false) {
  const s = state.settings;
  $('#s-first').hidden = !first;
  $('#s-email').value = s.email || '';
  $('#s-dir').value = s.downloadDir || '';
  $('#s-ex').value = s.exhibits || 'ex99';
  $('#s-error').hidden = true;
  $('#s-cancel').hidden = !s.email;
  $('#s-browser').textContent = s.browser ? 'PDFs are made with ' + s.browser : 'Microsoft Edge was not found, so filings cannot be saved as PDF. Install Edge from microsoft.com/edge.';
  $('#s-browser').className = s.browser ? 'muted' : 'warn';
  $('#s-version').textContent = 'SECquence version ' + (s.version || '');
  $('#s-update-msg').textContent = '';
  const dlg = $('#settings');
  if (!dlg.open) dlg.showModal();
  setTimeout(() => (s.email ? $('#s-dir') : $('#s-email')).focus(), 50);
}
on('need-email', () => openSettings(true));
$('#settings-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  const firstSave = !state.settings.email;
  try {
    state.settings = await api('/api/settings', { json: { email: $('#s-email').value.trim(), downloadDir: $('#s-dir').value.trim(), exhibits: $('#s-ex').value } });
    $('#settings').close();
    renderRailPath();
    emit('settings');
    toast('Settings saved');
    if (firstSave) resume();
    refreshWatch();
  } catch (err) {
    $('#s-error').textContent = err.message;
    $('#s-error').hidden = false;
  }
});
$('#s-cancel').addEventListener('click', () => $('#settings').close());
$('#settings').addEventListener('cancel', (e) => { if (!state.settings.email) e.preventDefault(); });
$('#s-browse').addEventListener('click', async () => {
  const b = $('#s-browse');
  b.disabled = true;
  try {
    const r = await api('/api/pick-folder', { json: { initial: $('#s-dir').value.trim() } });
    if (r.picked && r.path) $('#s-dir').value = r.path;
  } catch (err) {
    $('#s-error').textContent = err.message;
    $('#s-error').hidden = false;
  } finally {
    b.disabled = false;
  }
});
$('#nav-settings').addEventListener('click', () => openSettings(false));
$('#nav-folder').addEventListener('click', () => reveal({}));
$('#nav-compare').addEventListener('click', () => showPage('compare'));

// ------------------------------------------------------------------ search
const q = $('#q'), sug = $('#suggest');
let found = [], pick = -1, searchSeq = 0, movedByKeys = false;

function renderSuggest(msg) {
  if (msg) sug.innerHTML = `<li class="s-msg">${esc(msg)}</li>`;
  else sug.innerHTML = found.map((s, i) => `<li data-i="${i}" class="${i === pick ? 'on' : ''}"><span class="mono">${esc(s.ticker || '—')}</span><span class="s-name">${esc(s.name)}</span><span class="muted">CIK ${s.cik}</span></li>`).join('');
  sug.hidden = !msg && !found.length;
}
const suggest = debounce(async () => {
  const text = q.value.trim(), seq = ++searchSeq;
  if (!text) { found = []; renderSuggest(); return; }
  try {
    const res = await api('/api/search?q=' + enc(text));
    if (seq !== searchSeq) return;
    found = res;
    pick = res.length ? 0 : -1;
    renderSuggest(res.length ? '' : 'No company matches. Try the exact ticker or a CIK number.');
  } catch (e) {
    if (seq === searchSeq) renderSuggest(e.message);
    if (/email/i.test(e.message)) openSettings(true);
  }
}, 140);
q.addEventListener('input', () => { movedByKeys = false; suggest(); });
q.addEventListener('focus', () => { if (q.value.trim()) suggest(); });
q.addEventListener('blur', () => setTimeout(() => (sug.hidden = true), 150));
q.addEventListener('keydown', async (e) => {
  if ((e.key === 'ArrowDown' || e.key === 'ArrowUp') && found.length) {
    e.preventDefault();
    pick = (pick + (e.key === 'ArrowDown' ? 1 : -1) + found.length) % found.length;
    movedByKeys = true;
    renderSuggest();
  } else if (e.key === 'Enter') {
    e.preventDefault();
    const text = q.value.trim();
    if (!text) return;
    // Enter takes the best match for what is typed now, even if the list is stale.
    let entry = movedByKeys ? found[pick] : null;
    if (!entry) {
      try { entry = (await api('/api/search?q=' + enc(text)))[0]; } catch (err) { toast(err.message, true); return; }
    }
    if (entry) choose(entry); else toast(`No company found for “${text}”`, true);
  } else if (e.key === 'Escape') {
    sug.hidden = true;
    q.blur();
  }
});
sug.addEventListener('mousedown', (e) => {
  const li = e.target.closest('li[data-i]');
  if (li) { e.preventDefault(); choose(found[+li.dataset.i]); }
});
document.addEventListener('keydown', (e) => {
  if ((e.ctrlKey && e.key.toLowerCase() === 'k') || (e.key === '/' && !/INPUT|SELECT|TEXTAREA/.test(document.activeElement.tagName))) {
    e.preventDefault();
    q.focus();
    q.select();
  }
});
$$('[data-try]').forEach((b) => b.addEventListener('click', async () => {
  try {
    const res = await api('/api/search?q=' + enc(b.dataset.try));
    if (res[0]) choose(res[0]);
  } catch (e) { toast(e.message, true); if (/email/i.test(e.message)) openSettings(true); }
}));
function choose(entry) {
  sug.hidden = true;
  found = [];
  q.value = '';
  q.blur();
  openCompany(entry);
}
on('open-company', (entry) => openCompany(entry));

// ------------------------------------------------------------------ company
let loadSeq = 0;
async function openCompany({ cik, ticker, name }, tab, dropEdits) {
  // Opening another company replaces the valuation model on screen, so ask first.
  if (!dropEdits && valuation.unsaved()) return askUnsaved(() => openCompany({ cik, ticker, name }, tab, true));
  const seq = ++loadSeq;
  showPage('company');
  Object.assign(state, { company: null, filings: [], newAccs: new Set(), watched: false, data: {} });
  $('#co-name').textContent = name || 'Loading';
  $('#co-tick').textContent = ticker || '';
  $('#co-tick').hidden = !ticker;
  $('#co-quote').hidden = true;
  $('#co-meta').innerHTML = '<span class="busy">Loading filings from SEC EDGAR</span>';
  $('#strip').innerHTML = '';
  $$('.tab .n').forEach((el) => (el.textContent = ''));
  views.forEach((v) => v.reset && v.reset());
  $$('#watch li').forEach((li) => li.classList.toggle('on', +li.dataset.cik === cik));
  try {
    const d = await api(`/api/company/${cik}/filings?ticker=${enc(ticker || '')}`);
    if (seq !== loadSeq) return;
    state.company = d.company;
    state.filings = d.filings;
    state.newAccs = new Set(d.newAccessions || []);
    state.watched = d.watched;
    if (state.watchStatus[cik]) state.watchStatus[cik].newCount = 0;
    renderWatch();
    renderHead();
    renderStrip();
    emit('count', { tab: 'filings', n: d.filings.length });
    showTab(tab || state.tab);
    loadQuote();
  } catch (e) {
    if (seq !== loadSeq) return;
    $('#co-meta').innerHTML = `<span class="warn">${esc(e.message)}</span>`;
    if (/email/i.test(e.message)) openSettings(true);
  }
}

function renderHead() {
  const c = state.company;
  $('#co-name').textContent = c.name;
  $('#co-tick').textContent = c.ticker || 'CIK ' + c.cik;
  $('#co-tick').hidden = false;
  const fye = c.fiscalYearEnd && c.fiscalYearEnd.length === 4 ? `Fiscal year ends ${MONTHS[+c.fiscalYearEnd.slice(0, 2) - 1]} ${+c.fiscalYearEnd.slice(2)}` : '';
  const bits = [(c.exchanges || []).filter(Boolean).join(', '), c.sicDescription, fye, c.stateOfIncorporation ? 'Incorporated in ' + c.stateOfIncorporation : '', 'CIK ' + c.cik].filter(Boolean);
  $('#co-meta').innerHTML = bits.map((b) => `<span>${esc(b)}</span>`).join('');
  renderWatchButton();
}
function renderWatchButton() {
  $('#co-watch').classList.toggle('on', state.watched);
  $('#co-star').textContent = state.watched ? '★' : '☆';
  $('#co-watch-label').textContent = state.watched ? 'Watching' : 'Watch';
}
$('#co-watch').addEventListener('click', async () => {
  const c = state.company;
  if (!c) return;
  try {
    state.settings.watchlist = await api('/api/watchlist', { json: { action: state.watched ? 'remove' : 'add', cik: c.cik, ticker: c.ticker, name: c.name } });
    state.watched = !state.watched;
    if (state.watched) state.watchStatus[c.cik] = { newCount: 0 };
    renderWatchButton();
    renderWatch();
    toast(state.watched ? `Watching ${c.ticker || c.name}` : `Stopped watching ${c.ticker || c.name}`);
  } catch (e) { toast(e.message, true); }
});
$('#co-edgar').addEventListener('click', () => state.company && openUrl(`https://www.sec.gov/edgar/browse/?CIK=${state.company.cik}`));
$('#co-quote').addEventListener('click', () => showTab('stock'));

async function loadQuote() {
  const c = state.company;
  try {
    const d = await data('quote');
    if (state.company !== c || !d) return;
    const diff = d.price - d.prevClose;
    const cls = diff > 0 ? 'up' : diff < 0 ? 'down' : 'muted';
    $('#co-quote').innerHTML = `${fmt.price(d.price)} <span class="${cls}">${d.prevClose ? fmt.pct((diff / d.prevClose) * 100, 2) : ''}</span>`;
    $('#co-quote').hidden = false;
  } catch { /* the Stock tab explains price problems */ }
}
on('quote', loadQuote);

// The filing strip: five lanes, one band per filing, over the last six years.
const LANES = [['annual', '10-K'], ['quarterly', '10-Q'], ['current', '8-K'], ['offering', 'Offering'], ['ownership', 'Insider']];
function renderStrip() {
  const el = $('#strip');
  const W = el.clientWidth || 900, left = 54, laneH = 9, gap = 3;
  const end = Date.now() + 20 * 864e5;
  const oldest = state.filings.length ? new Date(state.filings[state.filings.length - 1].filed).getTime() : end;
  const start = Math.max(oldest - 30 * 864e5, end - 6 * 365.25 * 864e5);
  const x = (t) => left + ((t - start) / (end - start)) * (W - left - 6);
  let svg = '';
  LANES.forEach(([cat, label], i) => {
    const y = i * (laneH + gap);
    svg += `<rect x="${left}" y="${y}" width="${W - left - 6}" height="${laneH}" rx="1.5" fill="var(--sunk)"/><text x="${left - 6}" y="${y + laneH - 0.5}" text-anchor="end">${label}</text>`;
  });
  for (let y = new Date(start).getFullYear() + 1; y <= new Date(end).getFullYear(); y++) {
    const xx = x(Date.UTC(y, 0, 1)).toFixed(1);
    svg += `<line x1="${xx}" x2="${xx}" y1="0" y2="${LANES.length * (laneH + gap)}" stroke="var(--line)"/><text x="${+xx + 3}" y="${LANES.length * (laneH + gap) + 8}">${y}</text>`;
  }
  for (const f of state.filings) {
    const t = new Date(f.filed).getTime();
    const lane = LANES.findIndex(([cat]) => cat === category(f.form));
    if (lane < 0 || t < start) continue;
    svg += `<rect class="band" data-acc="${f.acc}" x="${(x(t) - 1.25).toFixed(1)}" y="${lane * (laneH + gap)}" width="2.5" height="${laneH}" fill="var(--${LANES[lane][0] === 'ownership' ? 'insider' : LANES[lane][0]})"/>`;
  }
  el.innerHTML = `<svg viewBox="0 0 ${W} 72" preserveAspectRatio="none">${svg}</svg><div class="strip-tip" hidden></div>`;
}
$('#strip').addEventListener('mousemove', (e) => {
  const tip = $('.strip-tip');
  const band = e.target.closest('.band');
  if (!tip) return;
  if (!band) { tip.hidden = true; return; }
  const f = byAcc(band.dataset.acc);
  tip.textContent = `${f.form} filed ${niceDate(f.filed)}${itemsText(f) ? ': ' + itemsText(f) : f.form === describe(f) ? '' : ': ' + describe(f)}`;
  tip.hidden = false;
  const box = $('#strip').getBoundingClientRect();
  tip.style.left = Math.min(box.width - 140, Math.max(140, e.clientX - box.left)) + 'px';
});
$('#strip').addEventListener('mouseleave', () => { const tip = $('.strip-tip'); if (tip) tip.hidden = true; });
$('#strip').addEventListener('click', (e) => {
  const band = e.target.closest('.band');
  if (band) emit('open-filing', { acc: band.dataset.acc });
});
window.addEventListener('resize', debounce(() => { if (state.company) renderStrip(); }, 150));

// ------------------------------------------------------------------ watchlist
function renderWatch() {
  const list = state.settings.watchlist || [];
  $('#watch').innerHTML = list.length ? list.map((w) => {
    const s = state.watchStatus[w.cik] || {};
    const on = state.page === 'company' && state.company && state.company.cik === w.cik;
    return `<li data-cik="${w.cik}" data-ticker="${esc(w.ticker)}" data-name="${esc(w.name)}" class="${on ? 'on' : ''}">
      <span class="mono">${esc(w.ticker || 'CIK ' + w.cik)}</span>${s.newCount ? `<span class="w-new" title="Filed since you last opened this company">${s.newCount} new</span>` : '<span></span>'}
      <span class="w-name">${esc(w.name)}${s.latestForm ? `, last filed ${esc(s.latestForm)} on ${esc(niceDate(s.latestFiled))}` : ''}</span></li>`;
  }).join('') : '<li class="watch-empty" style="display:block;cursor:default">Open a company and choose Watch to keep it here. New filings are flagged.</li>';
}
$('#watch').addEventListener('click', (e) => {
  const li = e.target.closest('li[data-cik]');
  if (li) openCompany({ cik: +li.dataset.cik, ticker: li.dataset.ticker, name: li.dataset.name });
});
async function refreshWatch() {
  if (!state.settings.email || !(state.settings.watchlist || []).length) return;
  $('#wl-refresh').classList.add('spin');
  try {
    for (const s of await api('/api/watchlist/status')) state.watchStatus[s.cik] = s;
    if (state.company && state.watchStatus[state.company.cik]) state.watchStatus[state.company.cik].newCount = 0;
    renderWatch();
  } catch (e) {
    toast(e.message, true);
  } finally {
    $('#wl-refresh').classList.remove('spin');
  }
}
$('#wl-refresh').addEventListener('click', refreshWatch);
setInterval(refreshWatch, 15 * 60 * 1000);

// ------------------------------------------------------------------ downloads
on('download', async (items) => {
  if (!items.length) return;
  if (state.job && !state.job.finished) { toast('A download is already running. Wait for it to finish.', true); return; }
  try {
    const { id } = await api('/api/download', { json: { items } });
    state.job = { id, total: items.length, done: 0, results: [], finished: false };
    renderJob();
    pollJob(id);
  } catch (e) {
    toast(e.message, true);
    if (/email/i.test(e.message)) openSettings(true);
  }
});
async function pollJob(id) {
  try {
    const job = await api('/api/jobs/' + id);
    if (!state.job || state.job.id !== id) return;
    state.job = job;
    renderJob();
    if (!job.finished) setTimeout(() => pollJob(id), 700);
  } catch {
    setTimeout(() => pollJob(id), 1500);
  }
}
function renderJob() {
  const job = state.job, el = $('#job');
  el.hidden = !job;
  if (!job) return;
  const errors = (job.results || []).filter((r) => r.status === 'error');
  const title = job.finished ? (job.canceled ? 'Download canceled' : 'Download finished') : `Saving PDFs: filing ${Math.min(job.done + 1, job.total)} of ${job.total}`;
  el.innerHTML = `<h4>${esc(title)}</h4>
    <div class="bar"><div style="width:${job.finished ? 100 : Math.round((job.done / job.total) * 100)}%"></div></div>
    ${job.finished ? '' : `<div class="cur">${esc(job.current || 'Starting')}</div>`}
    <div class="counts"><span class="up">${job.saved || 0} saved</span>${job.skipped ? `<span class="muted">${job.skipped} already downloaded</span>` : ''}${job.failed ? `<span class="down">${job.failed} failed</span>` : ''}</div>
    ${errors.length ? `<ul>${errors.map((r) => `<li><b>${esc(r.label)}</b>: ${esc(r.message)}</li>`).join('')}</ul>` : ''}
    <div class="acts">${job.finished ? `<button class="btn" data-a="close">Close</button>${job.lastFile ? '<button class="btn primary" data-a="show">Show files</button>' : ''}` : '<button class="btn" data-a="cancel">Cancel download</button>'}</div>`;
}
$('#job').addEventListener('click', async (e) => {
  const a = e.target.closest('[data-a]')?.dataset.a, job = state.job;
  if (!a || !job) return;
  if (a === 'cancel') await api(`/api/jobs/${job.id}/cancel`, { json: {} }).catch(() => {});
  if (a === 'close') { state.job = null; renderJob(); }
  if (a === 'show') reveal({ path: job.lastFile });
});

// ------------------------------------------------------------------ start
function resume() {
  const last = state.settings.last;
  if (last && last.cik && state.page === 'welcome') openCompany(last, last.tab);
}
(async function start() {
  // A few bands on the welcome screen, in the filing-type colours.
  const colours = ['annual', 'quarterly', 'current', 'quarterly', 'offering', 'quarterly', 'current', 'annual', 'insider', 'current', 'quarterly', 'current'];
  $('#gel').innerHTML = colours.map((c, i) => `<i style="background:var(--${c});margin-top:${(i * 7) % 16}px"></i>`).join('');
  buildViews();
  try {
    state.settings = await api('/api/state');
  } catch (e) { toast(e.message, true); return; }
  applyTheme();
  renderRailPath();
  renderWatch();
  renderSession();
  if (state.settings.session && state.settings.session.missing) toast(`Couldn't open the session file ${state.settings.session.missing}. If it is on a USB drive, plug it in and open it from Session. Showing this computer's session for now.`, true);
  emit('settings');
  if (!state.settings.email) openSettings(true); else resume();
  refreshWatch();
  checkUpdate(false);
})();
