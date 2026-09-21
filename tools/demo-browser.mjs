// Isolated Chrome recorder transport: inherited pipes, never a network debug URL.
import {spawn} from 'node:child_process';
import {once} from 'node:events';

export async function launchDemoBrowser(profile) {
  const chrome=spawn(process.env.CHROME_BIN||'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',[
    '--headless=new','--disable-gpu','--no-first-run','--no-default-browser-check',
    '--disable-background-networking','--disable-component-update','--disable-sync',
    '--remote-debugging-pipe',`--user-data-dir=${profile}`,'about:blank',
  ],{stdio:['ignore','ignore','ignore','pipe','pipe']});
  let sequence=0,buffer=Buffer.alloc(0),sessionId;
  const pending=new Map(),listeners=new Set();
  function fail(error){for(const entry of pending.values()){clearTimeout(entry.timer);entry.reject(error);}pending.clear();}
  chrome.on('error',fail);
  chrome.on('exit',()=>fail(new Error('Demo browser exited')));
  chrome.stdio[3].on('error',fail);
  chrome.stdio[4].on('data',chunk=>{
    buffer=Buffer.concat([buffer,chunk]);
    let end;
    while((end=buffer.indexOf(0))!==-1){
      const raw=buffer.subarray(0,end);buffer=buffer.subarray(end+1);
      if(!raw.length)continue;
      let message;try{message=JSON.parse(raw.toString('utf8'));}catch{fail(new Error('Invalid browser response'));return;}
      const entry=pending.get(message.id);
      if(entry){pending.delete(message.id);clearTimeout(entry.timer);message.error?entry.reject(new Error(JSON.stringify(message.error))):entry.resolve(message.result);}
      else for(const listener of listeners)listener(message);
    }
  });
  const cdp=(method,params={})=>new Promise((resolve,reject)=>{
    const id=++sequence;
    const timer=setTimeout(()=>{pending.delete(id);reject(new Error(`Browser command timed out: ${method}`));},30000);
    pending.set(id,{resolve,reject,timer});
    const target=sessionId&&!method.startsWith('Browser.')&&!method.startsWith('Target.')?{sessionId}:{};
    chrome.stdio[3].write(JSON.stringify({id,method,params,...target})+'\0');
  });
  const unwrap=result=>{if(result.exceptionDetails)throw new Error(JSON.stringify(result.exceptionDetails));return result.result.value;};
  // evaluate is reserved for literal source. Variable data uses call arguments.
  const evaluate=async expression=>unwrap(await cdp('Runtime.evaluate',{expression,awaitPromise:true,returnByValue:true}));
  const call=async(fn,...values)=>{
    const context=await cdp('Runtime.evaluate',{expression:'globalThis'});
    try{return unwrap(await cdp('Runtime.callFunctionOn',{
      objectId:context.result.objectId,functionDeclaration:fn.toString(),
      arguments:values.map(value=>({value})),awaitPromise:true,returnByValue:true,
    }));}finally{await cdp('Runtime.releaseObject',{objectId:context.result.objectId});}
  };
  const close=async()=>{
    const exited=once(chrome,'exit');chrome.kill();
    if(chrome.exitCode===null)await exited;
    fail(new Error('Demo browser closed'));
  };
  try{
    const {targetId}=await cdp('Target.createTarget',{url:'about:blank'});
    ({sessionId}=await cdp('Target.attachToTarget',{targetId,flatten:true}));
    return {cdp,evaluate,call,onEvent:listener=>listeners.add(listener),close};
  }catch(error){await close();throw error;}
}
