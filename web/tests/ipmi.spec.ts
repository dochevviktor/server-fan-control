import { expect, test, type Page } from '@playwright/test';

const identity = {board:'E3C246D2I', manufacturer:'ASRock', firmware:'1.20'};
const newSettings = () => ({schema_version:1, revision:1, profile:'', expected_identity:{board:'',manufacturer:'',firmware:''}, min_duty:0, automatic_verified:false,mapping_verified:false,thermal_verified:false,fan_sensors:{'1':'FAN1','2':'FAN2','3':'FAN3'}});
async function fixture(page: Page) {
  let settings = newSettings();
  const state = {enabled:true,mock:true,state:'monitoring',reason:'',reading:{identity,fans:[{name:'FAN1',rpm:1400,status:'ok'},{name:'FAN2',rpm:1000,status:'ok'},{name:'FAN3',rpm:900,status:'ok'}],duties:[0,0,0,0,0,0,0,0],collected_at:new Date().toISOString()},owned_fans:[] as number[],updated_at:new Date().toISOString()};
  const targets: unknown[] = [];
  let failRestore = false;
  await page.route('**/api/v1/ipmi', route => route.fulfill({json:state}));
  await page.route('**/api/v1/ipmi/config', async route => {
    if(route.request().method()==='PUT') {
      const next=route.request().postDataJSON();
      if(route.request().headers()['if-match']!==`"${settings.revision}"`) { await route.fulfill({status:412,json:{error:'configuration conflict'}}); return; }
      settings={...next,revision:settings.revision+1};
    }
    await route.fulfill({json:settings});
  });
  await page.route('**/api/v1/ipmi/target', async route => {
    const target=route.request().postDataJSON(); targets.push(target);
    state.reading.duties[target.fan-1]=target.duty; state.owned_fans=[target.fan];state.state='manual';
    await route.fulfill({json:{applied:true,persisted:true,restored:false}});
  });
  await page.route('**/api/v1/ipmi/default', async route => {
    if(failRestore) { await route.fulfill({json:{restored:false,persisted:false,hardware_error:'BMC readback mismatch',storage_error:'ownership retained'}}); return; }
    state.owned_fans.forEach(fan=>state.reading.duties[fan-1]=0);state.owned_fans=[];state.state='automatic';
    await route.fulfill({json:{restored:true,persisted:true}});
  });
  return {targets, state, changeRevision:()=>{settings={...settings,revision:settings.revision+1};}, failRestore:()=>{failRestore=true;}};
}
async function login(page: Page) {
  await page.goto('/');
  await page.getByLabel('Admin token',{exact:true}).fill('e2e-test-admin-token-0123456789abcdef');
  await page.getByRole('button',{name:'Open console'}).click();
  await page.getByRole('tab',{name:'System fans',exact:true}).click();
  await expect(page.getByRole('heading',{name:'System fans',exact:true})).toBeVisible();
}
async function verifyProfile(page: Page) {
  if (!await page.getByRole('combobox',{name:'System fan profile',exact:true}).isVisible()) {
    await page.getByText('Hardware profile & verification',{exact:false}).click();
  }
  await page.getByRole('combobox',{name:'System fan profile',exact:true}).selectOption('asrock-e3c246d2i');
  await page.getByRole('button',{name:'Use detected board and firmware'}).click();
  await page.getByLabel('System fan minimum duty (%)',{exact:true}).fill('25');
  await page.getByLabel('I verified that command slots',{exact:false}).check();
  await page.getByLabel('I verified that setting a selected fan byte',{exact:false}).check();
  await page.getByLabel('I verified the minimum duty and cooling',{exact:false}).check();
  await page.getByRole('button',{name:'Save system fan profile',exact:true}).click();
  await expect(page.getByText('Hardware profile saved.',{exact:false})).toBeVisible();
}

test('system fans stay available without GPUs; verification precedes explicit target and automatic restoration', async ({page}) => {
  const f=await fixture(page);
  await page.route('**/api/v1/gpus',route=>route.fulfill({json:{gpus:[]}}));
  await login(page);
  await page.getByRole('tab',{name:'GPU cooling',exact:true}).click();
  await expect(page.getByRole('heading',{name:'No GPU telemetry available'})).toBeVisible();
  await page.getByRole('tab',{name:'System fans',exact:true}).click();
  await expect(page.getByRole('button',{name:'Apply system fan duty'})).toBeDisabled();
  await verifyProfile(page);
  expect(f.targets).toEqual([]);
  await page.getByLabel('System fan duty (%)',{exact:true}).fill('40');
  await page.getByRole('button',{name:'Apply system fan duty'}).click();
  await expect(page.getByText('FAN3 target accepted;', {exact:false})).toBeVisible();
  expect(f.targets).toEqual([{fan:3,duty:40}]);
  await expect(page.getByText('40%',{exact:true})).toBeVisible();
  await page.getByRole('button',{name:'Restore system fans to automatic'}).click();
  await expect(page.getByText('Owned system fans restored to automatic; readback confirmed.',{exact:true})).toBeVisible();
  expect(f.state.owned_fans).toEqual([]);
  await page.setViewportSize({width:390,height:844});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBeTruthy();
});

test('system fan edits survive polling and concurrent profile updates',async({page})=>{
  const f=await fixture(page);
  await login(page);
  await page.getByLabel('System fan minimum duty (%)',{exact:true}).fill('33');
  f.changeRevision();
  await expect(page.getByText('The system fan profile changed.',{exact:false})).toBeVisible({timeout:7000});
  await expect(page.getByLabel('System fan minimum duty (%)',{exact:true})).toHaveValue('33');
  await expect(page.getByRole('button',{name:'Save system fan profile',exact:true})).toBeDisabled();
  expect(f.targets).toEqual([]);
  await page.getByRole('button',{name:'Discard system fan edits and reload',exact:true}).click();
  await expect(page.getByLabel('System fan minimum duty (%)',{exact:true})).toHaveValue('0');
});

test('system fan restoration failures remain visible through polling',async({page})=>{
  const f=await fixture(page);
  await login(page);
  f.failRestore();
  await page.getByRole('button',{name:'Restore system fans to automatic'}).click();
  await expect(page.getByText('BMC readback mismatch · ownership retained',{exact:true})).toBeVisible();
  await page.waitForTimeout(3200);
  await expect(page.getByText('BMC readback mismatch · ownership retained',{exact:true})).toBeVisible();
});

test('real mock-IPMI worker saves a profile, verifies FAN3 duty and recovers automatic across restart', async({page})=>{
  const {readFile}=await import('node:fs/promises');
  await login(page);
  await expect(page.getByText('SIMULATED IPMI',{exact:true})).toBeVisible();
  await verifyProfile(page);
  await page.getByLabel('System fan duty (%)',{exact:true}).fill('35');
  await page.getByRole('button',{name:'Apply system fan duty'}).click();
  await expect(page.getByText('FAN3 target accepted;', {exact:false})).toBeVisible();
  let snapshot=await page.request.get('/api/v1/ipmi').then(r=>r.json());
  expect(snapshot.mock).toBe(true);
  expect(snapshot.state).toBe('manual');
  expect(snapshot.reading.duties).toEqual([0,0,35,0,0,0,0,0]);
  expect(snapshot.owned_fans).toEqual([3]);
  await page.getByRole('button',{name:'Restore system fans to automatic'}).click();
  await expect(page.getByText('Owned system fans restored to automatic; readback confirmed.',{exact:true})).toBeVisible();
  snapshot=await page.request.get('/api/v1/ipmi').then(r=>r.json());
  expect(snapshot.reading.duties).toEqual([0,0,0,0,0,0,0,0]);
  expect(snapshot.owned_fans).toEqual([]);
  await page.getByRole('button',{name:'Apply system fan duty'}).click();
  await expect(page.getByText('FAN3 target accepted;', {exact:false})).toBeVisible();
  const before=await page.request.get('/api/v1/ipmi/config').then(r=>r.json());
  const pid=Number(await readFile(process.env.SERVER_FAN_E2E_CONTROL_FILE!,'utf8'));
  process.kill(pid,'SIGUSR2');
  await expect.poll(async()=>{
    try {return (await page.request.get('/api/v1/session')).status();} catch {return 0;}
  },{timeout:12000}).toBe(401);
  await login(page);
  snapshot=await page.request.get('/api/v1/ipmi').then(r=>r.json());
  expect(snapshot.state).toBe('monitoring');
  expect(snapshot.reading.duties).toEqual([0,0,0,0,0,0,0,0]);
  expect(snapshot.owned_fans).toEqual([]);
  const after=await page.request.get('/api/v1/ipmi/config').then(r=>r.json());
  expect(after).toEqual(before);
});
