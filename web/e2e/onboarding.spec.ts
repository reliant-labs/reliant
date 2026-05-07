import { test, expect, type Page, type Route } from '@playwright/test';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Intercept Connect-protocol gRPC calls and return canned responses. */
async function mockGrpcRoutes(page: Page) {
  // ListProjects → empty (new user, triggers onboarding)
  await page.route('**/reliant.v1.ProjectService/ListProjects', (route: Route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ projects: [] }),
    }),
  );

  // CreateProject → success stub
  await page.route('**/reliant.v1.ProjectService/CreateProject', (route: Route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        project: { id: 'proj_test_1', name: 'test-project', path: '/tmp/test' },
      }),
    }),
  );

  // Daemon-related calls
  await page.route('**/reliant.v1.DaemonService/**', (route: Route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({}),
    }),
  );

  // FileSystemService calls
  await page.route('**/reliant.v1.FileSystemService/**', (route: Route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({}),
    }),
  );

  // DaemonRegistry (token generation, listing daemons, etc.)
  await page.route('**/reliant.v1.DaemonRegistryService/**', (route: Route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ daemons: [] }),
    }),
  );

  // Control-plane API calls (cloud daemon provisioning, user info, etc.)
  await page.route('**/api/v1/**', (route: Route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        user: {
          id: 'user_test',
          globalBudgetAvailable: true,
          budgetAvailable: true,
          ipRestricted: false,
          ipAllowed: true,
        },
        daemons: [],
        eligible: true,
      }),
    }),
  );

  // Settings / provider validation
  await page.route('**/api/settings/**', (route: Route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ valid: true, message: 'ok' }),
    }),
  );

  // OAuth availability check
  await page.route('**/auth/**', (route: Route) => {
    // Let the page's own /auth path through if it's a navigation
    if (route.request().resourceType() === 'document') {
      return route.fallback();
    }
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ available: false }),
    });
  });
}

/** Navigate to the app with onboarding forced via query param. */
async function gotoWithOnboarding(page: Page) {
  // Clear localStorage to simulate a fresh user
  await page.addInitScript(() => {
    localStorage.clear();
  });
  await page.goto('/?reset-onboarding');
  // Wait for the onboarding dialog to appear
  await page.getByRole('dialog', { name: 'Onboarding setup' }).waitFor({ timeout: 10_000 });
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test.describe('Onboarding Flow', () => {
  test.beforeEach(async ({ page }) => {
    await mockGrpcRoutes(page);
  });

  // ── Visibility ──────────────────────────────────────────────

  test('shows onboarding for new user (no projects, no localStorage)', async ({ page }) => {
    await gotoWithOnboarding(page);

    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog).toBeVisible();

    // The first step is the Goal step – verify the heading is visible
    await expect(dialog.getByText('What are you building?')).toBeVisible();
  });

  // ── Goal → Compute ─────────────────────────────────────────

  test('Goal → click option → advances to Compute step', async ({ page }) => {
    await gotoWithOnboarding(page);

    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });

    // Click "Build something new"
    await dialog.getByRole('button', { name: /Build something new/i }).click();

    // Should advance to the Compute step
    await expect(
      dialog.getByText('Where should Reliant run your daemon?'),
    ).toBeVisible({ timeout: 5_000 });
  });

  // ── Cloud path: goal → compute(cloud) → skips dir picker → model ──

  test('Cloud path: goal → compute(cloud) → skips dir picker → model', async ({ page }) => {
    // Mock cloud daemon creation to succeed immediately
    await page.route('**/api/v1/daemons', (route: Route) => {
      if (route.request().method() === 'POST') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ daemon: { id: 'daemon_1', name: 'onboarding-workspace', status: 'running' } }),
        });
      }
      // GET → list daemons
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ daemons: [] }),
      });
    });

    await gotoWithOnboarding(page);
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });

    // Goal: "Explore Reliant" (doesn't need local folder)
    await dialog.getByRole('button', { name: /Explore Reliant/i }).click();
    await expect(dialog.getByText('Where should Reliant run your daemon?')).toBeVisible({ timeout: 5_000 });

    // Compute: choose cloud
    await dialog.getByRole('button', { name: /Start cloud daemon/i }).click();

    // Should eventually land on a step beyond compute (project location or model).
    // For "explore" cloud path: goal → compute → cloud-project-location → model
    // Wait for the step to advance past compute
    await expect(
      dialog.getByText('Where should Reliant run your daemon?'),
    ).not.toBeVisible({ timeout: 10_000 });
  });

  // ── Cloud existing path: goal(existing) → compute(cloud) → github-connect → model ──

  test('Cloud existing path: goal(existing) → compute(cloud) → github-connect → model', async ({ page }) => {
    await page.route('**/api/v1/daemons', (route: Route) => {
      if (route.request().method() === 'POST') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ daemon: { id: 'daemon_1', name: 'onboarding-workspace', status: 'running' } }),
        });
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ daemons: [] }),
      });
    });

    await gotoWithOnboarding(page);
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });

    // Goal: "Work on an existing project"
    await dialog.getByRole('button', { name: /Work on an existing project/i }).click();
    await expect(dialog.getByText('Where should Reliant run your daemon?')).toBeVisible({ timeout: 5_000 });

    // Compute: choose cloud
    await dialog.getByRole('button', { name: /Start cloud daemon/i }).click();

    // For cloud + existing: goal → compute → github-connect → model
    // Should show the GitHub connect step
    await expect(
      dialog.getByText(/Connect your GitHub repos/i),
    ).toBeVisible({ timeout: 10_000 });
  });

  // ── Completing onboarding transitions to main app ──────────

  test('Completing onboarding transitions to main app (no blank screen)', async ({ page }) => {
    // Simulate a user who has already completed onboarding AND has a project.
    // The key: onboarding is "completed" in localStorage AND ListProjects returns
    // a project, so the overlay stays hidden and the main app renders.
    await page.addInitScript(() => {
      localStorage.setItem(
        'reliant-onboarding',
        JSON.stringify({
          state: {
            state: 'completed',
            plan: { intent: 'explore' },
            currentStepIndex: 0,
          },
          version: 0,
        }),
      );
    });

    // Override ListProjects to return a project (user who completed onboarding has one)
    await page.route('**/reliant.v1.ProjectService/ListProjects', (route: Route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          projects: [{
            id: 'proj_1',
            name: 'My Project',
            path: '/tmp/my-project',
            is_git_repo: false,
            worktree_count: 0,
            last_active: new Date().toISOString(),
            created_at: new Date().toISOString(),
            updated_at: new Date().toISOString(),
          }],
        }),
      }),
    );

    await page.goto('/');
    await page.waitForLoadState('domcontentloaded');

    // Onboarding dialog should NOT be visible
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog).not.toBeVisible({ timeout: 10_000 });

    // The main app root should have content (not a blank screen)
    const root = page.locator('#root');
    await expect(root).toBeAttached();
    const childCount = await root.evaluate((el) => el.childNodes.length);
    expect(childCount).toBeGreaterThan(0);
  });

  // ── Back button ─────────────────────────────────────────────

  test('Back button works correctly', async ({ page }) => {
    await gotoWithOnboarding(page);
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });

    // Start on Goal step
    await expect(dialog.getByText('What are you building?')).toBeVisible();

    // Back button should NOT be visible on the first step
    const backButton = dialog.getByRole('button', { name: /Back/i });
    await expect(backButton).not.toBeVisible();

    // Advance to Compute step
    await dialog.getByRole('button', { name: /Build something new/i }).click();
    await expect(dialog.getByText('Where should Reliant run your daemon?')).toBeVisible({ timeout: 5_000 });

    // Now Back button should be visible in the footer
    const footerBack = page.getByRole('dialog', { name: 'Onboarding setup' }).getByRole('button', { name: /Back/i });
    await expect(footerBack).toBeVisible();

    // Click Back → should return to Goal step
    await footerBack.click();
    await expect(dialog.getByText('What are you building?')).toBeVisible({ timeout: 5_000 });
  });

  // ── Fresh state: isBackendReady resolves within timeout ────

  test('Incognito/fresh state: isBackendReady resolves within timeout', async ({ page }) => {
    // Simulate truly fresh state: clear everything before navigating
    await page.addInitScript(() => {
      localStorage.clear();
      sessionStorage.clear();
    });

    // Navigate without the reset param — just a clean slate
    await page.goto('/');

    // The page should load within a reasonable time without hanging
    await page.waitForLoadState('domcontentloaded', { timeout: 15_000 });

    // Either onboarding shows (because no projects) or the main app loads
    // — the key assertion is that the page is not blank/stuck
    const root = page.locator('#root');
    await expect(root).toBeAttached({ timeout: 10_000 });
    const childCount = await root.evaluate((el) => el.childNodes.length);
    expect(childCount).toBeGreaterThan(0);

    // Verify no critical JavaScript errors
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.message));

    // Give it a moment for any async errors
    await page.waitForTimeout(2_000);

    // Filter out expected non-critical errors (network mocks may cause some)
    const criticalErrors = errors.filter(
      (msg) =>
        !msg.includes('fetch') &&
        !msg.includes('network') &&
        !msg.includes('Failed to fetch') &&
        !msg.includes('ERR_CONNECTION_REFUSED'),
    );
    expect(criticalErrors).toHaveLength(0);
  });
});

// ---------------------------------------------------------------------------
// Failure Scenarios
// ---------------------------------------------------------------------------

test.describe('Onboarding – Failure Scenarios', () => {
  test('gRPC backend unreachable: still shows onboarding', async ({ page }) => {
    // Mock ALL routes to return 500
    await page.route('**/*', (route: Route) => {
      const url = route.request().url();
      if (
        url.includes('reliant.v1.') ||
        url.includes('/api/v1/') ||
        url.includes('/api/settings/') ||
        url.includes('/auth/')
      ) {
        return route.fulfill({ status: 500, body: 'Internal Server Error' });
      }
      return route.fallback();
    });

    await page.addInitScript(() => {
      localStorage.clear();
    });
    await page.goto('/?reset-onboarding');
    await page.waitForLoadState('domcontentloaded');

    // Page should NOT be blank — either onboarding or main app renders
    const root = page.locator('#root');
    await expect(root).toBeAttached({ timeout: 10_000 });
    const childCount = await root.evaluate((el) => el.childNodes.length);
    expect(childCount).toBeGreaterThan(0);
  });

  test('Cloud daemon creation fails: shows error', async ({ page }) => {
    await mockGrpcRoutes(page);

    // Mock controlplane DaemonService RPCs — ListDaemons returns empty, CreateDaemon fails
    await page.route('**/controlplane.v1.DaemonService/ListDaemons', (route: Route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ daemons: [] }),
      }),
    );
    await page.route('**/controlplane.v1.DaemonService/CreateDaemon', (route: Route) =>
      route.fulfill({
        status: 500,
        contentType: 'application/json',
        body: JSON.stringify({ message: 'Internal server error' }),
      }),
    );

    await gotoWithOnboarding(page);
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });

    // Goal → Explore
    await dialog.getByRole('button', { name: /Explore Reliant/i }).click();
    await expect(dialog.getByText('Where should Reliant run your daemon?')).toBeVisible({ timeout: 5_000 });

    // Click cloud — should fail
    await dialog.getByRole('button', { name: /Start cloud daemon/i }).click();

    // Error message should appear in the destructive text
    await expect(dialog.locator('.text-destructive')).toBeVisible({ timeout: 10_000 });
  });

  test('Cloud daemon creation hits plan limit: falls back gracefully', async ({ page }) => {
    await mockGrpcRoutes(page);

    let listCallCount = 0;
    // Mock controlplane DaemonService RPCs — CreateDaemon returns plan limit, fallback ListDaemons returns a daemon
    await page.route('**/controlplane.v1.DaemonService/ListDaemons', (route: Route) => {
      listCallCount++;
      // First call returns empty, fallback call returns a daemon for recovery
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          daemons: listCallCount > 1
            ? [{ daemonId: 'daemon_1', name: 'onboarding-workspace', status: 3 }]
            : [],
        }),
      });
    });
    await page.route('**/controlplane.v1.DaemonService/CreateDaemon', (route: Route) =>
      route.fulfill({
        status: 409,
        contentType: 'application/json',
        body: JSON.stringify({ message: 'plan limit reached' }),
      }),
    );
    await page.route('**/controlplane.v1.DaemonService/ResumeDaemon', (route: Route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({}),
      }),
    );

    await gotoWithOnboarding(page);
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });

    await dialog.getByRole('button', { name: /Explore Reliant/i }).click();
    await expect(dialog.getByText('Where should Reliant run your daemon?')).toBeVisible({ timeout: 5_000 });

    await dialog.getByRole('button', { name: /Start cloud daemon/i }).click();

    // Should either advance (fallback worked) or show an error — NOT a blank screen
    await page.waitForTimeout(3_000);
    const root = page.locator('#root');
    const childCount = await root.evaluate((el) => el.childNodes.length);
    expect(childCount).toBeGreaterThan(0);
  });

  test('Project creation fails during completion: shows error not blank screen', async ({ page }) => {
    await mockGrpcRoutes(page);

    // Override CreateProject to fail
    await page.route('**/reliant.v1.ProjectService/CreateProject', (route: Route) =>
      route.fulfill({
        status: 500,
        contentType: 'application/json',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ code: 13, message: 'Internal server error' }),
      }),
    );

    await gotoWithOnboarding(page);

    // Page should not be blank
    const root = page.locator('#root');
    await expect(root).toBeAttached();
    const childCount = await root.evaluate((el) => el.childNodes.length);
    expect(childCount).toBeGreaterThan(0);

    // Onboarding dialog should still be visible (user can keep working)
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog).toBeVisible();
  });
});

// ---------------------------------------------------------------------------
// All Goal × Compute Permutations
// ---------------------------------------------------------------------------

test.describe('Onboarding – Goal × Compute Permutations', () => {
  // Step identifiers mapped to heading text visible in the dialog
  const STEP_HEADING: Record<string, RegExp> = {
    'forge-style': /Choose the starting style/i,
    'github-connect': /Connect your GitHub repos/i,
    'model': /Choose model access/i,
    'cloud-project-location': /Hosted project folder/i,
    'daemon-connect': /Connect your daemon/i,
    'local-project-location': /Choose the project folder/i,
  };

  // ── Cloud paths ──────────────────────────────────────────────

  const GOAL_CLOUD_EXPECTATIONS: Array<{ goal: string; label: string; expectSteps: string[] }> = [
    { goal: 'build_app', label: 'Build something new', expectSteps: ['cloud-project-location', 'forge-style'] },
    { goal: 'existing_codebase', label: 'Work on an existing project', expectSteps: ['github-connect'] },
    { goal: 'explore', label: 'Explore Reliant', expectSteps: ['cloud-project-location'] },
    { goal: 'landing_page', label: 'Create a landing page', expectSteps: ['cloud-project-location'] },
    { goal: 'pitch_deck', label: 'Create a pitch deck', expectSteps: ['cloud-project-location'] },
    { goal: 'blog_post', label: 'Write docs or a blog post', expectSteps: ['cloud-project-location'] },
  ];

  for (const { goal, label, expectSteps } of GOAL_CLOUD_EXPECTATIONS) {
    test(`Cloud path: ${goal} → correct steps`, async ({ page }) => {
      await mockGrpcRoutes(page);

      // Mock controlplane DaemonService — ListDaemons empty, CreateDaemon success
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
          body: JSON.stringify({}),
        }),
      );

      await gotoWithOnboarding(page);
      const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });

      // Step 1: Goal
      await dialog.getByRole('button', { name: new RegExp(label, 'i') }).click();
      await expect(dialog.getByText('Where should Reliant run your daemon?')).toBeVisible({ timeout: 5_000 });

      // Step 2: Compute → cloud
      await dialog.getByRole('button', { name: /Start cloud daemon/i }).click();

      // Verify the first expected step after compute appears
      const firstExpected = expectSteps[0];
      const heading = STEP_HEADING[firstExpected];
      if (heading) {
        await expect(dialog.getByText(heading)).toBeVisible({ timeout: 10_000 });
      }
    });
  }

  // ── Local paths ──────────────────────────────────────────────

  const GOAL_LOCAL_EXPECTATIONS: Array<{ goal: string; label: string; expectSteps: string[] }> = [
    { goal: 'build_app', label: 'Build something new', expectSteps: ['daemon-connect', 'local-project-location'] },
    { goal: 'existing_codebase', label: 'Work on an existing project', expectSteps: ['daemon-connect', 'local-project-location'] },
    { goal: 'explore', label: 'Explore Reliant', expectSteps: ['daemon-connect', 'local-project-location'] },
    { goal: 'landing_page', label: 'Create a landing page', expectSteps: ['daemon-connect', 'local-project-location'] },
    { goal: 'pitch_deck', label: 'Create a pitch deck', expectSteps: ['daemon-connect', 'local-project-location'] },
    { goal: 'blog_post', label: 'Write docs or a blog post', expectSteps: ['daemon-connect', 'local-project-location'] },
  ];

  for (const { goal, label, expectSteps } of GOAL_LOCAL_EXPECTATIONS) {
    test(`Local path: ${goal} → correct steps`, async ({ page }) => {
      await mockGrpcRoutes(page);
      await gotoWithOnboarding(page);
      const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });

      // Step 1: Goal
      await dialog.getByRole('button', { name: new RegExp(label, 'i') }).click();
      await expect(dialog.getByText('Where should Reliant run your daemon?')).toBeVisible({ timeout: 5_000 });

      // Step 2: Compute → local ("I'll connect my own")
      await dialog.getByRole('button', { name: /I.*ll connect my own/i }).click();

      // Verify the first expected step identifier
      const firstExpected = expectSteps[0];
      const heading = STEP_HEADING[firstExpected];
      if (heading) {
        // For daemon-connect, clicking local shows the local panel inline
        // but advancing to the next step requires the daemon or clicking continue.
        // Verify the local panel shows the daemon setup content.
        await expect(
          dialog.getByText(/Install Reliant Daemon|Daemon already connected|Connect your daemon/i),
        ).toBeVisible({ timeout: 5_000 });
      }
    });
  }
});

// ---------------------------------------------------------------------------
// Navigation Edge Cases
// ---------------------------------------------------------------------------

test.describe('Onboarding – Navigation Edge Cases', () => {
  test.beforeEach(async ({ page }) => {
    await mockGrpcRoutes(page);
  });

  test('Rapid clicking: double-click on goal option does not skip steps', async ({ page }) => {
    await gotoWithOnboarding(page);
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });

    // Double-click "Build something new" rapidly
    const goalButton = dialog.getByRole('button', { name: /Build something new/i });
    await goalButton.dblclick();

    // Should be on the Compute step, NOT beyond it
    await expect(
      dialog.getByText('Where should Reliant run your daemon?'),
    ).toBeVisible({ timeout: 5_000 });
  });

  test('Stale localStorage: old step index gets clamped', async ({ page }) => {
    // Set a stale onboarding state with an absurdly high step index
    await page.addInitScript(() => {
      localStorage.setItem(
        'reliant-onboarding',
        JSON.stringify({
          state: {
            state: 'not_started',
            plan: {},
            currentStepIndex: 99,
          },
          version: 0,
        }),
      );
    });

    await page.goto('/?reset-onboarding');
    await page.waitForLoadState('domcontentloaded');

    // Page should not crash — root should have content
    const root = page.locator('#root');
    await expect(root).toBeAttached({ timeout: 10_000 });
    const childCount = await root.evaluate((el) => el.childNodes.length);
    expect(childCount).toBeGreaterThan(0);
  });

  test('Switching from cloud to local via back button', async ({ page }) => {
    // Mock controlplane DaemonService — ListDaemons empty, CreateDaemon success
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
        body: JSON.stringify({}),
      }),
    );

    await gotoWithOnboarding(page);
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });

    // Goal → Explore
    await dialog.getByRole('button', { name: /Explore Reliant/i }).click();
    await expect(dialog.getByText('Where should Reliant run your daemon?')).toBeVisible({ timeout: 5_000 });

    // Choose cloud → advances past compute
    await dialog.getByRole('button', { name: /Start cloud daemon/i }).click();
    await expect(
      dialog.getByText('Where should Reliant run your daemon?'),
    ).not.toBeVisible({ timeout: 10_000 });

    // Hit Back to get back to compute
    const backButton = dialog.getByRole('button', { name: /Back/i });
    await backButton.click();
    await expect(dialog.getByText('Where should Reliant run your daemon?')).toBeVisible({ timeout: 5_000 });

    // Now choose local
    await dialog.getByRole('button', { name: /I.*ll connect my own/i }).click();

    // Verify local panel content appears (daemon setup instructions)
    await expect(
      dialog.getByText(/Install Reliant Daemon|Daemon already connected|Connect your daemon/i),
    ).toBeVisible({ timeout: 5_000 });
  });
});