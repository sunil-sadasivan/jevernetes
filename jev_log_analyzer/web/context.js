'use strict';
// Pure helpers shared by the context panel and its synthetic checks.
const LogContext = (() => {
  const instance = s => JSON.stringify([s?.type, s?.context, s?.namespace, s?.pod, s?.container, s?.path, s?.pod_uid, s?.restart_count, Boolean(s?.previous)]);
  const sameInstance = (a, b) => instance(a) === instance(b);
  function collected(report) {
    const events = new Map();
    for (const e of [...(report?.tail_events || []), ...(report?.events || [])]) events.set(e.id, e);
    return [...events.values()];
  }
  function nearby(events, anchor, radius) {
    if (anchor.source?.type !== 'kubernetes') return events.filter(e => sameInstance(e.source, anchor.source) && e.line_end >= anchor.line_start - radius && e.line_start <= anchor.line_end + radius).sort((a,b) => a.line_start - b.line_start);
    const at = Date.parse(anchor.timestamp);
    if (!Number.isFinite(at)) return events.filter(e => sameInstance(e.source, anchor.source) && e.line_end >= anchor.line_start - radius && e.line_start <= anchor.line_end + radius).sort((a,b) => a.line_start - b.line_start);
    return events.filter(e => Math.abs(Date.parse(e.timestamp) - at) <= radius * 1000).sort((a,b) => Date.parse(a.timestamp) - Date.parse(b.timestamp) || (a.sequence || a.line_start) - (b.sequence || b.line_start));
  }
  function matches(e, filters) {
    return ['namespace','pod','container'].every(key => !filters[key] || e.source?.[key] === filters[key]);
  }
  function withFetched(events, fetched, anchor) {
    if (!fetched) return events;
    const result = events.filter(e => !sameInstance(e.source, anchor.source));
    let found = false;
    for (const e of fetched) {
      if (!found && e.timestamp === anchor.timestamp && e.text === anchor.text) { result.push(anchor); found = true; }
      else result.push(e);
    }
    if (!found) result.push(anchor);
    return result;
  }
  return {sameInstance, collected, nearby, matches, withFetched};
})();
