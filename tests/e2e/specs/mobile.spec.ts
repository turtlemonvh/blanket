// Phone layout and PWA install (turtlemonvh/blanket#216).
//
// Runs the same pages at a phone-sized viewport: the lists become cards,
// the top links give way to a bottom tab bar, every cancel/delete asks
// first. A desktop block at the end checks the wide screen keeps its table
// and top nav, and that the parts the review asked to match (#216) do:
// the same confirms, the Duration column, and the same log order --
// oldest first, newest at the bottom, a short log sitting at the top.
//
// Rows are located with CSS (#tasks-rows tr) rather than getByRole('row'):
// restyling a <tr> as a grid can cost it its table semantics in the
// accessibility tree, which is fine for a card but not for a role query.

import { test, expect, type Page, type APIRequestContext } from '@playwright/test';

const skipBrowser = process.env.SKIP_BROWSER_TESTS === '1';

const PHONE = { width: 390, height: 844 };
const DESKTOP = { width: 1280, height: 800 };

async function purgeTasks(api: APIRequestContext) {
  const res = await api.get('/task/');
  if (!res.ok()) return;
  for (const t of (await res.json()) as Array<{ id: string }>) {
    await api.delete(`/task/${t.id}`);
  }
}

async function createTask(api: APIRequestContext, type = 'echo_task'): Promise<string> {
  const res = await api.post('/task/', { data: { type } });
  expect(res.status()).toBe(201);
  return (await res.json()).id as string;
}

/** A finished task with the given stdout. */
async function finishedTask(api: APIRequestContext, stdout: string): Promise<string> {
  const res = await api.post('/task/', {
    multipart: {
      data: JSON.stringify({ type: 'echo_task' }),
      'blanket.stdout.log': {
        name: 'blanket.stdout.log',
        mimeType: 'text/plain',
        buffer: Buffer.from(stdout),
      },
    } as never,
  });
  expect(res.status()).toBe(201);
  const id = (await res.json()).id as string;
  expect((await api.put(`/task/${id}/finish?state=SUCCESS&exitCode=0`)).status()).toBe(200);
  return id;
}

const card = (page: Page, id: string) =>
  page.locator('#tasks-rows tr').filter({ has: page.locator(`a[href="/ui/tasks/${id}"]`) });

/** Resolve with the next dialog's message, answering it with `accept`. */
function nextDialog(page: Page, accept: boolean): Promise<string> {
  return new Promise((resolve) => {
    page.once('dialog', async (d) => {
      resolve(d.message());
      if (accept) await d.accept();
      else await d.dismiss();
    });
  });
}

/**
 * Cancel and delete both raise htmx's confirm dialog, naming the task
 * type; declining leaves the task alone. Shared by the phone and desktop
 * blocks: the hx-confirm attributes are in the one set of row markup.
 */
async function confirmJourney(page: Page, request: APIRequestContext) {
  const waiting = await createTask(request);
  const done = await finishedTask(request, 'x\n');
  await page.goto('/ui/');

  // Declining a delete leaves the task alone.
  let msg = nextDialog(page, false);
  await card(page, done).getByText('Delete', { exact: true }).click();
  expect(await msg).toContain('echo_task');
  expect((await request.get(`/task/${done}`)).status()).toBe(200);

  // Accepting it deletes.
  msg = nextDialog(page, true);
  await card(page, done).getByText('Delete', { exact: true }).click();
  expect(await msg).toMatch(/Delete this echo_task task/);
  await expect(card(page, done)).toHaveCount(0);
  expect((await request.get(`/task/${done}`)).status()).toBe(404);

  // Cancel on a WAITING task asks too.
  msg = nextDialog(page, true);
  await card(page, waiting).getByText('Cancel', { exact: true }).click();
  expect(await msg).toMatch(/Cancel this echo_task task/);
  await expect(card(page, waiting).getByText('STOPPED', { exact: true })).toBeVisible();
}

/**
 * The task log reads oldest first with the newest line at the bottom, has
 * no order toggle, and a short log sits at the top of its pane rather than
 * being pushed down. Same on both viewports.
 */
async function logJourney(page: Page, request: APIRequestContext) {
  const id = await finishedTask(request, 'line one\nline two\nline three\n');
  await page.goto(`/ui/tasks/${id}`);

  const pane = page.locator('#task-log');
  await expect(pane).toHaveText(/^\s*line one\s+line two\s+line three\s*$/);
  await expect(page.getByLabel('Newest first')).toHaveCount(0);
  await expect(page.getByLabel('Pin to bottom')).toBeChecked();

  // Top-aligned: the pane is no taller than its three lines need, so the
  // first line starts right under the pane's top edge.
  const box = await pane.boundingBox();
  expect(box).not.toBeNull();
  const firstLine = await pane.evaluate((el) => {
    const range = document.createRange();
    range.selectNodeContents(el);
    return range.getClientRects()[0].top - el.getBoundingClientRect().top;
  });
  expect(firstLine).toBeLessThan(30);
  expect(box!.height).toBeLessThan(150);
}

test.describe('Phone layout', () => {
  test.skip(skipBrowser, 'SKIP_BROWSER_TESTS=1');
  test.use({ viewport: PHONE });

  test.beforeEach(async ({ request }) => purgeTasks(request));
  test.afterEach(async ({ request }) => purgeTasks(request));

  test('is installable: manifest, icons and iOS tags are linked', async ({ page, request }) => {
    await page.goto('/ui/');
    await expect(page.locator('link[rel="manifest"]')).toHaveAttribute(
      'href',
      '/ui/manifest.webmanifest',
    );
    await expect(page.locator('meta[name="theme-color"]')).toHaveAttribute('content', '#2a6fb0');
    await expect(page.locator('link[rel="apple-touch-icon"]')).toHaveAttribute(
      'href',
      '/ui/static/apple-touch-icon.png',
    );
    await expect(page.locator('meta[name="apple-mobile-web-app-capable"]')).toHaveAttribute(
      'content',
      'yes',
    );

    const res = await request.get('/ui/manifest.webmanifest');
    expect(res.status()).toBe(200);
    const m = await res.json();
    expect(m.start_url).toBe('/ui/');
    expect(m.display).toBe('standalone');
    expect(m.short_name).toBe('Blanket');
    const sizes = (m.icons as Array<{ sizes: string; src: string; purpose: string }>).map(
      (i) => i.sizes,
    );
    expect(sizes).toEqual(expect.arrayContaining(['192x192', '512x512']));
    expect(m.icons.some((i: { purpose: string }) => i.purpose.includes('maskable'))).toBe(true);
    for (const icon of m.icons) {
      expect((await request.get(icon.src)).status()).toBe(200);
    }

    // The header names the instance (the hostname, unless configured).
    await expect(page.getByLabel('instance name')).not.toBeEmpty();
  });

  test('tasks render as cards with no sideways scroll', async ({ page, request }) => {
    const waiting = await createTask(request);
    const done = await finishedTask(request, 'hello\n');
    await page.goto('/ui/');

    // No table header on a phone; the cards carry their own hierarchy.
    await expect(page.locator('table.cards thead')).toBeHidden();

    const w = card(page, waiting);
    await expect(w.getByText('WAITING', { exact: true })).toBeVisible();
    await expect(w.getByText(/^queued \d/)).toBeVisible();
    await expect(w.getByText('echo_task', { exact: true })).toBeVisible();
    await expect(w.getByText(waiting.slice(0, 8))).toBeVisible();
    await expect(w.locator('.cell-context')).toContainText('created ');

    const d = card(page, done);
    await expect(d.getByText('SUCCESS', { exact: true })).toBeVisible();
    await expect(d.locator('.cell-context')).toContainText(/exit 0\s+· finished/);

    // Desktop-only columns are gone, and the page fits the screen.
    await expect(w.locator('.cell-tags')).toBeHidden();
    await expect(w.locator('.cell-created')).toBeHidden();
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);

    // Tapping anywhere on the card opens the task (the id link is stretched
    // over it), with its log one more tap away.
    await w.click();
    await expect(page).toHaveURL(new RegExp(`/ui/tasks/${waiting}$`));
    await expect(page.getByRole('link', { name: /Log ↓/ })).toBeVisible();
  });

  test('bottom tab bar replaces the top links', async ({ page }) => {
    await page.goto('/ui/');
    const tabs = page.getByRole('navigation', { name: 'sections' });
    await expect(tabs).toBeVisible();
    await expect(tabs.getByRole('link', { name: 'Tasks' })).toHaveAttribute('aria-current', 'page');

    // The top bar's own section links are hidden; only one Workers link shows.
    await expect(page.getByRole('link', { name: 'Workers', exact: true })).toHaveCount(1);

    await tabs.getByRole('link', { name: 'Workers' }).click();
    await expect(page.getByRole('heading', { name: 'Workers', exact: true })).toBeVisible();

    await tabs.getByText('More', { exact: true }).click();
    await tabs.getByRole('link', { name: 'Task Types' }).click();
    await expect(page.getByRole('heading', { name: 'Task Types', exact: true })).toBeVisible();
  });

  test('cancel and delete both ask first, naming the task type', async ({ page, request }) => {
    await confirmJourney(page, request);
  });

  test('task log is oldest first, newest at the bottom, top-aligned', async ({ page, request }) => {
    await logJourney(page, request);
  });
});

test.describe('Desktop', () => {
  test.skip(skipBrowser, 'SKIP_BROWSER_TESTS=1');
  test.use({ viewport: DESKTOP });

  test.beforeEach(async ({ request }) => purgeTasks(request));
  test.afterEach(async ({ request }) => purgeTasks(request));

  test('no tab bar; table header and Duration column visible', async ({ page, request }) => {
    const id = await createTask(request); // WAITING: duration reads "queued Ns"
    await page.goto('/ui/');
    await expect(page.getByRole('navigation', { name: 'sections' })).toBeHidden();
    await expect(page.getByRole('columnheader', { name: 'Tags' })).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Duration' })).toBeVisible();
    await expect(card(page, id).locator('.cell-duration')).toBeVisible();
    await expect(card(page, id).locator('.cell-duration')).toHaveText(/queued \d+s/);
  });

  test('cancel and delete both ask first, naming the task type', async ({ page, request }) => {
    await confirmJourney(page, request);
  });

  test('task log is oldest first, newest at the bottom, top-aligned', async ({ page, request }) => {
    await logJourney(page, request);
  });
});
