import { expect, test, type Page, type Route } from '@playwright/test';

import { mockAppsBackend } from './appsMocks';

// The forge journey ("Explore a forge app", Onboarding/journeys/forge.tsx).
// Signed in, it walks a sample app inside the signed-in shell
// (/forge/p/sample_shop), and an old /forge/demo link opens its page there.
// Signed out, the demo has one home, the demo bundle on the demo host: the
// app's /forge/demo and /demo leave for it, query kept
// (Forge/Demo/demoHandoff.ts), so a journey started in the forge shell
// continues there. The demo host's own tour is at the bottom (?shell=demo).

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

// Signed in: the sample app INSIDE the shell (/forge/p/sample_shop), on the
// shell's own nav, its data the sample's TransportScope's (fixtures/scope.tsx:
// the shop pack, every id `sample_`-prefixed), and back to where it started
// when it ends. Nothing of it may reach the backends (lib/sampleGuard.ts).
test.describe('the forge journey in Reliant, signed in', () => {
  const SAMPLE = '/forge/p/sample_shop';
  const TRACE = 'sample_4bf92f3577b34da6a3ce929d0e0e4736';
  const ISSUE = 'sample_iss-orders-deadline';
  const T = 16;
  const callout = (page: Page) => page.getByTestId('tour-callout').or(page.getByTestId('tour-card'));

  async function atStep(page: Page, n: number, title: string) {
    await expect(page.getByTestId('tour-step-count')).toContainText(`${n} of ${T}`, LOADED);
    await expect(callout(page)).toContainText(title);
  }

  /** Every request the page sends to a backend that names the sample: there must be none. */
  function watchForSampleRequests(page: Page): string[] {
    const leaks: string[] = [];
    page.on('request', (request) => {
      if (!/\/(reliant|controlplane)\.v1\./.test(request.url())) return;
      if (/sample_/.test(`${request.url()} ${request.postData() ?? ''}`)) leaks.push(request.url());
    });
    return leaks;
  }

  async function startFromThePicker(page: Page) {
    await page.goto('/projects?tour=pick:menu', { waitUntil: 'domcontentloaded' });
    await page.getByTestId('journey-card-forge').click(LOADED);
    await page.getByTestId('journey-picker').getByRole('button', { name: /start/i }).click();
  }

  test('walks the sample app in the shell, its nav and its data, and Finish returns to where it started', async ({ page }) => {
    test.slow();
    await mockAppsBackend(page);
    const leaks = watchForSampleRequests(page);
    await startFromThePicker(page);
    await expect.poll(() => new URL(page.url()).pathname, LOADED).toBe(SAMPLE);

    // [title, the page it is on, what it rings (null: a card, or unanchored)], in the journey's order.
    const steps: [string, RegExp, string | null][] = [
      ['What is Forge?', /\/forge\/p\/sample_shop$/, null],
      ["A Forge app's console", /\/forge\/p\/sample_shop$/, "[data-tour='forge-console-nav']"],
      ['Monitoring from the start', /\/forge\/p\/sample_shop$/, null],
      ['Releases, built once', /\/releases$/, "[data-tour='nav-deployments']"],
      ['One request, end to end', /\/observability\/traces$/, `[data-testid='obs-trace-row'][data-trace-id='${TRACE}'], [data-testid='lead-trace-row'][data-trace-id='${TRACE}']`],
      ['Across services', /\/observability\/traces$/, `[data-testid='trace-span-row'][data-span-id='sample_e1']`],
      ['What that request logged', /\/observability\/traces$/, "[data-testid='trace-logs']"],
      ['Errors, grouped by release', /\/observability\/errors$/, `[data-testid='obs-issue-${ISSUE}'], [data-testid='lead-featured-issue'][data-issue-id='${ISSUE}'], [data-testid='lead-issue-row'][data-issue-id='${ISSUE}']`],
      ['How an agent gets called', /\/observability\/alerts$/, "[data-testid='obs-alerts-firing']"],
      ['Hand it to an agent', /\/observability\/errors$/, "[data-testid='ask-reliant-actions']"],
      ['Who uses the app', /\/analytics$/, "[data-tour='nav-analytics']"],
      ["The app's database", /\/data$/, "[data-tour='nav-data']"],
      ["The app's tests", /\/tests$/, "[data-tour='nav-tests']"],
      ['What each release ships', /\/security\/vulnerabilities$/, "[data-tour='nav-security']"],
      ['Secrets, domains and activity', /\/secrets$/, "[data-tour='nav-configure']"],
      ['Try it on your app', /\/secrets$/, null],
    ];
    for (const [i, [title, path, target]] of steps.entries()) {
      await atStep(page, i + 1, title);
      await expect.poll(() => new URL(page.url()).pathname, { message: title }).toMatch(path);
      // The shell's own nav, with the sample app open: never a frame of its own.
      await expect(page.getByTestId('shell-nav'), title).toBeVisible();
      await expect(page.locator("[data-tour='forge-console']")).toHaveCount(0);
      if (target) await expect(page.locator(target).first(), title).toBeVisible(LOADED);
      // The sample's pages show the sample's data, never a page waiting for a deploy.
      await expect(page.getByText('appears once this project is deployed')).toHaveCount(0);
      await callout(page).getByTestId('tour-next').click();
    }

    // Back where the picker was opened, with the tour closed and no sample left behind.
    await expect(page).toHaveURL(/\/projects(\?|$)/, LOADED);
    expect(new URL(page.url()).searchParams.get('tour')).toBeNull();
    expect(await page.evaluate(() => localStorage.getItem('shell.currentProject'))).not.toBe('app:sample_shop');
    expect(leaks).toEqual([]);
  });

  test('Exit tour returns to where it started', async ({ page }) => {
    await mockAppsBackend(page);
    await startFromThePicker(page);
    await atStep(page, 1, 'What is Forge?');
    await callout(page).getByTestId('tour-next').click();
    await atStep(page, 2, "A Forge app's console");
    await page.getByTestId('tour-exit').click();
    await expect(page).toHaveURL(/\/projects(\?|$)/, LOADED);
  });

  test('an old /forge/demo link opens the page it names on the sample app', async ({ page }) => {
    await mockAppsBackend(page);
    await page.goto('/forge/demo?view=traces&tour=forge:fg-trace', { waitUntil: 'domcontentloaded' });
    await expect.poll(() => new URL(page.url()).pathname, LOADED).toBe(`${SAMPLE}/observability/traces`);
    expect(new URL(page.url()).searchParams.get('tour')).toBe('forge:fg-trace');
    await atStep(page, 5, 'One request, end to end');
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
  test('every page is in the app\'s shell: its nav, the demo\'s rows, and on a phone the page header opens it as a drawer', async ({ page }) => {
    await openDemo(page, '/forge/p/shop?shell=demo');
    const nav = page.getByTestId('shell-nav');
    await expect(nav).toBeVisible(LOADED);
    await expect(nav.getByTestId('nav-demo-home')).toHaveAttribute('href', '/forge/demo?section=home');
    await expect(nav.getByTestId('sidebar-home-button')).toHaveAttribute('aria-current', 'page');
    await expect(nav.getByTestId('nav-demo-built')).toBeVisible();
    // The app's own rows are not the demo's.
    for (const id of ['nav-agents', 'nav-usage-billing', 'sidebar-settings-button', 'nav-find']) await expect(nav.getByTestId(id)).toHaveCount(0);
    // Observability drills in, and its header leads back to the Overview.
    await nav.getByTestId('nav-observability').click();
    await expect(page).toHaveURL(/\/forge\/p\/shop\/observability(\?|$)/);
    await expect(nav.getByTestId('nav-drill-in')).toHaveAttribute('data-area', 'observability');
    await expect(nav.locator("[data-tour='nav-drill-in']")).toBeVisible();
    await nav.getByTestId('nav-back').click();
    await expect(page).toHaveURL(/\/forge\/p\/shop(\?|$)/);
    await expect(nav.getByTestId('nav-top')).toBeVisible();
    // A phone: the nav is the shell's drawer, behind the page header's toggle, with the demo's actions in it.
    await page.setViewportSize({ width: 390, height: 844 });
    await expect(nav).toHaveAttribute('data-state', 'closed');
    await page.getByTestId('shell-nav-toggle').click();
    await expect(nav).toHaveAttribute('data-state', 'open');
    await expect(nav.getByTestId('demo-drawer-actions')).toBeVisible();
    await nav.getByTestId('nav-data').click();
    await expect(page).toHaveURL(/\/forge\/p\/shop\/data/);
    await expect(nav).toHaveAttribute('data-state', 'closed');
  });

  test('walks the whole forge journey on the demo host, every step a page of the app\'s shell', async ({ page }) => {
    test.slow();
    const origin = new URL(test.info().project.use.baseURL ?? 'http://localhost:5173').origin;
    // DEMO_TOUR=full: every step, the nav's (fg-console) included.
    await page.route('**/config.js', (route) =>
      route.fulfill({ status: 200, contentType: 'text/javascript', body: `window.__FORGE_CONFIG__ = ${JSON.stringify({ CONTROL_PLANE_API_URL: origin, DEMO_TELEMETRY: 'off', DEMO_TOUR: 'full' })};` }),
    );
    await page.goto('/forge/p/shop?shell=demo&tour=forge:fg-intro');
    const TRACE = '4bf92f3577b34da6a3ce929d0e0e4736';
    const ISSUE = 'iss-orders-deadline';
    // [title, the page it is on, what it rings (null: a card)], in the journey's order (Onboarding/journeys/forge.tsx).
    const steps: [string, RegExp | null, string | null][] = [
      ['What is Forge?', /\/forge\/p\/shop$/, null],
      ["A Forge app's console", /\/forge\/p\/shop$/, "[data-tour='forge-console-nav']"],
      // Its anchors (obs-overview, overview-services) are not on the console's Overview today: the card shows unanchored.
      ['Monitoring from the start', /\/forge\/p\/shop$/, null],
      ['One request, end to end', /\/observability\/traces$/, `[data-testid='obs-trace-row'][data-trace-id='${TRACE}'], [data-testid='lead-trace-row'][data-trace-id='${TRACE}']`],
      ['Across services', /\/observability\/traces$/, "[data-testid='trace-span-row'][data-span-id='e1']"],
      ['Inside a service', /\/observability\/traces$/, "[data-testid='trace-span-row'][data-span-id='a2']"],
      ['What that request logged', /\/observability\/traces$/, "[data-testid='trace-logs']"],
      ['Where the CPU went', /\/observability\/traces$/, "[data-testid='trace-flamegraph']"],
      ['Who calls whom', /\/observability\/graph$/, "[data-testid='graph-view']"],
      ['Errors, grouped by release', /\/observability\/errors$/, `[data-testid='obs-issue-${ISSUE}'], [data-testid='lead-featured-issue'][data-issue-id='${ISSUE}'], [data-testid='lead-issue-row'][data-issue-id='${ISSUE}']`],
      ['How an agent gets called', /\/observability\/alerts$/, "[data-testid='obs-alerts-firing']"],
      ['What your agent reads', null, null],
      ['Hand it to an agent', /\/observability\/errors$/, "[data-testid='ask-reliant-actions']"],
      ['Who uses the app', /\/analytics$/, "[data-tour='nav-analytics']"],
      ["The app's database", /\/data$/, "[data-tour='nav-data']"],
      ['How it was built', /\/forge\/p\/shop$/, null],
      ['Try it on your app', null, null],
    ];
    // A spotlight step's callout, or a card step's card (no target).
    const callout = page.getByTestId('tour-callout').or(page.getByTestId('tour-card'));
    for (const [title, path, target] of steps) {
      await expect(callout, title).toContainText(title, LOADED);
      if (path) await expect.poll(() => new URL(page.url()).pathname, { message: title }).toMatch(path);
      // In the shell, on every step: the nav beside the page.
      await expect(page.getByTestId('shell-nav'), title).toBeVisible();
      if (target) {
        const ringed = page.locator(target).first();
        await expect(ringed, title).toBeVisible(LOADED);
        if (title === "A Forge app's console") {
          // The nav step rings the shell's own nav, not a frame of the demo's.
          await expect(page.getByTestId('shell-nav').locator(target)).toBeVisible();
          await expect(page.locator("[data-tour='forge-console']")).toHaveCount(0);
        }
      }
      if (title === 'Try it on your app') break;
      await callout.getByTestId('tour-next').click();
    }
    await callout.getByTestId('tour-next').click();
    // The journey is over (the page's own tip may open after it), and the visitor is left in the shell.
    await expect(callout.filter({ hasText: 'Try it on your app' })).toHaveCount(0);
    await expect(page.getByTestId('shell-nav')).toBeVisible();
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
