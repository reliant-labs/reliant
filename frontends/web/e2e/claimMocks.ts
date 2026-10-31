import { create, toBinary } from '@bufbuild/protobuf';
import { test, type Page, type Route } from '@playwright/test';

import { FreeSiteChoiceRequiredSchema } from '../src/gen/controlplane/services/claimable/v1/claimable_pb';

// The /claim page against no backend: control-plane's ClaimableService and a
// stateful fake of the one GoTrue endpoint set the flow touches, so the REAL
// supabase-js runs setSession, the email upgrade and the session refresh.
//
// W5a (the server half of OpenClaimSession and CompleteClaim) was not built when
// this was written; every response here follows claimable.proto. When W5a lands,
// run this spec's flow against the real stack instead (CLAIM_E2E_LIVE in
// claim.spec.ts's header) and keep this file for the states a real server
// cannot be made to produce on demand (taken down, throttled GoTrue, a lost race).
//
// Same approach as appsMocks.ts: broad `{}` fallbacks first, specific RPCs
// after (the LAST matching route wins).

export const CLAIM_TOKEN = 'rcl_9f3KQeAbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_';
export const CLAIM_FRAGMENT = `#${CLAIM_TOKEN}`;

const ANON_USER_ID = '7c1f0a52-3b0e-4f0e-9d7c-2a1c0f6c9a11';

export const EXISTING_SITE = { deploymentId: 'dep_mine', project: 'blog', env: 'prod', name: 'web', url: 'https://quiet-heron.sites.reliantlabs.dev' };
export const CLAIMED_SITE = { deploymentId: 'dep_new', project: 'storefront', env: 'prod', name: 'web', url: 'https://amber-quokka.reliantpreview.dev' };

export const SITE = {
  project: 'storefront',
  env: 'prod',
  hostnames: ['amber-quokka.reliantpreview.dev'],
};

export type ClaimStateName = 'LIVE' | 'SUSPENDED' | 'CLAIMED' | 'EXPIRED' | 'TAKEN_DOWN';

export interface ClaimMockOptions {
  /** What GetClaim answers. Default: a live site expiring in 70 hours. */
  state?: ClaimStateName;
  /** Make GetClaim fail with this Connect error (reason in x-forge-error-reason). */
  getClaimError?: { code: string; reason?: string; status?: number };
  openSessionError?: { code: string; reason?: string; status?: number };
  /** Make CompleteClaim fail with this error, `times` calls in a row (default: every call). */
  completeError?: { code: string; reason?: string; status?: number; times?: number };
  /** The claim path CompleteClaim answers with (default: "upgrade"). */
  completeClaimPath?: 'upgrade' | 'membership';
  /**
   * The claimer already has their one free static site (membership claims). The
   * mock behaves like the server: GetClaimFreeSite names both sites, and
   * CompleteClaim refuses with claimable_free_site_choice_required until it is
   * sent a choice, then reports the outcome and the paused site. `subscribed`
   * (see ClaimMock.subscribe) removes the conflict, as buying a plan does.
   */
  freeSiteConflict?: boolean;
  /**
   * Serve GetClaimFreeSite as "no choice needed" while CompleteClaim still
   * refuses: the site was added between the preview and the claim.
   */
  conflictAppearsAtClaim?: boolean;
  /** Start already signed in as this user (a real account, or a different anonymous one). */
  signedInAs?: { id: string; email?: string; anonymous: boolean };
}

export interface ClaimMock {
  /** Every ClaimableService call, in order, with the JSON body it carried. */
  calls: { rpc: string; body: Record<string, unknown> }[];
  /** Every request URL the page made, to prove the token never appears in one. */
  urls: string[];
  /** Every request's header values, flattened. */
  headerValues: string[];
  /** Requests that reached GoTrue, by "METHOD path". */
  gotrue: string[];
  /** The `freeSite` value each CompleteClaim carried, in order. */
  choices: string[];
  /** The buyer subscribes (in billing, say): the conflict is gone from now on. */
  subscribe: () => void;
}

const hours = (n: number) => new Date(Date.now() + n * 3_600_000).toISOString();

const json = (route: Route, body: unknown, status = 200) =>
  route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

const CODE_STATUS: Record<string, number> = {
  not_found: 404,
  failed_precondition: 400,
  unavailable: 503,
  unimplemented: 501,
  permission_denied: 403,
};

function connectError(route: Route, e: { code: string; reason?: string; status?: number }) {
  return route.fulfill({
    status: e.status ?? CODE_STATUS[e.code] ?? 500,
    contentType: 'application/json',
    headers: e.reason ? { 'x-forge-error-reason': e.reason } : {},
    body: JSON.stringify({ code: e.code, message: 'refused' }),
  });
}

/** A JWT the real supabase-js can decode: it only reads `exp` and `sub` locally. */
function fakeJwt(sub: string, anonymous: boolean, ttlSeconds = 3600): string {
  const b64 = (o: unknown) => Buffer.from(JSON.stringify(o)).toString('base64url');
  const exp = Math.floor(Date.now() / 1000) + ttlSeconds;
  return `${b64({ alg: 'HS256', typ: 'JWT' })}.${b64({ sub, exp, aud: 'authenticated', role: 'authenticated', is_anonymous: anonymous })}.sig`;
}

export async function mockClaimBackend(page: Page, options: ClaimMockOptions = {}): Promise<ClaimMock> {
  const origin = new URL(test.info().project.use.baseURL ?? 'http://localhost:5173').origin;
  const mock: ClaimMock = { calls: [], urls: [], headerValues: [], gotrue: [], choices: [], subscribe: () => { subscribed = true; } };
  let subscribed = false;

  // The account GoTrue is currently holding. The upgrade (PUT /user, then the
  // email_change verify) flips it to non-anonymous on the SAME id.
  const account = options.signedInAs
    ? { ...options.signedInAs }
    : { id: ANON_USER_ID, email: undefined as string | undefined, anonymous: true };
  let pendingEmail: string | undefined;
  const gotrueUser = () => ({
    id: account.id,
    aud: 'authenticated',
    role: 'authenticated',
    email: account.email ?? '',
    new_email: pendingEmail,
    is_anonymous: account.anonymous,
    email_confirmed_at: account.email ? new Date().toISOString() : undefined,
    app_metadata: {},
    user_metadata: {},
    created_at: new Date().toISOString(),
  });
  const gotrueSession = () => ({
    access_token: fakeJwt(account.id, account.anonymous),
    refresh_token: `refresh-${account.id}`,
    token_type: 'bearer',
    expires_in: 3600,
    expires_at: Math.floor(Date.now() / 1000) + 3600,
    user: gotrueUser(),
  });

  page.on('request', (req) => {
    mock.urls.push(req.url());
    for (const v of Object.values(req.headers())) mock.headerValues.push(v);
  });

  await page.route('**/reliant.v1.**', (route) => json(route, {}));
  await page.route('**/controlplane.v1.**', (route) => json(route, {}));
  await page.route('**/health', (route) => json(route, { auth_mode: 'supabase' }));

  // ── GoTrue ──────────────────────────────────────────────────────────────
  await page.route('**/supabase/**', async (route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname.replace(/^.*\/supabase/, '');
    mock.gotrue.push(`${req.method()} ${path}`);

    if (path === '/auth/v1/user' && req.method() === 'GET') return json(route, gotrueUser());
    if (path === '/auth/v1/user' && req.method() === 'PUT') {
      const body = req.postDataJSON() as { email?: string };
      pendingEmail = body.email;
      return json(route, gotrueUser());
    }
    if (path === '/auth/v1/verify' && req.method() === 'POST') {
      // The emailed code confirms the pending address: the SAME user, now real.
      account.email = pendingEmail;
      account.anonymous = false;
      pendingEmail = undefined;
      return json(route, gotrueSession());
    }
    if (path === '/auth/v1/token') {
      // grant_type=refresh_token: re-issue from the (possibly upgraded) user.
      return json(route, gotrueSession());
    }
    return json(route, {});
  });

  // ── control-plane: the claim rpcs ───────────────────────────────────────
  const claimRpc = (
    rpc: string,
    answer: (body: Record<string, unknown>) => unknown,
    failure?: ClaimMockOptions['getClaimError'] & { times?: number },
  ) => {
    let failed = 0;
    return page.route(`**/controlplane.v1.ClaimableService/${rpc}`, async (route) => {
      const body = (route.request().postDataJSON() ?? {}) as Record<string, unknown>;
      mock.calls.push({ rpc, body });
      if (failure && failed < (failure.times ?? Infinity)) {
        failed += 1;
        return connectError(route, failure);
      }
      return json(route, answer(body));
    });
  };

  await claimRpc(
    'GetClaim',
    () => ({
      state: `CLAIMABLE_STATE_${options.state ?? 'LIVE'}`,
      claimable: (options.state ?? 'LIVE') === 'LIVE' || options.state === 'SUSPENDED',
      ...SITE,
      expiresAt: hours(options.state === 'SUSPENDED' ? -2 : 70),
      purgeAfter: hours(options.state === 'SUSPENDED' ? 166 : 238),
    }),
    options.getClaimError,
  );
  await claimRpc(
    'OpenClaimSession',
    () => {
      // The anonymous user that deployed the site: the session the page installs.
      account.id = ANON_USER_ID;
      account.anonymous = true;
      account.email = undefined;
      const s = gotrueSession();
      return { accessToken: s.access_token, refreshToken: s.refresh_token, expiresIn: '3600' };
    },
    options.openSessionError,
  );
  const conflictNow = () => (options.freeSiteConflict ?? false) && !subscribed;

  await claimRpc(
    'GetClaimFreeSite',
    () =>
      conflictNow() && !options.conflictAppearsAtClaim
        ? { choiceRequired: true, existingSite: EXISTING_SITE, claimedSite: CLAIMED_SITE }
        : { choiceRequired: false },
    undefined,
  );

  await page.route('**/controlplane.v1.ClaimableService/CompleteClaim', async (route) => {
    const body = (route.request().postDataJSON() ?? {}) as Record<string, unknown>;
    mock.calls.push({ rpc: 'CompleteClaim', body });
    mock.choices.push(String(body.freeSite ?? ''));
    const failure = options.completeError;
    if (failure && mock.calls.filter((c) => c.rpc === 'CompleteClaim').length <= (failure.times ?? Infinity)) {
      return connectError(route, failure);
    }
    const choice = body.freeSite as string | undefined;
    if (conflictNow() && !choice) {
      // The refusal carries the two sites as a typed detail.
      return route.fulfill({
        status: 400,
        contentType: 'application/json',
        headers: { 'x-forge-error-reason': 'claimable_free_site_choice_required' },
        body: JSON.stringify({
          code: 'failed_precondition',
          message: 'choose',
          details: [
            {
              type: 'controlplane.v1.FreeSiteChoiceRequired',
              // Connect carries a detail as the message's proto BINARY, base64 without padding.
              value: Buffer.from(
                toBinary(
                  FreeSiteChoiceRequiredSchema,
                  create(FreeSiteChoiceRequiredSchema, { existingSite: EXISTING_SITE, claimedSite: CLAIMED_SITE }),
                ),
              )
                .toString('base64')
                .replace(/=+$/, ''),
            },
          ],
        }),
      });
    }
    const kept = conflictNow() ? choice : undefined;
    return json(route, {
      orgId: 'org_claimed',
      claimPath: options.completeClaimPath ?? 'upgrade',
      hostnames: ['amber-quokka.sites.reliantlabs.dev'],
      holds: [],
      freeSiteOutcome:
        kept === 'FREE_SITE_CHOICE_CLAIMED'
          ? 'FREE_SITE_OUTCOME_CLAIMED_SITE_LIVE'
          : kept === 'FREE_SITE_CHOICE_EXISTING'
            ? 'FREE_SITE_OUTCOME_EXISTING_SITE_LIVE'
            : options.completeClaimPath === 'membership'
              ? 'FREE_SITE_OUTCOME_NO_CONFLICT'
              : 'FREE_SITE_OUTCOME_UNSPECIFIED',
      pausedSite: kept === 'FREE_SITE_CHOICE_CLAIMED' ? EXISTING_SITE : kept === 'FREE_SITE_CHOICE_EXISTING' ? CLAIMED_SITE : undefined,
    });
  });

  // An existing user has onboarded already; a new human has not (the claim must not send them there).
  await page.route('**/controlplane.v1.UserService/GetCurrentUser', (route) =>
    json(route, { user: { id: 'user_claimed', email: 'new@example.com', onboardingCompleted: Boolean(options.signedInAs) } }),
  );
  // The orgs the signed-in person belongs to. A membership claim adds the
  // claimed org and the page switches to it, so the list names both.
  await page.route('**/controlplane.v1.OrganizationService/ListMyOrganizations', (route) =>
    json(route, {
      organizations: [
        { id: 'org_own', name: 'My workspace', slug: 'my-workspace', role: 'owner', isOwner: true, isDefault: true, createdAt: hours(-2000) },
        ...(options.completeClaimPath === 'membership'
          ? [{ id: 'org_claimed', name: 'Personal workspace', slug: 'personal-claimed', role: 'owner', isOwner: true, isDefault: false, createdAt: hours(-72) }]
          : []),
      ],
      defaultOrgId: 'org_own',
    }),
  );
  await page.route('**/controlplane.v1.DeployService/ListEnvironments', (route) =>
    json(route, {
      environments: [{ id: 'env_sf_prod', name: 'prod', project: SITE.project, kind: 'DEPLOY_ENVIRONMENT_KIND_PERSISTENT', createdAt: hours(-72) }],
    }),
  );
  await page.route('**/controlplane.v1.DeployService/GetStatus', (route) =>
    json(route, {
      environmentVerdict: 'DEPLOY_VERDICT_CONVERGED',
      currentPromotion: { id: 'promo_1', environmentId: 'env_sf_prod', releaseId: 'rel_1', releaseVersion: 'v1.0.0', kind: 'DEPLOY_PROMOTION_KIND_PROMOTE', createdAt: hours(-70) },
      deployments: [
        {
          deployment: {
            id: 'dep_web',
            environmentId: 'env_sf_prod',
            name: 'web',
            tier: 'DEPLOY_TIER_STATIC',
            runState: 'DEPLOY_RUN_STATE_RUNNING',
            observed: { state: 'DEPLOY_OBSERVED_STATE_READY', url: 'https://amber-quokka.sites.reliantlabs.dev' },
          },
          verdict: 'DEPLOY_VERDICT_CONVERGED',
          observedAt: hours(-1),
        },
      ],
    }),
  );

  // Cloud mode: a control plane and a Supabase to talk to. Served here, not set
  // in an init script, so the built app's own config.js cannot replace it.
  const config = { AUTH_MODE: 'cloud', API_URL: origin, CONTROL_PLANE_API_URL: origin, SUPABASE_URL: `${origin}/supabase`, SUPABASE_ANON_KEY: 'anon-test-key' };
  await page.route('**/config.js', (route) =>
    route.fulfill({ status: 200, contentType: 'text/javascript', body: `window.__FORGE_CONFIG__ = ${JSON.stringify(config)};` }),
  );

  if (options.signedInAs) {
    const s = gotrueSession();
    await page.addInitScript(
      ([key, value]) => {
        if (!window.localStorage.getItem(key)) window.localStorage.setItem(key, value);
      },
      ['supabase-auth', JSON.stringify(s)],
    );
  }

  return mock;
}
