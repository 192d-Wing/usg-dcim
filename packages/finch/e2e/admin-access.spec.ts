// Admin plane + capability gating. Roles: full lifecycle through the
// admin UI. Users: create, role assignment, unassignment, deactivate
// (users have no DELETE — one inactive E2E user accumulates per run).
// API tokens: issue/reveal/revoke through the UI with live API
// verification. Gating: the SPA is seeded with a limited API token
// (the token UI can only delegate capabilities the caller holds, and
// the admin holds only `*`, so the granular token is issued via the
// API) and the nav/buttons must hide everything the token lacks.
import { test, expect } from '@playwright/test';
import { Api, FIXTURES, uniq } from './helpers';

test.describe.configure({ mode: 'serial' });

const runId = uniq();
const roleName = `E2E Role ${runId}`;
const userEmail = `e2e-user-${runId}@dcim.local`;
const tokenName = `E2E Token ${runId}`;

const READONLY_CAPS = [
  'inventory:sites:read', 'inventory:buildings:read', 'inventory:rooms:read',
  'inventory:rows:read', 'inventory:racks:read', 'inventory:assets:read',
  'dashboards:dashboards:read',
];

test('role create through the capability picker', async ({ page }) => {
  await page.goto('/admin');
  await page.getByRole('tab', { name: 'Roles' }).click();
  await page.getByRole('button', { name: 'New role' }).click();
  const modal = page.getByRole('dialog');

  await modal.getByLabel('Name', { exact: true }).fill(roleName);
  await modal.getByLabel(/Description/).fill('E2E limited role');
  // Search collapses the picker to one domain; its header "all"
  // checkbox grants every dashboards capability.
  await modal.getByPlaceholder(/Filter codes/).fill('dashboards');
  await modal.getByRole('checkbox', { name: 'all' }).check();
  await modal.getByRole('button', { name: 'Create' }).click();

  await expect(page.getByText('Role created')).toBeVisible();
  await expect(page.getByRole('row', { name: new RegExp(roleName) })).toBeVisible();
});

test('user create, role assignment, unassignment, deactivate', async ({ page }) => {
  // Assignment removal goes through window.confirm.
  page.on('dialog', (d) => d.accept());
  await page.goto('/admin');
  await page.getByRole('button', { name: 'New user' }).click();
  const modal = page.getByRole('dialog');
  await modal.getByLabel('Email').fill(userEmail);
  await modal.getByLabel(/Display name/).fill('E2E Limited User');
  await modal.getByRole('button', { name: 'Create' }).click();
  await expect(page.getByText('User created')).toBeVisible();

  const row = page.getByRole('row', { name: new RegExp(userEmail) });
  await expect(row).toBeVisible();
  await row.getByRole('button', { name: 'Roles' }).click();
  const assignModal = page.getByRole('dialog');
  await expect(assignModal.getByText('No role assignments.')).toBeVisible();

  await assignModal.getByText('Pick a role').click();
  await page.getByRole('option', { name: new RegExp(roleName) }).click();
  await assignModal.getByRole('button', { name: 'Assign role' }).click();
  await expect(assignModal.getByText(roleName, { exact: true })).toBeVisible();
  await expect(assignModal.getByText('no scope (role default)')).toBeVisible();

  await assignModal.getByRole('button', { name: 'Remove assignment' }).click();
  await expect(assignModal.getByText('No role assignments.')).toBeVisible();
  // The assignments modal doesn't close on Escape — reload resets it.
  await page.reload();

  await row.getByRole('button', { name: `Deactivate ${userEmail}` }).click();
  await expect(page.getByText('User deactivated')).toBeVisible();
  await expect(row.getByText('inactive')).toBeVisible();
});

test('role delete', async ({ page }) => {
  page.on('dialog', (d) => d.accept());
  await page.goto('/admin');
  await page.getByRole('tab', { name: 'Roles' }).click();
  const row = page.getByRole('row', { name: new RegExp(roleName) });
  await expect(row).toBeVisible();
  await row.getByRole('button', { name: `Delete ${roleName}` }).click();
  await expect(page.getByText('Role deleted')).toBeVisible();
  await expect(page.getByRole('row', { name: new RegExp(roleName) })).toHaveCount(0);
});

test('API token: issue, reveal once, authenticate, revoke', async ({ page, request, baseURL }) => {
  page.on('dialog', (d) => d.accept());
  await page.goto('/settings/tokens');
  await page.getByRole('button', { name: 'Issue token' }).click();
  let modal = page.getByRole('dialog');
  await modal.getByLabel('Name', { exact: true }).fill(tokenName);
  // The picker offers only the caller's own capabilities — for the
  // admin that is the single `*` entry.
  await modal.getByRole('checkbox', { name: '*' }).check();
  await modal.getByRole('button', { name: 'Issue token' }).click();

  modal = page.getByRole('dialog');
  await expect(modal.getByText('copy it now', { exact: false })).toBeVisible();
  const plaintext = (await modal.locator('code').first().innerText()).trim();
  expect(plaintext.length).toBeGreaterThan(20);
  await page.keyboard.press('Escape');

  // The plaintext authenticates against the live API…
  const me = await request.get(`${baseURL}/api/v1/auth/me`, {
    headers: { Authorization: `Bearer ${plaintext}` },
  });
  expect(me.ok()).toBeTruthy();

  // …until revoked from the table.
  const row = page.getByRole('row', { name: new RegExp(tokenName) });
  await row.getByRole('button', { name: `Revoke ${tokenName}` }).click();
  await expect(page.getByText('Token revoked')).toBeVisible();
  await expect(row.getByText('revoked')).toBeVisible();

  const after = await request.get(`${baseURL}/api/v1/auth/me`, {
    headers: { Authorization: `Bearer ${plaintext}` },
  });
  expect(after.status()).toBe(401);
});

test.describe('capability gating with a read-only principal', () => {
  // A clean context: no admin session — the SPA runs on a limited
  // API token seeded into localStorage before load.
  test.use({ storageState: { cookies: [], origins: [] } });

  let limited = { id: '', plaintext: '' };

  test.beforeAll(async ({ request, baseURL }) => {
    const api = await Api.login(request, baseURL!);
    const t = await api.post('/auth/tokens', {
      name: `E2E RO Token ${runId}`, permission_codes: READONLY_CAPS,
      scope_json: {}, expires_at: null,
    });
    limited = { id: t.id, plaintext: t.plaintext };
  });

  test.afterAll(async ({ request, baseURL }) => {
    const api = await Api.login(request, baseURL!);
    await api.delete(`/auth/tokens/${limited.id}`);
  });

  test('nav and mutations hide everything the principal lacks', async ({ page, request, baseURL }) => {
    await page.addInitScript((tok) => localStorage.setItem('dcim.token', tok), limited.plaintext);

    await page.goto('/sites');
    await expect(page.getByText('Loading sites…')).toBeHidden();
    await expect(page.getByRole('table')).toBeVisible();
    // Read works; create/edit affordances must not exist.
    await expect(page.getByRole('button', { name: 'New site' })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Edit' })).toHaveCount(0);

    // Capability-gated nav entries are absent; ungated ones remain.
    for (const visible of ['/sites', '/buildings', '/racks']) {
      await expect(page.locator(`nav a[href="${visible}"]`).first()).toBeVisible();
    }
    for (const hidden of ['/admin', '/audit', '/import', '/dns', '/ipam', '/lir', '/registrations']) {
      await expect(page.locator(`nav a[href="${hidden}"]`)).toHaveCount(0);
    }

    await page.goto('/buildings');
    await expect(page.getByRole('button', { name: 'New building' })).toHaveCount(0);

    // The fixture building renders read-only: no floor management,
    // and the floor plan shows the non-editing hint.
    const api = await Api.login(request, baseURL!);
    const { rackHomeBuildingId } = await api.ensureFixtures();
    await page.goto(`/buildings/${rackHomeBuildingId}`);
    await expect(page.getByText(/front face \(thick edge\)|marks each rack/)).toBeVisible();
    await expect(page.getByText('Drag racks onto tiles')).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Add floor' })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Add row' })).toHaveCount(0);
  });
});
