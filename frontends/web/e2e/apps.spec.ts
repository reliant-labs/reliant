import { expect, test } from '@playwright/test';

import { DEPLOY_CHAT, PROJECT, mockAppsBackend } from './appsMocks';
import { PHONE } from './mobileMocks';

// Deployed apps as first-class surfaces, against a mocked backend
// (appsMocks.ts): the Apps page, the sidebar, the home view, the console's
// Overview, a chat that deployed, and onboarding's choice between building an
// app, connecting a repository and just chatting.

test.describe('Apps', () => {
  test('the Apps page lists every app in one table with its live site', async ({ page }) => {
    await mockAppsBackend(page);
    // The index is ready when its rows render; an unrelated Monaco CDN
    // loader can leave the document load event pending (CI trace 40471064815).
    await page.goto('/apps', { waitUntil: 'domcontentloaded' });

    const store = page.getByTestId('app-row-storefront');
    await expect(store).toBeVisible({ timeout: 15_000 });
    // The row opens the console; the site link opens the app itself.
    await expect(store.getByRole('link', { name: 'storefront' })).toHaveAttribute('href', '/forge/p/storefront');
    await expect(store.getByTestId('app-live-link')).toHaveAttribute('href', 'https://bright-otter.reliantapps.dev');
    // An API is not a destination: its URL is never linked here.
    await expect(store.getByRole('link', { name: /calm-heron\.reliantapps\.dev/ })).toHaveCount(0);
    await expect(store.getByTestId('app-env-prod')).toBeVisible();
    await expect(store.getByTestId('app-env-staging')).toBeVisible();

    // An app nobody opened as a Reliant project is still listed: the org deployed it.
    await expect(page.getByTestId('app-row-status-page').getByRole('link', { name: /status\.example\.com/ })).toBeVisible();
    // Newest deploy first: staging went out 3 minutes ago.
    await expect(page.getByTestId('apps-list').locator('[data-testid^="app-row-"]').first()).toHaveAttribute(
      'data-testid',
      'app-row-storefront',
    );
  });

  test('the bare /forge lands on the Apps page', async ({ page }) => {
    await mockAppsBackend(page);
    await page.goto('/forge');
    await expect(page).toHaveURL(/\/apps$/, { timeout: 15_000 });
    await expect(page.getByTestId('apps-page')).toBeVisible();
  });

  test('with nothing deployed, the Apps page offers to build one', async ({ page }) => {
    await mockAppsBackend(page, { environments: [] });
    await page.goto('/apps');
    // The forge project exists but is not live: listed, with how to deploy it.
    await expect(page.getByTestId('app-row-storefront')).toContainText('Not deployed yet', { timeout: 15_000 });
    await page.getByRole('button', { name: 'Build an app' }).first().click();
    await expect(page).toHaveURL(new RegExp(`/project/${PROJECT.id}`));
    await expect(page.getByTestId('home-build-heading')).toBeVisible({ timeout: 15_000 });
  });

  test('the sidebar lists live apps with a link to each live site', async ({ page }) => {
    await mockAppsBackend(page);
    await page.goto(`/project/${PROJECT.id}`);
    const sidebarApps = page.getByTestId('sidebar-apps');
    await expect(sidebarApps).toBeVisible({ timeout: 15_000 });
    await expect(page.getByTestId('sidebar-app-link-storefront')).toHaveAttribute('href', 'https://bright-otter.reliantapps.dev');
    await expect(page.getByTestId('sidebar-app-link-status-page')).toHaveAttribute('href', 'https://status.example.com');
    await page.getByTestId('sidebar-app-storefront').click();
    await expect(page).toHaveURL(/\/forge\/p\/storefront/);
  });

  test("the home view leaves a forge project's apps to the sidebar", async ({ page }) => {
    await mockAppsBackend(page);
    await page.goto(`/project/${PROJECT.id}`);
    await expect(page.getByTestId('sidebar-apps')).toBeVisible({ timeout: 15_000 });
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
      const card = page.getByTestId('deployed-app-card');
      await expect(card).toBeVisible({ timeout: 15_000 });
      await expect(card).toHaveAttribute('data-env', 'prod');
      await expect(card).toContainText('storefront is deployed to prod');
      await expect(card.getByRole('link', { name: /bright-otter\.reliantapps\.dev/ })).toHaveAttribute(
        'href',
        'https://bright-otter.reliantapps.dev',
      );
      await card.getByTestId('deployed-app-open').click();
      await expect(page).toHaveURL(/\/forge\/p\/storefront/);
    });
  });

  test("the console's Overview leads with the live sites, never an API", async ({ page }) => {
    await mockAppsBackend(page);
    await page.goto('/forge/p/storefront');
    const frontends = page.getByRole('list', { name: 'Frontends' });
    await expect(frontends.getByRole('link', { name: /bright-otter\.reliantapps\.dev/ })).toHaveAttribute('href', 'https://bright-otter.reliantapps.dev', {
      timeout: 15_000,
    });
    // An API is not a destination for a person: there is no APIs strip.
    await expect(page.getByTestId('overview-live-links')).toHaveCount(0);
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
