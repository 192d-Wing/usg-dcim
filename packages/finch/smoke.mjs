// Smoke-test script: snap every major route after the Cloudscape migration
// to a screenshot. Detects React errors / blank renders.

import puppeteer from 'puppeteer-core';
import { mkdir } from 'node:fs/promises';
import { join } from 'node:path';

const OUT = './smoke';
const BASE = 'http://localhost:5173';

// Routes to visit. The shell + each page chunk is the smoke target.
const ROUTES = [
  '/login',
  '/',
  '/sites',
  '/racks',
  '/racks/new',
  '/alerts',
  '/alerts/rules',
  '/maintenance',
  '/collectors',
  '/capacity',
  '/settings/tokens',
  '/audit',
  '/import',
  '/admin',
  '/settings/notifications',
  '/ipam',
];

await mkdir(OUT, { recursive: true });

const browser = await puppeteer.launch({
  executablePath: 'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',
  headless: 'new',
  args: ['--no-sandbox'],
});

const page = await browser.newPage();
await page.setViewport({ width: 1440, height: 900 });

const consoleErrors = [];
page.on('pageerror', (err) => consoleErrors.push({ kind: 'pageerror', msg: err.message }));
page.on('console', (msg) => {
  if (msg.type() === 'error') consoleErrors.push({ kind: 'console', msg: msg.text() });
});

// Real login via the dev-seeded admin so the protected routes render
// with actual data instead of bouncing back to /login.
await page.goto(BASE + '/login', { waitUntil: 'networkidle2' });
await page.evaluate(() => {
  const inputs = document.querySelectorAll('input');
  // Cloudscape Input renders a real <input> under the hood; we trigger
  // a native input event so React picks it up.
  function setValue(el, v) {
    const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
    setter.call(el, v);
    el.dispatchEvent(new Event('input', { bubbles: true }));
  }
  setValue(inputs[0], 'admin@dcim.local');
  setValue(inputs[1], 'changeme');
});
await page.click('button[type=submit]');
await page.waitForFunction(
  () => !!localStorage.getItem('dcim.token'),
  { timeout: 10000 },
).catch(() => console.error('login did not set token within 10s'));
await new Promise((r) => setTimeout(r, 500));

const results = [];
for (const route of ROUTES) {
  consoleErrors.length = 0;
  const url = BASE + route;
  try {
    await page.goto(url, { waitUntil: 'networkidle2', timeout: 15000 });
  } catch (e) {
    results.push({ route, ok: false, why: `nav: ${e.message}` });
    continue;
  }
  // Wait briefly for React tree to mount + initial data to settle.
  await new Promise((r) => setTimeout(r, 1500));
  const file = join(OUT, route.replace(/\W+/g, '_') + '.png');
  await page.screenshot({ path: file, fullPage: false });
  results.push({
    route, ok: consoleErrors.length === 0,
    errors: consoleErrors.slice(0, 5),
    file,
  });
}

console.log(JSON.stringify(results, null, 2));
await browser.close();
