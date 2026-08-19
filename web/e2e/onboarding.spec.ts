import { test, expect, type Page, type Route } from '@playwright/test';

// ---------------------------------------------------------------------------
// Current onboarding model (see web/src/components/OnboardingFlow/stepConfig.ts)
//
//   ONBOARDING_STEPS = ['compute', 'model', 'project-choice', 'github-connect', 'project-picker']
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

/**
 * Intercept Connect-protocol gRPC calls and return canned success responses.
 *
 * Route registration order matters to Playwright: the LAST matching
 * `page.route()` call wins. So this registers broad `**` fallbacks for each
 * service package FIRST, then specific per-RPC overrides after, letting the
 * specific ones take priority over the generic empty-object fallback.
 */
async function mockGrpcRoutes(page: Page) {
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

  // ListProjects → empty (new user, triggers onboarding by default).
  await page.route('**/reliant.v1.ProjectService/ListProjects', (route: Route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ projects: [] }),
    }),
  );

  // CreateProject → success stub.
  await page.route('**/reliant.v1.ProjectService/CreateProject', (route: Route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        project: { id: 'proj_test_1', name: 'test-project', path: '/tmp/test' },
      }),
    }),
  );

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
  await page.route('**/reliant.v1.SettingsService/UpdateProviderAPIKey', (route: Route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ message: 'ok' }),
    }),
  );

  // Cloud user + eligibility — a fresh, not-yet-onboarded, cloud-eligible user.
  await page.route('**/controlplane.v1.UserService/GetCurrentUser', (route: Route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ user: { id: 'user_test', onboardingCompleted: false } }),
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
          grantedMinutesRemaining: '0',
          planName: 'free',
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

  test('shows onboarding for a new (not-yet-onboarded) user at /onboarding', async ({ page }) => {
    await gotoOnboarding(page);

    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog).toBeVisible();

    // First step is Compute — verify the heading is visible.
    await expect(dialog.getByText('One chat interface. Daemons anywhere.')).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Start cloud daemon' })).toBeVisible();
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

    await dialog.getByRole('button', { name: 'Start cloud daemon' }).click();

    await expect(dialog.getByText('Choose model access')).toBeVisible({ timeout: 10_000 });
    await expect(page).toHaveURL(/cloud_free_trial/, { timeout: 10_000 });
  });

  test('Compute: "I\'ll connect my own" shows self-hosted connect instructions inline', async ({ page }) => {
    await gotoOnboarding(page);
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });

    await dialog.getByRole('button', { name: "I'll connect my own" }).click();

    // Stays on the compute step (URL keeps no `compute` plan field) — the
    // local-daemon instructions render inline rather than navigating.
    await expect(
      dialog.getByText('Install Reliant Daemon and connect with a token'),
    ).toBeVisible({ timeout: 5_000 });
    await expect(dialog.getByRole('button', { name: 'Generate token' })).toBeVisible();
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
    await expect(dialog.getByText('Choose model access')).toBeVisible({ timeout: 10_000 });
    await expect(page).toHaveURL(/local_daemon/, { timeout: 10_000 });
  });

  // ── Model → Project-choice / Project-picker ─────────────────

  test('Model: saving a BYO Anthropic key on the cloud path advances to Pick a starting point', async ({ page }) => {
    await gotoOnboardingWithPlan(page, { compute: 'cloud_free_trial' });
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog.getByText('Choose model access')).toBeVisible();

    await dialog.getByRole('button', { name: 'Anthropic', exact: true }).click();
    await dialog.getByPlaceholder('sk-ant-...').fill('sk-ant-test-key');
    await dialog.getByRole('button', { name: 'Save key and start' }).click();

    // Cloud path lands on project-choice ("Pick a starting point").
    await expect(dialog.getByText('Pick a starting point')).toBeVisible({ timeout: 10_000 });
    await expect(page).toHaveURL(/modelProvider.*anthropic|anthropic.*modelProvider/, {
      timeout: 10_000,
    });
  });

  test('Model: saving a BYO key on the local path advances to Pick a project', async ({ page }) => {
    await gotoOnboardingWithPlan(page, { compute: 'local_daemon' });
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog.getByText('Choose model access')).toBeVisible();

    await dialog.getByRole('button', { name: 'Anthropic', exact: true }).click();
    await dialog.getByPlaceholder('sk-ant-...').fill('sk-ant-test-key');
    await dialog.getByRole('button', { name: 'Save key and start' }).click();

    // Local path skips project-choice/github-connect and lands directly on
    // project-picker ("Pick a project") — see deriveStep in stepConfig.ts.
    await expect(dialog.getByText('Pick a project')).toBeVisible({ timeout: 10_000 });
  });

  // ── Project-choice (cloud): Start new vs Connect GitHub ─────

  test('Project-choice: "Start something new" (cloud) creates a project and leaves onboarding', async ({ page }) => {
    await gotoOnboardingWithPlan(page, {
      compute: 'cloud_free_trial',
      modelProvider: 'anthropic',
    });
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog.getByText('Pick a starting point')).toBeVisible();

    await dialog.getByRole('button', { name: /Start something new/i }).click();

    // completeOnboarding + ensureProject succeed → navigates off /onboarding
    // to the newly-selected project.
    await expect(page).not.toHaveURL(/\/onboarding/, { timeout: 10_000 });
  });

  test('Project-choice: "Connect GitHub" (cloud, no credential) advances to the GitHub connect step', async ({ page }) => {
    await gotoOnboardingWithPlan(page, {
      compute: 'cloud_free_trial',
      modelProvider: 'anthropic',
    });
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog.getByText('Pick a starting point')).toBeVisible();

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

  // ── Project-picker (local): pick existing project ───────────

  test('Project-picker: selecting an existing project finishes onboarding', async ({ page }) => {
    await page.route('**/reliant.v1.ProjectService/ListProjects', (route: Route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
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
        }),
      }),
    );

    await gotoOnboardingWithPlan(page, { compute: 'local_daemon', modelProvider: 'anthropic' });
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog.getByText('Pick a project')).toBeVisible();

    await dialog.getByRole('button', { name: /my-proj/i }).click();

    await expect(page).not.toHaveURL(/\/onboarding/, { timeout: 10_000 });
  });

  // ── Back button ─────────────────────────────────────────────

  test('Back button is hidden on the first step and returns to Compute from Model', async ({ page }) => {
    await gotoOnboarding(page);
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });

    // First step (compute): no Back button.
    await expect(dialog.getByRole('button', { name: /Back/i })).not.toBeVisible();

    await dialog.getByRole('button', { name: 'Start cloud daemon' }).click();
    await expect(dialog.getByText('Choose model access')).toBeVisible({ timeout: 10_000 });

    // Back button now visible in the footer, and returns to Compute.
    const backButton = dialog.getByRole('button', { name: /Back/i });
    await expect(backButton).toBeVisible();
    await backButton.click();

    await expect(dialog.getByText('One chat interface. Daemons anywhere.')).toBeVisible({
      timeout: 5_000,
    });
  });

  // ── Direct URL / plan-driven navigation ─────────────────────

  test('Direct navigation with a seeded plan lands on the corresponding step', async ({ page }) => {
    await gotoOnboardingWithPlan(page, { compute: 'cloud_free_trial' });
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog).toBeVisible({ timeout: 10_000 });
    await expect(dialog.getByText('Choose model access')).toBeVisible({ timeout: 5_000 });
  });

  test('An unrecognized top-level search param (no plan key) is ignored, landing on the first step', async ({ page }) => {
    // Old bookmarked links like `?step=goal` from the pre-plan-model UI:
    // `step` isn't part of onboardingSearchSchema, so tanstack-router strips
    // it silently and the app falls through to the default (no plan → compute).
    await page.goto('/onboarding?step=goal');
    await page.getByRole('dialog', { name: 'Onboarding setup' }).waitFor({ timeout: 10_000 });

    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog.getByText('One chat interface. Daemons anywhere.')).toBeVisible({
      timeout: 5_000,
    });
  });
});

// ---------------------------------------------------------------------------
// Failure Scenarios
// ---------------------------------------------------------------------------

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
    const childCount = await root.evaluate((el) => el.childNodes.length);
    expect(childCount).toBeGreaterThan(0);
  });

  test('Cloud daemon creation fails: shows the server error inline, stays on Compute', async ({ page }) => {
    await page.route('**/controlplane.v1.DaemonService/CreateDaemon', (route: Route) =>
      route.fulfill({
        status: 500,
        contentType: 'application/json',
        body: JSON.stringify({ message: 'Internal server error' }),
      }),
    );

    await gotoOnboarding(page);
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });

    await dialog.getByRole('button', { name: 'Start cloud daemon' }).click();

    // Stays on Compute and surfaces the server's error text.
    await expect(dialog.getByText('One chat interface. Daemons anywhere.')).toBeVisible({
      timeout: 5_000,
    });
    await expect(dialog.getByText('Internal server error')).toBeVisible({ timeout: 10_000 });
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
    await expect(dialog.getByText('Pick a starting point')).toBeVisible();

    await dialog.getByRole('button', { name: /Start something new/i }).click();

    await expect(
      dialog.getByText(/Couldn't create your workspace/i),
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
    await expect(dialog.getByText('Choose model access')).toBeVisible();

    await dialog.getByRole('button', { name: 'Anthropic', exact: true }).click();
    await dialog.getByPlaceholder('sk-ant-...').fill('sk-ant-bad-key');
    await dialog.getByRole('button', { name: 'Save key and start' }).click();

    await expect(dialog.getByText(/Invalid API key/i)).toBeVisible({ timeout: 10_000 });
    // Still on Model — no navigation happened.
    await expect(dialog.getByText('Choose model access')).toBeVisible();
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
  // fires (it owns the terminal step for that branch).
  test('Cloud + build_app: "Start something new" leaves onboarding directly', async ({ page }) => {
    await gotoOnboardingWithPlan(page, {
      compute: 'cloud_free_trial',
      modelProvider: 'anthropic',
    });
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await dialog.getByRole('button', { name: /Start something new/i }).click();
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

  // Local daemon -> always project-picker regardless of intent (see
  // getStepsForPlan / deriveStep: isCloud gates the branch, not intent).
  for (const modelProvider of ['anthropic', 'openai', 'reliant_credits'] as const) {
    test(`Local daemon + ${modelProvider}: lands on the project picker`, async ({ page }) => {
      await gotoOnboardingWithPlan(page, { compute: 'local_daemon', modelProvider });
      const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
      await expect(dialog.getByText('Pick a project')).toBeVisible({ timeout: 10_000 });
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

  test('Rapid double-click on "Start cloud daemon" does not skip past Model', async ({ page }) => {
    await gotoOnboarding(page);
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });

    await dialog.getByRole('button', { name: 'Start cloud daemon' }).dblclick();

    // Lands on Model, not further — a double-fire would otherwise race two
    // updatePlan calls and could land past it.
    await expect(dialog.getByText('Choose model access')).toBeVisible({ timeout: 10_000 });
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

    await dialog.getByRole('button', { name: 'Start cloud daemon' }).click();
    await expect(dialog.getByText('Choose model access')).toBeVisible({ timeout: 10_000 });

    await dialog.getByRole('button', { name: /Back/i }).click();
    await expect(dialog.getByText('One chat interface. Daemons anywhere.')).toBeVisible({
      timeout: 5_000,
    });

    await dialog.getByRole('button', { name: "I'll connect my own" }).click();
    await expect(
      dialog.getByText('Install Reliant Daemon and connect with a token'),
    ).toBeVisible({ timeout: 5_000 });
  });
});
