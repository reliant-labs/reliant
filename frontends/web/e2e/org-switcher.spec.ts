import { expect, test } from '@playwright/test';

import { ENVIRONMENTS, PROJECT, mockAppsBackend } from './appsMocks';
import { PHONE } from './mobileMocks';

// The org switcher, against a mocked backend (appsMocks.ts): a person in two
// orgs lists them, marks the default, switches, and every control-plane call
// the Apps page makes then names the new org, as do the project and chat
// lists (projects and chats belong to one org, so the old org's leave); a person in one org sees it
// named, with "Create organization"; the phone has the same choice on its Account screen; and the
// CLI-login approve page makes the org a credential will act in an explicit
// choice. Screenshots land in e2e/screenshots/org-switcher/.
//
// Every test signs in as a person (`person: true`): an API-key session is a
// machine credential, which has no membership list to choose an org from.

const SHOTS = 'e2e/screenshots/org-switcher';
const LOADED = { timeout: 15_000 };
const errors = new WeakMap<import('@playwright/test').Page, string[]>();
test.beforeEach(({ page }) => {
  const recorded: string[] = [];
  errors.set(page, recorded);
  page.on('pageerror', (error) => recorded.push(error.message));
});
test.afterEach(({ page }) => {
  expect(errors.get(page), 'no uncaught JavaScript errors').toEqual([]);
});

const ACME = { id: 'org_acme', name: 'Acme', slug: 'acme', role: 'owner', isOwner: true, isDefault: true };
const SIDE = { id: 'org_side', name: 'Side Project', slug: 'side', role: 'member', isOwner: false, isDefault: false };

const SIDE_ENVS = [
  { id: 'env_blog_prod', name: 'prod', project: 'weekend-blog', kind: 'DEPLOY_ENVIRONMENT_KIND_PERSISTENT', createdAt: new Date(Date.now() - 9_000_000).toISOString() },
];

function twoOrgs(onOrgRequest?: (rpc: string, orgId: string | undefined) => void) {
  return {
    person: true,
    orgs: [ACME, SIDE],
    environmentsByOrg: { org_acme: ENVIRONMENTS, org_side: SIDE_ENVS },
    // The storefront checkout is Acme's; Side Project has none yet.
    projectsByOrg: { org_acme: [{ ...PROJECT, orgId: 'org_acme' }], org_side: [] },
    onOrgRequest,
  };
}

test.describe('org switcher (desktop)', () => {
  test.use({ viewport: { width: 1280, height: 800 } });

  test('lists my orgs with the default marked, and switching changes the org every call names', async ({ page }) => {
    const seen: Array<[string, string | undefined]> = [];
    await mockAppsBackend(page, twoOrgs((rpc, org) => seen.push([rpc, org])));
    await page.goto('/apps');

    // Starts in the default org: its apps, and never another org on the wire.
    // A tab with nothing saved may read before the membership list answers,
    // naming no org, which the control plane answers in the person's default:
    // the org the tab then resolves to, so that read is not repeated.
    await expect(page.getByTestId('app-row-storefront')).toBeVisible(LOADED);
    await expect(page.getByTestId('org-switcher-name')).toHaveText('Acme');
    const initial = seen.filter(([rpc]) => rpc === 'ListEnvironments').map(([, org]) => org);
    expect(initial.length).toBeGreaterThan(0);
    expect(initial.every((org) => org === 'org_acme' || org === undefined)).toBe(true);
    // The product's lists wait for the tab's org instead (lib/orgScope): with
    // no org they would be answered with every org's rows.
    await expect.poll(() => seen.filter(([rpc]) => rpc === 'ListProjects').map(([, org]) => org)).toContain('org_acme');
    expect(seen.filter(([rpc]) => rpc === 'ListProjects' || rpc === 'ListChats').every(([, org]) => org === 'org_acme')).toBe(true);

    await page.getByTestId('org-switcher').click();
    const acme = page.getByRole('menuitemradio', { name: /Acme/ });
    await expect(acme).toHaveAttribute('aria-checked', 'true');
    await expect(acme).toContainText('Default');
    await expect(page.getByRole('menuitemradio', { name: /Side Project/ })).toHaveAttribute('aria-checked', 'false');
    await page.screenshot({ path: `${SHOTS}/desktop-switcher-open.png` });

    seen.length = 0;
    await page.getByRole('menuitemradio', { name: /Side Project/ }).click();

    // The other org's apps replace this org's, and the request names the new org.
    await expect(page.getByTestId('app-row-weekend-blog')).toBeVisible(LOADED);
    // Acme's checkout leaves with Acme: projects belong to one org, and the
    // project list is re-read for the new one at once, not on its next fetch.
    await expect(page.getByTestId('app-row-storefront')).toHaveCount(0);
    await expect(page.getByTestId('app-row-status-page')).toHaveCount(0);
    await expect(page.getByTestId('org-switcher-name')).toHaveText('Side Project');
    expect(seen.filter(([rpc]) => rpc === 'ListEnvironments').every(([, org]) => org === 'org_side')).toBe(true);
    await expect.poll(() => seen.filter(([rpc]) => rpc === 'ListProjects').map(([, org]) => org)).toContain('org_side');
    expect(seen.filter(([rpc]) => rpc === 'ListProjects' || rpc === 'ListChats').every(([, org]) => org === 'org_side')).toBe(true);
    await page.screenshot({ path: `${SHOTS}/desktop-after-switch.png` });
  });

  test('the choice survives a reload', async ({ page }) => {
    await mockAppsBackend(page, twoOrgs());
    await page.goto('/apps');
    await page.getByTestId('org-switcher').click();
    await page.getByRole('menuitemradio', { name: /Side Project/ }).click();
    await expect(page.getByTestId('app-row-weekend-blog')).toBeVisible(LOADED);

    await page.reload();
    await expect(page.getByTestId('app-row-weekend-blog')).toBeVisible(LOADED);
    await expect(page.getByTestId('org-switcher-name')).toHaveText('Side Project');
  });

  test('a person in one org sees their org and a way to create another', async ({ page }) => {
    await mockAppsBackend(page, { person: true });
    await page.goto('/apps');
    await expect(page.getByTestId('app-row-storefront')).toBeVisible(LOADED);
    await page.getByTestId('org-switcher').first().click();
    await expect(page.getByTestId('org-create')).toBeVisible();
    await page.screenshot({ path: `${SHOTS}/desktop-single-org.png` });
  });

  test('leaving the selected org falls back to the default on the next read', async ({ page }) => {
    let membership = [ACME, SIDE];
    await mockAppsBackend(page, { ...twoOrgs(), orgs: membership });
    await page.route('**/controlplane.v1.OrganizationService/ListMyOrganizations', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ organizations: membership, defaultOrgId: 'org_acme' }),
      }),
    );
    await page.goto('/apps');
    await page.getByTestId('org-switcher').click();
    await page.getByRole('menuitemradio', { name: /Side Project/ }).click();
    await expect(page.getByTestId('app-row-weekend-blog')).toBeVisible(LOADED);

    membership = [ACME];
    await page.reload();
    await expect(page.getByTestId('app-row-storefront')).toBeVisible(LOADED);
    await expect(page.getByTestId('org-switcher-name').first()).toHaveText('Acme');
  });
});

test.describe('org switcher (phone)', () => {
  test.use(PHONE);

  test('the Account screen lists my orgs and switches', async ({ page }) => {
    const seen: Array<[string, string | undefined]> = [];
    await mockAppsBackend(page, twoOrgs((rpc, org) => seen.push([rpc, org])));
    await page.goto('/m/account');

    const section = page.getByTestId('mobile-org-section');
    await expect(section).toBeVisible(LOADED);
    await expect(section.getByRole('radio', { name: /Acme/ })).toHaveAttribute('aria-checked', 'true');
    await expect(section.getByRole('radio', { name: /Side Project/ })).toHaveAttribute('aria-checked', 'false');
    await page.screenshot({ path: `${SHOTS}/mobile-account-orgs.png` });

    await section.getByRole('radio', { name: /Side Project/ }).click();
    await expect(section.getByRole('radio', { name: /Side Project/ })).toHaveAttribute('aria-checked', 'true');
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
    await page.screenshot({ path: `${SHOTS}/mobile-account-orgs-switched.png` });
  });

  test('a person in one org has no Organization section', async ({ page }) => {
    await mockAppsBackend(page, { person: true });
    await page.goto('/m/account');
    await expect(page.getByRole('heading', { name: 'Account' })).toBeVisible(LOADED);
    await expect(page.getByTestId('mobile-org-section')).toHaveCount(0);
  });
});

test.describe('CLI login approve page', () => {
  const REQUEST =
    '?response_type=code&client_id=forge-cli&redirect_uri=http%3A%2F%2F127.0.0.1%3A5000%2Fcallback' +
    '&state=st&code_challenge=abc&code_challenge_method=S256&scope=deploy%3Awrite&device=laptop';

  test('makes the org the credential acts in an explicit choice, and sends it', async ({ page }) => {
    await mockAppsBackend(page, twoOrgs());
    let approved: Record<string, unknown> | undefined;
    await page.route('**/oauth/approve', async (route) => {
      approved = route.request().postDataJSON() as Record<string, unknown>;
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ redirect_to: 'http://127.0.0.1:1/never', scopes: ['deploy:write'], org_id: 'org_side' }),
      });
    });
    await page.goto(`/oauth/cli${REQUEST}`);

    const picker = page.getByTestId('authorize-org');
    await expect(picker).toBeVisible(LOADED);
    await expect(picker.getByRole('radio', { name: /Acme/ })).toHaveAttribute('aria-checked', 'true');
    await picker.getByRole('radio', { name: /Side Project/ }).click();
    await expect(picker).toContainText('cannot be moved to another organization later');
    await page.screenshot({ path: `${SHOTS}/desktop-approve-cli-login.png` });
    await page.getByRole('button', { name: 'Approve' }).click();
    await expect.poll(() => approved?.org_id).toBe('org_side');
  });
});
