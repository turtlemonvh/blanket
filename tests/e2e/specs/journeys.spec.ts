// Journey-level UI tests.
//
// These are written against what a user sees — role, text, label — rather
// than against implementation details (ng-* directives, CSS class names,
// bootstrap structure). The goal is that this suite is the acceptance
// criteria for the upcoming HTMX/Go-template UI rewrite: the selectors
// should survive a framework swap untouched.
//
// Conventions:
//   - Use getByRole / getByLabel / getByText / getByPlaceholder.
//   - Assert with expect(locator).toBeVisible() / toContainText(), not
//     fixed waitForTimeout(). Avoid CSS class selectors.
//   - Each test cleans up its own resources via the API so order doesn't
//     matter and re-runs on a dirty DB don't interfere.

import { test, expect } from '@playwright/test';

const skipBrowser = process.env.SKIP_BROWSER_TESTS === '1';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Delete every task currently in the DB. Safe on an empty DB. */
async function purgeTasks(apiRequest: import('@playwright/test').APIRequestContext) {
  const res = await apiRequest.get('/task/');
  if (!res.ok()) return;
  const tasks = (await res.json()) as Array<{ id: string }>;
  for (const t of tasks) {
    await apiRequest.delete(`/task/${t.id}`);
  }
}

/** Submit a task via the API and return its id. */
async function createTask(
  apiRequest: import('@playwright/test').APIRequestContext,
  type: string,
): Promise<string> {
  const res = await apiRequest.post('/task/', { data: { type } });
  expect(res.status()).toBe(201);
  const body = await res.json();
  return body.id as string;
}

// ---------------------------------------------------------------------------
// Navigation — top-level nav links reach each main view
// ---------------------------------------------------------------------------

test.describe('Navigation', () => {
  test.skip(skipBrowser, 'SKIP_BROWSER_TESTS=1');

  test('nav links reach Tasks, Workers, Task Types, About', async ({ page }) => {
    await page.goto('/ui/');

    // Landing page is the tasks view by default.
    await expect(
      page.getByRole('heading', { name: 'Tasks', exact: true }),
    ).toBeVisible();

    await page.getByRole('link', { name: 'Workers' }).click();
    await expect(
      page.getByRole('heading', { name: 'Workers', exact: true }),
    ).toBeVisible();

    await page.getByRole('link', { name: 'Task Types' }).click();
    await expect(
      page.getByRole('heading', { name: 'Task Types', exact: true }),
    ).toBeVisible();

    // About page is nav-reachable; URL should reflect that even if the
    // heading wording changes.
    await page.getByRole('link', { name: 'About' }).click();
    await expect(page).toHaveURL(/\/about$/);

    // Back to Tasks.
    await page.getByRole('link', { name: 'Tasks', exact: true }).click();
    await expect(
      page.getByRole('heading', { name: 'Tasks', exact: true }),
    ).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Task list journey — submit via API, see the row appear after refresh
// ---------------------------------------------------------------------------

test.describe('Task list', () => {
  test.skip(skipBrowser, 'SKIP_BROWSER_TESTS=1');

  test.beforeEach(async ({ request }) => {
    await purgeTasks(request);
  });

  test.afterEach(async ({ request }) => {
    await purgeTasks(request);
  });

  test('empty state, then a task submitted via API appears after Refresh', async ({
    page,
    request,
  }) => {
    await page.goto('/ui/');
    await expect(
      page.getByRole('columnheader', { name: 'State' }),
    ).toBeVisible();

    const taskId = await createTask(request, 'echo_task');
    await page.getByRole('button', { name: /refresh list/i }).click();

    // The row's ID cell shows the first 8 chars; search by that prefix.
    const idPrefix = taskId.slice(0, 8);
    await expect(page.getByText(idPrefix, { exact: false })).toBeVisible();

    await expect(
      page.getByRole('cell', { name: 'echo_task' }).first(),
    ).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Submit a task via the UI form
// ---------------------------------------------------------------------------

test.describe('Submit task via UI', () => {
  test.skip(skipBrowser, 'SKIP_BROWSER_TESTS=1');

  test.beforeEach(async ({ request }) => {
    await purgeTasks(request);
  });

  test.afterEach(async ({ request }) => {
    await purgeTasks(request);
  });

  test('opening New, choosing a type, Launch creates a task', async ({
    page,
    request,
  }) => {
    await page.goto('/ui/');

    await page.getByRole('button', { name: 'New', exact: true }).click();

    const typeSelect = page.getByLabel(/new task type/i);
    await expect(typeSelect).toBeVisible();
    await typeSelect.selectOption({ label: 'echo_task' });

    // echo_task in the test fixture has no required env vars, so Launch
    // should be enabled immediately.
    await page.getByRole('button', { name: /launch task/i }).click();

    await page.getByRole('button', { name: /refresh list/i }).click();
    await expect(
      page.getByRole('cell', { name: 'echo_task' }).first(),
    ).toBeVisible();

    const res = await request.get('/task/');
    const tasks = (await res.json()) as Array<{ type: string }>;
    expect(tasks).toHaveLength(1);
    expect(tasks[0].type).toBe('echo_task');
  });
});

// ---------------------------------------------------------------------------
// Cancel a WAITING task from the list
// ---------------------------------------------------------------------------

test.describe('Cancel task from list', () => {
  test.skip(skipBrowser, 'SKIP_BROWSER_TESTS=1');

  test.beforeEach(async ({ request }) => {
    await purgeTasks(request);
  });

  test.afterEach(async ({ request }) => {
    await purgeTasks(request);
  });

  test('clicking Cancel on a WAITING task transitions it to STOPPED', async ({
    page,
    request,
  }) => {
    const taskId = await createTask(request, 'echo_task');
    await page.goto('/ui/');
    await page.getByRole('button', { name: /refresh list/i }).click();

    const idPrefix = taskId.slice(0, 8);
    const row = page.getByRole('row').filter({ hasText: idPrefix });
    await expect(row).toBeVisible();
    await expect(row.getByText('WAITING')).toBeVisible();

    // The Cancel control in the current UI is an <a> without href, which
    // isn't a "link" in ARIA terms. Match by visible text scoped to the row.
    await row.getByText('Cancel', { exact: true }).click();

    await page.getByRole('button', { name: /refresh list/i }).click();
    await expect(row.getByText('STOPPED')).toBeVisible();

    const res = await request.get(`/task/${taskId}`);
    const t = await res.json();
    expect(t.state).toBe('STOPPED');
  });
});

// ---------------------------------------------------------------------------
// Task types view lists configured types
// ---------------------------------------------------------------------------

test.describe('Task types view', () => {
  test.skip(skipBrowser, 'SKIP_BROWSER_TESTS=1');

  test('lists the echo_task fixture', async ({ page }) => {
    // Navigate via the visible nav link rather than a deep hash URL so the
    // test doesn't depend on AngularJS ui-router fragment parsing.
    await page.goto('/ui/');
    await page.getByRole('link', { name: 'Task Types' }).click();
    await expect(
      page.getByRole('heading', { name: 'Task Types', exact: true }),
    ).toBeVisible();
    await expect(page.getByRole('link', { name: 'echo_task' })).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Workers list view renders
// ---------------------------------------------------------------------------

test.describe('Workers view', () => {
  test.skip(skipBrowser, 'SKIP_BROWSER_TESTS=1');

  test('header and column labels render', async ({ page }) => {
    await page.goto('/ui/');
    await page.getByRole('link', { name: 'Workers' }).click();
    await expect(
      page.getByRole('heading', { name: 'Workers', exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole('button', { name: 'New', exact: true }),
    ).toBeVisible();
    await expect(page.getByRole('columnheader', { name: 'Tags' })).toBeVisible();
  });
});

// Auto-refresh — tasks/workers tbody polls every 2s, so a row submitted via
// the API while the page is open should appear WITHOUT clicking Refresh.
// ---------------------------------------------------------------------------

test.describe('Auto-refresh', () => {
  test.skip(skipBrowser, 'SKIP_BROWSER_TESTS=1');

  test.beforeEach(async ({ request }) => {
    await purgeTasks(request);
  });
  test.afterEach(async ({ request }) => {
    await purgeTasks(request);
  });

  test('tasks page picks up an API-submitted task within the poll window', async ({
    page,
    request,
  }) => {
    await page.goto('/ui/');
    // Confirm the tbody has the polling attributes wired up.
    const tbody = page.locator('#tasks-rows');
    await expect(tbody).toHaveAttribute('hx-trigger', 'every 2s');

    const taskId = await createTask(request, 'echo_task');
    const idPrefix = taskId.slice(0, 8);

    // Default polling cadence is 2s; allow up to 5s for the swap to land.
    await expect(page.getByText(idPrefix, { exact: false })).toBeVisible({
      timeout: 5000,
    });
  });

  test('workers tbody is wired for polling', async ({ page }) => {
    await page.goto('/ui/');
    await page.getByRole('link', { name: 'Workers' }).click();
    const tbody = page.locator('#workers-rows');
    await expect(tbody).toHaveAttribute('hx-trigger', 'every 2s');
    await expect(tbody).toHaveAttribute('hx-get', '/ui/partials/workers-rows');
  });
});

// ---------------------------------------------------------------------------
// Worker detail page — metadata table and Live Log section
// ---------------------------------------------------------------------------

test.describe('Worker detail page', () => {
  test.skip(skipBrowser, 'SKIP_BROWSER_TESTS=1');

  let workerId: string;

  test.beforeEach(async ({ request }) => {
    // Register a fake worker via the API so the detail page has data.
    workerId = require('crypto').randomBytes(12).toString('hex');
    const res = await request.put(`/worker/${workerId}`, {
      data: {
        id: workerId,
        tags: ['bash', 'unix'],
        pid: 99999,
        stopped: false,
        checkInterval: 2,
        logfile: '/tmp/test-worker.log',
        startedTs: Math.floor(Date.now() / 1000),
      },
    });
    expect(res.status()).toBe(200);
  });

  test.afterEach(async ({ request }) => {
    // Stop then delete the worker.
    await request.put(`/worker/${workerId}/stop`);
    await request.delete(`/worker/${workerId}`);
  });

  test('shows worker metadata and Live Log heading', async ({ page }) => {
    await page.goto(`/ui/workers/${workerId}`);
    await expect(
      page.getByRole('heading', { name: 'Worker Detail', exact: true }),
    ).toBeVisible();

    // Metadata table contains the worker ID.
    await expect(page.getByRole('cell', { name: workerId })).toBeVisible();

    // Tags appear.
    await expect(page.getByText('bash, unix')).toBeVisible();

    // Live Log section heading is present.
    await expect(
      page.getByRole('heading', { name: /Live Log/i }),
    ).toBeVisible();

    // Back link navigates to workers list.
    await page.getByRole('link', { name: /Back to Workers/i }).click();
    await expect(
      page.getByRole('heading', { name: 'Workers', exact: true }),
    ).toBeVisible();
  });

  test('workers list links to detail page', async ({ page }) => {
    await page.goto('/ui/workers');
    await page.getByRole('button', { name: /refresh list/i }).click();

    // The PID cell is a link to the detail page.
    const pidLink = page.getByRole('link', { name: '99999' });
    await expect(pidLink).toBeVisible();
    await pidLink.click();

    await expect(
      page.getByRole('heading', { name: 'Worker Detail', exact: true }),
    ).toBeVisible();
  });

  test('stopped worker shows streaming-disabled message', async ({ page, request }) => {
    await request.put(`/worker/${workerId}/stop`);
    await page.goto(`/ui/workers/${workerId}`);
    await expect(
      page.getByText('Worker is stopped', { exact: false }),
    ).toBeVisible();
  });
});
