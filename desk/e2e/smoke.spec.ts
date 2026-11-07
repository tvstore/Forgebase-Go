// The smoke test of the desk (M4.2, extended in UX.2, UX.4 and UX.5): login -> home -> list -> search -> create
// (with a child row) -> save (toast) -> edit -> submit (badge) -> the More menu on the example
// `sales_order` DocType (submittable, naming series, a child table, a Link to `customer`), then a
// user without the submit right. Home, the list and the form (a draft and a submitted order) are
// also checked for horizontal scroll at 375 and 320 px, so that a runner with a wider fallback font
// than the developer's machine does not surprise us. It runs against a real server prepared by seed.sh and checks the
// M4 exit criteria that need a browser: no secret in browser storage, and an action the user has
// no right for is hidden (the API refusal itself is tested in Rust: forgebase-http/tests/resources.rs
// and crates/forgebase-db/tests/permissions.rs; one refused request is repeated here). UX.4 adds
// the command bar (Ctrl+K), the filter chips, link titles and reduced motion as small tests of
// their own, and the link title in the list and the form to the main flow. M8.2 adds the workflow
// on the e2e-only DocType `purchase_request` (seed.sh): a clerk sends a request for approval, a
// manager approves it; the state badge, the actions, the confirmation, the history and the state
// filter. Sales orders keep their plain Submit. M8.3 adds the saved views (private, shared by an
// Administrator, updated, deleted, a stale one), the Columns menu and the group-by summary panel. M8.4 adds a
// query report (the app `e2e` of the example binary e2e_app, seed.sh): a manager opens it from the sidebar,
// runs it, sorts the result and reloads; a clerk does not see the Reports section.

import { expect, test } from '@playwright/test';
import type { Locator, Page } from '@playwright/test';

const PASSWORD = process.env.E2E_PASSWORD ?? 'e2e-test-password';
const CUSTOMER = 'ada@example.com';
// The seeded customer has a `title_field` (its full name): a Link to it shows that title.
const CUSTOMER_TITLE = 'Ada Lovelace';

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

const PHONE_WIDTHS = [375, 320];

/**
 * The open page does not scroll sideways at the width of a phone, and of a small one (only a table
 * may, inside its own container). The viewport is put back afterwards.
 */
async function expectNoHorizontalScroll(page: Page, what: string): Promise<void> {
  const before = page.viewportSize() ?? { width: 1280, height: 800 };
  for (const width of PHONE_WIDTHS) {
    await page.setViewportSize({ width, height: 800 });
    const widths = await page.evaluate(() => ({
      scroll: document.documentElement.scrollWidth,
      client: document.documentElement.clientWidth,
    }));
    expect(widths.scroll, `${what} scrolls sideways at ${width} px`).toBeLessThanOrEqual(widths.client);
  }
  await page.setViewportSize(before);
}

/** Waits until the list has loaded (nothing in the page is busy any more). */
async function expectLoaded(page: Page): Promise<void> {
  await expect(page.locator('[aria-busy="true"]')).toHaveCount(0);
}

/** The text of the document status badge of the open form. */
const statusBadge = (page: Page) => page.locator('.status-badge');
const toast = (page: Page, text: string) => page.locator('.toast', { hasText: text });

/** The only secret the desk may leave in the browser is the HttpOnly cookie, which scripts cannot see. */
async function expectNoSecretsInStorage(page: Page): Promise<void> {
  const stored = await page.evaluate(() => ({
    local: window.localStorage.length,
    session: window.sessionStorage.length,
    cookie: document.cookie,
  }));
  expect(stored).toEqual({ local: 0, session: 0, cookie: '' });
  const cookies = await page.context().cookies();
  const session = cookies.find((c) => c.name === 'fb_session');
  expect(session?.httpOnly).toBe(true);
}

/** From the sales order list: opens a new sales order and fills it with one row; leaves it unsaved. */
async function fillNewOrder(page: Page): Promise<void> {
  await page.getByRole('main').getByRole('link', { name: 'New', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'New Sales Order', exact: true })).toBeFocused();

  // The Link field suggests the names of the target DocType as you type.
  await page.getByLabel('Customer').fill('ada');
  await expect(page.locator(`datalist option[value="${CUSTOMER}"]`)).toHaveCount(1);
  await page.getByLabel('Customer').fill(CUSTOMER);
  await page.getByLabel('Order Date').fill('2026-10-29');

  await page.getByRole('button', { name: 'Add row' }).click();
  await expect(page.getByLabel('Description, row 1')).toBeFocused();
  await page.getByLabel('Description, row 1').fill('Widget');
  await page.getByLabel('Quantity, row 1').fill('2');
  await page.getByLabel('Rate, row 1').fill('9.50');
}

test('login, list, create, edit and submit a sales order; a clerk cannot submit', async ({ page }) => {
  // ---- login, home and list -------------------------------------------------------------
  await login(page, 'e2e_manager');
  await expectNoSecretsInStorage(page);
  await expect(page.getByRole('navigation', { name: 'Document types' })).toBeVisible(); // the sidebar
  await expect(page.getByRole('main').getByRole('link', { name: 'Sales Order', exact: true })).toBeVisible();
  await expectNoHorizontalScroll(page, 'home');
  await page.getByRole('main').getByRole('link', { name: 'Sales Order', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Sales Order', level: 1 })).toBeVisible();
  await expectLoaded(page); // the list may hold orders of an earlier run

  // ---- search: kept in the address, survives a reload, and clears -----------------------
  const search = page.getByRole('searchbox');
  await search.fill('no-such-order-zzz');
  await expect(page).toHaveURL(/[?&]q=no-such-order-zzz/);
  await expect(page.getByRole('heading', { name: 'No documents match' })).toBeVisible();
  await page.reload();
  await expect(search).toHaveValue('no-such-order-zzz');
  await expect(page.getByRole('heading', { name: 'No documents match' })).toBeVisible();
  await page.getByRole('button', { name: 'Clear search and filters' }).click();
  await expect(search).toHaveValue('');
  await expect(page.getByRole('heading', { name: 'No documents match' })).toHaveCount(0);
  await expectLoaded(page);
  await expectNoHorizontalScroll(page, 'the list');

  // ---- create ---------------------------------------------------------------------------
  await fillNewOrder(page);

  // A row with a non-integer quantity is refused on its cell, and nothing is created.
  await page.getByLabel('Quantity, row 1').fill('1.5');
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page.getByRole('alert')).toContainText('not saved');
  await expect(page.getByLabel('Quantity, row 1')).toBeFocused();
  await expect(page.getByLabel('Quantity, row 1')).toHaveAttribute('aria-invalid', 'true');
  await expect(page.getByText('Enter a whole number.')).toBeVisible();
  await page.getByLabel('Quantity, row 1').fill('2');

  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page).toHaveURL(/#\/form\/sales_order\/SO-\d{4}-\d{5}$/);
  const name = decodeURIComponent(page.url().split('/').pop() ?? '');
  await expect(toast(page, 'Saved')).toBeVisible(); // success feedback
  // The heading is the title field (the customer, shown by its title), with the document's name
  // muted under it.
  await expect(page.getByRole('heading', { name: CUSTOMER_TITLE, level: 1 })).toBeFocused();
  await expect(page.locator('.doc-title-name')).toHaveText(name);
  await expect(statusBadge(page)).toHaveText('Draft');
  await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toContainText(name);
  await expect(page.getByLabel('Customer')).toHaveValue(CUSTOMER); // the control holds the key
  await expect(page.getByLabel('Rate, row 1')).toHaveValue('9.50');
  await expect(page.getByRole('button', { name: 'Submit' })).toBeVisible();
  await expect(page.getByText('* required')).toBeVisible(); // a draft has fields to fill in
  // Delete is in the More menu (a draft may be deleted by a manager); Escape closes the menu.
  await page.getByRole('button', { name: 'More actions' }).click();
  await expect(page.getByRole('button', { name: 'Delete' })).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.getByRole('button', { name: 'Delete' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'More actions' })).toBeFocused();
  await expectNoHorizontalScroll(page, 'the draft form');

  // ---- edit: only the changed field is sent ---------------------------------------------
  await page.getByLabel('Remarks').fill('first remark');
  const patch = page.waitForRequest((r) => r.method() === 'PATCH');
  await page.getByRole('button', { name: 'Save' }).click();
  expect(JSON.parse((await patch).postData() ?? '{}')).toEqual({ version: 1, data: { remarks: 'first remark' } });
  await expect(toast(page, 'Saved').last()).toBeVisible();
  await expect(page.locator('dd', { hasText: /^2$/ })).toBeVisible();

  // Unsaved changes are not lost by navigating away: the page asks first.
  await page.getByLabel('Remarks').fill('unsaved remark');
  await page.getByRole('link', { name: 'Home' }).click();
  await expect(page.getByRole('dialog', { name: 'You have unsaved changes.' })).toBeVisible();
  await page.getByRole('button', { name: 'Stay' }).click();
  await expect(page).toHaveURL(/#\/form\/sales_order\//);
  await expect(page.getByLabel('Remarks')).toHaveValue('unsaved remark');
  await page.getByLabel('Remarks').fill('first remark');

  // ---- Ctrl+S saves ---------------------------------------------------------------------
  await page.getByLabel('Remarks').fill('saved with the keyboard');
  const shortcut = page.waitForRequest((r) => r.method() === 'PATCH');
  await page.keyboard.press('Control+s');
  expect(JSON.parse((await shortcut).postData() ?? '{}').data).toEqual({ remarks: 'saved with the keyboard' });
  await expect(page.locator('dd', { hasText: /^3$/ })).toBeVisible();

  // ---- submit ---------------------------------------------------------------------------
  await page.getByRole('button', { name: 'Submit' }).click();
  await expect(toast(page, 'Submitted')).toBeVisible();
  await expect(statusBadge(page)).toHaveText('Submitted');
  await expect(page.getByRole('button', { name: 'Submit' })).toHaveCount(0);
  // Cancel is in the More menu and asks first; Escape answers no and the focus returns.
  await page.getByRole('button', { name: 'More actions' }).click();
  await expect(page.getByRole('button', { name: 'Cancel', exact: true })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Delete' })).toHaveCount(0); // a submitted document cannot be deleted
  await page.getByRole('button', { name: 'Cancel', exact: true }).click();
  await expect(page.getByRole('dialog', { name: `Cancel ${name}?` })).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'More actions' })).toBeFocused();
  await expect(statusBadge(page)).toHaveText('Submitted');
  // A submitted order edits only its allow_on_submit field; its table is read-only.
  await expect(page.getByLabel('Remarks')).toBeEditable();
  // A locked value is plain text, not an input: the label is its term and the value, the link's
  // title with its key muted after it, is its definition.
  await expect(page.getByLabel('Customer')).toHaveCount(0);
  await expect(page.getByRole('term').filter({ hasText: 'Customer' })).toHaveText('Customer');
  const customerValue = page.getByRole('definition').filter({ hasText: CUSTOMER_TITLE });
  await expect(customerValue).toContainText(CUSTOMER);
  await expect(page.getByRole('button', { name: 'Add row' })).toHaveCount(0);
  await expect(page.getByText('* required')).toHaveCount(0); // every required field is a value now
  await expectNoSecretsInStorage(page);

  // The order is in the list, with its status.
  await page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: 'Sales Order' }).click();
  // The primary text is the title field (the customer's title), with the order's name muted beside it.
  await expect(page.getByRole('row', { name: new RegExp(`${CUSTOMER_TITLE}.*${name}.*Submitted`) })).toBeVisible();
  await page.getByRole('row', { name: new RegExp(name) }).getByRole('cell').first().click({ position: { x: 4, y: 4 } }); // the whole row opens it
  await expect(page).toHaveURL(new RegExp(`#/form/sales_order/${name}$`));

  // ---- a phone: no horizontal scroll on the list and the form ---------------------------
  await expectNoHorizontalScroll(page, 'the submitted form');
  await page.goto('/desk/#/list/sales_order');
  await expect(page.getByRole('row', { name: new RegExp(name) })).toBeVisible();
  await expectNoHorizontalScroll(page, 'the list with orders');
  // On a phone the status badge is not off-screen in the scrolling table: it sits under the title.
  await page.setViewportSize({ width: 375, height: 800 });
  const inline = page.getByRole('row', { name: new RegExp(name) }).locator('.status-inline');
  await expect(inline).toBeVisible();
  const box = await inline.boundingBox();
  expect((box?.x ?? 0) + (box?.width ?? 0)).toBeLessThanOrEqual(375);

  // The menu drawer: the focus goes to its first link, Escape closes it and returns to the Menu
  // button, and a click outside (on the backdrop) closes it too.
  const menu = page.getByRole('button', { name: 'Menu' });
  const drawer = page.getByRole('navigation', { name: 'Document types' });
  await menu.click();
  await expect(menu).toHaveAttribute('aria-expanded', 'true');
  await expect(drawer.getByRole('link', { name: 'Home' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(menu).toHaveAttribute('aria-expanded', 'false');
  await expect(menu).toBeFocused();
  await menu.click();
  await expect(drawer).toBeVisible();
  await page.mouse.click(360, 500);
  await expect(drawer).toBeHidden();
  await expect(menu).toHaveAttribute('aria-expanded', 'false');

  await page.setViewportSize({ width: 1280, height: 800 });
  await page.goto(`/desk/#/form/sales_order/${encodeURIComponent(name)}`);
  await expect(statusBadge(page)).toHaveText('Submitted');
  await logout(page);

  // ---- a user without the submit right --------------------------------------------------
  await login(page, 'e2e_clerk');
  await page.getByRole('main').getByRole('link', { name: 'Sales Order', exact: true }).click();
  await page.getByRole('row', { name: new RegExp(name) }).getByRole('link').click();
  await expect(statusBadge(page)).toHaveText('Submitted');
  await expect(page.getByRole('button', { name: 'More actions' })).toHaveCount(0); // no cancel, amend or delete right
  await expect(page.getByRole('button', { name: 'Cancel', exact: true })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Amend' })).toHaveCount(0);

  await page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: 'Sales Order' }).click();
  await fillNewOrder(page);
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page).toHaveURL(/#\/form\/sales_order\/SO-\d{4}-\d{5}$/);
  const draft = decodeURIComponent(page.url().split('/').pop() ?? '');
  await expect(statusBadge(page)).toHaveText('Draft');
  await expect(page.getByRole('button', { name: 'Save' })).toBeVisible(); // the clerk can work on the draft
  await expect(page.getByRole('button', { name: 'Submit' })).toHaveCount(0); // but not submit it
  await expect(page.getByRole('button', { name: 'More actions' })).toHaveCount(0); // and not delete it

  // The API refuses what the desk hides (the Rust tests cover every right; this is one request).
  const status = await page.evaluate(async (documentName) => {
    const session = (await (await fetch('/api/v1/session', { headers: { 'X-Forgebase-CSRF': 'refresh' } })).json()) as {
      data: { csrf_token: string };
    };
    const response = await fetch(`/api/v1/resource/sales_order/${encodeURIComponent(documentName)}/submit`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Forgebase-CSRF': session.data.csrf_token },
      body: JSON.stringify({ version: 1 }),
    });
    return response.status;
  }, draft);
  expect(status).toBe(403);
  await expectNoSecretsInStorage(page);
});

// ---- UX.4: the command bar --------------------------------------------------------------

test('the command bar opens with Ctrl+K, jumps to a list and a new form, and gives the focus back', async ({ page }) => {
  await login(page, 'e2e_manager');
  const bar = page.getByRole('dialog', { name: 'Search or jump to' });

  // Ctrl+K opens it with the focus in its input; typing a DocType's label and Enter opens its list.
  await page.keyboard.press('Control+k');
  await expect(bar).toBeVisible();
  const input = bar.getByRole('combobox');
  await expect(input).toBeFocused();
  await input.fill('sales ord');
  await expect(bar.getByRole('option', { name: 'Go to Sales Order', exact: true })).toBeVisible();
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/#\/list\/sales_order$/);
  await expect(bar).toHaveCount(0);
  await expect(page.getByRole('heading', { name: 'Sales Order', level: 1 })).toBeVisible();

  // The arrow keys move the active option (aria-activedescendant); Enter opens "New".
  await page.keyboard.press('Control+k');
  await input.fill('sales order');
  const goTo = bar.getByRole('option', { name: 'Go to Sales Order', exact: true });
  const create = bar.getByRole('option', { name: 'New Sales Order', exact: true });
  await expect(goTo).toHaveAttribute('aria-selected', 'true');
  await page.keyboard.press('ArrowDown');
  await expect(goTo).toHaveAttribute('aria-selected', 'false');
  // ArrowDown walks the list down to "New".
  for (let i = 0; i < 10 && (await create.getAttribute('aria-selected')) !== 'true'; i++) {
    await page.keyboard.press('ArrowDown');
  }
  await expect(create).toHaveAttribute('aria-selected', 'true');
  await expect(input).toHaveAttribute('aria-activedescendant', (await create.getAttribute('id')) ?? '');
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/#\/new\/sales_order$/);

  // Nothing matches: it says so. Escape closes it and the focus returns to the search button.
  const button = page.getByRole('button', { name: /^Search or jump to/ });
  await button.click();
  await expect(bar).toBeVisible();
  await input.fill('zzz-nothing');
  await expect(bar.getByText('No matches.')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(bar).toHaveCount(0);
  await expect(button).toBeFocused();
  await expectNoSecretsInStorage(page); // the recent documents are in memory only
});

test('the recent documents of one user are not shown to the next user of the tab', async ({ page }) => {
  await login(page, 'e2e_manager');
  await page.getByRole('main').getByRole('link', { name: 'Sales Order', exact: true }).click();
  await fillNewOrder(page);
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page).toHaveURL(/#\/form\/sales_order\/SO-\d{4}-\d{5}$/);
  const name = decodeURIComponent(page.url().split('/').pop() ?? '');

  await expect(toast(page, 'Saved')).toBeVisible();
  // The saved order is listed as recent once its form has loaded: wait for that, not for the toast.
  await expect(page.locator('.doc-title-name')).toHaveText(name);
  const bar = page.getByRole('dialog', { name: 'Search or jump to' });
  await page.keyboard.press('Control+k');
  await expect(bar).toBeVisible();
  await expect(bar.getByRole('group', { name: 'Recent' })).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(bar).toHaveCount(0);

  // Logged out and in again as someone else, in the same tab: nothing of the first user is left.
  await logout(page);
  // Log in again WITHOUT navigating: a page load would wipe the in-memory list by itself and the
  // test would prove nothing. The marker shows that it is still the same document.
  await page.evaluate(() => {
    (window as unknown as { __sameDocument?: boolean }).__sameDocument = true;
  });
  await page.getByLabel('Username').fill('e2e_clerk');
  await page.getByLabel('Password').fill(PASSWORD);
  await page.getByRole('button', { name: 'Log in' }).click();
  await expect(page.getByRole('heading', { name: 'Home', level: 1 })).toBeVisible();
  expect(await page.evaluate(() => (window as unknown as { __sameDocument?: boolean }).__sameDocument)).toBe(true);
  await page.keyboard.press('Control+k');
  await expect(bar).toBeVisible();
  // Wait for /api/v1/meta: without DocTypes the Recent group is filtered out anyway.
  await expect(bar.getByRole('option', { name: 'Go to Sales Order', exact: true })).toBeVisible();
  await expect(bar.getByRole('group', { name: 'Recent' })).toHaveCount(0);
  await expect(bar.getByText(name)).toHaveCount(0);
});

test('a clerk without create rights is not offered "New" in the command bar', async ({ page }) => {
  await login(page, 'e2e_clerk');
  await page.keyboard.press('Control+k');
  const bar = page.getByRole('dialog', { name: 'Search or jump to' });
  await bar.getByRole('combobox').fill('customer');
  await expect(bar.getByRole('option', { name: 'Go to Customer', exact: true })).toBeVisible();
  await expect(bar.getByRole('option', { name: 'New Customer', exact: true })).toHaveCount(0); // the clerk may only read customers
});

// ---- UX.4: filter chips and link titles -------------------------------------------------

test('a filter shows as a chip that can be removed; a Link shows its title', async ({ page }) => {
  await login(page, 'e2e_manager');
  await page.goto('/desk/#/list/sales_order');
  await expectLoaded(page);

  // The seeded order's customer link shows its title, not the key.
  const orders = page.getByRole('row').filter({ hasText: CUSTOMER_TITLE });
  await expect(orders.first()).toBeVisible();
  await expect(page.getByRole('row').filter({ hasText: CUSTOMER })).toHaveCount(0);

  await page.getByRole('button', { name: 'Filter', exact: true }).click();
  await page.getByRole('button', { name: 'Add filter' }).click();
  await page.getByLabel('Filter 1 field').selectOption({ label: 'Customer' });
  await page.getByLabel('Filter 1 value').fill(CUSTOMER);
  // Applied a moment after the last change: the chip appears and the address holds the filter.
  const chip = page.getByRole('button', { name: `Remove filter: Customer is ${CUSTOMER}` });
  await expect(chip).toBeVisible();
  await expect(page).toHaveURL(/[?&]filters=/);
  await expect(orders.first()).toBeVisible(); // the orders of that customer are still listed

  await chip.click();
  await expect(chip).toHaveCount(0);
  await expect(page).not.toHaveURL(/filters=/);
  await expect(page.getByLabel('Filter 1 field')).toHaveCount(0); // the removed row left the filter controls
  await expectLoaded(page);
});

// ---- UX.4: reduced motion ---------------------------------------------------------------

test('prefers-reduced-motion switches the motion of a dialog off', async ({ page }) => {
  await login(page, 'e2e_manager');
  await page.goto('/desk/#/new/sales_order');
  await page.getByLabel('Remarks').fill('unsaved');

  const durations = async () => {
    await page.getByRole('link', { name: 'Home' }).click(); // asks about the unsaved changes
    const dialog = page.getByRole('dialog', { name: 'You have unsaved changes.' });
    await expect(dialog).toBeVisible();
    const result = await dialog.evaluate((el) => {
      const style = getComputedStyle(el);
      return {
        animation: style.animationDuration,
        transition: style.transitionDuration,
        backdrop: getComputedStyle(el, '::backdrop').animationDuration,
      };
    });
    await page.getByRole('button', { name: 'Stay' }).click();
    await expect(dialog).toHaveCount(0);
    return result;
  };

  // With motion, the dialog fades and scales in...
  await page.emulateMedia({ reducedMotion: 'no-preference' });
  const moving = await durations();
  expect(moving.animation).not.toBe('0s');
  expect(moving.backdrop).not.toBe('0s');
  // ...and with reduced motion, every animation and transition has no duration.
  await page.emulateMedia({ reducedMotion: 'reduce' });
  expect(await durations()).toEqual({ animation: '0s', transition: '0s', backdrop: '0s' });
});

// ---- M8.2: the workflow -----------------------------------------------------------------

/** The entries of the Activity card, newest first. */
const history = (page: Page) => page.getByRole('list', { name: 'Workflow history' }).getByRole('listitem');

/** From the Purchase Request list: a new request with a title and an amount, saved. Returns its name. */
async function createRequest(page: Page, title: string): Promise<string> {
  await page.goto('/desk/#/new/purchase_request');
  await expect(page.getByRole('heading', { name: 'New Purchase Request', exact: true })).toBeFocused();
  await page.getByLabel('Title').fill(title);
  await page.getByLabel('Amount').fill('120.50');
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(page).toHaveURL(/#\/form\/purchase_request\/PR-\d{4}-\d{5}$/);
  await expect(toast(page, 'Saved')).toBeVisible();
  return decodeURIComponent(page.url().split('/').pop() ?? '');
}

test('a clerk sends a purchase request for approval and a manager approves it', async ({ page }) => {
  // ---- the clerk -------------------------------------------------------------------------
  await login(page, 'e2e_clerk');
  const name = await createRequest(page, 'Office chairs');
  await expect(statusBadge(page)).toHaveText('Draft');
  const sendButton = page.getByRole('button', { name: 'Submit for Approval' });
  await expect(sendButton).toBeVisible();
  // The plain Submit and Cancel are replaced by the workflow's actions; the clerk has no Approve.
  await expect(page.getByRole('button', { name: 'Submit', exact: true })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Cancel', exact: true })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Approve' })).toHaveCount(0);
  await expect(page.getByRole('heading', { name: 'Activity', level: 2 })).toBeVisible();
  await expect(page.getByText('No transitions yet.')).toBeVisible();
  await expectNoHorizontalScroll(page, 'the workflow form');

  // Unsaved changes: the action is disabled (Save comes first), and clicking it says why.
  await page.getByLabel('Amount').fill('130.00');
  await expect(sendButton).toBeDisabled();
  await expect(sendButton).toHaveAttribute('title', 'Save first');
  await expect(page.getByRole('button', { name: 'Save' })).toHaveClass(/btn-primary/);
  await sendButton.click({ force: true });
  await expect(page.getByRole('alert')).toContainText('Save them first');
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(toast(page, 'Saved').last()).toBeVisible();
  await expect(sendButton).toBeEnabled();
  await expect(sendButton).toHaveClass(/btn-primary/);

  // The confirmation names the move; Back (and Escape) answers no and nothing changes.
  await sendButton.click();
  const dialog = page.getByRole('dialog', { name: 'Submit for Approval' });
  await expect(dialog).toContainText(`Move Office chairs from Draft to Pending Approval?`);
  await expect(dialog.getByRole('button', { name: 'Back' })).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);
  await expect(sendButton).toBeFocused();
  await expect(statusBadge(page)).toHaveText('Draft');
  await sendButton.click();
  await dialog.getByRole('button', { name: 'Back' }).click();
  await expect(dialog).toHaveCount(0);

  // Confirm: the version is sent with the action. The new actions are delayed, so the old ones
  // (fetched for the old version) are visible for a while: they must be disabled, not usable.
  await page.route('**/transitions', async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 2000));
    await route.continue();
  });
  await sendButton.click();
  const request = page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith('/transition'));
  await dialog.getByRole('button', { name: 'Confirm' }).click();
  expect(JSON.parse((await request).postData() ?? '{}')).toEqual({ action: 'Submit for Approval', version: 2 });
  await expect(toast(page, 'Submit for Approval: Pending Approval')).toBeVisible();
  await expect(statusBadge(page)).toHaveText('Pending Approval');
  await expect(sendButton).toBeDisabled(); // still the old action: the new list is in flight
  await sendButton.click({ force: true });
  await expect(page.getByRole('dialog')).toHaveCount(0);
  await expect(sendButton).toHaveCount(0);
  await page.unroute('**/transitions');
  await expect(page.getByRole('button', { name: 'Approve' })).toHaveCount(0); // not the clerk's action
  await expect(history(page)).toHaveCount(1);
  await expect(history(page).first()).toContainText('e2e_clerk Submit for Approval');
  await expect(history(page).first()).toContainText('Draft → Pending Approval');
  await expect(history(page).first().locator('time')).toHaveAttribute('datetime', /^\d{4}-/);
  await expectNoSecretsInStorage(page);
  await logout(page);

  // ---- the manager -----------------------------------------------------------------------
  await login(page, 'e2e_manager');
  await page.goto(`/desk/#/form/purchase_request/${encodeURIComponent(name)}`);
  await expect(statusBadge(page)).toHaveText('Pending Approval');
  const approve = page.getByRole('button', { name: 'Approve' });
  await expect(approve).toHaveClass(/btn-primary/); // the first action is the primary one
  await expect(page.getByRole('button', { name: 'Reject' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Submit', exact: true })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Cancel', exact: true })).toHaveCount(0);
  await expect(history(page)).toHaveCount(1);
  await expectNoHorizontalScroll(page, 'the pending form');

  await approve.click();
  await expect(page.getByRole('dialog', { name: 'Approve' })).toContainText(
    'Move Office chairs from Pending Approval to Approved?',
  );
  await page.getByRole('button', { name: 'Confirm' }).click();
  await expect(toast(page, 'Approve: Approved')).toBeVisible();
  await expect(statusBadge(page)).toHaveText('Approved');
  await expect(history(page)).toHaveCount(2);
  await expect(history(page).first()).toContainText('e2e_manager Approve'); // newest first
  await expect(history(page).last()).toContainText('e2e_clerk Submit for Approval');
  await expect(page.getByRole('button', { name: 'Approve' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Cancel Approval' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Cancel', exact: true })).toHaveCount(0); // the plain Cancel stays hidden
  await expect(page.getByRole('heading', { name: 'Office chairs', level: 1 })).toBeFocused();
  await expectNoHorizontalScroll(page, 'the approved form');

  // ---- the list: the state is the badge, and it filters ------------------------------------
  await page.goto('/desk/#/list/purchase_request');
  await expectLoaded(page);
  await expect(page.getByRole('columnheader', { name: 'Status' })).toBeVisible();
  const row = page.getByRole('row', { name: new RegExp(`Office chairs.*${name}.*Approved`) });
  await expect(row).toBeVisible();
  await page.getByRole('button', { name: 'Filter', exact: true }).click();
  await page.getByRole('button', { name: 'Add filter' }).click();
  await page.getByLabel('Filter 1 field').selectOption({ label: 'Status' });
  await page.getByLabel('Filter 1 value').selectOption('Draft');
  await expect(page.getByRole('button', { name: 'Remove filter: Status is Draft' })).toBeVisible();
  await expect(row).toHaveCount(0);
  await page.getByLabel('Filter 1 value').selectOption('Approved');
  await expect(page.getByRole('button', { name: 'Remove filter: Status is Approved' })).toBeVisible();
  await expect(row).toBeVisible();
  await expectNoHorizontalScroll(page, 'the workflow list');
});

test('a workflow action on a stale version shows the conflict and Reload brings the actions back', async ({ page }) => {
  await login(page, 'e2e_manager');
  const name = await createRequest(page, 'Stale request');
  // Somebody else changes the document after it was opened.
  const status = await page.evaluate(async (documentName) => {
    const session = (await (await fetch('/api/v1/session', { headers: { 'X-Forgebase-CSRF': 'refresh' } })).json()) as {
      data: { csrf_token: string };
    };
    const response = await fetch(`/api/v1/resource/purchase_request/${encodeURIComponent(documentName)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json', 'X-Forgebase-CSRF': session.data.csrf_token },
      body: JSON.stringify({ version: 1, data: { amount: '1.00' } }),
    });
    return response.status;
  }, name);
  expect(status).toBe(200);

  await page.getByRole('button', { name: 'Submit for Approval' }).click();
  await page.getByRole('button', { name: 'Confirm' }).click();
  await expect(page.getByRole('alert')).toContainText('Someone else changed this document');
  await expect(statusBadge(page)).toHaveText('Draft');
  await page.getByRole('button', { name: 'Reload' }).click();
  await expect(toast(page, 'Loaded the current version')).toBeVisible();
  await expect(page.getByLabel('Amount')).toHaveValue('1.00');
  await page.getByRole('button', { name: 'Submit for Approval' }).click();
  await page.getByRole('button', { name: 'Confirm' }).click();
  await expect(statusBadge(page)).toHaveText('Pending Approval');
});

// ---- M8.3: saved views, columns and the summary ------------------------------------------

/** Adds the filter "Status is <value>" of a purchase request list through the filter bar. */
async function filterByStatus(page: Page, value: string): Promise<void> {
  await page.getByRole('button', { name: 'Filter', exact: true }).click();
  await page.getByRole('button', { name: 'Add filter' }).click();
  await page.getByLabel('Filter 1 field').selectOption({ label: 'Status' });
  await page.getByLabel('Filter 1 value').selectOption(value);
  await expect(page.getByRole('button', { name: `Remove filter: Status is ${value}` })).toBeVisible();
}

const viewsButton = (page: Page) => page.getByRole('button', { name: 'Views', exact: true });
const viewLabel = (page: Page) => page.locator('.view-label');

/** Opens "Save view", names the view and saves it. */
async function saveView(page: Page, label: string, shared = false): Promise<void> {
  await viewsButton(page).click();
  await page.getByRole('button', { name: /^Save view/ }).click();
  const dialog = page.getByRole('dialog', { name: 'Save view' });
  await expect(dialog.getByLabel('Name')).toBeFocused();
  await dialog.getByLabel('Name').fill(label);
  if (shared) await dialog.getByLabel('Shared with everyone').check();
  await dialog.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(toast(page, 'View saved')).toBeVisible();
  await expect(viewLabel(page)).toHaveText(`\u00B7 ${label}`);
}

test('a private view survives a reload and is invisible to others; it can be updated and deleted', async ({ page }) => {
  const label = `Drafts ${Date.now()}`;
  await login(page, 'e2e_manager');
  await createRequest(page, 'For the Drafts view'); // a draft, so that the filtered list has rows to sort
  await page.goto('/desk/#/list/purchase_request');
  await expectLoaded(page);

  // The menu opens with the keyboard and Escape gives the focus back to its button.
  await viewsButton(page).focus();
  await page.keyboard.press('Enter');
  await expect(viewsButton(page)).toHaveAttribute('aria-expanded', 'true');
  await expect(page.getByText('No saved views yet.')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(viewsButton(page)).toHaveAttribute('aria-expanded', 'false');
  await expect(viewsButton(page)).toBeFocused();

  await filterByStatus(page, 'Draft');
  await saveView(page, label);
  await expect(page).toHaveURL(/[?&]view=[0-9a-f-]{36}/);
  await expect(page.getByRole('button', { name: 'Remove filter: Status is Draft' })).toBeVisible();

  // The same name again is refused, in the dialog.
  await viewsButton(page).click();
  await page.getByRole('button', { name: /^Save view/ }).click();
  const dialog = page.getByRole('dialog', { name: 'Save view' });
  await expect(dialog.getByLabel('Shared with everyone')).toHaveCount(0); // only an Administrator may share
  await dialog.getByLabel('Name').fill(label);
  await dialog.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(dialog.getByRole('alert')).toContainText('already exists');
  await dialog.getByRole('button', { name: 'Cancel' }).click();
  await expect(dialog).toHaveCount(0);

  // A reload keeps the view, its filter and the title next to the heading.
  await page.reload();
  await expect(viewLabel(page)).toHaveText(`\u00B7 ${label}`);
  await expect(page.getByRole('button', { name: 'Remove filter: Status is Draft' })).toBeVisible();
  await viewsButton(page).click();
  await expect(page.getByRole('group', { name: 'My views' }).getByRole('button', { name: label })).toHaveAttribute(
    'aria-current',
    'true',
  );
  await page.keyboard.press('Escape');

  // Nothing to update until something changes; then "Update" saves the new sort into the view.
  await viewsButton(page).click();
  await expect(page.getByRole('button', { name: `Update \u2018${label}\u2019` })).toHaveAttribute('aria-disabled', 'true');
  await page.keyboard.press('Escape');
  await page.getByRole('button', { name: 'Amount', exact: true }).click(); // sort by Amount
  await expect(page).toHaveURL(/[?&]sort=amount/);
  await viewsButton(page).click();
  await page.getByRole('button', { name: `Update \u2018${label}\u2019` }).click();
  await expect(toast(page, 'View updated')).toBeVisible();
  await viewsButton(page).click();
  await expect(page.getByRole('button', { name: `Update \u2018${label}\u2019` })).toHaveAttribute('aria-disabled', 'true');
  await page.keyboard.press('Escape');

  // The default view clears the view, its filter and the title.
  await viewsButton(page).click();
  await page.getByRole('button', { name: 'Default view' }).click();
  await expect(viewLabel(page)).toHaveCount(0);
  await expect(page).not.toHaveURL(/view=|filters=/);
  // Choosing it applies it again: its filter and its sort.
  await viewsButton(page).click();
  await page.getByRole('button', { name: label }).click();
  await expect(page).toHaveURL(/[?&]view=/);
  await expect(page).toHaveURL(/[?&]sort=amount/);
  await expect(page.getByRole('button', { name: 'Remove filter: Status is Draft' })).toBeVisible();
  await expectNoHorizontalScroll(page, 'the list with a view');

  // The clerk does not see the manager's private view.
  await logout(page);
  await login(page, 'e2e_clerk');
  await page.goto('/desk/#/list/purchase_request');
  await expectLoaded(page);
  await viewsButton(page).click();
  await expect(page.getByRole('button', { name: label })).toHaveCount(0);
  await expect(page.getByRole('group', { name: 'My views' })).toHaveCount(0);
  await page.keyboard.press('Escape');
  await logout(page);

  // Delete it (with a confirmation).
  await login(page, 'e2e_manager');
  await page.goto('/desk/#/list/purchase_request');
  await viewsButton(page).click();
  await page.getByRole('button', { name: label }).click();
  await expect(viewLabel(page)).toHaveText(`\u00B7 ${label}`);
  await viewsButton(page).click();
  await page.getByRole('button', { name: `Delete \u2018${label}\u2019` }).click();
  const confirm = page.getByRole('dialog', { name: `Delete the view \u2018${label}\u2019?` });
  await expect(confirm.getByRole('button', { name: 'Cancel' })).toBeFocused();
  await confirm.getByRole('button', { name: 'Delete view' }).click();
  await expect(toast(page, 'View deleted')).toBeVisible();
  await expect(viewLabel(page)).toHaveCount(0);
  await viewsButton(page).click();
  await expect(page.getByRole('button', { name: label })).toHaveCount(0);
});

test('an administrator shares a view and a clerk sees it but cannot change it', async ({ page }) => {
  const label = `Waiting ${Date.now()}`;
  await login(page, 'e2e_admin');
  await page.goto('/desk/#/list/purchase_request');
  await expectLoaded(page);
  await filterByStatus(page, 'Pending Approval');
  await saveView(page, label, true);
  await viewsButton(page).click();
  await expect(page.getByRole('group', { name: 'Shared' }).getByRole('button', { name: label })).toBeVisible();
  await page.keyboard.press('Escape');
  await logout(page);

  await login(page, 'e2e_clerk');
  await page.goto('/desk/#/list/purchase_request');
  await expectLoaded(page);
  await viewsButton(page).click();
  await page.getByRole('group', { name: 'Shared' }).getByRole('button', { name: label }).click();
  await expect(viewLabel(page)).toHaveText(`\u00B7 ${label}`);
  await expect(page.getByRole('button', { name: 'Remove filter: Status is Pending Approval' })).toBeVisible();
  // No Update or Delete for a view the clerk may not change.
  await viewsButton(page).click();
  await expect(page.getByRole('button', { name: /^Update / })).toHaveCount(0);
  await expect(page.getByRole('button', { name: /^Delete / })).toHaveCount(0);
  await page.keyboard.press('Escape');
  await logout(page);

  // The administrator removes it again.
  await login(page, 'e2e_admin');
  await page.goto('/desk/#/list/purchase_request');
  await viewsButton(page).click();
  await page.getByRole('button', { name: label }).click();
  await viewsButton(page).click();
  await page.getByRole('button', { name: `Delete \u2018${label}\u2019` }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Delete view' }).click();
  await expect(toast(page, 'View deleted')).toBeVisible();
});

test('a view that names a field that no longer exists says so and offers to fix or delete it', async ({ page }) => {
  await login(page, 'e2e_manager');
  const stale = {
    id: '0190b3a2-5c1e-7f00-8a00-1234567890ab',
    label: 'Old view',
    shared: false,
    owner: 'e2e_manager',
    definition: { filters: [{ field: 'gone', op: 'is null' }], sort: [], columns: [] },
    version: 1,
    modified_at: '2026-10-30T10:00:00Z',
  };
  // The server answers for real (400 invalid_query for the unknown field); only the view list is made up.
  await page.route('**/api/v1/views/purchase_request', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [stale] }) }),
  );
  await page.goto(`/desk/#/list/purchase_request?view=${stale.id}`);
  const notice = page.getByRole('alert').filter({ hasText: 'This view refers to fields that no longer exist.' });
  await expect(notice).toBeVisible();
  await expect(notice.getByRole('button', { name: 'Update view' })).toHaveAttribute('aria-disabled', 'true');
  await expect(notice.getByRole('button', { name: 'Delete view' })).toBeVisible();
  // Clearing the filters brings the list back, and the view can then be updated.
  await notice.getByRole('button', { name: 'Clear filters' }).click();
  await expect(notice).toHaveCount(0);
  await expectLoaded(page);
  await expect(page.getByRole('columnheader', { name: 'Status' })).toBeVisible();
  await expect(viewLabel(page)).toHaveText('\u00B7 Old view');
});

test('the Columns menu hides a column, keeps the choice in the address and resets', async ({ page }) => {
  await login(page, 'e2e_manager');
  // The rows come late, as on a slow runner. `expectLoaded` alone is no ready signal: it is also
  // true before the list has rendered at all. Wait for the rows ("Showing ..." in the live region)
  // before the menu is opened, so that no reload of the list can run under the clicks.
  await page.route('**/api/v1/resource/sales_order?*', async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 1000));
    await route.continue();
  });
  await page.goto('/desk/#/list/sales_order');
  const columns = page.getByRole('button', { name: 'Columns', exact: true });
  await expect(page.getByRole('status').filter({ hasText: /^Showing / })).toBeVisible();
  await expectLoaded(page);
  await page.unroute('**/api/v1/resource/sales_order?*');
  const header = page.getByRole('columnheader', { name: 'Order Date' });
  await expect(header).toBeVisible();

  await columns.click();
  await page.getByRole('checkbox', { name: 'Order Date' }).uncheck();
  // The menu stays open to change several columns; the column is gone and the address says so.
  await expect(columns).toHaveAttribute('aria-expanded', 'true');
  await expect(header).toHaveCount(0);
  await expect(page).toHaveURL(/[?&]columns=/);
  await page.keyboard.press('Escape');
  await expect(columns).toBeFocused();

  await page.reload();
  await expectLoaded(page);
  await expect(page.getByRole('columnheader', { name: 'Order Date' })).toHaveCount(0);
  await expect(page.getByRole('columnheader', { name: 'Total' })).toBeVisible();
  await columns.click();
  await expect(page.getByRole('checkbox', { name: 'Order Date' })).not.toBeChecked();
  // A column the list does not show by default can be added.
  await page.getByRole('checkbox', { name: 'Remarks' }).count(); // Text fields are not offered
  await expect(page.getByRole('checkbox', { name: 'Remarks' })).toHaveCount(0);
  await page.getByRole('button', { name: 'Reset to default' }).click();
  await expect(page.getByRole('columnheader', { name: 'Order Date' })).toBeVisible();
  await expect(page).not.toHaveURL(/columns=/);
});

test('the Summary panel counts by status, follows the filters and a click on a group filters the list', async ({ page }) => {
  await login(page, 'e2e_manager');
  await createRequest(page, 'Summary one');
  await page.goto('/desk/#/list/purchase_request');
  await expectLoaded(page);

  const summary = page.getByRole('button', { name: 'Summary', exact: true });
  await summary.click();
  await expect(summary).toHaveAttribute('aria-expanded', 'true');
  const panel = page.getByRole('region', { name: 'Summary' });
  await expect(panel.getByLabel('Group by')).toHaveValue('workflow_state'); // the status of a workflow DocType
  const table = panel.getByRole('table', { name: 'Purchase Request by Status' });
  await expect(table).toBeVisible();
  await expect(table.getByRole('columnheader', { name: 'Count' })).toBeVisible();
  await expect(table.getByRole('columnheader', { name: 'Sum of Amount' })).toBeVisible();
  const draft = table.getByRole('row').filter({ has: page.getByRole('button', { name: 'Draft', exact: true }) });
  const count = Number((await draft.getByRole('cell').first().innerText()).replace(/,/g, ''));
  expect(count).toBeGreaterThanOrEqual(1);
  await expectNoHorizontalScroll(page, 'the summary panel');

  // A group by another field, and an average instead of a sum.
  await panel.getByLabel('Group by').selectOption({ label: 'Owner' });
  await expect(panel.getByRole('table', { name: 'Purchase Request by Owner' })).toBeVisible();
  await panel.getByLabel('Value 1: calculation').selectOption('avg');
  await expect(panel.getByRole('columnheader', { name: 'Average of Amount' })).toBeVisible();
  await panel.getByLabel('Group by').selectOption({ label: 'Status' });

  // The panel counts what the list shows: with a filter, the other groups are gone.
  await page.getByRole('button', { name: 'Filter', exact: true }).click();
  await page.getByRole('button', { name: 'Add filter' }).click();
  await page.getByLabel('Filter 1 field').selectOption({ label: 'Status' });
  await page.getByLabel('Filter 1 value').selectOption('Draft');
  await expect(page.getByRole('button', { name: 'Remove filter: Status is Draft' })).toBeVisible();
  await expect(table.getByRole('row')).toHaveCount(2); // the header and the Draft group
  await page.getByRole('button', { name: 'Remove filter: Status is Draft' }).click();
  await expect(table.getByRole('row').nth(2)).toBeVisible();

  // A click on a group closes the panel and filters the list to it.
  await table.getByRole('button', { name: 'Draft', exact: true }).click();
  await expect(panel).toHaveCount(0);
  await expect(summary).toBeFocused();
  await expect(page).toHaveURL(/[?&]filters=/);
  await expect(page.getByRole('button', { name: 'Remove filter: Status is Draft' })).toBeVisible();
  await expectLoaded(page);
  if (count <= 20) await expect(page.getByRole('status').filter({ hasText: `Showing 1\u2013${count}` })).toBeVisible();
  await expectNoHorizontalScroll(page, 'the list after a group was chosen');
});

/**
 * What a keyboard user sees on the focused element: an outline of at least 2 px in a visible colour
 * (not transparent, alpha of at least 0.5, and not white unless the control is on the navy top bar:
 * a white ring on a white page is no indicator), or a box shadow.
 *
 * `getComputedStyle` can return `oklab()`, `color-mix()` or another modern colour syntax, which
 * cannot be read as numbers. The colour is therefore painted on a 1x1 canvas and the pixel is read
 * back: the browser converts any colour it understands to sRGB. A colour the browser cannot parse
 * would leave the canvas colour unchanged, which the sentinel detects (the test then fails loudly
 * instead of passing on a wrong reading).
 */
async function focusIndicator(target: Locator): Promise<{ outline: boolean; shadow: boolean; text: string }> {
  return target.evaluate((el) => {
    const style = getComputedStyle(el);
    const canvas = document.createElement('canvas');
    canvas.width = 1;
    canvas.height = 1;
    const context = canvas.getContext('2d', { willReadFrequently: true });
    if (!context) throw new Error('no 2d canvas to normalise the outline colour');
    const pixel = (color: string): [number, number, number, number] => {
      context.clearRect(0, 0, 1, 1);
      context.fillStyle = '#010203'; // sentinel: an unparsable colour leaves it in place
      context.fillStyle = color;
      context.fillRect(0, 0, 1, 1);
      const data = context.getImageData(0, 0, 1, 1).data;
      const [r = 0, g = 0, b = 0, a = 0] = Array.from(data);
      if (r === 1 && g === 2 && b === 3 && a === 255) throw new Error(`cannot read the colour ${color}`);
      return [r, g, b, a / 255];
    };
    const [r, g, b, a] = pixel(style.outlineColor);
    const white = r >= 250 && g >= 250 && b >= 250;
    const onNavy = el.closest('.topbar') !== null;
    const visible = a >= 0.5 && (!white || onNavy);
    const outline = style.outlineStyle !== 'none' && parseFloat(style.outlineWidth) >= 2 && visible;
    return { outline, shadow: style.boxShadow !== 'none', text: el.tagName + (el.getAttribute('aria-label') ?? el.textContent ?? '').slice(0, 30) };
  });
}

test('the focus indicator check reads oklab() and color-mix() colours and catches a white ring', async ({ page }) => {
  const outline = async (color: string): Promise<boolean> => {
    await page.setContent(`<button id="b" style="outline: 3px solid ${color}; background: #fff">x</button>`);
    return (await focusIndicator(page.locator('#b'))).outline;
  };
  // White in the modern syntaxes is no indicator on a white page; a dark colour is one.
  expect(await outline('oklab(1 0 0)')).toBe(false);
  expect(await outline('color-mix(in srgb, white 100%, white)')).toBe(false);
  expect(await outline('color-mix(in oklab, white, white)')).toBe(false);
  expect(await outline('rgb(255 255 255)')).toBe(false);
  expect(await outline('oklab(0.3 0 0)')).toBe(true);
  expect(await outline('color-mix(in oklab, #0b2545 80%, #0b2545)')).toBe(true);
  expect(await outline('rgb(11 37 69)')).toBe(true);
  // Half transparent (alpha 0.4) is too faint.
  expect(await outline('oklab(0.3 0 0 / 0.4)')).toBe(false);
});

/**
 * Walks forward with the Tab key until each target (in page order) has the focus, and asserts that
 * it shows an outline of at least 2 px (UX.5: the fills of the fields have no 3:1 border, so the
 * keyboard focus must be clearly visible). `shadowOk` is for a control whose indicator is a shadow.
 */
async function expectKeyboardFocusVisible(page: Page, targets: Locator[], shadowOk = false): Promise<void> {
  for (const target of targets) {
    let found = false;
    for (let i = 0; i < 150 && !found; i++) {
      await page.keyboard.press('Tab');
      found = await target.evaluate((el) => el === document.activeElement);
    }
    expect(found, 'Tab reaches the control').toBe(true);
    await expect
      .poll(async () => {
        const seen = await focusIndicator(target);
        return seen.outline || (shadowOk && seen.shadow);
      }, { message: 'the focused control shows a focus indicator' })
      .toBe(true);
  }
}

test('keyboard focus is visible on the controls of a form, the list toolbar and the command bar (UX.5)', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 800 });
  await login(page, 'e2e_manager');

  // A new sales order: a sidebar link, the primary Save, a text input, a textarea, a cell input and
  // the secondary "Add row" button.
  await page.goto('/desk/#/new/sales_order');
  await expect(page.getByRole('heading', { name: 'New Sales Order', exact: true })).toBeFocused();
  await page.getByRole('button', { name: 'Add row' }).click();
  await expect(page.getByLabel('Description, row 1')).toBeFocused();
  await page.getByRole('heading', { name: 'New Sales Order', exact: true }).focus();
  await expectKeyboardFocusVisible(page, [
    page.locator('.sidebar .side-link').first(),
    page.getByRole('button', { name: 'Save', exact: true }),
    page.getByLabel('Customer'),
    page.getByLabel('Remarks'),
    page.getByLabel('Description, row 1'),
    page.getByRole('button', { name: 'Remove row 1' }),
    page.getByRole('button', { name: 'Add row' }),
  ]);

  // The list toolbar: the search field, the secondary Filter button and, inside the panel, a select.
  await page.goto('/desk/#/list/sales_order');
  await page.getByRole('button', { name: 'Leave and discard' }).click(); // the form has an unsaved row
  await expectLoaded(page);
  await page.getByRole('button', { name: 'Filter', exact: true }).click();
  await page.getByRole('button', { name: 'Add filter' }).click();
  const search = page.getByRole('searchbox');
  await search.focus();
  await expectKeyboardFocusVisible(page, [
    page.getByRole('button', { name: 'Filter', exact: true }),
    page.locator('.filter-row select').first(),
  ]);
  await search.focus();
  await expect.poll(async () => (await focusIndicator(search)).outline).toBe(true);

  // The command bar: its input has no outline of its own; the row shows a navy bar (a box shadow).
  await page.keyboard.press('Control+k');
  const bar = page.getByRole('dialog', { name: 'Search or jump to' });
  await expect(bar.getByRole('combobox')).toBeFocused();
  await expect
    .poll(async () => bar.locator('.command-input').evaluate((el) => getComputedStyle(el).boxShadow !== 'none'))
    .toBe(true);
});

// ---- M8.4: query reports ------------------------------------------------------------------

/** The text of the cells of one column of the result table, in order (the first column is 0). */
async function resultColumn(page: Page, index: number): Promise<string[]> {
  return page.locator('.report-table tbody tr').evaluateAll((rows, i) => rows.map((r) => r.children[i]?.textContent ?? ''), index);
}

/** A parameter's control by its label; a required one is labelled "From *" (the star is decoration). */
const paramControl = (page: Page, label: string) => page.getByLabel(new RegExp(`^${label}( \\*)?$`));

test('a manager runs a report, sorts it and reloads; a clerk is not offered Reports (M8.4)', async ({ page, request }) => {
  await page.setViewportSize({ width: 1280, height: 800 });

  // Data for a date range of its own (a year no other test uses, new at every run): two orders of
  // Ada Lovelace (the seeded customer) and one each of two new customers. Created through the API
  // with a session of its own, so that the desk's CSRF token is not rotated.
  const api = await request.post('/api/v1/login', { data: { username: 'e2e_manager', password: PASSWORD } });
  expect(api.ok()).toBe(true);
  const csrf = ((await api.json()) as { data: { csrf_token: string } }).data.csrf_token;
  const post = async (path: string, data: unknown) =>
    request.post(`/api/v1${path}`, { data, headers: { 'X-Forgebase-CSRF': csrf } });
  for (const [email, full_name] of [
    ['report-alpha@example.com', 'Report Alpha'],
    ['report-beta@example.com', 'Report Beta'],
  ]) {
    await post('/resource/customer', { email, full_name }); // a second run: already there
  }
  const year = 2100 + (Date.now() % 7000);
  const day = `${year}-03-05`;
  for (const [customer, rows] of [
    [CUSTOMER, [['Widget', 2, '9.50']]],
    [CUSTOMER, [['Gadget', 1, '100.00']]],
    ['report-alpha@example.com', [['Cable', 4, '10.00']]],
    ['report-beta@example.com', [['Server', 10, '125.00']]],
  ] as const) {
    const created = await post('/resource/sales_order', {
      customer,
      order_date: day,
      items: rows.map(([description, qty, rate]) => ({ description, qty, rate })),
    });
    expect(created.status(), 'the order is created').toBe(201);
  }

  await login(page, 'e2e_manager');
  const sidebar = page.getByRole('navigation', { name: 'Document types' });
  await expect(sidebar.getByRole('heading', { name: 'Reports' })).toBeVisible();
  await sidebar.getByRole('link', { name: 'Sales by customer' }).click();
  await expect(page.getByRole('heading', { name: 'Sales by customer', level: 1 })).toBeVisible();
  await expect(page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: 'Reports' })).toBeVisible();
  await expect(sidebar.getByRole('link', { name: 'Sales by customer' })).toHaveAttribute('aria-current', 'page');

  // Nothing runs by itself on a bare address.
  await expect(page.getByText('Set the parameters, then run the report.')).toBeVisible();
  await expect(page.locator('.report-table')).toHaveCount(0);

  // The required dates are asked for on the fields, and the focus goes to the first.
  await page.getByRole('button', { name: 'Run report' }).click();
  await expect(paramControl(page, 'From')).toBeFocused();
  await expect(page.getByText('This field is required.')).toHaveCount(2);

  await paramControl(page, 'From').fill(`${year}-03-01`);
  await paramControl(page, 'To').fill(`${year}-03-31`);
  await page.getByRole('button', { name: 'Run report' }).click();
  await expect(page.locator('.report-table tbody tr')).toHaveCount(3);
  await expect(page).toHaveURL(new RegExp(`from=${year}-03-01.*to=${year}-03-31`));
  await expect(page.locator('.report-count')).toHaveText('3 rows');
  // The order of the SQL: by customer. Numbers are formatted and on the right; dates are formatted.
  expect(await resultColumn(page, 0)).toEqual(['Ada Lovelace', 'Report Alpha', 'Report Beta']);
  expect(await resultColumn(page, 1)).toEqual(['2', '1', '1']);
  expect(await resultColumn(page, 2)).toEqual(['119.00', '40.00', '1,250.00']);
  expect(await resultColumn(page, 3)).toEqual([`5 Mar ${year}`, `5 Mar ${year}`, `5 Mar ${year}`]);
  await expect(page.locator('.report-table tbody tr').first().locator('td').nth(2)).toHaveClass(/num/);
  await expectLoaded(page);
  await expectNoHorizontalScroll(page, 'a report result');

  // Sorting by Total (numbers, not text): ascending, descending, and back to the order of the SQL.
  const total = page.getByRole('columnheader', { name: /Total/ });
  await total.getByRole('button').click();
  await expect(total).toHaveAttribute('aria-sort', 'ascending');
  expect(await resultColumn(page, 0)).toEqual(['Report Alpha', 'Ada Lovelace', 'Report Beta']);
  await total.getByRole('button').click();
  await expect(total).toHaveAttribute('aria-sort', 'descending');
  expect(await resultColumn(page, 0)).toEqual(['Report Beta', 'Ada Lovelace', 'Report Alpha']);
  await total.getByRole('button').click();
  await expect(total).not.toHaveAttribute('aria-sort', /.+/);
  expect(await resultColumn(page, 0)).toEqual(['Ada Lovelace', 'Report Alpha', 'Report Beta']);

  // A reload with the parameters in the address shows them in the form and runs the report again.
  await page.reload();
  await expect(paramControl(page, 'From')).toHaveValue(`${year}-03-01`);
  await expect(page.locator('.report-table tbody tr')).toHaveCount(3);

  // A Select narrows the result; nothing matching is the empty state.
  await paramControl(page, 'Status').selectOption('Submitted');
  await page.getByRole('button', { name: 'Run report' }).click();
  await expect(page.getByRole('heading', { name: 'No rows' })).toBeVisible();
  await expect(page.locator('.report-table')).toHaveCount(0);
  await expectNoSecretsInStorage(page);

  // The command bar offers it.
  await page.keyboard.press('Control+k');
  await page.getByRole('combobox', { name: 'Search or jump to' }).fill('sales by');
  await expect(page.getByRole('option', { name: 'Run Sales by customer', exact: true })).toBeVisible();
  await page.keyboard.press('Escape');

  // A clerk has no role of the report: no section, no command, and the address is "not found".
  await logout(page);
  await login(page, 'e2e_clerk');
  await expect(sidebar.getByRole('heading', { name: 'Reports' })).toHaveCount(0);
  await page.keyboard.press('Control+k');
  await page.getByRole('combobox', { name: 'Search or jump to' }).fill('sales by');
  await expect(page.getByRole('option', { name: /^Run /i })).toHaveCount(0);
  await page.keyboard.press('Escape');
  await page.goto('/desk/#/report/e2e.sales_by_customer');
  await expect(page.getByRole('heading', { name: 'Report not found' })).toBeVisible();
  // The API says the same: 404 for the clerk, and the list is empty.
  const refusal = await page.evaluate(async () => {
    const session = (await (await fetch('/api/v1/session', { headers: { 'X-Forgebase-CSRF': 'refresh' } })).json()) as {
      data: { csrf_token: string };
    };
    const run = await fetch('/api/v1/report/e2e.sales_by_customer', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Forgebase-CSRF': session.data.csrf_token },
      body: JSON.stringify({ params: {} }),
    });
    const list = (await (await fetch('/api/v1/report')).json()) as { data: unknown[] };
    return { status: run.status, listed: list.data.length };
  });
  expect(refusal).toEqual({ status: 404, listed: 0 });
});
