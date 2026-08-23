// Asset lifecycle on the rack page: add a device through the modal
// (slot collision validation included), the rack-edit orphan guard,
// moving a device, and decommissioning it from the asset page.
// Assets have no DELETE API (decommission is a lifecycle state), so
// each run works in its own freshly created rack under the E2E-RACKS
// fixture building and leaves only inert decommissioned assets.
import { test, expect, type Page } from '@playwright/test';
import { Api, FIXTURES, uniq } from './helpers';

// The rack visualization also exposes row roles bearing the asset
// name — scope device-table lookups to the table that has the
// Hostname column.
function devicesTable(page: Page) {
  return page.getByRole('table').filter({
    has: page.getByRole('columnheader', { name: 'Hostname' }),
  });
}

test.describe.configure({ mode: 'serial' });

const runId = uniq();
const rackCode = `RKA${runId}`;
const assetName = `E2E-SRV-${runId}`;
let rackId = '';

test.beforeAll(async ({ request, baseURL }) => {
  const api = await Api.login(request, baseURL!);
  const { siteId } = await api.ensureFixtures();
  const rooms = await api.list('/inventory/rooms', {
    building_id: (await api.list('/inventory/buildings', { site_id: siteId }))
      .find((b: any) => b.code === FIXTURES.rackHomeBuilding).id,
  });
  const room = rooms.find((r: any) => r.code === FIXTURES.rackHomeFloor);
  const rows = await api.list('/inventory/rows', { room_id: room.id });
  const rack = await api.post('/inventory/racks', {
    site_id: siteId,
    row_id: rows.find((r: any) => r.code === FIXTURES.rackHomeRow).id,
    name: `E2E Asset Rack ${runId}`, code: rackCode, u_height: 42,
  });
  rackId = rack.id;
});

test('add device through the rack modal', async ({ page }) => {
  await page.goto(`/racks/${rackId}`);
  await expect(page.locator('h1:visible').first()).toContainText(rackCode);

  await page.getByRole('button', { name: 'Add device' }).first().click();
  const modal = page.getByRole('dialog');
  await modal.getByLabel('Name', { exact: true }).fill(assetName);
  await modal.getByLabel('Manufacturer').fill('Dell');
  await modal.getByLabel('Model').fill('PowerEdge R750');
  await modal.getByLabel(/Position U/).fill('30');
  await modal.getByLabel('Size (U)').fill('2');
  await modal.getByLabel(/Serial/).fill(`SN${runId}`);
  await modal.getByRole('button', { name: 'Add device' }).click();

  await expect(page.getByText('Device added')).toBeVisible();
  await expect(devicesTable(page).getByRole('row', { name: new RegExp(assetName) })).toBeVisible();
});

test('slot collision is flagged inline in the add form', async ({ page }) => {
  await page.goto(`/racks/${rackId}`);
  await page.getByRole('button', { name: 'Add device' }).first().click();
  const modal = page.getByRole('dialog');
  await modal.getByLabel('Name', { exact: true }).fill('Collider');
  // U31 sits inside the 2U device mounted at U30–31.
  await modal.getByLabel(/Position U/).fill('31');
  await modal.getByRole('button', { name: 'Add device' }).click();
  await expect(modal.getByText(/Slots already occupied: U31/)).toBeVisible();
  await page.keyboard.press('Escape');
});

test('rack edit: orphan guard blocks shrinking below mounted devices', async ({ page }) => {
  await page.goto(`/racks/${rackId}`);
  await page.getByRole('button', { name: 'Edit rack' }).click();
  const modal = page.getByRole('dialog');

  // 24U is below the device's U30–31 span — the guard must engage.
  await modal.getByRole('button', { name: '24U' }).click();
  await expect(modal.getByText(/would be orphaned at 24U/)).toBeVisible();
  await expect(modal.getByRole('button', { name: /Save/ })).toBeDisabled();

  // Back to 42U the guard clears; save a Max kW change (regression:
  // the edit path sends NUMERIC as a string since #374).
  await modal.getByRole('button', { name: '42U' }).click();
  await expect(modal.getByText(/would be orphaned/)).toBeHidden();
  await modal.getByLabel('Max kW').fill('15');
  await modal.getByRole('button', { name: /Save/ }).click();
  await expect(page.getByText('Rack updated')).toBeVisible();
  await expect(page.getByText('15 kW max')).toBeVisible();
});

test('move the device to a new U position', async ({ page }) => {
  await page.goto(`/racks/${rackId}`);
  await page.getByRole('button', { name: `Move ${assetName}` }).click();
  const modal = page.getByRole('dialog');
  await expect(modal.getByText(`Move ${assetName}`)).toBeVisible();

  await modal.getByLabel('Position U', { exact: true }).fill('5');
  await expect(modal.getByText('Will occupy U5–U6 on front.')).toBeVisible();
  await modal.getByRole('button', { name: 'Move', exact: true }).click();
  await expect(page.getByText(new RegExp(`Moved ${assetName}`))).toBeVisible();

  const row = devicesTable(page).getByRole('row', { name: new RegExp(assetName) });
  await expect(row.getByRole('cell').first()).toHaveText('5');
});

test('device opens from the table and decommissions from its page', async ({ page }) => {
  await page.goto(`/racks/${rackId}`);
  await devicesTable(page).getByRole('row', { name: new RegExp(assetName) }).click();
  await expect(page).toHaveURL(/\/assets\/[0-9a-f-]+/);
  await expect(page.locator('h1:visible').first()).toContainText(assetName);

  await page.getByRole('button', { name: 'Decommission' }).click();
  const modal = page.getByRole('dialog');
  await modal.getByLabel('Reason').fill('E2E lifecycle test');
  await modal.getByLabel('Sanitization note').fill('n/a — synthetic E2E asset');
  await modal.getByLabel('Type the asset name to confirm').fill(assetName);
  await modal.getByText('I understand power connections will be dropped').click();
  await modal.getByRole('button', { name: 'Decommission', exact: true }).click();

  await expect(page.getByText(new RegExp(`${assetName} decommissioned`))).toBeVisible();
  // canDecommission goes false once the state lands — the button is
  // the observable proof the page refreshed into the new state.
  await expect(page.getByRole('button', { name: 'Decommission' })).toBeHidden();
});
