import { test, type Page, type Route } from '@playwright/test';

// A signed-in, onboarded user whose project is a deployed forge app, against
// no backend: every Connect RPC is answered here. Used by apps.spec.ts, which
// exercises the surfaces that put deployed apps in front of the user — the
// Apps index, the sidebar, the home view, a chat that deployed, and the
// onboarding choice between building an app and connecting a repo.
//
// Same approach as mobileMocks.ts: broad `{}` fallbacks first, specific RPCs
// after (the LAST matching route wins), and the API-key login path so no
// Supabase session is needed. The control plane is configured through
// window.__FORGE_CONFIG__ so the deploy reads go out at all.

const minutesAgo = (n: number) => new Date(Date.now() - n * 60_000).toISOString();

export const PROJECT = {
  id: 'proj_store',
  userId: 'user_apps',
  name: 'storefront',
  path: '/home/workspace/projects/storefront',
  isGitRepo: true,
  defaultBranch: 'main',
  createdAt: '2026-10-01T00:00:00Z',
  updatedAt: '2026-10-20T00:00:00Z',
  lastActive: '2026-10-20T00:00:00Z',
  isForge: true,
  forgeProjectName: 'storefront',
};

/** Every environment the org holds: two apps, one of them never opened as a Reliant project. */
export const ENVIRONMENTS = [
  { id: 'env_store_prod', name: 'prod', project: 'storefront', kind: 'DEPLOY_ENVIRONMENT_KIND_PERSISTENT', createdAt: minutesAgo(9000) },
  { id: 'env_store_staging', name: 'staging', project: 'storefront', kind: 'DEPLOY_ENVIRONMENT_KIND_PERSISTENT', createdAt: minutesAgo(9000) },
  { id: 'env_status_prod', name: 'prod', project: 'status-page', kind: 'DEPLOY_ENVIRONMENT_KIND_PERSISTENT', createdAt: minutesAgo(20000) },
];

const promotion = (environmentId: string, version: string, ago: number) => ({
  id: `promo_${environmentId}`,
  environmentId,
  releaseId: `rel_${version}`,
  releaseVersion: version,
  kind: 'DEPLOY_PROMOTION_KIND_PROMOTE',
  promotedByActor: 'reliant',
  createdAt: minutesAgo(ago),
});

const deployment = (environmentId: string, name: string, tier: string, url: string) => ({
  deployment: {
    id: `dep_${environmentId}_${name}`,
    environmentId,
    name,
    tier,
    runState: 'DEPLOY_RUN_STATE_RUNNING',
    observed: { state: 'DEPLOY_OBSERVED_STATE_READY', url },
  },
  verdict: 'DEPLOY_VERDICT_CONVERGED',
  observedAt: minutesAgo(1),
});

/** GetStatus per environment: a static frontend and an API in each storefront env. */
export const STATUSES: Record<string, unknown> = {
  env_store_prod: {
    environmentVerdict: 'DEPLOY_VERDICT_CONVERGED',
    currentPromotion: promotion('env_store_prod', 'v1.4.0', 12),
    deployments: [
      deployment('env_store_prod', 'web', 'DEPLOY_TIER_STATIC', 'https://bright-otter.reliantapps.dev'),
      deployment('env_store_prod', 'api', 'DEPLOY_TIER_BACKEND', 'https://calm-heron.reliantapps.dev'),
      deployment('env_store_prod', 'db', 'DEPLOY_TIER_DATABASE', ''),
    ],
  },
  env_store_staging: {
    environmentVerdict: 'DEPLOY_VERDICT_CONVERGING',
    currentPromotion: promotion('env_store_staging', 'v1.5.0-rc.1', 3),
    deployments: [
      deployment('env_store_staging', 'web', 'DEPLOY_TIER_STATIC', 'https://quiet-finch.reliantapps.dev'),
      deployment('env_store_staging', 'api', 'DEPLOY_TIER_BACKEND', 'https://brisk-lynx.reliantapps.dev'),
    ],
  },
  env_status_prod: {
    environmentVerdict: 'DEPLOY_VERDICT_CONVERGED',
    currentPromotion: promotion('env_status_prod', 'v0.3.2', 60 * 26),
    deployments: [deployment('env_status_prod', 'site', 'DEPLOY_TIER_STATIC', 'https://status.example.com')],
  },
};

/** The deploy chat: an agent that built the storefront and shipped it to prod. */
export const DEPLOY_CHAT = {
  id: 'chat_deploy',
  userId: 'user_apps',
  title: 'Ship the storefront to prod',
  projectId: PROJECT.id,
  state: 1,
  createdAt: minutesAgo(60),
  updatedAt: minutesAgo(10),
  lastActive: minutesAgo(10),
  lastMessageAt: minutesAgo(10),
  activity: 0,
};

export const CHATS = [
  DEPLOY_CHAT,
  { ...DEPLOY_CHAT, id: 'chat_b', title: 'Add a newsletter signup', lastActive: minutesAgo(90), updatedAt: minutesAgo(90) },
];

export const DEPLOY_MESSAGES = [
  {
    id: 'msg_1',
    seq: '1',
    thread: 'main',
    role: 1,
    createdAt: minutesAgo(20),
    updatedAt: minutesAgo(20),
    contentBlocks: [{ id: 'b1', index: 0, type: 1, content: 'Looks good. Deploy it to prod.' }],
  },
  {
    id: 'msg_2',
    seq: '2',
    thread: 'main',
    role: 2,
    createdAt: minutesAgo(19),
    updatedAt: minutesAgo(19),
    contentBlocks: [
      { id: 'b2', index: 0, type: 1, content: 'Deploying the storefront to prod.' },
      {
        id: 'b3',
        index: 1,
        type: 2,
        toolName: 'shell',
        toolCallId: 'tc_deploy',
        input: JSON.stringify({ command: 'reliant forge env deploy prod', description: 'Deploy the storefront to prod' }),
        toolCallStatus: 3,
      },
      { id: 'b4', index: 2, type: 1, content: 'Done. v1.4.0 is live in prod.' },
    ],
  },
  // Results are separate TOOL messages, joined by toolCallId. The server no
  // longer copies matchedResult onto an assistant's call block.
  {
    id: 'msg_3', seq: '3', thread: 'main', role: 4,
    createdAt: minutesAgo(19), updatedAt: minutesAgo(19),
    contentBlocks: [{
      id: 'b5', index: 0, type: 3, toolCallId: 'tc_deploy', toolName: 'shell',
      content: JSON.stringify({ stdout: 'release v1.4.0 promoted to prod\n', stderr: '', exit_code: 0 }),
    }],
  },
];

const json = (route: Route, body: unknown) =>
  route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });

/** A Connect server-stream that ends at once, cleanly: an end-stream envelope with `{}`. */
const emptyStream = (route: Route) =>
  route.fulfill({
    status: 200,
    contentType: 'application/connect+proto',
    body: Buffer.from([0x02, 0, 0, 0, 2, 0x7b, 0x7d]),
  });

export interface AppsMockOptions {
  /**
   * The orgs ListMyOrganizations returns. Default: one, so no switcher shows.
   * Environments are served per org (`environmentsByOrg`) when given.
   */
  orgs?: Array<{ id: string; name: string; slug: string; role: string; isOwner: boolean; isDefault: boolean }>;
  /** Environments by org id; an org without an entry has none. Overrides `environments`. */
  environmentsByOrg?: Record<string, typeof ENVIRONMENTS>;
  /** Called with the org_id of every control-plane request that carried one (undefined: none named). */
  onOrgRequest?: (rpc: string, orgId: string | undefined) => void;
  /** Whether the user has finished onboarding. Default true. */
  onboarded?: boolean;
  /** The org's deploy environments. Default ENVIRONMENTS; [] for a user who has deployed nothing. */
  environments?: typeof ENVIRONMENTS;
}

export async function mockAppsBackend(page: Page, options: AppsMockOptions = {}): Promise<void> {
  const onboarded = options.onboarded ?? true;
  const environments = options.environments ?? ENVIRONMENTS;
  // The control plane is served from the app's own origin: every RPC below is mocked there.
  const origin = new URL(test.info().project.use.baseURL ?? 'http://localhost:5173').origin;

  await page.route('**/reliant.v1.**', (route) => json(route, {}));
  await page.route('**/controlplane.v1.**', (route) => json(route, {}));
  await page.route('**/reliant.v1.StreamingService/**', emptyStream);
  await page.route('**/health', (route) => json(route, { auth_mode: 'apikey' }));
  await page.route('**/supabase/**', (route) => json(route, {}));

  await page.route('**/controlplane.v1.UserService/GetCurrentUser', (route) =>
    json(route, { user: { id: 'user_apps', email: 'dev@example.com', onboardingCompleted: onboarded } }),
  );
  await page.route('**/controlplane.v1.BillingService/GetCurrentUserComputeEligibility', (route) =>
    json(route, { eligible: true, hasActiveSubscription: true, grantedSmallDaemonSecondsRemaining: '0', planName: 'free' }),
  );
  await page.route('**/controlplane.v1.BillingService/GetCurrentUserWalletOverview', (route) =>
    json(route, { overview: { wallet: { balanceUsdNanos: '1000000000' } } }),
  );
  // One connected machine, so a chat opens on its transcript rather than on a machine wait.
  await page.route('**/reliant.v1.DaemonRegistryService/ListDaemons', (route) =>
    json(route, { daemons: [{ daemonId: 'daemon_1', hostname: 'cloud-machine', status: 'DAEMON_STATUS_ACTIVE' }] }),
  );
  await page.route('**/reliant.v1.ProjectService/ListProjects', (route) => json(route, { projects: [PROJECT] }));
  await page.route('**/reliant.v1.ProjectService/GetProject', (route) => json(route, { project: PROJECT }));
  await page.route('**/reliant.v1.ChatService/ListChats', (route) =>
    json(route, { chats: CHATS, totalCount: CHATS.length, lastSequence: '1' }),
  );
  await page.route('**/reliant.v1.ChatService/GetChat', (route) => {
    const { chatId } = route.request().postDataJSON() as { chatId?: string };
    return json(route, { chat: CHATS.find((c) => c.id === chatId) ?? CHATS[0] });
  });
  await page.route('**/reliant.v1.ChatService/ListMessages', (route) => {
    const { chatId } = route.request().postDataJSON() as { chatId?: string };
    const messages = chatId === DEPLOY_CHAT.id ? DEPLOY_MESSAGES.map((m) => ({ ...m, chatId, thread: chatId })) : [];
    return json(route, { messages, total: messages.length, count: messages.length, hasMore: false, oldestSeq: '1' });
  });

  const orgs = options.orgs ?? [{ id: 'org_acme', name: 'Acme', slug: 'acme', role: 'owner', isOwner: true, isDefault: true }];
  await page.route('**/controlplane.v1.OrganizationService/ListMyOrganizations', (route) =>
    json(route, { organizations: orgs, defaultOrgId: orgs.find((o) => o.isDefault)?.id ?? '' }),
  );
  await page.route('**/controlplane.v1.OrganizationService/SetDefaultOrganization', (route) =>
    json(route, { defaultOrgId: (route.request().postDataJSON() as { orgId?: string }).orgId ?? '' }),
  );
  // Observe requests independently of which route fulfils them.
  page.on('request', (request) => {
    if (!request.url().includes('/controlplane.v1.DeployService/')) return;
    const body = (request.postDataJSON() ?? {}) as { orgId?: string };
    options.onOrgRequest?.(new URL(request.url()).pathname.split('/').pop() ?? '', body.orgId);
  });

  await page.route('**/controlplane.v1.DeployService/ListEnvironments', (route) => {
    const { project, orgId } = (route.request().postDataJSON() ?? {}) as { project?: string; orgId?: string };
    const pool = options.environmentsByOrg ? (options.environmentsByOrg[orgId ?? orgs.find((o) => o.isDefault)?.id ?? ''] ?? []) : environments;
    const envs = project === undefined ? pool : pool.filter((env) => env.project === project);
    return json(route, { environments: envs });
  });
  await page.route('**/controlplane.v1.DeployService/GetStatus', (route) => {
    const { environmentId = '' } = route.request().postDataJSON() as { environmentId?: string };
    return json(route, STATUSES[environmentId] ?? {});
  });
  await page.route('**/controlplane.v1.DeployService/GetLiveView', (route) => {
    const { project } = route.request().postDataJSON() as { project?: string };
    const live = environments
      .filter((env) => env.project === project)
      .map((env) => {
        const status = STATUSES[env.id] as { currentPromotion?: { releaseVersion: string } } | undefined;
        return {
          environment: { ...env, currentPromotionId: `promo_${env.id}` },
          currentPromotion: status?.currentPromotion,
          currentRelease: status?.currentPromotion ? { id: 'rel', version: status.currentPromotion.releaseVersion } : undefined,
        };
      });
    return json(route, { environments: live });
  });

  // The control plane is configured the way a deployment configures it: by
  // the runtime config script (src/lib/config_gen.ts). Served here rather than
  // set in an init script, so the built app's own config.js cannot replace it.
  // A control plane implies Supabase auth is configured too; the API-key
  // session is what actually signs the user in.
  const config = { CONTROL_PLANE_API_URL: origin, SUPABASE_URL: `${origin}/supabase`, SUPABASE_ANON_KEY: 'anon-test-key' };
  await page.route('**/config.js', (route) =>
    route.fulfill({ status: 200, contentType: 'text/javascript', body: `window.__FORGE_CONFIG__ = ${JSON.stringify(config)};` }),
  );
  await page.addInitScript((appOrigin) => {
    if (location.origin === appOrigin) localStorage.setItem('reliant-api-key', 'test-key-123');
  }, origin);
}
