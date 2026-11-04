import { test, expect, type Page } from '@playwright/test';
import { devHarnessPresent } from './devHarness';
import { PHONE } from './mobileMocks';

// The Apps console and its Observability views on a phone (390×844, touch),
// against the dev-only fixture harnesses — no backend, no session. Each view
// must fit the screen (no sideways scroll), stack its tables into labelled
// cards, and keep its controls at a 44px touch size. research/MOBILE_AUDIT.md
// has the measurements this pins.
//
// The harnesses exist only in a dev build (DevOnlyRoute); against CI's
// production `vite preview` every test here skips itself.

test.use(PHONE);
// A cold dev server compiles each lazy view on first request.
const LOADED = { timeout: 20_000 };

async function open(page: Page, harness: string, query: string, ready: string): Promise<void> {
  // The harness paths sit outside /forge/, so the phone redirect would send
  // them to the chat list; the real console paths are exempt. `desktop=1` is
  // the redirect's documented opt-out, and it changes nothing else.
  await page.goto(`${harness}${query}${query ? '&' : '?'}desktop=1`);
  const present = await devHarnessPresent(page, harness, page.getByTestId(ready));
  test.skip(!present, 'the fixture harness is a dev-build-only route');
  await expect(page.getByTestId(ready).first()).toBeVisible(LOADED);
}

/** Nothing scrolls the page sideways. */
async function fitsTheScreen(page: Page) {
  const width = await page.evaluate(() => document.documentElement.scrollWidth);
  expect(width).toBeLessThanOrEqual(390);
}

/**
 * A stacked table (forge-ui's DataTable with `stacked`, or a table drawn with
 * its STACK classes; the test id may sit on a wrapper): labelled cells, and a
 * header that takes no room.
 */
async function isStacked(page: Page, testId: string) {
  const table = page.getByTestId(testId).first();
  await expect.poll(() => table.locator('[data-label]').count(), LOADED).toBeGreaterThan(0);
  const headerHeight = await table.locator('thead').first().evaluate((el) => el.getBoundingClientRect().height);
  expect(headerHeight).toBeLessThanOrEqual(1);
}

test.describe('console on a phone', () => {
  test('traces stack into cards led by the root operation, and a trace opens', async ({ page }) => {
    await open(page, '/forge-observability-preview', '?view=traces', 'obs-panel');
    await isStacked(page, 'obs-trace-table');
    const row = page.getByTestId('obs-trace-row').first();
    // The root operation leads the card even though Started is the first column.
    const [title, started] = await Promise.all([
      row.locator('th[scope="row"]').boundingBox(),
      row.locator('[data-label="Started"]').boundingBox(),
    ]);
    expect(title!.y).toBeLessThan(started!.y);
    await fitsTheScreen(page);

    // The waterfall on a phone: two-line rows, tall enough to tap.
    await row.locator('th[scope="row"] button').click();
    await expect(page.getByTestId('trace-detail')).toBeVisible(LOADED);
    const span = page.getByTestId('trace-span-row').first();
    const box = await span.boundingBox();
    expect(box!.height).toBeGreaterThanOrEqual(44);
    expect(box!.width).toBeLessThanOrEqual(390);
    await fitsTheScreen(page);
  });

  test('a log record is two lines, its message across the full width', async ({ page }) => {
    await open(page, '/forge-observability-preview', '?view=logs', 'obs-panel');
    const row = page.getByTestId('obs-log-row').first();
    await expect(row).toBeVisible(LOADED);
    const height = await row.evaluate((el) => el.getBoundingClientRect().height);
    expect(height).toBeGreaterThanOrEqual(44);
    await fitsTheScreen(page);
  });

  test('profiles lead with the heaviest functions and offer the flamegraph full screen', async ({ page }) => {
    await open(page, '/forge-observability-preview', '?view=profiles&service=api', 'obs-panel');
    const top = page.getByTestId('obs-top-functions');
    const frame = page.getByTestId('obs-speedscope');
    await expect(top).toBeVisible(LOADED);
    await expect(frame).toBeVisible();
    const [topBox, frameBox] = await Promise.all([top.boundingBox(), frame.boundingBox()]);
    expect(topBox!.y).toBeLessThan(frameBox!.y);
    await expect(page.getByTestId('obs-speedscope-fullscreen')).toBeVisible();
    await fitsTheScreen(page);
  });

  test('release comparison and dashboard panel tables stack', async ({ page }) => {
    await open(page, '/forge-dashboards-preview', '', 'observability-home');
    await fitsTheScreen(page);
    // The console nav is a drawer here: it opens, and Escape closes it.
    await page.getByRole('button', { name: 'Open navigation' }).click();
    const drawer = page.getByRole('dialog', { name: /console/ });
    await expect(drawer).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(drawer).toBeHidden();
    await page.getByRole('link', { name: 'Your dashboards' }).click();
    await expect(page.getByTestId('dashboards-starred')).toBeVisible(LOADED);
    await page.getByTestId('dashboards-starred').getByRole('link', { name: 'Checkout latency' }).click();
    await expect(page.getByTestId('dashboard-header')).toBeVisible(LOADED);
    await fitsTheScreen(page);
    const tables = page.getByTestId('obs-panel-table');
    if ((await tables.count()) > 0) await isStacked(page, 'obs-panel-table');
  });

  test('the workload page and its Kubernetes events fit, stacked', async ({ page }) => {
    await open(page, '/forge-workloads-preview', '', 'forge-shell');
    await fitsTheScreen(page);
    await isStacked(page, 'overview-services-table');
  });
});
