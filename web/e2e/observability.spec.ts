import { test, expect, type Page } from '@playwright/test';

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
  const present = await panel.waitFor({ timeout: 20_000 }).then(() => true, () => false);
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

  test('dashboards draw forge per-rpc RED panels', async ({ page }) => {
    await openHarness(page, '?view=dashboards');
    await expect(page.getByTestId('obs-panel-requests').getByTestId('obs-panel-number')).toBeVisible();
    await expect(page.getByTestId('obs-panel-sessions').getByTestId('obs-panel-unsupported')).toContainText('count_distinct');
    await expect(page.getByTestId('obs-panel-red-by-service').getByRole('table')).toContainText('worker');
    await shot(page, 'dashboards');

    await page.getByTestId('obs-dashboard-picker').selectOption('service-api');
    await expect(page.getByTestId('obs-panel-red-by-rpc').getByRole('table')).toContainText('orders.v1.OrdersService/CreateOrder');
    // Per-rpc latency over time is a real line now (QueryPanel), drawn on a canvas.
    const latency = page.getByTestId('obs-panel-rpc-CreateOrder-latency');
    await expect(latency.locator('canvas').first()).toBeVisible();
    await expect(latency.locator('figcaption')).toContainText('p95: latest');
    await shot(page, 'dashboards-service');
  });

  test('the project area compares environments, edits a dashboard in place and saves it as code', async ({ page }) => {
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

    // Dashboards: grouped series, an in-place edit, then Save as code.
    await page.getByTestId('obs-view-dashboards').click();
    await page.getByTestId('obs-panel-edit-request-rate').click();
    await page.getByTestId('obs-editor-title').fill('Requests by environment');
    await page.getByTestId('obs-editor-apply').click();
    await expect(page.getByTestId('obs-panel-request-rate')).toHaveAttribute('data-edited', 'true');
    await expect(page.getByTestId('obs-dashboard-changed')).toContainText('1 panel edited');
    await page.getByTestId('obs-save-as-code-open').click();
    await expect(page.getByTestId('obs-save-path')).toHaveText('deploy/observability/dashboards/shop-overview.json');
    await expect(page.getByTestId('obs-save-json')).toContainText('"version": "forge.dashboards/v1"');
    await expect(page.getByTestId('obs-save-json')).toContainText('Requests by environment');
    await page.keyboard.press('Escape');

    // Workloads: customer metadata, and one workload across environments.
    await page.getByTestId('obs-view-workloads').click();
    await expect(page.getByTestId('obs-workload-prod-api').getByTestId('obs-exit-reason')).toHaveText('OOMKilled');
    await page.getByTestId('obs-workload-prod-api').getByRole('button', { name: 'api' }).click();
    await expect(page.getByTestId('obs-workload-detail')).toBeVisible();
    await expect(page.getByTestId('obs-workload-events')).toContainText('OOMKilling');

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
