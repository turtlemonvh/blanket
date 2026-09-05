// Journey-level UI tests for the scheduling surfaces (turtlemonvh/blanket#98):
// the Upcoming page, the series detail page, and the series card that links a
// spawned run back to the series it came from.
//
// Same conventions as journeys.spec.ts: drive by role/text, assert with
// expect(locator) rather than fixed waits, and clean up via the API so a
// re-run on a dirty DB is fine. Note that a RECURRING template left behind
// keeps spawning children on the shared server, so every test in here purges.

import { test, expect, Page } from '@playwright/test';

const skipBrowser = process.env.SKIP_BROWSER_TESTS === '1';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

type Api = import('@playwright/test').APIRequestContext;

/** Delete every task currently in the DB. Safe on an empty DB. */
async function purgeTasks(apiRequest: Api) {
  const res = await apiRequest.get('/task/');
  if (!res.ok()) return;
  const tasks = (await res.json()) as Array<{ id: string }>;
  for (const t of tasks) {
    await apiRequest.delete(`/task/${t.id}`);
  }
}

/** Submit a recurring template via the API and return its id. */
async function createSeries(apiRequest: Api, cron: string): Promise<string> {
  const res = await apiRequest.post('/task/', {
    data: { type: 'echo_task', cron },
  });
  expect(res.status()).toBe(201);
  const body = await res.json();
  expect(body.state).toBe('RECURRING');
  return body.id as string;
}

/** Submit a delayed one-shot task via the API and return its id. */
async function createScheduled(apiRequest: Api, notBefore: string): Promise<string> {
  const res = await apiRequest.post('/task/', {
    data: { type: 'echo_task', notBefore },
  });
  expect(res.status()).toBe(201);
  const body = await res.json();
  expect(body.state).toBe('SCHEDULED');
  return body.id as string;
}

/**
 * Locate the table row for a task by its full id.
 *
 * Rows only *display* an 8-char id prefix, and ObjectIds minted in the same
 * second share those 8 characters -- so filtering on the visible prefix can
 * match the wrong row. The row's link href carries the full id.
 */
function rowFor(page: Page, taskId: string) {
  return page
    .getByRole('row')
    .filter({ has: page.locator(`a[href="/ui/tasks/${taskId}"]`) });
}

/** Auto-accept htmx's hx-confirm dialogs for the rest of the page's life. */
function acceptDialogs(page: Page) {
  page.on('dialog', (d) => d.accept());
}

// ---------------------------------------------------------------------------
// Upcoming page
// ---------------------------------------------------------------------------

test.describe('Upcoming page', () => {
  test.skip(skipBrowser, 'SKIP_BROWSER_TESTS=1');

  test.beforeEach(async ({ request }) => {
    await purgeTasks(request);
  });
  test.afterEach(async ({ request }) => {
    await purgeTasks(request);
  });

  test('nav reaches Upcoming; one-time and series are listed separately', async ({
    page,
    request,
  }) => {
    const oneTimeId = await createScheduled(request, '2h');
    const seriesId = await createSeries(request, '*/5 * * * *');

    await page.goto('/ui/');
    await page.getByRole('link', { name: 'Upcoming' }).click();
    await expect(
      page.getByRole('heading', { name: 'Upcoming', exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole('heading', { name: 'One-time', exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole('heading', { name: 'Series', exact: true }),
    ).toBeVisible();

    // The one-shot task shows its friendly "Once, at …" description and a
    // Cancel action.
    const oneTimeRow = rowFor(page, oneTimeId);
    await expect(oneTimeRow).toBeVisible();
    await expect(oneTimeRow).toContainText('Once, at');
    await expect(oneTimeRow.getByText('Cancel', { exact: true })).toBeVisible();

    // The series shows its friendly cron text, the raw expression, a Live
    // badge and a link to its detail page.
    const seriesRow = rowFor(page, seriesId);
    await expect(seriesRow).toContainText('Every 5 minutes');
    await expect(seriesRow).toContainText('*/5 * * * *');
    await expect(seriesRow.getByText('Live', { exact: true })).toBeVisible();
    await expect(
      seriesRow.getByRole('link', { name: 'View series' }),
    ).toBeVisible();
  });

  test('a paused series stays listed, showing when it was paused', async ({
    page,
    request,
  }) => {
    const seriesId = await createSeries(request, '*/5 * * * *');
    expect((await request.put(`/task/${seriesId}/pause`)).status()).toBe(200);

    await page.goto('/ui/upcoming');
    const row = rowFor(page, seriesId);
    await expect(row.getByText('Paused', { exact: true })).toBeVisible();
    await expect(row).toContainText(/Paused \d{4}\/\d{2}\/\d{2}/);
  });

  test('a cancelled series drops off the page', async ({ page, request }) => {
    const seriesId = await createSeries(request, '*/5 * * * *');
    await page.goto('/ui/upcoming');
    await expect(page.getByText(seriesId.slice(0, 8))).toBeVisible();

    expect((await request.put(`/task/${seriesId}/cancel`)).status()).toBe(200);
    await page.getByRole('button', { name: /refresh list/i }).click();
    await expect(page.getByText('No scheduled series.')).toBeVisible();
  });

  test('cancelling a one-time task from Upcoming removes its row', async ({
    page,
    request,
  }) => {
    acceptDialogs(page);
    const oneTimeId = await createScheduled(request, '2h');

    await page.goto('/ui/upcoming');
    const row = rowFor(page, oneTimeId);
    await expect(row).toBeVisible();

    await row.getByText('Cancel', { exact: true }).click();

    await expect(page.getByText('No one-time tasks scheduled.')).toBeVisible();
    const after = await (await request.get(`/task/${oneTimeId}`)).json();
    expect(after.state).toBe('STOPPED');
  });

  test('empty state renders for both sections', async ({ page }) => {
    await page.goto('/ui/upcoming');
    await expect(page.getByText('No one-time tasks scheduled.')).toBeVisible();
    await expect(page.getByText('No scheduled series.')).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Series detail: schedule, pause/resume, change schedule, cancel
// ---------------------------------------------------------------------------

test.describe('Series detail', () => {
  test.skip(skipBrowser, 'SKIP_BROWSER_TESTS=1');

  test.beforeEach(async ({ request }) => {
    await purgeTasks(request);
  });
  test.afterEach(async ({ request }) => {
    await purgeTasks(request);
  });

  test('pause shows paused-at, resume restores Live', async ({ page, request }) => {
    const seriesId = await createSeries(request, '*/5 * * * *');

    await page.goto('/ui/upcoming');
    await rowFor(page, seriesId).getByRole('link', { name: 'View series' }).click();

    await expect(
      page.getByRole('heading', { name: 'Series Detail', exact: true }),
    ).toBeVisible();
    const block = page.locator('#series-schedule');
    await expect(block).toContainText('Every 5 minutes');
    await expect(block).toContainText('*/5 * * * *');
    await expect(block.getByLabel('series status')).toHaveText('Live');

    await page.getByRole('button', { name: 'Pause', exact: true }).click();
    await expect(block.getByLabel('series status')).toHaveText('Paused');
    await expect(block).toContainText('Paused at');
    await expect(block).toContainText(/Paused at[\s\S]*\d{4}\/\d{2}\/\d{2}/);
    expect((await (await request.get(`/task/${seriesId}`)).json()).state).toBe('PAUSED');

    await page.getByRole('button', { name: 'Resume', exact: true }).click();
    await expect(block.getByLabel('series status')).toHaveText('Live');
    await expect(block).not.toContainText('Paused at');
    expect((await (await request.get(`/task/${seriesId}`)).json()).state).toBe(
      'RECURRING',
    );
  });

  test('changing the schedule previews the new cron and saves it', async ({
    page,
    request,
  }) => {
    const seriesId = await createSeries(request, '*/5 * * * *');
    await page.goto(`/ui/tasks/${seriesId}`);

    const block = page.locator('#series-schedule');
    // The shared schedule editor (schedule_editor.html), rendered with the
    // "series-schedule" id prefix.
    const cronInput = page.locator('#series-schedule-cron');
    const preview = page.locator('#series-schedule-preview');

    // A series has no schedule to opt into, so the editor is open with the
    // repeating mode already picked.
    await expect(block.locator('#series-schedule-editor')).toHaveClass(
      /schedule-open/,
    );
    await expect(page.locator('#series-schedule-mode-repeating')).toBeChecked();
    await expect(cronInput).toHaveValue('*/5 * * * *');

    // The preview is server-rendered for the current expression, then
    // follows typing.
    await expect(preview).toContainText('Every 5 minutes');

    // Type it the way a user would — the shared editor debounces its
    // preview off keyup, which fill() doesn't produce.
    await cronInput.fill('');
    await cronInput.pressSequentially('0 3 * * *');
    await expect(preview).toContainText('At 03:00 AM');
    await expect(preview).toContainText('Next:');

    // An invalid expression is reported inline and doesn't save anything.
    await cronInput.fill('');
    await cronInput.pressSequentially('nope');
    await expect(preview).toContainText('invalid cron expression');
    await page.getByRole('button', { name: /save schedule/i }).click();
    await expect(page.locator('#series-schedule > p.inline-error')).toContainText(
      'invalid cron expression',
    );
    expect((await (await request.get(`/task/${seriesId}`)).json()).cronExpr).toBe(
      '*/5 * * * *',
    );

    // A valid one re-renders the block with the new friendly description.
    await cronInput.fill('');
    await cronInput.pressSequentially('0 3 * * *');
    await page.getByRole('button', { name: /save schedule/i }).click();
    await expect(page.locator('#series-schedule')).toContainText('At 03:00 AM');
    await expect(page.locator('#series-schedule')).not.toContainText(
      'Every 5 minutes',
    );
    expect((await (await request.get(`/task/${seriesId}`)).json()).cronExpr).toBe(
      '0 3 * * *',
    );
  });

  test('cancelling from the detail page hides the actions and empties Upcoming', async ({
    page,
    request,
  }) => {
    acceptDialogs(page);
    const seriesId = await createSeries(request, '*/5 * * * *');
    await page.goto(`/ui/tasks/${seriesId}`);

    await page.getByRole('button', { name: /cancel series/i }).click();

    const block = page.locator('#series-schedule');
    await expect(block.getByLabel('series status')).toHaveText('Cancelled');
    await expect(block).toContainText('will not fire again');
    await expect(page.getByRole('button', { name: 'Pause', exact: true })).toHaveCount(0);
    await expect(page.locator('#series-schedule-cron')).toHaveCount(0);
    // The record survives — the schedule and Past runs are still shown.
    await expect(page.getByRole('heading', { name: 'Past runs' })).toBeVisible();

    await page.goto('/ui/upcoming');
    await expect(page.getByText('No scheduled series.')).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// A spawned run: series card on its detail page, backlink in the list, and
// the run itself under the series' Past runs.
//
// This one waits for the scheduler to actually fire a "* * * * *" template,
// which is up to a minute away — hence the raised timeout. It's the only
// place a real parent/child pair can come from without reaching into the DB.
// ---------------------------------------------------------------------------

test.describe('Series membership', () => {
  test.skip(skipBrowser, 'SKIP_BROWSER_TESTS=1');

  test.beforeEach(async ({ request }) => {
    await purgeTasks(request);
  });
  test.afterEach(async ({ request }) => {
    await purgeTasks(request);
  });

  test('a fired run shows the series card, the row backlink, and Past runs', async ({
    page,
    request,
  }) => {
    test.setTimeout(180_000);

    const seriesId = await createSeries(request, '* * * * *');

    // Wait for the scheduler to spawn the first child (next minute boundary).
    let childId = '';
    await expect
      .poll(
        async () => {
          const res = await request.get(`/task/?parentId=${seriesId}`);
          if (!res.ok()) return 0;
          const kids = (await res.json()) as Array<{ id: string }>;
          if (kids.length > 0) childId = kids[0].id;
          return kids.length;
        },
        { timeout: 120_000, intervals: [2000] },
      )
      .toBeGreaterThan(0);

    // Tasks list: the child row carries a compact backlink to its series.
    await page.goto('/ui/');
    const childRow = rowFor(page, childId);
    await expect(childRow).toBeVisible();
    const backlink = childRow.getByRole('link', {
      name: `part of series ${seriesId.slice(0, 8)}`,
    });
    await expect(backlink).toBeVisible();

    // Task detail: the series card names the series, its schedule and status.
    await page.goto(`/ui/tasks/${childId}`);
    const card = page.locator('.series-card');
    await expect(card).toContainText('Part of a scheduled series');
    await expect(card).toContainText('Every minute');
    await expect(card.getByLabel('series status')).toHaveText('Live');

    // Following the card lands on the series, whose Past runs lists the child.
    await card.getByRole('link', { name: new RegExp(seriesId) }).click();
    await expect(
      page.getByRole('heading', { name: 'Series Detail', exact: true }),
    ).toBeVisible();
    const runRow = rowFor(page, childId);
    await expect(runRow).toBeVisible();
    // Rows here don't repeat the "part of series" backlink.
    await expect(page.getByText('part of series')).toHaveCount(0);

    // Pausing the series is reflected on the child's card.
    await page.getByRole('button', { name: 'Pause', exact: true }).click();
    await expect(
      page.locator('#series-schedule').getByLabel('series status'),
    ).toHaveText('Paused');
    await page.goto(`/ui/tasks/${childId}`);
    await expect(page.locator('.series-card').getByLabel('series status')).toHaveText(
      'Paused',
    );
  });
});
