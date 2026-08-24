// Login-page UX: backend-driven SSO visibility, the local form, and
// the inline failure feedback (regression: failures used to be silent).
//
// The dev stack this suite runs against has no OIDC configured, so
// GET /api/v1/auth/methods reports sso:false and the page must show
// the local form immediately with NO E-ICAM button — the dead-button
// bug this replaced was the frontend build-time constant rendering
// SSO regardless of what the backend could honor. (The SSO-first
// disclosure flow is therefore not exercisable here; it needs an
// OIDC-configured stack.)
import { test, expect } from '@playwright/test';
import { CREDS } from './helpers';

test.describe('login page', () => {
  test('no OIDC configured: local form is immediate and the SSO button is absent', async ({ page }) => {
    await page.goto('/login');
    // Local form is the loading/error fallback AND the sso:false state,
    // so it must be visible without clicking any disclosure.
    await expect(page.getByLabel('Email')).toBeVisible();
    await expect(page.getByLabel('Password')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible();
    // /auth/methods answered sso:false → no E-ICAM button (and no
    // disclosure link, since there is no SSO to fall back from).
    await expect(page.getByRole('button', { name: /E-ICAM/i })).toHaveCount(0);
  });

  test('wrong password shows an inline error on the form', async ({ page }) => {
    await page.goto('/login');
    await page.getByLabel('Email').fill(CREDS.email);
    await page.getByLabel('Password').fill('definitely-wrong');
    await page.getByRole('button', { name: 'Sign in' }).click();
    // Rendered twice (visible alert + a11y live region) — first() it.
    await expect(page.getByText('Invalid email or password.').first()).toBeVisible();
    await expect(page).toHaveURL(/\/login/);
  });

  test('client-side validation flags empty and malformed input', async ({ page }) => {
    await page.goto('/login');
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
    await page.getByLabel('Email').fill(CREDS.email);
    await page.getByLabel('Password').fill(CREDS.password);
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page.locator('a[href="/sites"]').first()).toBeVisible();
    await expect(page).not.toHaveURL(/\/login/);
  });
});
