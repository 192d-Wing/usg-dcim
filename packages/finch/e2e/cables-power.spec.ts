// Cables and power-chain panels on the rack page. Cables get full
// CRUD (log/edit/delete between two devices with port pickers). The
// power side asserts the honest current state: the connect dialog
// opens and offers the rack's PDU, but outlets cannot exist yet —
// asset-create defers the PDU outlet auto-seed and no outlet
// provisioning endpoint or UI exists, so the outlet list is empty
// and Connect stays disabled. When outlet seeding lands, this spec
// is where the real connect/disconnect coverage goes.
import { test, expect } from '@playwright/test';
import { Api, uniq } from './helpers';

test.describe.configure({ mode: 'serial' });

const runId = uniq();
const switchName = `E2E-SW-${runId}`;
const serverName = `E2E-SRV2-${runId}`;
const pduName = `E2E-PDU-${runId}`;
const cableLabel = `CAB-${runId}`;
let rackId = '';

test.beforeAll(async ({ request, baseURL }) => {
  const api = await Api.login(request, baseURL!);
  const run = await api.createRunRack(`RKC${runId}`);
  rackId = run.rackId;
  const base = {
    site_id: run.siteId, rack_id: rackId, hostname: null, manufacturer: null,
    model: null, serial: null, lifecycle_state: 'active', metadata_json: {},
  };
  await api.post('/inventory/assets', {
    ...base, name: switchName, kind: 'switch', rack_position_u: 1, rack_units: 1, port_count: 24,
  });
  await api.post('/inventory/assets', {
    ...base, name: serverName, kind: 'server', rack_position_u: 3, rack_units: 1, port_count: 4,
  });
  await api.post('/inventory/assets', {
    ...base, name: pduName, kind: 'pdu', rack_position_u: 40, rack_units: 1, port_count: null,
  });
});

test('log a cable between two devices with port pickers', async ({ page }) => {
  await page.goto(`/racks/${rackId}`);
  await page.getByRole('button', { name: 'Add cable' }).click();
  const modal = page.getByRole('dialog');

  // A/B ends pre-select the rack's first two devices — assert rather
  // than re-pick (the fixture created exactly these two + the PDU).
  await expect(modal.getByText(switchName)).toBeVisible();
  await expect(modal.getByText(serverName)).toBeVisible();
  await modal.getByText('Pick port (1-24)').click();
  await page.getByRole('option', { name: '1', exact: true }).click();
  await modal.getByText('Pick port (1-4)').click();
  await page.getByRole('option', { name: '2', exact: true }).click();
  await modal.getByLabel('Medium').fill('cat6');
  await modal.getByLabel('Color').fill('blue');
  await modal.getByLabel(/Length/).fill('3');
  await modal.getByLabel(/Label/).fill(cableLabel);
  await modal.getByRole('button', { name: 'Add cable' }).click();

  await expect(page.getByText('Cable added')).toBeVisible();
  const row = page.getByRole('row', { name: new RegExp(cableLabel) });
  await expect(row).toBeVisible();
  await expect(row.getByText(switchName)).toBeVisible();
  await expect(row.getByText(serverName)).toBeVisible();
  await expect(row.getByText('cat6')).toBeVisible();
});

test('edit the cable', async ({ page }) => {
  await page.goto(`/racks/${rackId}`);
  const row = page.getByRole('row', { name: new RegExp(cableLabel) });
  await row.getByRole('button', { name: 'Edit cable' }).click();
  const modal = page.getByRole('dialog');
  await modal.getByLabel('Color').fill('yellow');
  await modal.getByRole('button', { name: 'Save', exact: true }).click();

  await expect(page.getByText('Cable updated')).toBeVisible();
  await expect(row.getByText('yellow')).toBeVisible();
});

test('delete the cable', async ({ page }) => {
  page.on('dialog', (d) => d.accept());
  await page.goto(`/racks/${rackId}`);
  const row = page.getByRole('row', { name: new RegExp(cableLabel) });
  await row.getByRole('button', { name: 'Delete cable' }).click();
  await expect(page.getByText('Cable removed')).toBeVisible();
  await expect(page.getByText('No cables logged for this rack yet.')).toBeVisible();
});

test('power chain lists the PDU but outlets cannot be provisioned yet', async ({ page }) => {
  await page.goto(`/racks/${rackId}`);
  const serverRow = page.getByRole('row', { name: new RegExp(serverName) });
  await serverRow.getByRole('button', { name: 'Connect' }).click();
  const modal = page.getByRole('dialog');
  await expect(modal.getByText(`Connect ${serverName} to a PDU outlet`)).toBeVisible();
  // The rack's PDU is pre-selected…
  await expect(modal.getByText(new RegExp(pduName))).toBeVisible();
  // …but no outlet-provisioning path exists (create defers the
  // auto-seed; no endpoint or UI), so Connect must stay disabled.
  await expect(modal.getByRole('button', { name: 'Connect', exact: true })).toBeDisabled();
});
