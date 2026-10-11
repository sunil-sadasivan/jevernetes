const fs=require('node:fs'),vm=require('node:vm'),assert=require('node:assert/strict');
(async()=>{
  function element(){return {textContent:'',value:'',open:true,children:[],listeners:{},replaceChildren(){this.children=[];},append(v){this.children.push(v);},addEventListener(k,f){this.listeners[k]=f;},set innerHTML(v){throw new Error('HTML evidence insertion');}};}
  const nodes=Object.fromEntries(['summary','filter','events','page-status','previous','next','controller','connection','metrics','incidents','detail'].map(k=>[k,element()]));
  const doc={hidden:false,listeners:{},getElementById:k=>nodes[k],createElement:element,addEventListener(k,f){this.listeners[k]=f;}};
  const requests=[],timers=[];let fail=false;
  const context={document:doc,window:{addEventListener(){}},AbortController,Date,JSON,Math,Error,encodeURIComponent,
    setTimeout(f,ms){timers.push({f,ms});return timers.length;},clearTimeout(){},
    async fetch(url,opts) {
      requests.push({url,opts}); if(fail) return {ok:false};
      let value;
      if(url.startsWith('/api/report')) value={page:{next_offset:url.includes('offset=1')?null:1,matched_events:2,retained_events:2,coverage_complete:false,unavailable_events:0},summary:{events:1},events:[{importance:'important',text:'<img src=x onerror=alert(1)>'}]};
      else if(url==='/api/controller/config') value={enabled:true,interval_seconds:5};
      else if(url==='/api/controller/status') value={sampled_at:1,controller:{ready:true,metrics:{received:2},recent_incidents:[{id:'a'.repeat(64),last_decision:'review',recurrence_count:3}]}};
      else value={controller:{incident:{id:'a'.repeat(64)}}};
      return {ok:true,json:async()=>value};
    }
  };

  vm.createContext(context);vm.runInContext(fs.readFileSync('internal/dashboard/web/app.js','utf8'),context);
  const tick=()=>new Promise(resolve=>setImmediate(resolve));await tick();await tick();
  assert.equal(nodes.events.children[0].textContent,'[important] <img src=x onerror=alert(1)>');
  assert.equal(nodes.incidents.children.length,1);assert.ok(nodes.connection.textContent.includes('collecting'));
  assert.equal(timers.at(-1).ms,5000);
  nodes.incidents.children[0].listeners.click();await tick();await tick();
  assert.ok(requests.some(r=>r.url==='/api/controller/incidents/'+'a'.repeat(64)));
  assert.ok(nodes['page-status'].textContent.includes('partial or unverified'));
  nodes.next.listeners.click();await tick();await tick();assert.ok(requests.some(r=>r.url==='/api/report?offset=1&q='));assert.equal(nodes.next.disabled,true);
  nodes.filter.value='<img';nodes.filter.listeners.input();await tick();await tick();assert.ok(requests.some(r=>r.url.includes('q=%3Cimg')));
  fail=true;timers.at(-1).f();await tick();assert.ok(nodes.connection.textContent.includes('stale'));assert.equal(nodes.detail.textContent,'Detail unavailable.');
  doc.hidden=true;doc.listeners.visibilitychange();const count=requests.length;await tick();assert.equal(requests.length,count);
  assert.ok(requests.every(r=>r.url.startsWith('/api/')&&(!r.url.includes('?')||r.url.startsWith('/api/report?'))));
  console.log('Go dashboard UI: safe text, fixed routes, sequential polling, detail, stale state and visibility cancellation passed');
})().catch(error=>{console.error(error);process.exitCode=1;});
