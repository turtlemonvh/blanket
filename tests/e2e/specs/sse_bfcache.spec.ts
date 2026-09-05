// Regression journey for issue #103: SSE streams surviving in the browser's
// back/forward cache used to exhaust the six-connections-per-host limit, so
// after a few tab switches the freshly loaded page's own SSE stream (and its
// htmx partial fetches) sat pending for ~30s and the app looked hung.
//
// The fix is server/ui/static/sse-lifecycle.js: close every `[sse-connect]`
// element's EventSource on `pagehide`, reopen it on a `pageshow` restore.
//
// Two tests here, doing different jobs:
//
//   1. "pagehide closes the SSE stream…" is a real regression test. It fires
//      the two page-transition events by hand and watches the network, which
//      needs no bfcache at all; it fails if sse-lifecycle.js is missing or
//      broken.
//   2. "repeated Tasks/Workers navigation…" is a smoke check only -- see
//      below.
//
// IMPORTANT: this file does *not* reproduce the original hang. Playwright launches
// Chromium with `--disable-back-forward-cache` in its default args (see
// playwright-core's chromiumSwitches.js), and the `chromium-bfcache` project
// in playwright.config.ts drops that switch via
// `launchOptions.ignoreDefaultArgs` -- but Chrome still declines to cache the
// page, because a CDP client is attached. Measured on Playwright 1.59.1 /
// bundled Chromium: after a back navigation the previous document's JS
// context is gone, and `performance.getEntriesByType('navigation')[0]
// .notRestoredReasons` reports `[{reason: "masked"}]` (Chrome hides
// embedder-level reasons). Adding `--enable-features=BackForwardCache` and
// disabling `BackForwardCacheMemoryControls` does not change that.
//
// So the second test is a navigation-cycle smoke check: it proves the
// lifecycle script is on the page and that a page reached after several tab
// switches still gets its SSE stream and its rows partial promptly. Its
// bfcache probe is kept because it is cheap and self-reporting -- if a future
// Chromium does restore the page, the annotation flips to "active" and the
// same assertions become a real end-to-end reproduction with no edit needed.
//
// Manual verification of the actual fix (Chrome 152, DevTools open):
// switch Tasks <-> Workers half a dozen times; with the fix nothing stays
// `(pending)` in the Network tab and `ss -tn | grep :8773` stops growing.

import { test, expect } from '@playwright/test';

const skipBrowser = process.env.SKIP_BROWSER_TESTS === '1';

/**
 * Navigate away and back, and report whether the original document survived
 * (i.e. was restored from the back/forward cache rather than re-fetched).
 */
async function bfcacheIsActive(page: import('@playwright/test').Page): Promise<boolean> {
  await page.goto('/ui/');
  await page.evaluate(() => {
    (window as unknown as Record<string, unknown>).__bfcacheProbe = 'alive';
  });
  await page.goto('/ui/workers');
  await page.goBack();
  return page.evaluate(
    () => (window as unknown as Record<string, unknown>).__bfcacheProbe === 'alive',
  );
}

test.describe('SSE lifecycle across navigations', () => {
  test.skip(skipBrowser, 'SKIP_BROWSER_TESTS=1');

  // The browser won't hand us a bfcache freeze/restore, but the two events it
  // fires when it does one are dispatchable by hand -- and that is the whole
  // contract sse-lifecycle.js implements. Observed from the network side:
  // the tasks page's SSE request should end when the page is hidden and a
  // fresh one should be issued when it is restored.
  test('pagehide closes the SSE stream, a persisted pageshow reopens it', async ({
    page,
  }) => {
    const isTasksStream = (url: string) => url.includes('/ui/sse/tasks');
    const opened: string[] = [];
    const ended: string[] = [];

    page.on('request', (r) => {
      if (isTasksStream(r.url())) opened.push(r.url());
    });
    page.on('requestfinished', (r) => {
      if (isTasksStream(r.url())) ended.push('finished');
    });
    page.on('requestfailed', (r) => {
      if (isTasksStream(r.url())) ended.push('failed');
    });

    await page.goto('/ui/');

    // htmx opens the stream once the table element is processed.
    await expect.poll(() => opened.length, { timeout: 5000 }).toBe(1);
    // …and it stays open: an SSE response only "finishes" when it is closed.
    expect(ended).toHaveLength(0);

    await page.evaluate(() => {
      window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true }));
    });
    await expect
      .poll(() => ended.length, { timeout: 5000 })
      .toBeGreaterThan(0);

    await page.evaluate(() => {
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }));
    });
    await expect.poll(() => opened.length, { timeout: 5000 }).toBe(2);

    // A non-persisted pageshow is an ordinary load, not a restore; reopening
    // there would double up the connections we just worked to free.
    await page.evaluate(() => {
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: false }));
    });
    await page.waitForTimeout(500);
    expect(opened).toHaveLength(2);
  });

  test('repeated Tasks/Workers navigation leaves the SSE stream connectable', async ({
    page,
  }, testInfo) => {
    const cached = await bfcacheIsActive(page);
    testInfo.annotations.push({
      type: 'bfcache',
      description: cached
        ? 'active — this run reproduces issue #103 without the fix'
        : 'inactive — degraded to a navigation-cycle smoke check',
    });

    // The lifecycle script has to be on the page for the fix to be in play.
    await page.goto('/ui/');
    await expect(
      page.locator('script[src="/ui/static/sse-lifecycle.js"]'),
    ).toHaveCount(1);

    // Walk the tabs enough times to fill the bfcache. Each cached page used to
    // keep its own EventSource open; six of them saturate Chrome's per-host
    // HTTP/1.1 connection pool.
    for (let i = 0; i < 3; i++) {
      await page.getByRole('link', { name: 'Workers' }).click();
      await expect(
        page.getByRole('heading', { name: 'Workers', exact: true }),
      ).toBeVisible();

      await page.getByRole('link', { name: 'Tasks' }).click();
      await expect(
        page.getByRole('heading', { name: 'Tasks', exact: true }),
      ).toBeVisible();
    }

    // One more hop, this time watching for the rows partial. The tasks table
    // fetches /ui/partials/tasks-rows off the SSE `tasks-changed` event, and
    // the server sends one immediately on connect — so this response only
    // arrives once the page has actually got a connection. That is exactly
    // what used to be stuck behind the bfcached pages' streams.
    await page.getByRole('link', { name: 'Workers' }).click();
    await expect(
      page.getByRole('heading', { name: 'Workers', exact: true }),
    ).toBeVisible();

    const rowsLoaded = page.waitForResponse(
      (res) => res.url().includes('/ui/partials/tasks-rows') && res.status() === 200,
      { timeout: 5000 },
    );
    await page.getByRole('link', { name: 'Tasks' }).click();
    await rowsLoaded;
  });
});
