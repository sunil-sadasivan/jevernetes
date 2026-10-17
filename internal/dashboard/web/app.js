'use strict';
(() => {
  const $ = id => document.getElementById(id);
  let epoch = 0, timer, request, selected = '', events = [], interval = 5000, enabled = false;
  const text = (id, value) => { $(id).textContent = value; };
  const active = () => enabled && !document.hidden && $('controller').open;
  async function get(path, signal) {
    const response = await fetch((typeof location!=="undefined" && location.pathname.startsWith("/controller/") ? "/controller" : "") + path, {signal, cache: 'no-store', credentials: 'same-origin'});
    if (!response.ok) throw new Error('Sample unavailable');
    return response.json();
  }
  function cancel() { epoch++; clearTimeout(timer); if (request) request.abort(); request = null; }
  async function poll() {
    cancel(); if (!active()) return;
    const generation = epoch; request = new AbortController();
    try {
      const value = await get('/api/controller/status', request.signal);
      if (generation !== epoch) return;
      const state = value.controller;
      text('connection', `Sample ${new Date(value.sampled_at * 1000).toLocaleTimeString()} · ${state.ready ? 'collecting' : 'collection stopped / not ready'}`);
      text('metrics', JSON.stringify(state.metrics, null, 2));
      $('incidents').replaceChildren();
      for (const incident of state.recent_incidents || []) {
        if (!/^[a-f0-9]{64}$/i.test(incident.id)) continue;
        const button = document.createElement('button');
        button.textContent = `${incident.id.slice(0, 12)} · ${incident.last_decision || 'review'} · ${incident.recurrence_count} occurrences`;
        button.addEventListener('click', () => { selected = incident.id; poll(); });
        $('incidents').append(button);
      }
      if (selected) {
        const detail = await get(`/api/controller/incidents/${selected}`, request.signal);
        if (generation !== epoch) return;
        text('detail', JSON.stringify(detail.controller, null, 2));
      }
    } catch (error) {
      if (generation !== epoch) return;
      text('connection', 'Disconnected — prior counters are stale.');
      text('detail', 'Detail unavailable.');
    } finally {
      if (generation === epoch && active()) timer = setTimeout(poll, interval);
    }
  }
  let reportEpoch = 0, reportRequest, nextOffset = null;
  async function reportPage(offset = 0) {
    const generation = ++reportEpoch;
    if (reportRequest) reportRequest.abort();
    reportRequest = new AbortController();
    $('next').disabled = true;
    const query = $('filter').value;
    const path = offset || query ? `/api/report?offset=${offset}&q=${encodeURIComponent(query)}` : '/api/report';
    try {
      const report = await get(path, reportRequest.signal);
      if (generation !== reportEpoch) return;
      events = report.events || [];
      text('summary', JSON.stringify(report.summary));
      $('events').replaceChildren();
      for (const event of events) {
        const row = document.createElement('pre'); row.textContent = `[${event.importance}] ${event.text}`;
        $('events').append(row);
      }
      const page = report.page;
      nextOffset = page.next_offset;
      $('next').disabled = nextOffset === null;
      $('previous').disabled = offset === 0;
      text('page-status', `${offset + (events.length ? 1 : 0)}–${offset + events.length} of ${page.matched_events} matches in ${page.retained_events} retained events. Coverage ${page.coverage_complete ? 'complete within report window' : 'partial or unverified'}. Unavailable on this page: ${page.unavailable_events}.`);
    } catch (error) {
      if (generation !== reportEpoch) return;
      $('events').replaceChildren(); text('page-status', 'Report page unavailable. No completeness claim.');
    }
  }
  $('filter').addEventListener('input', () => reportPage());
  $('previous').addEventListener('click', () => reportPage());
  $('next').addEventListener('click', () => { if (nextOffset !== null) reportPage(nextOffset); });
  $('controller').addEventListener('toggle', () => active() ? poll() : cancel());
  document.addEventListener('visibilitychange', () => active() ? poll() : cancel());
  window.addEventListener('pagehide', cancel);
  reportPage();
  get('/api/controller/config').then(config => {
    if (!config.enabled) { text('connection', 'Controller inspection is not configured.'); return; }
    enabled = true; interval = Math.max(2000, config.interval_seconds * 1000); poll();
  }).catch(() => text('connection', 'Controller configuration unavailable.'));
})();
