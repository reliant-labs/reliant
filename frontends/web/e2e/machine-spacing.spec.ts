import { expect, test } from '@playwright/test';
import { mockAppsBackend } from './appsMocks';

for (const daemonType of ['managed', 'self_hosted']) {
  test(`default badge and build have row clearance on a ${daemonType} machine`, async ({ page }) => {
    await mockAppsBackend(page);
    await page.route('**/reliant.v1.DaemonRegistryService/ListDaemons', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ daemons: [{
          daemonId: 'daemon_spacing', name: 'My machine', hostname: 'my-machine',
          daemonType, status: 'DAEMON_STATUS_ACTIVE', isDefault: true, version: 'v1.2.3',
          platform: 'linux', size: daemonType === 'managed' ? 'm' : '',
        }] }),
      }),
    );
    await page.goto('/settings/machines');
    const row = page.getByRole('row').filter({ has: page.getByText('My machine', { exact: true }) });
    const badge = row.getByText('Default', { exact: true });
    const build = row.getByText('v1.2.3', { exact: true });
    await expect(badge).toBeVisible();
    await expect(build).toBeVisible();

    const geometry = await row.evaluate((element) => {
      const rect = element.getBoundingClientRect();
      const spans = Array.from(element.querySelectorAll('span'));
      const badgeRect = spans.find((span) => span.textContent === 'Default')!.getBoundingClientRect();
      const buildRect = spans.find((span) => span.textContent === 'v1.2.3')!.getBoundingClientRect();
      return {
        aboveBadge: badgeRect.top - rect.top,
        belowBuild: rect.bottom - buildRect.bottom,
      };
    });
    expect(geometry.aboveBadge).toBeGreaterThanOrEqual(7);
    expect(geometry.belowBuild).toBeGreaterThanOrEqual(7);
  });
}
