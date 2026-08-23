// Smoke: every nav destination renders a page shell without the
// generic failure text. Console errors are collected as annotations
// (visible in the report) rather than failing the smoke outright.
import { test, expect } from '@playwright/test';

const PAGES: Array<[path: string, heading: RegExp]> = [
  ['/', /./],
  ['/sites', /Sites/],
  ['/buildings', /Buildings/],
  ['/racks', /Racks/],
  ['/capacity', /Capacity/],
  ['/alerts', /Alerts/],
  ['/maintenance', /Maintenance/],
  ['/collectors', /Collectors/],
  ['/ipam', /IPAM|Fabrics|Subnets/i],
  ['/lir', /LIR/i],
  ['/registrations', /Registrations/i],
  ['/dns', /DNS/i],
  ['/import', /Import/i],
  ['/audit', /Audit/i],
  ['/admin', /Admin|Users|Roles/i],
];

for (const [path, heading] of PAGES) {
  test(`page renders: ${path}`, async ({ page }, testInfo) => {
    const consoleErrors: string[] = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') consoleErrors.push(msg.text());
    });

    await page.goto(path);
    // Role-based, name-matched: the side-nav header (also inside main)
    // and Cloudscape's hidden a11y headings make document-order
    // heading lookups unreliable.
    await expect(page.getByRole('heading', { name: heading }).first()).toBeVisible();
    await expect(page.getByText('Failed to load', { exact: false })).toHaveCount(0);

    for (const err of consoleErrors) {
      testInfo.annotations.push({ type: 'console-error', description: `${path}: ${err}` });
    }
  });
}
