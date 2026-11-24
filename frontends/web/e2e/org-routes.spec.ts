import { expect, test, type Page } from '@playwright/test';

import { PROJECT, mockAppsBackend } from './appsMocks';

// The org routes with the flag web.org_routes on (ORG_ROUTES="true" in
// config.js), against a mocked backend (appsMocks.ts), for a person in two
// orgs (control-plane docs/design/unified-shell-and-projects.md §3.4, D8):
// an old org-less URL lands on its `/o/<org>/…` form (a project's in the
// project's own org), and a link into the other org switches the tab's org
// with one notice. With the flag off the old URLs open as they are.

const ACME = { id: 'org_acme', name: 'Acme', slug: 'acme', role: 'owner', isOwner: true, isDefault: true };
const SIDE = { id: 'org_side', name: 'Side Project', slug: 'side', role: 'member', isOwner: false, isDefault: false };
const BLOG = { ...PROJECT, id: 'proj_blog', name: 'weekend-blog', forgeProjectName: 'weekend-blog', orgId: 'org_side' };

const LOADED = { timeout: 15_000 };

function twoOrgs(orgRoutes: boolean) {
  return {
    person: true,
    orgRoutes,
    orgs: [ACME, SIDE],
    environmentsByOrg: { org_acme: [], org_side: [] },
    projectsByOrg: { org_acme: [{ ...PROJECT, orgId: 'org_acme' }], org_side: [BLOG] },
  };
}

/** The arrival notices on screen (lib/orgArrivalNotice.ts, one toast id). */
const notices = (page: Page) => page.locator('[data-sonner-toast]').filter({ hasText: /opened there|to open this link/ });

const errors = new WeakMap<Page, string[]>();
test.beforeEach(({ page }) => {
  const recorded: string[] = [];
  errors.set(page, recorded);
  page.on('pageerror', (error) => recorded.push(error.message));
});
test.afterEach(({ page }) => {
  expect(errors.get(page), 'no uncaught JavaScript errors').toEqual([]);
});

test.describe('org routes, flag on', () => {
  test.use({ viewport: { width: 1280, height: 800 } });

  test('the bare / lands on the org home', async ({ page }) => {
    await mockAppsBackend(page, twoOrgs(true));
    await page.goto('/');
    await expect(page).toHaveURL(/\/o\/acme$/, LOADED);
    await expect(notices(page)).toHaveCount(0);
  });

  test("an old /project/<id> lands on the project's org form, keeping the query", async ({ page }) => {
    await mockAppsBackend(page, twoOrgs(true));
    await page.goto(`/project/${PROJECT.id}?view=detailed`);
    await expect(page).toHaveURL(new RegExp(`/o/acme/project/${PROJECT.id}\\?view=detailed$`), LOADED);
    // In the tab's own org: nothing to say.
    await expect(notices(page)).toHaveCount(0);
  });

  test("an old /projects/<id> of the other org lands in that org, with one notice", async ({ page }) => {
    await mockAppsBackend(page, twoOrgs(true));
    await page.goto(`/projects/${BLOG.id}/settings`);
    await expect(page).toHaveURL(new RegExp(`/o/side/projects/${BLOG.id}/settings$`), LOADED);
    await expect(notices(page)).toHaveText(['This project is in Side Project, so it opened there.'], LOADED);
    await expect(page.getByTestId('org-switcher-name')).toHaveText('Side Project', LOADED);
  });

  test('a link into the other org switches the org with one notice', async ({ page }) => {
    await mockAppsBackend(page, twoOrgs(true));
    await page.goto('/projects');
    await expect(page).toHaveURL(/\/o\/acme\/projects$/, LOADED);
    await expect(page.getByTestId('org-switcher-name')).toHaveText('Acme', LOADED);

    await page.goto('/o/side/projects');
    await expect(notices(page)).toHaveText(['Switched to Side Project to open this link.'], LOADED);
    await expect(page.getByTestId('org-switcher-name')).toHaveText('Side Project');
  });

  test('switching org with the switcher says nothing', async ({ page }) => {
    await mockAppsBackend(page, twoOrgs(true));
    await page.goto('/projects');
    await expect(page).toHaveURL(/\/o\/acme\/projects$/, LOADED);
    await page.getByTestId('org-switcher').click();
    await page.getByRole('menuitemradio', { name: /Side Project/ }).click();
    await expect(page).toHaveURL(/\/o\/side\/projects$/, LOADED);
    await expect(page.getByTestId('org-switcher-name')).toHaveText('Side Project');
    await expect(notices(page)).toHaveCount(0);
  });
});

test.describe('org routes, flag off', () => {
  test.use({ viewport: { width: 1280, height: 800 } });

  test('an old /project/<id> opens where it is', async ({ page }) => {
    await mockAppsBackend(page, twoOrgs(false));
    await page.goto(`/project/${PROJECT.id}`);
    await expect(page.getByRole('button', { name: /^Organization: Acme/ })).toBeVisible(LOADED);
    expect(new URL(page.url()).pathname).toBe(`/project/${PROJECT.id}`);
  });
});
