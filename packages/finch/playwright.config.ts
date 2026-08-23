// Playwright E2E suite. Runs against a deployed environment named by
// E2E_BASE_URL (e.g. the windep dev cluster's dcim host) with the
// local break-glass admin — E2E_EMAIL / E2E_PASSWORD to override.
// The URL is env-only on purpose: dev ingresses are plain http and a
// hardcoded http default would (rightly) trip security scanning.
//
// Projects:
//   auth   — unauthenticated login-page specs
//   setup  — logs in once and saves storage state for `app`
//   app    — everything else, reusing the saved session
//
// Serial (workers: 1): the inventory specs share mutable server state.
import { defineConfig, devices } from '@playwright/test';

const baseURL = process.env.E2E_BASE_URL;
if (!baseURL) {
  throw new Error('Set E2E_BASE_URL to the deployed environment to test, e.g. the dev cluster dcim host.');
}

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 60_000,
  expect: { timeout: 15_000 },
  reporter: [['list']],
  use: {
    baseURL,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    {
      name: 'auth',
      testMatch: /auth\.spec\.ts/,
      use: { ...devices['Desktop Chrome'] },
    },
    {
      name: 'setup',
      testMatch: /auth\.setup\.ts/,
      use: { ...devices['Desktop Chrome'] },
    },
    {
      name: 'app',
      testIgnore: /auth\.(spec|setup)\.ts/,
      dependencies: ['setup'],
      use: {
        ...devices['Desktop Chrome'],
        storageState: 'e2e/.auth/admin.json',
      },
    },
  ],
});
