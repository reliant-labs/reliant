import { test, expect, type Page } from '@playwright/test';
import { devHarnessPresent } from './devHarness';

// Smoke for the console's Overview and a workload's page, against the
// dev-only fixture harness (/forge-workloads-preview): the real OverviewHome
// and WorkloadDetail inside the real console chrome, fed by the fixtures in
// components/Forge/Workloads/__fixtures__. No backend, no session.
//
// The harness only exists in a DEV build (DevOnlyRoute), so against CI's
// `vite preview` of a production build the route redirects to / and this
// suite skips itself rather than failing on a page that is correctly absent.

async function openHarness(page: Page, query = '') {
  await page.goto(`/forge-workloads-preview${query}`);
  const overview = page.getByTestId('overview').or(page.getByTestId('workload-detail'));
  const present = await devHarnessPresent(page, '/forge-workloads-preview', overview);
  test.skip(!present, 'the fixture harness is a dev-build-only route');
}

test.describe('console overview and workload page', () => {
  test.use({ viewport: { width: 1440, height: 900 } });

  test('Overview → workload → Restart dialog (cancel) changes nothing', async ({ page }) => {
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));

    await openHarness(page, '?env=prod');
    await expect(page.getByTestId('overview')).toHaveAttribute('data-state', 'ready');
    await expect(page.getByTestId('overview-env-prod')).toHaveAttribute('aria-pressed', 'true');
    const worker = page.getByTestId('overview-service-reliant-temporal-worker');
    await expect(worker).toContainText('Crash-looping');

    // The whole row is the link to the workload, in its environment.
    await worker.getByRole('link', { name: 'reliant-temporal-worker' }).click();
    const detail = page.getByTestId('workload-detail');
    await expect(detail).toHaveAttribute('data-state', 'ready');
    await expect(detail.getByRole('heading', { level: 1 })).toHaveText('reliant-temporal-worker');

    // Restart names its environment, confirms, and Cancel leaves everything as it was.
    const restart = page.getByTestId('workload-restart');
    await expect(restart).toHaveText(/Restart in prod/);
    await restart.click();
    const dialog = page.getByRole('dialog', { name: 'Restart reliant-temporal-worker in prod?' });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Restart in prod' })).toBeVisible();
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(dialog).toBeHidden();
    await expect(page.getByTestId('workload-restart-requested')).toHaveCount(0);

    expect(errors).toEqual([]);
  });
});
