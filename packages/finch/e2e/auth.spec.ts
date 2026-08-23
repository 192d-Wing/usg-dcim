// Login-page UX: SSO-first layout, local-credentials disclosure, and
// the inline failure feedback (regression: failures used to be silent).
import { test, expect } from '@playwright/test';
import { CREDS } from './helpers';

test.describe('login page', () => {
  test('leads with SSO and hides the local form behind the disclosure', async ({ page }) => {
    await page.goto('/login');
    await expect(page.getByRole('button', { name: 'Login using DOD E-ICAM' })).toBeVisible();
    await expect(page.getByLabel('Email')).toBeHidden();
    await page.getByRole('button', { name: /use local credentials/i }).click();
    await expect(page.getByLabel('Email')).toBeVisible();
    await expect(page.getByLabel('Password')).toBeVisible();
  });

  test('wrong password shows an inline error on the form', async ({ page }) => {
    await page.goto('/login');
    await page.getByRole('button', { name: /use local credentials/i }).click();
    await page.getByLabel('Email').fill(CREDS.email);
    await page.getByLabel('Password').fill('definitely-wrong');
    await page.getByRole('button', { name: 'Sign in' }).click();
    // Rendered twice (visible alert + a11y live region) — first() it.
    await expect(page.getByText('Invalid email or password.').first()).toBeVisible();
    await expect(page).toHaveURL(/\/login/);
  });

  test('client-side validation flags empty and malformed input', async ({ page }) => {
    await page.goto('/login');
    await page.getByRole('button', { name: /use local credentials/i }).click();
    // "a@b" passes the input's native type=email check (which would
    // otherwise block submit) but fails the app's dot-requiring regex,
    // so this exercises the app's own validation layer.
    await page.getByLabel('Email').fill('a@b');
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page.getByText('Enter a valid email').first()).toBeVisible();
    await expect(page.getByText('Password required').first()).toBeVisible();
  });

  test('valid credentials land in the app', async ({ page }) => {
    await page.goto('/login');
    await page.getByRole('button', { name: /use local credentials/i }).click();
    await page.getByLabel('Email').fill(CREDS.email);
    await page.getByLabel('Password').fill(CREDS.password);
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page.locator('a[href="/sites"]').first()).toBeVisible();
    await expect(page).not.toHaveURL(/\/login/);
  });
});
