import { test, expect, request } from '@playwright/test';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Fetch the list of available task types from the API. */
async function getTaskTypes(baseURL: string) {
  const ctx = await request.newContext();
  const res = await ctx.get(`${baseURL}/task_type/`);
  if (!res.ok()) {
    await ctx.dispose();
    return [];
  }
  const body = await res.json();
  await ctx.dispose();
  return Array.isArray(body) ? (body as Array<{ name: string }>) : [];
}

// ---------------------------------------------------------------------------
// Infrastructure smoke tests (API-only, no browser needed)
// ---------------------------------------------------------------------------

test.describe('API health', () => {
  test('GET /version returns name and version', async ({ request }) => {
    const res = await request.get('/version');
    expect(res.ok()).toBeTruthy();
    const body = await res.json();
    expect(body.name).toBe('blanket');
    expect(body.version).toBeTruthy();
  });

  test('GET /ops/status/ is reachable', async ({ request }) => {
    const res = await request.get('/ops/status/');
    expect(res.ok()).toBeTruthy();
  });

  test('GET /task/ returns a JSON array', async ({ request }) => {
    const res = await request.get('/task/');
    expect(res.ok()).toBeTruthy();
    const body = await res.json();
    expect(Array.isArray(body)).toBeTruthy();
  });

  test('GET /worker/ returns a JSON array', async ({ request }) => {
    const res = await request.get('/worker/');
    expect(res.ok()).toBeTruthy();
    const body = await res.json();
    expect(Array.isArray(body)).toBeTruthy();
  });

  test('/ui/ returns 200 and contains HTML', async ({ request }) => {
    const res = await request.get('/ui/');
    expect(res.ok()).toBeTruthy();
    const text = await res.text();
    // The embedded UI starts with <!doctype html> and sets a <title> prefixed
    // with "Blanket" for every page.
    expect(text.toLowerCase()).toContain('<!doctype html');
    expect(text).toContain('<title>Blanket');
  });
});

// ---------------------------------------------------------------------------
// UI smoke tests (require a working browser)
//
// These are skipped automatically when SKIP_BROWSER_TESTS=1 (useful in
// environments where we cannot install Chromium's system dependencies, e.g.
// sandboxed CI without sudo). API-only coverage above still runs.
// ---------------------------------------------------------------------------

const skipBrowser = process.env.SKIP_BROWSER_TESTS === '1';

test.describe('UI loads', () => {
  test.skip(skipBrowser, 'SKIP_BROWSER_TESTS=1');

  test('redirects / to /ui/', async ({ page }) => {
    await page.goto('/');
    await expect(page).toHaveURL(/\/ui\//);
  });

  test('page renders without unhandled JS errors', async ({ page }) => {
    const errors: string[] = [];
    page.on('pageerror', (err) => errors.push(err.message));

    await page.goto('/ui/');
    // Give AngularJS time to bootstrap
    await page.waitForTimeout(1000);

    expect(errors).toHaveLength(0);
  });

  test('task list section is visible', async ({ page }) => {
    await page.goto('/ui/');
    await page.waitForTimeout(500);
    // The AngularJS app renders a tasks section; look for any sign of it
    const body = await page.content();
    expect(body.toLowerCase()).toContain('task');
  });
});

// ---------------------------------------------------------------------------
// Task lifecycle via API (eval-like assertions)
// ---------------------------------------------------------------------------

test.describe('Task API lifecycle', () => {
  test('POST /task/ with missing type returns 400', async ({ request }) => {
    const res = await request.post('/task/', { data: {} });
    expect(res.status()).toBe(400);
  });

  test('POST /task/ with unknown type returns 400', async ({ request }) => {
    const res = await request.post('/task/', {
      data: { type: 'type_that_does_not_exist' },
    });
    expect(res.status()).toBe(400);
  });

  // This test only runs if the server has at least one task type configured.
  test('task round-trip: submit -> get -> cancel', async ({ request, baseURL }) => {
    const types = await getTaskTypes(baseURL!);
    if (types.length === 0) {
      test.skip(true, 'no task types configured on this server');
      return;
    }
    const typeName = types[0].name;

    // Submit
    const postRes = await request.post('/task/', { data: { type: typeName } });
    expect(postRes.status()).toBe(201);
    const task = await postRes.json();
    expect(task.state).toBe('WAITING');
    const taskId: string = task.id;

    // Fetch by ID
    const getRes = await request.get(`/task/${taskId}`);
    expect(getRes.ok()).toBeTruthy();
    const fetched = await getRes.json();
    expect(fetched.id).toBe(taskId);
    expect(fetched.state).toBe('WAITING');

    // Cancel it
    const cancelRes = await request.put(`/task/${taskId}/cancel`);
    expect(cancelRes.ok()).toBeTruthy();

    // Confirm state is STOPPED
    const afterCancel = await request.get(`/task/${taskId}`);
    const stopped = await afterCancel.json();
    expect(stopped.state).toBe('STOPPED');

    // Clean up
    await request.delete(`/task/${taskId}`);
  });
});
