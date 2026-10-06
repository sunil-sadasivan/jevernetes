// Native binary smoke checks: synthetic stdin only, no credentials or sockets.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import {spawn,spawnSync} from 'node:child_process';
const binary='./dist/bin/jevernetes';
assert.ok(fs.readFileSync(binary).equals(fs.readFileSync('./dist/bin/jev')));
// This pair tests warning-based offline triage, not semantic security accuracy.
// An unflagged benign event remains uncertain; local rules do not certify safety.
const risky='WARN synthetic configuration changed: authentication disabled on public admin endpoint';
const benign='INFO synthetic configuration reload completed with no changes';
const temporary=fs.mkdtempSync('./dist/offline-smoke-');
try {
  for(const executable of [binary,'./dist/bin/jev']) {
    for(const lines of [[risky,benign],[benign,risky]]) {
      const output=`${temporary}/report.json`;
      fs.writeFileSync(output,'stale report\n');
      const child=spawnSync(executable,['files','-','--offline','--json','--output',output],{
        input:lines.join('\n')+'\n',encoding:'utf8',env:{},timeout:10000,maxBuffer:1024*1024,
      });
      assert.ifError(child.error);assert.equal(child.signal,null);assert.equal(child.status,0);assert.equal(child.stderr,'');
      const report=JSON.parse(child.stdout);
      assert.deepEqual(JSON.parse(fs.readFileSync(output,'utf8')),report);
      assert.equal(fs.statSync(output).mode & 0o777,0o600);
      assert.equal(report.runtime,'go');assert.equal(report.schema_version,2);assert.equal(report.mode,'offline-rules');
      assert.equal(report.summary.events,2);assert.equal(report.summary.important,1);assert.equal(report.summary.uncertain,1);
      assert.equal(report.summary.routine,0);assert.equal(report.summary.unknown,0);
      assert.equal(report.summary.total_api_requests,0);assert.equal(report.summary.total_estimated_cost_usd,0);
      assert.equal(report.provider_usage.risk.request_attempts,0);assert.equal(report.provider_usage.template,null);
      assert.deepEqual(report.events.map(event=>event.text),lines);
      const flagged=report.events.find(event=>event.text===risky);
      const unflagged=report.events.find(event=>event.text===benign);
      assert.equal(flagged.importance,'important');assert.equal(flagged.severity,'degraded');
      assert.deepEqual(flagged.baseline.signals,['warning_level']);
      assert.equal(unflagged.importance,'uncertain');assert.equal(unflagged.severity,'info');
      assert.deepEqual(unflagged.baseline.signals,[]);
      assert.equal(report.important_groups.length,1);
      assert.equal(report.important_groups[0].text,risky);
      assert.deepEqual(report.important_groups[0].event_ids,[flagged.id]);
    }
  }
} finally {
  fs.rmSync(temporary,{recursive:true,force:true});
}
for(const signal of ['SIGINT','SIGTERM']) {
  await new Promise((resolve,reject)=>{
    const child=spawn(binary,['files','-','--offline','--json'],{env:{},stdio:['pipe','pipe','pipe']});
    let stdout='',stderr='';
    const deadline=setTimeout(()=>{child.kill('SIGKILL');reject(new Error('Shutdown deadline exceeded'));},10000);
    child.on('error',error=>{clearTimeout(deadline);reject(error);});
    child.stdout.on('data',data=>{stdout+=data;if(stdout.length>1024*1024)child.kill('SIGKILL');});
    child.stderr.on('data',data=>{stderr+=data;});
    child.stdin.on('error',()=>{});
    child.stdin.write('INFO synthetic first\nERROR synthetic second\n');
    const interrupt=setTimeout(()=>child.kill(signal),300);
    child.on('close',(code,killed)=>{
      clearTimeout(deadline);clearTimeout(interrupt);
      try {assert.equal(code,130);assert.equal(killed,null);assert.equal(stderr,'');assert.equal(JSON.parse(stdout).runtime,'go');resolve();}catch(error){reject(error);}
    });
  });
}
console.log('Native smoke passed: both executables distinguish the synthetic warning change from the benign event in both input orders; stdout/private saved reports agree; zero reported provider attempts; identical executable alias; SIGINT and SIGTERM final reporting with blocked stdin. This fixture does not establish semantic security accuracy.');

// Stop at invalid listener setup, after SQLite opens but before cluster setup.
const stateTemporary=fs.mkdtempSync('./dist/state-smoke-');
try {
  const volume=`${stateTemporary}/volume`;
  fs.mkdirSync(volume,{mode:0o700});
  const openState=state=>{
    const result=spawnSync(binary,['controller','--namespace','synthetic','--state',state,'--offline','--listen','invalid-address'],{env:{},encoding:'utf8',timeout:10000});
    assert.ifError(result.error);
    assert.equal(result.status,1);
    assert.equal(result.stderr.trim(),'health listener unavailable');
    assert.equal(fs.statSync(state).mode&0o777,0o600);
    assert.equal(fs.readFileSync(state).subarray(0,16).toString(),'SQLite format 3\0');
    assert.equal(fs.existsSync(`${state}-shm`),false);
  };
  openState(`${volume}/state.db`);
  const original=fs.readFileSync(`${volume}/state.db`);
  // SHM preflight applies even to DELETE databases, before SQLite is initialized.
  // Real crashed-WAL recovery and FIFO deadlines are covered by the Go tests.
  const external=`${stateTemporary}/external`;
  const sentinel=Buffer.from('synthetic external target must remain unchanged');
  fs.writeFileSync(external,sentinel,{mode:0o600});
  fs.linkSync(external,`${volume}/state.db-shm`);
  const rejected=spawnSync(binary,['controller','--namespace','synthetic','--state',`${volume}/state.db`,'--offline','--listen','invalid-address'],{env:{},encoding:'utf8',timeout:10000});
  assert.ifError(rejected.error);
  assert.equal(rejected.status,1);
  assert.equal(rejected.stderr.trim(),'controller state operation failed');
  assert.ok(sentinel.equals(fs.readFileSync(external)));
  assert.ok(original.equals(fs.readFileSync(`${volume}/state.db`)));
  fs.unlinkSync(`${volume}/state.db-shm`);
  fs.chmodSync(volume,0o770);
  for (const name of ['state.db','state.db.lock']) fs.chmodSync(`${volume}/${name}`,0o660);
  const resources=JSON.parse(fs.readFileSync('deploy/base/resources.json','utf8'));
  const init=resources.items.find(r=>r.kind==='Deployment').spec.template.spec.initContainers[0];
  const prepare=()=>{
    const args=init.args.map(a=>a==='/state-volume'?volume:a);
    const result=spawnSync(binary,args,{env:{},encoding:'utf8',timeout:10000});
    assert.ifError(result.error);
    assert.equal(result.status,0,result.stderr);
    assert.equal(result.stdout,'');
    assert.equal(result.stderr,'');
    assert.equal(fs.statSync(`${volume}/private`).mode&0o777,0o700);
  };
  prepare(); prepare();
  assert.ok(original.equals(fs.readFileSync(`${volume}/private/state.db`)));
  assert.equal(fs.existsSync(`${volume}/state.db`),false);
  // Model subPath's direct mount beneath protected image directories.
  fs.renameSync(`${volume}/private`,`${stateTemporary}/mounted`);
  openState(`${stateTemporary}/mounted/state.db`);
  prepare(); prepare(); // Empty-volume initialization is also repeatable.
  console.log('Native state smoke passed: SHM hardlink rejected without target changes, no unused DELETE SHM, manifest preparation command, legacy migration without byte changes, repeatable clean initialization, private modes and SQLite reopen; empty child environments, no cluster/provider access.');
} finally {
  fs.rmSync(stateTemporary,{recursive:true,force:true});
}
