// Form regressions against the real App via the shared Chromium harness.
export const tests = {
  'F-12': `await fresh();mock.fail['GET /api/newapi/groups']='groups offline';await click('分组转换');check(btn('新建规则').disabled,'unavailable groups allow unusable rule creation');check(!text().includes('需手动填写'),'false manual fallback');delete mock.fail['GET /api/newapi/groups'];await click('重试');check(!btn('新建规则').disabled,'retry did not restore creation');return {retry:true};`,
  'F-13': `await fresh();await click('用户管理');await wait(250);await click('换绑');await wait(250);let dialog=document.querySelector('[role="dialog"]');const save=[...dialog.querySelectorAll('button')].find(b=>b.textContent.trim()==='保存');check(!save.disabled,'binding save stayed disabled (candidates never loaded)');mock.hold=['POST /api/bindings'];save.click();await wait(150);check(save.disabled,'write never entered the busy state');dialog.querySelector('[aria-label="关闭"]').click();await wait();check(dialog.isConnected,'busy close button dismissed dialog');dialog.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}));await wait();check(dialog.isConnected,'busy Escape dismissed dialog');dialog.parentElement.dispatchEvent(new MouseEvent('mousedown',{bubbles:true}));await wait();check(dialog.isConnected,'busy backdrop dismissed dialog');mock.release(0,{binding:{qq:10001,newapi_user_id:1},rules:[]});await wait();check(!dialog.isConnected,'success did not close');return {allClosePathsBlocked:true};`,
  'F-09': `await fresh();await click('监测设置');const command=[...document.querySelectorAll('textarea')].at(-1);await set(command,'/[broken/');mock.fail['POST /api/settings']='invalid regex';await click('保存');check(command.isConnected&&command.value==='/[broken/','failed save erased draft');check(text().includes('invalid regex'),'error missing');return {draftPreserved:true};`,
  'F-10': `await fresh();await click('分组转换');await click('编辑');const interval=document.querySelector('[aria-label="巡检周期（分钟）"]');await set(interval,'');check(interval.isConnected,'clearing interval unmounted input');check(btn('保存').disabled,'empty enabled interval can save');await set(interval,'30');check(!btn('保存').disabled,'valid interval cannot save');return {editorPreserved:true};`,
  'F-08': `
    await fresh(); await click('监测设置');
    const mode = () => document.querySelector('[aria-label="签到触发方式"]');
    const command = () => [...document.querySelectorAll('textarea')].at(-1);
    await set(command(), '');
    check(mode().value === 'command', 'clearing command switched to any-message mode');
    check(command()?.value === '' && document.querySelectorAll('textarea').length === 2, 'command editor unmounted');
    check(btn('保存').disabled, 'empty command can be saved');
    await click('保存'); check(writes('/api/settings').length === 0, 'empty command was posted');
    await set(command(), '   '); check(btn('保存').disabled, 'whitespace command can be saved');
    await set(command(), '/新的签到');
    await set(mode(), 'any'); await set(mode(), 'command');
    check(command().value === '/新的签到', 'mode toggle erased command draft');
    await click('保存');
    check(writes('/api/settings').at(-1).body.checkin_patterns === '/新的签到', 'command payload differs');
    await set(mode(), 'any'); await click('保存');
    check(writes('/api/settings').at(-1).body.checkin_patterns === '', 'explicit any-message mode not serialized');
    return {emptyBlocked:true, commandPreserved:true, explicitAnySaved:true};
  `,
};
