// Record the real dashboard with synthetic API responses. No cluster, keys, or reports are read.
// Requires Node 22+, Chrome/Chromium, and ffmpeg. Run: node tools/record-demo.mjs
// Set CHROME_BIN if Chrome is not at the default macOS location.
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {readFile, writeFile, mkdir, mkdtemp, rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join, resolve, dirname} from 'node:path';
import {fileURLToPath} from 'node:url';
import {spawn} from 'node:child_process';
import {once} from 'node:events';
import {launchDemoBrowser} from './demo-browser.mjs';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const output = process.env.DEMO_OUTPUT || join(root, 'docs/images/live-demo.gif');
const temp = await mkdtemp(join(tmpdir(), 'jevernetes-demo-'));
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const samples = [
  ['api', 'routine', 'info', 'request', 'GET /health 200 duration=3ms'],
  ['worker', 'routine', 'info', 'job', 'Job completed queue=notifications duration=42ms'],
  ['api', 'routine', 'info', 'request', 'GET /catalog 200 duration=18ms'],
  ['api', 'important', 'error', 'database', 'Database pool exhausted: active=20 max=20 waiting=48'],
  ['api', 'important', 'error', 'request', 'GET /orders 503: timed out waiting for a database connection'],
  ['worker', 'uncertain', 'info', 'data', 'Optional cache entry not found during warmup'],
  ['api', 'routine', 'info', 'request', 'GET /health 200 duration=2ms'],
  ['worker', 'routine', 'info', 'job', 'Scheduled cleanup completed: removed=12 expired entries'],
  ['api', 'routine', 'info', 'request', 'GET /catalog 200 duration=21ms'],
  ['worker', 'routine', 'info', 'job', 'Job completed queue=notifications duration=37ms'],
  ['worker', 'uncertain', 'info', 'data', '}'],
];
let count = 3;
const reviews = new Map();
function report() {
  const events = samples.slice(0, count).map(([service, importance, severity, category, text], i) => ({
    id: `demo-${i}`, text, importance, severity, category,
    importance_confidence: importance === 'uncertain' ? 0.58 : 0.97,
    timestamp: `2026-01-15T12:00:${String(i).padStart(2, '0')}Z`,
    line_start: i + 1, line_end: i + 1, line_count: 1, truncated: false,
    source: {type: 'kubernetes', context: 'demo-cluster', namespace: 'demo',
      pod: `${service}-7b9d-demo`, container: service, previous: false},
    baseline: {important: severity === 'error', signals: severity === 'error' ? ['error'] : []},
  }));
  for (const event of events) {
    if (reviews.has(event.id)) {
      event.original_judgment = {importance: event.importance, severity: event.severity, category: event.category};
      event.review = {status: reviews.get(event.id)};
      event.importance = 'routine';
    }
  }
  const requests = Math.ceil(count / 2), tokens = requests * 640;
  return {
    created_at: '2026-01-15T12:00:00Z', mode: 'jev', scope: {context: 'demo-cluster', since: '30s'},
    events, tail_events: events.map((e, i) => ({...e, sequence: i + 1})), coverage: [],
    summary: {streams: 2, elapsed_seconds: count, events: count, lines: count,
      important: events.filter(e => e.importance === 'important').length,
      routine: events.filter(e => e.importance === 'routine').length,
      uncertain: events.filter(e => e.importance === 'uncertain').length,
      unknown: 0, api_requests: requests, coverage_gaps: 0},
    live: {status: 'running', message: 'Following 2 containers across the demo namespace',
      active_streams: 2, queue_depth: 0, events_per_second: 2, dropped: 0,
      unfollowed_streams: 0, reconnects: 0, retained_events: count},
    usage: {estimated_cost_usd: tokens * 0.042 / 1000000, input_tokens: tokens,
      output_tokens: requests * 40, request_attempts: requests, unmetered_requests: 0,
      input_usd_per_million: 0.042, output_usd_per_million: 0, in_flight: 0, cost_complete: true},
  };
}
const assets = {'/': ['index.html', 'text/html'], '/style.css': ['style.css', 'text/css'],
  '/app.js': ['app.js', 'text/javascript'], '/context.js': ['context.js', 'text/javascript'],
  '/prompt.js': ['prompt.js', 'text/javascript'], '/search.js': ['search.js', 'text/javascript'], '/favicon.svg': ['favicon.svg', 'image/svg+xml']};
const server = createServer(async (req, res) => {
  try {
    const path = new URL(req.url, 'http://localhost').pathname;
    if (path === '/api/state' || path === '/api/live') {
      res.setHeader('Content-Type', 'application/json');
      res.end(JSON.stringify(path === '/api/live' ? report() : {csrf: 'synthetic-demo',
        jev_available: true, reports: [], job: {id: 'demo-live', live: true,
          status: 'running', message: 'Live analysis · following new Kubernetes logs'}}));
    } else if (path === '/api/review' && req.method === 'POST') {
      let body = '';
      for await (const chunk of req) body += chunk;
      const payload = JSON.parse(body);
      assert.equal(payload.live_id, 'demo-live');
      assert(['acknowledge', 'expected'].includes(payload.action));
      assert.equal(payload.events.length, 2);
      for (const entry of payload.events) {
        const event = report().events.find(e => e.id === entry.event_id);
        assert(event);
        if (payload.action === 'expected') assert(event.text.includes(entry.pattern));
        reviews.set(entry.event_id, payload.action === 'expected' ? 'expected' : 'acknowledged');
      }
      res.setHeader('Content-Type', 'application/json');
      res.end(JSON.stringify({status: 'saved'}));
    } else if (assets[path]) {
      res.setHeader('Content-Type', assets[path][1]);
      res.end(await readFile(join(root, 'jevernetes/web', assets[path][0])));
    } else { res.writeHead(404); res.end(); }
  } catch { res.writeHead(500); res.end(); }
});
let browser;
try {
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  const origin = `http://127.0.0.1:${server.address().port}`;
  browser = await launchDemoBrowser(join(temp, 'profile'));
  const {cdp, evaluate, call} = browser;
  await cdp('Page.enable');
  await cdp('Emulation.setDeviceMetricsOverride', {width: 1440, height: 1080, deviceScaleFactor: 1, mobile: false});
  await cdp('Emulation.setTimezoneOverride', {timezoneId: 'UTC'});
  await cdp('Browser.grantPermissions', {origin, permissions: ['clipboardReadWrite', 'clipboardSanitizedWrite']});
  // Prevent external requests even if a future dashboard asset gains one.
  browser.onEvent(message => {
    if (message.method === 'Fetch.requestPaused') {
      const {requestId, request} = message.params;
      void cdp(request.url.startsWith(origin + '/') ? 'Fetch.continueRequest' : 'Fetch.failRequest',
        {requestId, ...(request.url.startsWith(origin + '/') ? {} : {errorReason: 'BlockedByClient'})});
    }
  });
  await cdp('Fetch.enable', {patterns: [{urlPattern: '*'}]});
  await cdp('Page.navigate', {url: origin});
  for (let i = 0; i < 50; i++) {
    if (await evaluate("document.querySelectorAll('.tail-row').length === 3")) break;
    await pause(100);
  }
  assert.equal(await evaluate("document.querySelectorAll('.tail-row').length"), 3);
  assert.equal(await evaluate("document.querySelector('link[rel=icon]').getAttribute('href')"), '/favicon.svg');
  assert.equal(await evaluate("fetch('/favicon.svg').then(r=>r.headers.get('Content-Type'))"), 'image/svg+xml');
  await evaluate(`{
    const style = document.createElement('style');
    style.textContent = 'body{padding-bottom:320px}#demo-caption{position:fixed;inset:auto 0 0;z-index:2147483647;background:#17131ff5;border-top:1px solid #6c548b;padding:18px 28px;display:flex;align-items:center;justify-content:space-between;font:600 20px/1.4 system-ui;color:#f1f2f5;pointer-events:none}#demo-caption small{font-size:12px;color:#b8a0ff;letter-spacing:1px}#demo-pointer{position:fixed;width:30px;height:30px;border:3px solid #d4c3ff;background:#b8a0ff44;border-radius:50%;z-index:2147483647;pointer-events:none;display:none}';
    document.head.append(style);
    const caption = document.createElement('div'); caption.id='demo-caption';
    caption.innerHTML='<span></span><small>SYNTHETIC DEMO · SIMULATED LOGS + COST</small>';
    document.body.append(caption);
    const pointer=document.createElement('div');pointer.id='demo-pointer';document.body.append(pointer);
  }`);
  const frames = [], texts = [];
  async function frame(label, duration = 1) {
    await call(label => {const caption=document.getElementById('demo-caption');(document.querySelector('dialog[open]')||document.body).append(caption);caption.querySelector('span').textContent=label;}, label);
    await pause(80);
    texts.push(await evaluate('document.body.innerText'));
    const {data} = await cdp('Page.captureScreenshot', {format: 'png', captureBeyondViewport: false});
    const name = `frame-${String(frames.length).padStart(3, '0')}.png`;
    await writeFile(join(temp, name), Buffer.from(data, 'base64'));
    frames.push({name, duration});
  }
  async function click(selector) {
    const p = await call(selector => {const e=document.querySelector(selector);e.scrollIntoView({block:'nearest'});const r=e.getBoundingClientRect();return {x:r.x+r.width/2,y:r.y+r.height/2};}, selector);
    await call(({x,y}) => Object.assign(document.getElementById('demo-pointer').style,{display:'block',left:(x-15)+'px',top:(y-15)+'px'}), p);
    await cdp('Input.dispatchMouseEvent', {type: 'mousePressed', ...p, button: 'left', clickCount: 1});
    await cdp('Input.dispatchMouseEvent', {type: 'mouseReleased', ...p, button: 'left', clickCount: 1});
    await call(selector => {const r=document.querySelector(selector).getBoundingClientRect();Object.assign(document.getElementById('demo-pointer').style,{left:(r.x+r.width/2-15)+'px',top:(r.y+r.height/2-15)+'px'});}, selector);
  }
  const hidePointer = () => evaluate("document.getElementById('demo-pointer').style.display='none'");
  await frame('01  Follow Kubernetes logs. Watch usage as events arrive.', 2);
  for (count = 4; count <= samples.length; count++) {
    await evaluate('updateLive()');
    await frame('01  Live tail surfaces the events worth investigating.', 0.8);
  }
  count = samples.length;
  await hidePointer();
  await frame('02  Database errors stand out among routine activity.', 2);
  await click('[data-filter="important"]');
  await frame('02  Open Important to focus your investigation.', 1.5);
  await hidePointer();
  await evaluate('window.scrollTo(0,0)');
  await click('#event-rows tr:nth-child(1) input');
  await frame('03  Select the database error…', 1.4);
  await click('#event-rows tr:nth-child(2) input');
  await frame('03  …and the related request failure.', 1.8);
  assert.equal(await evaluate('state.selection.size'), 2);
  await click('#copy-prompt');
  await pause(150);
  assert.match(await evaluate("document.getElementById('selection-feedback').textContent"), /copied/);
  const clipboard = await evaluate('navigator.clipboard.readText()');
  assert(clipboard.includes(samples[3][4]) && clipboard.includes(samples[4][4]));
  assert(!clipboard.includes(samples[0][4]));
  await frame('04  Copy a ready-to-paste investigation prompt.', 2.4);
  await click('#preview-prompt');
  await hidePointer();
  await frame('05  Paste into Claude, Codex, or your coding agent.', 3);
  await evaluate("document.getElementById('prompt-text').scrollTop=570");
  await frame('05  Selected evidence includes pod, timestamp, and judgment.', 2.5);
  await click('#close-prompt');
  await hidePointer();
  await click('#ack-selected');
  for (let i = 0; i < 50 && await evaluate('Boolean(state.reviewBusy)'); i++) await pause(100);
  assert.equal(await evaluate("state.report.events.filter(e=>e.review?.status==='acknowledged').length"), 2);
  await hidePointer();
  await frame('06  Acknowledge reviewed events together.', 2);
  await click('[data-filter="review"]');
  await click('#select-page');
  assert.equal(await evaluate('state.selection.size'), 2);
  await frame('07  Select expected activity in Needs review.', 1.8);
  await click('#expect-selected');
  await hidePointer();
  assert.equal(await evaluate("document.querySelectorAll('#bulk-expected-items input').length"), 2);
  assert.equal(await evaluate("document.querySelectorAll('#bulk-expected-items input')[1].value"), '}');
  await frame('07  Review scoped rules, including standalone log fragments.', 3);
  await click('#save-bulk-expected');
  for (let i = 0; i < 50 && await evaluate('Boolean(state.reviewBusy)'); i++) await pause(100);
  assert.equal(await evaluate("state.report.events.filter(e=>e.review?.status==='expected').length"), 2);
  await hidePointer();
  await click('#tail-tab');
  await hidePointer();
  await frame('Live logs. Focused investigations. Less repeat noise.  jevernetes', 3);
  await writeFile(join(temp, 'frames.txt'), frames.map(f => `file '${f.name}'\nduration ${f.duration}\n`).join('') + `file '${frames.at(-1).name}'\n`);
  // Preserve text from every captured frame for privacy review without OCR guessing.
  await mkdir(dirname(output), {recursive: true});
  await writeFile(join(temp, 'visible-text.json'), JSON.stringify({frames: texts, clipboard}, null, 2));
  const ffmpeg = spawn('ffmpeg', ['-hide_banner', '-loglevel', 'error', '-y', '-f', 'concat', '-safe', '0',
    '-i', join(temp, 'frames.txt'), '-filter_complex',
    '[0:v]fps=10,scale=1280:-1:flags=lanczos,split[a][b];[a]palettegen=stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=3',
    '-loop', '0', output], {stdio: 'inherit'});
  assert.equal((await once(ffmpeg, 'exit'))[0], 0, 'ffmpeg failed');
  console.log(`Created ${output}\nReview frames and visible-text.json in ${temp}`);
} finally {
  await browser?.close(); server.close();
  // Keep screenshots for visual/privacy review; discard the isolated browser profile.
  await rm(join(temp, 'profile'), {recursive: true, force: true, maxRetries: 3});
}
