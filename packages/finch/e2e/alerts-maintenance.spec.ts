// Alert rules and maintenance windows CRUD. Both UPDATE paths ride
// the asset_filter_json COALESCE that was plan-time-broken until the
// ::json cast fix, so the edit tests double as regressions for it.
// Everything created here is deleted through the UI (both resources
// have full delete), so runs leave nothing behind.
import { test, expect } from '@playwright/test';
import { uniq } from './helpers';

test.describe.configure({ mode: 'serial' });

const runId = uniq();
const ruleName = `E2E Rule ${runId}`;
const windowName = `E2E Window ${runId}`;

test('alert rule: create with trigger expression', async ({ page }) => {
  await page.goto('/alerts/rules');
  await page.getByRole('button', { name: 'New rule' }).click();
  const modal = page.getByRole('dialog');

  await modal.getByLabel('Name', { exact: true }).fill(ruleName);
  await modal.getByLabel(/Description/).fill('E2E synthetic rule');
  await modal.getByLabel('Metric').fill('e2e.metric.kw');
  await modal.getByLabel('Threshold').fill('42.5');
  await modal.getByLabel(/Duration/).fill('120');
  await modal.getByRole('button', { name: 'Create rule' }).click();

  await expect(page.getByText('Alert rule created')).toBeVisible();
  const row = page.getByRole('row', { name: new RegExp(ruleName) });
  await expect(row).toBeVisible();
  await expect(row.getByText('e2e.metric.kw > 42.5')).toBeVisible();
  await expect(row.getByText('enabled', { exact: true })).toBeVisible();
});

test('alert rule: edit updates the trigger (json-cast regression)', async ({ page }) => {
  await page.goto('/alerts/rules');
  const row = page.getByRole('row', { name: new RegExp(ruleName) });
  await row.getByRole('button', { name: `Edit ${ruleName}` }).click();
  const modal = page.getByRole('dialog');
  await modal.getByLabel('Threshold').fill('99');
  await modal.getByRole('button', { name: 'Save changes' }).click();

  await expect(page.getByText('Alert rule updated')).toBeVisible();
  await expect(row.getByText('e2e.metric.kw > 99')).toBeVisible();
});

test('alert rule: disable toggle, then delete', async ({ page }) => {
  page.on('dialog', (d) => d.accept());
  await page.goto('/alerts/rules');
  const row = page.getByRole('row', { name: new RegExp(ruleName) });

  await row.getByRole('button', { name: `Disable ${ruleName}` }).click();
  await expect(page.getByText('Rule disabled')).toBeVisible();
  await expect(row.getByText('disabled', { exact: true })).toBeVisible();

  await row.getByRole('button', { name: `Delete ${ruleName}` }).click();
  await expect(page.getByText('Alert rule deleted')).toBeVisible();
  await expect(page.getByRole('row', { name: new RegExp(ruleName) })).toHaveCount(0);
});

test('maintenance window: create as upcoming', async ({ page }) => {
  await page.goto('/maintenance');
  await page.getByRole('button', { name: 'New window' }).click();
  const modal = page.getByRole('dialog');

  await modal.getByLabel('Name', { exact: true }).fill(windowName);
  // Native datetime-local inputs (not Cloudscape) — target by type.
  // A window far in the future keeps the status deterministic.
  await modal.locator('input[type="datetime-local"]').nth(0).fill('2027-01-15T22:00');
  await modal.locator('input[type="datetime-local"]').nth(1).fill('2027-01-16T02:00');
  await modal.getByLabel(/Reason/).fill('E2E synthetic window');
  await modal.getByRole('button', { name: 'Create window' }).click();

  await expect(page.getByText('Maintenance window created')).toBeVisible();
  const row = page.getByRole('row', { name: new RegExp(windowName) });
  await expect(row).toBeVisible();
  await expect(row.getByText('upcoming')).toBeVisible();
});

test('maintenance window: edit end-must-follow-start validation, then save (json-cast regression)', async ({ page }) => {
  await page.goto('/maintenance');
  const row = page.getByRole('row', { name: new RegExp(windowName) });
  await row.getByRole('button', { name: `Edit ${windowName}` }).click();
  const modal = page.getByRole('dialog');

  // End before start must be rejected client-side.
  await modal.locator('input[type="datetime-local"]').nth(1).fill('2027-01-15T20:00');
  await modal.getByRole('button', { name: 'Save changes' }).click();
  await expect(modal.getByText('End must be after start')).toBeVisible();

  await modal.locator('input[type="datetime-local"]').nth(1).fill('2027-01-16T06:00');
  await modal.getByLabel(/Reason/).fill('E2E synthetic window (extended)');
  await modal.getByRole('button', { name: 'Save changes' }).click();
  await expect(page.getByText('Maintenance window updated')).toBeVisible();
  await expect(row.getByText('E2E synthetic window (extended)')).toBeVisible();
});

test('maintenance window: delete', async ({ page }) => {
  page.on('dialog', (d) => d.accept());
  await page.goto('/maintenance');
  const row = page.getByRole('row', { name: new RegExp(windowName) });
  await row.getByRole('button', { name: `Delete ${windowName}` }).click();
  await expect(page.getByText('Maintenance window deleted')).toBeVisible();
  await expect(page.getByRole('row', { name: new RegExp(windowName) })).toHaveCount(0);
});
