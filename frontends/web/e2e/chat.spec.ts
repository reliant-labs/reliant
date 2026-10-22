import { test, expect } from '@playwright/test';

/**
 * Basic smoke tests for the application.
 * These tests verify the app loads without errors, regardless of auth state.
 */
test.describe('Application Smoke Tests', () => {
  test('should load the application without JavaScript errors', async ({ page }) => {
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));

    const response = await page.goto('/');

    // Page should return successful HTTP status
    expect(response?.status()).toBeLessThan(400);

    // Wait for the page to stabilize
    await page.waitForLoadState('domcontentloaded');

    // No JavaScript errors should have occurred
    expect(errors).toHaveLength(0);
  });

  test('should render without React hydration mismatches', async ({ page }) => {
    const consoleErrors: string[] = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') {
        consoleErrors.push(msg.text());
      }
    });

    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');

    // Filter for React-specific hydration mismatch errors only
    // Exclude our app's legitimate state rehydration logs (localStorage, settings, etc.)
    const reactHydrationErrors = consoleErrors.filter(
      (msg) =>
        (msg.includes('Hydration failed') ||
          msg.includes('Text content does not match') ||
          msg.includes('There was an error while hydrating')) &&
        !msg.includes('rehydrate') && // Exclude our app's rehydration
        !msg.includes('[WorkspaceState]') &&
        !msg.includes('settings store')
    );

    expect(reactHydrationErrors).toHaveLength(0);
  });

  test('should load React app root', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');

    // Check that the React root element exists and has content
    const root = page.locator('#root');
    await expect(root).toBeAttached();

    // The root should have child content (React mounted)
    const childCount = await root.evaluate((el) => el.childNodes.length);
    expect(childCount).toBeGreaterThan(0);
  });
});