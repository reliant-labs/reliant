/**
 * shopfront, U6c pages: what Storage and Speed insights read.
 *
 *   Storage          prod declares two buckets (uploads, private; assets,
 *                    public read, on Reliant hosting), staging one; uploads
 *                    lists a few folders and files.
 *   Speed insights   p75 web vitals for six pages of the storefront, from the
 *                    browser runtime's `webvitals` spans (one SQL QueryPanel,
 *                    panel id `speed-insights-pages`): mostly good, checkout's
 *                    LCP and search's INP needing work, one poor CLS.
 *
 * Fabricated; every time is relative to FIXED_NOW.
 */
import type { Fixtures } from '../rpc'
import { DAY, HOUR, iso } from './base'
import { queryPanel } from './console'
import { servicesQueryPanel } from './services'

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

// page, lcp ms, inp ms, cls, ttfb ms, page views
const VITALS: [string, number, number, number, number, number][] = [
  ['/', 1820, 96, 0.02, 310, 18_420],
  ['/products', 2140, 142, 0.05, 380, 9_870],
  ['/products/view', 2390, 168, 0.31, 410, 7_215],
  ['/cart', 1610, 88, 0.01, 290, 3_402],
  ['/checkout', 3120, 184, 0.04, 640, 1_288],
  ['/search', 2010, 356, 0.07, 350, 2_960],
]

export function u6cQueryPanel(req: Record<string, unknown>): Record<string, unknown> | undefined {
  const panel = (req.panel ?? {}) as { id?: string }
  if (panel.id === 'speed-insights-pages') {
    return {
      panelId: panel.id, kind: 'sql', unit: '', stepSeconds: 0, series: [], columns: ['lcp', 'inp', 'cls', 'ttfb', 'samples'],
      rows: VITALS.map(([page, lcp, inp, cls, ttfb, views]) => ({ group: { page }, values: [lcp, inp, cls, ttfb, views], missingValueIndexes: [] })),
    }
  }
  return undefined
}

export const u6cFixtures: Fixtures = {
  'controlplane.v1.StorageBrowserService/ListBrowsableBuckets': (req) => ({ buckets: BUCKETS[String(req.environmentId)] ?? [], policy: 'STORAGE_ACCESS_POLICY_OPEN' }),
  'controlplane.v1.StorageBrowserService/ListObjects': listObjects,
  'controlplane.v1.ObservabilityService/QueryPanel': (req) => u6cQueryPanel(req) ?? servicesQueryPanel(req) ?? queryPanel(req),
}
