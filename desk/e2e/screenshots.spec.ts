// Screenshots of the desk for a pull request (UX.2, UX.4). Skipped unless SCREENSHOTS=1, so CI is not
// affected. Run it against a server prepared by seed.sh (see CONTRIBUTING.md):
//
//   SCREENSHOTS=1 SCREENSHOT_DIR=/some/dir npx playwright test screenshots
//
// It adds a few sample documents (customers, and orders that are a draft, submitted and
// cancelled, so that every status badge shows), then takes the login page, home, list, form (draft, focused field and cell, error state, and
// submitted), the cancel dialog and the command bar at 1280, 800 and 375 px (and the list at 320 px), and asserts that
// no page scrolls sideways at those phone widths. M8.2 adds the workflow screens (workflow-*: a draft, a request
// waiting for approval with its actions, the confirmation, an approved one with its history, and the list with
// the state badges) on the e2e-only DocType purchase_request. M8.3 adds the Views menu, the Save view dialog, the
// Summary panel and the Columns menu (views-menu, save-view-dialog, summary-panel, columns-menu). M8.4 adds the
// report page (report-params, report-result, report-empty). M9.3 adds the Export menu of a list and the Import page
// (export-menu, import-errors, import-done). The PNGs are not committed.

import { expect, test } from '@playwright/test';
import type { Page } from '@playwright/test';

const PASSWORD = process.env.E2E_PASSWORD ?? 'e2e-test-password';
const DIR = process.env.SCREENSHOT_DIR ?? 'test-results/screenshots';
const WIDTHS = [1280, 800, 375];

test.skip(process.env.SCREENSHOTS !== '1', 'set SCREENSHOTS=1 to take the screenshots');

/** A screenshot taken after the short fade-in and pop-in animations have finished. */
async function snap(page: Page, path: string): Promise<void> {
  // The pointer out of the way, so that no field is captured in its hover fill.
  await page.mouse.move(0, 0);
  await page.waitForTimeout(400);
  await page.screenshot({ path });
}

async function noSideScroll(page: Page, what: string): Promise<void> {
  const w = await page.evaluate(() => ({
    scroll: document.documentElement.scrollWidth,
    client: document.documentElement.clientWidth,
  }));
  expect(w.scroll, `${what} scrolls sideways`).toBeLessThanOrEqual(w.client);
}

async function login(page: Page, username: string): Promise<void> {
  await page.goto('/desk/');
  await page.getByLabel('Username').fill(username);
  await page.getByLabel('Password').fill(PASSWORD);
  await page.getByRole('button', { name: 'Log in' }).click();
  await expect(page.getByRole('heading', { name: 'Home', level: 1 })).toBeVisible();
}

/** Sample data through the API, as the logged-in manager (the desk's own session and CSRF token). */
async function seed(page: Page): Promise<{ draft: string; submitted: string; cancelled: string }> {
  return page.evaluate(async () => {
    const session = (await (await fetch('/api/v1/session', { headers: { 'X-Forgebase-CSRF': 'refresh' } })).json()) as {
      data: { csrf_token: string };
    };
    const send = async (method: string, path: string, body: unknown) => {
      const response = await fetch(`/api/v1${path}`, {
        method,
        headers: { 'Content-Type': 'application/json', 'X-Forgebase-CSRF': session.data.csrf_token },
        body: JSON.stringify(body),
      });
      const json = (await response.json().catch(() => ({}))) as { data?: { name: string; version: number } };
      return { ok: response.ok, doc: json.data };
    };
    const people = [
      ['grace@example.com', 'Grace Hopper', 'gold', 4200],
      ['alan@example.com', 'Alan Turing', 'silver', 1250],
      ['edsger@example.com', 'Edsger Dijkstra', 'standard', 300],
    ] as const;
    for (const [email, full_name, tier, loyalty_points] of people) {
      await send('POST', '/resource/customer', { email, full_name, tier, loyalty_points, active: true }); // a second run: already there
    }
    const order = async (customer: string, rows: [string, number, string][]) => {
      const created = await send('POST', '/resource/sales_order', {
        customer,
        order_date: '2026-10-29',
        items: rows.map(([description, qty, rate]) => ({ description, qty, rate })),
      });
      if (!created.doc) throw new Error('order not created');
      return created.doc;
    };
    const draft = await order('ada@example.com', [['Widget', 2, '9.50']]);
    const submitted = await order('grace@example.com', [
      ['Gadget', 10, '1250.00'],
      ['Cable', 4, '12.00'],
    ]);
    const done = await send('POST', `/resource/sales_order/${submitted.name}/submit`, { version: submitted.version });
    const toCancel = await order('alan@example.com', [['Sprocket', 100, '3.25']]);
    const s2 = await send('POST', `/resource/sales_order/${toCancel.name}/submit`, { version: toCancel.version });
    const c2 = await send('POST', `/resource/sales_order/${toCancel.name}/cancel`, { version: s2.doc?.version ?? 0 });
    if (!done.ok || !s2.ok || !c2.ok) throw new Error('could not submit or cancel the sample orders');
    return { draft: draft.name, submitted: submitted.name, cancelled: toCancel.name };
  });
}

test('screenshots of login, home, list and form', async ({ page }) => {
  test.setTimeout(120_000); // many screenshots at three widths
  // ---- login (logged out) -------------------------------------------------------------
  for (const width of WIDTHS) {
    await page.setViewportSize({ width, height: 800 });
    await page.goto('/desk/#/login');
    await expect(page.getByRole('button', { name: 'Log in' })).toBeVisible();
    await noSideScroll(page, `login at ${width}`);
    await snap(page, `${DIR}/login-${width}.png`);
  }
  // A failed login shows the alert box.
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.getByLabel('Username').fill('e2e_manager');
  await page.getByLabel('Password').fill('wrong-password');
  await page.getByRole('button', { name: 'Log in' }).click();
  await expect(page.getByRole('alert')).toBeVisible();
  await snap(page, `${DIR}/login-error-1280.png`);

  await login(page, 'e2e_manager');
  const docs = await seed(page);

  for (const width of WIDTHS) {
    await page.setViewportSize({ width, height: 800 });

    await page.goto('/desk/#/');
    await expect(page.getByRole('main').getByRole('link', { name: 'Sales Order', exact: true })).toBeVisible();
    await noSideScroll(page, `home at ${width}`);
    await snap(page, `${DIR}/home-${width}.png`);

    await page.goto('/desk/#/list/sales_order');
    await expect(page.getByRole('row', { name: new RegExp(docs.submitted) })).toBeVisible();
    await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
    await noSideScroll(page, `list at ${width}`);
    await snap(page, `${DIR}/list-${width}.png`);

    await page.goto(`/desk/#/form/sales_order/${encodeURIComponent(docs.draft)}`);
    await expect(page.locator('.status-badge')).toHaveText('Draft');
    await expect(page.getByLabel('Description, row 1')).toBeVisible();
    await noSideScroll(page, `form at ${width}`);
    await snap(page, `${DIR}/form-${width}.png`);

    // UX.5: a focused field (white, grey border, navy keyboard ring) and a focused child-table cell.
    await page.getByLabel('Remarks').focus();
    await snap(page, `${DIR}/form-focus-${width}.png`);
    await page.getByLabel('Description, row 1').focus();
    await snap(page, `${DIR}/form-cell-focus-${width}.png`);

    // UX.5: the error state, a field and a child-table cell with a problem (an empty row, then Save).
    await page.goto('/desk/#/new/sales_order');
    await expect(page.getByRole('heading', { name: 'New Sales Order', exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Add row' }).click();
    await page.getByRole('button', { name: 'Save' }).click();
    await expect(page.getByRole('alert')).toBeVisible();
    await noSideScroll(page, `form with errors at ${width}`);
    await snap(page, `${DIR}/form-error-${width}.png`);
    await page.goto('/desk/#/list/sales_order');
    await page.getByRole('button', { name: 'Leave and discard' }).click(); // the new form has an unsaved row

    await page.goto(`/desk/#/form/sales_order/${encodeURIComponent(docs.submitted)}`);
    await expect(page.locator('.status-badge')).toHaveText('Submitted');
    await noSideScroll(page, `submitted form at ${width}`);
    await snap(page, `${DIR}/form-submitted-${width}.png`);

    // The dialog (Cancel is a destructive question): on a phone it must fit the screen.
    await page.getByRole('button', { name: 'More actions' }).click();
    await page.getByRole('button', { name: 'Cancel', exact: true }).click();
    await expect(page.getByRole('dialog')).toBeVisible();
    await noSideScroll(page, `dialog at ${width}`);
    await snap(page, `${DIR}/dialog-${width}.png`);
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toHaveCount(0);

    // The command bar (Ctrl+K), with a few results.
    await page.keyboard.press('Control+k');
    await expect(page.getByRole('dialog', { name: 'Search or jump to' })).toBeVisible();
    await page.getByRole('combobox').fill('sales');
    await expect(page.getByRole('option', { name: 'New Sales Order', exact: true })).toBeVisible();
    await noSideScroll(page, `command bar at ${width}`);
    await snap(page, `${DIR}/command-bar-${width}.png`);
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toHaveCount(0);

    // M8.4: the report page before a run (the parameters), with a result and with no rows.
    await page.goto('/desk/#/report/e2e.sales_by_customer');
    await expect(page.getByRole('heading', { name: 'Sales by customer', level: 1 })).toBeVisible();
    await noSideScroll(page, `report parameters at ${width}`);
    await snap(page, `${DIR}/report-params-${width}.png`);

    await page.goto('/desk/#/report/e2e.sales_by_customer?from=2026-10-01&to=2026-10-31&include_cancelled=true');
    await expect(page.locator('.report-table tbody tr')).toHaveCount(3);
    await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
    await noSideScroll(page, `report result at ${width}`);
    await snap(page, `${DIR}/report-result-${width}.png`);

    await page.goto('/desk/#/report/e2e.sales_by_customer?from=2020-01-01&to=2020-01-31');
    await expect(page.getByRole('heading', { name: 'No rows' })).toBeVisible();
    await noSideScroll(page, `report with no rows at ${width}`);
    await snap(page, `${DIR}/report-empty-${width}.png`);
  }

  // ---- more states at 1280 ------------------------------------------------------------
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto('/desk/#/list/customer');
  await expect(page.getByRole('row', { name: /Grace Hopper/ })).toBeVisible();
  await snap(page, `${DIR}/list-customer-1280.png`);

  await page.goto('/desk/#/list/sales_order?q=zzz');
  await expect(page.getByRole('heading', { name: 'No documents match' })).toBeVisible();
  await snap(page, `${DIR}/list-empty-1280.png`);

  await page.goto('/desk/#/list/sales_order');
  await page.getByRole('button', { name: 'Filter', exact: true }).click();
  await page.getByRole('button', { name: 'Add filter' }).click();
  await snap(page, `${DIR}/list-filters-1280.png`);

  // An active filter is a chip.
  const filters = encodeURIComponent(JSON.stringify([{ field: 'owner', op: '=', value: 'e2e_manager' }]));
  await page.goto(`/desk/#/list/sales_order?filters=${filters}`);
  await expect(page.getByRole('button', { name: 'Remove filter: Owner is e2e_manager' })).toBeVisible();
  await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
  await snap(page, `${DIR}/list-chips-1280.png`);

  await page.goto(`/desk/#/form/sales_order/${encodeURIComponent(docs.submitted)}`);
  await expect(page.locator('.status-badge')).toHaveText('Submitted');
  await page.getByRole('button', { name: 'More actions' }).click();
  await snap(page, `${DIR}/form-submitted-menu-1280.png`);

  await page.goto(`/desk/#/form/sales_order/${encodeURIComponent(docs.cancelled)}`);
  await expect(page.locator('.status-badge')).toHaveText('Cancelled');
  await snap(page, `${DIR}/form-cancelled-1280.png`);

  await page.goto('/desk/#/new/sales_order');
  await expect(page.getByRole('heading', { name: 'New Sales Order', exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Save' }).click(); // empty: the required field is refused
  await expect(page.getByRole('alert')).toBeVisible();
  await snap(page, `${DIR}/form-new-errors-1280.png`);

  await page.goto(`/desk/#/form/sales_order/${encodeURIComponent(docs.draft)}`);
  await page.getByLabel('Remarks').fill('Deliver on Friday.');
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.locator('.toast', { hasText: 'Saved' })).toBeVisible();
  await snap(page, `${DIR}/form-saved-toast-1280.png`);

  // The smallest phone and a submitted form on a phone (the status badge sits under the title in the list).
  await page.setViewportSize({ width: 320, height: 800 });
  await page.goto('/desk/#/list/sales_order');
  await expect(page.getByRole('row', { name: new RegExp(docs.submitted) })).toBeVisible();
  await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
  await expect(page.locator('.toast')).toHaveCount(0, { timeout: 8000 }); // an earlier "Saved" is gone
  await noSideScroll(page, 'list at 320');
  await snap(page, `${DIR}/list-320.png`);

  await page.setViewportSize({ width: 375, height: 800 });
  await page.goto(`/desk/#/form/sales_order/${encodeURIComponent(docs.submitted)}`);
  await expect(page.locator('.status-badge')).toHaveText('Submitted');
  await page.getByRole('button', { name: 'Menu' }).click();
  await snap(page, `${DIR}/form-menu-open-375.png`);
});

/** Purchase requests in every state of their workflow, made through the API as the manager. */
async function seedRequests(page: Page): Promise<{ draft: string; pending: string; approved: string }> {
  return page.evaluate(async () => {
    const session = (await (await fetch('/api/v1/session', { headers: { 'X-Forgebase-CSRF': 'refresh' } })).json()) as {
      data: { csrf_token: string };
    };
    type Doc = { name: string; version: number };
    const send = async (method: string, path: string, body: unknown): Promise<Doc> => {
      const response = await fetch(`/api/v1${path}`, {
        method,
        headers: { 'Content-Type': 'application/json', 'X-Forgebase-CSRF': session.data.csrf_token },
        body: JSON.stringify(body),
      });
      const json = (await response.json()) as { data?: Doc };
      if (!response.ok || !json.data) throw new Error(`${method} ${path} failed`);
      return json.data;
    };
    const create = (title: string, amount: string) => send('POST', '/resource/purchase_request', { title, amount });
    const move = (doc: Doc, action: string) =>
      send('POST', `/resource/purchase_request/${encodeURIComponent(doc.name)}/transition`, {
        action,
        version: doc.version,
      });
    const draft = await create('Standing desks for the new office', '2400.00');
    const pending = await move(await create('Laptops for the support team', '5600.00'), 'Submit for Approval');
    const approved = await move(
      await move(await create('Annual software licences', '18250.00'), 'Submit for Approval'),
      'Approve',
    );
    const rejected = await move(await create('Espresso machine', '899.00'), 'Submit for Approval');
    await move(await move(rejected, 'Reject'), 'Submit for Approval'); // a longer history
    const cancelled = await move(
      await move(await create('Conference tickets', '3100.00'), 'Submit for Approval'),
      'Approve',
    );
    await move(cancelled, 'Cancel Approval');
    return { draft: draft.name, pending: pending.name, approved: approved.name };
  });
}

test('screenshots of the workflow', async ({ page }) => {
  await login(page, 'e2e_manager');
  const docs = await seedRequests(page);
  const form = (name: string) => `/desk/#/form/purchase_request/${encodeURIComponent(name)}`;

  for (const width of WIDTHS) {
    await page.setViewportSize({ width, height: 800 });

    await page.goto(form(docs.draft));
    await expect(page.locator('.status-badge')).toHaveText('Draft');
    await expect(page.getByRole('button', { name: 'Submit for Approval' })).toBeVisible();
    await noSideScroll(page, `workflow draft at ${width}`);
    await snap(page, `${DIR}/workflow-form-draft-${width}.png`);

    await page.goto(form(docs.pending));
    await expect(page.locator('.status-badge')).toHaveText('Pending Approval');
    await expect(page.getByRole('button', { name: 'Approve' })).toBeVisible();
    await expect(page.getByRole('list', { name: 'Workflow history' })).toBeVisible();
    await noSideScroll(page, `workflow pending at ${width}`);
    await snap(page, `${DIR}/workflow-form-pending-${width}.png`);

    await page.getByRole('button', { name: 'Approve' }).click();
    await expect(page.getByRole('dialog', { name: 'Approve' })).toBeVisible();
    await noSideScroll(page, `workflow confirmation at ${width}`);
    await snap(page, `${DIR}/workflow-confirm-${width}.png`);
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toHaveCount(0);

    await page.goto(form(docs.approved));
    await expect(page.locator('.status-badge')).toHaveText('Approved');
    await expect(page.getByRole('list', { name: 'Workflow history' }).getByRole('listitem')).toHaveCount(2);
    await noSideScroll(page, `workflow approved at ${width}`);
    await snap(page, `${DIR}/workflow-form-approved-${width}.png`);

    await page.goto('/desk/#/list/purchase_request');
    await expect(page.getByRole('row', { name: new RegExp(docs.approved) })).toBeVisible();
    await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
    await noSideScroll(page, `workflow list at ${width}`);
    await snap(page, `${DIR}/workflow-list-${width}.png`);
  }

  // Unsaved changes: the actions are disabled, Save is the primary button.
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto(form(docs.pending));
  await page.getByLabel('Title').fill('Laptops for the whole support team');
  await expect(page.getByRole('button', { name: 'Approve' })).toBeDisabled();
  await snap(page, `${DIR}/workflow-form-dirty-1280.png`);
});

test('screenshots of saved views, the summary and the columns', async ({ page }) => {
  await login(page, 'e2e_manager');
  await seedRequests(page);
  const list = '/desk/#/list/purchase_request';

  // A shared view, made by the administrator, so that the menu shows both groups.
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto('/desk/');
  await page.getByRole('button', { name: /^Account:/ }).click();
  await page.getByRole('button', { name: 'Log out' }).click();
  await login(page, 'e2e_admin');
  await page.goto(`${list}?filters=${encodeURIComponent(JSON.stringify([{ field: 'workflow_state', op: '=', value: 'Pending Approval' }]))}`);
  await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
  await page.getByRole('button', { name: 'Views', exact: true }).click();
  await page.getByRole('button', { name: /^Save view/ }).click();
  await page.getByLabel('Name').fill('Waiting for approval');
  await page.getByLabel('Shared with everyone').check();
  await page.getByRole('dialog', { name: 'Save view' }).getByRole('button', { name: 'Save', exact: true }).click();
  await expect(page.locator('.toast', { hasText: 'View saved' })).toBeVisible();
  await page.getByRole('button', { name: /^Account:/ }).click();
  await page.getByRole('button', { name: 'Log out' }).click();
  await login(page, 'e2e_manager');

  // The manager's own view: drafts, newest amount first.
  await page.goto(`${list}?sort=amount%3Adesc&filters=${encodeURIComponent(JSON.stringify([{ field: 'workflow_state', op: '=', value: 'Draft' }]))}`);
  await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
  await page.getByRole('button', { name: 'Views', exact: true }).click();
  await page.getByRole('button', { name: /^Save view/ }).click();
  await page.getByLabel('Name').fill('My drafts by amount');
  await page.getByRole('dialog', { name: 'Save view' }).getByRole('button', { name: 'Save', exact: true }).click();
  await expect(page.locator('.toast', { hasText: 'View saved' })).toBeVisible();

  for (const width of WIDTHS) {
    await page.setViewportSize({ width, height: 800 });
    await page.goto(list);
    await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);

    // The Views menu with a shared and a private view; the current one is marked.
    await page.getByRole('button', { name: 'Views', exact: true }).click();
    await page.getByRole('button', { name: 'My drafts by amount' }).click();
    await expect(page.locator('.view-label')).toHaveText('\u00B7 My drafts by amount');
    await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
    await page.getByRole('button', { name: 'Views', exact: true }).click();
    await noSideScroll(page, `views menu at ${width}`);
    await snap(page, `${DIR}/views-menu-${width}.png`);
    await page.keyboard.press('Escape');

    // The Save view dialog (an administrator would also see "Shared with everyone").
    await page.getByRole('button', { name: 'Views', exact: true }).click();
    await page.getByRole('button', { name: /^Save view/ }).click();
    await page.getByLabel('Name').fill('Drafts, sorted');
    await noSideScroll(page, `save view dialog at ${width}`);
    await snap(page, `${DIR}/save-view-dialog-${width}.png`);
    await page.keyboard.press('Escape');
    await expect(page.getByRole('dialog')).toHaveCount(0);

    // The Summary panel over the whole list.
    await page.goto(list);
    await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
    await page.getByRole('button', { name: 'Summary', exact: true }).click();
    await expect(page.getByRole('table', { name: 'Purchase Request by Status' })).toBeVisible();
    await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
    await noSideScroll(page, `summary panel at ${width}`);
    await snap(page, `${DIR}/summary-panel-${width}.png`);

    // The Columns menu.
    await page.getByRole('button', { name: 'Summary', exact: true }).click();
    await page.getByRole('button', { name: 'Columns', exact: true }).click();
    await page.getByRole('checkbox', { name: 'Owner' }).uncheck();
    await page.evaluate(() => window.scrollTo(0, 0));
    await noSideScroll(page, `columns menu at ${width}`);
    await snap(page, `${DIR}/columns-menu-${width}.png`);
    await page.keyboard.press('Escape');
  }
  await page.setViewportSize({ width: 320, height: 800 });
  await page.goto(list);
  await page.getByRole('button', { name: 'Summary', exact: true }).click();
  await expect(page.getByRole('table', { name: 'Purchase Request by Status' })).toBeVisible();
  await noSideScroll(page, 'summary panel at 320');
  await snap(page, `${DIR}/summary-panel-320.png`);
});

test('screenshots of the Export menu and the Import page (M9.3)', async ({ page }) => {
  test.setTimeout(120_000);
  // A fresh Import page: through the home page, so that the page is mounted again (and, with a
  // reload first, the toasts of the step before are gone).
  const openImport = async () => {
    await page.goto('/desk/#/');
    await page.reload();
    await page.goto('/desk/#/import/customer');
    await expect(page.getByRole('heading', { name: 'Import Customer', level: 1 })).toBeVisible();
  };
  const tag = `Shot${Date.now().toString(36)}`;
  const file = (rows: string[]) =>
    ['email,full_name,tier,loyalty_points', ...rows, ''].join('\r\n');
  const bad = file([
    `${tag}-1@example.com,${tag} One,gold,10`,
    `${tag}-2@example.com,,silver,`,
    `${tag}-3@example.com,${tag} Three,platinum,3`,
    `${tag}-4@example.com,${tag} Four,gold,many`,
  ]);
  const good = file([
    `${tag}-1@example.com,${tag} One,gold,10`,
    `${tag}-2@example.com,${tag} Two,silver,`,
    `${tag}-3@example.com,${tag} Three,standard,3`,
  ]);

  await login(page, 'e2e_manager');
  for (const width of WIDTHS) {
    await page.setViewportSize({ width, height: 800 });

    // The list with the Export menu open (a reload first: no toast of the step before).
    await page.goto('/desk/#/');
    await page.reload();
    await page.goto('/desk/#/list/customer');
    await expect(page.getByRole('heading', { name: 'Customer', level: 1 })).toBeVisible();
    await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
    await page.getByRole('button', { name: 'Export', exact: true }).click();
    await expect(page.getByRole('link', { name: 'CSV', exact: true })).toBeVisible();
    await noSideScroll(page, `export menu at ${width}`);
    await snap(page, `${DIR}/export-menu-${width}.png`);
    await page.keyboard.press('Escape');

    // The Import page after a validation with errors (a missing name, a value that is not an
    // option, a number that is not one).
    await openImport();
    await page.getByLabel('File', { exact: true }).setInputFiles({ name: 'customers.csv', mimeType: 'text/csv', buffer: Buffer.from(bad) });
    await page.getByRole('button', { name: 'Validate', exact: true }).click();
    await expect(page.getByRole('alert').filter({ hasText: 'rows have errors' })).toBeVisible();
    await noSideScroll(page, `import errors at ${width}`);
    await snap(page, `${DIR}/import-errors-${width}.png`);

    // ... and after the import of a file without errors. Each width imports its own customers.
    const stamp = `${width}`;
    await openImport();
    await page.getByLabel('File', { exact: true }).setInputFiles({
      name: 'customers.csv',
      mimeType: 'text/csv',
      buffer: Buffer.from(good.replaceAll(`${tag}-`, `${tag}-${stamp}-`)),
    });
    await page.getByRole('button', { name: 'Validate', exact: true }).click();
    await expect(page.getByText('No errors. 3 rows: 3 new.')).toBeVisible();
    await page.getByRole('button', { name: 'Import', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Import finished' })).toBeVisible();
    await noSideScroll(page, `import done at ${width}`);
    await snap(page, `${DIR}/import-done-${width}.png`);
  }
  await page.setViewportSize({ width: 320, height: 800 });
  await openImport();
  await page.getByLabel('File', { exact: true }).setInputFiles({ name: 'customers.csv', mimeType: 'text/csv', buffer: Buffer.from(bad) });
  await page.getByRole('button', { name: 'Validate', exact: true }).click();
  await expect(page.getByRole('alert').filter({ hasText: 'rows have errors' })).toBeVisible();
  await noSideScroll(page, 'import errors at 320');
});
