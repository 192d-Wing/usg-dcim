// DNS zones and records through the IPAM page's DNS tab. Zones are
// fabric-scoped: the spec creates its own fabric via the API, pins
// the app's fabric scope through localStorage (dcim.fabric-scope),
// and cleans everything up afterwards (zone via the type-to-confirm
// modal, default VRF + fabric via the API). The record edit is the
// regression lock for the dns_records.data ::json cast fix (#375).
import { test, expect } from '@playwright/test';
import { Api, uniq } from './helpers';

test.describe.configure({ mode: 'serial' });

const runId = uniq();
const fabricName = `E2E Fabric ${runId}`;
const zoneFqdn = `e2e-${runId}.test`;
let fabricId = '';

test.beforeAll(async ({ request, baseURL }) => {
  const api = await Api.login(request, baseURL!);
  const f = await api.post('/ipam/fabrics', {
    name: fabricName, slug: `e2e-${runId}`, description: 'E2E fixture',
    enclave: null, classification: null,
    dns_recursive_upstreams: null, dns_deny_networks: null, dns_allow_networks: null,
  });
  fabricId = f.id;
});

test.afterAll(async ({ request, baseURL }) => {
  // Tolerant cleanup — the zone should already be gone via the UI.
  const api = await Api.login(request, baseURL!);
  for (const z of await api.list('/dns/zones', { fabric_id: fabricId }).catch(() => [])) {
    await api.delete(`/dns/zones/${z.id}`).catch(() => {});
  }
  for (const v of await api.list('/ipam/vrfs', { fabric_id: fabricId }).catch(() => [])) {
    await api.delete(`/ipam/vrfs/${v.id}`).catch(() => {});
  }
  await api.delete(`/ipam/fabrics/${fabricId}`).catch(() => {});
});

test.beforeEach(async ({ page }) => {
  await page.addInitScript((id) => localStorage.setItem('dcim.fabric-scope', id), fabricId);
});

async function openDnsTab(page: import('@playwright/test').Page) {
  await page.goto('/ipam');
  await page.getByRole('tab', { name: 'DNS', exact: true }).click();
}

test('create an apex hosted zone', async ({ page }) => {
  await openDnsTab(page);
  await page.getByRole('button', { name: 'Create hosted zone' }).first().click();
  const modal = page.getByRole('dialog');
  await modal.getByLabel('Zone FQDN').fill(zoneFqdn);
  await modal.getByText('Site (per-site)').click();
  await page.getByRole('option', { name: 'Apex (per-fabric)' }).click();
  await modal.getByRole('button', { name: 'Create', exact: true }).click();

  await expect(page.getByText('Zone created')).toBeVisible();
  await expect(page.getByRole('link', { name: zoneFqdn })).toBeVisible();
});

test('create an A record in the zone', async ({ page }) => {
  await openDnsTab(page);
  await page.getByRole('link', { name: zoneFqdn }).click();
  await page.getByRole('button', { name: 'Create record' }).click();
  const modal = page.getByRole('dialog');
  await modal.getByLabel('Record name').fill('www');
  // The target field's label is type-dependent — "IP address" for A.
  await modal.getByLabel('IP address').fill('10.0.111.50');
  await modal.getByRole('button', { name: 'Create', exact: true }).click();

  await expect(page.getByText('Record created')).toBeVisible();
  await expect(page.getByText(`www.${zoneFqdn}`).first()).toBeVisible();
});

test('edit the record target (json-cast regression)', async ({ page }) => {
  await openDnsTab(page);
  await page.getByRole('link', { name: zoneFqdn }).click();
  const row = page.getByRole('row', { name: /www/ });
  await row.getByRole('button', { name: /edit/i }).click();
  const modal = page.getByRole('dialog');
  await modal.getByLabel('IP address').fill('10.0.111.51');
  await modal.getByRole('button', { name: 'Save', exact: true }).click();

  await expect(page.getByText('Record updated')).toBeVisible();
  // .first(): the value can render in more than one place (cell +
  // activity feed) depending on prior suite state.
  await expect(page.getByText('10.0.111.51').first()).toBeVisible();
});

test('delete the record, then the zone via type-to-confirm', async ({ page }) => {
  page.on('dialog', (d) => d.accept());
  await openDnsTab(page);
  await page.getByRole('link', { name: zoneFqdn }).click();
  const row = page.getByRole('row', { name: /www/ });
  await row.getByRole('checkbox').check();
  await page.getByRole('button', { name: 'Delete', exact: true }).click();
  // Assert on the outcome, not toast wording: only the SOA row stays.
  await expect(page.getByRole('row', { name: /www/ })).toHaveCount(0);

  // Back to the zones list; single-zone delete requires typing the
  // FQDN into the confirm modal.
  await openDnsTab(page);
  const zoneRow = page.getByRole('row', { name: new RegExp(zoneFqdn.replaceAll('.', '\\.')) });
  await zoneRow.getByRole('checkbox').check();
  await page.getByRole('button', { name: 'Delete', exact: true }).click();
  const modal = page.getByRole('dialog');
  await expect(modal.getByText('Delete hosted zone')).toBeVisible();
  await modal.locator('input').last().fill(zoneFqdn);
  await modal.getByRole('button', { name: 'Delete zone' }).click();

  await expect(page.getByRole('link', { name: zoneFqdn })).toHaveCount(0);
});
