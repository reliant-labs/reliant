import { test, expect, type Page } from '@playwright/test';
import { devHarnessPresent } from './devHarness';

// Smoke for the Observability tab of an environment page, against the
// dev-only fixture harness (/forge-observability-preview): the real
// ObservabilityPanel inside the real forge shell, fed by the in-memory
// ObservabilityApi in components/Forge/Observability/__fixtures__. No backend,
// no session.
//
// The harness only exists in a DEV build (DevOnlyRoute), so against CI's
// `vite preview` of a production build the route redirects to / and this
// suite skips itself rather than failing on a page that is correctly absent.
//
// Set OBS_SCREENSHOTS=<dir> to also write one screenshot per view.

const shotDir = process.env.OBS_SCREENSHOTS;

async function openHarness(page: Page, query = '') {
  await page.goto(`/forge-observability-preview${query}`);
  const panel = page.getByTestId('obs-panel');
  const present = await devHarnessPresent(page, '/forge-observability-preview', panel);
  test.skip(!present, 'the fixture harness is a dev-build-only route');
}

async function shot(page: Page, name: string) {
  if (shotDir) await page.screenshot({ path: `${shotDir}/${name}.png`, fullPage: false });
}

test.describe('observability console', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test('overview → service → logs → trace waterfall → profiles', async ({ page }) => {
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));

    await openHarness(page);
    const overview = page.getByTestId('obs-overview');
    await expect(overview.getByRole('button', { name: 'api' })).toBeVisible();
    await expect(overview.getByTestId('obs-operator-badge')).toHaveText('daemon');
    // Sparklines draw on canvas; their numbers are in the figure captions.
    await expect(page.locator('canvas').first()).toBeVisible();
    await shot(page, 'overview');

    await overview.getByRole('button', { name: 'api' }).click();
    await expect(page.getByTestId('obs-service-detail')).toBeVisible();
    await expect(page.getByTestId('obs-service-latency')).toContainText('p95');

    // Logs, filtered to the service the reader came from.
    await page.getByRole('button', { name: 'Logs', exact: true }).click();
    await expect(page.getByTestId('obs-panel')).toHaveAttribute('data-view', 'logs');
    await expect(page.getByTestId('obs-service-filter')).toHaveValue('api');
    const firstRow = page.getByTestId('obs-log-row').first();
    await firstRow.click();
    await expect(firstRow).toHaveAttribute('aria-expanded', 'true');
    await expect(page.getByTestId('obs-log-details')).toContainText('rpc.method');
    // Keyboard: the list is one tab stop; arrows move between rows.
    await page.keyboard.press('ArrowDown');
    await expect(page.getByTestId('obs-log-row').nth(1)).toBeFocused();
    await shot(page, 'logs');

    // A log's trace id opens that trace's waterfall.
    await page.getByTestId('obs-log-open-trace').first().click();
    await expect(page.getByTestId('trace-detail')).toBeVisible();
    await expect(page.getByTestId('trace-waterfall-summary')).toContainText('9 spans');
    const tree = page.getByRole('tree', { name: 'Spans of this trace' });
    await expect(tree.getByRole('treeitem')).toHaveCount(9);
    await tree.getByRole('treeitem').nth(3).click();
    await expect(page.getByTestId('span-details')).toContainText('payment provider timeout');
    // Collapse the failing call's children from the keyboard (tree pattern).
    await page.keyboard.press('ArrowLeft');
    await expect(tree.getByRole('treeitem')).toHaveCount(7);
    await shot(page, 'trace');

    await page.getByTestId('span-flamegraph').click();
    await expect(page.getByTestId('obs-panel')).toHaveAttribute('data-view', 'profiles');
    await expect(page.getByTestId('obs-top-functions')).toContainText('syscall.Syscall');
    const frame = page.frameLocator('[data-testid=obs-speedscope]');
    await expect(frame.getByText('api CPU')).toBeVisible({ timeout: 15_000 });
    await shot(page, 'profiles');

    expect(errors).toEqual([]);
  });

  test('native dashboards: the home, the index, a dashboard with compare and drill-down', async ({ page }) => {
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));

    await page.goto('/forge-dashboards-preview');
    const home = page.getByTestId('observability-home');
    const present = await devHarnessPresent(page, '/forge-dashboards-preview', home);
    test.skip(!present, 'the fixture harness is a dev-build-only route');
    await expect(page.getByTestId('observability-strip')).toContainText('Requests');
    await expect(page.getByTestId('observability-services')).toContainText('Crash-looping');
    await shot(page, 'observability-home');

    await page.getByRole('link', { name: 'Your dashboards' }).click();
    await expect(page.getByTestId('dashboards-starred')).toContainText('Checkout latency');
    await shot(page, 'dashboards');

    await page.getByTestId('dashboards-starred').getByRole('link', { name: 'Checkout latency' }).click();
    await expect(page.getByTestId('dashboard-header')).toContainText('v4');
    await page.getByTestId('dashboard-compare').getByRole('button', { name: 'Previous period' }).click();
    await expect(page.getByTestId('obs-panel-change').first()).toContainText('vs previous period');
    await page.getByTestId('dashboard-panel-latency-by-route').getByTestId('obs-panel-legend').getByRole('button').first().click();
    await expect(page.getByTestId('dashboard-drill')).toContainText('View traces');
    await shot(page, 'dashboard');

    expect(errors).toEqual([]);
  });

  test('the project area compares environments and filters by attribute', async ({ page }) => {
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));

    await openHarness(page, '?place=project');
    await expect(page.getByTestId('obs-panel')).toHaveAttribute('data-place', 'project');
    await expect(page.getByTestId('obs-env-picker')).toContainText('All environments');

    // Group by environment: one overview row per service per environment.
    await page.getByTestId('obs-add-group-by').click();
    await page.getByTestId('obs-key-deployment.environment.name').click();
    await expect(page.getByTestId('obs-service-api-prod')).toBeVisible();
    await expect(page.getByTestId('obs-service-api-staging')).toBeVisible();

    // A filter, with its value from autocomplete.
    await page.getByTestId('obs-add-filter').click();
    await page.getByTestId('obs-key-service.version').click();
    await page.getByTestId('obs-value-v1.8.3').click();
    await expect(page.getByTestId('obs-filter-chip')).toContainText('v1.8.3');

    expect(errors).toEqual([]);
  });

  test('alerts: what fires, silence it, the rule as code, a receiver a route names, notifications', async ({ page }) => {
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));

    await openHarness(page, '?place=project&view=alerts');
    await expect(page.getByTestId('obs-panel')).toHaveAttribute('data-view', 'alerts');
    const checkout = page.getByTestId('obs-alert-CheckoutP95High');
    await expect(checkout).toHaveAttribute('data-state', 'firing');
    await expect(checkout.getByTestId('obs-rule-source')).toHaveText('Your rule');
    // Attribute filters belong to telemetry views; the env picker stays.
    await expect(page.getByTestId('obs-env-picker')).toBeVisible();
    await expect(page.getByTestId('obs-add-filter')).toHaveCount(0);
    await shot(page, 'alerts-firing');

    // Silence it from its row; the dialog starts from the alert's own labels.
    await page.getByTestId('obs-alert-silence-CheckoutP95High').click();
    await page.getByTestId('obs-silence-comment').fill('rolling back checkout');
    await page.getByTestId('obs-silence-create').click();
    await expect(page.getByTestId('obs-alerts')).toHaveAttribute('data-tab', 'silences');
    await expect(page.getByTestId('obs-silence-sil-3')).toContainText('alertname="CheckoutP95High"');

    // The rule, and the KCL that declares it.
    await page.getByTestId('obs-alerts-tab-rules').click();
    await page.getByTestId('obs-rule-prod/CheckoutP95High').click();
    await expect(page.getByTestId('obs-rule-as-code')).toContainText('forge.AlertRule {');
    await expect(page.getByTestId('obs-rule-as-code')).toContainText('deploy/kcl/prod/main.k');

    // Receivers: staging's default route names "slack", which isn't set up yet.
    await page.getByTestId('obs-alerts-tab-receivers').click();
    await expect(page.getByTestId('obs-receiver-missing-slack')).toBeVisible();
    await page.getByTestId('obs-receiver-missing-slack').getByRole('button', { name: 'Set up slack' }).click();
    await page.getByTestId('obs-receiver-secret-input').fill('https://hooks.slack.com/services/T/B/x');
    await page.getByTestId('obs-receiver-save').click();
    await expect(page.getByTestId('obs-receiver-slack')).toContainText('Routed from staging');
    await expect(page.getByTestId('obs-receivers-missing')).toHaveCount(0);

    // The bell: unread in-app notifications, marked read.
    await expect(page.getByTestId('obs-notifications-unread')).toHaveText('2');
    await page.getByTestId('obs-notifications').click();
    await page.getByTestId('obs-notifications-mark-all').click();
    await expect(page.getByTestId('obs-notifications-unread')).toHaveCount(0);

    expect(errors).toEqual([]);
  });

  test('alerting that is not running yet still shows the declared rules', async ({ page }) => {
    await openHarness(page, '?place=project&view=alerts&case=alerting-not-configured');
    await expect(page.getByTestId('obs-alerting-not-configured')).toContainText('already declare 7 rules');
    await expect(page.getByTestId('obs-notifications')).toHaveCount(0);
    await page.getByTestId('obs-alerting-not-configured').getByRole('button', { name: 'See the rules' }).click();
    await expect(page.getByTestId('obs-rules')).toBeVisible();
  });

  test('release markers open a before/after comparison', async ({ page }) => {
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));

    // The env tab (prod): v1.8.3 was promoted 40 minutes ago, inside the default hour.
    await openHarness(page);
    const strip = page.getByTestId('obs-releases');
    await expect(strip).toHaveAttribute('data-status', 'ready');
    await page.getByTestId('obs-releases-toggle').click();
    await expect(strip.locator('li').first()).toContainText('v1.8.3');
    await expect(strip.locator('li').first()).toContainText('by Ada Lovelace');
    await strip.locator('li').first().getByRole('button', { name: 'Compare before / after' }).click();
    await expect(page.getByTestId('obs-compare')).toHaveAttribute('data-state', 'ready');
    await expect(page.getByTestId('obs-compare-header')).toContainText('Before and after v1.8.3 in prod');
    await expect(page.getByTestId('obs-compare-p95-api')).toHaveAttribute('data-movement', 'up-bad');
    await shot(page, 'release-compare');
    await page.getByTestId('obs-compare-back').click();
    await expect(page.getByTestId('obs-overview')).toBeVisible();

    // (DORA is the console's Releases page now — /forge-releases-preview, covered by its component tests.)
    expect(errors).toEqual([]);
  });

  test('an org without observability gets the set-up explanation, once', async ({ page }) => {
    await openHarness(page, '?case=not-set-up');
    await expect(page.getByTestId('obs-not-set-up')).toHaveCount(1);
    await expect(page.getByTestId('obs-not-set-up')).toContainText("isn't set up for this organization yet");
    await page.getByTestId('obs-view-logs').click();
    await expect(page.getByTestId('obs-not-set-up')).toHaveCount(1);
    await shot(page, 'not-set-up');
  });
});
