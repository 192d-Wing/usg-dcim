// Inventory hierarchy UX: site create/edit modals (with region
// quick-create), then a full building → floor → row lifecycle
// including the FK-guard toasts and both delete paths. Rows have no
// delete UI by design, so cleanup of the row goes through the API.
import { test, expect, type Page } from '@playwright/test';
import { Api, FIXTURES, uniq } from './helpers';

test.describe.configure({ mode: 'serial' });

async function dialog(page: Page) {
  return page.getByRole('dialog');
}

test('site create modal — with region quick-create when needed', async ({ page, request, baseURL }) => {
  await page.goto('/sites');
  await expect(page.locator('h1').first()).toContainText('Sites');
  // Idempotent across runs: only exercise create when the fixture
  // site is absent (sites cannot be deleted).
  await expect(page.getByText('Loading sites…')).toBeHidden();
  await expect(page.getByRole('table')).toBeVisible();
  const existing = page.getByRole('link', { name: FIXTURES.siteCode });
  if (await existing.count()) {
    test.info().annotations.push({ type: 'note', description: 'fixture site already present — create path previously verified' });
    return;
  }

  await page.getByRole('button', { name: 'New site' }).click();
  const modal = await dialog(page);

  // Region: pick the E2E region if it exists, otherwise quick-create
  // it through the window.prompt path.
  await modal.getByText('Pick a region…').click();
  const regionOpt = page.getByRole('option', { name: new RegExp(FIXTURES.regionCode) });
  if (await regionOpt.count()) {
    await regionOpt.first().click();
  } else {
    await page.keyboard.press('Escape');
    page.once('dialog', (d) => d.accept(FIXTURES.regionCode));
    await modal.getByRole('button', { name: 'New' }).click();
    await expect(page.getByText('Region created')).toBeVisible();
  }

  await modal.getByLabel('Name').fill(FIXTURES.siteName);
  await modal.getByLabel('Code').fill(FIXTURES.siteCode);
  await modal.getByRole('button', { name: 'Create' }).click();
  await expect(page.getByText('Site created')).toBeVisible();
  await expect(page).toHaveURL(/\/sites\/[0-9a-f-]+/);
});

test('site edit modal — updates name, code stays immutable', async ({ page }) => {
  await page.goto('/sites');
  const row = page.getByRole('row', { name: new RegExp(FIXTURES.siteCode) });
  await row.getByRole('button', { name: 'Edit' }).click();
  const modal = await dialog(page);
  await expect(modal.getByLabel('Code')).toBeDisabled();
  await modal.getByLabel('Name').fill(FIXTURES.siteName);
  await modal.getByRole('button', { name: 'Save' }).click();
  await expect(page.getByText('Site updated')).toBeVisible();
});

test('building → floor → row lifecycle with FK guards and deletes', async ({ page, request, baseURL }) => {
  const u = uniq();
  const bldgCode = `E2EB${u}`;
  const floorCode = `F${u}`;
  const rowCode = `R${u}`;

  // Create building from the list page.
  await page.goto('/buildings');
  await page.getByRole('button', { name: 'New building' }).click();
  let modal = await dialog(page);
  await modal.getByText('Pick a site…').click();
  await page.getByRole('option', { name: new RegExp(FIXTURES.siteCode) }).first().click();
  await modal.getByLabel('Name').fill(`E2E Building ${u}`);
  await modal.getByLabel('Code').fill(bldgCode);
  await modal.getByRole('button', { name: 'Create' }).click();
  await expect(page.getByText('Building created')).toBeVisible();
  await expect(page).toHaveURL(/\/buildings\/[0-9a-f-]+/);
  await expect(page.locator('h1').first()).toContainText(bldgCode);

  // Add a floor with a tile grid; the plan canvas must appear even
  // with zero rows (regression: it used to stay hidden).
  await page.getByRole('button', { name: 'Add floor' }).click();
  modal = await dialog(page);
  await modal.getByLabel('Name').fill(`Floor ${u}`);
  await modal.getByLabel('Code').fill(floorCode);
  await modal.getByLabel('Plan grid columns').fill('8');
  await modal.getByLabel('Plan grid rows').fill('6');
  await modal.getByRole('button', { name: 'Create' }).click();
  await expect(page.getByText('Floor created')).toBeVisible();
  await expect(page.getByText(new RegExp(`${floorCode} ·`))).toBeVisible();
  await expect(page.getByText('Drag racks onto tiles')).toBeVisible();

  // Add a row from the floor section (regression: the empty state
  // used to be a dead end).
  await page.getByRole('button', { name: 'Add row' }).first().click();
  modal = await dialog(page);
  await modal.getByLabel('Name').fill(`Row ${u}`);
  await modal.getByLabel('Code').fill(rowCode);
  await modal.getByRole('button', { name: 'Create' }).click();
  await expect(page.getByText('Row created')).toBeVisible();

  // Deleting the floor while a row exists must surface the FK guard.
  await page.getByRole('button', { name: 'Delete', exact: true }).first().click();
  modal = await dialog(page);
  await modal.getByRole('button', { name: 'Delete' }).click();
  await expect(page.getByText(/still has rows/)).toBeVisible();
  await page.keyboard.press('Escape');

  // Rows have no delete UI — clean up via the API, then walk the UI
  // delete path for floor and building.
  const api = await Api.login(request, baseURL!);
  const buildingId = page.url().split('/').pop()!;
  const rooms = await api.list('/inventory/rooms', { building_id: buildingId });
  const room = rooms.find((r: any) => r.code === floorCode);
  const rows = await api.list('/inventory/rows', { room_id: room.id });
  await api.delete(`/inventory/rows/${rows.find((r: any) => r.code === rowCode).id}`);

  await page.reload();
  await page.getByRole('button', { name: 'Delete', exact: true }).first().click();
  modal = await dialog(page);
  await modal.getByRole('button', { name: 'Delete' }).click();
  await expect(page.getByText('Floor deleted')).toBeVisible();

  await page.goto('/buildings');
  const bldgRow = page.getByRole('row', { name: new RegExp(bldgCode) });
  await bldgRow.getByRole('button', { name: 'Delete' }).click();
  modal = await dialog(page);
  await modal.getByRole('button', { name: 'Delete' }).click();
  await expect(page.getByText('Building deleted')).toBeVisible();
  await expect(page.getByRole('row', { name: new RegExp(bldgCode) })).toHaveCount(0);
});
