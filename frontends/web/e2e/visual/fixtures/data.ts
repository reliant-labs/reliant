/**
 * shopfront, U6e Data: the database's home and its tables.
 *
 *   Databases   prod and staging each have the managed Postgres `db` (its
 *               deployment is GetStatus's, console.ts): browsable, open
 *               policy. Its live status, backups and metrics are not
 *               reported by any RPC, so the page says "coming soon" — no
 *               fixture invents them.
 *   Tables      public.customers, public.orders (FK to customers) and
 *               public.order_items (FK to orders), with a page of rows each.
 *
 * Fabricated; every time is relative to FIXED_NOW.
 */
import type { Fixtures } from '../rpc'
import { DAY, HOUR, iso } from './base'
import { consoleFixtures } from './console'

const DATABASES: Record<string, string[]> = { 'env-shop-prod': ['db'], 'env-shop-staging': ['db'] }

const col = (name: string, pgType: string, extra: Record<string, unknown> = {}) => ({ name, pgType, nullable: false, comment: '', isPrimaryKey: false, ...extra })
const fk = (table: string, columns: string[], refTable: string, refColumns: string[]) => ({
  constraint: `${table}_${columns.join('_')}_fkey`, schema: 'public', table, columns, refSchema: 'public', refTable, refColumns,
})

const ORDERS_CUSTOMER = fk('orders', ['customer_id'], 'customers', ['id'])
const ITEMS_ORDER = fk('order_items', ['order_id'], 'orders', ['id'])

const TABLES = [
  {
    schema: 'public', name: 'customers', kind: 'DATABASE_TABLE_KIND_TABLE', estimatedRows: '1842', comment: 'People who have checked out at least once.',
    columns: [col('id', 'bigint', { isPrimaryKey: true }), col('email', 'text', { identitySubject: 'IDENTITY_SUBJECT_KIND_EMAIL' }), col('name', 'text'), col('created_at', 'timestamp with time zone')],
    primaryKey: ['id'], foreignKeys: [], referencedBy: [ORDERS_CUSTOMER],
  },
  {
    schema: 'public', name: 'orders', kind: 'DATABASE_TABLE_KIND_TABLE', estimatedRows: '5310', comment: '',
    columns: [col('id', 'bigint', { isPrimaryKey: true }), col('customer_id', 'bigint'), col('status', 'text'), col('total_cents', 'bigint'), col('discount_code', 'text', { nullable: true }), col('created_at', 'timestamp with time zone')],
    primaryKey: ['id'], foreignKeys: [ORDERS_CUSTOMER], referencedBy: [ITEMS_ORDER],
  },
  {
    schema: 'public', name: 'order_items', kind: 'DATABASE_TABLE_KIND_TABLE', estimatedRows: '12977', comment: '',
    columns: [col('id', 'bigint', { isPrimaryKey: true }), col('order_id', 'bigint'), col('sku', 'text'), col('quantity', 'integer'), col('unit_cents', 'bigint')],
    primaryKey: ['id'], foreignKeys: [ITEMS_ORDER], referencedBy: [],
  },
]

const PG_TYPE: Record<string, string> = { bigint: 'int8', integer: 'int4', text: 'text', 'timestamp with time zone': 'timestamptz' }

const NAMES = ['Ada Park', 'Bo Lindqvist', 'Chen Wei', 'Dara Okafor', 'Emil Novak', 'Farah Haddad', 'Gus Moreno', 'Hana Sato']
const STATUSES = ['paid', 'paid', 'shipped', 'paid', 'refunded', 'shipped', 'paid', 'pending']
const SKUS = ['MUG-BLK', 'MUG-WHT', 'TEE-M', 'TOTE', 'CAP']

function rowsOf(table: string): unknown[][] {
  switch (table) {
    case 'customers':
      return NAMES.map((name, i) => [1842 - i, `${name.split(' ')[0]!.toLowerCase()}@example.com`, name, iso((i + 1) * 3 * DAY)])
    case 'orders':
      return STATUSES.map((status, i) => [5310 - i, 1842 - (i % 5), status, 2400 + i * 1375, i % 3 === 0 ? 'SAVE10' : null, iso((i + 1) * 2 * HOUR)])
    case 'order_items':
      return Array.from({ length: 10 }, (_, i) => [12977 - i, 5310 - Math.floor(i / 2), SKUS[i % SKUS.length], 1 + (i % 3), 1200 + (i % 4) * 300])
    default:
      return []
  }
}

function browseTable(req: Record<string, unknown>) {
  const qualified = String(req.table ?? '')
  const name = qualified.includes('.') ? qualified.split('.')[1]! : qualified
  const table = TABLES.find((t) => t.name === name)
  if (!table) return { columns: [], rows: [], hasMore: false, durationMs: '3', database: String(req.database ?? ''), table: qualified, replica: 'primary', nextOffset: '0' }
  const rows = rowsOf(name)
  return {
    columns: table.columns.map((c) => ({ name: c.name, pgType: PG_TYPE[c.pgType] ?? c.pgType })),
    rows: rows.map((values) => ({ values, truncatedCells: [] })),
    hasMore: false,
    total: table.estimatedRows,
    totalIsEstimate: true,
    totalIsLowerBound: false,
    durationMs: '7',
    database: String(req.database ?? 'db'),
    table: qualified,
    replica: 'primary',
    truncated: false,
    nextOffset: String(rows.length),
  }
}

export const dataFixtures: Fixtures = {
  'controlplane.v1.AppDatabaseService/ListBrowsableDatabases': (req) => ({
    databases: (DATABASES[String(req.environmentId)] ?? []).map((name) => ({ name, kind: 'BROWSABLE_DATABASE_KIND_MANAGED', policy: 'DATA_ACCESS_POLICY_OPEN', reachable: true, unreachableReason: '' })),
  }),
  'controlplane.v1.AppDatabaseService/DescribeDatabase': (req) => ({ database: String(req.database ?? 'db'), tables: TABLES, durationMs: '11', auditId: '' }),
  'controlplane.v1.AppDatabaseService/BrowseTable': browseTable,
}

/**
 * Staging with its managed Postgres declared, for the surface that shows the
 * database's home (prod opens the audited-read dialog first). Only that page
 * asks for it: the shared console fixture's staging stays as it is.
 */
export const stagingDatabaseFixtures: Fixtures = {
  'controlplane.v1.DeployService/GetStatus': (req) => {
    const base = (consoleFixtures['controlplane.v1.DeployService/GetStatus'] as (r: Record<string, unknown>) => Record<string, unknown>)
    const status = base(req)
    if (req.environmentId !== 'env-shop-staging') return status
    const prodDb = (base({ environmentId: 'env-shop-prod' }).deployments as { deployment: { name: string } }[]).find((d) => d.deployment.name === 'db')
    return { ...status, deployments: [...(status.deployments as unknown[]), prodDb] }
  },
}
