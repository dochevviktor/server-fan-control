import { expect, test, type Page } from '@playwright/test';
import { readFile } from 'node:fs/promises';

const token = 'e2e-test-admin-token-0123456789abcdef';

// Give this additional suite its own login-rate window without changing service limits.
test.beforeAll(async ({request, baseURL}) => {
  const login = await request.post('/api/v1/session', {data:{token}, headers:{Origin:new URL(baseURL!).origin}});
  expect(login.ok()).toBeTruthy();
  const pid = Number(await readFile(process.env.SERVER_FAN_E2E_CONTROL_FILE!, 'utf8'));
  process.kill(pid, 'SIGUSR2');
  await expect.poll(async () => {
    try { return (await request.get('/api/v1/session')).status(); }
    catch { return 0; }
  }, {timeout:12000}).toBe(401);
});

async function login(page: Page) {
  await page.goto('/');
  await page.getByLabel('Admin token', {exact:true}).fill(token);
  await page.getByRole('button', {name:'Open console'}).click();
  await expect(page.getByRole('heading', {name:'GPU cooling',exact:true})).toBeVisible();
  await expect(page.locator('.metric-value').first()).not.toHaveText('—°C');
}

test('cooling navigation preserves local drafts and supports keyboard selection',async({page})=>{
  await login(page);
  const writes: string[]=[];
  page.on('request',request=>{if(request.method()==='PUT') writes.push(request.url());});
  await page.getByLabel('Point 1 duty',{exact:true}).fill('41');
  const gpuTab=page.getByRole('tab',{name:'GPU cooling',exact:true});
  const systemTab=page.getByRole('tab',{name:'System fans',exact:true});
  const hddTab=page.getByRole('tab',{name:'HDD cooling',exact:true});
  await gpuTab.focus();
  await page.keyboard.press('ArrowRight');
  await expect(systemTab).toBeFocused();
  await expect(systemTab).toHaveAttribute('aria-selected','true');
  await expect(page.getByLabel('Point 1 duty',{exact:true})).toBeHidden();
  await page.keyboard.press('End');
  await expect(hddTab).toBeFocused();
  await expect(page.getByRole('heading',{name:'HDD case-fan curve',exact:true})).toBeVisible();
  await page.getByLabel('HDD point 1 duty',{exact:true}).fill('42');
  await hddTab.focus();
  await page.keyboard.press('Home');
  await expect(gpuTab).toBeFocused();
  await expect(page.getByLabel('Point 1 duty',{exact:true})).toHaveValue('41');
  await hddTab.click();
  await expect(page.getByLabel('HDD point 1 duty',{exact:true})).toHaveValue('42');
  expect(writes).toEqual([]);
});

test('connection failures are visible in the header and recover after polling resumes',async({page})=>{
  await login(page);
  await page.route('**/api/v1/status',route=>route.abort('failed'));
  await expect(page.locator('.connection')).toHaveText('Connection lost');
  await expect(page.getByText('Displayed data may be stale.',{exact:false})).toBeVisible();
  await page.unroute('**/api/v1/status');
  await expect(page.locator('.connection')).toHaveText('Controller online');
  await expect(page.getByText('Displayed data may be stale.',{exact:false})).toBeHidden();
});

test('cleared curve fields show validation without invalid SVG coordinates',async({page})=>{
  await login(page);
  const browserErrors: string[]=[];
  page.on('pageerror',error=>browserErrors.push(error.message));
  await page.getByLabel('Point 1 temperature',{exact:true}).fill('');
  await expect(page.getByRole('button',{name:'Save draft',exact:true})).toBeDisabled();
  await expect(page.getByText('Point 1: enter a temperature 0–200°C and duty 0–100%.',{exact:true})).toBeVisible();
  const graph=page.getByRole('group',{name:'Fan curve: temperature in Celsius and fan duty in percent'}).first();
  expect(await graph.innerHTML()).not.toMatch(/NaN|Infinity/);
  expect(browserErrors).toEqual([]);
});

test('saving locks GPU selection and graph editing until the request completes',async({page})=>{
  await login(page);
  await page.getByLabel('Point 1 duty',{exact:true}).fill('39');
  let release!: () => void;
  const pending = new Promise<void>(resolve=>release=resolve);
  await page.route('**/api/v1/config',async route=>{
    if(route.request().method()==='PUT') await pending;
    await route.continue();
  });
  await page.getByRole('button',{name:'Save draft',exact:true}).click();
  try {
    await expect(page.getByRole('button',{name:'Saving…',exact:true})).toBeDisabled();
    await expect(page.getByLabel('Selected GPU')).toBeDisabled();
    const point=page.getByRole('slider').first();
    await expect(point).toHaveAttribute('aria-disabled','true');
    await point.focus();
    await page.keyboard.press('ArrowUp');
    await expect(page.getByLabel('Point 1 duty',{exact:true})).toHaveValue('39');
  } finally { release(); }
  await expect(page.getByText('Draft saved. Firmware automatic control remains selected.')).toBeVisible();
  await expect(page.getByLabel('Selected GPU')).toBeEnabled();
  await expect(page.getByRole('slider').first()).toHaveAttribute('aria-disabled','false');
});

test('unsaved edits prompt before leaving and stay intact when navigation is cancelled',async({page})=>{
  await login(page);
  const savedDuty=await page.getByLabel('Point 1 duty',{exact:true}).inputValue();
  await page.getByLabel('Point 1 duty',{exact:true}).fill('43');
  const dialogPromise=page.waitForEvent('dialog');
  await page.evaluate(()=>{setTimeout(()=>location.reload(),0);});
  const dialog=await dialogPromise;
  expect(dialog.type()).toBe('beforeunload');
  await dialog.dismiss();
  await expect(page.getByLabel('Point 1 duty',{exact:true})).toHaveValue('43');
  await page.getByRole('button',{name:'Discard edits',exact:true}).click();
  await expect(page.getByLabel('Point 1 duty',{exact:true})).toHaveValue(savedDuty);
});
