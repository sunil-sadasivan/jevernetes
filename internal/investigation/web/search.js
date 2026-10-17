'use strict';
// A query owns a frozen report reference, so live updates and report switches
// cannot redirect result inspection to a different source.
let logSearch = null;
function openLogSearch() {
  if (!state.report) return;
  const prefix = state.view === 'search' ? logSearch?.prefix || 'events' : state.view === 'tail' ? 'tail' : 'events', scope=scopeValues(prefix);
  const source = prefix === 'events' ? $('source-filter').value : 'all';
  const reference = state.watchingLive ? {live_id:state.job?.id} : {report_id:state.selected};
  const sourceKey=JSON.stringify([reference,scope,source]);
  if (!logSearch || (!['running','starting'].includes(logSearch.status) && (logSearch.reportSource !== state.report || logSearch.sourceKey!==sourceKey))) {
    const events = LogContext.collected(state.report).filter(e => LogContext.matches(e,scope) && (source==='all' || sourceName(e.source)===source));
    logSearch = {prefix,scope,source,sourceKey,reportSource:state.report,report:{...state.report,events,tail_events:[]},reference,events:new Map(events.map(e=>[e.id,e])),status:'idle',offset:0};
    $('ask-scope').textContent=`${fmt(events.length)} collected instances currently shown in these source filters. Each search captures the latest retained logs when submitted, including Routine and pending logs.`;
    $('ask-results').replaceChildren();$('ask-status').textContent='Enter a question to find the log evidence.';
    $('ask-usage').textContent='';$('ask-query-used').textContent='';$('ask-error').classList.add('hidden');$('ask-pagination').classList.add('hidden');
    $('ask-submit').disabled=false;$('ask-stop').classList.add('hidden');
  }
  switchView(false,false,true);$('ask-query').focus();
}
async function startLogSearch(event) {
  event.preventDefault();
  if (!logSearch || ['running','starting'].includes(logSearch.status)) return;
  const search=logSearch={...logSearch,status:'starting',offset:0,id:null,polling:false};
  $('ask-submit').disabled=true;$('ask-stop').classList.add('hidden');$('ask-error').classList.add('hidden');
  $('ask-status').textContent='Preparing search…';$('ask-results').replaceChildren();$('ask-pagination').classList.add('hidden');
  try {
    const result=await api('/api/search',{method:'POST',headers:{'Content-Type':'application/json','X-Jev-Token':state.csrf},body:JSON.stringify({...search.reference,scope:search.scope,source:search.source,query:$('ask-query').value,mode:'jev',max_batches:Number($('ask-budget').value)})});
    search.report=result.report;
    search.events=new Map(LogContext.collected(result.report).map(e=>[e.id,e]));
    $('ask-scope').textContent=`${fmt(search.events.size)} retained instances captured when this search started, including Routine and pending logs. Submit another question to search the latest window.`;
    search.id=result.id;search.status='running';
    $('ask-stop').classList.remove('hidden');
    await pollLogSearch(search);
  } catch(error) {
    if (logSearch!==search) return;
    search.status='error';$('ask-error').textContent=error.message;$('ask-error').classList.remove('hidden');$('ask-status').textContent='Search could not start.';$('ask-submit').disabled=false;
  }
}
async function pollLogSearch(search) {
  if (logSearch!==search || search.polling || !search.id) return;
  search.polling=true;
  const offset=search.offset;
  try {
    const result=await api('/api/search?id='+encodeURIComponent(search.id)+'&offset='+offset);
    if (logSearch!==search || offset!==search.offset) return;
    search.status=result.status;
    $('ask-error').classList.add('hidden');
    renderLogSearch(search,result);
  } catch(error) {
    if (logSearch===search) {
      $('ask-error').textContent=error.message+(search.status==='running'?' Retrying progress…':'');$('ask-error').classList.remove('hidden');$('ask-submit').disabled=search.status==='running';
    }
  } finally {
    search.polling=false;
    if (logSearch===search && (search.status==='running' || offset!==search.offset)) setTimeout(()=>pollLogSearch(search),500);
  }
}
function renderLogSearch(search,result) {
  const active=result.status==='running', u=result.usage;
  const mode='Jev semantic search';
  $('ask-status').textContent=`${mode}: ${fmt(result.matched_groups)} matching groups (${fmt(result.matching_instances)} instances), ${fmt(result.possible_groups)} possible groups. Evaluated ${fmt(result.examined_groups)} of ${fmt(result.total_groups)} groups. ${result.message}${result.partial ? ` ${fmt(result.unexamined_groups)} unexamined; ${fmt(result.failed_groups)} failed. This is not an exhaustive answer.` : ''}`;
  $('ask-usage').textContent=`${fmt(u.request_attempts)} Jev requests · ${fmt(result.cache_hits)} cached groups · estimated $${Number(u.estimated_cost_usd).toFixed(8)} · ${result.elapsed_seconds}s${u.cost_complete?'':' · Usage incomplete'} · Query cost is separate from live analysis.`;
  $('ask-query-used').textContent='Search: '+result.query;
  $('ask-submit').disabled=active;$('ask-stop').classList.toggle('hidden',!active);
  $('ask-results').replaceChildren();
  for (const match of result.results) {
    const e=search.events.get(match.event_id);
    if (!e) continue;
    const row=node('div',undefined,'search-log-row search-relevance-'+match.relevance);
    row.tabIndex=0;row.setAttribute('role','listitem');
    const label=match.relevance==='unknown'?'Not evaluated':match.relevance==='possible'?'Possible match':'Match';
    const confidence=match.confidence==null?'No relevance confidence':Math.round(match.confidence*100)+'% relevance confidence';
    const time=node('time',e.timestamp?.replace('T',' ') || 'No timestamp','search-log-time');
    if(e.timestamp)time.dateTime=e.timestamp;
    time.title=e.timestamp || 'Timestamp not recorded';
    const relevance=node('span',match.relevance==='unknown'?'Unchecked':(match.relevance==='possible'?'Possible':'Match')+(match.confidence==null?'':' '+Math.round(match.confidence*100)+'%'),'search-log-relevance');
    relevance.title=label+' · '+(match.relevance==='unknown'?(match.error||'Retry this search'):confidence);
    const source=node('span',sourceName(e.source),'search-log-source');source.title=sourceName(e.source);
    const message=node('span',e.text.replace(/\s*\n\s*/g,' ↵ '),'search-log-message');
    const count=node('span','×'+fmt(match.count),'search-log-count');count.title=fmt(match.count)+' instances';
    row.append(time,relevance,source,message,count);
    const actions=node('div',undefined,'search-log-actions');
    const inspect=node('button','Inspect','secondary'), instances=node('button','View all instances','secondary');
    const inspectEvidence=()=>detail(e,search.report,{...search.reference,event_id:e.id});
    inspect.addEventListener('click',inspectEvidence);
    instances.addEventListener('click',()=>openInstances(e,search.report,{...search.reference,event_id:e.id}));
    row.addEventListener('keydown',event=>{if(event.target===row && event.key==='Enter'){event.preventDefault();inspectEvidence();}});
    actions.append(inspect,instances);row.append(actions);$('ask-results').append(row);
  }
  if (!result.results.length) $('ask-results').append(node('p',active?'Jev is evaluating candidate groups…':result.partial?'No matches found in the evaluated groups. Narrow the question or increase the batch limit to search more.':'No matching evidence found in this collected window.','field-note'));
  const pages=Math.max(1,Math.ceil(result.result_count/20));
  $('ask-page').textContent=`${Math.floor(search.offset/20)+1} / ${pages}`;
  $('ask-prev').disabled=search.offset===0;$('ask-next').disabled=search.offset+20>=result.result_count;
  $('ask-pagination').classList.toggle('hidden',!result.result_count);
}
$('open-search').addEventListener('click',openLogSearch);
$('ask-form').addEventListener('submit',startLogSearch);
$('ask-stop').addEventListener('click',async()=>{
  const search=logSearch;if(!search?.id)return;
  try {await api('/api/search/stop',{method:'POST',headers:{'Content-Type':'application/json','X-Jev-Token':state.csrf},body:JSON.stringify({id:search.id})});$('ask-status').textContent='Stopping search; waiting for in-flight requests…';}
  catch(error){$('ask-error').textContent=error.message;$('ask-error').classList.remove('hidden');}
});
for (const [id,change] of [['ask-prev',-20],['ask-next',20]]) $(id).addEventListener('click',()=>{logSearch.offset+=change;pollLogSearch(logSearch);});
