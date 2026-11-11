// News tab: articles from Google News and Yahoo Finance, merged with the company's
// 8-K filings so press coverage and primary disclosures sit on one timeline.
import { $, $$, state, data, emit, esc, debounce, openUrl, relTime, describe, itemsText } from './core.js';

let root, items = null, problems = [], filter = 'all', text = '';

const html = `
<div class="toolbar"><div id="news-chips" class="chips"></div><span class="grow"></span>
  <input id="news-text" class="input" type="search" placeholder="Filter headlines" style="width:210px"><button id="news-refresh" class="btn">Refresh</button></div>
<div class="scroll" style="padding-top:0"><div class="feed" id="news-list"></div></div>`;

// The company's own announcements: 8-K current reports.
export const secItems = () => state.filings.filter((f) => /^8-K/.test(f.form)).slice(0, 60).map((f) => ({
  title: `${f.form}: ${itemsText(f) || describe(f)}`, published: f.accepted || f.filed + 'T12:00:00Z', source: 'SEC EDGAR', feed: 'SEC', acc: f.acc,
}));

function render() {
  const news = items || [], sec = secItems();
  const counts = { all: news.length + sec.length, news: news.length, sec: sec.length };
  $('#news-chips', root).innerHTML = [['all', 'All'], ['news', 'News articles'], ['sec', '8-K filings']].map(([k, l]) => `<button class="chip ${filter === k ? 'on' : ''}" data-f="${k}">${l}<span class="n">${counts[k]}</span></button>`).join('');
  emit('count', { tab: 'news', n: news.length });
  let list = filter === 'news' ? news : filter === 'sec' ? sec : [...news, ...sec];
  if (text) list = list.filter((it) => `${it.title} ${it.source} ${it.summary || ''}`.toLowerCase().includes(text));
  list = list.slice().sort((a, b) => (b.published || '').localeCompare(a.published || ''));
  const note = problems.length ? `<div class="banner">${esc(problems.join('. '))}.</div>` : '';
  if (!list.length) { $('#news-list', root).innerHTML = note + '<div class="empty">Nothing matches.</div>'; return; }
  let day = '';
  $('#news-list', root).innerHTML = note + list.map((it) => {
    const d = it.published ? new Date(it.published) : null;
    const key = d ? d.toLocaleDateString('en-US', { weekday: 'long', month: 'long', day: 'numeric', year: 'numeric' }) : 'Undated';
    const head = key !== day ? `<div class="day">${esc((day = key))}</div>` : '';
    return `${head}<div class="news-item" ${it.acc ? `data-acc="${it.acc}"` : `data-url="${esc(it.url)}"`}>
      <div class="n-title">${esc(it.title)}</div>
      <div class="n-meta">${it.acc ? '<span class="pill" style="--c:var(--current)">SEC filing</span>' : ''}<span>${esc(it.source)}</span><span title="${esc(d ? d.toLocaleString() : '')}">${esc(relTime(it.published))}</span>${it.acc ? '' : `<span>via ${esc(it.feed)}</span>`}</div>
      ${it.summary ? `<div class="n-sum">${esc(it.summary)}</div>` : ''}</div>`;
  }).join('');
}

async function load(fresh) {
  const c = state.company;
  if (!items) $('#news-list', root).innerHTML = '<div class="loading">Loading news</div>';
  try {
    const r = await data('news', fresh);
    if (state.company !== c) return;
    items = r.items;
    problems = r.problems || [];
  } catch (e) {
    if (state.company !== c) return;
    items = items || [];
    problems = [e.message.replace(/\.$/, '')];
  }
  render();
}

export default {
  id: 'news', label: 'News', html,
  init(el) {
    root = el;
    $('#news-chips', root).addEventListener('click', (e) => { const b = e.target.closest('[data-f]'); if (b) { filter = b.dataset.f; render(); } });
    $('#news-text', root).addEventListener('input', debounce((e) => { text = e.target.value.trim().toLowerCase(); render(); }, 120));
    $('#news-refresh', root).addEventListener('click', () => load(true));
    $('#news-list', root).addEventListener('click', (e) => {
      const it = e.target.closest('.news-item');
      if (!it) return;
      if (it.dataset.url) openUrl(it.dataset.url); else emit('open-filing', { acc: it.dataset.acc });
    });
  },
  reset() { items = null; problems = []; },
  show() { if (items) render(); else load(false); },
};
