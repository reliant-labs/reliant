/**
 * Shared fixtures: the signed-in "Alex Demo" with two projects (`shopfront`,
 * a fictional TypeScript storefront API, and `docs-site`), a cloud machine
 * and a suspended laptop, an active compute subscription, and one org.
 * Fabricated and neutral; nothing here is real data. Ported from the
 * November review harness (fixtures/00-base.js, 05-catalog.js, 06-org.js).
 *
 * Every timestamp is relative to FIXED_NOW, the clock the page is frozen at,
 * so "5m ago" reads the same on every run.
 */
import type { Fixtures } from '../rpc'

/** 2026-11-18 15:30 UTC, a Wednesday. */
export const FIXED_NOW = Date.UTC(2026, 10, 18, 15, 30, 0)
export const MIN = 60_000
export const HOUR = 60 * MIN
export const DAY = 24 * HOUR
export const iso = (ago = 0) => new Date(FIXED_NOW - ago).toISOString()
export const isoAhead = (ahead: number) => new Date(FIXED_NOW + ahead).toISOString()

export const USER = { id: '00000000-0000-4000-8000-00000000a1ec', email: 'alex@example.com', name: 'Alex Demo' }

export const IDS = {
  project: '7b1f3c2a-1d4e-4c8a-9f10-5a6b7c8d9e01',
  docsProject: '9c2e1d00-2222-4bbb-9ccc-000000000002',
  localDaemon: 'd-local-8f2c41',
  cloudDaemon: 'd-cloud-3a91e7',
  org: 'org-demo',
}

export const PROJECT = {
  id: IDS.project, userId: USER.id, name: 'shopfront', path: '/home/alex/code/shopfront',
  description: 'Storefront API and admin', isGitRepo: true, defaultBranch: 'main',
  createdAt: iso(20 * DAY), updatedAt: iso(2 * HOUR), lastActive: iso(5 * MIN),
  remoteUrl: 'https://github.com/acme/shopfront.git', isForge: true, forgeProjectName: 'shopfront',
}

export const DOCS_PROJECT = {
  id: IDS.docsProject, userId: USER.id, name: 'docs-site', path: '/home/alex/code/docs-site', description: 'Docs',
  isGitRepo: true, defaultBranch: 'main', createdAt: iso(9 * DAY), updatedAt: iso(DAY),
  lastActive: iso(3 * HOUR), remoteUrl: 'https://github.com/acme/docs-site.git', isForge: false,
}

const DAEMONS = [
  { daemonId: IDS.cloudDaemon, userId: USER.id, hostname: 'cloud-machine', platform: 'linux/amd64',
    status: 'DAEMON_STATUS_ACTIVE', projects: [], connectedAt: iso(3 * HOUR), lastHeartbeat: iso(5000),
    daemonType: 'cloud', lifecyclePhase: 'DAEMON_LIFECYCLE_PHASE_READY', size: 'm', createdAt: iso(12 * DAY) },
  { daemonId: IDS.localDaemon, userId: USER.id, hostname: 'alex-macbook', platform: 'darwin/arm64',
    status: 'DAEMON_STATUS_SUSPENDED', projects: [], connectedAt: iso(9 * HOUR), lastHeartbeat: iso(40 * MIN),
    daemonType: 'self_hosted', lifecyclePhase: 'DAEMON_LIFECYCLE_PHASE_SUSPENDED', size: '', createdAt: iso(20 * DAY),
    lastStatusMessage: 'Offline since 40 minutes ago' },
]

export const WORKTREE = (id: string, name: string, branch: string, isMain: boolean, projectId = IDS.project) => ({
  id, name, branch, baseBranch: 'main', projectId, isMain, status: 'WORKTREE_STATUS_ACTIVE',
  path: isMain ? `/home/alex/code/${name}` : `/home/alex/.reliant/worktrees/${id}`,
  createdAt: iso(2 * DAY), updatedAt: iso(2 * HOUR), lastActive: iso(2 * HOUR),
})

export const WORKTREES = {
  main: WORKTREE('wt-main-0001', 'shopfront', 'main', true),
  rate: WORKTREE('wt-rate-limit-0002', 'rate-limit-checkout', 'feat/Checkout-RateLimit-per-IP-and-per-API-key', false),
  pay: WORKTREE('wt-pay-0003', 'stripe-webhooks', 'fix/stripe-webhook-retries', false),
  docsMain: WORKTREE('wt-docs-main', 'docs-site', 'main', true, IDS.docsProject),
}

// The plan catalog of control-plane's pricing.yaml (machine-pooled-v1):
// name, monthly cents, overage cents/hr, disk GiB.
const SIZES: Array<[string, number, string, number]> = [
  ['s1', 2500, '20', 30], ['s2', 3000, '25', 30], ['m', 3900, '30', 50], ['l', 6900, '55', 80],
  ['xl', 14900, '115', 120], ['2xl', 24900, '195', 200], ['3xl', 48900, '380', 300],
]
const PLANS = SIZES.map(([size, cents, , disk], i) => ({
  id: `plan_compute_${size}`, productId: 'prod_compute', name: `Compute ${size.toUpperCase()}`, tier: 'PLAN_TIER_STARTER',
  stripePriceId: `price_visual_${size}`, createdAt: iso(30 * DAY), priceCents: String(cents), displayOrder: i + 1,
  structuredLimits: {
    maxSeats: 1, maxWorkspaces: 5, maxLlmKeys: 1, maxLlmSpendMonthly: 25,
    allowedDaemonSizes: SIZES.slice(0, i + 1).map(([s]) => s), machineSize: size, machineIncludedHours: '160',
    machineIncludedStorageGib: String(disk),
  },
}))
const M_PLAN = PLANS.find((p) => p.id === 'plan_compute_m')!

export const PERIOD = { start: iso(12 * DAY), end: isoAhead(18 * DAY) }

const SUBSCRIPTION = {
  id: 'sub-visual-0001', orgId: IDS.org, planId: M_PLAN.id, plan: M_PLAN, stripeSubscriptionId: 'sub_visual',
  status: 'SUBSCRIPTION_STATUS_ACTIVE', currentPeriodStart: PERIOD.start, currentPeriodEnd: PERIOD.end,
  createdAt: PERIOD.start, updatedAt: iso(DAY), overageEnabled: false,
}

export const ORG = {
  id: IDS.org, name: "Alex's org", slug: 'alex', role: 'owner', isOwner: true, isDefault: true,
  createdAt: iso(20 * DAY), joinedAt: iso(20 * DAY),
}

const setting = (key: string, value: string) => ({ id: key, key, value, valueType: 'string', createdAt: iso(DAY), updatedAt: iso(DAY) })

export const baseFixtures: Fixtures = {
  'controlplane.v1.UserService/GetCurrentUser': {
    user: { id: USER.id, externalId: USER.id, email: USER.email, name: USER.name, avatarUrl: '', role: 'user',
      createdAt: iso(20 * DAY), updatedAt: iso(DAY), onboardingCompleted: true },
  },
  'reliant.v1.SettingsService/ListSettings': { settings: [setting('onboarding.wizard.dismissed', 'true')], total: 1 },
  'reliant.v1.SettingsService/GetPrivacySettings': { analyticsEnabled: false, crashReportingEnabled: false },
  'reliant.v1.SettingsService/GetProviderStatuses': {
    providers: [
      { provider: 'anthropic', configured: true, displayName: 'Anthropic', outputModalities: ['text'], accountCount: 1 },
      { provider: 'openai', configured: true, displayName: 'OpenAI', outputModalities: ['text'], accountCount: 1 },
    ],
  },
  'reliant.v1.ProjectService/ListProjects': { projects: [PROJECT, DOCS_PROJECT], total: 2 },
  'reliant.v1.ProjectService/GetProject': (req) => ({ project: req.id === IDS.docsProject || req.projectId === IDS.docsProject ? DOCS_PROJECT : PROJECT }),
  'reliant.v1.DaemonRegistryService/ListDaemons': { daemons: DAEMONS },
  'reliant.v1.WorktreeService/ListWorktrees': { worktrees: Object.values(WORKTREES), total: Object.keys(WORKTREES).length },
  'reliant.v1.WorktreeService/GetWorktree': (req) => {
    const id = req.id ?? req.worktreeId
    const worktree = Object.values(WORKTREES).find((w) => w.id === id)
    return worktree ? { worktree } : { __error: { code: 'not_found', message: 'worktree not found' } }
  },
  'controlplane.v1.OrganizationService/ListMyOrganizations': { organizations: [ORG], defaultOrgId: ORG.id },
  'controlplane.v1.BillingService/ListPlans': { plans: PLANS },
  'controlplane.v1.BillingService/GetCurrentUserComputeEligibility': {
    eligible: true, reason: 'COMPUTE_INELIGIBLE_REASON_UNSPECIFIED', hasActiveSubscription: true,
    grantedSmallDaemonSecondsRemaining: '0', planName: 'Compute M', allowedDaemonSizes: ['s1', 's2', 'm'],
  },
  'controlplane.v1.BillingService/GetCurrentUserSubscription': { subscription: SUBSCRIPTION },
  'controlplane.v1.BillingService/GetCurrentUserComputeSubscription': { subscription: SUBSCRIPTION },
  'controlplane.v1.BillingService/GetCurrentUserReliantOverview': {
    overview: { currentPeriodSpendCents: '1840', remainingSpendCents: '8160', spendCapReached: false, spendCapUnlimited: false,
      requestRateUnlimited: true, currentPeriodRequestCount: '412' },
  },
  'controlplane.v1.BillingService/GetCurrentUserWalletOverview': {
    overview: { ledgerEntries: [], topups: [], ledgerPage: 1, ledgerPageSize: 20, totalLedgerEntries: 0 },
  },
  'controlplane.v1.BillingService/GetCurrentUserBillingEmail': { email: USER.email },
}
