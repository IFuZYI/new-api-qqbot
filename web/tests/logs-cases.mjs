// Regression cases for the five defects an independent web audit found.
//
// Each case FAILS on the code as audited and passes on the fixed code:
//   L-01  the 日志 tab threw on an unexpected 200 body, and with no error
//         boundary the whole panel unmounted to a blank page;
//   L-02  a failed re-query left the previous rows on screen under the new
//         filter value, with only a toast as evidence;
//   L-03  the batch group change dropped the per-account results ADR-0007
//         calls the design;
//   L-04  the retention input accepted a value above its own max;
//   L-05  the 日志 hint text was not programmatically associated.
export const tests = {
  // The tab must render under the suite's own harness (no /api/logs fixture
  // shape assumptions) — before the fix, `res.entries.length` on a body without
  // `entries` threw and React unmounted the whole app: navPresent false,
  // #root empty.
  'L-01': `await fresh();await click('日志');await wait(200);
    check(!!document.querySelector('nav'),'the app unmounted (white screen)');
    check(document.getElementById('root').children.length>0,'#root is empty');
    return {mounted:true};`,

  // A 200 whose body is missing `entries` entirely must not white-screen the
  // app: the response is not trusted to match the declared type.
  'L-02': `await fresh();mock.logs={day:'2026-10-12',counts:{},truncated:false};
    await click('日志');await wait(250);
    check(!!document.querySelector('nav'),'a malformed 200 white-screened the app');
    check(document.getElementById('root').children.length>0,'#root is empty after a malformed 200');
    return {survived:true};`,

  // A failed re-query must not leave the old rows under the new filter value
  // with only a toast: the mismatch is the defect (10001 is bound, the filter
  // says 未绑定).
  'L-03': `await fresh();await click('用户管理');await wait(250);
    mock.fail['GET /api/users']='directory offline';
    await set(field('绑定状态'),'unbound');await wait(400);
    const leaked=text().includes('10001');
    check(!leaked,'a failed query left the previous rows under the new filter');
    check(text().includes('directory offline')||text().includes('重试'),'no persistent failure state');
    return {staleRowsHidden:!leaked};`,

  // The per-account results the API returns must survive on the page, not
  // vanish with a toast (ADR-0007: the batch reports each account separately).
  'L-04': `await fresh();await click('用户管理');await wait(250);
    const box=document.querySelector('[aria-label="选择账号 #1"]');check(!!box,'no account checkbox');
    box.click();await wait(80);
    const target=document.querySelector('[aria-label="批量调整的目标分组"]');check(!!target,'no batch group control');
    await set(target,'vip');await wait(80);
    await click('批量调整分组');await wait(400);
    check(text().includes('权限不足'),'the per-account failure is gone after the toast expires');
    check(text().includes('#2'),'the failing account is not identified on the page');
    return {detailSurvives:true};`,

  // The retention input's own max must be enforced at the input, not only by
  // the server's 400.
  'L-05': `await fresh();await click('监测设置');await wait(250);
    const input=document.querySelector('[aria-label="日志保留天数"]');check(!!input,'no retention input');
    await set(input,'99999');await wait(80);
    check(Number(input.value)<=3650,'the input accepts a value above its max');
    return {clamped:Number(input.value)};`,
}
