/**
 * Apps console fixtures: two forge apps, ported from the November review's
 * fixtures-apps-observability/10-apps.js.
 *
 *   shopfront   prod, staging and a preview env; live: a static site, an
 *               api and a managed postgres
 *   ledger      prod only, degraded (its api is crash-looping)
 */
import type { Fixtures } from '../rpc'
import { DAY, FIXED_NOW, HOUR, MIN, USER, iso } from './base'

const ENVIRONMENTS = [
  { id: 'env-shop-prod', name: 'prod', project: 'shopfront', lifetime: 'DEPLOY_ENVIRONMENT_LIFETIME_PERSISTENT', secretPosture: 'DEPLOY_SECRET_POSTURE_WRITE_ONLY', hostedSlot: true, createdAt: iso(30 * DAY) },
  { id: 'env-shop-staging', name: 'staging', project: 'shopfront', lifetime: 'DEPLOY_ENVIRONMENT_LIFETIME_PERSISTENT', secretPosture: 'DEPLOY_SECRET_POSTURE_WRITE_ONLY', hostedSlot: true, createdAt: iso(30 * DAY) },
  { id: 'env-shop-pr12', name: 'pr-12', project: 'shopfront', lifetime: 'DEPLOY_ENVIRONMENT_LIFETIME_PREVIEW', secretPosture: 'DEPLOY_SECRET_POSTURE_WRITE_ONLY', hostedSlot: true, createdAt: iso(5 * HOUR) },
  { id: 'env-ledger-prod', name: 'prod', project: 'ledger', lifetime: 'DEPLOY_ENVIRONMENT_LIFETIME_PERSISTENT', secretPosture: 'DEPLOY_SECRET_POSTURE_WRITE_ONLY', hostedSlot: true, createdAt: iso(9 * DAY) },
]

const deployment = (name: string, tier: string, verdict: string, url = '', extra: Record<string, unknown> = {}) => ({
  deployment: {
    id: `dep-${name}`, name, tier, runState: 'DEPLOY_RUN_STATE_RUNNING',
    observed: {
      url, imageDigest: 'sha256:9f2c1e0a7b33', lastError: '',
      state: verdict === 'DEPLOY_VERDICT_DEGRADED' ? 'DEPLOY_OBSERVED_STATE_DEGRADED' : 'DEPLOY_OBSERVED_STATE_READY',
    },
  },
  verdict, observedAt: iso(2 * MIN), desiredDigest: 'sha256:9f2c1e0a7b33', ...extra,
})

const promotion = (version: string, ago: number) => ({
  id: `pr-${version}`, releaseVersion: version, kind: 'DEPLOY_PROMOTION_KIND_PROMOTE', promotedByActor: USER.email, createdAt: iso(ago),
})

const CONVERGED = 'DEPLOY_VERDICT_CONVERGED'
const STATUSES: Record<string, Record<string, unknown>> = {
  'env-shop-prod': {
    environmentVerdict: CONVERGED, currentPromotion: promotion('20261118.121200-a1b2c3d4e5f6', 3 * HOUR),
    deployments: [
      deployment('web', 'DEPLOY_TIER_STATIC', CONVERGED, 'https://shopfront-prod.reliant.app'),
      deployment('api', 'DEPLOY_TIER_BACKEND', CONVERGED, 'https://api-shopfront-prod.reliant.app'),
      deployment('db', 'DEPLOY_TIER_DATABASE', CONVERGED),
    ],
  },
  'env-shop-staging': {
    environmentVerdict: CONVERGED, currentPromotion: promotion('20261118.144000-f6e5d4c3b2a1', 50 * MIN),
    deployments: [
      deployment('web', 'DEPLOY_TIER_STATIC', CONVERGED, 'https://shopfront-staging.reliant.app'),
      deployment('api', 'DEPLOY_TIER_BACKEND', CONVERGED),
    ],
  },
  'env-shop-pr12': {
    environmentVerdict: CONVERGED,
    deployments: [deployment('web', 'DEPLOY_TIER_STATIC', CONVERGED, 'https://shopfront-pr-12.reliant.app')],
  },
  'env-ledger-prod': {
    environmentVerdict: 'DEPLOY_VERDICT_DEGRADED', currentPromotion: promotion('20261117.090000-0badc0ffee00', 30 * HOUR),
    deployments: [deployment('api', 'DEPLOY_TIER_BACKEND', 'DEPLOY_VERDICT_DEGRADED', '', { verdictReason: 'CrashLoopBackOff: 6 restarts in 10m' })],
  },
}

// Observability for shopfront prod: an api and a worker, with 24 hourly
// points of request and error trend (a deterministic wave, not noise).
const SERVICES = [
  { service: 'api', rps: 42.5, errors: 0.004, kind: 'WORKLOAD_KIND_DEPLOYMENT' },
  { service: 'worker', rps: 3.2, errors: 0.021, kind: 'WORKLOAD_KIND_DEPLOYMENT' },
]
const trend = (base: number, phase: number) =>
  Array.from({ length: 24 }, (_, i) => ({
    time: iso((23 - i) * HOUR),
    value: Math.round(base * 3600 * (1 + 0.35 * Math.sin((i + phase) / 3.2)) * 100) / 100,
    partial: i === 23,
  }))

const WORKLOADS = SERVICES.map((s) => ({
  envId: 'env-shop-prod', envName: 'prod', workload: s.service, namespace: 'shopfront-prod',
  serviceVersion: '20261118.121200-a1b2c3d4e5f6', replicasObserved: 2, restartsInWindow: '0',
  containers: [{ container: s.service, imageName: `registry.reliant.app/shopfront/${s.service}`, imageTag: '20261118.121200', restartsInWindow: '0' }],
  kind: s.kind, health: 'WORKLOAD_HEALTH_HEALTHY', healthReason: '', readyReplicas: 2, desiredReplicas: 2,
  releaseVersion: '20261118.121200-a1b2c3d4e5f6', releaseBasis: 'WORKLOAD_RELEASE_BASIS_PROMOTION', pods: [],
}))

const SERVICE_OVERVIEW = SERVICES.map((s, i) => {
  const requests = Math.round(s.rps * 86400)
  return {
    service: s.service, requestCount: String(requests), ratePerSecond: s.rps,
    errorCount: String(Math.round(requests * s.errors)), errorRate: s.errors, p50Ms: 38 + i * 12, p95Ms: 180 + i * 140, p99Ms: 640 + i * 380, errorLogCount: String(Math.round(requests * s.errors * 0.6)),
    requestTrend: trend(s.rps, i * 5), errorTrend: trend(s.rps * s.errors, i * 5 + 2), source: 'otel', operatorData: false, group: {},
  }
})

const issue = (id: string, o: { title: string; culprit: string; type: string; service: string; status: string; events: number; affected: number; first: number; last: number; platform?: string }) => ({
  id, platform: o.platform ?? 'ISSUE_PLATFORM_SERVER', envId: 'env-shop-prod', envName: 'prod', service: o.service,
  fingerprint: `fp-${id}`, fpVersion: 1, title: o.title, culprit: o.culprit, exceptionType: o.type, status: o.status,
  firstSeenAt: iso(o.first), firstVersion: '20261117.090000-0badc0ffee00', lastSeenAt: iso(o.last),
  lastVersion: '20261118.121200-a1b2c3d4e5f6', events: String(o.events), affected: String(o.affected),
  trend: Array.from({ length: 24 }, (_, i) => ({ hour: iso((23 - i) * HOUR), events: String(Math.round((o.events / 24) * (1 + Math.sin(i / 2.5)))) })),
  symbolicated: true, version: 1, source: 'otel', affectedUnit: 'users', relatedCount: 0,
})

const ISSUES = [
  issue('iss-1', { title: 'TypeError: Cannot read properties of undefined (reading \'quantity\')', culprit: 'src/cart/totals.ts in lineTotal',
    type: 'TypeError', service: 'api', status: 'ISSUE_STATUS_NEW', events: 182, affected: 37, first: 2 * HOUR, last: 4 * MIN }),
  issue('iss-2', { title: 'StripeError: idempotency key reused with different parameters', culprit: 'src/payments/webhook.ts in handleEvent',
    type: 'StripeError', service: 'worker', status: 'ISSUE_STATUS_REGRESSED', events: 41, affected: 9, first: 6 * DAY, last: 25 * MIN }),
  issue('iss-3', { title: 'ChunkLoadError: Loading chunk 412 failed', culprit: '/checkout', type: 'ChunkLoadError', service: 'web',
    status: 'ISSUE_STATUS_UNRESOLVED', events: 12, affected: 11, first: 3 * DAY, last: 3 * HOUR, platform: 'ISSUE_PLATFORM_BROWSER' }),
]

// The Overview headline and the Observability home read the same RED panel:
// requests, errors and p95 as numbers, and as 24 hourly points. The previous
// period is the same panel asked for a window that ends earlier, and reads a
// little lower.
const RED_NOW = {
  requests: 3_950_000, errors: 20_100, p95: 180,
}
const RED_BEFORE = { requests: 3_610_000, errors: 17_300, p95: 196 }
function queryPanel(req: Record<string, unknown>) {
  const panel = (req.panel ?? {}) as { id?: string; kind?: string }
  if (!String(panel.id ?? '').startsWith('observability-home-')) return {}
  const range = ((req.scope ?? {}) as { timeRange?: { start?: string; end?: string } }).timeRange
  const end = range?.end ? Date.parse(range.end) : FIXED_NOW
  const totals = end < FIXED_NOW - 5 * MIN ? RED_BEFORE : RED_NOW
  const at = (ago: number) => new Date(FIXED_NOW - ago).toISOString()
  const wave = (i: number, phase: number) => 1 + 0.3 * Math.sin((i + phase) / 3.2)
  const line = (label: string, per: number, phase: number) => ({
    label, group: {}, value: 0,
    points: Array.from({ length: 24 }, (_, i) => ({ time: at((23 - i) * HOUR), value: Math.round(per * wave(i, phase) * 1000) / 1000, partial: i === 23 })),
  })
  if (panel.kind === 'number') {
    return {
      panelId: panel.id, kind: 'number', unit: '', stepSeconds: 0,
      series: [
        { label: 'requests', group: {}, points: [], value: totals.requests },
        { label: 'errors', group: {}, points: [], value: totals.errors },
        { label: 'p95', group: {}, points: [], value: totals.p95 },
      ],
    }
  }
  return {
    panelId: panel.id, kind: 'line', unit: '', stepSeconds: 3600,
    series: [
      line('requests', totals.requests / 24, 0),
      line('errors', totals.errors / 24, 2),
      line('p95', totals.p95, 4),
    ],
  }
}

/** The Overview's Vulnerabilities card: shopfront's prod and staging, scanned 2h ago against a current database. */
const vulnEnv = (id: string, name: string, version: string, counts: Record<string, number>, kev: number, fixable: number) => ({
  environmentId: id, environmentName: name, releaseId: `rel-${version}`, releaseVersion: version,
  counts: { critical: 0, high: 0, medium: 0, low: 0, negligible: 0, unknown: 0, ...counts },
  kevCount: kev, fixableCount: fixable, blockingCount: kev, exemptCount: 0,
  coverage: { artifacts: 3, withSbom: 3, scanned: 3 },
  lastScannedAt: iso(2 * HOUR), scanState: 'VULNERABILITY_SCAN_STATE_SCANNED',
})
const VULN_SUMMARY = {
  environments: [
    vulnEnv('env-shop-prod', 'prod', '20261118.121200-a1b2c3d4e5f6', { critical: 1, high: 2, medium: 3, low: 1 }, 1, 4),
    vulnEnv('env-shop-staging', 'staging', '20261118.144000-f6e5d4c3b2a1', { high: 1, medium: 2 }, 0, 2),
  ],
  database: { scanner: 'grype', scannerVersion: 'v0.122.1', schema: 'v6.1.10', builtAt: iso(12 * HOUR), stale: false },
}

export const consoleFixtures: Fixtures = {
  'controlplane.v1.ObservabilityService/QueryPanel': queryPanel,
  'controlplane.v1.VulnerabilityService/GetVulnerabilitySummary': (req) => (req.project === 'shopfront' ? VULN_SUMMARY : { environments: [] }),
  'controlplane.v1.IssuesService/ListIssues': (req) => {
    const statuses = (req.statuses as string[] | undefined) ?? []
    return { issues: statuses.length ? ISSUES.filter((i) => statuses.includes(i.status)) : ISSUES, nextPageToken: '' }
  },
  'controlplane.v1.ObservabilityService/ListWorkloads': { workloads: WORKLOADS, truncated: false, jobs: [], jobsTruncated: false },
  'controlplane.v1.ObservabilityService/GetServiceOverview': { services: SERVICE_OVERVIEW, trendStepSeconds: 3600, unavailableSections: [] },
  'controlplane.v1.DeployService/ListEnvironments': (req) => ({
    environments: req.project ? ENVIRONMENTS.filter((e) => e.project === req.project) : ENVIRONMENTS,
  }),
  'controlplane.v1.DeployService/GetStatus': (req) => STATUSES[String(req.environmentId)] ?? {},
  'controlplane.v1.DeployService/GetLiveView': (req) => ({
    environments: ENVIRONMENTS.filter((env) => !req.project || env.project === req.project).map((env) => {
      const promo = STATUSES[env.id]?.currentPromotion as { releaseVersion: string } | undefined
      return {
        environment: { ...env, currentPromotionId: promo ? `pr-${promo.releaseVersion}` : '' },
        currentPromotion: promo,
        currentRelease: promo ? { id: `rel-${promo.releaseVersion}`, version: promo.releaseVersion } : undefined,
      }
    }),
  }),
}
