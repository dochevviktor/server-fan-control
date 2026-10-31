import { expect, test, type Page } from '@playwright/test';
import type { HDDPolicy, IPMISettings } from '../src/types';

const identity = {board:'E3C246D2I',manufacturer:'ASRock',firmware:'1.20'};
const policy = (): HDDPolicy => ({mode:'automatic',fan:3,drives:[{id:'data-1',serial:'DISK-001',path:'/dev/sda',type:'sat'}],curve:{type:'linear',points:[{temperature:25,duty:30},{temperature:35,duty:50},{temperature:45,duty:100}]},sampling_seconds:60,freshness_seconds:180,missing_grace_seconds:60,critical_temp:50,hysteresis:2,cooling_hold_seconds:120,shared_min_duty:30,limits_confirmed:false,standby_verified:false,shared_cooling_verified:false});
async function fixture(page: Page) {
  let settings: IPMISettings = {schema_version:1,revision:1,profile:'asrock-e3c246d2i',expected_identity:identity,min_duty:25,automatic_verified:true,mapping_verified:true,thermal_verified:true,fan_sensors:{'1':'FAN1','2':'FAN2','3':'FAN3'},hdd:policy()};
  const now = new Date().toISOString();
  const samples = [{id:'data-1',path:'/dev/sda',serial:'DISK-001',temperature_c:35,status:'ok',attempted_at:now,read_completed_at:now,last_valid_at:now}];
  const snapshot = {enabled:true,mock:true,state:'monitoring',reading:{identity,fans:[{name:'FAN1',rpm:1400,status:'ok'},{name:'FAN2',rpm:1000,status:'ok'},{name:'FAN3',rpm:900,status:'ok'}],duties:[0,0,0,0,0,0,0,0],collected_at:now},owned_fans:[] as number[],updated_at:now,hdd:{state:'monitoring',reason:'Curve is not armed',mean_c:35,max_c:35,valid:1,expected:1,asleep:0,target:null as number|null,armed:false,samples,updated_at:now}};
  const puts: IPMISettings[] = [];
  let restoreFailure = false;
  await page.route('**/api/v1/disks', route => route.fulfill({json:{enabled:true,mock:true,samples,updated_at:now}}));
  await page.route('**/api/v1/ipmi', route => route.fulfill({json:snapshot}));
  await page.route('**/api/v1/ipmi/config', async route => {
    if(route.request().method() === 'PUT') {
      if(route.request().headers()['if-match'] !== `"${settings.revision}"`) { await route.fulfill({status:412,json:{error:'configuration conflict'}}); return; }
      const next = route.request().postDataJSON(); puts.push(next);
      settings = {...next,revision:settings.revision+1};
      if (settings.hdd?.mode === 'curve') { snapshot.state='curve';snapshot.owned_fans=[3];snapshot.hdd={...snapshot.hdd,state:'curve',armed:true,target:50,reason:'Average HDD curve active'}; }
    }
    await route.fulfill({json:settings});
  });
  await page.route('**/api/v1/ipmi/default', async route => {
    snapshot.hdd.armed = false;
    if (restoreFailure) { await route.fulfill({json:{restored:false,persisted:false,hardware_error:'BMC unreachable',storage_error:'configuration storage read-only'}});return; }
    settings={...settings,revision:settings.revision+1,hdd:{...settings.hdd!,mode:'automatic'}};
    snapshot.state='automatic';snapshot.owned_fans=[];snapshot.hdd={...snapshot.hdd,state:'automatic',target:null,reason:'Owned fans restored'};
    await route.fulfill({json:{restored:true,persisted:true}});
  });
  return {puts,snapshot,samples,changeRevision:()=>{settings={...settings,revision:settings.revision+1};},failRestore:()=>{restoreFailure=true;}};
}
async function login(page: Page) {
  await page.goto('/');
  await page.getByLabel('Admin token',{exact:true}).fill('e2e-test-admin-token-0123456789abcdef');
  await page.getByRole('button',{name:'Open console'}).click();
  await page.getByRole('tab',{name:'HDD cooling',exact:true}).click();
  await expect(page.getByRole('heading',{name:'HDD case-fan curve',exact:true})).toBeVisible();
}
async function confirmations(page: Page) {
  await page.getByLabel('I verified the HDD temperature limit',{exact:false}).check();
  await page.getByLabel('I verified SMART identity',{exact:false}).check();
  await page.getByLabel('I verified the shared-cooling minimum',{exact:false}).check();
}

test('HDD curve preview stays local; save draft and explicit guarded activation then restore',async({page})=>{
  const f=await fixture(page);await login(page);
  const panel=page.getByRole('region',{name:'HDD case-fan curve'});
  await expect(panel.getByText('SIMULATED HDDs',{exact:true})).toBeVisible();
  await expect(page.getByRole('button',{name:'Save & apply HDD curve',exact:true})).toBeDisabled();
  await page.getByLabel('HDD point 2 temperature',{exact:true}).fill('36');
  expect(f.puts).toEqual([]);
  await page.getByRole('button',{name:'Save HDD draft',exact:true}).click();
  await expect(panel.getByText('HDD draft saved.',{exact:false})).toBeVisible();
  expect(f.puts[0].hdd?.mode).toBe('automatic');
  expect(f.snapshot.hdd.armed).toBe(false);
  await confirmations(page);
  await page.getByRole('button',{name:'Save & apply HDD curve',exact:true}).click();
  await expect(panel.getByText('HDD curve saved and activation requested.',{exact:false})).toBeVisible();
  expect(f.puts[1].hdd?.mode).toBe('curve');
  expect(f.puts[1].hdd?.curve.points?.[1].temperature).toBe(36);
  await page.getByRole('tab',{name:'System fans',exact:true}).click();
  await expect(page.getByRole('button',{name:'Apply system fan duty',exact:true})).toBeDisabled();
  await page.getByRole('tab',{name:'HDD cooling',exact:true}).click();
  await expect(panel.getByText('Average HDD curve active',{exact:true})).toBeVisible();
  await page.getByRole('button',{name:'Stop HDD curve & restore automatic',exact:true}).click();
  await expect(panel.getByText('HDD curve stopped and owned system fans restored to automatic.',{exact:true})).toBeVisible();
  expect(f.snapshot.hdd.armed).toBe(false);
  await page.setViewportSize({width:390,height:844});
  expect(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth)).toBeTruthy();
});

test('HDD selection edits survive polling conflicts; missing readings stay visibly unavailable',async({page})=>{
  const f=await fixture(page);await login(page);
  await page.getByLabel('HDD 1 expected serial',{exact:true}).fill('REPLACEMENT');
  f.changeRevision();
  f.samples[0].status='missing';
  f.snapshot.hdd={...f.snapshot.hdd,state:'fallback',reason:'Selected HDD missing; automatic fallback',mean_c:null as unknown as number,max_c:null as unknown as number,valid:0,target:null};
  await expect(page.getByText('System fan settings changed. Your HDD edits are preserved.',{exact:false})).toBeVisible({timeout:7000});
  await expect(page.getByLabel('HDD 1 expected serial',{exact:true})).toHaveValue('REPLACEMENT');
  await expect(page.getByRole('button',{name:'Save HDD draft',exact:true})).toBeDisabled();
  await expect(page.getByText('missing · last known reading, not a fresh sample',{exact:true})).toBeVisible();
  await expect(page.getByText('Selected HDD missing; automatic fallback',{exact:true})).toBeVisible();
  await page.getByRole('button',{name:'Discard HDD edits and reload',exact:true}).click();
  await expect(page.getByLabel('HDD 1 expected serial',{exact:true})).toHaveValue('DISK-001');
  expect(f.puts).toEqual([]);
});

test('HDD restoration failure remains visible and incomplete guards prevent activation',async({page})=>{
  const f=await fixture(page);await login(page);
  await confirmations(page);
  await page.getByLabel('Shared-cooling minimum duty (%)',{exact:true}).fill('20');
  await expect(page.getByRole('button',{name:'Save & apply HDD curve',exact:true})).toBeDisabled();
  f.failRestore();
  await page.getByRole('button',{name:'Stop HDD curve & restore automatic',exact:true}).click();
  await expect(page.getByText('BMC unreachable · configuration storage read-only',{exact:true})).toBeVisible();
  await page.waitForTimeout(3200);
  await expect(page.getByText('BMC unreachable · configuration storage read-only',{exact:true})).toBeVisible();
  expect(f.puts).toEqual([]);
});

test('real mock HDD collector drives the saved curve and restart requires explicit activation',async({page})=>{
  test.setTimeout(65_000);
  const {readFile}=await import('node:fs/promises');
  await login(page);
  await page.getByRole('tab',{name:'System fans',exact:true}).click();
  if (!await page.getByRole('combobox',{name:'System fan profile',exact:true}).isVisible()) {
    await page.getByText('Hardware profile & verification',{exact:false}).click();
  }
  await page.getByRole('combobox',{name:'System fan profile',exact:true}).selectOption('asrock-e3c246d2i');
  await page.getByRole('button',{name:'Use detected board and firmware',exact:true}).click();
  await page.getByLabel('System fan minimum duty (%)',{exact:true}).fill('25');
  await page.getByLabel('I verified that command slots',{exact:false}).check();
  await page.getByLabel('I verified that setting a selected fan byte',{exact:false}).check();
  await page.getByLabel('I verified the minimum duty and cooling',{exact:false}).check();
  await page.getByRole('button',{name:'Save system fan profile',exact:true}).click();
  await expect(page.getByText('Hardware profile saved.',{exact:false})).toBeVisible();
  await page.getByRole('tab',{name:'HDD cooling',exact:true}).click();
  // Wait for the HDD editor to observe the hardware profile's new revision.
  await expect(page.getByText('Save a verified hardware profile in System fans.',{exact:true})).toBeHidden();
  await page.getByRole('button',{name:'Add selected HDD',exact:false}).click();
  await page.getByLabel('HDD 1 ID',{exact:true}).fill('data-1');
  await page.getByLabel('HDD 1 expected serial',{exact:true}).fill('MOCK-DATA-001');
  await page.getByLabel('HDD 1 mapped device',{exact:true}).fill('/dev/sda');
  await page.getByLabel('Hottest HDD limit (°C)',{exact:true}).fill('50');
  await page.getByLabel('Shared-cooling minimum duty (%)',{exact:true}).fill('30');
  await page.getByRole('button',{name:'Save HDD draft',exact:true}).click();
  await expect(page.getByText('HDD draft saved.',{exact:false})).toBeVisible();
  await expect.poll(async()=>{
    const disks=await page.request.get('/api/v1/disks').then(r=>r.json());
    return disks.samples?.[0]?.status;
  },{timeout:7000}).toBe('ok');
  let disks=await page.request.get('/api/v1/disks').then(r=>r.json());
  expect(disks.mock).toBe(true);
  expect(disks.samples[0].serial).toBe('MOCK-DATA-001');
  expect(disks.samples[0].temperature_c).toBeGreaterThan(0);
  await confirmations(page);
  await page.getByRole('button',{name:'Save & apply HDD curve',exact:true}).click();
  await expect(page.getByText('HDD curve saved and activation requested.',{exact:false})).toBeVisible();
  await expect.poll(async()=>{
    const snapshot=await page.request.get('/api/v1/ipmi').then(r=>r.json());
    return snapshot.state;
  },{timeout:20_000}).toBe('curve');
  let snapshot=await page.request.get('/api/v1/ipmi').then(r=>r.json());
  expect(snapshot.hdd.valid).toBe(1);
  expect(snapshot.hdd.mean_c).toBe(disks.samples[0].temperature_c);
  expect(snapshot.hdd.max_c).toBe(disks.samples[0].temperature_c);
  expect(snapshot.hdd.target).toBeGreaterThanOrEqual(30);
  expect(snapshot.reading.duties[2]).toBe(snapshot.hdd.target);
  expect(snapshot.owned_fans).toEqual([3]);
  const before=await page.request.get('/api/v1/ipmi/config').then(r=>r.json());
  const pid=Number(await readFile(process.env.SERVER_FAN_E2E_CONTROL_FILE!,'utf8'));
  process.kill(pid,'SIGUSR2');
  await expect.poll(async()=>{try{return(await page.request.get('/api/v1/session')).status();}catch{return 0;}},{timeout:12000}).toBe(401);
  await login(page);
  snapshot=await page.request.get('/api/v1/ipmi').then(r=>r.json());
  expect(snapshot.hdd.armed).toBe(false);
  expect(snapshot.hdd.state).toBe('awaiting_activation');
  expect(snapshot.owned_fans).toEqual([]);
  expect(snapshot.reading.duties).toEqual([0,0,0,0,0,0,0,0]);
  const after=await page.request.get('/api/v1/ipmi/config').then(r=>r.json());
  expect(after).toEqual(before);
  await page.getByRole('button',{name:'Stop HDD curve & restore automatic',exact:true}).click();
  await expect(page.getByText('HDD curve stopped and owned system fans restored to automatic.',{exact:true})).toBeVisible();
  expect((await page.request.get('/api/v1/ipmi/config').then(r=>r.json())).hdd.mode).toBe('automatic');
});
