// Deep smoke test: drill into a rack and an asset so we render
// rack-visualization + asset-show (recharts) — the high-risk migrations.

import puppeteer from 'puppeteer-core';
import { mkdir } from 'node:fs/promises';
import { join } from 'node:path';

const OUT = './smoke';
const BASE = 'http://localhost:5173';
// Backend lives behind the vite dev proxy at /api/v1/*.
const API = 'http://localhost:5173/api/v1';

await mkdir(OUT, { recursive: true });

const browser = await puppeteer.launch({
  executablePath: 'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',
  headless: 'new',
  args: ['--no-sandbox'],
});
const page = await browser.newPage();
await page.setViewport({ width: 1440, height: 900 });

const consoleErrors = [];
page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${e.message}`));
page.on('console', (m) => { if (m.type() === 'error') consoleErrors.push(`console: ${m.text()}`); });

// Login.
await page.goto(BASE + '/login', { waitUntil: 'networkidle2' });
function setValueScript(el, v) {
  const setter = Object.getOwnPropertyDescriptor(globalThis.HTMLInputElement.prototype, 'value').set;
  setter.call(el, v);
  el.dispatchEvent(new Event('input', { bubbles: true }));
}
await page.evaluate((src) => {
  const fn = new Function('return ' + src)();
  const inputs = document.querySelectorAll('input');
  fn(inputs[0], 'admin@dcim.local');
  fn(inputs[1], 'changeme');
}, setValueScript.toString());
await page.click('button[type=submit]');
await page.waitForFunction(() => !!localStorage.getItem('dcim.token'), { timeout: 10000 });
const token = await page.evaluate(() => localStorage.getItem('dcim.token'));

// Pull an arbitrary rack and asset ID from the API.
async function api(path) {
  const r = await fetch(API + path, { headers: { Authorization: `Bearer ${token}` } });
  if (!r.ok) throw new Error(`${path} → ${r.status}`);
  return r.json();
}
const racks = await api('/inventory/racks?page_size=1');
const rackId = racks.items?.[0]?.id;
const assets = await api('/inventory/assets?page_size=1');
const assetId = assets.items?.[0]?.id;

console.log(`rackId=${rackId} assetId=${assetId}`);

const results = [];
for (const [name, url] of [
  ['rack-show', `/racks/${rackId}`],
  ['asset-show', `/assets/${assetId}`],
  ['site-show', `/sites/${(await api('/inventory/sites?page_size=1')).items[0].id}`],
]) {
  consoleErrors.length = 0;
  await page.goto(BASE + url, { waitUntil: 'networkidle2', timeout: 20000 });
  // Asset show needs longer for the recharts series to render.
  await new Promise((r) => setTimeout(r, 3000));
  const file = join(OUT, name + '.png');
  await page.screenshot({ path: file, fullPage: false });
  results.push({ name, url, ok: consoleErrors.length === 0, errors: consoleErrors.slice(0, 5) });
}
console.log(JSON.stringify(results, null, 2));
await browser.close();
