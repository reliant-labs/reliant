import { expect, test } from '@playwright/test';

import { ENVIRONMENTS, mockAppsBackend } from './appsMocks';
import { PHONE } from './mobileMocks';

// The org switcher, against a mocked backend (appsMocks.ts): a person in two
// orgs lists them, marks the default, switches, and every control-plane call
// the Apps page makes then names the new org; a person in one org sees no
// switcher at all; the phone has the same choice on its Account screen; and the
// CLI-login approve page makes the org a credential will act in an explicit
// choice. Screenshots land in e2e/screenshots/org-switcher/.

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
    orgs: [ACME, SIDE],
    environmentsByOrg: { org_acme: ENVIRONMENTS, org_side: SIDE_ENVS },
    onOrgRequest,
  };
}

test.describe('org switcher (desktop)', () => {
  test.use({ viewport: { width: 1280, height: 800 } });

  test('lists my orgs with the default marked, and switching changes the org every call names', async ({ page }) => {
    const seen: Array<[string, string | undefined]> = [];
    await mockAppsBackend(page, twoOrgs((rpc, org) => seen.push([rpc, org])));
    await page.goto('/apps');

    // Starts in the default org: its apps, and its id on the wire.
    await expect(page.getByTestId('app-card-storefront')).toBeVisible(LOADED);
    await expect(page.getByTestId('org-switcher-name')).toHaveText('Acme');
    await expect.poll(() => seen.filter(([rpc]) => rpc === 'ListEnvironments').map(([, org]) => org)).toContain('org_acme');

    await page.getByTestId('org-switcher').click();
    const acme = page.getByRole('menuitemradio', { name: /Acme/ });
    await expect(acme).toHaveAttribute('aria-checked', 'true');
    await expect(acme).toContainText('Default');
    await expect(page.getByRole('menuitemradio', { name: /Side Project/ })).toHaveAttribute('aria-checked', 'false');
    await page.screenshot({ path: `${SHOTS}/desktop-switcher-open.png` });

    seen.length = 0;
    await page.getByRole('menuitemradio', { name: /Side Project/ }).click();

    // The other org's apps replace this org's, and the request names the new org.
    await expect(page.getByTestId('app-card-weekend-blog')).toBeVisible(LOADED);
    // The user's checkout remains as an undeployed draft; only the old org's
    // deployed environments disappear (projects themselves are user-scoped).
    await expect(page.getByTestId('app-card-storefront')).toContainText('Not deployed yet');
    await expect(page.getByTestId('app-card-status-page')).toHaveCount(0);
    await expect(page.getByTestId('org-switcher-name')).toHaveText('Side Project');
    expect(seen.filter(([rpc]) => rpc === 'ListEnvironments').every(([, org]) => org === 'org_side')).toBe(true);
    await page.screenshot({ path: `${SHOTS}/desktop-after-switch.png` });
  });

  test('the choice survives a reload', async ({ page }) => {
    await mockAppsBackend(page, twoOrgs());
    await page.goto('/apps');
    await page.getByTestId('org-switcher').click();
    await page.getByRole('menuitemradio', { name: /Side Project/ }).click();
    await expect(page.getByTestId('app-card-weekend-blog')).toBeVisible(LOADED);

    await page.reload();
    await expect(page.getByTestId('app-card-weekend-blog')).toBeVisible(LOADED);
    await expect(page.getByTestId('org-switcher-name')).toHaveText('Side Project');
  });

  test('a person in one org sees no switcher chrome', async ({ page }) => {
    await mockAppsBackend(page);
    await page.goto('/apps');
    await expect(page.getByTestId('app-card-storefront')).toBeVisible(LOADED);
    await expect(page.getByTestId('org-switcher')).toHaveCount(0);
    await page.screenshot({ path: `${SHOTS}/desktop-single-org-no-chrome.png` });
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
    await expect(page.getByTestId('app-card-weekend-blog')).toBeVisible(LOADED);

    membership = [ACME];
    await page.reload();
    await expect(page.getByTestId('app-card-storefront')).toBeVisible(LOADED);
    await expect(page.getByTestId('org-switcher')).toHaveCount(0);
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
    await mockAppsBackend(page);
    await page.goto('/m/account');
    await expect(page.getByRole('heading', { name: 'Account' })).toBeVisible(LOADED);
    await expect(page.getByTestId('mobile-org-section')).toHaveCount(0);
  });
});

test.describe('CLI login approve page', () => {
  const REQUEST =
    '?response_type=code&client_id=forge-cli&redirect_uri=http%3A%2F%2F127.0.0.1%3A5000%2Fcallback' +
    '&state=st&code_challenge=abc&code_challenge_method=S256&scope=deploy%3Awrite&device=laptop';

  async function signedIn(page: import('@playwright/test').Page, options = twoOrgs()) {
    await mockAppsBackend(page, options);
    await page.route('**/supabase/**', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: '{}' }),
    );
  }

  test('makes the org the credential acts in an explicit choice, and sends it', async ({ page }) => {
    await signedIn(page);
    let approved: Record<string, unknown> | undefined;
    await page.route('**/oauth/approve', async (route) => {
      approved = route.request().postDataJSON() as Record<string, unknown>;
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ redirect_to: 'http://127.0.0.1:1/never', scopes: ['deploy:write'], org_id: 'org_side' }),
      });
    });
    // A non-expiring mock Supabase session; no provider or live backend is used.
    const payload = Buffer.from(JSON.stringify({ sub: 'user_apps', exp: 4102444800 })).toString('base64url');
    const session = {
      access_token: `e30.${payload}.signature`, refresh_token: 'test-refresh', token_type: 'bearer',
      expires_at: 4102444800, expires_in: 3600,
      user: { id: 'user_apps', email: 'dev@example.com', aud: 'authenticated', app_metadata: {}, user_metadata: {}, created_at: '2026-01-01T00:00:00Z' },
    };
    await page.addInitScript(({ value, origin }) => {
      // The callback deliberately leaves the app origin. Seed only app pages,
      // not the browser's opaque network-error document at the loopback target.
      if (location.origin === origin) localStorage.setItem('supabase-auth', JSON.stringify(value));
    }, { value: session, origin: new URL(test.info().project.use.baseURL ?? 'http://localhost:5173').origin });
    await page.route('**/reliant.v1.SystemService/DevAuthLoad', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ success: true, sessionJson: JSON.stringify(session) }) }),
    );
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
