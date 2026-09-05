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

  test('task-type validation warnings surface as a dismissible flash message', async ({
    page,
    request,
  }) => {
    await page.goto('/ui/');

    await page.getByRole('button', { name: 'New', exact: true }).click();
    const typeSelect = page.getByLabel(/new task type/i);
    await expect(typeSelect).toBeVisible();
    await typeSelect.selectOption({ label: 'echo_task' });
    await page.getByRole('button', { name: /launch task/i }).click();

    // testdata/types/echo_task.toml has neither a description nor
    // documentation, which tasks.ValidateTaskType flags as warnings (codes
    // 006/007). Warnings must not block submission, but should surface to
    // the user rather than only going to the server log (#64).
    const flash = page.getByRole('alert');
    await expect(flash).toContainText(/warnings/i);
    await expect(flash).toContainText('description is missing');
    await expect(flash).toContainText('documentation is missing');

    const res = await request.get('/task/');
    const tasks = (await res.json()) as Array<{ type: string }>;
    expect(tasks).toHaveLength(1);
    expect(tasks[0].type).toBe('echo_task');

    // Dismissible.
    await page.getByRole('button', { name: /dismiss warnings/i }).click();
    await expect(flash).toHaveCount(0);
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

  test('type name links to the UI detail page and renders its settings', async ({
    page,
  }) => {
    await page.goto('/ui/task-types');
    const link = page.getByRole('link', { name: 'echo_task' });
    await expect(link).toHaveAttribute('href', '/ui/task-types/echo_task');
    await link.click();

    // Lands on the detail page (not the raw JSON API route) and renders
    // the type's settings, including the env-var table via its partial.
    await expect(page).toHaveURL(/\/ui\/task-types\/echo_task$/);
    await expect(
      page.getByRole('heading', { name: 'echo_task', exact: true }),
    ).toBeVisible();
    await expect(page.getByText('exec:bash, os:unix')).toBeVisible();
    await expect(page.getByText('Environment Variables')).toBeVisible();
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

// SSE push — tasks/workers tables connect to SSE endpoints. A mutation
// (e.g. API-submitted task) fires an event that triggers an htmx re-fetch.
// ---------------------------------------------------------------------------

test.describe('SSE push', () => {
  test.skip(skipBrowser, 'SKIP_BROWSER_TESTS=1');

  test.beforeEach(async ({ request }) => {
    await purgeTasks(request);
  });
  test.afterEach(async ({ request }) => {
    await purgeTasks(request);
  });

  test('tasks page picks up an API-submitted task via SSE', async ({
    page,
    request,
  }) => {
    await page.goto('/ui/');
    const table = page.locator('table[sse-connect="/ui/sse/tasks"]');
    await expect(table).toBeVisible();
    const tbody = page.locator('#tasks-rows');
    // hx-trigger also carries the "refresh-rows" event row actions poke
    // (#98), so match the SSE part rather than the whole attribute.
    await expect(tbody).toHaveAttribute('hx-trigger', /sse:tasks-changed/);

    const taskId = await createTask(request, 'echo_task');
    const idPrefix = taskId.slice(0, 8);

    // SSE push should deliver the update within a few seconds.
    await expect(page.getByText(idPrefix, { exact: false })).toBeVisible({
      timeout: 5000,
    });
  });

  test('workers table is wired for SSE push', async ({ page }) => {
    await page.goto('/ui/');
    await page.getByRole('link', { name: 'Workers' }).click();
    const table = page.locator('table[sse-connect="/ui/sse/workers"]');
    await expect(table).toBeVisible();
    const tbody = page.locator('#workers-rows');
    await expect(tbody).toHaveAttribute('hx-trigger', 'sse:workers-changed');
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
        tags: ['exec:bash', 'os:unix'],
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
    await expect(page.getByText('exec:bash, os:unix')).toBeVisible();

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

  test('stopped worker loads final log output', async ({ page, request }) => {
    await request.put(`/worker/${workerId}/stop`);
    await page.goto(`/ui/workers/${workerId}`);
    // Stopped workers show a static log pre-load instead of SSE streaming.
    const logPre = page.locator('pre[aria-label="worker log output"]');
    await expect(logPre).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Scheduling section on the new-task form (turtlemonvh/blanket#97)
// ---------------------------------------------------------------------------

/** A local "YYYY-MM-DDTHH:mm" value, offsetMs from now — what an
 *  <input type="datetime-local"> expects. */
function datetimeLocalValue(offsetMs: number): string {
  const d = new Date(Date.now() + offsetMs);
  const pad = (n: number) => String(n).padStart(2, '0');
  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}` +
    `T${pad(d.getHours())}:${pad(d.getMinutes())}`
  );
}

test.describe('Schedule a task from the new-task form', () => {
  test.skip(skipBrowser, 'SKIP_BROWSER_TESTS=1');

  test.beforeEach(async ({ request }) => {
    await purgeTasks(request);
  });

  test.afterEach(async ({ request }) => {
    await purgeTasks(request);
  });

  /** Open the tasks page with the New Task form expanded. */
  async function openNewTaskForm(page: import('@playwright/test').Page) {
    await page.goto('/ui/');
    await page.getByRole('button', { name: 'New', exact: true }).click();
    await expect(page.getByLabel(/new task type/i)).toBeVisible();
  }

  test('the schedule section is hidden until "Schedule task?" is checked', async ({
    page,
  }) => {
    await openNewTaskForm(page);

    const scheduleToggle = page.getByLabel('Schedule task?');
    await expect(scheduleToggle).toBeVisible();
    await expect(scheduleToggle).not.toBeChecked();

    // Nothing schedule-related is on screen yet.
    await expect(page.getByRole('group', { name: /when should it run/i })).toBeHidden();
    await expect(page.getByLabel('Start no earlier than')).toBeHidden();

    await scheduleToggle.check();

    await expect(page.getByRole('group', { name: /when should it run/i })).toBeVisible();
    // "One time" is the default, so its field is the one showing.
    await expect(page.getByLabel('One time', { exact: true })).toBeChecked();
    await expect(page.getByLabel('Start no earlier than')).toBeVisible();
    await expect(page.getByLabel('Repeat on this cron schedule')).toBeHidden();
  });

  test('the One time / Repeating radios swap which field is shown', async ({
    page,
  }) => {
    await openNewTaskForm(page);
    await page.getByLabel('Schedule task?').check();

    await page.getByLabel('Repeating', { exact: true }).check();
    await expect(page.getByLabel('Repeat on this cron schedule')).toBeVisible();
    await expect(page.getByLabel('Start no earlier than')).toBeHidden();

    await page.getByLabel('One time', { exact: true }).check();
    await expect(page.getByLabel('Start no earlier than')).toBeVisible();
    await expect(page.getByLabel('Repeat on this cron schedule')).toBeHidden();
  });

  test('typing a cron expression shows a live human-readable preview', async ({
    page,
  }) => {
    await openNewTaskForm(page);
    await page.getByLabel('Schedule task?').check();
    await page.getByLabel('Repeating', { exact: true }).check();

    const cron = page.getByLabel('Repeat on this cron schedule');
    const preview = page.locator('#schedule-preview');
    await expect(preview).toContainText(/type a cron expression/i);

    // Type it like a user would: the preview is debounced off keyup.
    await cron.pressSequentially('0 14 * * 2');
    await expect(preview).toContainText(/Tuesday/i);
    await expect(preview).toContainText(/02:00 PM/i);
    // ... and lists the next fire times.
    await expect(preview).toContainText(/Next:/);

    // A bad expression reports the parser's complaint inline instead.
    await cron.fill('');
    await cron.pressSequentially('nope');
    await expect(preview).toContainText(/invalid cron expression/i);
  });

  test('a one-time schedule creates a SCHEDULED task', async ({
    page,
    request,
  }) => {
    await openNewTaskForm(page);
    await page.getByLabel(/new task type/i).selectOption({ label: 'echo_task' });

    await page.getByLabel('Schedule task?').check();
    await page
      .getByLabel('Start no earlier than')
      .fill(datetimeLocalValue(2 * 60 * 60 * 1000));
    await page.getByRole('button', { name: /launch task/i }).click();

    // The flash says the task is scheduled, with its friendly description.
    const flash = page.getByRole('status');
    await expect(flash).toContainText(/task scheduled/i);
    await expect(flash).toContainText('SCHEDULED - Once, at');

    // The row lands in state SCHEDULED, and its detail page agrees.
    const res = await request.get('/task/');
    const tasks = (await res.json()) as Array<{ id: string; state: string }>;
    expect(tasks).toHaveLength(1);
    expect(tasks[0].state).toBe('SCHEDULED');

    const row = page.getByRole('row').filter({ hasText: tasks[0].id.slice(0, 8) });
    await expect(row.getByText('SCHEDULED')).toBeVisible();

    await row.getByRole('link').first().click();
    await expect(
      page.getByRole('heading', { name: 'Task Detail', exact: true }),
    ).toBeVisible();
    await expect(page.getByRole('cell', { name: 'SCHEDULED', exact: true })).toBeVisible();
    await expect(page.getByRole('cell', { name: 'Scheduled For' })).toBeVisible();
  });

  test('a repeating schedule creates a RECURRING template', async ({
    page,
    request,
  }) => {
    await openNewTaskForm(page);
    await page.getByLabel(/new task type/i).selectOption({ label: 'echo_task' });

    await page.getByLabel('Schedule task?').check();
    await page.getByLabel('Repeating', { exact: true }).check();
    const cron = page.getByLabel('Repeat on this cron schedule');
    await cron.pressSequentially('*/5 * * * *');
    await expect(page.locator('#schedule-preview')).toContainText(
      'Every 5 minutes',
    );

    await page.getByRole('button', { name: /launch task/i }).click();

    const flash = page.getByRole('status');
    await expect(flash).toContainText(/task scheduled/i);
    await expect(flash).toContainText('RECURRING - Every 5 minutes');

    const res = await request.get('/task/');
    const tasks = (await res.json()) as Array<{
      id: string;
      state: string;
      cronExpr: string;
      scheduleDescription: string;
    }>;
    expect(tasks).toHaveLength(1);
    expect(tasks[0].state).toBe('RECURRING');
    expect(tasks[0].cronExpr).toBe('*/5 * * * *');
    expect(tasks[0].scheduleDescription).toBe('Every 5 minutes');

    const row = page.getByRole('row').filter({ hasText: tasks[0].id.slice(0, 8) });
    await expect(row.getByText('RECURRING')).toBeVisible();

    await row.getByRole('link').first().click();
    await expect(page.getByRole('cell', { name: 'RECURRING', exact: true })).toBeVisible();
    await expect(page.getByRole('cell', { name: '*/5 * * * *' })).toBeVisible();
  });

  test('a start time in the past is rejected with a visible error', async ({
    page,
    request,
  }) => {
    await openNewTaskForm(page);
    await page.getByLabel(/new task type/i).selectOption({ label: 'echo_task' });

    await page.getByLabel('Schedule task?').check();
    await page
      .getByLabel('Start no earlier than')
      .fill(datetimeLocalValue(-2 * 60 * 60 * 1000));
    await page.getByRole('button', { name: /launch task/i }).click();

    // The form stays open with the error next to the field it's about.
    const error = page.locator('#task-form-error').getByRole('alert');
    await expect(error).toContainText(/in the past/i);
    await expect(page.getByLabel('Start no earlier than')).toBeVisible();

    const res = await request.get('/task/');
    expect(await res.json()).toHaveLength(0);
  });

  test('an unscheduled task still submits and runs right away', async ({
    page,
    request,
  }) => {
    await openNewTaskForm(page);
    await page.getByLabel(/new task type/i).selectOption({ label: 'echo_task' });

    // "Schedule task?" left unchecked — the fields are hidden, and their
    // values (if any) must not reach the server.
    await page.getByRole('button', { name: /launch task/i }).click();

    // Wait for the swapped-in row rather than racing the POST.
    await expect(
      page.getByRole('cell', { name: 'echo_task' }).first(),
    ).toBeVisible();

    const res = await request.get('/task/');
    const tasks = (await res.json()) as Array<{
      state: string;
      scheduleDescription: string;
    }>;
    expect(tasks).toHaveLength(1);
    // It went straight onto the queue — no schedule of its own. (Asserted
    // as "not scheduled" rather than "WAITING" so a worker draining the
    // queue alongside the suite can't make this flaky.)
    expect(['SCHEDULED', 'RECURRING']).not.toContain(tasks[0].state);
    expect(tasks[0].scheduleDescription).toBe('');
  });
});
