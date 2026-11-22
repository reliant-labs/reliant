import { expect, test, type Page } from '@playwright/test';

import { DEPLOY_CHAT, PROJECT, mockAppsBackend } from './appsMocks';
import { PHONE } from './mobileMocks';

// Deployed apps as first-class surfaces, against a mocked backend
// (appsMocks.ts): the apps on Projects, the home view, a chat that deployed,
// and onboarding's choice between building an app, connecting a repository
// and just chatting.

// The apps are a filter of Projects (U6, one shell): a project row carries
// its app's chip, and an app with no code here is a row of its own
// (components/Projects/ProjectsPage.tsx, over ProjectService/ListProjectsWithApps).
const catalogRow = (page: Page, key: string) => page.locator(`[data-testid="projects-row"][data-row-key="${key}"]`);

test.describe('Apps', () => {
  test('the retired /apps opens Projects narrowed to the apps, each with its deploy state', async ({ page }) => {
    await mockAppsBackend(page);
    // The index is ready when its rows render; an unrelated Monaco CDN
    // loader can leave the document load event pending (CI trace 40471064815).
    // The retired /apps: Projects, narrowed to the apps (lib/paths/legacy.ts).
    await page.goto('/apps', { waitUntil: 'domcontentloaded' });
    await expect(page).toHaveURL(/\/projects\?show=apps$/, { timeout: 15_000 });
    await expect(page.getByTestId('projects-filter-with_apps')).toHaveAttribute('aria-pressed', 'true');

    // The forge project: its row opens the project, its chip says the app is live.
    const store = catalogRow(page, PROJECT.id);
    await expect(store).toBeVisible({ timeout: 15_000 });
    await expect(store.getByRole('link', { name: 'Open storefront' })).toHaveAttribute('href', `/projects/${PROJECT.id}`);
    await expect(store.getByTestId('app-chip').first()).toHaveAttribute('data-app-state', 'live');
    // An API is not a destination: its URL is never linked here.
    await expect(store.getByRole('link', { name: /calm-heron\.reliantapps\.dev/ })).toHaveCount(0);

    // An app nobody opened as a Reliant project is still listed: the org deployed it.
    const status = catalogRow(page, 'app:status-page');
    await expect(status).toBeVisible();
    await expect(status.getByRole('link', { name: 'Open the status-page app' })).toHaveAttribute('href', '/forge/p/status-page');
  });

  test('the bare /forge lands on the apps', async ({ page }) => {
    await mockAppsBackend(page);
    await page.goto('/forge');
    await expect(page).toHaveURL(/\/projects\?show=apps$/, { timeout: 15_000 });
    await expect(page.getByTestId('projects-page')).toBeVisible();
  });

  test('with nothing deployed, the apps offer to build one', async ({ page }) => {
    await mockAppsBackend(page, { environments: [] });
    await page.goto('/projects?show=apps');
    // The forge project exists but is not live: listed, and its chip says so.
    await expect(catalogRow(page, PROJECT.id).getByTestId('app-chip').first()).toHaveAttribute('data-app-state', 'not_deployed', {
      timeout: 15_000,
    });
    await page.getByTestId('projects-build-app').click();
    await expect(page).toHaveURL(new RegExp(`/project/${PROJECT.id}`));
    await expect(page.getByTestId('home-build-heading')).toBeVisible({ timeout: 15_000 });
  });

  test("a forge project's home has no Build an app prompt", async ({ page }) => {
    await mockAppsBackend(page);
    await page.goto(`/project/${PROJECT.id}`);
    await expect(page.getByRole('textbox').first()).toBeVisible({ timeout: 15_000 });
    // A forge project: no app block and no "Build an app" prompt mid-screen.
    await expect(page.getByTestId('home-build-app')).toHaveCount(0);
  });

  test('?start=app opens the app-building composer', async ({ page }) => {
    // Loaded cold, the way onboarding's exit lands: the shell settles the
    // project and remounts the view on the way, and the composer survives it.
    await mockAppsBackend(page, { environments: [] });
    await page.goto(`/project/${PROJECT.id}?start=app`);
    await expect(page.getByTestId('home-build-heading')).toBeVisible({ timeout: 15_000 });
    await expect(page.getByRole('textbox').first()).toHaveValue(/Build me a web app with forge/);
  });

  test.describe('a chat that deployed', () => {
    // The transcript's tool cards are one component on every surface
    // (ToolExecution); the phone's chat screen mounts it with the least chrome
    // around it, so the card is asserted there.
    test.use(PHONE);

    test("links to the app's live URLs", async ({ page }) => {
      await mockAppsBackend(page);
      await page.goto(`/m/chats/${DEPLOY_CHAT.id}`);
      // A deploy is a user-facing call, so the simplified thread renders it in
      // place as its own row (#2441), not inside the turn's "Activity" fold.
      // This turn's only call is the deploy, so there is no fold at all.
      const card = page.getByTestId('deployed-app-card');
      await expect(card).toBeVisible({ timeout: 15_000 });
      await expect(card).toHaveAttribute('data-env', 'prod');
      await expect(card).toContainText('storefront is deployed to prod');
      await expect(card.getByRole('link', { name: /bright-otter\.reliantapps\.dev/ })).toHaveAttribute(
        'href',
        'https://bright-otter.reliantapps.dev',
      );
      // One card, and no fold to hide it in.
      await expect(page.getByTestId('deployed-app-card')).toHaveCount(1);
      await expect(page.getByRole('button', { name: /^Activity\b/ })).toHaveCount(0);
      await card.getByTestId('deployed-app-open').click();
      await expect(page).toHaveURL(/\/forge\/p\/storefront/);
    });
  });

  test('onboarding offers building an app as a peer of chatting', async ({ page }) => {
    await mockAppsBackend(page, { onboarded: false });
    const plan = encodeURIComponent(JSON.stringify({ compute: 'cloud_free_trial', modelProvider: 'reliant_credits' }));
    await page.goto(`/onboarding?plan=${plan}`);
    const dialog = page.getByRole('dialog', { name: 'Onboarding setup' });
    await expect(dialog.getByText('What do you want to do first?')).toBeVisible({ timeout: 15_000 });
    await expect(dialog.getByTestId('onboarding-choice-build-app')).toBeVisible();
    await expect(dialog.getByTestId('onboarding-choice-github')).toBeVisible();
    await expect(dialog.getByTestId('onboarding-choice-chat')).toBeVisible();
  });
});
