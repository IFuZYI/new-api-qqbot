export const helpers=`
window.wait=(n=80)=>new Promise(r=>setTimeout(r,n));
window.until=async(predicate,label)=>{for(let i=0;i<100;i++){if(predicate())return;await wait(50)}throw Error('timed out: '+label)};
window.text=()=>document.body.innerText;
window.btn=(s,scope=document)=>[...scope.querySelectorAll('button')].find(x=>x.textContent.trim()===s);
window.click=async(s,scope=document)=>{let b;for(let i=0;i<50;i++){b=btn(s,scope);if(b)break;await wait()}if(!b)throw Error('no button '+s);b.click();await wait()};
window.set=async(el,v)=>{if(!el)throw Error('no control '+v+'; page: '+text());const proto=el instanceof HTMLSelectElement?HTMLSelectElement.prototype:el instanceof HTMLTextAreaElement?HTMLTextAreaElement.prototype:HTMLInputElement.prototype;Object.getOwnPropertyDescriptor(proto,'value').set.call(el,v);el.dispatchEvent(new Event(el instanceof HTMLSelectElement?'change':'input',{bubbles:true}));await wait()};
window.field=s=>[...document.querySelectorAll('label')].find(x=>x.firstElementChild?.textContent===s)?.querySelector('input,select,textarea');

// fresh() resets the fixture and waits for the app to be USABLE, not for a
// fixed delay.
//
// A flat 180 ms was the flake source measured by the web audit: give a fixture
// endpoint the latency a real one has and F-03/F-14/reward-held fail because
// set() runs before the control exists, or before the data it renders has
// arrived. Polling the actual precondition is faster in the common case and
// immune to a slow machine.
window.appReady=()=>{
  if(!document.querySelector('nav'))return false;
  // A card still showing its loading state means a first query has not settled;
  // tabs render their controls only after that.
  if(text().includes('载入中…'))return false;
  return true;
};
window.fresh=async()=>{mock.reset();restart();
  for(let i=0;i<40;i++){if(document.querySelector('nav'))break;await wait(25)};
  for(let i=0;i<80;i++){if(appReady())break;await wait(25)};
  await wait(20);
};
window.check=(ok,msg)=>{if(!ok)throw Error(msg)};
window.writes=p=>mock.calls.filter(x=>x.p===p&&x.method!=='GET');
`;
export const tests={
 'F-03':`await fresh();await click('连接配置');await until(()=>field('WebUI 地址'),'WebUI 地址 field');await set(field('WebUI 地址'),'http://draft.invalid');mock.hold=['POST /api/config/newapi'];await click('保存');await set(field('WebUI 地址'),'http://newer-draft.invalid');mock.release(0,{config:mock.config,revisions:{newapi:'n2',napcat:'unrelated-new'}});await wait();check(field('WebUI 地址').value==='http://newer-draft.invalid','saving gateway erased NapCat draft');mock.hold=[];const saves=[...document.querySelectorAll('button')].filter(b=>b.textContent.trim()==='保存');saves[1].click();await wait();check(writes('/api/config/napcat')[0].body.revision==='p','unrelated revision advanced');await set(field('URL'),'https://gateway-draft.invalid');await click('自动配置');await click('开始配置');check(field('URL').value==='https://gateway-draft.invalid','autoconfigure erased gateway draft');mock.fail['POST /api/config/napcat']='section save failed';await set(field('WebUI 地址'),'http://failed-draft.invalid');[...document.querySelectorAll('button')].filter(b=>b.textContent.trim()==='保存')[1].click();await wait();check(field('WebUI 地址').value==='http://failed-draft.invalid'&&field('URL').value==='https://gateway-draft.invalid','failed save erased drafts');return {preserved:true,napcatRevision:'p'};`,

 'F-02':`await fresh();await click('用户管理');mock.hold=['GET /api/users'];await set(field('绑定状态'),'bound');await set(field('绑定状态'),'unbound');check(mock.pending.length===2,'missing delayed directory requests');mock.release(1,mock.userData([mock.rows[1]]));await wait();mock.release(0,mock.userData([mock.rows[0]]));await wait();check(text().includes('10002')&&!text().includes('10001'),'old response replaced latest filter');await click('刷新');await click('刷新');mock.release(3,mock.userData([mock.rows[1]]));await wait();mock.release(2,{error:'obsolete directory failure'},500);await wait();check(!text().includes('obsolete directory failure'),'stale error leaked');return {requests:mock.pending.length,visible:'10002'};`,

 'F-01':`await fresh();mock.hold=['GET /api/groups'];await click('群管理');check(btn('保存').disabled,'save enabled before groups load');await click('保存');check(!writes('/api/groups').length,'empty list submitted');mock.release(0,{error:'groups offline'},500);await wait();check(btn('保存').disabled&&btn('重试'),'failure must disable save and offer retry');mock.hold=[];await click('重试');check(!btn('保存').disabled,'retry did not recover');await click('保存');check(writes('/api/groups')[0].body.group_ids[0]===123,'enabled group lost');return {saved:mock.groups};`,
};
