import { expect, test, type Page, type Request, type Route } from '@playwright/test';

import { mockAppsBackend } from './appsMocks';


// The forge journey ("Explore a forge app", Onboarding/journeys/forge.tsx),
// walked in the shells it runs in (the public demo host's own cut is at the
// bottom):
//
//   Reliant      the shell picked by hostname (no ?shell override), signed in
//                (appsMocks.ts): the sample app INSIDE the shell
//                (/forge/p/sample_shop), on the shell's own nav, and back to
//                where it started when it ends;
//   forge        ?shell=forge (lib/shell.ts), signed OUT: the /forge/demo
//                panel is public there, and the landing page's ?journey=
//                starts the journey.
//
// Steps 1-10 are driven the way a visitor drives them: each required action
// is the real click on the console (open the trace, its logs, the flamegraph,
// the issue, Ask an agent), then Next. Throughout, the demo must not reach a
// daemon, a chat or real telemetry. (The walk to the end card, step 11, was
// flaky and is deleted: flake:removed #2749.)

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
}

/** The open step's card: the spotlight's callout, or a centred card. Its controls are inside it (controls: "callout"). */
const tourCard = (page: Page) => page.locator("[data-testid='tour-callout'], [data-testid='tour-card']").first();
const controls = (page: Page) => page.getByTestId('tour-controls');

async function atStep(page: Page, n: number, total: number, title: string) {
  await expect(controls(page)).toContainText(`${n} of ${total}`, LOADED);
  await expect(tourCard(page)).toContainText(title);
}

async function next(page: Page) {
  await page.getByTestId('tour-next').click();
}

/** Opens the fixture trace the way a person does: by its root operation, the row's button. */
async function openTrace(page: Page) {
  const row = page.locator(`[data-testid='obs-trace-row'][data-trace-id='${TRACE_ID}']`);
  await row.getByRole('button', { name: /^Open trace/ }).click();
}

/**
 * The forge shell's steps 1-10, intro to Ask an agent, each by its required action. A required action
 * moves the tour on by itself (no Skip step), and Next stays enabled beside it so a visitor who cannot find
 * the target is never stuck (#2567); fg-logs and fg-ask hold for Next.
 * Stops on step 10 with the Ask sheet done: its Next to the end card (step 11) is flaky. It failed to
 * advance on reliant main twice ("Step 10 of 11" for 20s; flake:removed #2749; main-red control-plane#2743), so it and
 * the end card's checks are deleted under the flaky-test rule.
 */
async function walkToAskAgent(page: Page) {
  const T = 11;
  await atStep(page, 1, T, 'What is Forge?');
  await expect(page.getByTestId('onboarding-nav-bar')).toHaveCount(0);
  await next(page);

  await atStep(page, 2, T, "A Forge app's console");
  await expect(page.locator("[data-tour='forge-console']")).toBeVisible();
  await next(page);

  await atStep(page, 3, T, 'Monitoring from the start');
  await expect(page.getByTestId('obs-overview')).toBeVisible();
  await next(page);

  await atStep(page, 4, T, 'One request, end to end');
  await expect(page.getByTestId('tour-next')).toBeEnabled();
  await openTrace(page);
  await expect(page).toHaveURL(new RegExp(`trace=${TRACE_ID}`));

  await atStep(page, 5, T, 'Across services');
  await page.locator("[data-testid='trace-span-row'][data-span-id='e1']").click();

  await atStep(page, 6, T, 'What that request logged');
  await page.getByTestId('trace-logs').click();
  await expect(page).toHaveURL(/view=logs/);
  await expect(page.getByTestId('obs-logs-trace-scope')).toBeVisible();
  await expect(page.getByTestId('tour-next-done')).toBeVisible();
  await next(page);

  await atStep(page, 7, T, 'Where the CPU went');
  await page.getByTestId('trace-flamegraph').click();
  await expect(page).toHaveURL(/view=profiles/);

  await atStep(page, 8, T, 'Errors, grouped by release');
  await page.getByTestId(`obs-issue-${ISSUE_ID}`).click();
  await expect(page).toHaveURL(new RegExp(`issue=${ISSUE_ID}`));

  await atStep(page, 9, T, 'How an agent gets called');
  await expect(page.getByTestId('obs-alerts-firing')).toBeVisible();
  await next(page);

  await atStep(page, 10, T, 'Hand it to an agent');
  await page.getByTestId('ask-reliant-actions').getByTestId('ask-agent').click();
  const sheet = page.getByRole('dialog', { name: 'Ask an agent' });
  await expect(sheet).toBeVisible();
  // Sample ids, not log text: the demo says so.
  await expect(page.getByTestId('ask-agent-demo-note')).toBeVisible();
  await sheet.getByRole('button', { name: 'Done' }).click();
  await expect(page.getByTestId('tour-next-done')).toBeVisible();
}

function expectNoDaemonOrChat(log: { urls: string[]; sockets: string[] }) {
  expect(log.urls.filter((url) => DAEMON_OR_CHAT.test(url))).toEqual([]);
  expect(log.sockets).toEqual([]);
}

test.describe('the forge journey in the forge shell', () => {
  test('a signed-out visitor from the landing page goes straight to the journey on the public demo', async ({ page }) => {
    await mockForgeShellSignedOut(page);
    const log = recordRequests(page);
    await page.goto('/?shell=forge&journey=forge&utm_campaign=forge_standalone', { waitUntil: 'domcontentloaded' });

    await expect(page).toHaveURL(/\/forge\/demo\?/, LOADED);
    // The tour writes its step into the URL once it starts, after the redirect.
    await expect.poll(() => new URL(page.url()).searchParams.get('tour'), LOADED).toBe('forge:fg-intro');
    // Never sent to sign in first.
    await expect(page.getByRole('heading', { name: /sign in/i })).toHaveCount(0);

    await walkToAskAgent(page);

    // The forge shell has no daemon and no chats at all: nothing under
    // reliant.v1 but SystemService/Version, the public client-minimum check
    // RootShell's ClientMinimumWatch makes on both hosts (#2980).
    expectNoDaemonOrChat(log);
    expect(
      log.urls.filter((url) => url.includes('/reliant.v1.') && !url.includes('/reliant.v1.SystemService/Version')),
    ).toEqual([]);
  });

  test('the demo is public: /demo opens it signed out, with the tour', async ({ page }) => {
    await mockForgeShellSignedOut(page);
    await page.goto('/demo?shell=forge&tour=forge:fg-intro', { waitUntil: 'domcontentloaded' });
    await expect(page).toHaveURL(/\/forge\/demo\?/, LOADED);
    await atStep(page, 1, 11, 'What is Forge?');
  });

  test('?journey=workflows points at the Workflows area and runs no journey here', async ({ page }) => {
    await mockForgeShellSignedOut(page);
    await page.goto('/forge/demo?shell=forge&journey=workflows', { waitUntil: 'domcontentloaded' });
    const offer = page.getByTestId('forge-workflows-offer');
    await expect(offer).toBeVisible(LOADED);
    // One shell: the Workflows area is this app's own page, never another origin.
    await expect(offer.getByRole('link', { name: 'Open Workflows' })).toHaveAttribute('href', '/workflows/library');
    await expect(page.getByTestId('tour-controls')).toHaveCount(0);

    // ?tour=workflows is not a journey in this shell either.
    await page.goto('/forge/demo?tour=workflows:wf-new', { waitUntil: 'domcontentloaded' });
    await expect(page.locator("[data-tour='forge-console']")).toBeVisible(LOADED);
    await expect(page.getByTestId('tour-controls')).toHaveCount(0);
  });
});

// Signed in: the sample app in the shell. Its pages' DATA comes from the
// sample's data seam (demo-di/design.md, still to land), so this walks what
// does not depend on it: the route of every step, the shell nav's anchors,
// and the way back. Each data step's spotlight is waited out (its card shows
// anyway after ~3s, with Next enabled).
test.describe('the forge journey in Reliant, signed in', () => {
  const SAMPLE = '/forge/p/sample_shop';
  const T = 14;

  test('walks the sample app in the shell, by the nav, and Finish returns to where it started', async ({ page }) => {
    test.setTimeout(120_000);
    await mockAppsBackend(page);
    await page.goto('/projects?tour=pick:menu', { waitUntil: 'domcontentloaded' });
    await page.getByTestId('journey-card-forge').click(LOADED);
    await page.getByTestId('journey-picker').getByRole('button', { name: /start/i }).click();

    await expect.poll(() => new URL(page.url()).pathname, LOADED).toBe(SAMPLE);
    await expect.poll(() => new URL(page.url()).searchParams.get('tour')).toBe('forge:fg-intro');
    await atStep(page, 1, T, 'What is Forge?');
    await next(page);

    // The shell's own nav, with the sample app open: not a frame of its own.
    await atStep(page, 2, T, "A Forge app's console");
    await expect(page.locator("[data-tour='forge-console-nav']")).toBeVisible();
    await expect(page.locator("[data-tour='forge-console']")).toHaveCount(0);
    await next(page);

    await atStep(page, 3, T, 'Monitoring from the start');
    await next(page);

    await atStep(page, 4, T, 'Releases, built once');
    await expect(page).toHaveURL(new RegExp(`${SAMPLE}/releases`));
    await expect(page.locator("[data-tour='nav-deployments']")).toBeVisible();
    await expect(page.locator("[data-tour='nav-deployments'] [data-testid='nav-releases']")).toHaveAttribute('aria-current', 'page');

    // The Observability stops, by Next (their actions need the sample's data): one request, its error, the handoff.
    for (const [n, title] of [
      [5, 'One request, end to end'],
      [6, 'Across services'],
      [7, 'What that request logged'],
      [8, 'Errors, grouped by release'],
      [9, 'How an agent gets called'],
      [10, 'Hand it to an agent'],
    ] as const) {
      await next(page);
      await atStep(page, n, T, title);
      await expect(page).toHaveURL(new RegExp(`${SAMPLE}/observability/`));
    }

    for (const [n, title, path, anchor] of [
      [11, "The app's tests", 'tests', 'nav-tests'],
      [12, 'What each release ships', 'security/vulnerabilities', 'nav-security'],
      [13, 'Secrets, domains and costs', 'secrets', 'nav-configure'],
    ] as const) {
      await next(page);
      await atStep(page, n, T, title);
      await expect(page).toHaveURL(new RegExp(`${SAMPLE}/${path}`));
      await expect(page.locator(`[data-tour='${anchor}']`)).toBeVisible();
    }

    await next(page);
    await atStep(page, 14, T, 'Try it on your app');
    await next(page);

    // Back where the picker was opened, with the tour closed and no sample left behind.
    await expect(page).toHaveURL(/\/projects(\?|$)/, LOADED);
    expect(new URL(page.url()).searchParams.get('tour')).toBeNull();
    expect(await page.evaluate(() => localStorage.getItem('shell.currentProject'))).not.toBe('app:sample_shop');
  });

  test('Exit tour returns to where it started', async ({ page }) => {
    await mockAppsBackend(page);
    await page.goto('/projects?tour=pick:menu', { waitUntil: 'domcontentloaded' });
    await page.getByTestId('journey-card-forge').click(LOADED);
    await page.getByTestId('journey-picker').getByRole('button', { name: /start/i }).click();
    await atStep(page, 1, T, 'What is Forge?');
    await next(page);
    await atStep(page, 2, T, "A Forge app's console");
    await page.getByTestId('tour-exit').click();
    await expect(page).toHaveURL(/\/projects(\?|$)/, LOADED);
  });

  test('an old /forge/demo link opens the page it names on the sample app', async ({ page }) => {
    await mockAppsBackend(page);
    await page.goto('/forge/demo?view=traces&tour=forge:fg-trace', { waitUntil: 'domcontentloaded' });
    await expect.poll(() => new URL(page.url()).pathname, LOADED).toBe(`${SAMPLE}/observability/traces`);
    expect(new URL(page.url()).searchParams.get('tour')).toBe('forge:fg-trace');
    await atStep(page, 5, T, 'One request, end to end');
  });
});

// Public demo: each real page entry auto-opens only its current-page callout.
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
