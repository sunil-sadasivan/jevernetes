// Social demo of the real UI. All logs, relevance decisions and usage are synthetic.
// Node 22+, Chrome and ffmpeg required. No cluster, credentials or saved reports are read.
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {readFile,writeFile,mkdir,mkdtemp,rm} from 'node:fs/promises';
import {join,resolve,dirname} from 'node:path';
import {tmpdir} from 'node:os';
import {fileURLToPath} from 'node:url';
import {spawn} from 'node:child_process';
import {once} from 'node:events';
const root=resolve(dirname(fileURLToPath(import.meta.url)),'..');
const output=join(root,'docs/images');
const temp=await mkdtemp(join(tmpdir(),'jevernetes-search-social-'));
const pause=ms=>new Promise(r=>setTimeout(r,ms));
const samples=[
 ['api','GET /health 200 duration=3ms',12],
 ['worker','Job completed queue=notifications duration=42ms',8],
 ['api','GET /catalog 200 duration=18ms',10],
 ['db','ERROR PostgreSQL connection pool exhausted: active=50 max=50 waiting=128',24,.98],
 ['api','ERROR GET /orders 503: timed out waiting for a database connection',18,.97],
 ['db','ERROR database connection refused: retries exhausted after 30s',9,.96],
 ['db','WARN replication lag=480s; read replicas serving stale results',8,.94],
 ['db','ERROR deadlock detected: transaction rolled back during checkout',6,.92],
 ['api','WARN query timeout after 10000ms: SELECT orders WHERE status=pending',5,.89],
 ['worker','WARN payment queue backlog=420; database writes delayed',4,.84],
 ['db','WARN slow query duration=3200ms; threshold=1000ms',3,.67],
 ['api','GET /orders 503 client=192.0.2.42 duration=10004ms',7],
 ['api','POST /checkout 503 client=192.0.2.42 duration=10001ms',4],
 ['api','GET /catalog 200 client=192.0.2.42 duration=18ms',6],
 ['api','GET /catalog 200 client=192.0.2.43 duration=21ms',8],
 ['worker','Scheduled cleanup completed: removed=12 expired entries',7],
];
const events=samples.flatMap(([service,text,count,confidence],g)=>Array.from({length:count},(_,i)=>({
 id:`demo-${g}-${i}`,group_id:`group-${g}`,text,importance:confidence||text.includes('503')?'important':'routine',
 importance_confidence:confidence||.97,severity:confidence||text.includes('503')?'degraded':'info',category:confidence||text.includes('503')?'dependency':'routine',
 timestamp:new Date(Date.UTC(2026,8,21,12,41,g*2+i*3)).toISOString(),
 line_start:i+1,line_end:i+1,line_count:1,truncated:false,
 source:{type:'kubernetes',context:'demo-cluster',namespace:'demo',pod:service+'-7bd4',container:service,previous:false},
 baseline:{important:Boolean(confidence),signals:confidence?['error']:[]},analysis_reused:i>0,
})));
const usage={request_attempts:2,estimated_cost_usd:.00012,input_tokens:2857,output_tokens:40,unmetered_requests:0,in_flight:0,cost_complete:true};
const report={created_at:'2026-09-21T12:41:00Z',mode:'jev',scope:{context:'demo-cluster',since:'5m'},events,
 tail_events:[...events].sort((a,b)=>a.timestamp.localeCompare(b.timestamp)).map((e,i)=>({...e,sequence:i+1})),coverage:[],
 summary:{streams:3,elapsed_seconds:40,events:events.length,lines:events.length,important:events.filter(e=>e.importance==='important').length,routine:events.filter(e=>e.importance==='routine').length,uncertain:0,unknown:0,api_requests:2,reused_events:events.length-samples.length,coverage_gaps:0},
 live:{status:'running',message:'Following 3 containers in the demo namespace',active_streams:3,queue_depth:0,dropped:0,unfollowed_streams:0,retained_events:events.length},usage};
let search=null,polls=0;
function searchResult(){
 const active=polls++<1;
 const results=search.query.includes('192.0.2.42')?[11,12,13].map(g=>({g,confidence:.98})):
 samples.map((s,g)=>({g,confidence:s[3]})).filter(r=>r.confidence);
 const rows=results.map(({g,confidence})=>({group_id:`group-${g}`,event_id:`demo-${g}-0`,count:samples[g][2],relevance:confidence<.7?'possible':'match',confidence}));
 return {id:search.id,query:search.query,mode:'jev',status:active?'running':'complete',message:active?'Evaluating log groups…':'Finished searching the frozen log window.',
 matched_groups:active?0:rows.filter(r=>r.relevance==='match').length,possible_groups:active?0:rows.filter(r=>r.relevance==='possible').length,matching_instances:active?0:rows.filter(r=>r.relevance==='match').reduce((n,r)=>n+r.count,0),
 examined_groups:active?0:samples.length,total_groups:samples.length,unexamined_groups:active?samples.length:0,failed_groups:0,partial:active,cache_hits:0,elapsed_seconds:active?0:1.4,usage,result_count:active?0:rows.length,results:active?[]:rows};
}
const server=createServer(async(req,res)=>{
 try{
  const path=new URL(req.url,'http://localhost').pathname;res.setHeader('Content-Type','application/json');
  if(path==='/api/state')return res.end(JSON.stringify({csrf:'synthetic-demo',jev_available:true,reports:[],job:{id:'demo-live',live:true,status:'running',message:'Synthetic demo'}}));
  if(path==='/api/live')return res.end(JSON.stringify(report));
  if(path==='/api/search'&&req.method==='POST'){
   let body='';for await(const c of req)body+=c;const payload=JSON.parse(body);assert.equal(payload.mode,'jev');assert.equal(payload.live_id,'demo-live');
   search={id:'demo-query-'+Date.now(),query:payload.query};polls=0;res.statusCode=202;return res.end(JSON.stringify({id:search.id,report:{...report,tail_events:[]}}));
  }
  if(path==='/api/search')return res.end(JSON.stringify(searchResult()));
  const files={'/':'index.html','/app.js':'app.js','/context.js':'context.js','/prompt.js':'prompt.js','/search.js':'search.js','/style.css':'style.css','/favicon.svg':'favicon.svg'};
  if(files[path]){res.setHeader('Content-Type',path.endsWith('.js')?'text/javascript':path.endsWith('.css')?'text/css':path.endsWith('.svg')?'image/svg+xml':'text/html');return res.end(await readFile(join(root,'jevernetes/web',files[path])));}
  res.writeHead(404);res.end();
 }catch(e){console.error(e.message);res.writeHead(500);res.end();}
});
let chrome,socket;
try{
 server.listen(0,'127.0.0.1');await once(server,'listening');const origin=`http://127.0.0.1:${server.address().port}`;
 chrome=spawn(process.env.CHROME_BIN||'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',['--headless=new','--disable-gpu','--no-first-run','--no-default-browser-check','--disable-background-networking','--disable-component-update','--disable-sync','--remote-debugging-port=0',`--user-data-dir=${join(temp,'profile')}`,'about:blank'],{stdio:['ignore','ignore','ignore']});
 let port;for(let i=0;i<100;i++){try{port=(await readFile(join(temp,'profile/DevToolsActivePort'),'utf8')).split('\n')[0];break;}catch{await pause(100);}}assert(port);
 const tabs=await(await fetch(`http://127.0.0.1:${port}/json/list`)).json();socket=new WebSocket(tabs.find(t=>t.type==='page').webSocketDebuggerUrl);await once(socket,'open');
 let sequence=0;const pending=new Map(),errors=[];
 socket.addEventListener('message',({data})=>{const m=JSON.parse(data),entry=pending.get(m.id);if(entry){pending.delete(m.id);m.error?entry.reject(new Error(JSON.stringify(m.error))):entry.resolve(m.result);}if(m.method==='Runtime.exceptionThrown')errors.push(m.params.exceptionDetails);});
 const cdp=(method,params={})=>new Promise((resolve,reject)=>{pending.set(++sequence,{resolve,reject});socket.send(JSON.stringify({id:sequence,method,params}));});
 const evaluate=async expression=>{const r=await cdp('Runtime.evaluate',{expression,awaitPromise:true,returnByValue:true});if(r.exceptionDetails)throw new Error(JSON.stringify(r.exceptionDetails));return r.result.value;};
 const until=async expression=>{for(let i=0;i<100;i++){if(await evaluate(expression))return;await pause(100);}throw new Error('Timed out: '+expression);};
 await cdp('Runtime.enable');await cdp('Page.enable');await cdp('Emulation.setDeviceMetricsOverride',{width:1280,height:800,deviceScaleFactor:1,mobile:false});await cdp('Emulation.setTimezoneOverride',{timezoneId:'UTC'});
 socket.addEventListener('message',({data})=>{const m=JSON.parse(data);if(m.method==='Fetch.requestPaused'){const{requestId,request}=m.params;void cdp(request.url.startsWith(origin+'/')?'Fetch.continueRequest':'Fetch.failRequest',{requestId,...(request.url.startsWith(origin+'/')?{}:{errorReason:'BlockedByClient'})});}});
 await cdp('Fetch.enable',{patterns:[{urlPattern:'*'}]});await cdp('Page.navigate',{url:origin});await until("document.querySelectorAll('.tail-row').length>0");
 // Recording-only framing: focus the actual viewer, with clear demo disclosure.
 await evaluate(`{
 const style=document.createElement('style');style.textContent='.sidebar,.topbar,.page-heading,.stats,.distribution,#job,#live-panel,footer{display:none!important}main{margin:0;width:100%}.workspace{padding:112px 28px 94px;max-width:none}.tail-lines{height:470px}.search-log-row,.search-log-heading{font-size:12px;line-height:20px;grid-template-columns:25ch 108px 142px minmax(240px,1fr) 48px}.search-log-row{padding-top:9px;padding-bottom:9px}.search-log-viewport{min-height:365px;max-height:410px}.search-log-heading{font-size:12px}.search-log-actions button{font-size:12px}#ask-status,#ask-query-used{font-size:12px}.log-search-toolbar input{font-size:18px;padding:13px 15px}#ask-pagination{margin-top:12px}#social-head{position:fixed;inset:0 0 auto;z-index:5;padding:22px 28px;background:#111318;border-bottom:1px solid #2a2e39;display:flex;justify-content:space-between;align-items:center;color:#f1f2f5;font:600 24px/1.3 system-ui}#social-head strong{color:#c6adff}#social-head small{display:block;font:13px/1.5 system-ui;color:#a0a5b6;margin-top:4px}#social-caption{position:fixed;inset:auto 0 0;z-index:2147483647;padding:18px 28px;background:#191620;border-top:1px solid #544268;color:#f1f2f5;font:600 22px/1.4 system-ui;display:flex;align-items:center;justify-content:space-between;gap:16px}#social-caption small{font:11px/1.5 system-ui;color:#bca9d8;text-align:right;white-space:nowrap}#social-pointer{position:fixed;width:22px;height:22px;background:#c6adff44;border:2px solid #d8c8ff;border-radius:50%;pointer-events:none;z-index:2147483647;display:none}dialog{max-height:72vh}#instances-dialog{width:1000px}.instances-list{max-height:43vh}';document.head.append(style);
 const head=document.createElement('div');head.id='social-head';head.innerHTML='<div><strong>jevernetes</strong><small>Kubernetes logs. Search with Jev.</small></div><div style="font-size:15px;color:#a0a5b6">github.com/sunil-sadasivan/jevernetes</div>';document.body.append(head);
 const caption=document.createElement('div');caption.id='social-caption';caption.innerHTML='<span></span><small>SYNTHETIC DEMO<br>Illustrative logs, results &amp; usage</small>';document.body.append(caption);
 const pointer=document.createElement('div');pointer.id='social-pointer';document.body.append(pointer);
 }`);
 const frames=[],texts=[];
 async function frame(label,duration=1){
  await evaluate(`{const c=document.getElementById('social-caption');(document.querySelector('dialog[open]')||document.body).append(c);c.querySelector('span').textContent=${JSON.stringify(label)};}`);await pause(50);
  const{data}=await cdp('Page.captureScreenshot',{format:'png'});const name=`frame-${String(frames.length).padStart(3,'0')}.png`;await writeFile(join(temp,name),Buffer.from(data,'base64'));frames.push({name,duration});texts.push(await evaluate('document.body.innerText'));
 }
 async function point(selector,click=false){
  const p=await evaluate(`(()=>{const e=document.querySelector(${JSON.stringify(selector)});const r=e.getBoundingClientRect();return{x:r.x+r.width/2,y:r.y+r.height/2}})()`);
  await evaluate(`{const p=document.getElementById('social-pointer');(document.querySelector('dialog[open]')||document.body).append(p);Object.assign(p.style,{display:'block',left:'${p.x-11}px',top:'${p.y-11}px'});}`);
  await cdp('Input.dispatchMouseEvent',{type:'mouseMoved',...p});
  if(click){await cdp('Input.dispatchMouseEvent',{type:'mousePressed',...p,button:'left',clickCount:1});await cdp('Input.dispatchMouseEvent',{type:'mouseReleased',...p,button:'left',clickCount:1});}
 }
 const hide=()=>evaluate("document.getElementById('social-pointer').style.display='none'");
 async function type(question,label){await point('#ask-query',true);await evaluate("document.getElementById('ask-query').value=''");await hide();for(let i=0;i<question.length;i+=4){await cdp('Input.insertText',{text:question.slice(i,i+4)});await frame(label,.1);}}
 await evaluate("document.getElementById('tail-follow').checked=false;document.getElementById('tail-lines').scrollTop=0");
 await frame('Too many logs. Start with a question.',1.8);
 await point('#open-search',true);await hide();await frame('Ask Jev in plain English.',.7);
 await type('What is causing database failures?','Ask Jev in plain English.');
 await point('#ask-submit',true);await hide();await until("document.querySelectorAll('.search-log-row').length===8");
 await frame('Matching logs. Confidence. Original timestamps.',3);
 await point('.search-log-row');await frame('Hover a log to inspect the evidence.',1.3);
 await point('.search-log-actions button:nth-child(2)',true);await hide();
 assert.equal(await evaluate('state.instances.events.length'),24);
 await frame('24 repeats. Every instance is still there.',2.5);
 await point('#close-instances',true);await hide();
 await type('Find requests from 192.0.2.42','Find the requests you care about.');
 await point('#ask-submit',true);await hide();await until("document.querySelectorAll('.search-log-row').length===3");
 await frame('One question. Relevant requests across your logs.',2.8);
 await point('.search-log-actions button:first-child',true);await hide();
 assert.equal(await evaluate("document.querySelector('#detail-dialog').open"),true);
 await frame('Full log text. Source. Date and time.',1.8);
 await evaluate("document.querySelector('#detail-dialog').close()");await hide();
 await frame('Find the signal. Keep the evidence.',2.8);
 assert.equal(errors.length,0,JSON.stringify(errors));
 await mkdir(output,{recursive:true});
 await writeFile(join(temp,'frames.txt'),frames.map(f=>`file '${f.name}'\nduration ${f.duration}\n`).join('')+`file '${frames.at(-1).name}'\n`);
 await writeFile(join(temp,'visible-text.json'),JSON.stringify(texts,null,2));
 for(const name of ['search-demo.gif','search-demo.mp4']){
  const filter=name.endsWith('.gif')?['-filter_complex','[0:v]fps=10,split[a][b];[a]palettegen=stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=3','-loop','0']:['-vf','fps=30,format=yuv420p','-c:v','libx264','-preset','slow','-crf','20','-movflags','+faststart'];
  const process=spawn('ffmpeg',['-hide_banner','-loglevel','error','-y','-f','concat','-safe','0','-i',join(temp,'frames.txt'),...filter,join(output,name)],{stdio:'inherit'});assert.equal((await once(process,'exit'))[0],0);
 }
 await writeFile(join(output,'search-demo-preview.png'),await readFile(join(temp,frames.find((f,i)=>texts[i].includes('Matching logs. Confidence. Original timestamps.')).name)));
 console.log(JSON.stringify({gif:join(output,'search-demo.gif'),mp4:join(output,'search-demo.mp4'),frames:temp,seconds:frames.reduce((n,f)=>n+f.duration,0)}));
}finally{
 socket?.close();chrome?.kill();server.close();if(chrome&&chrome.exitCode===null)await Promise.race([once(chrome,'exit'),pause(3000)]);await rm(join(temp,'profile'),{recursive:true,force:true,maxRetries:3});
}
