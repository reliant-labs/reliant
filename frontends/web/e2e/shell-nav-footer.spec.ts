import { expect, test } from '@playwright/test';

import { mockAppsBackend } from './appsMocks';

// The nav column sits under the macOS title strip (AppShell's ShellNav), so a
// column sized h-full was that strip taller than the window: the footer's
// Settings row hung off the bottom of a short window. jsdom has no layout, so
// this is measured in a real browser, as the desktop app on a Mac draws it.
for (const height of [560, 720]) {
  test(`the nav footer stays inside a ${height}px macOS window on Settings`, async ({ page }) => {
    await page.setViewportSize({ width: 1280, height });
    await mockAppsBackend(page);
    await page.addInitScript(() => {
      // The title strip is drawn only for the desktop app on macOS (useTitleBarChrome).
      // Every other bridge call is inert: a subscription returns its unsubscribe, a call resolves empty.
      const bridge = new Proxy({} as Record<string | symbol, unknown>, {
        get: (_, key) => {
          if (key === 'platform') return 'darwin';
          if (typeof key !== 'string' || key === 'then') return undefined;
          return () => (key.startsWith('on') ? () => {} : Promise.resolve(undefined));
        },
      });
      (window as unknown as { electronAPI: unknown }).electronAPI = bridge;
    });
    // Settings' drill-in is the nav's longest list.
    await page.goto('/settings/account', { waitUntil: 'domcontentloaded' });

    const nav = page.getByTestId('shell-nav');
    const settings = nav.getByTestId('sidebar-settings-button');
    await expect(settings).toBeVisible();
    await expect(nav.getByTestId('nav-back')).toBeVisible();

    const geometry = await nav.evaluate((el) => {
      const row = el.querySelector('[data-testid="sidebar-settings-button"]')!.getBoundingClientRect();
      const regions = el.querySelector<HTMLElement>('[data-testid="sidebar-regions"]');
      return {
        navBottom: el.getBoundingClientRect().bottom,
        rowBottom: row.bottom,
        viewport: window.innerHeight,
        // The rows above the footer scroll instead of pushing it down.
        scrolls: regions ? regions.scrollHeight > regions.clientHeight : false,
      };
    });
    expect(geometry.rowBottom).toBeLessThanOrEqual(geometry.navBottom);
    expect(geometry.rowBottom).toBeLessThanOrEqual(geometry.viewport);
    expect(geometry.scrolls).toBe(true);
    // Reachable, not just painted: nothing sits over it.
    await settings.click({ trial: true });
  });
}
