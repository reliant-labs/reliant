import { expect, test } from '@playwright/test';

import { PROJECT, mockAppsBackend } from './appsMocks';

// Use the real app, styles and Tooltip wrappers: jsdom cannot detect either
// the collapsed line box or a centered strip extending past its clip boundary.
test('New workspace matches every selector, including after the controls wrap', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await mockAppsBackend(page);
  await page.goto(`/project/${PROJECT.id}`, { waitUntil: 'domcontentloaded' });
  await page.getByRole('button', { name: 'New chat', exact: true }).click();
  const controls = page.locator('[data-onboarding="workspace-buttons"]');
  await expect(controls.getByRole('button', { name: 'New workspace', exact: true })).toBeVisible();
  await expect(controls.getByTestId('new-chat-project-chip')).toBeVisible();
  await expect(controls.getByTestId('machine-picker')).toBeVisible();
  await page.evaluate(() => document.fonts.ready);

  for (const width of [576, 300]) {
    // The pane can be split or resized independently of the viewport. Bound
    // just its controls to exercise wrapping without switching to mobile UI.
    await controls.evaluate((el, value) => { el.style.width = `${value}px`; }, width);
    const geometry = await controls.evaluate((el) => Array.from(el.querySelectorAll('button')).map((button) => {
      const rect = button.getBoundingClientRect();
      return { label: button.textContent, height: rect.height, top: rect.top };
    }));
    expect(geometry).toHaveLength(4);
    const selectorHeight = geometry[0].height;
    expect(selectorHeight).toBeGreaterThan(0);
    for (const control of geometry) {
      expect(control.height, `${control.label} at ${width}px`).toBeCloseTo(selectorHeight, 1);
    }
    if (width === 300) expect(new Set(geometry.map((control) => control.top)).size).toBeGreaterThan(1);
  }
});

for (const { width, electron } of [200, 240].flatMap((width) =>
  [false, true].map((electron) => ({ width, electron })),
)) {
  test(`sidebar navigation fits at ${width}px (${electron ? 'Electron with browser' : 'web'})`, async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 1000 });
    await mockAppsBackend(page);
    await page.addInitScript(({ desktop, panelWidth }) => {
      localStorage.setItem('file-browser-width', String(panelWidth));
      if (desktop) window.RELIANT_CONFIG = { isElectron: true, isDev: true } as typeof window.RELIANT_CONFIG;
    }, { desktop: electron, panelWidth: width });
    await page.goto(`/project/${PROJECT.id}`, { waitUntil: 'domcontentloaded' });
    const sidebar = page.locator('[data-onboarding="right-sidebar"]');
    const tabs = sidebar.getByRole('tab');
    await expect(tabs).toHaveCount(electron ? 5 : 4);
    await expect(sidebar.getByRole('tab', { name: 'Browser' })).toHaveCount(electron ? 1 : 0);
    const terminal = sidebar.getByRole('button', { name: /^(Show|Hide) Terminal$/ });
    await expect(terminal).toBeVisible();

    const geometry = await sidebar.evaluate((el) => {
      const edge = el.getBoundingClientRect();
      const controls = Array.from(el.querySelectorAll<HTMLElement>('[role="tab"], button[aria-pressed]'));
      return {
        width: edge.width,
        controls: controls.map((control) => {
          const rect = control.getBoundingClientRect();
          const icon = control.querySelector('svg')!.getBoundingClientRect();
          const hit = document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2);
          return {
            label: control.getAttribute('aria-label'),
            left: rect.left - edge.left, right: rect.right - edge.left,
            iconLeft: icon.left - edge.left, iconRight: icon.right - edge.left,
            reachable: hit !== null && control.contains(hit),
          };
        }),
      };
    });
    expect(geometry.width).toBeGreaterThanOrEqual(width - 2);
    expect(geometry.width).toBeLessThanOrEqual(width);
    expect(geometry.controls).toHaveLength(electron ? 6 : 5);
    for (const control of geometry.controls) {
      expect(control.left, `${control.label}: left edge`).toBeGreaterThanOrEqual(0);
      expect(control.right, `${control.label}: right edge`).toBeLessThanOrEqual(geometry.width);
      expect(control.iconLeft).toBeGreaterThanOrEqual(0);
      expect(control.iconRight).toBeLessThanOrEqual(geometry.width);
      expect(control.reachable, `${control.label}: hit target`).toBe(true);
    }

    // The compact strip keeps the existing keyboard contract. Terminal is
    // still an independent toggle rather than a tab in the roving tab order.
    const files = sidebar.getByRole('tab', { name: 'Files' });
    await files.focus();
    await files.press('End');
    await expect(tabs.last()).toBeFocused();
    await expect(tabs.last()).toHaveAttribute('aria-selected', 'true');
    await tabs.last().press('Home');
    await expect(files).toBeFocused();
    await expect(files).toHaveAttribute('aria-selected', 'true');
    const pressed = await terminal.getAttribute('aria-pressed');
    await terminal.click();
    await expect(terminal).toHaveAttribute('aria-pressed', pressed === 'true' ? 'false' : 'true');
    await expect(files).toHaveAttribute('aria-selected', 'true');
  });
}
