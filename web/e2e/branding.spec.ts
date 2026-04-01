import { test, expect } from '@playwright/test';

// Favicon branding spec: index.html should reference our app favicon, not the Vite default
// Contract: We will replace the default /vite.svg with /favicon.svg (plus PNG fallbacks)

test.describe('Branding - Favicon', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
  });

  test('uses app favicon instead of Vite default', async ({ page }) => {
    const favicon = page.locator('head link[rel="icon"]');
    await expect(favicon).toHaveCount(1);

    const href = await favicon.getAttribute('href');
    expect(href).toBeTruthy();
    expect(href).not.toContain('vite.svg');
    // Preferred convention
    expect(href).toContain('favicon');
  });
});
