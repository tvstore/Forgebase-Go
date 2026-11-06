// M10.4: what the desk shows when data is customized and restricted (ADR 0023, 0024, 0025). The desk is not
// changed by M10: it reads the metadata API, so a field the caller may not read is not in the form or the Columns
// menu, a field the caller may read but not write is plain text, a restricted user's lists are shorter, a Link to
// a hidden document is not found, and `create` is `none` for a DocType the user is restricted on. A site custom
// field is a field like any other. The users, the groups, the credit limits and `custom_region` come from seed.sh.
// Set SCREENSHOTS=1 (and SCREENSHOT_DIR) to also save a PNG of each state; CI does not take them.

import { expect, test } from '@playwright/test';
import type { Page } from '@playwright/test';

const PASSWORD = process.env.E2E_PASSWORD ?? 'e2e-test-password';
const DIR = process.env.SCREENSHOT_DIR ?? 'test-results/screenshots';
const ADA = '/desk/#/form/customer/ada%40example.com';

async function login(page: Page, username: string): Promise<void> {
  await page.goto('/desk/');
  await page.getByLabel('Username').fill(username);
  await page.getByLabel('Password').fill(PASSWORD);
  await page.getByRole('button', { name: 'Log in' }).click();
  await expect(page.getByRole('heading', { name: 'Home', level: 1 })).toBeVisible();
}

/** A screenshot of the state, when SCREENSHOTS=1, after the short animations have finished. */
async function snap(page: Page, name: string): Promise<void> {
  if (process.env.SCREENSHOTS !== '1') return;
  await page.mouse.move(0, 0);
  await page.waitForTimeout(400);
  await page.screenshot({ path: `${DIR}/${name}.png` });
}

/** The form of Ada Lovelace, loaded. */
async function openAda(page: Page): Promise<void> {
  await page.goto(ADA);
  await expect(page.getByRole('heading', { name: 'Ada Lovelace', level: 1 })).toBeVisible();
}

/** The customer list, loaded. */
async function openCustomers(page: Page): Promise<void> {
  await page.goto('/desk/#/list/customer');
  await expect(page.getByRole('status').filter({ hasText: /^Showing / })).toBeVisible();
  await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
}

test('a Sales User does not see the level 1 field credit_limit, in the form or the Columns menu', async ({ page }) => {
  await login(page, 'e2e_sales');
  await openAda(page);
  // A Sales User only reads customers, so every field is plain text. The other fields are there; the field of
  // level 1 is not a field for this user at all.
  await expect(page.getByText('Full Name', { exact: true })).toBeVisible();
  await expect(page.getByText('Credit Limit')).toHaveCount(0);
  await expect(page.getByText('1250.50')).toHaveCount(0);
  // The site custom field of seed.sh is a field like any other.
  await expect(page.getByText('Region', { exact: true })).toBeVisible();
  await snap(page, 'level1-hidden-form');

  await openCustomers(page);
  await page.getByRole('button', { name: 'Columns', exact: true }).click();
  await expect(page.getByRole('checkbox', { name: 'Loyalty Points' })).toBeVisible();
  await expect(page.getByRole('checkbox', { name: 'Credit Limit' })).toHaveCount(0);
  await snap(page, 'level1-hidden-columns');
});

test('a Sales Manager sees credit_limit and can change it', async ({ page }) => {
  await login(page, 'e2e_manager');
  await openAda(page);
  const limit = page.getByLabel('Credit Limit');
  await expect(limit).toHaveValue('1250.50');
  await snap(page, 'level1-visible-form');

  await limit.fill('1300.00');
  const patch = page.waitForRequest((r) => r.method() === 'PATCH');
  await page.getByRole('button', { name: 'Save' }).click();
  expect(JSON.parse((await patch).postData() ?? '{}').data).toEqual({ credit_limit: '1300.00' });
  await expect(page.locator('.toast', { hasText: 'Saved' }).first()).toBeVisible();
  await page.reload();
  await expect(page.getByLabel('Credit Limit')).toHaveValue('1300.00');

  // Put it back, so that the test can run again on the same database.
  await page.getByLabel('Credit Limit').fill('1250.50');
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.locator('.toast', { hasText: 'Saved' }).last()).toBeVisible();
  await page.reload();
  await expect(page.getByLabel('Credit Limit')).toHaveValue('1250.50');

  // The Columns menu offers it.
  await openCustomers(page);
  await page.getByRole('button', { name: 'Columns', exact: true }).click();
  await expect(page.getByRole('checkbox', { name: 'Credit Limit' })).toBeVisible();
});

test('a role that reads level 1 but not writes it sees credit_limit as plain text', async ({ page }) => {
  await login(page, 'e2e_auditor');
  await openAda(page);
  // Not an input: a read-only value, while a level 0 field of the same form is editable.
  await expect(page.getByLabel('Credit Limit')).toHaveCount(0);
  const readOnly = page.locator('dl.readonly', { hasText: 'Credit Limit' });
  await expect(readOnly.locator('dd')).toHaveText('1,250.50');
  await expect(page.getByLabel('Full Name')).toBeEditable();
  await snap(page, 'level1-read-only-form');

  // Saving a level 0 change sends nothing for level 1.
  await page.getByLabel('Loyalty Points').fill('7');
  const patch = page.waitForRequest((r) => r.method() === 'PATCH');
  await page.getByRole('button', { name: 'Save' }).click();
  expect(JSON.parse((await patch).postData() ?? '{}').data).toEqual({ loyalty_points: 7 });
  await expect(page.locator('.toast', { hasText: 'Saved' }).first()).toBeVisible();
  await page.getByLabel('Loyalty Points').fill('');
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.locator('.toast', { hasText: 'Saved' }).last()).toBeVisible();
});

test('a Sales User restricted to Retail sees only the allowed customers and groups', async ({ page }) => {
  await login(page, 'e2e_retail');
  await openCustomers(page);
  // Rita (Retail) and Ada (no group) are visible; Walt (Wholesale) is not.
  await expect(page.getByRole('row', { name: /Rita Retail/ })).toBeVisible();
  await expect(page.getByRole('row', { name: /Ada Lovelace/ })).toBeVisible();
  await expect(page.getByText('Walt Wholesale')).toHaveCount(0);
  await snap(page, 'user-permission-customer-list');

  // A hidden customer opened by its address is the normal "cannot open" state, as if it did not exist.
  await page.goto('/desk/#/form/customer/walt%40example.com');
  await expect(page.getByRole('heading', { name: 'Cannot open this page', level: 1 })).toBeVisible();
  await expect(page.getByRole('alert')).toContainText(/not found/i);
  await expect(page.getByText('Walt Wholesale')).toHaveCount(0);
  await snap(page, 'user-permission-hidden-customer');

  // The group list has the allowed group only, and nothing to create.
  await page.goto('/desk/#/list/customer_group');
  await expect(page.getByRole('status').filter({ hasText: /^Showing / })).toBeVisible();
  await expect(page.getByRole('row', { name: /Retail/ })).toBeVisible();
  await expect(page.getByRole('row', { name: /Wholesale/ })).toHaveCount(0);
  await expect(page.getByRole('main').getByRole('link', { name: 'New', exact: true })).toHaveCount(0);
  await snap(page, 'user-permission-group-list');
});

test('a Sales Manager restricted to Retail is not offered a new customer group', async ({ page }) => {
  // Without the restriction the manager creates groups ...
  await login(page, 'e2e_manager');
  await page.goto('/desk/#/list/customer_group');
  await expect(page.getByRole('row', { name: /Wholesale/ })).toBeVisible();
  await expect(page.getByRole('main').getByRole('link', { name: 'New', exact: true })).toBeVisible();
  await page.getByRole('button', { name: /^Account:/ }).click();
  await page.getByRole('button', { name: 'Log out' }).click();
  await expect(page.getByRole('button', { name: 'Log in' })).toBeVisible();

  // ... and restricted to Retail does not: they could not see the group they made.
  await login(page, 'e2e_retail_manager');
  await page.goto('/desk/#/list/customer_group');
  await expect(page.getByRole('status').filter({ hasText: /^Showing / })).toBeVisible();
  await expect(page.getByRole('row', { name: /Retail/ })).toBeVisible();
  await expect(page.getByRole('row', { name: /Wholesale/ })).toHaveCount(0);
  await expect(page.getByRole('main').getByRole('link', { name: 'New', exact: true })).toHaveCount(0);
  await snap(page, 'user-permission-manager-group-list');
});

test('a site custom field added with the command line is on the customer form and writable', async ({ page }) => {
  await login(page, 'e2e_manager');
  await openAda(page);
  const region = page.getByLabel('Region');
  await expect(region).toBeVisible();
  await region.fill('North');
  const patch = page.waitForRequest((r) => r.method() === 'PATCH');
  await page.getByRole('button', { name: 'Save' }).click();
  expect(JSON.parse((await patch).postData() ?? '{}').data).toEqual({ custom_region: 'North' });
  await expect(page.locator('.toast', { hasText: 'Saved' }).first()).toBeVisible();
  await page.reload();
  await expect(page.getByLabel('Region')).toHaveValue('North');
  await snap(page, 'custom-field-form');

  // Clear it, so that the test can run again on the same database.
  await page.getByLabel('Region').fill('');
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.locator('.toast', { hasText: 'Saved' }).last()).toBeVisible();
});
