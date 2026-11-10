import { expect, test } from '@playwright/test';

/** Run against the guarded production demo build, never a credentialed backend. */
test.describe('demo Data and Storage browser routes', () => {
  test.skip(!process.env.DEMO_BROWSER_PROOF, 'Requires guarded demo build and DEMO_BROWSER_PROOF=1');
  for (const width of [360, 390, 768, 1440]) {
    test(`direct links, read-only preview and bounded layout at ${width}px`, async ({ page, baseURL }) => {
      await page.setViewportSize({ width, height: 900 });
      const errors: string[] = [];
      const requests: string[] = [];
      page.on('pageerror', (error) => errors.push(error.message));
      page.on('request', (request) => {
        if (request.resourceType() === 'fetch' || request.resourceType() === 'xhr') requests.push(request.url());
      });
      await page.goto('/forge/p/shop/data?env=staging&db=shop&table=public.users');
      await expect(page.getByTestId('data-grid')).toHaveAttribute('data-state', 'ready');
      await expect(page.getByText('maya@example.com', { exact: true })).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      await expect(page.getByTestId('data-row-open').first()).toHaveAttribute('href', /row=/);
      await page.getByTestId('data-row-open').first().click();
      await expect(page.getByTestId('data-row-page')).toHaveAttribute('data-state', 'ready');
      await page.getByTestId('data-row-back').click();
      await expect(page.getByTestId('data-grid')).toHaveAttribute('data-state', 'ready');
      await page.goto('/forge/p/shop/storage?env=staging&bucket=uploads&object=README.txt');
      await expect(page.getByTestId('storage-preview-text')).toContainText('Synthetic shop uploads');
      await expect(page.getByTestId('storage-download')).toBeDisabled();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      await page.goto('/forge/p/shop/data?env=prod&db=shop&table=public.users');
      await expect(page.getByTestId('data-access-dialog')).toBeVisible();
      await expect(page.getByTestId('data-row')).toHaveCount(0);
      await page.getByTestId('data-access-reason').fill('Inspect synthetic demo rows');
      await page.getByTestId('data-access-confirm').click();
      await expect(page.getByTestId('data-grid')).toHaveAttribute('data-state', 'ready');
      await page.goto('/forge/p/shop/storage?env=prod&bucket=uploads&prefix=orders%2F&object=orders%2Fsample.json');
      await expect(page.getByTestId('storage-access-dialog')).toBeVisible();
      await page.getByTestId('storage-access-reason').fill('Inspect synthetic demo files');
      await page.getByTestId('storage-access-confirm').click();
      await expect(page.getByTestId('storage-preview-text')).toContainText('demo-order-001');
      await page.goto('/forge/p/shop/environments/staging?tab=identity');
      await expect(page.getByText('maya@example.com', { exact: true })).toBeVisible();
      if (width < 768) {
        const trigger = page.getByRole('button', { name: 'Open navigation' });
        await trigger.click();
        const drawer = page.getByRole('dialog');
        await expect(drawer).toHaveCount(1);
        const first = drawer.getByRole('link').first();
        await expect(first).toBeFocused();
        const links = drawer.getByRole('link');
        await links.last().focus();
        await page.keyboard.press('Tab');
        await expect(first).toBeFocused();
        await page.keyboard.press('Escape');
        await expect(drawer).toHaveCount(0);
        await expect(trigger).toBeFocused();
      }
      expect(errors).toEqual([]);
      expect(requests.filter((url) => !url.startsWith(baseURL!) && !/otel|telemetry|analytics/.test(url))).toEqual([]);
      expect(requests.filter((url) => /controlplane\.v1|supabase|auth\/token/.test(url))).toEqual([]);
    });
  }
});
