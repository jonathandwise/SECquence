// Website resources tab: files the company posts on its own website and does not
// file with the SEC, such as posters, scientific presentations and slide decks.
import { $, api, state, data, emit, esc, debounce, niceDate, relTime, openUrl, toast, icon } from './core.js';

let root, d = null, filter = 'all', text = '', config = false, busy = false;

const html = `
<div class="toolbar"><div id="web-chips" class="chips"></div><span class="grow"></span>
  <input id="web-text" class="input" type="search" placeholder="Filter, e.g. poster or a drug name" style="width:230px">
  <button class="btn" id="web-rescan" title="Read the website again">Rescan</button></div>
<div class="scroll" style="padding-top:0"><div class="feed" style="max-width:1050px">
  <div id="web-sources" class="muted" style="font-size:12px;line-height:1.6;margin-bottom:4px"></div>
  <div id="web-config" class="web-config" hidden></div>
  <div id="web-list"></div>
  <p class="note">These files come from the company's own website, not from SEC filings, and are opened or downloaded from there. Every company builds its site differently, so the list is a best effort: it reads the home page and the pages that look like investor events, presentations, science, publications and pipeline. If something is missing, use "Website and pages" to add the page that lists it. Dates and titles are taken from the text beside each link and can be wrong.</p>
</div></div>`;

const KINDS = [['all', 'All'], ['Presentation', 'Presentations'], ['Poster', 'Posters'], ['Publication', 'Publications'], ['other', 'Other']];
const PILL = { Presentation: 'k-data', Poster: 'k-start', Publication: 'k-reg', 'Fact sheet': 'k-trial', Report: 'k-event', Other: 'k-cash' };
const bucket = (doc) => (['Presentation', 'Poster', 'Publication'].includes(doc.kind) ? doc.kind : 'other');
const when = (s) => (/^\d{4}-\d{2}-\d{2}$/.test(s) ? niceDate(s) : /^\d{4}-\d{2}$/.test(s) ? niceDate(s + '-01').replace(/ \d{1,2},/, '') : '');
const host = (u) => { try { return new URL(u).host; } catch { return u; } };

function renderConfig() {
  const box = $('#web-config', root);
  box.hidden = !config;
  if (!config) return;
  box.innerHTML = `
    <label>Company website<div class="row"><input class="input" id="web-url" placeholder="www.example.com" value="${esc(d && d.site ? d.site : '')}"><button class="btn" id="web-url-save">Use this website</button></div>
      <small class="muted">${d && d.source && d.source !== 'you' ? `Found from ${d.source === 'SEC' ? "the SEC's records" : "the company's latest press release"}. Change it if it is wrong.` : 'The home page, for example www.example.com.'}</small></label>
    <label>Add a page that lists documents<div class="row"><input class="input" id="web-page" placeholder="Paste the page's address"><button class="btn" id="web-page-add">Add page</button></div>
      <small class="muted">For pages the scan did not find by itself, such as a posters or publications page.</small></label>
    ${d && d.extra && d.extra.length ? `<div><small class="muted">Pages you added</small>${d.extra.map((p) => `<div class="web-extra"><span>${esc(p)}</span><button class="icon-btn" data-remove="${esc(p)}" title="Stop reading this page">×</button></div>`).join('')}</div>` : ''}`;
}

function render() {
  const docs = d.docs || [];
  const counts = { all: docs.length };
  for (const doc of docs) counts[bucket(doc)] = (counts[bucket(doc)] || 0) + 1;
  $('#web-chips', root).innerHTML = KINDS.map(([k, l]) => `<button class="chip ${filter === k ? 'on' : ''}" data-f="${k}">${l}<span class="n">${counts[k] || 0}</span></button>`).join('');
  emit('count', { tab: 'website', n: docs.length });
  $('#web-sources', root).innerHTML = d.site
    ? `Read ${d.pages.length} page${d.pages.length === 1 ? '' : 's'} on <button class="link" data-open="${esc(d.site)}">${esc(host(d.site))}${icon.ext}</button>${d.scanned ? `, ${esc(relTime(d.scanned))}` : ''}. <button class="link" id="web-toggle">Website and pages</button>${(d.problems || []).length ? `<div class="warn">${esc(d.problems.join(' '))}</div>` : ''}`
    : '';
  renderConfig();
  if (!d.site) {
    config = true; renderConfig();
    $('#web-list', root).innerHTML = '<div class="empty">SECquence could not find this company\'s website. Enter it above to list its presentations, posters and publications.</div>';
    return;
  }
  let list = docs.filter((doc) => filter === 'all' || bucket(doc) === filter);
  if (text) list = list.filter((doc) => `${doc.title} ${doc.kind} ${doc.pageName} ${doc.url}`.toLowerCase().includes(text));
  if (!list.length) {
    $('#web-list', root).innerHTML = `<div class="empty">${docs.length ? 'Nothing matches these filters.' : 'No documents were found.'}</div>`;
    return;
  }
  let out = '', year = null;
  for (const doc of list) {
    const y = doc.date ? doc.date.slice(0, 4) : 'Undated';
    if (y !== year) out += `<div class="year-h">${(year = y)}</div>`;
    const i = docs.indexOf(doc);
    out += `<div class="cat web-doc"><div class="cat-when"><div class="w">${esc(when(doc.date) || '')}</div><div class="ws">${esc(doc.fileType || 'File')}</div></div><div>
      <div class="cat-top"><span class="pill ${PILL[doc.kind] || 'k-cash'}">${esc(doc.kind)}</span><b>${esc(doc.title)}</b></div>
      <div class="cat-meta"><button class="link" data-doc="${i}">Open${icon.ext}</button><button class="link" data-save="${i}">Save to download folder</button><span>Found on <button class="link" data-open="${esc(doc.page)}" title="${esc(doc.page)}">${esc(doc.pageName || host(doc.page))}</button></span></div></div></div>`;
  }
  $('#web-list', root).innerHTML = out;
}

async function load(fresh) {
  const c = state.company;
  if (busy) return;
  busy = true;
  $('#web-rescan', root).disabled = true;
  $('#web-list', root).innerHTML = '<div class="loading">Reading the company\'s website. This can take up to a minute</div>';
  try {
    if (fresh) state.data.site = api(`/api/company/${c.cik}/site?fresh=1`);
    const r = await data('site');
    if (state.company !== c) return;
    d = r;
    render();
  } catch (e) {
    delete state.data.site;
    if (state.company === c) $('#web-list', root).innerHTML = `<div class="err">${esc(e.message)}</div>`;
  } finally { busy = false; $('#web-rescan', root).disabled = false; }
}

async function change(body) {
  const c = state.company;
  try {
    await api(`/api/company/${c.cik}/site`, { method: 'PUT', json: body });
    delete state.data.site;
    config = false;
    load(false);
  } catch (e) { toast(e.message, true); }
}

export default {
  id: 'website', label: 'Website Resources', html,
  init(el) {
    root = el;
    $('#web-chips', root).addEventListener('click', (e) => { const b = e.target.closest('[data-f]'); if (b) { filter = b.dataset.f; if (d) render(); } });
    $('#web-text', root).addEventListener('input', debounce((e) => { text = e.target.value.trim().toLowerCase(); if (d) render(); }, 120));
    $('#web-rescan', root).addEventListener('click', () => load(true));
    root.addEventListener('click', async (e) => {
      const b = e.target.closest('button');
      if (!b) return;
      if (b.id === 'web-toggle') { config = !config; return renderConfig(); }
      if (b.id === 'web-url-save') return change({ url: $('#web-url', root).value });
      if (b.id === 'web-page-add') return change({ add: $('#web-page', root).value });
      if (b.dataset.remove) return change({ remove: b.dataset.remove });
      if (b.dataset.open) return openUrl(b.dataset.open);
      if (b.dataset.doc) return openUrl(d.docs[+b.dataset.doc].url);
      if (b.dataset.save) {
        const c = state.company, doc = d.docs[+b.dataset.save];
        b.disabled = true; b.textContent = 'Saving…';
        try {
          const r = await api(`/api/company/${c.cik}/site/save`, { json: { url: doc.url, ticker: c.ticker || '' } });
          b.textContent = 'Saved';
          toast(`Saved to ${r.file}`);
        } catch (err) { b.disabled = false; b.textContent = 'Save to download folder'; toast(err.message, true); }
      }
    });
  },
  show() { if (!d) load(false); },
  reset() { d = null; filter = 'all'; text = ''; config = false; busy = false; if (root) { $('#web-text', root).value = ''; $('#web-list', root).innerHTML = ''; $('#web-sources', root).innerHTML = ''; $('#web-chips', root).innerHTML = ''; $('#web-config', root).hidden = true; } },
};
