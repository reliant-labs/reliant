/**
 * shopfront, U6e: what the project Overview's Analytics box and the
 * Observability Overview read beyond the other tables.
 *
 *   Analytics box          QueryPanel `overview-analytics-totals` (active users and
 *                          events over 7 days; the previous 7 days a little lower) and
 *                          `overview-analytics-daily` (active users per day, 7 days).
 *                          The top event comes from services.ts's events catalog.
 *   Slowest endpoints      GetServiceGraphCatalog: shopfront's Connect methods, each
 *                          with a log-normal latency histogram around its median
 *                          (checkout's PlaceOrder the slowest, with a few faults)
 *   Service map thumbnail  GetServiceGraph: web → api → db, api → worker (the
 *                          hosted preview's richer map, preview/obs, wins there)
 *
 * QueryPanel answers its own two panels and hands every other to the table
 * registered before it (u6c.ts), so the chain keeps working. Fabricated;
 * every time is relative to FIXED_NOW.
 */
import type { Fixtures } from '../rpc'
import { DAY, FIXED_NOW, MIN, iso } from './base'
import { u6cFixtures } from './u6c'

const earlier = u6cFixtures['controlplane.v1.ObservabilityService/QueryPanel']

/** Whether a panel read is for an earlier window (the box's previous period, asked with an offset). */
function previousWindow(req: Record<string, unknown>): boolean {
  const range = ((req.scope ?? {}) as { timeRange?: { end?: string } }).timeRange
  const end = range?.end ? Date.parse(range.end) : FIXED_NOW
  return end < FIXED_NOW - 5 * MIN
}

// Active users per day, oldest first: weekdays busier than the weekend.
const DAILY = [1_284, 1_402, 1_377, 1_455, 1_198, 862, 931]

export function overviewQueryPanel(req: Record<string, unknown>): Record<string, unknown> | undefined {
  const panel = (req.panel ?? {}) as { id?: string }
  if (panel.id === 'overview-analytics-totals') {
    const before = previousWindow(req)
    return {
      panelId: panel.id, kind: 'sql', unit: '', stepSeconds: 0, series: [], columns: ['users', 'events'],
      rows: [{ group: {}, values: before ? [3_918, 61_240] : [4_412, 70_385], missingValueIndexes: [] }],
    }
  }
  if (panel.id === 'overview-analytics-daily') {
    const day0 = Math.floor(FIXED_NOW / DAY) * DAY - 6 * DAY
    return {
      panelId: panel.id, kind: 'sql', unit: '', stepSeconds: 86_400, columns: ['users'],
      series: [{ label: 'users', group: {}, value: 0, points: DAILY.map((value, i) => ({ time: new Date(day0 + i * DAY).toISOString(), value, partial: i === DAILY.length - 1 })) }],
      rows: [],
    }
  }
  return undefined
}

// ── Latency histograms (proto JSON: GraphHistogram) ─────────────────────────

const BOUNDS = [0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5]

function erf(x: number): number {
  const t = 1 / (1 + 0.3275911 * Math.abs(x))
  const y = 1 - ((((1.061405429 * t - 1.453152027) * t + 1.421413741) * t - 0.284496736) * t + 0.254829592) * t * Math.exp(-x * x)
  return x >= 0 ? y : -y
}

/** Noncumulative bucket counts of `count` calls, log-normal around `medianS`. */
function histogram(count: number, medianS: number, sigma = 0.7) {
  const cdf = (x: number) => 0.5 * (1 + erf(Math.log(x / medianS) / (sigma * Math.SQRT2)))
  let prev = 0
  const buckets = BOUNDS.map((b) => {
    const c = cdf(b)
    const n = Math.round(count * (c - prev))
    prev = c
    return n
  })
  buckets.push(Math.round(count * (1 - prev)))
  const total = buckets.reduce((a, b) => a + b, 0)
  return { version: 1, upperBoundsSeconds: BOUNDS, bucketCounts: buckets.map(String), count: String(total), sumSeconds: Math.round(total * medianS * Math.exp((sigma * sigma) / 2) * 1000) / 1000 }
}

// [service, method, calls per day, median seconds, fault rate]
const METHODS: [string, string, number, number, number][] = [
  ['shop.v1.CheckoutService', 'PlaceOrder', 6_400, 0.42, 0.006],
  ['shop.v1.CheckoutService', 'QuoteShipping', 9_100, 0.19, 0.001],
  ['shop.v1.SearchService', 'Search', 58_000, 0.11, 0.0005],
  ['shop.v1.CartService', 'AddItem', 41_000, 0.038, 0.002],
  ['shop.v1.CatalogService', 'GetProduct', 212_000, 0.012, 0],
  ['shop.v1.CatalogService', 'ListProducts', 96_000, 0.024, 0],
  ['shop.v1.CartService', 'GetCart', 120_000, 0.009, 0],
]

function windowDays(req: Record<string, unknown>): number {
  const range = ((req.scope ?? {}) as { timeRange?: { start?: string; end?: string } }).timeRange
  const start = range?.start ? Date.parse(range.start) : FIXED_NOW - DAY
  const end = range?.end ? Date.parse(range.end) : FIXED_NOW
  return Math.max(1 / 96, (end - start) / DAY)
}

const isShopfront = (req: Record<string, unknown>) => ((req.scope ?? {}) as { projectId?: string }).projectId === 'shopfront'

function graphCatalog(req: Record<string, unknown>) {
  const days = windowDays(req)
  const methods = isShopfront(req)
    ? METHODS.map(([service, method, perDay, medianS, faultRate]) => {
        const count = Math.round(perDay * days)
        return {
          rpcId: `${service}/${method}`, rpcService: service, method, processNodeIds: ['api'], evidence: 'GRAPH_EVIDENCE_KIND_OBSERVED',
          observedCount: String(count), serverFaultCount: String(Math.round(count * faultRate)), userOutcomeCount: '0',
          duration: histogram(count, medianS), inputType: '', outputType: '', streamMode: '', sourceUnavailableReason: '', idempotency: '',
          servesProcessIds: ['api'], canCallProcessIds: [],
        }
      })
    : []
  return { methods, declaredCatalogStatus: '', declaredCatalogUnavailableReason: '', declaredCatalogs: [] }
}

function serviceGraph(req: Record<string, unknown>) {
  if (!isShopfront(req)) return { nodes: [], edges: [] }
  const days = windowDays(req)
  const node = (id: string, kind: string) => ({ id, envId: 'env-shop-prod', kind: `GRAPH_NODE_KIND_${kind}`, label: id, parentNodeId: '', rpcId: '', instrumentationScope: '', sourceUnavailableReason: '' })
  const edge = (src: string, dst: string, kind: string, perDay: number, medianS: number, faultRate: number, unit = 'server_request', viewpoint = 'server') => {
    const count = Math.round(perDay * days)
    return {
      id: `${src}-${dst}`, sourceNodeId: src, destinationNodeId: dst, envId: 'env-shop-prod', kind: `GRAPH_EDGE_KIND_${kind}`, evidence: 'GRAPH_EVIDENCE_KIND_OBSERVED',
      rpcId: '', unit, viewpoint, cohort: 'env-agent', count: String(count), serverFaultCount: String(Math.round(count * faultRate)), userOutcomeCount: '0',
      cancelledCount: '0', unknownStatusCount: '0', matchedCount: String(count), unmatchedCount: '0', duration: histogram(count, medianS),
      exemplars: [], firstSeen: iso(days * DAY), lastSeen: iso(15 * 1000),
    }
  }
  return {
    nodes: [node('web', 'FRONTEND'), node('api', 'PROCESS'), node('worker', 'PROCESS'), node('db', 'DATABASE')],
    edges: [
      edge('web', 'api', 'SYNC', 540_000, 0.03, 0.004),
      edge('api', 'db', 'DATABASE', 1_200_000, 0.004, 0, 'database_operation', 'client'),
      edge('api', 'worker', 'ASYNC_SEND', 6_400, 0.002, 0, 'publish', 'producer'),
      edge('worker', 'db', 'DATABASE', 90_000, 0.006, 0, 'database_operation', 'client'),
    ],
    completeness: {
      metricsOrigin: 'PRE_TAIL_RECEIVED', upstreamLossUnknown: false, upstreamSamplingUnknown: false, truncated: false, provisional: false,
      duplicateCount: '0', conflictingCount: '0', unmatchedCount: '0', rejectedCount: '0', overflowCount: '0',
      aggregatedThrough: iso(30 * 1000), warnings: [], availableMinutes: Math.round(days * 24 * 60), requestedMinutes: Math.round(days * 24 * 60),
    },
    effectiveTimeRange: { start: iso(days * DAY), end: iso(0) },
  }
}

export const overviewFixtures: Fixtures = {
  'controlplane.v1.ObservabilityService/QueryPanel': (req) => overviewQueryPanel(req) ?? (typeof earlier === 'function' ? earlier(req) : (earlier ?? {})),
  'controlplane.v1.ObservabilityService/GetServiceGraphCatalog': graphCatalog,
  'controlplane.v1.ObservabilityService/GetServiceGraph': serviceGraph,
}
