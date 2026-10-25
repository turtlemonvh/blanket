// Docs journey (turtlemonvh/blanket#234): reach the built-in docs from the
// nav, search them, and follow a hit to the section it names.
//
// Written against whatever docs/*.md the binary embeds -- no heading is
// named here, because the docs are edited far more often than this spec.
// "worker" is a word every revision of blanket's docs will contain.

import { test, expect } from '@playwright/test';

const skipBrowser = process.env.SKIP_BROWSER_TESTS === '1';

test.describe('Docs', () => {
  test.skip(skipBrowser, 'SKIP_BROWSER_TESTS=1');

  test('open docs, search, follow a result to its section', async ({ page }) => {
    await page.goto('/ui/');
    await page.getByRole('link', { name: 'Docs', exact: true }).click();
    await expect(page).toHaveURL(/\/ui\/docs$/);
    await expect(page.getByRole('heading', { name: 'Docs', exact: true })).toBeVisible();

    // The page list links to the embedded pages.
    const pages = page.getByRole('main').getByRole('list').last().getByRole('link');
    expect(await pages.count()).toBeGreaterThan(1);

    // Live search: typing fills the results without a page load.
    await page.getByLabel('Search the docs').fill('worker');
    const results = page.getByRole('list', { name: 'search results' });
    await expect(results).toBeVisible();
    await expect(page).toHaveURL(/\/ui\/docs$/);

    // Follow the first hit whose link names a section, and land on it.
    const hit = results.locator('a[href*="#"]').first();
    const href = await hit.getAttribute('href');
    expect(href).toMatch(/^\/ui\/docs\/[a-z]+#.+/);
    const heading = (await hit.textContent())!.trim();
    await hit.click();

    await expect(page).toHaveURL(href!);
    const anchor = decodeURIComponent(href!.split('#')[1]);
    const target = page.locator(`[id="${anchor}"]`);
    await expect(target).toBeVisible();
    await expect(target).toBeInViewport();
    await expect(target).toContainText(heading);
  });

  test('a search with no matches says so', async ({ page }) => {
    await page.goto('/ui/docs?q=zzqxnotaword');
    await expect(page.getByText(/No sections match/)).toBeVisible();
  });
});
