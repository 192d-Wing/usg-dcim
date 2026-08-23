import { test as setup } from '@playwright/test';
import { loginViaUi } from './helpers';

const AUTH_FILE = 'e2e/.auth/admin.json';

setup('authenticate', async ({ page }) => {
  await loginViaUi(page);
  await page.context().storageState({ path: AUTH_FILE });
});
