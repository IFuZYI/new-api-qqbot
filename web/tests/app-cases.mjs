// Real App regressions. Uses the shared cases.mjs helpers and harness.tsx.
export const tests = {
  'reward-held': `for(const state of ['claimed','legacy']){await fresh();Object.assign(mock.rows[0],{checked_today:true,reward_today:77,reward_state:state,reward_error:'fixture unconfirmed'});await click('用户管理');await until(()=>text().includes('77'),'held reward row');check(text().includes('待核对')||text().includes('待确认'),'held reward lacks warning: '+state);await click('概览');await until(()=>text().includes('待确认')||text().includes('待核对'),'overview warning');check(text().includes('待核对')||text().includes('待确认'),'overview lacks warning: '+state);}return {heldAndLegacyVisible:true};`,
  'F-11': `await fresh();mock.rows.push({...mock.rows[0],qq:10003});await click('用户管理');for(let i=0;i<50&&!document.querySelector('[aria-label="全选可调整的账号"]');i++)await wait();const all=document.querySelector('[aria-label="全选可调整的账号"]');all.click();await wait();check(all.checked,'duplicate account breaks checked state');all.click();await wait();check(!document.querySelector('[aria-label="选择账号 #1"]').checked,'select all cannot clear');return {deduplicated:true};`,
  'F-14': `await fresh();await click('用户管理');await until(()=>document.querySelector('[aria-label="账号 #1 的分组"]'),'account group control');const one=document.querySelector('[aria-label="账号 #1 的分组"]');mock.hold=['POST /api/users/1/group'];await set(one,'vip');check(one.disabled,'single group change remains editable');await set(one,'gold');check(writes('/api/users/1/group').length===1,'concurrent group write');mock.release(0,{ok:true});await until(()=>!one.disabled,'group control unlocked');check(!one.disabled,'group control never unlocked');return {writes:1};`,
  'F-07': `await fresh();mock.expired=true;await click('监测设置');await wait(200);check(!document.querySelector('nav')&&document.querySelector('input[type="password"]'),'401 did not return to login');return {login:true};`,
  'F-05-06': `
    await fresh(); await click('用户管理'); await wait(250); await click('换绑'); await wait(250);
    const dialog=document.querySelector('[role=dialog]');
    const save=[...dialog.querySelectorAll('button')].find(b=>b.textContent.trim()==='保存');
    check(!save.disabled,'binding save stayed disabled (candidates never loaded)');
    mock.hold=['POST /api/bindings']; save.click(); save.click(); await wait();
    check(writes('/api/bindings').length===1,'duplicate binding submitted');
    check(save.disabled&&[...dialog.querySelectorAll('button')].find(b=>b.textContent.trim()==='取消').disabled,'binding controls remain enabled');
    check(document.querySelector('[aria-label="选择 QQ 号"]').disabled,'target editable during binding');
    mock.release(0,{binding:{qq:10001,newapi_user_id:1},rules:[],rule_error:'fixture conversion failed'}); await wait();
    check(text().includes('fixture conversion failed')&&text().includes('绑定'),'partial success lost');
    return {writes:writes('/api/bindings').length,partialFailureVisible:true};
  `,
  'F-04': `
    await fresh(); await click('用户管理'); await set(field('类型'),'account');
    mock.hold=['GET /api/users']; await click('绑定 QQ');
    check(mock.pending.length===1&&mock.pending[0].query==='', 'binding candidates must request the unfiltered directory');
    check(btn('保存').disabled, 'saving before candidates load must be blocked');
    mock.release(0,{error:'candidate load failed'},500); await wait();
    check(text().includes('candidate load failed')&&btn('重试')&&btn('保存').disabled,'candidate failure needs disabled save and retry');
    mock.hold=[]; await click('重试');
    await until(()=>document.querySelector('[aria-label="选择 QQ 号"]')?.disabled===false,"QQ candidates loaded");
    const qq=document.querySelector('[aria-label="选择 QQ 号"]'); qq.focus(); qq.click(); await until(()=>document.querySelector('[role="listbox"]'),"QQ listbox opened");
    check(document.querySelector('[role="listbox"]').textContent.includes('10002'),'account filter hid QQ candidates');
    await click('取消'); await set(field('类型'),'member');
    await click('绑定');
    await until(()=>document.querySelector('[aria-label="选择 new-api 账号"]')?.disabled===false,"account candidates loaded");
    const account=document.querySelector('[aria-label="选择 new-api 账号"]'); account.focus(); account.click(); await until(()=>document.querySelector('[role="listbox"]'),"account listbox opened");
    check(document.querySelector('[role="listbox"]').textContent.includes('beta'),'member filter hid account candidates');
    await click('取消');
    return {unfilteredCandidates:true,retry:true,bothDirections:true};
  `,

  // S-4: the retention field is an int on the server, and a range-only clamp
  // let "1.5" through — the operator got a 400 naming neither the field nor the
  // reason. The clamp must also round.
  'F-15': `
    await fresh(); await click('监测设置'); await wait(250);
    const input=document.querySelector('[aria-label="日志保留天数"]'); check(!!input,'no retention input');
    await set(input,'1.5'); await wait(80);
    check(input.value==='2'||input.value==='1','a non-integer retention must be rounded, got '+input.value);
    await set(input,'99999'); await wait(80);
    check(Number(input.value)<=3650,'retention above the cap must be clamped, got '+input.value);
    return {rounded:input.value};
  `,

  // S-5: a hard failure of the next batch must clear the previous batch's note.
  // It described a DIFFERENT run; leaving it under the new failure's toast read
  // as if it belonged to it.
  'F-16': `
    await fresh(); await click('用户管理');
    // The batch controls render only once something is selected, so select first.
    await until(()=>document.querySelectorAll('input[type="checkbox"]').length>0,'account checkboxes');
    const boxes=[...document.querySelectorAll('input[type="checkbox"]')];
    boxes[0].click(); await wait();
    await until(()=>text().includes('已选'),'selection banner');
    await set(document.querySelector('[aria-label="批量调整的目标分组"]'),'vip'); await wait();
    await click('批量调整分组'); await wait(200);
    check(text().includes('批量调整到'),'no batch note after the first run');
    // A successful batch clears the selection (and with it the controls), so
    // select again for the second run.
    const boxes2=[...document.querySelectorAll('input[type="checkbox"]')];
    boxes2[0].click(); await wait();
    await until(()=>text().includes('已选'),'second selection banner');
    await set(document.querySelector('[aria-label="批量调整的目标分组"]'),'vip'); await wait();
    // A second batch that fails hard: the note must go.
    mock.fail['POST /api/users/groups']='gateway down';
    await click('批量调整分组'); await wait(200);
    check(!text().includes('批量调整到'),'a stale batch note survived the next hard failure');
    return {staleCleared:true};
  `,
};
