const fs = require('node:fs'), vm = require('node:vm'), assert = require('node:assert/strict');
const app = fs.readFileSync('jevernetes/web/app.js', 'utf8');
const elements = new Map();
class Element {
  constructor(tag='div') { this.tagName=tag; this.children=[]; this.listeners={}; this.dataset={}; this.value=''; this.textContent=''; this.checked=false; this.open=false; this.classList={toggle(){},add(){},remove(){}}; }
  append(...items) { this.children.push(...items); }
  replaceChildren(...items) { this.children=items; }
  addEventListener(name, handler) { this.listeners[name]=handler; }
  querySelectorAll(tag) { return this.children.flatMap(child => [...(child.tagName===tag ? [child] : []), ...child.querySelectorAll(tag)]); }
  setAttribute(name, value) { this[name]=value; }
  showModal() { this.open=true; }
  close() { this.open=false; }
}
const get = id => { if (!elements.has(id)) elements.set(id, new Element()); return elements.get(id); };
const state = {selection:new Map(), visibleEvents:[], page:0, pageSize:40};
let inspected;
const sandbox = {state, document:{createElement:tag=>new Element(tag)}, $:get,
  sourceName:s=>s.path, human:s=>s||'unknown', fmt:n=>String(n||0), reviewLabel:e=>e.review?.status||e.importance,
  eventReference:e=>({report_id:'frozen-report.json',event_id:e.id}),
  detail:(...args)=>{inspected=args;}, scopeValues:()=>({}),
  LogContext:{matches:()=>true,collected:r=>r.events}, TextEncoder};
vm.createContext(sandbox);
vm.runInContext(fs.readFileSync('jevernetes/web/prompt.js','utf8'), sandbox);
for (const name of ['confidenceValue','confidenceBand','matchesConfidence','confidenceSummary','confidenceBadge','orderConfidenceRows','loadConfidencePreferences','saveConfidencePreferences','node','eventGroupKey','groupEvents','openInstances','renderInstances','renderEvents','renderSelection','selectEvents','selectedPrompt','selectedReview']) {
  const match = app.match(new RegExp(`^function ${name}\\([^]*?^\\}`, 'm'));
  // node() is intentionally a one-line helper.
  const code = name === 'node' ? app.split('\n').find(line=>line.startsWith('function node(')) : match?.[0];
  assert.ok(code, name); vm.runInContext(code, sandbox);
}
const events = Array.from({length:85},(_,i)=>({id:'event-'+i, group_id:'same', source:{type:'file',path:'synthetic.log'},text:'ERROR status=503',importance:'important',severity:'impact',category:'dependency',line_start:i+1,line_end:i+1,timestamp:`2026-09-21T12:00:${String(i%60).padStart(2,'0')}Z`,analysis_reused:i>0}));
const other = {...events[0],id:'changed',group_id:'changed',text:'ERROR status=500'};
state.report={events:[...events,other]};
get('importance').value='all';get('source-filter').value='all';get('event-display').value='groups';
sandbox.renderEvents();
assert.equal(get('event-rows').children.length,2);
assert.match(get('event-count').textContent,/2 groups · 86 matching instances/);
assert.equal(state.visibleEvents.length,86,'Select-all includes every matching instance');
assert.equal(get('select-page').disabled,false);
const groupCheckbox = () => get('event-rows').children[0].children[0].children[0];
groupCheckbox().checked=true;groupCheckbox().listeners.change();
assert.equal(state.selection.size,85,'Selecting a group includes all members, beyond the former 50-event cap');
assert.equal(groupCheckbox().checked,true);
assert.equal(get('select-page').indeterminate,true);
assert.match(get('selection-count').textContent,/85 instances selected/);
const prompt=sandbox.selectedPrompt();
assert.match(prompt,/"instance_count": 85/);
assert.ok(prompt.includes('event-84'));
assert.ok(!prompt.includes('ERROR status=500'),'Prompt excludes unselected evidence');
state.selection.delete('event-0');sandbox.renderEvents();
assert.equal(groupCheckbox().indeterminate,true);
groupCheckbox().checked=true;groupCheckbox().listeners.change();
assert.equal(state.selection.size,85,'Clicking a partial group selects the remainder');
vm.runInContext(app.split('\n').find(line=>line.startsWith("$('select-page').addEventListener")),sandbox);
get('select-page').checked=true;get('select-page').listeners.change();
assert.equal(state.selection.size,86);
assert.equal(get('select-page').checked,true);
get('select-page').checked=false;get('select-page').listeners.change();
assert.equal(state.selection.size,0);
groupCheckbox().checked=true;groupCheckbox().listeners.change();
assert.equal(sandbox.selectedReview().events.length,85);
get('event-display').value='instances';sandbox.renderEvents();
assert.equal(groupCheckbox().checked,true,'Selection persists when switching display');
groupCheckbox().checked=false;groupCheckbox().listeners.change();
assert.equal(state.selection.size,84);
get('event-display').value='groups';sandbox.renderEvents();
assert.equal(groupCheckbox().indeterminate,true);
groupCheckbox().checked=false;groupCheckbox().listeners.change();
assert.equal(state.selection.size,0,'Deselecting a group removes all its selected instances');
get('event-rows').children[0].children[2].children[0].listeners.click();
assert.equal(get('instances-list').children.length,40);
assert.equal(get('instances-page').textContent,'1 / 3');
assert.match(get('instances-note').textContent,/85 instances/);
state.instances.page=2;sandbox.renderInstances();
assert.equal(get('instances-list').children.length,5);
assert.equal(get('instances-next').disabled,true);
const report=state.report;
state.report={events:[]};
get('instances-list').children[4].listeners.click();
assert.equal(inspected[0].id,'event-84');
assert.equal(inspected[1],report,'Inspect uses the frozen report even if the current view changes');
assert.equal(inspected[2].report_id,'frozen-report.json');
assert.equal(inspected[2].event_id,'event-84');
state.report=report;get('event-display').value='instances';sandbox.renderEvents();
assert.equal(state.visibleEvents.length,40);
assert.equal(get('event-rows').children[0].children[0].children[0].type,'checkbox');
get('search').value='status=500';get('event-display').value='groups';sandbox.renderEvents();
assert.equal(get('event-rows').children.length,1);
assert.match(get('event-count').textContent,/1 groups · 1 matching instances/);
get('select-page').checked=true;get('select-page').listeners.change();
assert.equal(state.selection.size,1);
assert.ok(state.selection.has('changed'),'Filtered select-all excludes hidden groups');
get('search').value='';sandbox.renderEvents();
assert.equal(get('select-page').indeterminate,true);
state.selection.clear();
state.pageSize=1;state.page=0;sandbox.renderEvents();
get('select-page').checked=true;get('select-page').listeners.change();
assert.equal(state.selection.size,85);
state.page=1;sandbox.renderEvents();
assert.equal(get('select-page').checked,false);
get('select-page').checked=true;get('select-page').listeners.change();
assert.equal(state.selection.size,86,'Selections persist across pages');
state.page=0;sandbox.renderEvents();
assert.equal(groupCheckbox().checked,true);
state.report={events:[...report.events,{...events[0],id:'arrived-later'}]};sandbox.renderEvents();
assert.equal(groupCheckbox().indeterminate,true,'New live arrivals are not silently selected');
assert.equal(state.selection.size,86);
assert.ok(!state.selection.has('arrived-later'));
state.report=report;
const reviewed={...events[0],id:'reviewed',importance:'routine',review:{status:'acknowledged'}};
assert.equal(sandbox.groupEvents([events[0],reviewed]).length,2,'Reviews remain distinct');
const fallback={...events[0]};delete fallback.group_id;
assert.equal(sandbox.eventGroupKey(fallback),sandbox.eventGroupKey({...fallback,source:{path:'synthetic.log',type:'file'}}));
assert.notEqual(sandbox.eventGroupKey(fallback),sandbox.eventGroupKey({...fallback,text:'ERROR status=500'}));
// Confidence controls must retain grouping and selection semantics.
for (const [value,band] of [[0,'low'],[.699,'low'],[.7,'medium'],[.899,'medium'],[.9,'high'],[1,'high']]) {
  assert.equal(sandbox.confidenceBand(sandbox.confidenceValue({...events[0],importance_confidence:value})),band);
}
for (const value of [null,undefined,'0.99',true,NaN,Infinity,-1,1.1]) {
  assert.equal(sandbox.confidenceValue({...events[0],importance_confidence:value}),null);
}
assert.equal(sandbox.confidenceValue({...events[0],importance_confidence:.99,review:{status:'acknowledged'}}),null);
assert.equal(sandbox.confidenceValue({...events[0],importance_confidence:.99,importance:'pending'}),null);
const low={...events[0],id:'low',importance_confidence:.4};
const high={...events[1],id:'high',importance_confidence:.99};
const medium={...other,id:'medium',importance_confidence:.8};
const unscored={...other,id:'unscored',group_id:'unscored'};
state.selection.clear();state.page=0;state.pageSize=40;
state.report={events:[low,high,medium,unscored]};
get('search').value='';get('confidence-order').value='high';get('confidence-filter').value='all';
sandbox.renderEvents();
assert.equal(get('event-rows').children[0].className,'confidence-row-mixed');
assert.match(get('event-rows').children[0].children[1].children[1].textContent,/40%–99%/);
assert.equal(state.visibleEvents[0].id,'low','Highest group confidence surfaces the whole group');
assert.equal(state.visibleEvents.at(-1).id,'unscored');
get('confidence-filter').value='high';sandbox.renderEvents();
assert.equal(state.visibleEvents.length,1);
assert.equal(state.visibleEvents[0].id,'high');
assert.equal(get('event-rows').children[0].className,'confidence-row-high');
get('select-page').checked=true;get('select-page').listeners.change();
assert.equal(state.selection.size,1);
assert.ok(state.selection.has('high'),'Confidence filter only selects matching instances');
get('confidence-filter').value='all';get('event-display').value='instances';sandbox.renderEvents();
assert.equal(state.visibleEvents.map(e=>e.id).join(','),'high,medium,low,unscored');
get('confidence-order').value='low';sandbox.renderEvents();
assert.equal(state.visibleEvents.map(e=>e.id).join(','),'low,medium,high,unscored');
get('confidence-order').value='collection';sandbox.renderEvents();
assert.equal(state.visibleEvents.map(e=>e.id).join(','),'low,high,medium,unscored');
get('confidence-filter').value='unscored';sandbox.renderEvents();
assert.equal(state.visibleEvents.map(e=>e.id).join(','),'unscored');
const storage=new Map();sandbox.localStorage={getItem:key=>storage.get(key),setItem:(key,value)=>storage.set(key,value)};
sandbox.loadConfidencePreferences();
assert.equal(get('confidence-order').value,'high');
assert.equal(get('confidence-filter').value,'all');
get('confidence-order').value='low';get('confidence-filter').value='medium';sandbox.saveConfidencePreferences();
get('confidence-order').value='high';sandbox.loadConfidencePreferences();
assert.equal(get('confidence-order').value,'low');assert.equal(get('confidence-filter').value,'medium');
storage.set('jevernetes-confidence','invalid JSON');sandbox.loadConfidencePreferences();
assert.equal(get('confidence-order').value,'high');
sandbox.localStorage={getItem(){throw Error('disabled');},setItem(){throw Error('disabled');}};
sandbox.loadConfidencePreferences();sandbox.saveConfidencePreferences();
state.report=report;state.selection.clear();get('event-display').value='groups';sandbox.renderEvents();
vm.runInContext(app.slice(app.indexOf("$('ack-selected').addEventListener"),app.indexOf('function previewPrompt')),sandbox);
const calls=[];
sandbox.reviewAction=async payload=>{calls.push(payload);};
sandbox.sameReviewSource=()=>true;
sandbox.reloadReviewedSelection=async (_reference,ids)=>{for(const id of ids)state.selection.delete(id);};
sandbox.expectedPattern=e=>e.text;
(async()=>{
  state.selection.clear();sandbox.selectEvents(events,true);
  await get('ack-selected').listeners.click();
  assert.equal(calls[0].events.length,85,'Acknowledgment includes the entire selected group');
  assert.equal(state.selection.size,0);
  sandbox.selectEvents([...events,other],true);
  get('expect-selected').listeners.click();
  assert.equal(get('bulk-expected-items').children.length,2,'Expected-rule form combines repeated instances');
  await get('bulk-expected-form').listeners.submit({preventDefault(){}});
  assert.equal(calls[1].events.length,2);
  assert.equal(calls[1].selected_event_ids.length,86,'Server validates every selected instance before saving rules');
  assert.equal(state.selection.size,0);
  console.log('Grouping UI: row and page multi-select, mixed state, pagination, filtering, live arrivals, grouped prompts, bulk acknowledgment, expected rules and confidence filters/order/preferences passed');
})().catch(error=>{console.error(error);process.exitCode=1;});
