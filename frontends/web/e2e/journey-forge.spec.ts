import { expect, test, type Page, type Route } from '@playwright/test';


// The forge journey ("Explore a forge app", Onboarding/journeys/forge.tsx).
// The demo has one home, the demo bundle on the demo host: the app's
// /forge/demo and /demo leave for it, query kept (Forge/Demo/demoHandoff.ts),
// so a journey started in the forge shell continues there. The demo host's own
// tour is at the bottom (?shell=demo).

const RELIANT_ORIGIN = 'https://app.reliantlabs.io';
const DEMO_ORIGIN = 'https://demo.reliantlabs.io';
const LOADED = { timeout: 20_000 };

const json = (route: Route, body: unknown) =>
  route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });

/** The demo host, answered here: the handoff is the app's, not the demo's, to prove. */
async function stubDemoHost(page: Page): Promise<void> {
  await page.route(`${DEMO_ORIGIN}/**`, (route) => route.fulfill({ status: 200, contentType: 'text/html', body: '<!doctype html><title>demo</title>' }));
}

/** The query the app handed the demo host. */
async function handedOff(page: Page): Promise<URLSearchParams> {
  await expect(page).toHaveURL(new RegExp(`^${DEMO_ORIGIN}/forge/demo`), LOADED);
  return new URL(page.url()).searchParams;
}

/** A signed-out visitor on the forge host: no session, GoTrue answering nothing, APP_URL pointing at Reliant. */
async function mockForgeShellSignedOut(page: Page): Promise<void> {
  const origin = new URL(test.info().project.use.baseURL ?? 'http://localhost:5173').origin;
  await page.route('**/reliant.v1.**', (route) => json(route, {}));
  await page.route('**/controlplane.v1.**', (route) => json(route, {}));
  await page.route('**/health', (route) => json(route, { auth_mode: 'supabase' }));
  await page.route('**/supabase/**', (route) => json(route, {}));
  const config = {
    CONTROL_PLANE_API_URL: origin,
    SUPABASE_URL: `${origin}/supabase`,
    SUPABASE_ANON_KEY: 'anon-test-key',
    APP_URL: RELIANT_ORIGIN,
  };
  await page.route('**/config.js', (route) =>
    route.fulfill({ status: 200, contentType: 'text/javascript', body: `window.__FORGE_CONFIG__ = ${JSON.stringify(config)};` }),
  );
}

test.describe('the forge journey in the forge shell', () => {
  test('a signed-out visitor from the landing page goes straight to the journey on the demo host', async ({ page }) => {
    await mockForgeShellSignedOut(page);
    await stubDemoHost(page);
    await page.goto('/?shell=forge&journey=forge&utm_campaign=forge_standalone', { waitUntil: 'domcontentloaded' });
    // Never sent to sign in first: the journey's first step, on the demo host.
    expect((await handedOff(page)).get('tour')).toBe('forge:fg-intro');
  });

  test('/demo leaves for the demo host, with the tour', async ({ page }) => {
    await mockForgeShellSignedOut(page);
    await stubDemoHost(page);
    await page.goto('/demo?shell=forge&tour=forge:fg-intro', { waitUntil: 'domcontentloaded' });
    expect((await handedOff(page)).get('tour')).toBe('forge:fg-intro');
  });

  test('?journey=workflows points at the Workflows area and runs no journey here', async ({ page }) => {
    await mockForgeShellSignedOut(page);
    await page.goto('/?shell=forge&journey=workflows', { waitUntil: 'domcontentloaded' });
    const offer = page.getByTestId('forge-workflows-offer');
    await expect(offer).toBeVisible(LOADED);
    // One shell: the Workflows area is this app's own page, never another origin.
    await expect(offer.getByRole('link', { name: 'Open Workflows' })).toHaveAttribute('href', '/workflows/library');
    await expect(page.getByTestId('tour-controls')).toHaveCount(0);
  });
});

test.describe('the demo page-entry tour', () => {
  async function openDemo(page: Page, path = '/?shell=demo') {
    const origin = new URL(test.info().project.use.baseURL ?? 'http://localhost:5173').origin;
    await page.route('**/config.js', route => route.fulfill({status:200,contentType:'text/javascript',body:`window.__FORGE_CONFIG__ = ${JSON.stringify({CONTROL_PLANE_API_URL:origin,DEMO_TELEMETRY:'off'})};`}));
    await page.goto(path);
  }
  test('explains direct Logs by itself once a session; the ? brings it back after a refresh', async ({page}) => {
    await openDemo(page, '/forge/p/shop/observability/logs?shell=demo');
    await expect(page.getByTestId('tour-callout')).toContainText('Read what your app recorded', LOADED);
    // A one-step tour shows no step count (TourControls: #2399).
    await expect(page.getByTestId('tour-step-count')).toHaveCount(0);
    await expect(page.getByTestId('demo-page-tour-start')).toHaveCount(0);
    await page.getByTestId('tour-next').click();
    await expect(page.getByTestId('tour-callout')).toHaveCount(0);
    // A section's tip opens by itself once per session (#2567), so a refresh
    // keeps it closed and the corner ? brings it back.
    await page.reload();
    await expect(page.getByTestId('demo-page-info')).toBeVisible(LOADED);
    await expect(page.getByTestId('tour-callout')).toHaveCount(0);
    await page.getByTestId('demo-page-info').click();
    await expect(page.getByTestId('tour-callout')).toContainText('Logs');
    await page.getByTestId('tour-next').click();
    await expect(page).toHaveURL(/observability\/logs/);
    await expect(page.getByTestId('tour-callout')).toHaveCount(0);
    await expect(page.getByTestId('onboarding-nav-bar')).toHaveCount(0);
  });
  test('legacy logs link opens the console Logs with no prerequisite or cross-site sequence', async ({page}) => {
    // An old /forge/demo?view=…&tour= link redirects to the console page it names (#2687).
    await openDemo(page, '/forge/demo?shell=demo&view=logs&tour=forge:fg-logs');
    await expect(page.getByTestId('tour-callout')).toContainText('Logs', LOADED);
    await page.getByTestId('tour-next').click();
    await expect(page.getByTestId('tour-callout')).toHaveCount(0);
    await expect(page).toHaveURL(/\/forge\/p\/shop\/observability\/logs\?/);
  });
});
