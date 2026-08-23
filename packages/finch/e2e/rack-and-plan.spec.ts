// Rack creation through the cascade (Max kW filled — regression for
// the NUMERIC-as-string 400) and floor-plan behaviors. The drag test
// paces its mouse gesture: dnd-kit attaches its move listeners via a
// React state update after mousedown, so an unpaced gesture finishes
// before the sensor exists and degrades into text selection. Placed-
// rack UX (render/rotate/open) is covered separately with an API-
// placed rack so it doesn't depend on the gesture. Racks have no
// delete API, so they accumulate in the E2E-RACKS fixture building.
import { test, expect } from '@playwright/test';
import { Api, FIXTURES, uniq } from './helpers';

test.describe.configure({ mode: 'serial' });

const runId = uniq();
const dragRackCode = `RKD${runId}`;
const placeRackCode = `RKP${runId}`;
let rackHomeBuildingId = '';

async function freeTiles(api: Api, count: number): Promise<Array<{ x: number; y: number }>> {
  const detail = await api.get(`/dashboards/buildings/${rackHomeBuildingId}`);
  const floor = detail.floors.find((f: any) => f.code === FIXTURES.rackHomeFloor);
  const occupied = new Set<string>(
    floor.rows.flatMap((rw: any) => rw.racks)
      .filter((r: any) => r.grid_x !== null && r.grid_y !== null)
      .map((r: any) => `${r.grid_x},${r.grid_y}`),
  );
  const free: Array<{ x: number; y: number }> = [];
  for (let y = floor.grid_rows - 1; y >= 0 && free.length < count; y--) {
    for (let x = floor.grid_cols - 1; x >= 0 && free.length < count; x--) {
      if (!occupied.has(`${x},${y}`)) free.push({ x, y });
    }
  }
  expect(free.length, 'fixture floor has no free tiles left').toBe(count);
  return free;
}

test.beforeAll(async ({ request, baseURL }) => {
  const api = await Api.login(request, baseURL!);
  ({ rackHomeBuildingId } = await api.ensureFixtures());
});

test('rack create cascade with Max kW set', async ({ page }) => {
  await page.goto('/racks/new');

  await page.getByText('Pick a site…').click();
  await page.getByRole('option', { name: new RegExp(FIXTURES.siteCode) }).first().click();
  await page.getByText('Select a building…').click();
  await page.getByRole('option', { name: new RegExp(FIXTURES.rackHomeBuilding) }).first().click();
  await page.getByText('Select a room…').click();
  await page.getByRole('option', { name: new RegExp(FIXTURES.rackHomeFloor) }).first().click();
  await page.getByText('Select a row…').click();
  await page.getByRole('option', { name: new RegExp(FIXTURES.rackHomeRow) }).first().click();

  await page.getByLabel('Name').fill(`E2E Rack ${dragRackCode}`);
  await page.getByLabel('Code').fill(dragRackCode);
  await page.getByLabel('Max kW').fill('12.5');
  await page.getByRole('button', { name: 'Create rack' }).click();

  await expect(page.getByText('Rack created')).toBeVisible();
  await expect(page).toHaveURL(/\/racks\/[0-9a-f-]+/);
  await expect(page.getByText('12.5 kW max')).toBeVisible();
});

test('floor plan: drag places a tray rack onto a free tile', async ({ page, request, baseURL }) => {
  const api = await Api.login(request, baseURL!);
  const [tile] = await freeTiles(api, 1);

  await page.goto(`/buildings/${rackHomeBuildingId}`);
  await expect(page.getByText('Drag racks onto tiles')).toBeVisible();

  const chip = page.locator(`[title*="${dragRackCode}"]`).first();
  await chip.scrollIntoViewIfNeeded();
  await expect(chip).toBeVisible();

  // Boxes AFTER scrolling. Grid geometry: 46px cells, 2px gap, 6px
  // padding (floor-plan.tsx constants).
  const grid = page.locator('div[style*="grid-template-columns"]').first();
  const gridBox = (await grid.boundingBox())!;
  const chipBox = (await chip.boundingBox())!;
  const CELL = 46, GAP = 2, PAD = 6;
  const target = {
    x: gridBox.x + PAD + tile.x * (CELL + GAP) + CELL / 2,
    y: gridBox.y + PAD + tile.y * (CELL + GAP) + CELL / 2,
  };

  const patchDone = page.waitForResponse(
    (r) => r.url().includes('/inventory/racks/') && r.request().method() === 'PATCH',
    { timeout: 15_000 },
  );
  await page.mouse.move(chipBox.x + chipBox.width / 2, chipBox.y + chipBox.height / 2);
  await page.mouse.down();
  // Let React commit the sensor before moving (see file header).
  await page.waitForTimeout(150);
  // Cross the 5px activation distance, then pause for the DragOverlay.
  await page.mouse.move(chipBox.x + chipBox.width / 2 + 10, chipBox.y + chipBox.height / 2 - 10, { steps: 5 });
  await page.waitForTimeout(150);
  await page.mouse.move(target.x, target.y, { steps: 25 });
  await page.waitForTimeout(150);
  await page.mouse.up();

  // Placement persists through the PATCH — verify against a reload,
  // not just the optimistic cache.
  expect((await patchDone).ok()).toBeTruthy();
  await page.reload();
  await expect(page.getByText('Drag racks onto tiles')).toBeVisible();
  // No `exact`: the placed cell's accessible name includes its child
  // rotate button's "↻".
  await expect(page.getByRole('button', { name: new RegExp(dragRackCode) })).toBeVisible();
  await expect(page.locator(`[title*="${dragRackCode}"]`)).toHaveCount(1);
});

test('placed rack renders, rotates, and opens its detail page', async ({ page, request, baseURL }) => {
  const api = await Api.login(request, baseURL!);
  // Deterministic placement via API — this test covers the placed-
  // rack UX independently of the drag gesture.
  const racks = await api.list('/inventory/racks');
  const rack = racks.find((r: any) => r.code === placeRackCode)
    ?? await api.post('/inventory/racks', {
      site_id: (await api.list('/inventory/sites')).find((s: any) => s.code === FIXTURES.siteCode).id,
      row_id: await rowId(api),
      name: `E2E Rack ${placeRackCode}`, code: placeRackCode, u_height: 42,
    });
  const [tile] = await freeTiles(api, 1);
  await api.patch(`/inventory/racks/${rack.id}`, { grid_x: tile.x, grid_y: tile.y, grid_rotation: 0 });

  await page.goto(`/buildings/${rackHomeBuildingId}`);
  // No `exact`: the cell's accessible name includes the rotate "↻".
  const cell = page.getByRole('button', { name: new RegExp(placeRackCode) });
  await expect(cell).toBeVisible();

  // Rotate persists a PATCH.
  const rotateDone = page.waitForResponse(
    (r) => r.url().includes(`/inventory/racks/${rack.id}`) && r.request().method() === 'PATCH',
  );
  await cell.locator('button[title="Rotate front face"]').click();
  expect((await rotateDone).ok()).toBeTruthy();

  // Clicking the tile opens the rack.
  await cell.click();
  await expect(page).toHaveURL(/\/racks\/[0-9a-f-]+/);
  await expect(page.locator('h1:visible').first()).toContainText(placeRackCode);
});

async function rowId(api: Api): Promise<string> {
  const rooms = await api.list('/inventory/rooms', { building_id: rackHomeBuildingId });
  const room = rooms.find((r: any) => r.code === FIXTURES.rackHomeFloor);
  const rows = await api.list('/inventory/rows', { room_id: room.id });
  return rows.find((r: any) => r.code === FIXTURES.rackHomeRow).id;
}
