/**
 * shopfront, live (U6b round 3): what the project shell's Services, a
 * service's page, Usage and Product analytics › Events & metrics read.
 *
 *   prod      api ×3 (one restarting: CrashLoopBackOff, 7 restarts), worker ×1,
 *             a nightly-digest CronJob and a migrate Job, the static site
 *             (web) and Postgres (db) from GetStatus (console.ts)
 *   staging   api ×1
 *
 * CPU and memory are per container (api has an envoy sidecar); GetWorkload
 * carries 24 hourly points of each. QueryPanel answers the custom-metric
 * charts and the product-events catalog; ListMetrics a few custom metrics
 * beside forge's built-ins; GetProjectCost a month to date of ~$38 with a
 * ~$61 forecast over three environments.
 *
 * Fabricated; every time is relative to FIXED_NOW.
 */
import type { Fixtures } from '../rpc'
import { DAY, FIXED_NOW, HOUR, MIN, USER, iso } from './base'
import { queryPanel } from './console'
import { fixtureReleaseCommit } from '../../../src/components/Forge/Tests/fixtureReleases'

const PROD = { envId: 'env-shop-prod', envName: 'prod', namespace: 'shopfront-prod' }
const STAGING = { envId: 'env-shop-staging', envName: 'staging', namespace: 'shopfront-staging' }
export const PROD_RELEASE = '20261118.121200-a1b2c3d4e5f6'
export const STAGING_RELEASE = '20261118.144000-f6e5d4c3b2a1'
const GIB = 1024 ** 3
const MIB = 1024 ** 2

const usage = (latest: number, p95: number, limit: number) => ({
  latest, p95, limit, request: limit / 2, limitUtilizationLatest: latest / limit, limitUtilizationP95: p95 / limit,
  requestUtilizationLatest: latest / (limit / 2), requestUtilizationP95: p95 / (limit / 2),
})
const container = (name: string, image: string, cpu: [number, number, number], mem: [number, number, number], restarts = 0, terminated = '') => ({
  container: name, imageName: image, imageTag: PROD_RELEASE.slice(0, 15), restartsInWindow: String(restarts),
  lastTerminatedReason: terminated, lastTerminatedAt: terminated ? iso(9 * MIN) : undefined,
  cpu: usage(...cpu), memory: usage(...mem),
})

const backoff = {
  time: iso(2 * MIN), envId: PROD.envId, type: 'Warning', reason: 'BackOff', message: 'Back-off restarting failed container api in pod api-7c9d8f6b5-k2x9q',
  count: '7', objectKind: 'Pod', objectName: 'api-7c9d8f6b5-k2x9q', container: 'api', plainReason: 'Crashing on start, restarting',
}

const pod = (name: string, o: { restarts?: number; exit?: string; age: number; lowerBound?: boolean; node: string; warning?: unknown }) => ({
  name, uid: `uid-${name}`, state: 'WORKLOAD_POD_STATE_RUNNING', restarts: String(o.restarts ?? 0), lastExitReason: o.exit ?? '',
  lastExitAt: o.exit ? iso(9 * MIN) : undefined, node: o.node, firstSeen: iso(o.age), lastSeen: iso(MIN), ageIsLowerBound: !!o.lowerBound,
  latestWarning: o.warning,
})

const workload = (env: typeof PROD, name: string, o: Record<string, unknown>) => ({
  ...env, workload: name, serviceVersion: env === PROD ? PROD_RELEASE : STAGING_RELEASE, replicasObserved: 1, restartsInWindow: '0',
  kind: 'WORKLOAD_KIND_DEPLOYMENT', health: 'WORKLOAD_HEALTH_HEALTHY', healthReason: '', healthCause: 'WORKLOAD_CAUSE_NONE',
  readyReplicas: 1, desiredReplicas: 1, releaseVersion: env === PROD ? PROD_RELEASE : STAGING_RELEASE, releaseBasis: 'WORKLOAD_RELEASE_BASIS_DIGEST',
  pods: [], ...o,
})

const API_PODS = [
  pod('api-7c9d8f6b5-4hfnm', { age: 3 * DAY, lowerBound: true, node: 'gke-prod-pool-1-a1b2' }),
  pod('api-7c9d8f6b5-p8wzt', { age: 3 * HOUR, node: 'gke-prod-pool-1-c3d4' }),
  pod('api-7c9d8f6b5-k2x9q', { restarts: 7, exit: 'Error', age: 52 * MIN, node: 'gke-prod-pool-1-e5f6', warning: backoff }),
]

const WORKLOADS = [
  workload(PROD, 'api', {
    replicasObserved: 3, restartsInWindow: '7', health: 'WORKLOAD_HEALTH_DEGRADED', healthCause: 'WORKLOAD_CAUSE_WORKLOAD',
    healthReason: '2 of 3 instances ready. One is crashing on start and restarting (7 restarts)', readyReplicas: 2, desiredReplicas: 3,
    containers: [
      container('api', 'registry.reliant.app/shopfront/api', [0.42, 0.61, 1], [612 * MIB, 690 * MIB, GIB], 7, 'Error'),
      container('envoy', 'registry.reliant.app/shopfront/envoy', [0.03, 0.05, 0.25], [48 * MIB, 52 * MIB, 128 * MIB]),
    ],
    latestWarning: backoff,
  }),
  workload(PROD, 'worker', {
    containers: [container('worker', 'registry.reliant.app/shopfront/worker', [0.18, 0.3, 0.5], [256 * MIB, 301 * MIB, 512 * MIB])],
  }),
  workload(PROD, 'nightly-digest', {
    kind: 'WORKLOAD_KIND_CRON_JOB', readyReplicas: 0, desiredReplicas: null, replicasObserved: 0,
    containers: [container('nightly-digest', 'registry.reliant.app/shopfront/worker', [0.02, 0.4, 0.5], [40 * MIB, 210 * MIB, 512 * MIB])],
  }),
  workload(STAGING, 'api', {
    containers: [container('api', 'registry.reliant.app/shopfront/api', [0.08, 0.12, 0.5], [301 * MIB, 330 * MIB, 512 * MIB])],
  }),
]

const run = (name: string, workloadName: string, result: string, ago: number, seconds: number) => ({
  ...{ envId: PROD.envId, envName: PROD.envName }, name, workload: workloadName, result, reason: result === 'WORKLOAD_JOB_RESULT_FAILED' ? 'BackoffLimitExceeded' : '',
  releaseVersion: PROD_RELEASE, startedAt: iso(ago), finishedAt: iso(ago - seconds * 1000), durationSeconds: String(seconds),
})
const JOBS = [
  run('nightly-digest-29384756', 'nightly-digest', 'WORKLOAD_JOB_RESULT_SUCCEEDED', 13 * HOUR, 94),
  run('nightly-digest-29383316', 'nightly-digest', 'WORKLOAD_JOB_RESULT_SUCCEEDED', 37 * HOUR, 88),
  run('shopfront-migrate-a1b2c3d4', 'shopfront-migrate', 'WORKLOAD_JOB_RESULT_SUCCEEDED', 3 * HOUR, 12),
]

// 24 hourly points, a deterministic wave.
const points = (base: number, phase: number, amp = 0.25) =>
  Array.from({ length: 24 }, (_, i) => ({ time: iso((23 - i) * HOUR), value: Math.round(base * (1 + amp * Math.sin((i + phase) / 3)) * 1000) / 1000, partial: i === 23, null: false }))

function getWorkload(req: Record<string, unknown>) {
  const name = String(req.workload ?? '')
  const envIds = ((req.scope as { envIds?: string[] } | undefined)?.envIds ?? []) as string[]
  const rows = WORKLOADS.filter((w) => w.workload === name && (envIds.length === 0 || envIds.includes(w.envId)))
  const series = rows.flatMap((w) =>
    (w.containers as ReturnType<typeof container>[]).flatMap((c, i) => [
      { envId: w.envId, container: c.container, metric: 'WORKLOAD_METRIC_CPU_USAGE', unit: 'cores', points: points(c.cpu.latest, i * 3) },
      { envId: w.envId, container: c.container, metric: 'WORKLOAD_METRIC_MEMORY_WORKING_SET', unit: 'bytes', points: points(c.memory.latest, i * 3 + 2, 0.08) },
    ]),
  )
  return {
    series, events: name === 'api' ? [backoff] : [], imageChanges: [], versionChanges: [], stepSeconds: 3600, truncated: false,
    workloads: rows.map((w) => (w.workload === 'api' && w.envId === PROD.envId ? { ...w, pods: API_PODS } : { ...w, pods: [pod(`${w.workload}-5d6f7a8b9-x1y2z`, { age: 2 * DAY, lowerBound: true, node: 'gke-prod-pool-1-a1b2' })] })),
    healthChanges: [],
  }
}

// ── Custom metrics and product events ───────────────────────────────────────

const metric = (name: string, type: string, unit: string, description: string, services: string[], o: Record<string, unknown> = {}) => ({
  name, type, unit, description, monotonic: type === 'METRIC_TYPE_SUM', temporality: 'METRIC_TEMPORALITY_CUMULATIVE', services, serviceCount: services.length,
  lastSeen: iso(MIN), seriesEstimate: '12', queryable: true, builtin: false, declared: false, seen: true, stability: '', declaredAttributes: [], ...o,
})
const METRICS = [
  metric('shop.orders.placed', 'METRIC_TYPE_SUM', '{order}', 'Orders placed, by payment method', ['api'], { declared: true, stability: 'stable' }),
  metric('shop.cart.value', 'METRIC_TYPE_HISTOGRAM', 'USD', 'Cart value at checkout', ['api'], { declared: true, stability: 'stable' }),
  metric('shop.inventory.sync_lag', 'METRIC_TYPE_GAUGE', 's', 'How far the inventory mirror trails the supplier feed', ['worker']),
  metric('shop.email.queue_depth', 'METRIC_TYPE_GAUGE', '{message}', 'Emails waiting to send', ['worker']),
  metric('rpc.server.duration', 'METRIC_TYPE_HISTOGRAM', 'ms', 'forge built-in', ['api', 'worker'], { builtin: true }),
  metric('k8s.container.cpu.usage', 'METRIC_TYPE_GAUGE', 's', 'infrastructure', ['api', 'worker'], { builtin: true }),
]

const EVENTS = [
  ['product.checkout_completed', 1_284, 1_102, 6 * MIN],
  ['product.cart_item_added', 9_420, 3_018, MIN],
  ['product.signup', 412, 412, 25 * MIN],
  ['product.search', 15_906, 4_221, MIN],
  ['product.coupon_applied', 233, 219, 2 * HOUR],
  ['page_view', 88_140, 9_310, MIN],
] as const

/** QueryPanel beyond console.ts's Overview panel: a custom metric's line, and the product-events catalog. */
export function servicesQueryPanel(req: Record<string, unknown>): Record<string, unknown> | undefined {
  const panel = (req.panel ?? {}) as { id?: string; kind?: string }
  const id = String(panel.id ?? '')
  if (id === 'product-events-catalog') {
    return {
      panelId: id, kind: 'sql', unit: '', stepSeconds: 0, series: [], columns: ['events', 'users', 'last_seen'],
      rows: EVENTS.map(([event, events, users, ago]) => ({ group: { event }, values: [events, users, Math.round((FIXED_NOW - ago) / 1000)], missingValueIndexes: [] })),
    }
  }
  if (id.startsWith('custom-metric-')) {
    const name = id.slice('custom-metric-'.length)
    const base = name === 'shop.orders.placed' ? 54 : name === 'shop.cart.value' ? 71.5 : name === 'shop.inventory.sync_lag' ? 42 : 18
    return {
      panelId: id, kind: 'line', unit: '', stepSeconds: 3600,
      series: [{ label: 'A', group: {}, value: 0, points: points(base, name.length % 5, 0.3) }],
    }
  }
  return undefined
}

// ── Project cost ────────────────────────────────────────────────────────────

const NANOS = 1_000_000_000
const usd = (dollars: number) => String(Math.round(dollars * NANOS))
const amount = (dollars: number, basis = 'estimated') => ({ usdNanos: usd(dollars), basis, lowerBound: false, unpricedDimensions: [], allocated: false })

const MONTH_START = Date.UTC(2026, 10, 1)
const DAYS_IN = Math.floor((FIXED_NOW - MONTH_START) / DAY) + 1 // 18
// Per day at list price, by environment and category: prod carries most of it, the preview a little.
const DAILY_SHAPE: [string, string, number][] = [
  ['env-shop-prod', 'compute', 1.24],
  ['env-shop-prod', 'databases', 0.42],
  ['env-shop-prod', 'storage', 0.06],
  ['env-shop-prod', 'bandwidth', 0.11],
  ['env-shop-staging', 'compute', 0.31],
  ['env-shop-staging', 'storage', 0.02],
  ['env-shop-pr12', 'static_sites', 0.03],
]
function projectCost() {
  const daily = Array.from({ length: DAYS_IN }, (_, d) =>
    DAILY_SHAPE.filter(([env]) => env !== 'env-shop-pr12' || d >= DAYS_IN - 1).map(([environmentId, category, perDay]) => {
      const wave = 1 + 0.12 * Math.sin(d / 2.2) + (d > 12 && category === 'compute' ? 0.15 : 0)
      const partial = d === DAYS_IN - 1
      return {
        day: new Date(MONTH_START + d * DAY).toISOString(), category, environmentId,
        list: amount(perDay * wave * (partial ? 0.65 : 1), 'list'), quantities: [], partial,
      }
    }),
  ).flat()
  const listTotal = daily.reduce((acc, d) => acc + Number(d.list.usdNanos) / NANOS, 0)
  const driver = (deploymentName: string, environmentId: string, environmentName: string, tier: string, category: string, dollars: number, requested?: Record<string, number>) => ({
    deploymentId: `dep-${environmentName}-${deploymentName}`, deploymentName, environmentId, environmentName, tier, category,
    list: amount(dollars, 'list'), previousUsdNanos: usd(dollars * 0.86), share: dollars / listTotal, quantities: [],
    requested: requested ? { replicas: 1, cpuMilli: 0, memoryMib: 0, storageGib: 0, ...requested } : undefined, deleted: false,
  })
  return {
    currency: 'USD', generatedAt: iso(5 * MIN), periodStart: new Date(MONTH_START).toISOString(), periodEnd: new Date(Date.UTC(2026, 11, 1)).toISOString(),
    measuredThrough: iso(40 * MIN), monthToDate: amount(38.12), monthToDateList: amount(listTotal, 'list'), samePointLastPeriod: amount(33.4),
    forecast: { amount: amount(61.3), lowUsdNanos: usd(56.8), highUsdNanos: usd(66.9), method: 'run_rate_and_trailing_7d', measuredHours: 168, unavailableReason: '', list: amount(64.2, 'list') },
    lastPeriod: amount(54.75, 'invoiced'),
    daily,
    drivers: [
      driver('api', 'env-shop-prod', 'prod', 'backend', 'compute', 17.6, { replicas: 3, cpuMilli: 500, memoryMib: 1024 }),
      driver('db', 'env-shop-prod', 'prod', 'database', 'databases', 8.31, { cpuMilli: 250, memoryMib: 512, storageGib: 10 }),
      driver('worker', 'env-shop-prod', 'prod', 'backend', 'compute', 5.2, { cpuMilli: 250, memoryMib: 512 }),
      driver('api', 'env-shop-staging', 'staging', 'backend', 'compute', 5.52, { cpuMilli: 250, memoryMib: 512 }),
      driver('web', 'env-shop-prod', 'prod', 'static', 'bandwidth', 1.98),
      driver('web', 'env-shop-pr12', 'pr-12', 'static', 'static_sites', 0.02),
    ],
    allowances: [], caps: [],
  }
}

// ── Releases and promotions ─────────────────────────────────────────────────

// Each release's commit is the shared table's (Tests/fixtureReleases.ts), so its header and its Tests tab agree.
export const RELEASES = [
  { version: PROD_RELEASE, ago: 3 * HOUR + 10 * MIN },
  { version: STAGING_RELEASE, ago: 55 * MIN },
  { version: '20261117.090000-0badc0ffee00', ago: 30 * HOUR + 30 * MIN },
  { version: '20261116.160500-5eed5eed5eed', ago: 47 * HOUR },
].map((r) => ({ ...r, commit: fixtureReleaseCommit(r.version) }))

/** One release as DeployService answers it: ListReleases here, GetRelease in releases.ts. */
export const releaseMessage = (r: (typeof RELEASES)[number]) => ({
  id: `rel-${r.version}`, version: r.version, gitCommit: r.commit, createdAt: iso(r.ago), project: 'shopfront', createdByUserId: USER.id, artifacts: [],
})
const promotion = (environmentId: string, version: string, ago: number, from = '') => ({
  id: `pr-${environmentId}-${version}`, environmentId, releaseId: `rel-${version}`, releaseVersion: version, kind: 'DEPLOY_PROMOTION_KIND_PROMOTE',
  promotedByActor: USER.email, fromEnvironmentName: from, createdAt: iso(ago), targets: [], gates: [], resolvedArtifacts: {},
})
const PROMOTIONS: Record<string, unknown[]> = {
  'env-shop-prod': [
    promotion('env-shop-prod', PROD_RELEASE, 3 * HOUR, 'staging'),
    promotion('env-shop-prod', '20261117.090000-0badc0ffee00', 30 * HOUR, 'staging'),
    promotion('env-shop-prod', '20261116.160500-5eed5eed5eed', 46 * HOUR, 'staging'),
  ],
  'env-shop-staging': [promotion('env-shop-staging', STAGING_RELEASE, 50 * MIN), promotion('env-shop-staging', PROD_RELEASE, 3 * HOUR + 5 * MIN)],
}

export const servicesFixtures: Fixtures = {
  'controlplane.v1.ObservabilityService/ListWorkloads': { workloads: WORKLOADS, truncated: false, jobs: JOBS, jobsTruncated: false },
  'controlplane.v1.ObservabilityService/GetWorkload': getWorkload,
  'controlplane.v1.ObservabilityService/QueryPanel': (req) => servicesQueryPanel(req) ?? queryPanel(req),
  'controlplane.v1.ObservabilityService/ListMetrics': { metrics: METRICS, truncated: false },
  'controlplane.v1.BillingService/GetProjectCost': (req) => (req.projectId === 'shopfront' ? projectCost() : { __error: { code: 'not_found', message: 'no hosted project' } }),
  'controlplane.v1.DeployService/ListReleases': (req) =>
    req.project === 'shopfront'
      ? { releases: RELEASES.map(releaseMessage) }
      : { releases: [] },
  'controlplane.v1.DeployService/ListPromotions': (req) => ({ promotions: PROMOTIONS[String(req.environmentId)] ?? [] }),
}
