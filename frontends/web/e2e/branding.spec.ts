import { test, expect } from '@playwright/test';

// Favicon branding spec: index.html should reference our app favicon, not the Vite default
// Contract: We will replace the default /vite.svg with /favicon.svg (plus PNG fallbacks)

test.describe('Branding - Favicon', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
  });

  test('uses app favicon instead of Vite default', async ({ page }) => {
    // index.html declares two rel="icon" links: an SVG primary and a PNG
    // fallback for browsers without SVG favicon support. Both are expected.
    const favicons = page.locator('head link[rel="icon"]');
    const hrefs = await favicons.evaluateAll((links) => links.map((l) => l.getAttribute('href')));

    expect(hrefs.length).toBeGreaterThan(0);
    for (const href of hrefs) {
      expect(href).toBeTruthy();
      expect(href).not.toContain('vite.svg');
      // Preferred convention
      expect(href).toContain('favicon');
    }
  });
});
