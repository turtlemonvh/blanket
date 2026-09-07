// Journey-level UI tests for a task's *outcome* surfaces
// (turtlemonvh/blanket#104): the exit code in the task rows and on the
// detail page, the parsed `result_file` artifact, and the
// stdout / stderr / both log toggle.
//
// Same conventions as journeys.spec.ts: drive by role/text, assert with
// expect(locator) rather than fixed waits, clean up via the API.
//
// The browser suite runs a server with no worker, so nothing here actually
// executes a task. Everything a worker would have left behind is seeded
// through the API instead: the log files and the result artifact ride along
// with the submission as multipart uploads (POST /task/ writes each named
// file part into the new task's result dir), and the terminal state plus
// exit code come from the worker-facing PUT /task/:id/finish.

import { test, expect, Page } from '@playwright/test';

const skipBrowser = process.env.SKIP_BROWSER_TESTS === '1';

type Api = import('@playwright/test').APIRequestContext;

const STDOUT = 'stdout line one\nstdout line two\n';
const STDERR = 'stderr line one\n';
const RESULT = '{"answer": 42, "label": "from the fixture"}';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Delete every task currently in the DB. Safe on an empty DB. */
async function purgeTasks(apiRequest: Api) {
  const res = await apiRequest.get('/task/');
  if (!res.ok()) return;
  const tasks = (await res.json()) as Array<{ id: string }>;
  for (const t of tasks) {
    await apiRequest.delete(`/task/${t.id}`);
  }
}

function textFile(name: string, body: string) {
  return { name, mimeType: 'text/plain', buffer: Buffer.from(body) };
}

/**
 * Submit a task, attaching the files a worker would have written, then
 * report it finished with the given exit code. Returns the task id.
 */
async function seedFinishedTask(
  apiRequest: Api,
  opts: {
    type: string;
    state: string;
    exitCode?: number;
    stdout?: string;
    stderr?: string;
    result?: string;
  },
): Promise<string> {
  const multipart: Record<string, unknown> = {
    data: JSON.stringify({ type: opts.type }),
  };
  if (opts.stdout !== undefined) {
    multipart['blanket.stdout.log'] = textFile('blanket.stdout.log', opts.stdout);
  }
  if (opts.stderr !== undefined) {
    multipart['blanket.stderr.log'] = textFile('blanket.stderr.log', opts.stderr);
  }
  if (opts.result !== undefined) {
    multipart['result.json'] = textFile('result.json', opts.result);
  }

  const created = await apiRequest.post('/task/', { multipart: multipart as never });
  expect(created.status()).toBe(201);
  const id = (await created.json()).id as string;

  const query =
    opts.exitCode === undefined
      ? `state=${opts.state}`
      : `state=${opts.state}&exitCode=${opts.exitCode}`;
  const finished = await apiRequest.put(`/task/${id}/finish?${query}`);
  expect(finished.status()).toBe(200);

  return id;
}

/** The Tasks-list row for one task, matched on its full id (not the prefix). */
function rowFor(page: Page, taskId: string) {
  return page
    .getByRole('row')
    .filter({ has: page.locator(`a[href="/ui/tasks/${taskId}"]`) });
}

/** One of the log pane's toggle buttons. */
function logToggle(page: Page, label: 'stdout' | 'stderr' | 'both') {
  return page.getByRole('button', { name: `show ${label}` });
}

const logPane = (page: Page) => page.locator('#task-log');

// ---------------------------------------------------------------------------
// Exit code
// ---------------------------------------------------------------------------

test.describe('Exit code', () => {
  test.skip(skipBrowser, 'SKIP_BROWSER_TESTS=1');

  test.beforeEach(async ({ request }) => {
    await purgeTasks(request);
  });
  test.afterEach(async ({ request }) => {
    await purgeTasks(request);
  });

  test('shows in the task row and on the detail page', async ({ page, request }) => {
    const failed = await seedFinishedTask(request, {
      type: 'echo_task',
      state: 'ERROR',
      exitCode: 3,
    });

    await page.goto('/ui/');
    await expect(page.getByRole('columnheader', { name: 'Exit' })).toBeVisible();
    await expect(rowFor(page, failed).getByText('3', { exact: true })).toBeVisible();

    await page.goto(`/ui/tasks/${failed}`);
    const exitRow = page
      .getByRole('row')
      .filter({ has: page.getByRole('cell', { name: 'Exit Code', exact: true }) });
    await expect(exitRow.getByText('3', { exact: true })).toBeVisible();
  });

  test('a task with no exit status shows a dash, not a zero', async ({
    page,
    request,
  }) => {
    // Cancelling a WAITING task moves it to STOPPED: terminal, but with no
    // process to report an exit status for.
    const created = await request.post('/task/', { data: { type: 'echo_task' } });
    expect(created.status()).toBe(201);
    const id = (await created.json()).id as string;
    expect((await request.put(`/task/${id}/cancel`)).status()).toBe(200);

    await page.goto(`/ui/tasks/${id}`);
    const exitRow = page
      .getByRole('row')
      .filter({ has: page.getByRole('cell', { name: 'Exit Code', exact: true }) });
    await expect(exitRow).toContainText('—');
    await expect(exitRow.getByText('0', { exact: true })).toHaveCount(0);
  });
});

// ---------------------------------------------------------------------------
// Result artifact
// ---------------------------------------------------------------------------

test.describe('Result artifact', () => {
  test.skip(skipBrowser, 'SKIP_BROWSER_TESTS=1');

  test.beforeEach(async ({ request }) => {
    await purgeTasks(request);
  });
  test.afterEach(async ({ request }) => {
    await purgeTasks(request);
  });

  test('the parsed result_file is rendered on the detail page', async ({
    page,
    request,
  }) => {
    const id = await seedFinishedTask(request, {
      type: 'ui_result_task',
      state: 'SUCCESS',
      exitCode: 0,
      result: RESULT,
    });

    await page.goto(`/ui/tasks/${id}`);
    await expect(page.getByRole('heading', { name: 'Result', exact: true })).toBeVisible();
    await expect(page.getByRole('link', { name: 'raw file' })).toHaveAttribute(
      'href',
      `/results/${id}/result.json`,
    );

    // Pretty-printed, not the raw one-line file.
    const result = page.getByLabel('task result');
    await expect(result).toBeVisible();
    await expect(result).toContainText('"answer": 42');
    await expect(result).toContainText('from the fixture');
  });

  test('a task type with no result_file gets no result block', async ({
    page,
    request,
  }) => {
    const id = await seedFinishedTask(request, {
      type: 'echo_task',
      state: 'SUCCESS',
      exitCode: 0,
      stdout: STDOUT,
    });

    await page.goto(`/ui/tasks/${id}`);
    // The page rendered...
    await expect(page.getByRole('heading', { name: 'Live Log' })).toBeVisible();
    // ...without a result section.
    await expect(page.getByRole('heading', { name: 'Result', exact: true })).toHaveCount(0);
  });
});

// ---------------------------------------------------------------------------
// Log view toggle
// ---------------------------------------------------------------------------

test.describe('Log view toggle', () => {
  test.skip(skipBrowser, 'SKIP_BROWSER_TESTS=1');

  test.beforeEach(async ({ request }) => {
    await purgeTasks(request);
  });
  test.afterEach(async ({ request }) => {
    await purgeTasks(request);
  });

  test('switches between stdout, stderr and both', async ({ page, request }) => {
    const id = await seedFinishedTask(request, {
      type: 'echo_task',
      state: 'ERROR',
      exitCode: 1,
      stdout: STDOUT,
      stderr: STDERR,
    });

    await page.goto(`/ui/tasks/${id}`);

    // Default view: stdout, and stdout only.
    await expect(logToggle(page, 'stdout')).toHaveAttribute('aria-pressed', 'true');
    await expect(logPane(page)).toContainText('stdout line one');
    await expect(logPane(page)).not.toContainText('stderr line one');

    await logToggle(page, 'stderr').click();
    await expect(logToggle(page, 'stderr')).toHaveAttribute('aria-pressed', 'true');
    await expect(logToggle(page, 'stdout')).toHaveAttribute('aria-pressed', 'false');
    await expect(logPane(page)).toContainText('stderr line one');
    await expect(logPane(page)).not.toContainText('stdout line one');

    await logToggle(page, 'both').click();
    await expect(logToggle(page, 'both')).toHaveAttribute('aria-pressed', 'true');
    await expect(logPane(page)).toContainText('stdout line one');
    await expect(logPane(page)).toContainText('stderr line one');
    // Each line says which stream it came from.
    await expect(
      logPane(page).locator('.log-line-stderr', { hasText: 'stderr line one' }),
    ).toBeVisible();

    // And back to the default, which is still the raw stdout view.
    await logToggle(page, 'stdout').click();
    await expect(logToggle(page, 'stdout')).toHaveAttribute('aria-pressed', 'true');
    await expect(logPane(page)).not.toContainText('stderr line one');
  });

  test('a stream the task never wrote to says so', async ({ page, request }) => {
    const id = await seedFinishedTask(request, {
      type: 'echo_task',
      state: 'SUCCESS',
      exitCode: 0,
      stdout: STDOUT,
    });

    await page.goto(`/ui/tasks/${id}`);
    await logToggle(page, 'stderr').click();
    await expect(logPane(page)).toContainText('No stderr recorded for this task.');
  });
});

// ---------------------------------------------------------------------------
// The combined view of a *running* task
// ---------------------------------------------------------------------------

/**
 * Submit a task with the files a worker would have written, then walk it
 * to RUNNING through the worker-facing API so its log pane streams.
 * Returns the task and worker ids.
 */
async function seedRunningTask(
  apiRequest: Api,
  opts: { stdout: string; stderr: string },
): Promise<{ id: string; workerId: string }> {
  const workerId = require('crypto').randomBytes(12).toString('hex');
  const registered = await apiRequest.put(`/worker/${workerId}`, {
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
  expect(registered.status()).toBe(200);

  const created = await apiRequest.post('/task/', {
    multipart: {
      data: JSON.stringify({ type: 'echo_task' }),
      'blanket.stdout.log': textFile('blanket.stdout.log', opts.stdout),
      'blanket.stderr.log': textFile('blanket.stderr.log', opts.stderr),
    } as never,
  });
  expect(created.status()).toBe(201);
  const id = (await created.json()).id as string;

  const claimed = await apiRequest.post(`/task/claim/${workerId}`);
  expect(claimed.status()).toBe(200);
  expect((await claimed.json()).id).toBe(id);

  const running = await apiRequest.put(`/task/${id}/run`);
  expect(running.status()).toBe(200);

  return { id, workerId };
}

test.describe('Log view toggle on a running task', () => {
  test.skip(skipBrowser, 'SKIP_BROWSER_TESTS=1');

  let workerId = '';

  test.beforeEach(async ({ request }) => {
    await purgeTasks(request);
  });

  test.afterEach(async ({ request }) => {
    // A RUNNING task has to be finished before it can be deleted.
    const res = await request.get('/task/');
    if (res.ok()) {
      const tasks = (await res.json()) as Array<{ id: string; state: string }>;
      for (const t of tasks) {
        if (t.state === 'RUNNING' || t.state === 'CLAIMED') {
          await request.put(`/task/${t.id}/finish?state=SUCCESS&exitCode=0`);
        }
      }
    }
    await purgeTasks(request);
    if (workerId) {
      await request.put(`/worker/${workerId}/stop`);
      await request.delete(`/worker/${workerId}`);
      workerId = '';
    }
  });

  // The regression from the #104 review: sitting on `both` filled the pane,
  // but the history was lost every time you navigated away and back -- the
  // combined stream started tailing from wherever the two files happened to
  // be rather than replaying what they already held. It now replays both
  // files on connect, so coming back shows what staying put would have.
  test('coming back to both still shows the stderr history', async ({
    page,
    request,
  }) => {
    const seeded = await seedRunningTask(request, { stdout: STDOUT, stderr: STDERR });
    workerId = seeded.workerId;

    await page.goto(`/ui/tasks/${seeded.id}`);

    // A running task streams rather than printing a stored tail.
    await expect(logPane(page)).toHaveAttribute(
      'sse-connect',
      `/task/${seeded.id}/log`,
    );

    await logToggle(page, 'both').click();
    await expect(logPane(page)).toHaveAttribute(
      'sse-connect',
      `/ui/sse/tasks/${seeded.id}/log`,
    );
    await expect(
      logPane(page).locator('.log-line-stdout', { hasText: 'stdout line one' }),
    ).toBeVisible();
    await expect(
      logPane(page).locator('.log-line-stderr', { hasText: 'stderr line one' }),
    ).toBeVisible();

    // Away to each single-stream view...
    await logToggle(page, 'stdout').click();
    await expect(logPane(page)).toContainText('stdout line two');

    await logToggle(page, 'stderr').click();
    await expect(logPane(page)).toContainText('stderr line one');

    // ...and back. This is what used to come back with no stderr in it.
    await logToggle(page, 'both').click();
    await expect(
      logPane(page).locator('.log-line-stderr', { hasText: 'stderr line one' }),
    ).toBeVisible();
    await expect(
      logPane(page).locator('.log-line-stdout', { hasText: 'stdout line one' }),
    ).toBeVisible();

    // Replayed once, not once per reconnect.
    await expect(
      logPane(page).locator('.log-line', { hasText: 'stderr line one' }),
    ).toHaveCount(1);
    await expect(
      logPane(page).locator('.log-line', { hasText: 'stdout line two' }),
    ).toHaveCount(1);
  });
});
