// Playwright E2E suite. Runs against a deployed environment (default:
// the windep dev cluster) with the local break-glass admin — override
// via E2E_BASE_URL / E2E_EMAIL / E2E_PASSWORD.
//
// Projects:
//   auth   — unauthenticated login-page specs
//   setup  — logs in once and saves storage state for `app`
//   app    — everything else, reusing the saved session
//
// Serial (workers: 1): the inventory specs share mutable server state.
import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 60_000,
  expect: { timeout: 15_000 },
  reporter: [['list']],
  use: {
    baseURL: process.env.E2E_BASE_URL ?? 'http://dcim.oopl.dev.mil',
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
