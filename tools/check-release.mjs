// Check source, package boundaries and fixture privacy without printing matched values.
import fs from 'node:fs';
import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import path from 'node:path';
const forbidden = new RegExp(['ru'+'st', 'car'+'go', '\\.r'+'s\\b'].join('|'), 'i');
const secrets = /(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{30,}|AKIA[0-9A-Z]{16}|sk-[A-Za-z0-9_-]{40,})/;
const personal = /\/(?:Users|home)\/[A-Za-z0-9_.-]+\//;
const privateParts = new Set(['.cache','.runs','reports','.git','.kube','dist','bin','build']);
const binary = /\.(gif|png|mp4|jpg|jpeg|ico)$/i;
const privateExtensions = new Set(['.pem','.key','.db','.sqlite','.pyc']);
function readRegular(name, encoding) {
  let fd;
  try {
    fd = fs.openSync(name, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW);
    if (!fs.fstatSync(fd).isFile()) throw new Error('not a regular file');
    return fs.readFileSync(fd, encoding);
  } catch (error) {
    throw new Error(`Unable to read regular source file: ${name}`, {cause:error});
  } finally {
    if (fd !== undefined) fs.closeSync(fd);
  }
}
const listed = execFileSync('git',['ls-files','--cached','--others','--exclude-standard','-z'],{encoding:'utf8'}).split('\0').filter(Boolean);
const files = [...new Set(listed)].sort();
let textCount = 0;
for (const name of files) {
  const parts = name.split('/');
  const base = path.posix.basename(name);
  if (parts.some(p=>privateParts.has(p)) || base==='.env' || base==='kubeconfig' || privateExtensions.has(path.posix.extname(base))) throw new Error(`Private/build path in source: ${name}`);
  const content = readRegular(name, binary.test(name) ? undefined : 'utf8');
  if (binary.test(name)) continue;
  textCount++;
  if (forbidden.test(name) || forbidden.test(content)) throw new Error(`Retired-language reference in ${name}`);
  if (secrets.test(content) || personal.test(content)) throw new Error(`Potential credential/personal path in ${name}; values suppressed`);
  if (/\.py$/.test(name)) throw new Error(`Unexpected interpreter dependency: ${name}`);
}
const listAt = process.argv.indexOf('--write-list');
if (listAt>=0) fs.writeFileSync(process.argv[listAt+1],files.join('\n')+'\n');
const archiveAt = process.argv.indexOf('--archive');
if (archiveAt>=0) {
  const archive=process.argv[archiveAt+1];
  const entries=execFileSync('tar',['-tzf',archive],{encoding:'utf8'}).trim().split('\n');
  if (entries.length!==files.length || entries.some((name,i)=>name!==files[i])) throw new Error('Archive contents differ from checked source');
  for (const name of entries) { const data=execFileSync('tar',['-xOzf',archive,name],{maxBuffer:64*1024*1024});if (!data.equals(readRegular(name))) throw new Error(`Archive bytes differ: ${name}`); }
}
const resources=JSON.parse(readRegular('deploy/base/resources.json','utf8'));
assert.equal(resources.apiVersion,'v1');
assert.equal(resources.kind,'List');
const deployments=resources.items.filter(r=>r.kind==='Deployment');
assert.equal(deployments.length,1);
const deployment=deployments[0];
assert.equal(deployment.spec.replicas,1);
assert.equal(deployment.spec.strategy.type,'Recreate');
const pod=deployment.spec.template.spec;
assert.equal(pod.securityContext.fsGroup,65532);
assert.equal(pod.containers.length,1);
assert.equal(pod.initContainers.length,1);
const main=pod.containers[0], init=pod.initContainers[0];
assert.ok(main.image);
assert.equal(init.image,main.image);
assert.equal(init.imagePullPolicy,main.imagePullPolicy);
assert.deepEqual(init.command,['/usr/local/bin/jevernetes']);
assert.deepEqual(init.args,['prepare-state-volume','/state-volume']);
assert.deepEqual(init.volumeMounts,[{name:'state',mountPath:'/state-volume'}]);
assert.equal((init.env??[]).length,0);
assert.equal((init.envFrom??[]).length,0);
assert.deepEqual(main.command??[],[]);
assert.equal(main.args[0],'controller');
assert.equal(main.args.filter(a=>a==='--state').length,1);
assert.equal(main.args[main.args.indexOf('--state')+1],'/var/lib/jevernetes/state.db');
assert.deepEqual(main.volumeMounts.filter(m=>m.name==='state'),[{name:'state',mountPath:'/var/lib/jevernetes',subPath:'private'}]);
assert.deepEqual(pod.volumes.find(v=>v.name==='state'),{name:'state',persistentVolumeClaim:{claimName:'controller-state'}});
for (const container of [main,init]) {
  const sc={...pod.securityContext,...container.securityContext};
  assert.equal(sc.runAsNonRoot,true);
  assert.equal(sc.runAsUser,65532);
  assert.equal(sc.runAsGroup,65532);
  assert.equal(sc.seccompProfile.type,'RuntimeDefault');
  assert.equal(sc.privileged??false,false);
  assert.equal(sc.allowPrivilegeEscalation,false);
  assert.equal(sc.readOnlyRootFilesystem,true);
  assert.deepEqual(sc.capabilities.drop,['ALL']);
  assert.deepEqual(sc.capabilities.add??[],[]);
}
for (const resource of resources.items) {
  if (resource.kind==='Role') for (const rule of resource.rules) {
    if (rule.resources.some(r=>!['pods','pods/log'].includes(r)) || rule.verbs.some(v=>!['get','list','watch'].includes(v))) throw new Error('Controller RBAC exceeds collection needs');
  }
  if (resource.kind==='Service' && resource.spec.ports.some(p=>p.port===9091)) throw new Error('Inspection must not be exposed by a Service');
}
const docker=readRegular('Dockerfile','utf8');
if (!docker.includes('CGO_ENABLED=0 go build')||!docker.includes('USER 65532:65532')||!docker.includes('ARG GO_IMAGE')) throw new Error('Container build contract');
assert.match(docker,/ENTRYPOINT \["\/usr\/local\/bin\/jevernetes"\]/);
console.log(`Checked ${files.length} source files (${textCount} text): retired-language references=0; credential/personal-path signatures=0; private/build paths=0; controller/container checks passed.`);
