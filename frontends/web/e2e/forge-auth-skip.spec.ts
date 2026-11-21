import { expect, test, type Route } from '@playwright/test';

// forge.reliantlabs.io/auth, signed out, "Skip for now": the guest session it
// mints must land on the Apps page. It used to bounce / → /apps → / and stop
// on a blank page with no console error (the forge root's <Navigate> did not
// redirect a second visit to `/`). The forge host now mounts the app's one
// route tree (routes.tsx, U6): a guest has not onboarded, so the forge
// brand's home sends them to /apps (OnboardingFlow/onboardingGate.ts,
// forgeBrandHomeTarget) on every visit.

const LOADED = { timeout: 20_000 };
const json = (route: Route, body: unknown) =>
  route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });

const b64 = (o: unknown) => Buffer.from(JSON.stringify(o)).toString('base64url');
const GUEST = {
  id: 'guest_1', aud: 'authenticated', role: 'authenticated', is_anonymous: true,
  app_metadata: {}, user_metadata: {}, created_at: '2026-01-01T00:00:00Z',
};
const GUEST_SESSION = {
  access_token: `${b64({ alg: 'HS256', typ: 'JWT' })}.${b64({ sub: GUEST.id, exp: 4102444800, aud: 'authenticated', role: 'authenticated', is_anonymous: true })}.sig`,
  refresh_token: 'test-refresh', token_type: 'bearer', expires_at: 4102444800, expires_in: 3600, user: GUEST,
};

async function mockForgeGuest(page: import('@playwright/test').Page) {
  const origin = new URL(test.info().project.use.baseURL ?? 'http://localhost:5173').origin;
  await page.route('**/reliant.v1.**', (route) => json(route, {}));
  await page.route('**/controlplane.v1.**', (route) => json(route, {}));
  await page.route('**/health', (route) => json(route, { auth_mode: 'supabase' }));
  await page.route('**/supabase/**', (route) => {
    const path = new URL(route.request().url()).pathname.replace(/^.*\/supabase/, '');
    if (path === '/auth/v1/signup' || path === '/auth/v1/token') return json(route, GUEST_SESSION);
    if (path === '/auth/v1/user') return json(route, GUEST);
    return json(route, {});
  });
  await page.route('**/config.js', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'text/javascript',
      body: `window.__FORGE_CONFIG__ = ${JSON.stringify({
        CONTROL_PLANE_API_URL: origin,
        SUPABASE_URL: `${origin}/supabase`,
        SUPABASE_ANON_KEY: 'anon-test-key',
        APP_URL: 'https://app.reliantlabs.io',
      })};`,
    }),
  );

}

test('"Skip for now" on the forge host lands a signed-out visitor on /apps, not a blank page', async ({ page }) => {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(e.message));
  await mockForgeGuest(page);

  await page.goto('/auth?shell=forge');
  await page.getByRole('button', { name: /skip for now/i }).click();

  await expect(page).toHaveURL(/\/apps$/, LOADED);
  await expect(page.getByTestId('apps-page')).toBeVisible(LOADED);
  // Settled, not passing through: still here, still rendered.
  await page.waitForTimeout(1500);
  await expect(page).toHaveURL(/\/apps$/);
  await expect(page.getByTestId('apps-page')).toBeVisible();
  expect(errors).toEqual([]);
});

// The root redirect on its own: the guest is already on /apps and `/` is visited
// again (twice). The forge root used to render nothing for the second visit.
test('revisiting / on the forge host always ends on /apps', async ({ page }) => {
  await mockForgeGuest(page);
  await page.goto('/auth?shell=forge');
  await page.getByRole('button', { name: /skip for now/i }).click();
  await expect(page).toHaveURL(/\/apps$/, LOADED);

  for (let i = 0; i < 2; i++) {
    await page.evaluate(() =>
      (globalThis as unknown as { __RELIANT_ROUTER: { navigate: (o: { to: string }) => Promise<void> } }).__RELIANT_ROUTER.navigate({ to: '/' }),
    );
    await expect(page).toHaveURL(/\/apps$/, LOADED);
    await expect(page.getByTestId('apps-page')).toBeVisible(LOADED);
  }
});
