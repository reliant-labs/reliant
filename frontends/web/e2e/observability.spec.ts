import { test, expect, type Page } from '@playwright/test';
import { devHarnessPresent } from './devHarness';

// Smoke for the console's Observability views, against the dev-only fixture
// harnesses (/forge-observability-preview, /forge-dashboards-preview): the
// real console's pages on their real clients, over the shop fixture pack
// (src/fixtures/shop). No backend, no session. The pack refuses every write,
// so these walk what a reader sees; the write flows are the component tests'.
//
// The harness only exists in a DEV build (DevOnlyRoute), so against CI's
// `vite preview` of a production build the route redirects to / and this
// suite skips itself rather than failing on a page that is correctly absent.
//
// Set OBS_SCREENSHOTS=<dir> to also write one screenshot per view.

const shotDir = process.env.OBS_SCREENSHOTS;

async function openHarness(page: Page, query = '', ready = 'obs-panel') {
  await page.goto(`/forge-observability-preview${query}`);
  const present = await devHarnessPresent(page, '/forge-observability-preview', page.getByTestId(ready));
  test.skip(!present, 'the fixture harness is a dev-build-only route');
}

async function shot(page: Page, name: string) {
  if (shotDir) await page.screenshot({ path: `${shotDir}/${name}.png`, fullPage: false });
}

test.describe('observability console', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test('traces → the slowest request → its waterfall', async ({ page }) => {
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));

    await openHarness(page, '?view=traces', 'lead-traces');
    const slowest = page.getByTestId('lead-slowest-trace');
    // Which request is slowest moves with the clock; the incident's failure does not.
    await expect(slowest).toContainText('context deadline exceeded');
    await expect(page.getByTestId('lead-trace-row').first()).toBeVisible();
    await shot(page, 'traces');

    await page.getByTestId('lead-slowest-trace-open').click();
    await expect(page.getByTestId('trace-detail')).toBeVisible();
    const tree = page.getByRole('tree', { name: 'Spans of this trace' });
    await expect(tree.getByRole('treeitem').first()).toBeVisible();
    await shot(page, 'trace');

    expect(errors).toEqual([]);
  });

  test('native dashboards: the index, a dashboard, compare with the previous period', async ({ page }) => {
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));

    await page.goto('/forge-dashboards-preview');
    const link = page.getByRole('link', { name: 'Payment provider' }).first();
    const present = await devHarnessPresent(page, '/forge-dashboards-preview', link);
    test.skip(!present, 'the fixture harness is a dev-build-only route');
    await shot(page, 'dashboards');

    await link.click();
    await expect(page.getByTestId('dashboard-header')).toContainText('Payment provider');
    await page.getByTestId('dashboard-compare').getByRole('button', { name: 'Previous period' }).click();
    await expect(page.getByTestId('obs-panel-change').first()).toContainText('vs previous period');
    await shot(page, 'dashboard');

    expect(errors).toEqual([]);
  });

  test('alerts: what fires, and the rule as code', async ({ page }) => {
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));

    await openHarness(page, '?view=alerts');
    await expect(page.getByTestId('obs-panel')).toHaveAttribute('data-view', 'alerts');
    const firing = page.getByTestId('obs-alert-OrdersErrorRateHigh');
    await expect(firing).toHaveAttribute('data-state', 'firing');
    await expect(firing.getByTestId('obs-rule-source')).toBeVisible();
    await shot(page, 'alerts-firing');

    await page.getByTestId('obs-alerts-tab-rules').click();
    await page.getByTestId('obs-rule-prod/OrdersErrorRateHigh').click();
    await expect(page.getByTestId('obs-rule-as-code')).toContainText('forge.AlertRule {');

    expect(errors).toEqual([]);
  });

  test('alerting that is not running yet still shows the declared rules', async ({ page }) => {
    await openHarness(page, '?view=alerts&case=alerting-not-configured');
    await expect(page.getByTestId('obs-alerting-not-configured')).toContainText(/already declare \d+ rules?/);
    await expect(page.getByTestId('obs-notifications')).toHaveCount(0);
    await page.getByTestId('obs-alerting-not-configured').getByRole('button', { name: 'See the rules' }).click();
    await expect(page.getByTestId('obs-rules')).toBeVisible();
  });

  test('errors: the incident, opened from the inbox, with its events', async ({ page }) => {
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));

    await openHarness(page, '?view=errors', 'lead-errors');
    await expect(page.getByTestId('lead-featured-issue')).toBeVisible();
    await shot(page, 'errors');
    await page.getByTestId('lead-featured-issue-open').click();
    const issue = page.getByTestId('obs-issue');
    await expect(issue).toHaveAttribute('data-state', 'ready');
    await expect(page.getByTestId('obs-issue-header')).toBeVisible();
    await expect(page.getByTestId('obs-issue-event').first()).toBeAttached();
    await shot(page, 'issue');

    expect(errors).toEqual([]);
  });

  test('an org without observability gets the set-up explanation, once', async ({ page }) => {
    await openHarness(page, '?view=logs&case=not-set-up', 'obs-not-set-up');
    await expect(page.getByTestId('obs-not-set-up')).toHaveCount(1);
    await expect(page.getByTestId('obs-not-set-up')).toContainText("isn't set up for this organization yet");
    await page.getByRole('link', { name: 'Profiles', exact: true }).click();
    await expect(page.getByTestId('obs-panel')).toHaveAttribute('data-view', 'profiles');
    await expect(page.getByTestId('obs-not-set-up')).toHaveCount(1);
    await shot(page, 'not-set-up');
  });
});
