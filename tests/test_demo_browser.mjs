// Optional integration check: requires Chrome. No API or cluster access.
import assert from 'node:assert/strict';
import {mkdtemp,rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {launchDemoBrowser} from '../tools/demo-browser.mjs';
const profile=await mkdtemp(join(tmpdir(),'jevernetes-browser-test-'));
let browser;
try {
  browser=await launchDemoBrowser(profile);
  const hostile='";globalThis.injected=true;//</script>\u2028';
  await browser.call(value=>{const p=document.createElement('p');p.id='evidence';p.textContent=value;document.body.append(p);},hostile);
  assert.equal(await browser.call(selector=>document.querySelector(selector).textContent,'#evidence'),hostile);
  assert.equal(await browser.evaluate('typeof globalThis.injected'),'undefined');
  assert.deepEqual(await browser.call(value=>value,{x:12,y:34}),{x:12,y:34});
  console.log('Recorder security: private pipe transport and separate data arguments passed');
} finally {
  await browser?.close();
  await rm(profile,{recursive:true,force:true,maxRetries:3});
}
