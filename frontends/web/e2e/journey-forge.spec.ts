import { expect, test, type Page, type Request, type Route } from '@playwright/test';

import { mockAppsBackend } from './appsMocks';

// The forge journey ("Explore a forge app", Onboarding/journeys/forge.tsx) on
// the demo console (/forge/demo, fixture data), walked end to end in the
// shells it runs in (the public demo host's own cut is at the bottom):
//
//   Reliant      the shell picked by hostname (no ?shell override), signed in
//                (appsMocks.ts), the demo under Reliant's authenticated layout;
//   forge        ?shell=forge (lib/shell.ts), signed OUT: the demo is public
//                there, and the landing page's ?journey= starts the journey.
//
// The nine steps are driven the way a visitor drives them: each required
// action is the real click on the console (open the trace, its logs, the
// flamegraph, the issue, Ask an agent), then Next. Throughout, the demo must
// not reach a daemon, a chat or real telemetry; and each shell's end card
// offers the other journey the way that shell can (in the forge shell, a link
// to Reliant at the runtime config's APP_URL).

const TRACE_ID = '4bf92f3577b34da6a3ce929d0e0e4736';
const ISSUE_ID = 'iss-orders-deadline';
const RELIANT_ORIGIN = 'https://app.reliantlabs.io';
const LOADED = { timeout: 20_000 };

/** Calls the demo must never make, in either shell: chats, runs, the daemon's own services, real telemetry. */
const DAEMON_OR_CHAT =
  /reliant\.v1\.(ChatService\/(StartChat|SendMessage)|MessageService|RunService|WorkflowService|ForgeService|DaemonService|ToolsDaemonService|TerminalService|FileSystemService|StreamingService)\/|ObservabilityService\//;

const json = (route: Route, body: unknown) =>
  route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });

/** Every request URL from now on; read it after the walk. */
function recordRequests(page: Page): { urls: string[]; sockets: string[] } {
  const log = { urls: [] as string[], sockets: [] as string[] };
  page.on('request', (request: Request) => log.urls.push(request.url()));
  page.on('websocket', (socket) => log.sockets.push(socket.url()));
  return log;
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
  // The end card opens Reliant in a new tab: record it instead of leaving the test's origin.
  await page.addInitScript(() => {
    const opened: string[] = [];
    (window as unknown as { __opened: string[] }).__opened = opened;
    window.open = ((url?: string | URL) => {
      opened.push(String(url));
      return null;
    }) as typeof window.open;
  });
}

const navBar = (page: Page) => page.getByTestId('onboarding-nav-bar');

async function atStep(page: Page, n: number, title: string) {
  await expect(navBar(page)).toContainText(`${n} / 9`, LOADED);
  await expect(navBar(page)).toContainText(title);
}

async function next(page: Page) {
  await navBar(page).getByRole('button', { name: /^Next/ }).click();
}

/** Steps 1–8, from the console to Ask an agent, each by its required action. Leaves the end card open. */
async function walkToEndCard(page: Page) {
  await atStep(page, 1, "A forge app's console");
  await expect(page.locator("[data-tour='forge-console']")).toBeVisible();
  await next(page);

  await atStep(page, 2, 'Instrumented from the start');
  await expect(page.getByTestId('obs-overview')).toBeVisible();
  await next(page);

  await atStep(page, 3, 'One request, end to end');
  await page.locator(`[data-testid='obs-trace-row'][data-trace-id='${TRACE_ID}']`).click();
  await expect(page).toHaveURL(new RegExp(`trace=${TRACE_ID}`));
  await expect(page.getByTestId('trace-waterfall')).toBeVisible();
  await next(page);

  await atStep(page, 4, 'What that request logged');
  await page.getByTestId('trace-logs').click();
  await expect(page).toHaveURL(/view=logs/);
  await expect(page.getByTestId('obs-logs-trace-scope')).toBeVisible();
  await next(page);

  await atStep(page, 5, 'Where the CPU went');
  await page.getByTestId('trace-flamegraph').click();
  await expect(page).toHaveURL(/view=profiles/);
  await expect(page.getByTestId('obs-profiles')).toBeVisible();
  await next(page);

  await atStep(page, 6, 'Errors, grouped');
  await page.getByTestId(`obs-issue-${ISSUE_ID}`).click();
  await expect(page).toHaveURL(new RegExp(`issue=${ISSUE_ID}`));
  await expect(page.getByTestId('obs-issue-header')).toBeVisible();
  await next(page);

  await atStep(page, 7, 'How an agent gets called');
  await expect(page.getByTestId('obs-alerts-firing')).toBeVisible();
  await next(page);

  await atStep(page, 8, 'Ask an agent about it');
  await page.getByTestId('ask-reliant-actions').getByTestId('ask-agent').click();
  const sheet = page.getByRole('dialog', { name: 'Ask an agent' });
  await expect(sheet).toBeVisible();
  // Sample ids, not log text: the demo says so.
  await expect(page.getByTestId('ask-agent-demo-note')).toBeVisible();
  await sheet.getByRole('button', { name: 'Done' }).click();
  await expect(navBar(page)).toContainText('Done');
  await next(page);

  await atStep(page, 9, 'Try it on your app');
}

function expectNoDaemonOrChat(log: { urls: string[]; sockets: string[] }) {
  expect(log.urls.filter((url) => DAEMON_OR_CHAT.test(url))).toEqual([]);
  expect(log.sockets).toEqual([]);
}

test.describe('the forge journey in Reliant', () => {
  test('walks the nine steps on the demo and ends on Finish or the workflows journey', async ({ page }) => {
    await mockAppsBackend(page);
    await page.goto('/forge/demo?tour=forge:fg-console', { waitUntil: 'domcontentloaded' });
    await expect(page.locator("[data-tour='forge-console']")).toBeVisible(LOADED);
    // Reliant's own chrome (chats, machines) loads with the page; the demo's walk adds none of it.
    const log = recordRequests(page);

    await walkToEndCard(page);

    // The end card: Reliant's CLI, and the other journey runs right here.
    await expect(page.getByTestId('tour-commands')).toContainText('reliant forge obs status prod');
    await expect(navBar(page).getByRole('button', { name: 'Try the other journey' })).toBeVisible();
    expectNoDaemonOrChat(log);

    await navBar(page).getByRole('button', { name: /^Finish/ }).click();
    // Finish lands on forge's Apps (the bare /forge lands there), tour closed.
    await expect(page).toHaveURL(/\/(forge|apps)(\?|$)/, LOADED);
    expect(page.url()).not.toContain('tour=');
  });
});

test.describe('the forge journey in the forge shell', () => {
  test('a signed-out visitor from the landing page goes straight to the journey on the public demo', async ({ page }) => {
    await mockForgeShellSignedOut(page);
    const log = recordRequests(page);
    await page.goto('/?shell=forge&journey=forge&utm_campaign=forge_standalone', { waitUntil: 'domcontentloaded' });

    await expect(page).toHaveURL(/\/forge\/demo\?/, LOADED);
    expect(new URL(page.url()).searchParams.get('tour')).toBe('forge:fg-console');
    // Never sent to sign in first.
    await expect(page.getByRole('heading', { name: /sign in/i })).toHaveCount(0);

    await walkToEndCard(page);

    // The end card: the forge CLI, and the workflows journey offered where it runs, at APP_URL.
    await expect(page.getByTestId('tour-commands')).toContainText('forge obs status prod');
    await expect(page.getByTestId('tour-commands')).not.toContainText('reliant forge');
    await navBar(page).getByRole('button', { name: 'Try AI workflows in Reliant' }).click();
    expect(await page.evaluate(() => (window as unknown as { __opened: string[] }).__opened)).toEqual([
      `${RELIANT_ORIGIN}/?journey=workflows`,
    ]);

    // The forge shell has no daemon and no chats at all: nothing under reliant.v1.
    expectNoDaemonOrChat(log);
    expect(log.urls.filter((url) => url.includes('/reliant.v1.'))).toEqual([]);

    // Finish goes to the shell's Apps, which asks a signed-out visitor to sign in.
    await navBar(page).getByRole('button', { name: /^Finish/ }).click();
    await expect(page).toHaveURL(/\/(apps|auth)(\?|$)/, LOADED);
    expect(page.url()).not.toContain('tour=');
  });

  test('the demo is public: /demo opens it signed out, with the tour', async ({ page }) => {
    await mockForgeShellSignedOut(page);
    await page.goto('/demo?shell=forge&tour=forge:fg-console', { waitUntil: 'domcontentloaded' });
    await expect(page).toHaveURL(/\/forge\/demo\?/, LOADED);
    await atStep(page, 1, "A forge app's console");
  });

  test('?journey=workflows offers Reliant and runs no journey here', async ({ page }) => {
    await mockForgeShellSignedOut(page);
    await page.goto('/forge/demo?shell=forge&journey=workflows', { waitUntil: 'domcontentloaded' });
    const offer = page.getByTestId('forge-workflows-offer');
    await expect(offer).toBeVisible(LOADED);
    await expect(offer.getByRole('link', { name: 'Open Reliant' })).toHaveAttribute('href', `${RELIANT_ORIGIN}/?journey=workflows`);
    await expect(navBar(page)).toHaveCount(0);

    // ?tour=workflows is not a journey in this shell either.
    await page.goto('/forge/demo?tour=workflows:wf-new', { waitUntil: 'domcontentloaded' });
    await expect(page.locator("[data-tour='forge-console']")).toBeVisible(LOADED);
    await expect(navBar(page)).toHaveCount(0);
  });
});

// The public demo host (demo.reliantlabs.io, lib/shell.ts `demo`), picked here
// with ?shell=demo: the tour starts by itself on every load, its short cut is
// ten steps (DEMO_TOUR=short), every write is off, and the page reaches no
// other origin — its Connect transport is forge's mock transport
// (Forge/Demo/data), so not even the sample data's reads leave the browser.
test.describe('the forge journey on the demo host', () => {
  async function openDemo(page: Page, path = '/?shell=demo') {
    const origin = new URL(test.info().project.use.baseURL ?? 'http://localhost:5173').origin;
    const config = { CONTROL_PLANE_API_URL: origin, APP_URL: RELIANT_ORIGIN, DEMO_TOUR: 'short' };
    await page.route('**/config.js', (route) =>
      route.fulfill({ status: 200, contentType: 'text/javascript', body: `window.__FORGE_CONFIG__ = ${JSON.stringify(config)};` }),
    );
    const log = recordRequests(page);
    await page.goto(path, { waitUntil: 'domcontentloaded' });
    return { log, origin };
  }

  const atDemoStep = async (page: Page, n: number, title: string) => {
    await expect(navBar(page)).toContainText(`${n} / 10`, LOADED);
    await expect(navBar(page)).toContainText(title);
  };

  test('starts the tour on load, walks ten steps on sample data, and reaches no other origin', async ({ page }) => {
    const { log, origin } = await openDemo(page);

    await expect(page).toHaveURL(/\/forge\/demo\?/, LOADED);
    expect(new URL(page.url()).searchParams.get('tour')).toBe('forge:fg-console');
    await expect(page.locator("[data-shell='demo']")).toHaveCount(1);
    await atDemoStep(page, 1, "A forge app's console");
    await next(page);

    await atDemoStep(page, 2, 'Instrumented from the start');
    await next(page);

    await atDemoStep(page, 3, 'One request, end to end');
    await page.locator(`[data-testid='obs-trace-row'][data-trace-id='${TRACE_ID}']`).click();
    await expect(page.getByTestId('trace-waterfall')).toBeVisible();
    await next(page);

    await atDemoStep(page, 4, 'Across services');
    await page.locator("[data-testid='trace-span-row'][data-span-id='e1']").click();
    await expect(page).toHaveURL(/span=e1/);
    await expect(page.getByTestId('span-details')).toContainText('inventory');
    await next(page);

    await atDemoStep(page, 5, 'Inside a service');
    await next(page);

    await atDemoStep(page, 6, 'What that request logged');
    await page.getByTestId('trace-logs').click();
    await expect(page.getByTestId('obs-logs-trace-scope')).toBeVisible();
    await next(page);

    await atDemoStep(page, 7, 'Errors, grouped');
    await page.getByTestId(`obs-issue-${ISSUE_ID}`).click();
    await expect(page.getByTestId('obs-issue-header')).toBeVisible();
    // Read-only: triage, assignee and comments are off, saying why.
    await expect(page.getByTestId('obs-issue-resolve')).toBeDisabled();
    await expect(page.getByTestId('obs-issue-resolve')).toHaveAttribute('title', 'Read-only demo');
    await expect(page.getByTestId('obs-issue-assignee-pick')).toBeDisabled();
    await expect(page.getByTestId('obs-comment-box')).toHaveAttribute('placeholder', 'Comments are off in the demo.');
    await next(page);

    await atDemoStep(page, 8, 'What your agent reads');
    await expect(page.getByTestId('tour-commands').first()).toContainText(`forge obs trace ${TRACE_ID} prod`);
    await next(page);

    await atDemoStep(page, 9, 'Ask an agent about it');
    await page.getByTestId('ask-reliant-actions').getByTestId('ask-agent').click();
    await page.getByRole('dialog', { name: 'Ask an agent' }).getByRole('button', { name: 'Done' }).click();
    await next(page);

    await atDemoStep(page, 10, 'Try it on your app');
    await expect(page.getByTestId('demo-cta-end_card')).toBeVisible();
    await navBar(page).getByRole('button', { name: /^Finish/ }).click();
    // Nowhere to go: the console stays, the tour closes.
    await expect(page).toHaveURL(/\/forge\/demo/);
    expect(page.url()).not.toContain('tour=');

    // Zero requests to any other origin, and no RPC at all (the mock transport answers in the page).
    expect(log.urls.filter((url) => !url.startsWith(origin) && !url.startsWith('data:') && !url.startsWith('blob:'))).toEqual([]);
    expect(log.urls.filter((url) => /\/(reliant|controlplane)\.v1\./.test(url))).toEqual([]);
    expect(log.sockets).toEqual([]);
  });

  test('every path lands on the demo; the banner offers Install forge and Sign up', async ({ page }) => {
    await openDemo(page, '/auth?shell=demo');
    await expect(page).toHaveURL(/\/forge\/demo/, LOADED);
    await expect(page.getByRole('heading', { name: /sign in/i })).toHaveCount(0);
    const banner = page.getByTestId('forge-demo-banner');
    await expect(banner).toContainText('Nothing you click here changes anything.');
    await expect(banner.getByTestId('forge-demo-exit')).toHaveCount(0);
    await expect(banner.getByTestId('demo-cta-signup')).toHaveAttribute('href', /\/auth\?utm_source=demo&utm_medium=banner/);
    await banner.getByTestId('demo-cta-install').click();
    await expect(page.getByTestId('demo-install-sheet')).toContainText('brew install reliant-labs/reliant/forge');
  });

  test('a deep link keeps its step; a reload starts the tour again (nothing is remembered)', async ({ page }) => {
    await openDemo(page, '/forge/demo?shell=demo&view=traces&tour=forge:fg-trace');
    await atDemoStep(page, 3, 'One request, end to end');
    await navBar(page).getByRole('button', { name: /End tour/ }).click();
    await expect(navBar(page)).toHaveCount(0);
    await page.goto('/forge/demo', { waitUntil: 'domcontentloaded' });
    await atDemoStep(page, 1, "A forge app's console");
  });
});
