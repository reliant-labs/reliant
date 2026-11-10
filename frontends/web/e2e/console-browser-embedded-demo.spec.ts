import { expect, test } from '@playwright/test';

/** Full-product Vite server, selecting its public forge shell (not the static demo bundle). */
test('forge shell embedded demo keeps Data and Storage routes synthetic', async ({ page }) => {
  test.skip(!process.env.EMBEDDED_DEMO_PROOF, 'Requires full-product Vite and EMBEDDED_DEMO_PROOF=1');
  await page.setViewportSize({ width: 390, height: 900 });
  const backend: string[] = [];
  page.on('request', (request) => { if (/controlplane\.v1|supabase.*auth\/token/.test(request.url())) backend.push(request.url()); });
  await page.goto('/forge/demo?shell=forge&browser=data&envs=%5B%22staging%22%5D&db=shop&table=public.users');
  await expect(page.getByTestId('data-grid')).toHaveAttribute('data-state', 'ready', { timeout: 20000 });
  await expect(page.getByText('maya@example.com', { exact: true })).toBeVisible();
  await page.getByTestId('data-row-open').first().click();
  await expect(page.getByTestId('data-row-page')).toHaveAttribute('data-state', 'ready');
  expect(page.url()).toContain('browser=data');
  await page.getByTestId('data-row-back').click();
  await expect(page.getByTestId('data-grid')).toHaveAttribute('data-state', 'ready', { timeout: 20000 });
  await page.goto('/forge/demo?browser=storage&envs=%5B%22staging%22%5D&bucket=uploads&object=README.txt');
  await expect(page.getByTestId('storage-preview-text')).toContainText('Synthetic shop uploads');
  await expect(page.getByTestId('storage-download')).toBeDisabled();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  expect(backend).toEqual([]);
});
