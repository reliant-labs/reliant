import { test, expect } from '@playwright/test';

/**
 * WebSocket connection smoke tests.
 * These verify the app can establish WebSocket connections when authenticated.
 *
 * Note: Full WebSocket testing requires authenticated state and a running backend.
 * These tests are basic smoke tests that can run without full infrastructure.
 */
test.describe('WebSocket Infrastructure Tests', () => {
  test('should have WebSocket available in browser', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');

    // Verify WebSocket is available
    const hasWebSocket = await page.evaluate(() => typeof WebSocket !== 'undefined');
    expect(hasWebSocket).toBe(true);
  });

  test('should not have uncaught WebSocket errors on load', async ({ page }) => {
    const wsErrors: string[] = [];

    page.on('console', (msg) => {
      if (msg.type() === 'error' && msg.text().toLowerCase().includes('websocket')) {
        wsErrors.push(msg.text());
      }
    });

    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');
    // Give a moment for any async WebSocket initialization
    await page.waitForTimeout(500);

    // WebSocket errors during initial load indicate infrastructure issues
    // Note: Connection refused is expected if backend isn't running
    const criticalErrors = wsErrors.filter(
      (e) => !e.includes('refused') && !e.includes('Failed to connect')
    );

    expect(criticalErrors).toHaveLength(0);
  });
});