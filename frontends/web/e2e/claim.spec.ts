import { expect, test, type Page } from '@playwright/test';

import { CLAIM_FRAGMENT, CLAIM_TOKEN, mockClaimBackend, SITE, type ClaimMockOptions } from './claimMocks';
import { PHONE } from './mobileMocks';

// The /claim page, every state, against a mocked control plane and a stateful
// fake GoTrue (claimMocks.ts). The REAL supabase-js, router and page run.
//
// What is mocked pending W5a (the server half of OpenClaimSession and
// CompleteClaim): both of those rpcs, and GoTrue itself. Everything else is the
// shipped app. When W5a merges, drive the happy path against a real stack with
// BASE_URL pointing at a vite dev server proxied to it; this file stays for the
// states a real server cannot be made to produce on demand.
//
// Screenshots: set CLAIM_SHOTS=<dir> to write each state at desktop and phone
// width. Not assertions; the assertions are the tests.

const SHOTS = process.env.CLAIM_SHOTS;
const LOADED = { timeout: 20_000 };

async function shot(page: Page, name: string) {
  if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}-${page.viewportSize()?.width}.png`, fullPage: true });
}

function collectErrors(page: Page): string[] {
  const errors: string[] = [];
  page.on('pageerror', (error) => errors.push(error.message));
  return errors;
}

async function arrive(page: Page, options: ClaimMockOptions = {}, hash = CLAIM_FRAGMENT) {
  const mock = await mockClaimBackend(page, options);
  await page.goto(`/claim${hash}`);
  return mock;
}

async function upgradeWithEmail(page: Page) {
  await page.getByLabel('Email address').fill('new@example.com');
  await page.getByLabel('Password', { exact: true }).fill('Sup3rSecret!pass');
  await page.getByLabel('Confirm Password').fill('Sup3rSecret!pass');
  await page.getByRole('button', { name: 'Create account and claim' }).click();
  await page.getByLabel(/Verification code/i).fill('123456');
  await page.getByRole('button', { name: /verify and continue/i }).click();
}

async function noSidewaysScroll(page: Page) {
  const width = await page.evaluate(() => document.documentElement.scrollWidth);
  const viewport = page.viewportSize()?.width ?? 0;
  expect(width).toBeLessThanOrEqual(viewport);
}

for (const viewport of [{ name: 'desktop', size: { width: 1280, height: 800 } }, { name: 'phone', size: PHONE.viewport }]) {
  test.describe(`/claim at ${viewport.name} width`, () => {
    test.use({ viewport: viewport.size, ...(viewport.name === 'phone' ? { isMobile: true, hasTouch: true } : {}) });

    test('shows the site before anyone signs in, and the token leaves the address bar', async ({ page }) => {
      const errors = collectErrors(page);
      const mock = await arrive(page);

      await expect(page.getByTestId('claim-site')).toBeVisible(LOADED);
      await expect(page.getByTestId('claim-site-name')).toHaveText(SITE.project);
      await expect(page.getByTestId('claim-site-host')).toContainText(SITE.hostnames[0]);
      await expect(page.getByTestId('claim-expiry')).toContainText(/goes offline/i);
      await expect(page.getByTestId('claim-submit')).toHaveText("Claim it, it's free");

      // The token is in neither the address bar nor any URL or header the page
      // sent; it travelled only in the GetClaim request body.
      expect(page.url()).not.toContain('rcl_');
      expect(mock.urls.filter((u) => u.includes(CLAIM_TOKEN))).toEqual([]);
      expect(mock.headerValues.filter((v) => v.includes(CLAIM_TOKEN))).toEqual([]);
      expect(mock.calls).toEqual([{ rpc: 'GetClaim', body: { claimToken: CLAIM_TOKEN } }]);
      expect(mock.gotrue).toEqual([]); // no session was asked for or created

      await noSidewaysScroll(page);
      expect(errors).toEqual([]);
      await shot(page, 'live');
    });

    test('a suspended site is shown as offline, with its deletion date', async ({ page }) => {
      await arrive(page, { state: 'SUSPENDED' });
      await expect(page.getByTestId('claim-site')).toHaveAttribute('data-site-state', 'suspended', LOADED);
      await expect(page.getByTestId('claim-expiry')).toContainText(/offline now/i);
      await expect(page.getByTestId('claim-expiry')).toContainText(/deleted/i);
      await noSidewaysScroll(page);
      await shot(page, 'suspended');
    });

    for (const [name, options, title] of [
      ['claimed', { state: 'CLAIMED' }, /already been claimed/i],
      ['expired', { state: 'EXPIRED' }, /has expired/i],
      ['taken-down', { state: 'TAKEN_DOWN' }, /was removed/i],
      ['not-found', { getClaimError: { code: 'not_found', reason: 'claimable_not_found' } }, /can't find this claim/i],
    ] as const) {
      test(`${name}: says why it cannot be claimed and offers no claim`, async ({ page }) => {
        const mock = await arrive(page, options as ClaimMockOptions);
        await expect(page.getByTestId('claim-page')).toContainText(title, LOADED);
        await expect(page.getByTestId('claim-submit')).toHaveCount(0);
        expect(mock.calls.map((c) => c.rpc)).toEqual(['GetClaim']);
        await noSidewaysScroll(page);
        await shot(page, name);
      });
    }

    test('an outage is shown as one, with a retry that recovers', async ({ page }) => {
      await mockClaimBackend(page, { getClaimError: { code: 'unavailable' } });
      await page.goto(`/claim${CLAIM_FRAGMENT}`);
      await expect(page.getByTestId('claim-unavailable')).toBeVisible(LOADED);
      await shot(page, 'unavailable');

      await page.unroute('**/controlplane.v1.ClaimableService/GetClaim');
      await page.route('**/controlplane.v1.ClaimableService/GetClaim', (route) =>
        route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ state: 'CLAIMABLE_STATE_LIVE', claimable: true, ...SITE, expiresAt: new Date(Date.now() + 70 * 3_600_000).toISOString() }),
        }),
      );
      await page.getByRole('button', { name: /try again/i }).click();
      await expect(page.getByTestId('claim-site')).toBeVisible();
    });

    test('a link opened without its fragment explains what to do', async ({ page }) => {
      await arrive(page, {}, '');
      await expect(page.getByText(/Open the claim link from your agent/i)).toBeVisible(LOADED);
      await shot(page, 'no-link');
    });

    test('a malformed fragment is scrubbed from the address bar and treated as no link', async ({ page }) => {
      await arrive(page, {}, '#rcl_mistyped');
      await expect(page.getByText(/Open the claim link from your agent/i)).toBeVisible(LOADED);
      expect(page.url()).not.toContain('rcl_');
    });

    test('an existing signed-in user is offered to claim into their account, not sent to /upgrade', async ({ page }) => {
      await arrive(page, { signedInAs: { id: 'real-user', email: 'me@example.com', anonymous: false } });
      const block = page.getByTestId('claim-into-account');
      await expect(block).toBeVisible(LOADED);
      await expect(page.getByTestId('claim-signed-in-as')).toContainText('me@example.com');
      await expect(block).toContainText(/you'll own it/i);
      await expect(block).toContainText(/one static site/i);
      await expect(page.getByTestId('claim-into-account-button')).toBeEnabled();
      await expect(page.getByTestId('claim-submit')).toHaveCount(0);
      await expect(page.getByTestId('claim-site')).toBeVisible();
      await noSidewaysScroll(page);
      await shot(page, 'claim-into-account');
    });

    test('a different anonymous account is warned about before it is replaced', async ({ page }) => {
      await arrive(page, { signedInAs: { id: 'someone-else', anonymous: true } });
      await expect(page.getByTestId('claim-replace-anonymous')).toContainText(/temporary account/i, LOADED);
      await expect(page.getByTestId('claim-submit')).toBeVisible();
      await shot(page, 'replace-anonymous');
    });

    test('an already-open claim in another browser is explained', async ({ page }) => {
      await arrive(page, { openSessionError: { code: 'failed_precondition', reason: 'claimable_session_released' } });
      await page.getByTestId('claim-submit').click();
      await expect(page.getByTestId('claim-refused-claimable_session_released')).toBeVisible(LOADED);
      await expect(page.getByTestId('claim-refused-claimable_session_released')).toContainText(/another browser/i);
      await shot(page, 'session-released');
    });
  });
}

test.describe('the whole new-human flow', () => {
  test('claim → upgrade with an email → CompleteClaim → /apps with the app highlighted', async ({ page }) => {
    const errors = collectErrors(page);
    const mock = await arrive(page);

    // 1. The preview, then "Claim it".
    await expect(page.getByTestId('claim-site')).toBeVisible(LOADED);
    await page.getByTestId('claim-submit').click();

    // 2. The released session was installed, and the EXISTING upgrade flow opens
    //    with the claim's returnTo. The page is claim-aware, not a second form.
    await expect(page).toHaveURL(/\/upgrade\?returnTo=%2Fclaim$/, LOADED);
    await expect(page.getByRole('heading', { name: 'Create your account' })).toBeVisible(LOADED);
    await expect(page.getByTestId('claim-existing-account')).toContainText(/signs out of the site's temporary account/i);
    expect(mock.calls.map((c) => c.rpc)).toEqual(['GetClaim', 'OpenClaimSession']);
    await shot(page, 'upgrade');

    // 3. Attach an email to the SAME user (updateUser), then confirm the code.
    await page.getByLabel('Email address').fill('new@example.com');
    await page.getByLabel('Password', { exact: true }).fill('Sup3rSecret!pass');
    await page.getByLabel('Confirm Password').fill('Sup3rSecret!pass');
    await page.getByRole('button', { name: 'Create account and claim' }).click();
    await page.getByLabel(/Verification code/i).fill('123456');
    await page.getByRole('button', { name: /verify and continue/i }).click();

    // 4. Back on /claim as the upgraded user, CompleteClaim runs and /apps opens.
    await expect(page).toHaveURL(/\/apps(\?|$)/, LOADED);
    expect(mock.calls.map((c) => c.rpc)).toEqual(['GetClaim', 'OpenClaimSession', 'CompleteClaim']);
    expect(mock.calls[2].body).toEqual({ claimToken: CLAIM_TOKEN });

    const card = page.getByTestId('app-card-storefront');
    await expect(card).toBeVisible(LOADED);
    await expect(card).toHaveAttribute('data-highlighted', 'true');
    await expect(page.getByTestId('apps-claimed')).toContainText('storefront is yours now');
    // The highlight is shown once: the param is dropped from the URL.
    await expect(page).toHaveURL(/\/apps$/);

    // 5. The token never reached a URL or a header; GoTrue saw the upgrade.
    expect(mock.urls.filter((u) => u.includes(CLAIM_TOKEN))).toEqual([]);
    expect(mock.headerValues.filter((v) => v.includes(CLAIM_TOKEN))).toEqual([]);
    expect(mock.gotrue).toContain('PUT /auth/v1/user');
    expect(errors).toEqual([]);
    await shot(page, 'apps-after-claim');
  });

  test('onboarding does not complete, and nothing sends the claimant to /onboarding on the way', async ({ page }) => {
    const completed: string[] = [];
    const mock = await mockClaimBackend(page);
    await page.route('**/controlplane.v1.UserService/CompleteOnboarding', (route) => {
      completed.push(route.request().url());
      return route.fulfill({ status: 200, contentType: 'application/json', body: '{}' });
    });
    const visited: string[] = [];
    page.on('framenavigated', (f) => f === page.mainFrame() && visited.push(new URL(f.url()).pathname));

    await page.goto(`/claim${CLAIM_FRAGMENT}`);
    await page.getByTestId('claim-submit').click();
    await expect(page).toHaveURL(/\/upgrade/, LOADED);

    expect(completed).toEqual([]);
    expect(visited.filter((p) => p.startsWith('/onboarding'))).toEqual([]);
    expect(mock.calls.map((c) => c.rpc)).not.toContain('CompleteClaim');
  });

  test('a reload mid-flow resumes the upgrade without asking for the session twice', async ({ page }) => {
    const mock = await arrive(page);
    await page.getByTestId('claim-submit').click();
    await expect(page).toHaveURL(/\/upgrade/, LOADED);

    // Back to the claim page with no fragment (the fragment was scrubbed).
    await page.goto('/claim');
    await expect(page.getByTestId('claim-submit')).toHaveText('Continue claiming', LOADED);
    await page.getByTestId('claim-submit').click();
    await expect(page).toHaveURL(/\/upgrade\?returnTo=%2Fclaim$/);
    expect(mock.calls.filter((c) => c.rpc === 'OpenClaimSession')).toHaveLength(1);
  });

  test('losing the race at CompleteClaim shows "already claimed"', async ({ page }) => {
    await arrive(page, { completeError: { code: 'failed_precondition', reason: 'claimable_claimed' } });
    await page.getByTestId('claim-submit').click();
    await expect(page).toHaveURL(/\/upgrade/, LOADED);

    await page.getByLabel('Email address').fill('new@example.com');
    await page.getByLabel('Password', { exact: true }).fill('Sup3rSecret!pass');
    await page.getByLabel('Confirm Password').fill('Sup3rSecret!pass');
    await page.getByRole('button', { name: 'Create account and claim' }).click();
    await page.getByLabel(/Verification code/i).fill('123456');
    await page.getByRole('button', { name: /verify and continue/i }).click();

    await expect(page.getByTestId('claim-refused-claimable_claimed')).toBeVisible(LOADED);
    await shot(page, 'lost-race');
  });
  test('claimable_upgrade_required sends them back through /upgrade once, then completes', async ({ page }) => {
    const mock = await arrive(page, { completeError: { code: 'permission_denied', reason: 'claimable_upgrade_required', times: 1 } });
    await page.getByTestId('claim-submit').click();
    await expect(page).toHaveURL(/\/upgrade/, LOADED);
    await upgradeWithEmail(page);

    await expect(page).toHaveURL(/\/apps(\?|$)/, LOADED);
    expect(mock.calls.map((c) => c.rpc)).toEqual(['GetClaim', 'OpenClaimSession', 'CompleteClaim', 'CompleteClaim']);
    await expect(page.getByTestId('apps-claimed')).toContainText('storefront is yours now');
  });

  test('an existing user claims into their account: CompleteClaim as themselves, then /apps with the app highlighted', async ({ page }) => {
    const errors = collectErrors(page);
    const mock = await arrive(page, {
      signedInAs: { id: 'real-user', email: 'me@example.com', anonymous: false },
      completeClaimPath: 'membership',
    });

    await page.getByTestId('claim-into-account-button').click({ timeout: 20_000 });

    await expect(page).toHaveURL(/\/apps(\?|$)/, LOADED);
    // No session was released, nothing was signed out, and the account was never upgraded.
    expect(mock.calls.map((c) => c.rpc)).toEqual(['GetClaim', 'CompleteClaim']);
    expect(mock.gotrue.some((g) => g.includes('/logout'))).toBe(false);
    await expect(page.getByTestId('apps-claimed')).toContainText('storefront is yours now');
    expect(errors).toEqual([]);
  });

  test('an existing user over the free site allowance is told what happens', async ({ page }) => {
    await arrive(page, {
      signedInAs: { id: 'real-user', email: 'me@example.com', anonymous: false },
      completeClaimPath: 'membership',
      exceedsFreeSiteAllowance: true,
    });
    await page.getByTestId('claim-into-account-button').click({ timeout: 20_000 });
    await expect(page.getByTestId('apps-claimed-over-allowance')).toContainText(/oldest live/i, LOADED);
  });

  test('"Sign in to claim" for someone not signed in goes to /auth and keeps the link good', async ({ page }) => {
    const mock = await arrive(page);
    await page.getByTestId('claim-existing-account-button').click({ timeout: 20_000 });
    await expect(page).toHaveURL(/\/auth\?redirect=%2Fclaim$/, LOADED);
    // No session was released for this, so the link still works for anyone.
    expect(mock.calls.map((c) => c.rpc)).toEqual(['GetClaim']);
  });

  test('a reason the page does not know gets a generic screen with a retry', async ({ page }) => {
    const mock = await arrive(page, { completeError: { code: 'permission_denied', reason: 'claimable_something_new', times: 1 } });
    await page.getByTestId('claim-submit').click();
    await expect(page).toHaveURL(/\/upgrade/, LOADED);
    await upgradeWithEmail(page);

    await expect(page.getByTestId('claim-finish-declined')).toBeVisible(LOADED);
    await shot(page, 'complete-declined');
    await page.getByRole('button', { name: /try again/i }).click();
    await expect(page).toHaveURL(/\/apps(\?|$)/, LOADED);
    expect(mock.calls.filter((c) => c.rpc === 'CompleteClaim')).toHaveLength(2);
  });
});
