import { expect, test, type Page } from '@playwright/test';

const token = 'e2e-test-admin-token-0123456789abcdef';
const uuid = 'GPU-MOCK-001';
async function login(page: Page) {
  await page.goto('/');
  await page.getByLabel('Admin token', {exact:true}).fill(token);
  await page.getByRole('button',{name:'Open console'}).click();
  await expect(page.getByRole('heading',{name:'GPU cooling',exact:true})).toBeVisible();
  await expect(page.locator('.metric-value').first()).not.toHaveText('—°C');
}
async function api(page: Page,path:string,method='GET',body?:unknown,revision?:number) {
  return page.evaluate(async ({path,method,body,revision})=>{
    const session=await fetch('/api/v1/session').then(r=>r.json());
    const response=await fetch('/api/v1'+path,{method,headers:{'Content-Type':'application/json','X-CSRF-Token':session.csrf_token,...(revision===undefined?{}:{'If-Match':`"${revision}"`})},body:body===undefined?undefined:JSON.stringify(body)});
    return {status:response.status,body:await response.json()};
  },{path,method,body,revision});
}

test('sign-in, live telemetry, passive GPU and responsive layout',async({page})=>{
  await page.goto('/');
  await page.getByLabel('Admin token',{exact:true}).fill('incorrect-token');
  await page.getByRole('button',{name:'Open console'}).click();
  await expect(page.getByRole('alert')).toBeVisible();
  await page.getByLabel('Admin token',{exact:true}).fill(token);
  await page.getByRole('button',{name:'Open console'}).click();
  await expect(page.getByText('MOCK HARDWARE',{exact:true})).toBeVisible();
  await expect(page.getByText('Fan 0',{exact:true}).last()).toBeVisible();
  await expect(page.getByText('Fan 1',{exact:true}).last()).toBeVisible();
  await page.getByLabel('Selected GPU').selectOption('GPU-MOCK-002');
  await expect(page.getByText('Monitoring only',{exact:true}).first()).toBeVisible();
  await expect(page.getByRole('button',{name:'Save & apply curve'})).toBeDisabled();
  await page.setViewportSize({width:390,height:844});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBeTruthy();
});

test('edits stay local through polling, save draft, apply all fans, return automatic',async({page})=>{
  await login(page);
  await page.getByLabel('Point 1 duty',{exact:true}).fill('35');
  await expect(page.getByText('Unsaved changes',{exact:true})).toBeVisible();
  await page.waitForTimeout(2400);
  await expect(page.getByLabel('Point 1 duty',{exact:true})).toHaveValue('35');
  let saved=await api(page,'/config');
  expect(saved.body.gpus[uuid]?.mode ?? 'automatic').toBe('automatic');
  await page.getByRole('button',{name:'Save draft',exact:true}).click();
  await expect(page.getByText('Draft saved. Firmware automatic control remains selected.')).toBeVisible();
  let gpuResponse=await api(page,'/gpus');
  expect(gpuResponse.body.gpus.find((g:any)=>g.uuid===uuid).fans.every((f:any)=>f.commanded_duty===null)).toBeTruthy();
  await page.getByLabel('Minimum duty (%)',{exact:true}).fill('20');
  await page.getByLabel('Critical temperature (°C)',{exact:true}).fill('90');
  await page.getByLabel("I have verified this GPU's minimum duty, critical temperature, and automatic restoration behavior.").check();
  await page.getByRole('button',{name:'Save & apply curve'}).click();
  await expect(page.getByText('Custom curve',{exact:true}).first()).toBeVisible();
  await expect.poll(async()=>{
    const result=await api(page,'/gpus');
    return result.body.gpus.find((g:any)=>g.uuid===uuid).fans.filter((f:any)=>f.commanded_duty!==null).length;
  }, {timeout: 12000}).toBe(2);
  await page.getByRole('button',{name:'Return to automatic'}).click();
  await expect(page.getByText('Automatic control restored.',{exact:true})).toBeVisible();
  saved=await api(page,'/config');
  expect(saved.body.gpus[uuid].mode).toBe('automatic');
  gpuResponse=await api(page,'/gpus');
  expect(gpuResponse.body.gpus.find((g:any)=>g.uuid===uuid).fans.every((f:any)=>f.commanded_duty===null)).toBeTruthy();
});

test('invalid curves never submit and concurrent changes preserve local drafts',async({page})=>{
  await login(page);
  await page.getByLabel('Point 2 temperature',{exact:true}).fill('20');
  await expect(page.getByText('Temperatures must increase and fan duties must not decrease.')).toBeVisible();
  await expect(page.getByRole('button',{name:'Save draft',exact:true})).toBeDisabled();
  await page.getByLabel('Point 2 temperature',{exact:true}).fill('62');
  const existing=await api(page,'/config');
  const result=await api(page,'/config','PUT',{...existing.body,sampling_seconds:2},existing.body.revision);
  expect(result.status).toBe(200);
  await expect(page.getByText('The saved configuration changed since this draft was loaded.',{exact:false})).toBeVisible();
  await expect(page.getByLabel('Point 2 temperature',{exact:true})).toHaveValue('62');
  await expect(page.getByRole('button',{name:'Save & apply curve'})).toBeDisabled();
  await page.getByRole('button',{name:'Discard edits and reload'}).click();
  await expect(page.getByLabel('Point 2 temperature',{exact:true})).toHaveValue('60');
});

test('return-to-automatic storage errors remain visible after telemetry refresh',async({page})=>{
  await login(page);
  await page.route(`**/api/v1/gpus/${uuid}/default`,route=>route.fulfill({status:200,contentType:'application/json',body:JSON.stringify({uuid,restored:true,persisted:false,storage_error:'read-only filesystem',revision:1})}));
  await page.getByRole('button',{name:'Return to automatic'}).click();
  await expect(page.getByText('The automatic request could not be saved:',{exact:false})).toBeVisible();
  await page.waitForTimeout(2200);
  await expect(page.getByText('restart may restore the previous desired mode.',{exact:false})).toBeVisible();
});


test('graph keyboard editing previews locally without a configuration write',async({page})=>{
  await login(page);
  const writes: string[]=[];
  page.on('request',request=>{if(request.method()==='PUT' && request.url().endsWith('/api/v1/config')) writes.push(request.url());});
  const slider=page.getByRole('slider').first();
  const before=Number(await page.getByLabel('Point 1 duty',{exact:true}).inputValue());
  await slider.focus();
  await page.keyboard.press('ArrowUp');
  await expect(page.getByLabel('Point 1 duty',{exact:true})).toHaveValue(String(before+1));
  await expect(page.getByText('Unsaved changes',{exact:true})).toBeVisible();
  expect(writes).toEqual([]);
});

test('unavailable readings remain null and sample freshness is visible',async({page})=>{
  await login(page);
  const response=await api(page,'/gpus');
  const device=response.body.gpus.find((g:any)=>g.uuid===uuid);
  device.metrics.temperature_c.collected_at=new Date(Date.now()-120_000).toISOString();
  device.metrics.power_watts={...device.metrics.power_watts,value:null,status:'unsupported',reason:'Power reporting unavailable'};
  await page.route('**/api/v1/gpus',route=>route.fulfill({status:200,contentType:'application/json',body:JSON.stringify(response.body)}));
  await expect(page.getByText('Stale ·',{exact:false})).toBeVisible();
  await expect(page.getByText('unsupported · Power reporting unavailable',{exact:true})).toBeVisible();
  await expect(page.locator('.metric-card').nth(3).locator('.metric-value')).toHaveText('—W');
});

test('a saved draft survives a real backend restart and remains automatic',async({page})=>{
  const {readFile}=await import('node:fs/promises');
  await login(page);
  await page.getByLabel('Point 1 duty',{exact:true}).fill('37');
  await page.getByRole('button',{name:'Save draft',exact:true}).click();
  await expect(page.getByText('Draft saved. Firmware automatic control remains selected.')).toBeVisible();
  const before=await api(page,'/config');
  const pid=Number(await readFile(process.env.SERVER_FAN_E2E_CONTROL_FILE!,'utf8'));
  process.kill(pid,'SIGUSR2');
  // The original session must disappear, proving this is a new server process.
  await expect.poll(async()=>{
    try { return (await page.request.get('/api/v1/session')).status(); }
    catch { return 0; }
  },{timeout:12000}).toBe(401);
  await login(page);
  await expect(page.getByLabel('Point 1 duty',{exact:true})).toHaveValue('37');
  const after=await api(page,'/config');
  expect(after.body.revision).toBe(before.body.revision);
  expect(after.body.gpus[uuid].mode).toBe('automatic');
  const devices=await api(page,'/gpus');
  expect(devices.body.gpus.find((g:any)=>g.uuid===uuid).fans.every((f:any)=>f.commanded_duty===null)).toBeTruthy();
});

test('legacy ranges stay step ranges until an explicit new point curve',async({page})=>{
  await login(page);
  const existing=await api(page,'/config');
  existing.body.curves['Legacy fixture']={type:'step',ranges:[{min_temperature:0,max_temperature:40,fan_speed:30,hysteresis:3},{min_temperature:40,max_temperature:80,fan_speed:100,hysteresis:3}]};
  const result=await api(page,'/config','PUT',existing.body,existing.body.revision);
  expect(result.status).toBe(200);
  await expect(page.getByRole('button',{name:'Discard edits and reload'})).toBeVisible();
  await page.getByRole('button',{name:'Discard edits and reload'}).click();
  await page.getByRole('combobox',{name:'Load saved curve',exact:true}).selectOption('Legacy fixture');
  await expect(page.getByRole('combobox',{name:'Curve type',exact:true})).toBeDisabled();
  await expect(page.getByLabel('Range 1 maximum temperature',{exact:true})).toHaveValue('40');
  await page.getByRole('button',{name:'Save draft',exact:true}).click();
  await expect(page.getByText('Draft saved. Firmware automatic control remains selected.')).toBeVisible();
  const saved=await api(page,'/config');
  const curve=saved.body.curves[saved.body.gpus[uuid].curve];
  expect(curve.type).toBe('step');
  expect(curve.points).toBeUndefined();
  expect(curve.ranges[0].max_temperature).toBe(40);
  await page.getByRole('button',{name:'Start a new point curve'}).click();
  await expect(page.getByRole('combobox',{name:'Curve type',exact:true})).toBeEnabled();
  await expect(page.getByLabel('Point 1 temperature',{exact:true})).toHaveValue('30');
  await page.getByRole('combobox',{name:'Curve type',exact:true}).selectOption('step');
  await page.getByRole('button',{name:'Save draft',exact:true}).click();
  await expect(page.getByText('Draft saved. Firmware automatic control remains selected.')).toBeVisible();
});

test('safety actions and re-authentication retain the original draft revision',async({page})=>{
  await login(page);
  await page.getByRole('button',{name:'Start a new point curve'}).click();
  await page.getByLabel('Point 1 duty',{exact:true}).fill('39');
  const existing=await api(page,'/config');
  const update=await api(page,'/config','PUT',{...existing.body,sampling_seconds:3},existing.body.revision);
  expect(update.status).toBe(200);
  await page.getByRole('button',{name:'Return to automatic'}).click();
  await expect(page.getByText('Automatic control restored.',{exact:true})).toBeVisible();
  await expect(page.getByLabel('Point 1 duty',{exact:true})).toHaveValue('39');
  await page.getByRole('button',{name:'Sign out',exact:true}).click();
  await page.getByLabel('Admin token',{exact:true}).fill(token);
  await page.getByRole('button',{name:'Open console'}).click();
  await expect(page.getByLabel('Point 1 duty',{exact:true})).toHaveValue('39');
  await expect(page.getByText('The saved configuration changed since this draft was loaded.',{exact:false})).toBeVisible();
  await expect(page.getByRole('button',{name:'Save draft',exact:true})).toBeDisabled();
});
