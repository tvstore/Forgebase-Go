// Import and export in the desk (M9.3, ADR 0021), against the server prepared by seed.sh: the
// Export menu of a list (the current filters, the shown columns), the Import page (validate with
// a row error, then import), a report export, and a clerk who has none of the rights. Every test
// uses names of its own (new at each run), because the database keeps the data of earlier runs.

import { expect, test } from '@playwright/test';
import type { APIRequestContext, Download, Page } from '@playwright/test';

const PASSWORD = process.env.E2E_PASSWORD ?? 'e2e-test-password';

async function login(page: Page, username: string): Promise<void> {
  await page.goto('/desk/');
  await page.getByLabel('Username').fill(username);
  await page.getByLabel('Password').fill(PASSWORD);
  await page.getByRole('button', { name: 'Log in' }).click();
  await expect(page.getByRole('heading', { name: 'Home', level: 1 })).toBeVisible();
}

async function logout(page: Page): Promise<void> {
  await page.getByRole('button', { name: /^Account:/ }).click();
  await page.getByRole('button', { name: 'Log out' }).click();
  await expect(page.getByRole('button', { name: 'Log in' })).toBeVisible();
}

/** The open page does not scroll sideways at the width of a phone and of a small one. */
async function expectNoHorizontalScroll(page: Page, what: string): Promise<void> {
  const before = page.viewportSize() ?? { width: 1280, height: 800 };
  for (const width of [375, 320]) {
    await page.setViewportSize({ width, height: 800 });
    const widths = await page.evaluate(() => ({
      scroll: document.documentElement.scrollWidth,
      client: document.documentElement.clientWidth,
    }));
    expect(widths.scroll, `${what} scrolls sideways at ${width} px`).toBeLessThanOrEqual(widths.client);
  }
  await page.setViewportSize(before);
}

async function expectLoaded(page: Page): Promise<void> {
  await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
}

/** The text of a download. */
async function textOf(download: Download): Promise<string> {
  const stream = await download.createReadStream();
  const chunks: Uint8Array[] = [];
  for await (const chunk of stream as AsyncIterable<Uint8Array>) chunks.push(chunk);
  const size = chunks.reduce((n, c) => n + c.length, 0);
  const bytes = new Uint8Array(size);
  let at = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, at);
    at += chunk.length;
  }
  return new TextDecoder().decode(bytes);
}

/** A name that no earlier run used. */
const unique = (prefix: string): string => `${prefix}${Date.now().toString(36)}${Math.floor(Math.random() * 1296).toString(36)}`;

/** Creates customers through the API, with a session of its own (the desk's CSRF token stays). */
async function createCustomers(
  request: APIRequestContext,
  customers: { email: string; full_name: string; tier?: string }[],
): Promise<void> {
  const api = await request.post('/api/v1/login', { data: { username: 'e2e_manager', password: PASSWORD } });
  expect(api.ok()).toBe(true);
  const csrf = ((await api.json()) as { data: { csrf_token: string } }).data.csrf_token;
  for (const customer of customers) {
    const created = await request.post('/api/v1/resource/customer', {
      data: customer,
      headers: { 'X-Forgebase-CSRF': csrf },
    });
    expect(created.status(), `${customer.email} is created`).toBe(201);
  }
}

const rows = (page: Page) => page.locator('.list-table tbody tr');

test('a manager exports the filtered list as CSV and JSON; a clerk is not offered Export or Import', async ({ page, request }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  const tag = unique('Exp');
  await createCustomers(request, [
    { email: `${tag}-1@example.com`, full_name: `${tag} One`, tier: 'gold' },
    { email: `${tag}-2@example.com`, full_name: `${tag} Two`, tier: 'silver' },
    { email: `${tag}-3@example.com`, full_name: `${tag} Three`, tier: 'gold' },
    { email: `${tag}-other@example.com`, full_name: `Zzz Unrelated ${tag.toLowerCase()}`, tier: 'gold' },
  ]);

  await login(page, 'e2e_manager');
  // The search is part of the list's filters: only the three customers named after the tag.
  await page.goto(`/desk/#/list/customer?q=${tag}`);
  await expect(rows(page)).toHaveCount(3);
  await expectLoaded(page);

  const exportMenu = page.getByRole('button', { name: 'Export', exact: true });
  await exportMenu.click();
  await expect(page.getByText('Export this list')).toBeVisible();
  await expectNoHorizontalScroll(page, 'the list with the Export menu open');
  const csvLink = page.getByRole('link', { name: 'CSV', exact: true });
  await expect(csvLink).toBeVisible();

  const [csvDownload] = await Promise.all([page.waitForEvent('download'), csvLink.click()]);
  expect(csvDownload.suggestedFilename()).toMatch(/^customer-\d{8}T\d{6}Z\.csv$/);
  const csv = await textOf(csvDownload);
  const lines = csv.split('\r\n');
  expect(lines.pop(), 'the file ends with a line end').toBe('');
  // The shown columns, with the title the Name column shows.
  expect(lines[0]).toBe('name,full_name,customer_group,tier,owner,modified_at');
  expect(lines).toHaveLength(4);
  expect(lines.slice(1).map((l) => l.split(',')[0]).sort()).toEqual([
    `${tag}-1@example.com`,
    `${tag}-2@example.com`,
    `${tag}-3@example.com`,
  ]);
  expect(csv).not.toContain('Unrelated');

  // The same filters as JSON: one object per document.
  await exportMenu.click();
  const [jsonDownload] = await Promise.all([
    page.waitForEvent('download'),
    page.getByRole('link', { name: 'JSON', exact: true }).click(),
  ]);
  expect(jsonDownload.suggestedFilename()).toMatch(/^customer-\d{8}T\d{6}Z\.json$/);
  const documents = JSON.parse(await textOf(jsonDownload)) as { name: string; full_name: string; tier: string }[];
  expect(documents.map((d) => d.full_name).sort()).toEqual([`${tag} One`, `${tag} Three`, `${tag} Two`]);
  expect(Object.keys(documents[0] ?? {}).sort()).toEqual(
    ['customer_group', 'full_name', 'modified_at', 'name', 'owner', 'tier'],
  );

  // The Export menu is reachable with the keyboard and closes with Escape.
  await exportMenu.focus();
  await page.keyboard.press('Enter');
  await expect(csvLink).toBeVisible();
  await page.keyboard.press('Tab');
  await expect(csvLink).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(csvLink).toHaveCount(0);
  await expect(exportMenu).toBeFocused();

  // A clerk may read customers but has neither right: no Export, no Import, and the API refuses.
  await logout(page);
  await login(page, 'e2e_clerk');
  await page.goto('/desk/#/list/customer');
  await expect(page.getByRole('heading', { name: 'Customer', level: 1 })).toBeVisible();
  await expectLoaded(page);
  await expect(page.getByRole('button', { name: 'Export', exact: true })).toHaveCount(0);
  await expect(page.getByRole('link', { name: 'Import', exact: true })).toHaveCount(0);
  const refused = await page.request.get('/api/v1/export/customer');
  expect(refused.status()).toBe(403);
  expect(((await refused.json()) as { error: { code: string } }).error.code).toBe('permission_denied');
  await page.goto('/desk/#/import/customer');
  await expect(page.getByRole('alert')).toContainText('You may not import');
});

/** The file picker of the Import page. */
const fileInput = (page: Page) => page.getByLabel('File', { exact: true });
const importButton = (page: Page) => page.getByRole('button', { name: 'Import', exact: true });
const validateButton = (page: Page) => page.getByRole('button', { name: 'Validate', exact: true });

test('an import with a bad row shows the row error after Validate, and nothing is created', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  const tag = unique('Bad');
  const file = [
    'email,full_name,tier',
    `${tag}-1@example.com,${tag} One,gold`,
    `${tag}-2@example.com,,silver`, // the full name is required
    `${tag}-3@example.com,${tag} Three,gold`,
    '',
  ].join('\r\n');

  await login(page, 'e2e_manager');
  await page.goto('/desk/#/list/customer');
  await page.getByRole('main').getByRole('link', { name: 'Import', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Import Customer', level: 1 })).toBeVisible();
  await expect(page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: 'Customer' })).toBeVisible();

  // Nothing to validate or import before a file is chosen.
  await expect(importButton(page)).toHaveAttribute('aria-disabled', 'true');
  await fileInput(page).setInputFiles({ name: 'people.csv', mimeType: 'text/csv', buffer: Buffer.from(file) });
  await expect(page.getByLabel('Format')).toHaveValue('csv');
  await expect(importButton(page)).toHaveAttribute('aria-disabled', 'true');

  await validateButton(page).click();
  // The error is announced and listed with its row, the line of the file and the document.
  await expect(page.getByRole('alert').filter({ hasText: '1 row has errors out of 3 rows' })).toBeVisible();
  const table = page.getByRole('region', { name: 'Rows with errors' });
  await expect(table.locator('tbody tr')).toHaveCount(1);
  await expect(table.locator('tbody tr td').nth(0)).toHaveText('2 (line 3)');
  await expect(table.locator('tbody tr td').nth(1)).toHaveText(`${tag}-2@example.com`);
  await expect(table.locator('tbody tr td').nth(2)).toContainText('full_name');
  // It cannot be imported until it validates without errors.
  await expect(importButton(page)).toHaveAttribute('aria-disabled', 'true');
  // `aria-disabled`, not `disabled`: it stays focusable, and a click does nothing.
  await importButton(page).click({ force: true });
  await expect(page.getByRole('heading', { name: 'Import finished' })).toHaveCount(0);
  await expectNoHorizontalScroll(page, 'the import page with an error');

  // Nothing was created by the validation, nor by the refused click.
  await page.goto(`/desk/#/list/customer?q=${tag}`);
  await expect(page.getByRole('heading', { name: 'No documents match' })).toBeVisible();
});

test('a valid import creates the documents and the list shows them', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  const tag = unique('Ok');
  const file = [
    'email,full_name,tier,loyalty_points',
    `${tag}-1@example.com,${tag} One,gold,10`,
    `${tag}-2@example.com,${tag} Two,silver,`,
    `${tag}-3@example.com,${tag} Three,standard,3`,
    '',
  ].join('\r\n');

  await login(page, 'e2e_manager');
  await page.goto('/desk/#/import/customer');
  await expect(page.getByRole('heading', { name: 'Import Customer', level: 1 })).toBeVisible();
  await fileInput(page).setInputFiles({ name: 'people.csv', mimeType: 'text/csv', buffer: Buffer.from(file) });

  await validateButton(page).click();
  await expect(page.getByText('No errors. 3 rows: 3 new.')).toBeVisible();
  await expect(importButton(page)).not.toHaveAttribute('aria-disabled', 'true');
  // Another choice is another validation: the button is off until the file is checked again.
  await page.getByLabel('Mode').selectOption('upsert');
  await expect(importButton(page)).toHaveAttribute('aria-disabled', 'true');
  await page.getByLabel('Mode').selectOption('insert');
  await validateButton(page).click();
  await expect(importButton(page)).not.toHaveAttribute('aria-disabled', 'true');
  await expectNoHorizontalScroll(page, 'the import page after a validation');

  await importButton(page).click();
  await expect(page.getByRole('heading', { name: 'Import finished' })).toBeVisible();
  await expect(page.locator('.toast', { hasText: 'Imported: 3 rows: 3 new.' })).toBeVisible();
  await expectNoHorizontalScroll(page, 'the import page after the import');

  await page.getByRole('link', { name: 'Back to Customer' }).click();
  await expect(page.getByRole('heading', { name: 'Customer', level: 1 })).toBeVisible();
  await page.goto(`/desk/#/list/customer?q=${tag}`);
  await expect(rows(page)).toHaveCount(3);
  await expect(page.getByRole('main')).toContainText(`${tag} Two`);

  // The same file again is refused by the server (the e-mail names the customer: it exists).
  await page.goto('/desk/#/import/customer');
  await fileInput(page).setInputFiles({ name: 'people.csv', mimeType: 'text/csv', buffer: Buffer.from(file) });
  await validateButton(page).click();
  await expect(page.getByRole('alert').filter({ hasText: '3 rows have errors out of 3 rows' })).toBeVisible();
  await expect(importButton(page)).toHaveAttribute('aria-disabled', 'true');
});

test('a manager exports the result of a report as CSV and JSON (M9.3)', async ({ page, request }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  // One order in a year of its own, through the API with a session of its own.
  const api = await request.post('/api/v1/login', { data: { username: 'e2e_manager', password: PASSWORD } });
  const csrf = ((await api.json()) as { data: { csrf_token: string } }).data.csrf_token;
  const year = 2100 + (Date.now() % 7000);
  const created = await request.post('/api/v1/resource/sales_order', {
    data: {
      customer: 'ada@example.com',
      order_date: `${year}-06-15`,
      items: [{ description: 'Widget', qty: 3, rate: '10.00' }],
    },
    headers: { 'X-Forgebase-CSRF': csrf },
  });
  expect(created.status()).toBe(201);

  await login(page, 'e2e_manager');
  await page.goto(`/desk/#/report/e2e.sales_by_customer?from=${year}-06-01&to=${year}-06-30`);
  await expect(page.locator('.report-table tbody tr').first()).toBeVisible();
  await expectLoaded(page);

  const menu = page.getByRole('button', { name: 'Export', exact: true });
  await menu.click();
  await expect(page.getByText('Export this result')).toBeVisible();
  const [csvDownload] = await Promise.all([
    page.waitForEvent('download'),
    page.getByRole('button', { name: 'CSV', exact: true }).click(),
  ]);
  expect(csvDownload.suggestedFilename()).toMatch(/^e2e\.sales_by_customer-\d{8}T\d{6}Z\.csv$/);
  const csv = await textOf(csvDownload);
  const lines = csv.split('\r\n');
  expect(lines[0]).toBe('customer,orders,total,last_order');
  expect(csv).toContain(`Ada Lovelace,`);
  expect(csv).toContain(`,${year}-06-15\r\n`);
  await expect(page.locator('.toast', { hasText: 'Exported' })).toBeVisible();

  await menu.click();
  const [jsonDownload] = await Promise.all([
    page.waitForEvent('download'),
    page.getByRole('button', { name: 'JSON', exact: true }).click(),
  ]);
  expect(jsonDownload.suggestedFilename()).toMatch(/\.json$/);
  const result = JSON.parse(await textOf(jsonDownload)) as {
    columns: { name: string }[];
    rows: unknown[][];
    truncated: boolean;
  };
  expect(result.columns.map((c) => c.name)).toEqual(['customer', 'orders', 'total', 'last_order']);
  expect(result.truncated).toBe(false);
  expect(result.rows.some((r) => r[0] === 'Ada Lovelace')).toBe(true);
  await expectNoHorizontalScroll(page, 'a report with the Export menu');

  // The clerk is not offered reports, so there is nothing to export; the API says 404 as for a
  // report that does not exist.
  await logout(page);
  await login(page, 'e2e_clerk');
  const refused = await page.request.post('/api/v1/report/e2e.sales_by_customer/export?format=csv', {
    data: { params: { from: `${year}-06-01`, to: `${year}-06-30` } },
    headers: { 'X-Forgebase-CSRF': await csrfOf(page) },
  });
  expect(refused.status()).toBe(404);
});

/** A fresh CSRF token of the page's session (the desk keeps its own in memory). */
async function csrfOf(page: Page): Promise<string> {
  const response = await page.request.get('/api/v1/session', { headers: { 'X-Forgebase-CSRF': 'refresh' } });
  return ((await response.json()) as { data: { csrf_token: string } }).data.csrf_token;
}
