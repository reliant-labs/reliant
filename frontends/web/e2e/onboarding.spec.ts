import { test, expect, type Page, type Route } from '@playwright/test';

// ---------------------------------------------------------------------------
// Current onboarding model (see web/src/components/OnboardingFlow/stepConfig.ts)
//
//   ONBOARDING_STEPS = ['compute', 'model', 'checkout', 'project-choice', 'github-connect', 'finish']
//
// There is no `?step=` URL param anymore — the current step is *derived* from
// a `plan` search-param object on `/onboarding` (see deriveStep in
// stepConfig.ts). Tests either drive the flow by clicking through from a
// fresh `/onboarding` load, or seed `?plan=<json>` directly to land deeper in
// the flow without re-clicking every prior step.
//
// The app also now sits behind AuthGuard: `/onboarding` is nested under the
// `_authenticated` layout route, so an unauthenticated visit renders the
// sign-in screen instead of onboarding. Tests authenticate via the API-key
// login path (`localStorage['reliant-api-key']`), which is honored by
// AuthGuard/authStore without needing a real Supabase session.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** The mocked server's project table, per page: CreateProject adds to it, ListProjects answers with it. */
const mockProjects = new WeakMap<Page, unknown[]>();

function serverProjects(page: Page): unknown[] {
  let projects = mockProjects.get(page);
  if (!projects) {
    projects = [];
    mockProjects.set(page, projects);
  }
  return projects;
}

/**
 * Intercept Connect-protocol gRPC calls and return canned success responses.
 *
 * Route registration order matters to Playwright: the LAST matching
 * `page.route()` call wins. So this registers broad `**` fallbacks for each
 * service package FIRST, then specific per-RPC overrides after, letting the
 * specific ones take priority over the generic empty-object fallback.
 */
async function mockGrpcRoutes(page: Page) {
  // Exercise the cloud-enabled onboarding contract in dev and preview alike;
  // never inherit the developer's deployed control-plane/auth configuration.
  const origin = new URL(test.info().project.use.baseURL ?? 'http://localhost:5173').origin;
  await page.route('**/config.js', route => route.fulfill({
    contentType: 'text/javascript',
    body: `window.__FORGE_CONFIG__ = ${JSON.stringify({ CONTROL_PLANE_API_URL: origin, SUPABASE_URL: `${origin}/supabase`, SUPABASE_ANON_KEY: 'anon-test-key' })};`,
  }));
  await page.route('**/supabase/**', route => route.fulfill({ json: {} }));
  // Broad fallbacks — any RPC not explicitly overridden below gets `{}`.
  await page.route('**/reliant.v1.**', (route: Route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({}),
    }),
  );
  await page.route('**/controlplane.v1.**', (route: Route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({}),
    }),
  );

  // ListProjects / CreateProject are STATEFUL, like GetCurrentUser below: a new
  // user starts with no projects (which triggers onboarding), and a project the
  // app creates is listed from then on, because that is what the real server
  // does. A ListProjects pinned to `[]` forever fought the app on the way out:
  // projectStore.loadProjects refetches once its 5s cooldown has passed, so on
  // a slow runner the onboarding finish step's re-list (or the workspace
  // restore on `/`) dropped the project it had just created, restoreLastProject
  // found nothing, and the user was left on `/?tour=…` instead of
  // `/project/<id>` — a mock artifact that only showed up when the run was slow.
  mockProjects.set(page, []);
  await page.route('**/reliant.v1.ProjectService/ListProjects', (route: Route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ projects: serverProjects(page) }),
    }),
  );

  // CreateProject → success stub.
  await page.route('**/reliant.v1.ProjectService/CreateProject', (route: Route) => {
    const project = { id: 'proj_test_1', name: 'test-project', path: '/tmp/test' };
    serverProjects(page).push(project);
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ project }),
    });
  });

  // Local daemon registry (reliant.v1.DaemonRegistryService) — no daemon by
  // default. ComputeStep polls this to decide whether to auto-skip.
  await page.route('**/reliant.v1.DaemonRegistryService/ListDaemons', (route: Route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ daemons: [] }),
    }),
  );

  // Provider API key validate/save — ModelStep's BYO-key path.
  await page.route('**/reliant.v1.SettingsService/ValidateProviderAPIKey', (route: Route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ valid: true, message: 'ok' }),
    }),
  );
  await page.route('**/reliant.v1.ProviderAccountService/AddProviderAPIKey', (route: Route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ created: true }),
    }),
  );

  // Managed Reliant key — the commit's `grant_ai_access` task for a
  // `reliant_credits` plan (commitLaunchPlan.ts). The real handler
  // (internal/grpc/services/settings.go SyncReliantProvider) answers success
  // with `synced: true`; the `{}` fallback above reads as `synced: false`,
  // which is a FAILED grant — a state the server never reports on success.
  await page.route('**/reliant.v1.SettingsService/SyncReliantProvider', (route: Route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ success: true, synced: true }),
    }),
  );

  // Cloud user + eligibility — a fresh, not-yet-onboarded, cloud-eligible user.
  //
  // GetCurrentUser is STATEFUL: it starts as not-onboarded and flips once
  // CompleteOnboarding is called, because that is what the real server does.
  // A mock pinned to `false` forever fights the app on the way out of the
  // flow: useCompleteOnboarding optimistically sets onboardingCompleted=true
  // (specifically so ModernApp does not bounce back to /onboarding) and then
  // invalidates, and a refetch answering `false` undoes the optimistic write.
  // ModernApp's redirect then correctly fires and the user is pulled back to
  // /onboarding — a mock artifact that looks exactly like a routing bug.
  let onboardingCompleted = false;
  await page.route('**/controlplane.v1.UserService/CompleteOnboarding', (route: Route) => {
    onboardingCompleted = true;
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({}),
    });
  });
  await page.route('**/controlplane.v1.UserService/GetCurrentUser', (route: Route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ user: { id: 'user_test', onboardingCompleted } }),
    }),
  );
  await page.route(
    '**/controlplane.v1.BillingService/GetCurrentUserComputeEligibility',
    (route: Route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          eligible: true,
          hasActiveSubscription: true,
          grantedSmallDaemonSecondsRemaining: '0',
          planName: 'free',
        }),
      }),
  );

  // Wallet overview — `requiresPayment`'s other leg (see
  // useOnboardingFacts.ts). Funded by default, matching the funded compute
  // eligibility above: a fresh, not-yet-onboarded, entitled user. A funded
  // wallet keeps `reliant_credits` plans off the checkout step, same as an
  // eligible account keeps cloud compute off it.
  await page.route(
    '**/controlplane.v1.BillingService/GetCurrentUserWalletOverview',
    (route: Route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          overview: { wallet: { balanceUsdNanos: '1000000000' } },
        }),
      }),
  );

  // Cloud daemon list/create — empty list, successful create.
  await page.route('**/controlplane.v1.DaemonService/ListDaemons', (route: Route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ daemons: [] }),
    }),
  );
  await page.route('**/controlplane.v1.DaemonService/CreateDaemon', (route: Route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        daemon: { id: 'daemon_1', name: 'onboarding-daemon', status: 1 },
      }),
    }),
  );

  // Health check — used by useAuthMode to decide the sign-in surface.
  await page.route('**/health', (route: Route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ auth_mode: 'apikey' }),
    }),
  );
}

/**
 * Authenticate via the API-key login path, which AuthGuard/authStore accept
 * without a real Supabase session (see authStore.initialize()'s
 * `reliant-api-key` branch). Must run via addInitScript so the key is present
 * before authStore.initialize() runs on the first render.
 */
async function loginWithApiKey(page: Page) {
  await page.addInitScript(() => {
    localStorage.setItem('reliant-api-key', 'test-key-123');
  });
}

/**
 * Answer every call to `url` with `body`, but only once `release()` is called.
 *
 * The local finish step (FinishStep.tsx) has no question to ask: it finalizes
 * on mount, and "Setting up your project…" is only its loading state. Against
 * these mocks the whole sequence settles in tens of milliseconds — usually
 * before `page.goto` has even resolved — so asserting on that text without
 * holding anything is a race the test loses whenever the page's `load` event
 * is late. Holding the RPC the step is waiting on keeps it on screen for
 * exactly as long as the test needs, and `release()` then lets the step
 * finish so the test can assert where it ends up.
 */
async function holdRpc(page: Page, url: string, body: unknown, onRelease?: () => void) {
  let release!: () => void;
  const released = new Promise<void>((resolve) => {
    release = resolve;
  });
  const requests: Record<string, unknown>[] = [];
  await page.route(url, async (route: Route) => {
    requests.push(route.request().postDataJSON() as Record<string, unknown>);
    await released;
    // Before the response, as a real server commits before it answers.
    onRelease?.();
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify(body),
    });
  });
  return { release, requests };
}

/** {@link holdRpc} for CreateProject: once released, the project is on the mocked server's list too. */
async function holdCreateProject(page: Page, project: Record<string, unknown>) {
  return holdRpc(page, '**/reliant.v1.ProjectService/CreateProject', { project }, () => {
    serverProjects(page).push(project);
  });
}

/** Navigate straight to `/onboarding` and wait for the dialog to mount. */
async function gotoOnboarding(page: Page) {
  await page.goto('/onboarding');
  await page.getByRole('dialog', { name: 'Onboarding setup' }).waitFor({ timeout: 10_000 });
}

/**
 * Navigate to `/onboarding` with a seeded plan, landing directly on whatever
 * step `deriveStep` computes for that plan — see stepConfig.ts. Lets a test
 * exercise a later step without re-clicking every step before it.
 */
async function gotoOnboardingWithPlan(page: Page, plan: Record<string, unknown>) {
  const planParam = encodeURIComponent(JSON.stringify(plan));
  await page.goto(`/onboarding?plan=${planParam}`);
  await page.getByRole('dialog', { name: 'Onboarding setup' }).waitFor({ timeout: 10_000 });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe('Onboarding Flow', () => {
  test.beforeEach(async ({ page }) => {
    await mockGrpcRoutes(page);
    await loginWithApiKey(page);
  });

  // Every plan includes 160 hours at its OWN size, so its small-hour total is
  // 160 x that size's multiplier (the 2XL plan holds 2,560 small hours).
  async function mockPricing(page: Page) {
    const sizes = ['small', 'medium', 'large', 'xl'];
    await page.route('**/controlplane.v1.BillingService/ListPlans', route => route.fulfill({
      json: {
        plans: sizes.map((size, index) => ({
          id: `compute_${size}`, kind: 'compute', name: `Compute ${size}`, priceCents: String(2000 * (index + 1)), displayOrder: index,
          structuredLimits: { allowedDaemonSizes: sizes.slice(0, index + 1), includedSmallDaemonHours: String(160 * 2 ** index) },
        })),
        daemonPricing: {
          sizes: sizes.map((size, index) => ({ size, multiplier: String(2 ** index), hourlyPriceCents: String(13 * 2 ** index), storageGib: String([120, 140, 180, 260][index]) })),
          suspendedDiskCentsPerGibMonth: '25', placeholder: false,
        },
      },
    }));
  }

  const FOOTNOTE = 'Includes 160 hours a month. Extra hours and storage are billed separately.';
  const BANNED = /GiB|\/hr|hrs|while suspended|per month\b.*GB|after included hours|uses included hours|as fast|small-daemon|hours\/month included|2,?560|1,?280/;

  test('onboarding plan rows: name, spec and monthly price only', async ({ page }, testInfo) => {
    page.on('pageerror', error => console.error(error));
    await mockPricing(page);
    // Not covered by a coupon: every row shows its monthly price.
    await page.route('**/controlplane.v1.BillingService/GetCurrentUserComputeEligibility', route =>
      route.fulfill({ json: { eligible: false, hasActiveSubscription: false, grantedSmallDaemonSecondsRemaining: '0', planName: '' } }));
    await gotoOnboarding(page);
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    for (const price of ['$20.00/mo', '$40.00/mo', '$60.00/mo', '$80.00/mo']) {
      await expect(dialog.getByText(price, { exact: true })).toBeVisible();
    }
    for (const spec of ['2 GB RAM · 0.5 CPU', '4 GB RAM · 1 CPU', '8 GB RAM · 2 CPU', '16 GB RAM · 4 CPU']) {
      await expect(dialog.getByText(spec, { exact: true })).toBeVisible();
    }
    await expect(dialog.getByTestId('machine-pricing-line')).toHaveText(FOOTNOTE);
    await expect(dialog.getByText(BANNED)).toHaveCount(0);
    await page.screenshot({ path: testInfo.outputPath('onboarding-plan-rows.png'), fullPage: true });
  });

  test('add machine sheet: name and spec per size, included terms without duplicate prices', async ({ page }, testInfo) => {
    page.on('pageerror', error => console.error(error));
    await mockPricing(page);
    await page.route('**/controlplane.v1.UserService/GetCurrentUser', route =>
      route.fulfill({ json: { user: { id: 'user_test', onboardingCompleted: true } } }));
    await page.route('**/controlplane.v1.BillingService/GetCurrentUserComputeEligibility', route =>
      route.fulfill({ json: { eligible: true, hasActiveSubscription: true, grantedSmallDaemonSecondsRemaining: '0', planName: 'Compute xl', allowedDaemonSizes: ['small', 'medium', 'large', 'xl'] } }));
    // The XL plan: 160 own-size hours = 1,280 small hours.
    await page.route('**/controlplane.v1.BillingService/GetCurrentUserComputeUsage', route =>
      route.fulfill({ json: { usageMeasured: true, includedSmallDaemonSeconds: String(1280 * 3600), remainingSmallDaemonSeconds: String(3 * 3600) } }));

    await page.goto('/settings/machines');
    await page.getByRole('button', { name: 'Add machine', exact: true }).first().click();
    const modal = page.getByRole('dialog', { name: 'Add a machine', exact: true });
    const sizes = modal.getByRole('radiogroup', { name: 'Machine' });
    for (const [name, spec] of [
      ['Small', '2 GB RAM · 0.5 CPU'], ['Medium', '4 GB RAM · 1 CPU'],
      ['Large', '8 GB RAM · 2 CPU'], ['XL', '16 GB RAM · 4 CPU'],
    ]) {
      const size = sizes.getByRole('radio', { name: new RegExp(`^${name}\\b`) });
      await expect(size).toBeVisible();
      await expect(size).toContainText(spec);
      await expect(size).toContainText('On your plan');
    }
    await expect(sizes).not.toContainText('$');
    await sizes.getByRole('radio', { name: /^Small\b/ }).click();
    await modal.getByRole('button', { name: 'Advanced: name, auto-suspend, repository' }).click();
    await expect(modal.getByRole('textbox', { name: 'Name', exact: true })).toBeVisible();
    await expect(modal.getByText(BANNED)).toHaveCount(0);
    await page.screenshot({ path: testInfo.outputPath('create-machine-modal.png'), fullPage: true });
  });

  test('shows onboarding for a new (not-yet-onboarded) user at /onboarding', async ({ page }) => {
    await gotoOnboarding(page);

    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog).toBeVisible();

    // First step is Compute — verify the heading is visible.
    await expect(dialog.getByText('Where should Reliant run your code?')).toBeVisible();
    await expect(dialog.getByRole('button', { name: /^Continue with / })).toBeVisible();
  });

  test('landing on / redirects a not-yet-onboarded user to /onboarding', async ({ page }) => {
    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');

    await expect(page).toHaveURL(/\/onboarding/, { timeout: 15_000 });
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog).toBeVisible({ timeout: 15_000 });
  });

  test('a user who has already completed onboarding is not sent back to /onboarding', async ({ page }) => {
    // Override GetCurrentUser to a completed user with an existing project.
    await page.route('**/controlplane.v1.UserService/GetCurrentUser', (route: Route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ user: { id: 'user_test', onboardingCompleted: true } }),
      }),
    );
    await page.route('**/reliant.v1.ProjectService/ListProjects', (route: Route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          projects: [
            {
              id: 'proj_1',
              name: 'My Project',
              path: '/tmp/my-project',
              is_git_repo: false,
              worktree_count: 0,
              last_active: new Date().toISOString(),
              created_at: new Date().toISOString(),
              updated_at: new Date().toISOString(),
            },
          ],
        }),
      }),
    );

    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');

    // Should NOT redirect to onboarding.
    await expect(page).not.toHaveURL(/\/onboarding/, { timeout: 10_000 });
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog).not.toBeVisible({ timeout: 10_000 });

    // The main app root should have rendered content (not a blank screen).
    const root = page.locator('#root');
    await expect(root).toBeAttached();
    const childCount = await root.evaluate((el) => el.childNodes.length);
    expect(childCount).toBeGreaterThan(0);
  });

  // ── Compute → Model ─────────────────────────────────────────

  test('Compute: choosing cloud advances to the Model step', async ({ page }) => {
    await gotoOnboarding(page);
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });

    await dialog.getByRole('button', { name: /^Continue with / }).click();

    await expect(dialog.getByText('Which AI should Reliant use?')).toBeVisible({ timeout: 10_000 });
    // chooseCloud() records `compute: "cloud_paid"` — see ComputeStep.tsx.
    // `cloud_free_trial` is a legacy ComputeChoice value nothing in the UI
    // writes any more (kept only so isCloudCompute() and old bookmarked URLs
    // still resolve).
    await expect(page).toHaveURL(/cloud_paid/, { timeout: 10_000 });
  });

  test('Compute: "Use your own computer" shows self-hosted connect instructions on the same step', async ({ page }) => {
    await gotoOnboarding(page);
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });

    await dialog.getByRole('button', { name: 'Use your own computer' }).click();

    // Stays on the compute step (URL keeps no `compute` plan field) — the
    // local-daemon instructions render below the machine list rather than
    // navigating. "inline" in the old title meant "without navigating", but
    // it now reads as a claim about POSITION that is no longer true: the
    // panel sits at the bottom of the step, not inside the list.
    await expect(
      dialog.getByText('Install Reliant on that machine', { exact: true }),
    ).toBeVisible({ timeout: 5_000 });
    await expect(dialog.getByText('reliant daemon start', { exact: true })).toBeVisible();
    await expect(dialog.getByRole('textbox', { name: 'Pairing code' })).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Continue', exact: true })).toBeDisabled();

    // The cloud CTA is gone while this path is selected — it would offer to
    // undo the choice the user just made, as the only primary button on the
    // step.
    await expect(dialog.getByRole('button', { name: /^Continue with / })).toHaveCount(0);
  });

  test('Compute: an already-connected local daemon auto-advances to Model', async ({ page }) => {
    // A daemon already registered in the local reliant.v1 registry.
    await page.route('**/reliant.v1.DaemonRegistryService/ListDaemons', (route: Route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          daemons: [
            { daemonId: 'd1', userId: 'u1', hostname: 'host', platform: 'mac', status: 1 },
          ],
        }),
      }),
    );

    await gotoOnboarding(page);
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });

    // Auto-skips straight past the compute step to Model.
    await expect(dialog.getByText('Which AI should Reliant use?')).toBeVisible({ timeout: 10_000 });
    await expect(page).toHaveURL(/local_daemon/, { timeout: 10_000 });
  });

  // ── Model → Project-choice / Finish ─────────────────

  test('Model: saving a BYO Anthropic key on the cloud path advances to What do you want to do first?', async ({ page }) => {
    await gotoOnboardingWithPlan(page, { compute: 'cloud_free_trial' });
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog.getByText('Which AI should Reliant use?')).toBeVisible();

    await dialog.getByRole('button', { name: 'Anthropic API', exact: true }).click();
    await dialog.getByPlaceholder('sk-ant-...').fill('sk-ant-test-key');
    await dialog.getByRole('button', { name: 'Save key and start' }).click();

    // Cloud path lands on project-choice ("What do you want to do first?").
    await expect(dialog.getByText('What do you want to do first?')).toBeVisible({ timeout: 10_000 });
    await expect(page).toHaveURL(/modelProvider.*anthropic|anthropic.*modelProvider/, {
      timeout: 10_000,
    });
  });

  test('Model: saving a BYO key on the local path advances to the finish step', async ({ page }) => {
    // Held so the finish step's loading state stays on screen — see holdRpc.
    const createProject = await holdCreateProject(page, {
      id: 'proj_default', name: 'my-project', path: '~/Projects/my-project',
    });

    await gotoOnboardingWithPlan(page, { compute: 'local_daemon' });
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog.getByText('Which AI should Reliant use?')).toBeVisible();

    await dialog.getByRole('button', { name: 'Anthropic API', exact: true }).click();
    await dialog.getByPlaceholder('sk-ant-...').fill('sk-ant-test-key');
    await dialog.getByRole('button', { name: 'Save key and start' }).click();

    // Local path skips project-choice/github-connect and lands directly on
    // finish ("Setting up your project") — see deriveStep in stepConfig.ts.
    await expect(dialog.getByText('Setting up your project')).toBeVisible({ timeout: 10_000 });

    // Own key + local compute: nothing to provision, so the step exits itself.
    createProject.release();
    await expect(page).not.toHaveURL(/\/onboarding/, { timeout: 10_000 });
  });

  // ── Project-choice (cloud): Start new vs Connect GitHub ─────

  test('Project-choice: "Build an app" (cloud) creates a project and leaves onboarding', async ({ page }) => {
    // DaemonConnectingGate reads the daemon registry (reliant.v1), not the
    // control-plane list, and treats DAEMON_STATUS_ACTIVE as connected.
    await page.route('**/reliant.v1.DaemonRegistryService/ListDaemons', (route: Route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          daemons: [{ daemonId: 'daemon_1', hostname: 'onboarding-daemon', status: 'DAEMON_STATUS_ACTIVE' }],
        }),
      }),
    );

    await gotoOnboardingWithPlan(page, {
      compute: 'cloud_free_trial',
      modelProvider: 'anthropic',
    });
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog.getByText('What do you want to do first?')).toBeVisible();

    await dialog.getByRole('button', { name: /Build an app/i }).click();

    // completeOnboarding + ensureProject succeed, the commit provisions the
    // machine, and the gate reports it connected — Continue is the only exit.
    await expect(dialog.getByText('Your machine is ready')).toBeVisible({ timeout: 10_000 });
    await dialog.getByRole('button', { name: 'Continue' }).click();
    await expect(page).not.toHaveURL(/\/onboarding/, { timeout: 10_000 });
  });

  test('Project-choice: "Connect GitHub" (cloud, no credential) advances to the GitHub connect step', async ({ page }) => {
    await gotoOnboardingWithPlan(page, {
      compute: 'cloud_free_trial',
      modelProvider: 'anthropic',
    });
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog.getByText('What do you want to do first?')).toBeVisible();

    // Simulate an existing_codebase plan seeded directly (equivalent to the
    // OAuth round trip having already happened) — deriveStep sends an
    // existing_codebase + cloud plan straight to github-connect.
    await gotoOnboardingWithPlan(page, {
      compute: 'cloud_free_trial',
      modelProvider: 'anthropic',
      intent: 'existing_codebase',
    });
    await expect(dialog.getByText('Choose a repository')).toBeVisible({ timeout: 10_000 });
  });

  // ── Finish (local): auto-finalizes onto the existing project ───────────

  test('Finish: local compute reuses an existing project and exits without a picker', async ({ page }) => {
    // Held, or the step finishes and leaves before the dialog can be waited
    // for — see holdRpc.
    const listProjects = await holdRpc(page, '**/reliant.v1.ProjectService/ListProjects', {
      projects: [
        {
          id: 'proj_existing',
          name: 'my-proj',
          path: '/home/me/my-proj',
          is_git_repo: false,
          worktree_count: 0,
          last_active: new Date().toISOString(),
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        },
      ],
    });
    const createProjectCalls: unknown[] = [];
    await page.route('**/reliant.v1.ProjectService/CreateProject', (route: Route) => {
      createProjectCalls.push(route.request().postDataJSON());
      return route.fulfill({ status: 500, contentType: 'application/json', body: '{}' });
    });

    await gotoOnboardingWithPlan(page, { compute: 'local_daemon', modelProvider: 'anthropic' });
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog.getByText('Setting up your project')).toBeVisible();

    // No click: the finish step selects the existing project itself. Local
    // compute + own key leaves the commit nothing to provision, so
    // ProvisioningGate's "nothing to do" branch exits onboarding by itself
    // (#289); no Continue button is rendered on this path.
    listProjects.release();
    await expect(page).toHaveURL((url) => url.pathname === '/' && url.searchParams.get('tour') === 'chat-and-sidebars', { timeout: 10_000 });
    await expect.poll(() => page.evaluate(() => JSON.parse(localStorage.getItem('workspace-state') ?? '{}').state?.lastProjectId)).toBe('proj_existing');
    await expect(dialog).toBeHidden();
    expect(createProjectCalls).toEqual([]);
  });

  // ── Back button ─────────────────────────────────────────────

  test('Back button is hidden on the first step and returns to Compute from Model', async ({ page }) => {
    await gotoOnboarding(page);
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });

    // First step (compute): no Back button.
    await expect(dialog.getByRole('button', { name: /Back/i })).not.toBeVisible();

    await dialog.getByRole('button', { name: /^Continue with / }).click();
    await expect(dialog.getByText('Which AI should Reliant use?')).toBeVisible({ timeout: 10_000 });

    // Back button now visible in the footer, and returns to Compute.
    const backButton = dialog.getByRole('button', { name: /Back/i });
    await expect(backButton).toBeVisible();
    await backButton.click();

    await expect(dialog.getByText('Where should Reliant run your code?')).toBeVisible({
      timeout: 5_000,
    });
  });

  // ── Direct URL / plan-driven navigation ─────────────────────

  test('Direct navigation with a seeded plan lands on the corresponding step', async ({ page }) => {
    await gotoOnboardingWithPlan(page, { compute: 'cloud_free_trial' });
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog).toBeVisible({ timeout: 10_000 });
    await expect(dialog.getByText('Which AI should Reliant use?')).toBeVisible({ timeout: 5_000 });
  });

  test('An unrecognized top-level search param (no plan key) is ignored, landing on the first step', async ({ page }) => {
    // Old bookmarked links like `?step=goal` from the pre-plan-model UI:
    // `step` isn't part of onboardingSearchSchema, so tanstack-router strips
    // it silently and the app falls through to the default (no plan → compute).
    await page.goto('/onboarding?step=goal');
    await page.getByRole('dialog', { name: 'Onboarding setup' }).waitFor({ timeout: 10_000 });

    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog.getByText('Where should Reliant run your code?')).toBeVisible({
      timeout: 5_000,
    });
  });
});

// ---------------------------------------------------------------------------
// Failure Scenarios
// ---------------------------------------------------------------------------

/**
 * The state a REAL new user is in on the Compute step.
 *
 * mockGrpcRoutes seeds `eligible: true`, which is the funded path every other
 * test here exercises. But the compute auto-grant at signup is gone, so an
 * actual brand-new account comes back NO_SUBSCRIPTION.
 *
 * Unlike the earlier "dead grey button" behavior this used to pin, the cloud
 * choice is now ALWAYS offered (see ComputeStep.tsx: "ENTITLEMENT NO LONGER
 * GATES THE CHOICE") — an ineligible cloud pick simply routes to the
 * checkout step instead of a dead end, so there is no billing exit left in
 * this step to promote. The coupon field is still offered alongside it.
 */
test.describe('Onboarding – Compute with no billing (the new-user default)', () => {
  test.beforeEach(async ({ page }) => {
    await mockGrpcRoutes(page);
    // Override the funded default from mockGrpcRoutes: no subscription, no
    // granted minutes — a fresh account that has never paid.
    await page.route(
      '**/controlplane.v1.BillingService/GetCurrentUserComputeEligibility',
      (route: Route) =>
        route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            eligible: false,
            reason: 3, // COMPUTE_INELIGIBLE_REASON_NO_SUBSCRIPTION
            hasActiveSubscription: false,
            grantedSmallDaemonSecondsRemaining: '0',
            planName: '',
          }),
        }),
    );
    await loginWithApiKey(page);
  });

  test('offers the cloud choice even when ineligible, alongside the coupon field', async ({
    page,
  }) => {
    await gotoOnboarding(page);
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });

    // No longer dead: an ineligible cloud choice records itself and routes
    // to the checkout step, so the button stays enabled rather than absent.
    await expect(dialog.getByRole('button', { name: /^Continue with / })).toBeEnabled();

    // A code still skips the checkout step, so it stays on offer too.
    await expect(dialog.getByRole('button', { name: /Have a coupon code/i })).toBeEnabled();

    // And the self-hosted path — which needs no billing at all — is still here.
    await expect(dialog.getByRole('button', { name: 'Use your own computer' })).toBeEnabled();
  });

  test('no control on the Compute step is disabled', async ({ page }) => {
    await gotoOnboarding(page);
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await dialog.getByRole('button', { name: /Have a coupon code/i }).waitFor();

    // A disabled control here is the whole defect, in any form.
    const buttons = await dialog.getByRole('button').all();
    expect(buttons.length).toBeGreaterThan(0);
    for (const button of buttons) {
      await expect(button).toBeEnabled();
    }
  });

  test('the coupon field opens in place, so redeeming never leaves onboarding', async ({
    page,
  }) => {
    await gotoOnboarding(page);
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });

    await dialog.getByRole('button', { name: /Have a coupon code/i }).click();

    await expect(dialog.getByPlaceholder(/Enter code/i)).toBeVisible();
    await expect(dialog.getByRole('button', { name: /^Redeem$/ })).toBeEnabled();
    // Still on Compute — the step did not navigate away.
    await expect(dialog.getByText('Where should Reliant run your code?')).toBeVisible();
  });
});

test.describe('Onboarding – Failure Scenarios', () => {
  test.beforeEach(async ({ page }) => {
    await mockGrpcRoutes(page);
    await loginWithApiKey(page);
  });

  test('gRPC backend unreachable: app still renders (no blank screen)', async ({ page }) => {
    // Override every reliant.v1./controlplane.v1. fetch/XHR call to 500 —
    // leave document/script/module requests alone.
    await page.route('**/*', (route: Route) => {
      const resourceType = route.request().resourceType();
      if (resourceType !== 'fetch' && resourceType !== 'xhr') {
        return route.fallback();
      }
      const url = route.request().url();
      if (url.includes('reliant.v1.') || url.includes('controlplane.v1.')) {
        return route.fulfill({ status: 500, body: 'Internal Server Error' });
      }
      return route.fallback();
    });

    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');

    const root = page.locator('#root');
    await expect(root).toBeAttached({ timeout: 10_000 });
    // #root exists before React's async auth bootstrap resolves. Wait on
    // rendered children, not the synchronous empty container at document load.
    await expect.poll(() => root.evaluate((el) => el.childNodes.length)).toBeGreaterThan(0);
  });

  test('Cloud daemon creation fails: the provisioning gate reports it, and lets the user continue anyway', async ({ page }) => {
    // ComputeStep no longer calls CreateDaemon at all — it only records the
    // choice (see ComputeStep.tsx's chooseCloud). The billable call moved to
    // the commit point (commitLaunchPlan.ts's provisionDaemon), which runs
    // once, after completeOnboarding, from the terminal step. So a failure
    // here surfaces on ProvisioningGate, not back on Compute.
    await page.route('**/controlplane.v1.DaemonService/CreateDaemon', (route: Route) =>
      route.fulfill({
        status: 500,
        contentType: 'application/json',
        body: JSON.stringify({ code: 'internal', message: 'Internal server error' }),
      }),
    );

    await gotoOnboardingWithPlan(page, {
      compute: 'cloud_free_trial',
      modelProvider: 'anthropic',
    });
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog.getByText('What do you want to do first?')).toBeVisible();

    await dialog.getByRole('button', { name: /Build an app/i }).click();

    // The machine task reports failed; the user is not trapped in the
    // wizard for a provisioning failure — Continue stays enabled (see
    // ProvisioningGate's "PARTIAL as well as COMPLETE" comment) alongside a
    // Try again retry.
    await expect(dialog.getByText('Your machine')).toBeVisible({ timeout: 10_000 });
    await expect(dialog.getByRole('button', { name: 'Continue' })).toBeEnabled();
    await expect(dialog.getByRole('button', { name: 'Try again' })).toBeEnabled();
  });

  test('Project creation fails during completion: shows an error, keeps the dialog open', async ({ page }) => {
    await page.route('**/reliant.v1.ProjectService/CreateProject', (route: Route) =>
      route.fulfill({
        status: 500,
        contentType: 'application/json',
        body: JSON.stringify({ code: 13, message: 'Internal server error' }),
      }),
    );

    await gotoOnboardingWithPlan(page, {
      compute: 'cloud_free_trial',
      modelProvider: 'anthropic',
    });
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog.getByText('What do you want to do first?')).toBeVisible();

    await dialog.getByRole('button', { name: /Build an app/i }).click();

    await expect(
      dialog.getByText(/Couldn't set up your project/i),
    ).toBeVisible({ timeout: 10_000 });
    // Onboarding dialog is still open — the user can retry.
    await expect(dialog).toBeVisible();
    await expect(page).toHaveURL(/\/onboarding/);
  });

  test('BYO API key validation failure: shows the error, stays on Model', async ({ page }) => {
    await page.route('**/reliant.v1.SettingsService/ValidateProviderAPIKey', (route: Route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ valid: false, message: 'Invalid API key' }),
      }),
    );

    await gotoOnboardingWithPlan(page, { compute: 'cloud_free_trial' });
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog.getByText('Which AI should Reliant use?')).toBeVisible();

    await dialog.getByRole('button', { name: 'Anthropic API', exact: true }).click();
    await dialog.getByPlaceholder('sk-ant-...').fill('sk-ant-bad-key');
    await dialog.getByRole('button', { name: 'Save key and start' }).click();

    await expect(dialog.getByText(/Invalid API key/i)).toBeVisible({ timeout: 10_000 });
    // Still on Model — no navigation happened.
    await expect(dialog.getByText('Which AI should Reliant use?')).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// Goal × Compute permutations (project-choice branch selection)
// ---------------------------------------------------------------------------

test.describe('Onboarding – Project-choice branch selection', () => {
  test.beforeEach(async ({ page }) => {
    await mockGrpcRoutes(page);
    await loginWithApiKey(page);
  });

  // Cloud + build_app -> stays on project-choice until completeOnboarding
  // fires (it owns the terminal step for that branch), then the commit
  // provisions a machine and ProvisioningGate/DaemonConnectingGate gate the
  // actual exit — see commitLaunchPlan.ts and ProvisioningGate.tsx.
  test('Cloud + build_app: "Build an app" leaves onboarding directly', async ({ page }) => {
    // DaemonConnectingGate reads the daemon registry (reliant.v1), not the
    // control-plane list, and treats DAEMON_STATUS_ACTIVE as connected.
    await page.route('**/reliant.v1.DaemonRegistryService/ListDaemons', (route: Route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          daemons: [{ daemonId: 'daemon_1', hostname: 'onboarding-daemon', status: 'DAEMON_STATUS_ACTIVE' }],
        }),
      }),
    );

    await gotoOnboardingWithPlan(page, {
      compute: 'cloud_free_trial',
      modelProvider: 'anthropic',
    });
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await dialog.getByRole('button', { name: /Build an app/i }).click();
    await expect(dialog.getByText('Your machine is ready')).toBeVisible({ timeout: 10_000 });
    await dialog.getByRole('button', { name: 'Continue' }).click();
    await expect(page).not.toHaveURL(/\/onboarding/, { timeout: 10_000 });
  });

  // Cloud + existing_codebase -> github-connect (repo picker) is the next step.
  test('Cloud + existing_codebase: seeded intent lands on the GitHub repo picker', async ({ page }) => {
    await gotoOnboardingWithPlan(page, {
      compute: 'cloud_free_trial',
      modelProvider: 'anthropic',
      intent: 'existing_codebase',
    });
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog.getByText('Choose a repository')).toBeVisible({ timeout: 10_000 });
  });

  // Local daemon -> always finish regardless of intent (see
  // getStepsForPlan / deriveStep: isCloud gates the branch, not intent).
  //
  // The finish step has no picker: with no projects it creates the default
  // one itself, then completes and commits. Where it ends depends on whether
  // the commit had work to do. Own key: nothing to provision, so
  // ProvisioningGate exits by itself. Reliant credits: the commit grants AI
  // access, so the gate reports that task and waits for Continue.
  for (const modelProvider of ['anthropic', 'openai', 'reliant_credits'] as const) {
    test(`Local daemon + ${modelProvider}: lands on the finish step (default project, no picker)`, async ({ page }) => {
      const createProject = await holdCreateProject(page, {
        id: 'proj_default', name: 'my-project', path: '~/Projects/my-project',
      });

      await gotoOnboardingWithPlan(page, { compute: 'local_daemon', modelProvider });
      const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
      await expect(dialog.getByText('Setting up your project')).toBeVisible({ timeout: 10_000 });
      await expect
        .poll(() => createProject.requests)
        .toEqual([expect.objectContaining({ name: 'my-project' })]);

      createProject.release();

      if (modelProvider === 'reliant_credits') {
        await expect(dialog.getByText('Setting up your workspace')).toBeVisible({ timeout: 10_000 });
        await expect(dialog.getByText('AI access ready.')).toBeVisible();
        await expect(dialog.getByRole('button', { name: 'Try again' })).toHaveCount(0);
        await dialog.getByRole('button', { name: 'Continue' }).click();
      }

      await expect(page).toHaveURL((url) => url.pathname === '/' && url.searchParams.get('tour') === 'chat-and-sidebars', { timeout: 10_000 });
      await expect.poll(() => page.evaluate(() => JSON.parse(localStorage.getItem('workspace-state') ?? '{}').state?.lastProjectId)).toBe('proj_default');
      await expect(dialog).toBeHidden();
    });
  }
});

// ---------------------------------------------------------------------------
// Navigation Edge Cases
// ---------------------------------------------------------------------------

test.describe('Onboarding – Navigation Edge Cases', () => {
  test.beforeEach(async ({ page }) => {
    await mockGrpcRoutes(page);
    await loginWithApiKey(page);
  });

  test('Rapid double-click on the hosted-machine Continue does not skip past Model', async ({ page }) => {
    await gotoOnboarding(page);
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });

    await dialog.getByRole('button', { name: /^Continue with / }).dblclick();

    // Lands on Model, not further — a double-fire would otherwise race two
    // updatePlan calls and could land past it.
    await expect(dialog.getByText('Which AI should Reliant use?')).toBeVisible({ timeout: 10_000 });
  });

  test('An absurd/garbage plan value does not leave a blank screen', async ({ page }) => {
    // KNOWN PRODUCT BUG (reported, not fixed here): a `plan` value that fails
    // onboardingSearchSchema's Zod validation (invalid `compute` enum value,
    // unrecognized key, non-JSON string, ...) throws synchronously inside
    // tanstack-router's `beforeLoad`/`matchRoutes`, which is NOT caught
    // gracefully — it is caught by the top-level ErrorBoundary and renders
    // "Something went wrong" instead of falling back to a fresh onboarding
    // plan the way an unrecognized *top-level* param does (see the test
    // above). This assertion only guards the floor: no blank #root. It does
    // NOT assert the onboarding dialog recovers, because today it doesn't.
    await page.goto('/onboarding?plan=%7B%22compute%22%3A%22nonsense_value%22%7D');
    await page.waitForLoadState('domcontentloaded');

    const root = page.locator('#root');
    await expect(root).toBeAttached({ timeout: 10_000 });
    const childCount = await root.evaluate((el) => el.childNodes.length);
    expect(childCount).toBeGreaterThan(0);
  });

  test('Switching from cloud to local via Back, then choosing local, shows the connect panel', async ({ page }) => {
    await gotoOnboarding(page);
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });

    await dialog.getByRole('button', { name: /^Continue with / }).click();
    await expect(dialog.getByText('Which AI should Reliant use?')).toBeVisible({ timeout: 10_000 });

    await dialog.getByRole('button', { name: /Back/i }).click();
    await expect(dialog.getByText('Where should Reliant run your code?')).toBeVisible({
      timeout: 5_000,
    });

    await dialog.getByRole('button', { name: 'Use your own computer' }).click();
    await expect(
      dialog.getByText('Install Reliant on that machine', { exact: true }),
    ).toBeVisible({ timeout: 5_000 });
  });
});
