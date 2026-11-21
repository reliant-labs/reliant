/**
 * shopfront, U6c pages: what Storage and Usage & billing read.
 *
 *   Storage          prod declares two buckets (uploads, private; assets,
 *                    public read, on Reliant hosting), staging one; uploads
 *                    lists a few folders and files.
 *   Usage & billing  the org's daily usage by category over the requested
 *                    window (hosted apps, databases, static hosting,
 *                    observability at list price; managed AI as settled
 *                    charges; agent sandboxes with running time), every
 *                    source complete; three paid monthly invoices.
 *
 * Fabricated; every time is relative to FIXED_NOW.
 */
import type { Fixtures } from '../rpc'
import { DAY, FIXED_NOW, HOUR, IDS, iso } from './base'
import { queryPanel } from './console'
import { servicesFixtures, servicesQueryPanel } from './services'

const bucket = (name: string, runtime: string, publicRead = false) => ({ name, runtime, publicRead, reachable: true, unreachableReason: '' })

const BUCKETS: Record<string, unknown[]> = {
  'env-shop-prod': [bucket('assets', 'BUCKET_RUNTIME_KIND_HOSTED', true), bucket('uploads', 'BUCKET_RUNTIME_KIND_HOSTED')],
  'env-shop-staging': [bucket('uploads', 'BUCKET_RUNTIME_KIND_HOSTED')],
}

const object = (key: string, size: number, ago: number) => ({ key, size: String(size), updated: iso(ago), etag: `"${key.length.toString(16)}a1"` })

function listObjects(req: Record<string, unknown>) {
  const prefix = String(req.prefix ?? '')
  if (req.bucket === 'assets') {
    return { prefixes: prefix ? [] : ['img/', 'fonts/'], objects: prefix ? [object(`${prefix}hero.webp`, 182_331, 2 * DAY), object(`${prefix}logo.svg`, 4_210, 12 * DAY)] : [object('robots.txt', 64, 30 * DAY)], nextPageToken: '', auditId: '' }
  }
  return {
    prefixes: prefix ? [] : ['avatars/', 'invoices/', 'product-photos/'],
    objects: prefix
      ? [object(`${prefix}2026-11-18-1a2b.png`, 88_120, 4 * HOUR), object(`${prefix}2026-11-17-9f3c.png`, 120_404, 30 * HOUR), object(`${prefix}2026-11-12-77de.pdf`, 412_998, 6 * DAY)]
      : [object('README.txt', 312, 20 * DAY)],
    nextPageToken: '',
    auditId: 'audit-u6c-1',
  }
}

// category, list or actual, dollars per day, quantities per day [id, unit, value]
const USAGE_SHAPE: [string, 'list' | 'actual', number, [string, string, number][]][] = [
  ['hosted_apps', 'list', 1.18, [['infra_cpu', 'vcpu_hours', 26], ['infra_memory', 'gib_hours', 52]]],
  ['databases', 'list', 0.42, [['infra_storage', 'gib_hours', 240]]],
  ['static_hosting', 'list', 0.07, [['egress_kib', 'kib', 3_400_000]]],
  ['observability', 'list', 0.11, [['stored_bytes', 'bytes', 220_000_000]]],
  ['managed_llm', 'actual', 3.6, [['llm_requests', 'count', 1_450]]],
  ['machines', 'list', 1.3, [['running_seconds', 'seconds', 5.2 * 3600]]],
]

const NANOS = 1_000_000_000
const tsMs = (value: unknown, fallback: number) => {
  const ms = typeof value === 'string' ? Date.parse(value) : NaN
  return Number.isFinite(ms) ? ms : fallback
}

/** BillingService.ListOrganizationUsage, group_by category, one point per day and category. */
function organizationUsage(req: Record<string, unknown>) {
  const end = Math.min(tsMs(req.end, FIXED_NOW), FIXED_NOW)
  const startDay = Math.floor(tsMs(req.start, end - 30 * DAY) / DAY) * DAY
  const days = Math.max(1, Math.min(400, Math.ceil((end - startDay) / DAY)))
  const points = Array.from({ length: days }, (_, d) =>
    USAGE_SHAPE.map(([category, basis, perDay, quantities], c) => {
      // A gentle weekly shape, a launch bump in the last ten days, and a partial today.
      const weekday = new Date(startDay + d * DAY).getUTCDay()
      const wave = (weekday === 0 || weekday === 6 ? 0.72 : 1) * (1 + 0.1 * Math.sin((d + c) / 2.3)) * (d > days - 10 ? 1.22 : 1) * (d === days - 1 ? 0.6 : 1)
      const nanos = String(Math.round(perDay * wave * NANOS))
      return {
        bucketStart: new Date(startDay + d * DAY).toISOString(), category, groupKey: '', groupLabel: '', detail: '',
        ...(basis === 'actual' ? { actualChargesUsdNanos: nanos } : { listEquivalentUsdNanos: nanos }),
        quantities: quantities.map(([id, unit, value]) => ({ id, unit, value: Math.round(value * wave) })),
        unpricedDimensions: [],
      }
    }),
  ).flat()
  const coverage = (category: string) => ({ category, completeness: 'complete', coverageStart: new Date(startDay).toISOString(), coverageEnd: new Date(end).toISOString(), measuredThrough: iso(10 * 60_000), limitations: [] })
  return {
    orgId: IDS.org, currency: 'USD', generatedAt: iso(), start: new Date(startDay).toISOString(), end: new Date(end).toISOString(),
    granularity: 'ORGANIZATION_USAGE_GRANULARITY_DAY', groupBy: 'ORGANIZATION_USAGE_GROUP_BY_CATEGORY',
    points,
    coverage: ['managed_llm', 'machines', 'hosted_infrastructure', 'observability', 'inventory'].map(coverage),
    spendRates: USAGE_SHAPE.map(([category, basis, perDay]) => ({
      category, basis: basis === 'actual' ? 'actual' : 'list_equivalent',
      last24hUsdNanos: String(Math.round(perDay * 1.15 * NANOS)), averageDaily7dUsdNanos: String(Math.round(perDay * 1.1 * NANOS)),
      measuredHours: 168, unpricedDimensions: [], lowerBound: false,
    })),
    inventory: [
      { category: 'hosted_apps', size: '', state: 'ready', environmentId: 'env-shop-prod', environmentName: 'prod', count: '3', storageGib: '0' },
      { category: 'databases', size: '', state: 'ready', environmentId: 'env-shop-prod', environmentName: 'prod', count: '1', storageGib: '0' },
      { category: 'static_hosting', size: '', state: 'ready', environmentId: 'env-shop-prod', environmentName: 'prod', count: '1', storageGib: '0' },
      { category: 'machines', size: 'm', state: 'running', environmentId: '', environmentName: '', count: '1', storageGib: '50' },
    ],
    completeness: 'complete',
  }
}

/** Three paid monthly invoices, newest first. */
function invoices() {
  const now = new Date(FIXED_NOW)
  return Array.from({ length: 3 }, (_, i) => {
    const start = Date.UTC(now.getUTCFullYear(), now.getUTCMonth() - 1 - i, 1)
    const end = Date.UTC(now.getUTCFullYear(), now.getUTCMonth() - i, 1)
    const cents = String([5_812, 5_240, 4_675][i])
    return {
      id: `in_visual_${i + 1}`, amountDue: cents, amountPaid: cents, currency: 'usd', status: 'paid',
      periodStart: new Date(start).toISOString(), periodEnd: new Date(end).toISOString(),
      hostedInvoiceUrl: 'https://invoice.stripe.com/i/visual', pdfUrl: 'https://pay.stripe.com/invoice/visual/pdf',
      createdAt: new Date(end + HOUR).toISOString(),
    }
  })
}

/** ledger's month so far (GetProjectCost): one small backend and its database, so Usage & billing lists two projects. */
function ledgerCost() {
  const monthStart = Date.UTC(2026, 10, 1)
  const days = Math.floor((FIXED_NOW - monthStart) / DAY) + 1
  const amount = (dollars: number, basis = 'estimated') => ({ usdNanos: String(Math.round(dollars * NANOS)), basis, lowerBound: false, unpricedDimensions: [], allocated: false })
  const daily = Array.from({ length: days }, (_, d) =>
    ([['compute', 0.36], ['databases', 0.14]] as const).map(([category, perDay]) => ({
      day: new Date(monthStart + d * DAY).toISOString(), category, environmentId: 'env-ledger-prod',
      list: amount(perDay * (1 + 0.08 * Math.sin(d / 1.7)), 'list'), quantities: [], partial: d === days - 1,
    })),
  ).flat()
  return {
    currency: 'USD', generatedAt: iso(5 * 60_000), periodStart: new Date(monthStart).toISOString(), periodEnd: new Date(Date.UTC(2026, 11, 1)).toISOString(),
    measuredThrough: iso(40 * 60_000), monthToDate: amount(8.94), monthToDateList: amount(9.1, 'list'), samePointLastPeriod: amount(8.2),
    forecast: { amount: amount(14.9), lowUsdNanos: String(13 * NANOS), highUsdNanos: String(16 * NANOS), method: 'run_rate_and_trailing_7d', measuredHours: 168, unavailableReason: '', list: amount(15.2, 'list') },
    lastPeriod: amount(13.6, 'invoiced'), daily, drivers: [], allowances: [], caps: [],
  }
}

const shopfrontCost = servicesFixtures['controlplane.v1.BillingService/GetProjectCost']

export const u6cFixtures: Fixtures = {
  'controlplane.v1.BillingService/GetProjectCost': (req) =>
    req.projectId === 'ledger' ? ledgerCost() : typeof shopfrontCost === 'function' ? shopfrontCost(req) : shopfrontCost,
  'controlplane.v1.BillingService/ListOrganizationUsage': organizationUsage,
  'controlplane.v1.BillingService/ListCurrentUserInvoices': () => ({ invoices: invoices() }),
  'controlplane.v1.StorageBrowserService/ListBrowsableBuckets': (req) => ({ buckets: BUCKETS[String(req.environmentId)] ?? [], policy: 'STORAGE_ACCESS_POLICY_OPEN' }),
  'controlplane.v1.StorageBrowserService/ListObjects': listObjects,
  'controlplane.v1.ObservabilityService/QueryPanel': (req) => servicesQueryPanel(req) ?? queryPanel(req),
}
