import { expect, test } from '@playwright/test';

import { mockAppsBackend } from './appsMocks';

// A desktop page load warms Monaco from the jsdelivr CDN (lib/monacoManager).
// When that script cannot load — the CDN is down, or its response is blocked,
// as it was on a CI runner (net::ERR_BLOCKED_BY_RESPONSE) — the page carries
// on without an editor and raises no uncaught error. It used to raise an
// unhandled rejection whose value was the script's DOM `Event`
// (@monaco-editor/loader's makeCancelable; frontends/web/patches), which
// failed every afterEach guard on a desktop route the day the CDN misbehaved.

test('a desktop page whose Monaco CDN is unreachable raises no uncaught error', async ({ page }) => {
  const errors: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));
  let cdnRequested = false;
  await page.route('https://cdn.jsdelivr.net/npm/monaco-editor@*/**', (route) => {
    cdnRequested = true;
    return route.abort('blockedbyresponse');
  });
  await mockAppsBackend(page, { person: true });

  await page.goto('/apps');
  await expect(page.getByTestId('app-row-storefront')).toBeVisible({ timeout: 15_000 });
  await expect.poll(() => cdnRequested, { message: 'the page warmed Monaco from the CDN' }).toBe(true);
  // The failed load is reported asynchronously after the script's error event.
  await page.evaluate(() => new Promise((resolve) => setTimeout(resolve, 500)));
  expect(errors, 'no uncaught JavaScript errors').toEqual([]);
});
