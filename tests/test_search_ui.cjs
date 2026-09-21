const fs=require('node:fs'),vm=require('node:vm'),assert=require('node:assert/strict');
class Element {
  constructor(){this.children=[];this.listeners={};this.value='';this.textContent='';this.open=false;this.hidden=false;this.classList={add:()=>{this.hidden=true;},remove:()=>{this.hidden=false;},toggle:(_name,value)=>{this.hidden=value;}};}
  setAttribute(name,value){this[name]=value;}
  append(...items){this.children.push(...items);}
  replaceChildren(...items){this.children=items;}
  addEventListener(name,handler){this.listeners[name]=handler;}
  showModal(){this.open=true;}close(){this.open=false;}focus(){}
}
const elements=new Map(),get=id=>{if(!elements.has(id))elements.set(id,new Element());return elements.get(id);};
const routine={id:'routine',importance:'routine',source:{type:'file',path:'synthetic.log'},text:'request from 192.0.2.1'};
const important={...routine,id:'important',importance:'important',timestamp:'2026-09-21T12:34:56.123Z',text:'db pool exhausted\n  nested detail'};
const report={events:[routine,important],summary:{}};
const state={report,selected:'original.json',watchingLive:false,view:'events',csrf:'synthetic'};
let inspected,opened,posted;
const sandbox={state,$:get,fmt:String,scopeValues:()=>({}),sourceName:s=>s.path,setTimeout:()=>{},
  LogContext:{collected:r=>r.events,matches:()=>true},
  node:(tag,text,className)=>Object.assign(new Element(),{tag,textContent:text,className}),
  renderTail:()=>{},
  detail:(...args)=>{inspected=args;},openInstances:(...args)=>{opened=args;},
  api:async(path,options)=>{if(options){posted=JSON.parse(options.body);return {id:'query',report};}return result;}};
const result={id:'query',query:'db issues',mode:'jev',status:'complete',matched_groups:1,matching_instances:1,possible_groups:0,examined_groups:2,total_groups:2,unexamined_groups:0,failed_groups:0,partial:false,message:'Complete',usage:{request_attempts:1,estimated_cost_usd:.0001,cost_complete:true},cache_hits:0,elapsed_seconds:1,result_count:1,results:[{event_id:'important',count:1,relevance:'match',confidence:.98}]};
vm.createContext(sandbox);
vm.runInContext(fs.readFileSync('jevernetes/web/app.js','utf8').match(/^function switchView\([^]*?^\}/m)[0],sandbox);
vm.runInContext(fs.readFileSync('jevernetes/web/search.js','utf8'),sandbox);
(async()=>{
  get('source-filter').value='all';
  sandbox.openLogSearch();
  assert.match(get('ask-scope').textContent,/2 collected/);
  get('ask-query').value='db issues';get('ask-budget').value='16';
  await sandbox.startLogSearch({preventDefault(){}});
  assert.equal(posted.event_ids,undefined,'Search submits filters, never stale browser IDs');
  assert.equal(posted.mode,'jev');
  assert.equal(posted.source,'all');
  assert.equal(posted.report_id,'original.json');
  assert.equal(get('ask-results').children.length,1);
  const row=get('ask-results').children[0];
  assert.equal(state.view,'search','Search is inline in the report viewer');
  assert.equal(get('ask-view').hidden,false);
  assert.equal(get('events-view').hidden,true);
  assert.equal(get('open-search')['aria-pressed'],'true');
  assert.equal(row.children[0].textContent,'2026-09-21 12:34:56.123Z');
  assert.equal(row.children[0].dateTime,important.timestamp);
  assert.equal(row.children[3].textContent,'db pool exhausted ↵ nested detail');
  assert.equal(row.children[5].className,'search-log-actions');
  row.listeners.keydown({target:row,key:'Enter',preventDefault(){}});
  assert.equal(inspected[0].text,important.text,'Inspection preserves multiline evidence');
  state.report={events:[]};state.selected='different.json';
  row.children[5].children[0].listeners.click();
  assert.equal(inspected[0].id,'important');
  assert.equal(inspected[2].report_id,'original.json','Evidence retains its original reference');
  row.children[5].children[1].listeners.click();
  assert.equal(opened[1].events.length,2);
  const search=vm.runInContext('logSearch',sandbox);
  sandbox.renderLogSearch(search,{...result,results:[],result_count:0,matched_groups:0,partial:true,unexamined_groups:9});
  assert.match(get('ask-status').textContent,/not an exhaustive answer/);
  assert.match(get('ask-results').children[0].textContent,/evaluated groups/);
  sandbox.renderLogSearch(search,{...result,results:[{event_id:'routine',count:1,relevance:'match',confidence:.97}]});
  assert.match(get('ask-status').textContent,/Jev semantic search/);
  assert.match(get('ask-results').children[0].children[1].title,/97% relevance confidence/);
  assert.equal(get('ask-results').children[0].children[0].textContent,'No timestamp');
  // An old pagination response must not overwrite a newly submitted question.
  let finishOld;
  sandbox.api=async(path,options)=>{
    if(options)return {id:'new-query',report};
    if(path.includes('new-query'))return {...result,id:'new-query',query:'new question'};
    return await new Promise(resolve=>{finishOld=resolve;});
  };
  const oldPoll=sandbox.pollLogSearch(search);
  get('ask-query').value='new question';
  await sandbox.startLogSearch({preventDefault(){}});
  finishOld({...result,query:'old question'});await oldPoll;
  assert.equal(get('ask-query-used').textContent,'Search: new question');
  // A transient progress error must not allow another search while work is active.
  const active=vm.runInContext('logSearch',sandbox);active.status='running';
  sandbox.api=async()=>{throw new Error('Temporary network failure');};
  await sandbox.pollLogSearch(active);
  assert.equal(active.status,'running');
  assert.equal(get('ask-submit').disabled,true);
  assert.match(get('ask-error').textContent,/Retrying progress/);
  // The file/source filter also scopes evidence, independent of importance.
  active.status='complete';state.report=report;state.selected='original.json';
  get('source-filter').value='different.log';sandbox.openLogSearch();
  assert.equal(vm.runInContext('logSearch.events.size',sandbox),0);
  // The submitted query gets server evidence even after every displayed ID expired.
  const fresh={...important,id:'fresh',timestamp:'2026-09-21T13:00:00Z'};
  state.watchingLive=true;state.job={id:'live-session'};state.view='tail';state.report=report;
  sandbox.openLogSearch();
  sandbox.api=async(path,options)=>{
    if(options){posted=JSON.parse(options.body);return {id:'fresh-query',report:{events:[fresh],tail_events:[]}};}
    return {...result,id:'fresh-query',results:[{event_id:'fresh',count:1,relevance:'match',confidence:.98}]};
  };
  await sandbox.startLogSearch({preventDefault(){}});
  assert.equal(posted.event_ids,undefined);
  assert.equal(posted.live_id,'live-session');
  assert.equal(vm.runInContext('logSearch.events.has("fresh")',sandbox),true);
  assert.equal(vm.runInContext('logSearch.events.has("important")',sandbox),false);
  assert.equal(get('ask-results').children[0].children[0].dateTime,fresh.timestamp);
  get('ask-results').children[0].children[5].children[1].listeners.click();
  assert.equal(opened[1].events[0].id,'fresh','Inspection uses server-captured evidence');
  sandbox.switchView(false);
  assert.equal(get('ask-view').hidden,true);
  assert.equal(get('events-view').hidden,false);
  console.log('Log search UI: inline rows, timestamps, multiline inspection, keyboard actions, frozen references, partial results and stale-response isolation passed');
})().catch(error=>{console.error(error);process.exitCode=1;});
