// Real React components, isolated fetch fixtures, real headless Chromium/CDP.
// Run: node web/tests/regression.mjs [F-01 ...]
import {spawn} from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import http from 'node:http';
import {createRequire} from 'node:module';
import {fileURLToPath} from 'node:url';
import {tests as baseTests, helpers} from './cases.mjs';
import {tests as appTests} from './app-cases.mjs';
import {tests as formsTests} from './forms-cases.mjs';
import {tests as logsTests} from './logs-cases.mjs';
const tests = {...baseTests, ...appTests, ...formsTests, ...logsTests};
const dir=path.dirname(fileURLToPath(import.meta.url));
const require=createRequire(path.join(dir,'../package.json'));
const scratch=fs.mkdtempSync(path.join(process.env.TMPDIR||os.tmpdir(),'qqbot-regression-'));
require('esbuild').buildSync({entryPoints:[dir+'/harness.tsx'],outfile:scratch+'/bundle.js',bundle:true,format:'esm',jsx:'automatic',define:{'process.env.NODE_ENV':'"development"'}});
const chromePath=process.env.CHROMIUM_PATH||['/root/.cache/ms-playwright/chromium-1243/chrome-linux64/chrome','/usr/bin/chromium','/usr/bin/chromium-browser'].find(p=>fs.existsSync(p));
if(!chromePath)throw Error('Set CHROMIUM_PATH to a Chromium executable');
const server=http.createServer((req,res)=>{res.setHeader('Content-Type',req.url==='/bundle.js'?'text/javascript':'text/html');res.end(req.url==='/bundle.js'?fs.readFileSync(scratch+'/bundle.js'):'<!doctype html><html lang="zh-CN"><div id="root"></div><script type="module" src="/bundle.js"></script></html>')});
await new Promise(r=>server.listen(0,'127.0.0.1',r));
const origin='http://127.0.0.1:'+server.address().port;
const wait=n=>new Promise(r=>setTimeout(r,n));
let chrome,ws,id=0;const pending=new Map(),consoleEvents=[],results=[];
const cdp=(method,params={})=>new Promise((resolve,reject)=>{const n=++id;pending.set(n,{resolve,reject});ws.send(JSON.stringify({id:n,method,params}))});
const evaluate=async expression=>{const r=await cdp('Runtime.evaluate',{expression,awaitPromise:true,returnByValue:true});if(r.exceptionDetails)throw Error(r.exceptionDetails.exception?.description||JSON.stringify(r.exceptionDetails));return r.result.value};
try{
 chrome=spawn(chromePath,['--headless=new','--no-sandbox','--disable-dev-shm-usage','--disable-background-timer-throttling','--disable-renderer-backgrounding','--remote-debugging-port=0','--user-data-dir='+scratch+'/profile','about:blank'],{stdio:['ignore','ignore','inherit']});
 let port;
 for(let i=0;;i++){try{port=Number(fs.readFileSync(scratch+'/profile/DevToolsActivePort','utf8').split('\n')[0]);await fetch(`http://127.0.0.1:${port}/json/version`);break}catch{if(chrome.exitCode!==null||i>100)throw Error('Chromium startup failed');await wait(100)}}
 const tab=await(await fetch(`http://127.0.0.1:${port}/json/new?${origin}`,{method:'PUT'})).json();
 ws=new WebSocket(tab.webSocketDebuggerUrl);await new Promise((r,j)=>{ws.onopen=r;ws.onerror=j});
 ws.onmessage=e=>{const m=JSON.parse(e.data);if(m.id){const p=pending.get(m.id);pending.delete(m.id);m.error?p.reject(m.error):p.resolve(m.result)}else if(m.method==='Runtime.exceptionThrown'||m.method==='Runtime.consoleAPICalled'&&['error','warning'].includes(m.params.type))consoleEvents.push(m)};
 await cdp('Runtime.enable');await cdp('Page.enable');await cdp('Page.navigate',{url:origin});await cdp('Page.bringToFront');
 for(let i=0;i<100;i++){if(await evaluate('typeof window.mock === "object"'))break;await wait(100)}
 await evaluate(helpers);
 for(const [name,test] of Object.entries(tests)){
  if(process.argv.length>2&&!process.argv.slice(2).includes(name))continue;
  try{const evidence=await evaluate(`(async()=>{${test}})()`);results.push({name,passed:true,evidence});console.log('PASS '+name+' '+JSON.stringify(evidence))}
  catch(e){results.push({name,passed:false,error:e.message});console.log('FAIL '+name+' '+e.message);process.exitCode=1}
 }
 if(!results.length)throw Error('No tests selected');
 // F13 (dialog keyboard) belongs to the next round, not this selected suite.
 console.log('BROWSER CONSOLE '+JSON.stringify(consoleEvents));
 if(consoleEvents.length)process.exitCode=1;
 const report={results,consoleEvents};
 if(process.env.REPORT)fs.writeFileSync(process.env.REPORT,JSON.stringify(report,null,2));
 console.log(`RESULT ${results.filter(x=>x.passed).length}/${results.length}`);
}finally{
 ws?.close();if(chrome&&chrome.exitCode===null){chrome.kill('SIGTERM');await Promise.race([new Promise(r=>chrome.once('exit',r)),wait(3000)]);if(chrome.exitCode===null)chrome.kill('SIGKILL')}
 await new Promise(r=>server.close(r));fs.rmSync(scratch,{recursive:true,force:true});console.log('TEARDOWN complete');
}
