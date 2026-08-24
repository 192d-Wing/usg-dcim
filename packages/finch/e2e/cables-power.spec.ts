// Cables and power-chain panels on the rack page. Cables get full
// CRUD (log/edit/delete between two devices with port pickers). The
// power side covers the real connect/disconnect flow: creating a
// PDU auto-seeds its 24-outlet strip (odd positions phase A, even
// B, C13 receptacles), so the per-run PDU is immediately
// connectable through the modal's outlet picker.
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

test('connect a server PSU to an auto-seeded PDU outlet', async ({ page }) => {
  await page.goto(`/racks/${rackId}`);
  // The per-run PDU was created after outlet auto-seeding landed, so
  // it carries the 24-outlet strip (odd = phase A, even = B, C13).
  await expect(page.getByText('0 / 24 outlets used')).toBeVisible();
  const serverRow = page.getByRole('row', { name: new RegExp(serverName) });
  await serverRow.getByRole('button', { name: 'Connect' }).click();
  const modal = page.getByRole('dialog');
  await expect(modal.getByText(`Connect ${serverName} to a PDU outlet`)).toBeVisible();
  // The rack's PDU is pre-selected; pick the first free outlet.
  await expect(modal.getByText(new RegExp(pduName))).toBeVisible();
  await modal.getByRole('button', { name: 'Pick an outlet' }).click();
  await page.getByRole('option', { name: /Outlet 01 · phase A · C13/ }).click();
  const connect = modal.getByRole('button', { name: 'Connect', exact: true });
  await expect(connect).toBeEnabled();
  await connect.click();
  await expect(page.getByText('Connected PSU1').first()).toBeVisible();
  // The chain row now shows the drop and the PDU counts it.
  await expect(page.getByText(new RegExp(`PSU1 → ${pduName}`))).toBeVisible();
  await expect(page.getByText('1 / 24 outlets used')).toBeVisible();
});

test('disconnect the PSU drop', async ({ page }) => {
  await page.goto(`/racks/${rackId}`);
  await expect(page.getByText(new RegExp(`PSU1 → ${pduName}`))).toBeVisible();
  await page.getByRole('button', { name: 'Disconnect' }).first().click();
  await expect(page.getByText('Disconnected').first()).toBeVisible();
  await expect(page.getByText('0 / 24 outlets used')).toBeVisible();
});
