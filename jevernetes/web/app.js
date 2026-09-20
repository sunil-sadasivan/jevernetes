'use strict';
const $ = (id) => document.getElementById(id);
const state = {csrf: '', reports: [], report: null, selected: null, page: 0, pageSize: 40, loading: false, job: null, watchingLive: false, view: 'events', tailPaused: false, frozenTail: [], selection: new Map(), visibleEvents: []};
const fmt = (n) => Number(n || 0).toLocaleString();
const reviewLabel = e => human(e.review?.status || e.importance);
const human = (s) => String(s || 'unknown').replaceAll('_', ' ');
const date = (s) => { const d = new Date(s); return Number.isNaN(d.valueOf()) ? 'Unknown date' : d.toLocaleString([], {month:'short', day:'numeric', hour:'numeric', minute:'2-digit'}); };
const sourceName = (s) => s?.type === 'kubernetes' ? [s.namespace, s.pod, s.container].filter(Boolean).join('/') : s?.path || 'Unknown source';
function node(tag, text, className) { const el = document.createElement(tag); if (text !== undefined) el.textContent = text; if (className) el.className = className; return el; }
function showError(message) { $('error').textContent = message; $('error').classList.toggle('hidden', !message); }
async function api(path, options) { const res = await fetch(path, options); const data = await res.json(); if (!res.ok) throw new Error(data.error || 'Request failed'); return data; }
function reportName(r) { return r.scope?.context || (r.scope?.files?.length === 1 ? r.scope.files[0].split('/').pop() : `${r.scope?.files?.length || 0} log files`); }

function renderHistory() {
  $('history-count').textContent = state.reports.length;
  $('history').replaceChildren();
  for (const r of state.reports) {
    const b = node('button', undefined, 'history-item' + (r.id === state.selected ? ' active' : ''));
    b.setAttribute('aria-current', r.id === state.selected ? 'true' : 'false');
    b.append(node('strong', reportName(r)), node('small', `${date(r.created_at)} · ${fmt(r.summary?.important)} important`));
    b.addEventListener('click', () => selectReport(r.id).catch(e => showError(e.message)));
    $('history').append(b);
  }
  if (!state.reports.length) $('history').append(node('p', 'Your saved analyses will appear here.', 'muted'));
}

async function selectReport(id) {
  if (state.selected !== id) clearSelection();
  state.watchingLive = false;
  switchView(false);
  const previous = state.selected;
  state.selected = id;
  renderHistory();
  try {
    const report = await api('/api/report?id=' + encodeURIComponent(id));
    if (state.selected !== id) return;
    state.report = report;
    state.page = 0;
    $('search').value = '';
    $('importance').value = 'important';
    $('source-filter').replaceChildren(new Option('All sources', 'all'));
    syncScope('events', LogContext.collected(report), true); syncScope('tail', LogContext.collected(report), true);
    const sources = [...new Set(report.events.map(e => sourceName(e.source)))].sort();
    for (const source of sources) $('source-filter').append(new Option(source, source));
    renderReport();
    showError('');
  } catch (e) { state.selected = previous; renderHistory(); throw e; }
}

function renderReport() {
  syncScope("events", LogContext.collected(state.report));
  syncScope("tail", state.tailPaused ? state.frozenTail : LogContext.collected(state.report));
  const r = state.report, s = r.summary;
  $('report').classList.remove('hidden'); $('empty').classList.add('hidden');
  $('report-title').textContent = r.scope?.context ? 'Kubernetes analysis' : 'File analysis';
  $('report-scope').textContent = r.scope?.context || 'LOG FILES';
  $('report-meta').textContent = `${date(r.created_at)} · ${fmt(s.streams)} sources · ${r.scope?.since ? (r.live ? 'Initial lookback ' : 'Last ') + r.scope.since + ' · ' : ''}${s.elapsed_seconds}s`;
  $('stat-important').textContent = fmt(s.important); $('stat-routine').textContent = fmt(s.routine);
  $('stat-review').textContent = fmt(s.uncertain + s.unknown); $('stat-total').textContent = fmt(s.events);
  $('review-note').textContent = `${fmt(s.uncertain)} uncertain · ${fmt(s.unknown)} unknown`;
  $('total-note').textContent = `${fmt(s.lines)} lines · ${fmt(s.api_requests)} AI requests`;
  $('download').classList.remove('hidden'); $('download').href = '/api/report?id=' + encodeURIComponent(state.selected);
  if (state.watchingLive) { $('download').href = '/api/live'; $('report-title').textContent = 'Live Kubernetes analysis'; }
  renderUsage(r);
  $('gap-count').textContent = fmt(s.coverage_gaps);
  $('mode').textContent = r.mode === 'jev' ? 'Jev semantic analysis' : 'Offline keyword rules';
  $('distribution').replaceChildren();
  for (const key of ['important', 'routine', 'uncertain', 'unknown']) if (s[key]) {
    const segment = node('span', undefined, key); segment.style.flexGrow = String(s[key]); segment.title = `${fmt(s[key])} ${key}`; $('distribution').append(segment);
  }
  $('coverage-list').replaceChildren();
  for (const c of r.coverage) {
    const card = node('div', undefined, 'coverage-card');
    card.append(node('span', c.status, 'badge ' + (c.status === 'ok' ? 'routine' : 'uncertain')));
    const content = node('div'); content.append(node('strong', sourceName(c.source)));
    content.append(node('p', `${c.source?.previous ? 'Previous instance · ' : ''}${fmt(c.events)} events${c.warnings?.length ? ' · ' + c.warnings.join(' · ') : ' · Collected within selected limits'}`));
    card.append(content); $('coverage-list').append(card);
  }
  renderEvents();
  $('tail-tab').classList.toggle('hidden', !r.tail_events);
  if (r.tail_events) renderTail();
}

function openTail() {
  clearSelection();
  state.watchingLive = true; state.selected = null; state.page = 0;
  state.tailPaused = false; state.frozenTail = [];
  $('tail-pause').textContent = 'Pause display'; $('tail-follow').checked = true;
  state.view = 'tail'; switchView(false, true);
}

function renderTail() {
  if (state.view !== 'tail' || !state.report) return;
  const events = state.tailPaused ? state.frozenTail : (state.report.tail_events || []);
  const search = $('tail-search').value.toLowerCase().trim();
  const matches = events.filter(e => LogContext.matches(e, scopeValues("tail")) && (!$('tail-important').checked || e.importance === 'important') && (!search || (e.text + ' ' + sourceName(e.source)).toLowerCase().includes(search)));
  const rows = matches.slice(-300), container = $('tail-lines'), oldScroll = container.scrollTop;
  container.replaceChildren();
  for (const e of rows) {
    const row = node('button', undefined, 'tail-row');
    const status = ['pending','dropped','important','routine','uncertain','unknown'].includes(e.importance) ? e.importance : 'unknown';
    row.append(node('span', e.timestamp?.replace(/^.*T/, '').replace(/Z$/, '') || '—', 'tail-time'), node('span', reviewLabel(e), 'badge '+ status), node('span', sourceName(e.source), 'tail-source'), node('span', e.text, 'tail-text'));
    row.setAttribute('aria-label', 'Inspect ' + status + ' log: ' + e.text.slice(0,100));
    row.addEventListener('click', () => detail(e)); container.append(row);
  }
  if (!rows.length) container.append(node('p', events.length ? 'No log lines match this filter.' : state.report.live?.inventory_error || (state.report.live?.status === 'stopped' ? 'No logs were received in this session. Check collection coverage.' : 'Waiting for Kubernetes log lines…'), 'tail-waiting'));
  $('tail-note').textContent = `${state.tailPaused ? 'Display paused; collection and analysis continue.' : 'Following incoming logs. Pending labels update when Jev finishes.'} Showing ${fmt(rows.length)} of ${fmt(matches.length)} matching buffered events. Cost counters cover the whole session.`;
  container.scrollTop = $('tail-follow').checked && !state.tailPaused ? container.scrollHeight : oldScroll;
}

function renderUsage(report) {
  const u = report.usage, live = report.live;
  $('live-panel').classList.toggle('hidden', !u);
  if (!u) return;
  $('live-status').textContent = live ? human(live.status).toUpperCase() : 'SESSION USAGE';
  $('live-message').textContent = live?.message || 'Cost calculated from provider-reported usage';
  $('live-cost').textContent = '$' + Number(u.estimated_cost_usd).toFixed(8);
  $('live-tokens').textContent = `${fmt(u.input_tokens)} / ${fmt(u.output_tokens)}`;
  $('live-requests').textContent = `${fmt(u.request_attempts)} / ${fmt(u.unmetered_requests)}`;
  $('live-queue').textContent = `${fmt(live?.active_streams)} / ${fmt(live?.queue_depth)}`;
  $('live-cost-note').textContent = `Estimate, not a bill · $${u.input_usd_per_million}/M input + $${u.output_usd_per_million}/M output · ${u.in_flight} requests in flight${u.cost_complete ? '' : ' · Incomplete usage; actual cost may be higher'}`;
  $('live-health').textContent = live ? `${live.events_per_second} events/s · ${fmt(live.dropped)} dropped · ${fmt(live.unfollowed_streams)} containers beyond stream limit · ${fmt(live.reconnects)} reconnects · showing latest ${fmt(live.retained_events)} events; counters cover the full session` : `${fmt(u.metered_requests)} requests with input usage · ${fmt(u.missing_output_usage)} with missing output usage`;
  $('live-health').classList.toggle('warning', Boolean(live?.dropped || live?.unfollowed_streams));
  $('stop-live').classList.toggle('hidden', !state.watchingLive || !live || live.status === 'stopped');
  $('stop-live').disabled = live?.status === 'stopping';
}

async function updateLive() {
  const r = await api('/api/live');
  if (!state.watchingLive) return;
  state.report = r;
  const selected = $('source-filter').value;
  const sources = [...new Set(r.events.map(e => sourceName(e.source)))].sort();
  $('source-filter').replaceChildren(new Option('All sources','all'));
  for (const source of sources) $('source-filter').append(new Option(source,source));
  if (sources.includes(selected)) $('source-filter').value = selected;
  renderReport();
}

function renderEvents() {
  if (!state.report) return;
  const search = $('search').value.toLowerCase().trim(), importance = $('importance').value, source = $('source-filter').value;
  const filtered = state.report.events.filter(e => LogContext.matches(e, scopeValues("events")) && (importance === 'all' || (importance === 'review' ? ['uncertain','unknown'].includes(e.importance) : (['expected','acknowledged'].includes(importance) ? e.review?.status === importance : e.importance === importance))) && (source === 'all' || sourceName(e.source) === source) && (!search || [e.text, sourceName(e.source), e.category, e.severity].join(' ').toLowerCase().includes(search)));
  const pages = Math.max(1, Math.ceil(filtered.length / state.pageSize));
  state.page = Math.min(state.page, pages - 1);
  $('event-rows').replaceChildren();
  state.visibleEvents = filtered.slice(state.page * state.pageSize, (state.page + 1) * state.pageSize);
  for (const e of state.visibleEvents) {
    const tr = node('tr'), judgment = node('td');
    const selectionCell = node('td'), checkbox = document.createElement('input');
    checkbox.type='checkbox'; checkbox.checked=state.selection.has(e.id);
    checkbox.setAttribute('aria-label','Select event: '+e.text.slice(0,100));
    checkbox.addEventListener('change',()=>{selectEvent(e,checkbox.checked);renderEvents();});
    selectionCell.append(checkbox);
    judgment.append(node('span', reviewLabel(e), 'badge ' + (['important','routine','uncertain','unknown'].includes(e.importance) ? e.importance : 'unknown')));
    judgment.append(node('span', e.review ? 'Local review override' : e.importance_confidence == null ? 'Local rules / unknown' : `${Math.round(e.importance_confidence * 100)}% confidence`, 'confidence'));
    const message = node('td'), open = node('button', undefined, 'event-button');
    open.append(node('span', e.text, 'event-text'), node('span', `${human(e.severity)} · ${human(e.category)} · line ${e.line_start}${e.line_end !== e.line_start ? '–' + e.line_end : ''}`, 'event-sub'));
    open.setAttribute('aria-label', 'Inspect event: ' + e.text.slice(0, 100)); open.addEventListener('click', () => detail(e)); message.append(open);
    const sourceCell = node('td'); sourceCell.append(node('span', sourceName(e.source), 'source-name')); if (e.source?.previous) sourceCell.append(node('span', 'previous instance', 'event-sub'));
    const baseline = node('td'); const differs = ['important','routine'].includes(e.importance) && e.baseline?.important !== (e.importance === 'important');
    baseline.append(node('span', e.baseline?.important ? 'Flagged' : 'Not flagged', 'baseline' + (differs ? ' difference' : ''))); if (differs) baseline.append(node('span', 'Disagrees', 'event-sub'));
    tr.append(selectionCell, judgment, message, sourceCell, baseline); $('event-rows').append(tr);
  }
  renderSelection();
  $('no-results').classList.toggle('hidden', filtered.length > 0);
  $('event-count').textContent = `${fmt(filtered.length)} matching events of ${fmt(state.report.events.length)}`;
  $('page-number').textContent = `${state.page + 1} / ${pages}`; $('prev').disabled = state.page === 0; $('next').disabled = state.page === pages - 1;
}

function detail(e) {
  state.detailEvent = e; state.detailReference = eventReference(e); state.detailReport = state.report;
  $('detail-title').textContent = human(e.category) + ' · ' + human(e.severity);
  $('detail-badges').replaceChildren(node('span', reviewLabel(e), 'badge ' + e.importance), node('span', e.truncated ? 'Truncated · review required' : `${e.line_count} log line${e.line_count === 1 ? '' : 's'}`, 'badge'));
  $('detail-meta').replaceChildren();
  const metadata = {Source: sourceName(e.source), Time: e.timestamp || 'Not available', Lines: `${e.line_start}–${e.line_end}`, Confidence: e.importance_confidence == null ? 'Not available' : `${Math.round(e.importance_confidence * 100)}%`, Baseline: e.baseline?.signals?.join(', ') || 'No keyword match'};
  if (e.source?.previous) metadata.Instance = 'Previous container';
  for (const [key,value] of Object.entries(metadata)) $('detail-meta').append(node('dt', key), node('dd', value));
  $('detail-log').textContent = e.text;
  $('detail-explanation').textContent = e.analysis_error || (e.importance === 'pending' ? 'This log has arrived and is waiting for classification.' : state.report.mode === 'jev' ? 'Jev classified this event from its meaning and source. Confidence is a model output, not an independently calibrated probability.' : 'Offline rules match severity levels and keywords. Unmatched events remain uncertain.');
  renderReview(e);
  if (!$('detail-dialog').open) $('detail-dialog').showModal();
}

function switchView(coverage, tail = false) {
  state.view = tail ? 'tail' : coverage ? 'coverage' : 'events';
  $('events-view').classList.toggle('hidden', coverage || tail); $('coverage-view').classList.toggle('hidden', !coverage);
  $('tail-view').classList.toggle('hidden', !tail);
  $('events-tab').classList.toggle('active', !coverage && !tail); $('coverage-tab').classList.toggle('active', coverage); $('tail-tab').classList.toggle('active', tail);
  $('events-tab').setAttribute('aria-pressed', String(!coverage && !tail)); $('coverage-tab').setAttribute('aria-pressed', String(coverage)); $('tail-tab').setAttribute('aria-pressed', String(tail));
  if (tail) renderTail();
}

function formMode() {
  const form = $('scan-form'), files = form.elements.kind.value === 'files', offline = form.elements.analysis_mode.value === 'offline';
  const live = !files && form.elements.live.checked;
  $('live-options').classList.toggle('hidden', files);
  $('live-settings').classList.toggle('hidden', !live);
  $('live-note').classList.toggle('hidden', !live);
  $('kube-fields').querySelector('.field-note').textContent = live ? 'Follows running containers. Initial lookback and line limits apply when a stream first starts; discovery checks for new pods and restarts every 15 seconds.' : 'Includes all containers and available previous logs. Read-only access through your kubectl configuration.';
  $('submit-scan').textContent = live ? 'Start live analysis →' : 'Run analysis →';
  form.elements.tail.min = live ? '0' : '1';
  if (!live && form.elements.tail.value === '0') form.elements.tail.value = '500';
  $('file-fields').classList.toggle('hidden', !files); $('kube-fields').classList.toggle('hidden', files);
  for (const input of $('kube-fields').querySelectorAll('input')) input.disabled = files;
  form.elements.max_batches.disabled = offline;
  $('data-note').textContent = offline ? 'Logs stay on this computer. Keyword rules flag obvious signals; unmatched events remain uncertain.' : 'Redacted log text and source metadata are sent to TypeSafe. Up to 8 events per batch; events beyond the budget stay unknown.';
}

function readFile(file) { return new Promise((resolve,reject) => { const reader = new FileReader(); reader.onerror = () => reject(new Error('Could not read ' + file.name)); reader.onload = () => resolve({name:file.name, data:String(reader.result).split(',')[1]}); reader.readAsDataURL(file); }); }
$('scan-form').addEventListener('submit', async event => {
  event.preventDefault(); const form = event.currentTarget, data = new FormData(form); $('submit-scan').disabled = true; $('form-error').classList.add('hidden');
  try {
    const payload = {kind:data.get('kind'), offline:data.get('analysis_mode') === 'offline', max_batches:Number(form.elements.max_batches.value)};
    payload.live = payload.kind === 'kubernetes' && form.elements.live.checked;
    if (payload.live) { payload.max_cost = Number(form.elements.max_cost.value); payload.max_streams = Number(form.elements.max_streams.value); }
    if (payload.kind === 'files') {
      const files = [...$('files').files];
      if (!files.length || files.length > 20 || files.reduce((n,f) => n+f.size,0) > 10*1024*1024) throw new Error('Choose 1–20 files totaling at most 10 MiB.');
      payload.files = await Promise.all(files.map(readFile));
    } else { for (const key of ['context','namespace','since','selector']) payload[key] = data.get(key); payload.tail = Number(data.get('tail')); }
    await api('/api/analyze', {method:'POST', headers:{'Content-Type':'application/json','X-Jev-Token':state.csrf}, body:JSON.stringify(payload)});
    if (payload.live) { openTail(); $('importance').value = 'all'; }
    $('scan-dialog').close(); await refresh();
  } catch (e) { $('form-error').textContent = e.message; $('form-error').classList.remove('hidden'); }
  finally { $('submit-scan').disabled = false; }
});

async function refresh() {
  if (state.loading) return; state.loading = true;
  try {
    const next = await api('/api/state'), oldJob = state.job;
    state.csrf = next.csrf; state.job = next.job; state.reports = next.reports;
    $('provider').textContent = next.jev_available ? 'Jev key configured' : 'Offline available · no Jev key';
    const job = next.job;
    $('job').classList.toggle('hidden', !job); $('job').classList.toggle('error', job?.status === 'error' || job?.phase === 'reconnecting');
    $('job').textContent = job ? `${job.status === 'running' ? '◌ ' : ''}${job.message}` : '';
    document.querySelectorAll('.new-scan').forEach(b => b.disabled = ['running','stopping'].includes(job?.status));
    $('view-live').classList.toggle('hidden', !job?.live);
    if (job?.live && !oldJob && ['running','stopping'].includes(job.status)) openTail();
    renderHistory();
    if (state.watchingLive) await updateLive();
    else if (job?.report_id && (oldJob?.report_id !== job.report_id)) await selectReport(job.report_id);
    else if (!state.selected && next.reports.length) await selectReport(next.reports[0].id);
    $('empty').classList.toggle('hidden', Boolean(state.report));
    showError('');
  } catch (e) { showError('Dashboard unavailable. ' + e.message); }
  finally { state.loading = false; }
}

document.querySelectorAll('.new-scan').forEach(b => b.addEventListener('click', () => { if (b.dataset.live) { const form = $('scan-form'); form.elements.kind.value = 'kubernetes'; form.elements.live.checked = true; form.elements.tail.value = '0'; form.elements.since.value = '30s'; } $('form-error').classList.add('hidden'); $('scan-dialog').showModal(); formMode(); }));
document.querySelectorAll('.close-dialog').forEach(b => b.addEventListener('click', () => $('scan-dialog').close()));
document.querySelectorAll('.close-detail').forEach(b => b.addEventListener('click', () => $('detail-dialog').close()));
document.querySelectorAll('[name="kind"], [name="analysis_mode"], [name="live"]').forEach(el => el.addEventListener('change', formMode));
document.querySelectorAll('[data-filter]').forEach(b => b.addEventListener('click', () => { $('importance').value = b.dataset.filter; state.page = 0; switchView(false); renderEvents(); }));
for (const id of ['search','importance','source-filter']) $(id).addEventListener('input', () => { state.page = 0; renderEvents(); });
$('events-tab').addEventListener('click', () => switchView(false)); $('coverage-tab').addEventListener('click', () => switchView(true));
$('prev').addEventListener('click', () => { state.page--; renderEvents(); }); $('next').addEventListener('click', () => { state.page++; renderEvents(); });
$('reset-filters').addEventListener('click', () => { $('search').value = ''; $('importance').value = 'all'; $('source-filter').value = 'all'; syncScope('events',LogContext.collected(state.report),true); state.page = 0; renderEvents(); });
$('view-live').addEventListener('click', () => { openTail(); updateLive().catch(e => showError(e.message)); });
$('tail-tab').addEventListener('click', () => switchView(false, true));
$('tail-pause').addEventListener('click', () => { state.tailPaused = !state.tailPaused; if (state.tailPaused) state.frozenTail = state.report?.tail_events || []; $('tail-pause').textContent = state.tailPaused ? 'Resume display' : 'Pause display'; renderTail(); });
for (const id of ['tail-search','tail-important','tail-follow']) $(id).addEventListener('input', renderTail);
$('tail-lines').addEventListener('scroll', () => { const c = $('tail-lines'); if (c.scrollHeight - c.scrollTop - c.clientHeight > 50) $('tail-follow').checked = false; });
$('stop-live').addEventListener('click', async () => { $('stop-live').disabled = true; try { await api('/api/live/stop', {method:'POST', headers:{'Content-Type':'application/json','X-Jev-Token':state.csrf}, body:'{}'}); await refresh(); } catch (e) { showError(e.message); $('stop-live').disabled = false; } });
function scopeValues(prefix) {
  return Object.fromEntries(['namespace','pod','container'].map(k => [k, $(prefix+'-'+k).value]));
}
function syncScope(prefix, events, reset = false) {
  let available = events;
  for (const [key,label] of [['namespace','namespaces'],['pod','pods'],['container','containers']]) {
    const select = $(prefix+'-'+key), old = reset ? '' : select.value;
    const values = [...new Set(available.map(e => e.source?.[key]).filter(Boolean))].sort();
    select.replaceChildren(new Option('All '+label, ''));
    for (const value of values) select.append(new Option(value, value));
    if (values.includes(old)) select.value = old;
    if (select.value) available = available.filter(e => e.source?.[key] === select.value);
  }
}
function eventReference(e) {
  return {event_id:e.id, ...(state.watchingLive ? {live_id:state.job?.id} : {report_id:state.selected})};
}
function openContext() {
  const e = state.detailEvent;
  if (!e) return;
  state.context = {anchor:e, events:LogContext.collected(state.detailReport), reference:state.detailReference, fetched:null, warnings:[], request:0};
  if (!state.context.events.some(item=>item.id===e.id)) state.context.events.push(e);
  $('detail-dialog').close();
  $('context-source').textContent = sourceName(e.source) + ' · ' + (e.timestamp || `lines ${e.line_start}–${e.line_end}`);
  $('context-anchor').textContent = e.text;
  $('context-search').value = '';
  $('context-window').value = '120';
  const timed = e.source?.type === 'kubernetes' && Number.isFinite(Date.parse(e.timestamp));
  $('context-window-label').textContent = timed ? 'Time either side' : 'Lines either side';
  for (const option of $('context-window').options) option.textContent = timed ? ({30:'30 seconds',120:'2 minutes',300:'5 minutes',900:'15 minutes'}[option.value]) : option.value+' lines';
  $('context-fetch').classList.toggle('hidden', !timed);
  $('context-fetch').disabled = false;
  $('context-fetch').textContent = 'Fetch from Kubernetes';
  $('context-error').classList.add('hidden');
  syncScope('context', state.context.events, true);
  for (const key of ['namespace','pod','container']) {
    $("context-"+key).value = e.source?.[key] || '';
    syncScope('context', state.context.events);
  }
  $('context-dialog').showModal();
  renderContext(true);
}
function renderContext(jump = false) {
  const c = state.context;
  if (!c) return;
  const search = $('context-search').value.toLowerCase().trim(), filters = scopeValues('context');
  const all = LogContext.withFetched(c.events, c.fetched, c.anchor);
  const nearby = LogContext.nearby(all, c.anchor, Number($('context-window').value));
  const matching = nearby.filter(e => LogContext.matches(e,filters) && (!search || e.text.toLowerCase().includes(search)));
  // Keep the visible window centered on the selected event when a busy source reaches the display cap.
  const anchorIndex = matching.findIndex(e => e.id === c.anchor.id);
  const start = Math.max(0, anchorIndex < 0 ? 0 : anchorIndex-250), rows = matching.slice(start,start+500);
  $('context-lines').replaceChildren();
  for (const e of rows) {
    const selected = e.id === c.anchor.id;
    const row = node('div', undefined, 'context-row' + (selected ? ' selected' : ''));
    if (selected) { row.id = 'context-selected'; row.setAttribute('aria-current','true'); }
    const meta = node('div', undefined, 'context-row-meta');
    meta.append(node('span', selected ? 'SELECTED EVENT' : e.context_only ? 'CONTEXT · NOT ANALYZED' : human(e.review?.status || e.importance)), node('span', e.timestamp || `lines ${e.line_start}–${e.line_end}`), node('span',sourceName(e.source)));
    row.append(meta, node('pre', e.text)); $('context-lines').append(row);
  }
  if (!rows.length) $('context-lines').append(node('p', 'No surrounding logs match these filters.', 'tail-waiting'));
  $('context-note').textContent = `${c.fetched ? 'Fetched context for the selected container; other sources use collected events.' : 'Frozen view of collected events; older lines may have left the live buffer.'} Showing ${fmt(rows.length)} of ${fmt(matching.length)} matching events.${anchorIndex < 0 ? ' The selected event is outside these filters and remains pinned above.' : ''} ${c.warnings.join(' ')}`;
  if (jump) $('context-selected')?.scrollIntoView({block:'center'});
}
async function fetchContext() {
  const c = state.context, request = ++c.request;
  $('context-fetch').disabled = true; $('context-fetch').textContent = 'Fetching context…';
  $('context-error').classList.add('hidden');
  try {
    const result = await api('/api/context', {method:'POST',headers:{'Content-Type':'application/json','X-Jev-Token':state.csrf},body:JSON.stringify({...c.reference,window_seconds:Number($('context-window').value)})});
    if (state.context !== c || c.request !== request) return;
    c.fetched = result.events; c.warnings = result.warnings;
    if (!result.events.some(e => e.timestamp === c.anchor.timestamp && e.text === c.anchor.text)) c.warnings.push('The original selected event was not found in the fetched logs; it remains pinned from the report.');
    renderContext(true);
  } catch (e) {
    if (state.context === c && c.request === request) { $('context-error').textContent=e.message; $('context-error').classList.remove('hidden'); }
  } finally {
    if (state.context === c && c.request === request) { $('context-fetch').disabled=false; $('context-fetch').textContent='Fetch from Kubernetes'; }
  }
}
$('view-context').addEventListener('click', openContext);
$('close-context').addEventListener('click', () => $('context-dialog').close());
$('context-fetch').addEventListener('click', fetchContext);
$('context-jump').addEventListener('click', () => {
  $('context-search').value='';
  syncScope('context',state.context.events,true);
  for (const key of ['namespace','pod','container']) { $('context-'+key).value=state.context.anchor.source?.[key] || ''; syncScope('context',state.context.events); }
  renderContext(true);
});
$('context-search').addEventListener('input', () => renderContext());
$('context-window').addEventListener('change', () => {
  state.context.request++; state.context.fetched=null; state.context.warnings=[];
  $('context-fetch').disabled=false; $('context-fetch').textContent='Fetch from Kubernetes';
  $('context-error').classList.add('hidden'); renderContext(true);
});
for (const prefix of ['events','tail','context']) for (const key of ['namespace','pod','container']) $(prefix+'-'+key).addEventListener('change', () => {
  const events = prefix === 'context' ? state.context.events : prefix === 'tail' && state.tailPaused ? state.frozenTail : LogContext.collected(state.report);
  syncScope(prefix, events);
  if (prefix === 'context') renderContext(); else if (prefix === 'tail') renderTail(); else { state.page=0; renderEvents(); }
});


function renderReview(e) {
  $('review-error').classList.add('hidden'); $('expected-editor').classList.add('hidden');
  $('ack-event').textContent = e.review?.status === 'acknowledged' ? 'Undo acknowledgment' : 'Acknowledge';
  $('ack-event').disabled = e.review?.status === 'expected';
  const original = e.original_judgment;
  $('review-decision-note').textContent = e.review ? `${human(e.review.status)} locally. ${original ? 'Original judgment: '+human(original.importance)+(original.importance_confidence == null ? '' : ' · '+Math.round(original.importance_confidence*100)+'% confidence')+'.' : ''}` : 'Review this event once, or save a scoped pattern for future expected messages.';
}
async function reviewAction(payload) {
  await api('/api/review',{method:'POST',headers:{'Content-Type':'application/json','X-Jev-Token':state.csrf},body:JSON.stringify(payload)});
}
async function updateReviewedDetail() {
  const reference = state.detailReference, id = state.detailEvent.id;
  const report = await api(reference.live_id ? '/api/live' : '/api/report?id='+encodeURIComponent(reference.report_id));
  if ((reference.live_id && state.watchingLive && state.job?.id === reference.live_id) || (!reference.live_id && state.selected === reference.report_id)) { state.report=report; renderReport(); }
  const updated=LogContext.collected(report).find(e=>e.id===id);
  if (updated) { const saved=state.detailReference; detail(updated); state.detailReference=saved; }
}
$('ack-event').addEventListener('click',async()=>{
  const button=$('ack-event'); button.disabled=true;
  try { await reviewAction({...state.detailReference,action:state.detailEvent.review?.status==='acknowledged'?'unacknowledge':'acknowledge'}); await updateReviewedDetail(); }
  catch(e){$('review-error').textContent=e.message;$('review-error').classList.remove('hidden');}
  finally{button.disabled=state.detailEvent.review?.status==='expected';}
});
$('expect-event').addEventListener('click',()=>{
  const text=state.detailEvent.text;
  let pattern=text.split('\n')[0].split(/\s+\{/)[0].replace(/^\d{4}-\d\d-\d\d[ T][\d:.]+(?:Z|[+-]\d\d:\d\d)?\s+/, '').replace(/^(?:info|warn|warning|error|debug|fatal)[:\s]+/i,'').trim();
  $('expected-pattern').value=(pattern.length>=8?pattern:text.slice(0,500)).slice(0,500);
  $('expected-scope').disabled=state.detailEvent.source?.type!=='kubernetes';
  $('expected-editor').classList.remove('hidden');$('expected-pattern').focus();
});
$('save-expected').addEventListener('click',async()=>{
  $('save-expected').disabled=true;
  try{await reviewAction({...state.detailReference,action:'expected',pattern:$('expected-pattern').value,scope:$('expected-scope').value});await updateReviewedDetail();}
  catch(e){$('review-error').textContent=e.message;$('review-error').classList.remove('hidden');}
  finally{$('save-expected').disabled=false;}
});
async function renderRules() {
  $('rules-error').classList.add('hidden');
  try {
    const rules=await api('/api/rules');$('expected-rules').replaceChildren();$('baseline-rules').replaceChildren();
    if(!rules.expected.length)$('expected-rules').append(node('p','No expected-event rules yet. Open a log event and choose Mark as expected.','field-note'));
    for(const r of rules.expected){
      const card=node('div',undefined,'rule-card'),label=node('label',undefined,'rule-toggle'),toggle=document.createElement('input');toggle.type='checkbox';toggle.checked=r.enabled;
      label.append(toggle,node('span',r.enabled?'Enabled':'Disabled'));card.append(label,node('pre',r.pattern),node('p',Object.entries(r.scope).filter(([k])=>k!=='type').map(([k,v])=>`${k}: ${v}`).join(' · '),'field-note'));
      toggle.addEventListener('change',async()=>{toggle.disabled=true;try{await reviewAction({action:'toggle',rule_id:r.id,enabled:toggle.checked});await renderRules();if(state.watchingLive)await updateLive();else if(state.selected)await selectReport(state.selected);}catch(e){$('rules-error').textContent=e.message;$('rules-error').classList.remove('hidden');toggle.checked=!toggle.checked;}finally{toggle.disabled=false;}});
      $('expected-rules').append(card);
    }
    for(const r of rules.baseline){const card=node('div',undefined,'rule-card');card.append(node('strong',human(r.id)),node('p',r.description,'field-note'),node('pre',r.pattern));$('baseline-rules').append(card);}
  }catch(e){$('rules-error').textContent=e.message;$('rules-error').classList.remove('hidden');}
}
$('open-rules').addEventListener('click',()=>{$('rules-dialog').showModal();renderRules();});
$('close-rules').addEventListener('click',()=> $('rules-dialog').close());
function setTheme(theme) {
  const light=theme==='light';document.documentElement.dataset.theme=light?'light':'dark';
  $('theme-toggle').textContent=light?'☾ Dark mode':'☀ Light mode';$('theme-toggle').setAttribute('aria-pressed',String(light));
  try{localStorage.setItem('jevernetes-theme',light?'light':'dark');}catch{}
}
let initialTheme='dark';try{initialTheme=localStorage.getItem('jevernetes-theme')||'dark';}catch{}
setTheme(initialTheme);
$('theme-toggle').addEventListener('click',()=>setTheme(document.documentElement.dataset.theme==='light'?'dark':'light'));


function clearSelection() {
  state.selection.clear(); state.visibleEvents=[];
  $('selection-feedback').textContent='';renderSelection();
}
function selectEvent(e,selected) {
  if (!selected) {state.selection.delete(e.id);return;}
  if(state.selection.size>=InvestigationPrompt.maxEvents && !state.selection.has(e.id)) {$('selection-feedback').textContent=`Select at most ${InvestigationPrompt.maxEvents} events.`;return;}
  state.selection.set(e.id,JSON.parse(JSON.stringify(e)));
}
function renderSelection() {
  const count=state.selection.size;
  $('selection-bar').classList.toggle('hidden',!count);
  $('selection-count').textContent=`${count} selected (including other pages or filters)`;
  const selected=state.visibleEvents.filter(e=>state.selection.has(e.id)).length;
  $('select-page').checked=Boolean(state.visibleEvents.length) && selected===state.visibleEvents.length;
  $('select-page').indeterminate=selected>0 && selected<state.visibleEvents.length;
  $('select-page').disabled=!state.visibleEvents.length;
}
function previewPrompt(text) {
  $('prompt-text').value=text; $('prompt-copy-status').textContent='';
  if(!$('prompt-dialog').open)$('prompt-dialog').showModal();
}
async function copyPrompt(text) {
  try {
    if(!navigator.clipboard?.writeText)throw new Error('Clipboard unavailable');
    await navigator.clipboard.writeText(text);
    $('selection-feedback').textContent='Investigation prompt copied. Paste it into your coding agent.';
    $('prompt-copy-status').textContent='Copied to clipboard.';
  } catch {
    previewPrompt(text);$('prompt-text').focus();$('prompt-text').select();
    $('prompt-copy-status').textContent='Clipboard access is unavailable. Press Ctrl+C or Cmd+C to copy the selected prompt.';
  }
}
function selectedPrompt() {return InvestigationPrompt.build([...state.selection.values()]);}
$('select-page').addEventListener('change',()=>{for(const e of state.visibleEvents)selectEvent(e,$('select-page').checked);renderEvents();});
$('clear-selection').addEventListener('click',()=>{clearSelection();renderEvents();});
$('preview-prompt').addEventListener('click',()=>{try{previewPrompt(selectedPrompt());}catch(e){$('selection-feedback').textContent=e.message;}});
$('copy-prompt').addEventListener('click',()=>{try{copyPrompt(selectedPrompt());}catch(e){$('selection-feedback').textContent=e.message;}});
$('copy-preview').addEventListener('click',()=>copyPrompt($('prompt-text').value));
$('close-prompt').addEventListener('click',()=>$('prompt-dialog').close());

refresh(); setInterval(refresh, 1000);
