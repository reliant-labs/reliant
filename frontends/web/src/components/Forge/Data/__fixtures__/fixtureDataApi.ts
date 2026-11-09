// Copyright (c) 2025 Reliant Labs

/**
 * An in-memory DataBrowserApi: a small shop's database, answered the way
 * control-plane's AppDatabaseService answers (filters, sort, offset paging,
 * has_more, ESTIMATED and capped EXACT counts, the audited environment's
 * confirmation), so the component tests and the dev-only preview
 * (/forge-data-preview) exercise the real browser against real behaviour.
 *
 *   environments  env-prod (prod, AUDITED), env-staging (staging, OPEN),
 *                 env-dev (dev, OPEN)
 *   databases     shop (managed, reachable) and analytics (external,
 *                 unreachable); `deny` makes shop's policy DENY
 *   shop          public.users        uuid key, jsonb profile, soft delete
 *                 public.orders       int8 key, user_id → users
 *                 public.order_items  composite key (order_id, line) → orders
 *                 billing.invoices    order_id → orders
 *                 public.events       25,000 rows (an exact count caps at 10,000)
 *                 public.order_totals a view: no key, no estimate
 *
 * Every call is recorded in `calls`, and `maxInFlight` is the most BrowseTable
 * reads that ran at once — what the Related section's gate is tested against.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import type {
  BrowseInput,
  BrowsePage,
  BrowsableDb,
  DataAccessConfirmation,
  DataAccessPolicyName,
  DbCell,
  DbDescription,
  DbForeignKey,
  DbTable,
} from "@/services/controlPlane/observability/appDatabaseApi";
import type { DataBrowserApi } from "@/services/controlPlane/observability/DataBrowserApiContext";

export const DATA_FIXTURE_ENVS = [
  { id: "env-prod", name: "prod" },
  { id: "env-staging", name: "staging" },
  { id: "env-dev", name: "dev" },
] as const;

export interface DataFixtureOptions {
  /** Every database's policy, per environment id (default: prod audited, the rest open). */
  policy?: Partial<Record<string, DataAccessPolicyName>>;
  /** ListBrowsableDatabases answers none. */
  noDatabases?: boolean;
  /** Only the shop database (no unreachable analytics). */
  onlyShop?: boolean;
  /** Fail this rpc with this code (and reason, sent as x-forge-error-reason). */
  fail?: { method: "listBrowsableDatabases" | "describeDatabase" | "browseTable"; code: Code; reason?: string; message?: string };
  /** Answer after this many ms (loading states; the concurrency gate). */
  delayMs?: number;
  /** The EXACT count's cap. */
  exactCap?: number;
  /** This many more (empty) tables with a foreign key to public.users: a long Related section. */
  extraReferences?: number;
  /**
   * The response-size cap, in bytes of row JSON: a page whose rows pass it is
   * cut (at least one row), with truncated set and next_offset where it
   * stopped. Default: none.
   */
  responseByteCap?: number;
  /** Fill truncated_cells for the cells cut at 4 KiB (the server leaves it empty until forge says which). Default false. */
  flagCutCells?: boolean;
}

/** A cell the server returns is cut at this many bytes, with a marker (forge pkg/dbquery MaxCellBytes). */
export const FIXTURE_CELL_CAP = 4096;

export interface DataFixtureApi extends DataBrowserApi {
  calls: { method: string; req: unknown }[];
  maxInFlight: number;
}

// ── The schema ──────────────────────────────────────────────────────────────

/**
 * The two spellings of each type, as the server sends them: DescribeDatabase
 * says the declared type as Postgres prints it (format_type), a BrowseTable
 * result column the internal name. The fixture declares a column by its
 * internal name and describes it with the printed one.
 */
const DESCRIBED: Record<string, string> = {
  int4: "integer",
  int8: "bigint",
  text: "text",
  varchar: "character varying(255)",
  uuid: "uuid",
  jsonb: "jsonb",
  timestamptz: "timestamp with time zone",
  _text: "text[]",
};
const RESULT_TYPE = new Map(Object.entries(DESCRIBED).map(([internal, described]) => [described, internal]));

const col = (name: string, internalType: string, extra: { nullable?: boolean; pk?: boolean; comment?: string } = {}) => ({
  name,
  pgType: DESCRIBED[internalType] ?? internalType,
  nullable: extra.nullable ?? false,
  comment: extra.comment ?? "",
  isPrimaryKey: extra.pk ?? false,
});

const fk = (constraint: string, schema: string, table: string, columns: string[], refSchema: string, refTable: string, refColumns: string[]): DbForeignKey => ({
  constraint,
  schema,
  table,
  columns,
  refSchema,
  refTable,
  refColumns,
});

const FKS: DbForeignKey[] = [
  fk("orders_user_id_fkey", "public", "orders", ["user_id"], "public", "users", ["id"]),
  fk("order_items_order_id_fkey", "public", "order_items", ["order_id"], "public", "orders", ["id"]),
  fk("invoices_order_id_fkey", "billing", "invoices", ["order_id"], "public", "orders", ["id"]),
  fk("events_user_id_fkey", "public", "events", ["user_id"], "public", "users", ["id"]),
];

type TableSpec = Omit<DbTable, "foreignKeys" | "referencedBy">;

const SPECS: TableSpec[] = [
  {
    schema: "billing",
    name: "invoices",
    kind: "table",
    estimatedRows: 180,
    comment: "One invoice per paid order.",
    columns: [col("id", "int8", { pk: true }), col("order_id", "int8"), col("amount_cents", "int8"), col("issued_at", "timestamptz"), col("pdf_url", "text", { nullable: true })],
    primaryKey: ["id"],
  },
  {
    schema: "public",
    name: "events",
    kind: "table",
    estimatedRows: 24_800,
    comment: "",
    columns: [col("id", "int8", { pk: true }), col("user_id", "uuid"), col("name", "text"), col("at", "timestamptz"), col("props", "jsonb")],
    primaryKey: ["id"],
  },
  {
    schema: "public",
    name: "order_items",
    kind: "table",
    estimatedRows: 610,
    comment: "",
    columns: [col("order_id", "int8", { pk: true }), col("line", "int4", { pk: true }), col("sku", "text"), col("qty", "int4")],
    primaryKey: ["order_id", "line"],
  },
  {
    schema: "public",
    name: "order_totals",
    kind: "view",
    estimatedRows: -1,
    comment: "Orders with their item count.",
    columns: [col("order_id", "int8", { nullable: true }), col("items", "int8", { nullable: true })],
    primaryKey: [],
  },
  {
    schema: "public",
    name: "orders",
    kind: "table",
    estimatedRows: 1_180,
    comment: "",
    columns: [
      col("id", "int8", { pk: true }),
      col("user_id", "uuid"),
      col("status", "text", { comment: "pending, paid, shipped or cancelled" }),
      col("total_cents", "int8"),
      col("notes", "text", { nullable: true }),
      col("metadata", "jsonb"),
      col("created_at", "timestamptz"),
    ],
    primaryKey: ["id"],
  },
  {
    schema: "public",
    name: "users",
    kind: "table",
    estimatedRows: 40,
    comment: "People who signed up.",
    columns: [
      col("id", "uuid", { pk: true }),
      col("email", "varchar"),
      col("name", "text", { nullable: true }),
      col("profile", "jsonb", { nullable: true }),
      col("tags", "_text"),
      col("created_at", "timestamptz"),
      col("deleted_at", "timestamptz", { nullable: true }),
    ],
    primaryKey: ["id"],
  },
];

export const FIXTURE_TABLES: DbTable[] = SPECS.map((spec) => ({
  ...spec,
  foreignKeys: FKS.filter((k) => k.schema === spec.schema && k.table === spec.name),
  referencedBy: FKS.filter((k) => k.refSchema === spec.schema && k.refTable === spec.name),
}));

// ── The rows ────────────────────────────────────────────────────────────────

type Row = Record<string, DbCell>;

const STATUSES = ["pending", "paid", "shipped", "cancelled"];
const BASE_MS = Date.UTC(2026, 9, 1, 9, 0, 0);
const iso = (ms: number) => new Date(ms).toISOString();

export function fixtureUserId(i: number): string {
  return `0b7c${String(i).padStart(4, "0")}-6a1e-4c2f-9d3b-${String(1000 + i * 37).padStart(12, "0")}`;
}

function buildRows(): Record<string, Row[]> {
  const users: Row[] = Array.from({ length: 40 }, (_, i) => ({
    id: fixtureUserId(i),
    email: `user${i}@example.com`,
    name: i % 7 === 3 ? null : `User ${i}`,
    profile: i % 5 === 0 ? null : { plan: i % 3 === 0 ? "team" : "solo", locale: "en-US", flags: { beta: i % 2 === 0 }, bio: i === 1 ? "A long biography ".repeat(20).trim() : i === 2 ? "An essay. ".repeat(600).trim() : "" },
    tags: i % 4 === 0 ? ["early", "vip"] : [],
    created_at: iso(BASE_MS + i * 86_400_000),
    deleted_at: i === 9 ? iso(BASE_MS + 30 * 86_400_000) : null,
  }));
  const orders: Row[] = Array.from({ length: 230 }, (_, i) => ({
    id: 1000 + i,
    user_id: fixtureUserId(i % 40),
    status: STATUSES[i % 4]!,
    total_cents: 1299 + ((i * 731) % 9000),
    notes: i % 6 === 0 ? `Gift wrap, please.\nLeave at the door (order ${1000 + i}).` : null,
    metadata: { channel: i % 2 ? "web" : "mobile", coupon: i % 9 === 0 ? "AUTUMN" : null },
    created_at: iso(BASE_MS + i * 3_600_000),
  }));
  const orderItems: Row[] = orders.flatMap((order, i) =>
    Array.from({ length: (i % 3) + 1 }, (_, line) => ({ order_id: order.id, line: line + 1, sku: `SKU-${(i * 7 + line) % 50}`, qty: (line % 3) + 1 }))
  );
  const invoices: Row[] = orders
    .filter((order) => order.status !== "pending" && order.status !== "cancelled")
    .map((order, i) => ({ id: 5000 + i, order_id: order.id, amount_cents: order.total_cents, issued_at: order.created_at, pdf_url: i % 10 === 0 ? null : `https://files.example.com/invoices/${5000 + i}.pdf` }));
  const totals: Row[] = orders.map((order) => ({ order_id: order.id, items: orderItems.filter((item) => item.order_id === order.id).length }));
  return { "billing.invoices": invoices, "public.order_items": orderItems, "public.order_totals": totals, "public.orders": orders, "public.users": users };
}

let rowsCache: Record<string, Row[]> | null = null;
let eventsCache: Row[] | null = null;

function rowsOf(table: string): Row[] {
  if (table === "public.events") {
    eventsCache ??= Array.from({ length: 25_000 }, (_, i) => ({
      id: i + 1,
      user_id: fixtureUserId(i % 40),
      name: ["page_view", "add_to_cart", "checkout"][i % 3]!,
      at: iso(BASE_MS + i * 60_000),
      props: { path: `/p/${i % 100}` },
    }));
    return eventsCache;
  }
  rowsCache ??= buildRows();
  return rowsCache[table] ?? [];
}

// ── Evaluation ──────────────────────────────────────────────────────────────

function asText(value: DbCell): string | null {
  if (value === null) return null;
  return typeof value === "string" ? value : typeof value === "object" ? JSON.stringify(value) : String(value);
}

function compare(a: DbCell, b: string): number {
  if (typeof a === "number") return a - Number(b);
  const text = asText(a) ?? "";
  return text < b ? -1 : text > b ? 1 : 0;
}

function likeRegex(pattern: string, flags: string): RegExp {
  const body = pattern.replace(/[.*+?^${}()|[\]\\]/g, "\\$&").replace(/%/g, ".*").replace(/_/g, ".");
  return new RegExp(`^${body}$`, flags);
}

function matches(row: Row, filter: BrowseInput["filters"][number], pgType: string): boolean {
  const value = row[filter.column] ?? null;
  const [v] = filter.values;
  if (pgType === "int8" || pgType === "int4") {
    for (const candidate of filter.values) {
      if (filter.op !== "like" && filter.op !== "ilike" && candidate !== "" && !/^-?\d+$/.test(candidate)) {
        throw new ConnectError(`invalid input syntax for type ${pgType === "int8" ? "bigint" : "integer"}: "${candidate}"`, Code.InvalidArgument);
      }
    }
  }
  switch (filter.op) {
    case "is_null":
      return value === null;
    case "not_null":
      return value !== null;
    case "eq":
      return value !== null && compare(value, v!) === 0;
    case "neq":
      return value !== null && compare(value, v!) !== 0;
    case "lt":
      return value !== null && compare(value, v!) < 0;
    case "lte":
      return value !== null && compare(value, v!) <= 0;
    case "gt":
      return value !== null && compare(value, v!) > 0;
    case "gte":
      return value !== null && compare(value, v!) >= 0;
    case "like":
      return value !== null && likeRegex(v!, "").test(asText(value)!);
    case "ilike":
      return value !== null && likeRegex(v!, "i").test(asText(value)!);
    case "in":
      return value !== null && filter.values.some((candidate) => compare(value, candidate) === 0);
  }
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(resolve, ms);
    signal?.addEventListener("abort", () => {
      clearTimeout(timer);
      reject(new ConnectError("aborted", Code.Canceled));
    });
  });
}

function reasonError(message: string, code: Code, reason?: string): ConnectError {
  return new ConnectError(message, code, reason ? new Headers({ "x-forge-error-reason": reason }) : undefined);
}

export function createDataFixtureApi(options: DataFixtureOptions = {}): DataFixtureApi {
  const policies: Record<string, DataAccessPolicyName> = { "env-prod": "audited", "env-staging": "open", "env-dev": "open", ...options.policy } as Record<string, DataAccessPolicyName>;
  const tables = tablesWith(options.extraReferences ?? 0);
  let inFlight = 0;

  const api: DataFixtureApi = {
    calls: [],
    maxInFlight: 0,

    async listBrowsableDatabases(environmentId, opts = {}) {
      api.calls.push({ method: "listBrowsableDatabases", req: { environmentId } });
      if (options.delayMs) await sleep(options.delayMs, opts.signal);
      if (options.fail?.method === "listBrowsableDatabases") throw reasonError(options.fail.message ?? "refused", options.fail.code, options.fail.reason);
      if (options.noDatabases) return [];
      const policy = policies[environmentId] ?? "open";
      const dbs: BrowsableDb[] = [{ name: "shop", kind: "managed", policy, reachable: true, unreachableReason: "" }];
      if (!options.onlyShop) {
        dbs.unshift({ name: "analytics", kind: "external", policy, reachable: false, unreachableReason: "external database not dialable from the control plane" });
      }
      return dbs;
    },

    async describeDatabase(req, opts = {}) {
      api.calls.push({ method: "describeDatabase", req });
      admit(req.environmentId, req.access);
      if (options.delayMs) await sleep(options.delayMs, opts.signal);
      if (options.fail?.method === "describeDatabase") throw reasonError(options.fail.message ?? "refused", options.fail.code, options.fail.reason);
      if (req.database !== "shop") throw new ConnectError(`no database ${req.database}`, Code.NotFound);
      const out: DbDescription = { database: "shop", tables, durationMs: 14 };
      return out;
    },

    async browseTable(req, opts = {}) {
      api.calls.push({ method: "browseTable", req });
      admit(req.environmentId, req.access);
      inFlight++;
      api.maxInFlight = Math.max(api.maxInFlight, inFlight);
      try {
        if (options.delayMs) await sleep(options.delayMs, opts.signal);
        if (options.fail?.method === "browseTable") throw reasonError(options.fail.message ?? "refused", options.fail.code, options.fail.reason);
        return browse(tables, req, options);
      } finally {
        inFlight--;
      }
    },
  };

  function admit(environmentId: string, access: DataAccessConfirmation | undefined) {
    const policy = policies[environmentId] ?? "open";
    if (policy === "deny" || policy === "unknown") throw new ConnectError("data access policy denies reads", Code.PermissionDenied);
    if (policy === "audited" && (!access || access.confirmEnvId !== environmentId || !access.reason)) {
      throw reasonError("this environment is audited: confirm_env_id and a reason are required", Code.FailedPrecondition, "confirmation_required");
    }
  }

  return api;
}

/** The fixture's tables, with `extra` more referencing public.users. */
function tablesWith(extra: number): DbTable[] {
  if (extra <= 0) return FIXTURE_TABLES;
  const refs = Array.from({ length: extra }, (_, i) => fk(`ref_${i}_user_id_fkey`, "audit", `ref_${i}`, ["user_id"], "public", "users", ["id"]));
  return [
    ...refs.map(
      (ref): DbTable => ({
        schema: "audit",
        name: ref.table,
        kind: "table",
        estimatedRows: 0,
        comment: "",
        columns: [col("id", "int8", { pk: true }), col("user_id", "uuid")],
        primaryKey: ["id"],
        foreignKeys: [ref],
        referencedBy: [],
      })
    ),
    ...FIXTURE_TABLES.map((t) => (t.schema === "public" && t.name === "users" ? { ...t, referencedBy: [...t.referencedBy, ...refs] } : t)),
  ];
}

/** A cell as the server sends it: cut at FIXTURE_CELL_CAP bytes with forge's marker, as text, when its JSON is longer. */
function capCell(value: DbCell): { value: DbCell; cut: boolean } {
  if (value === null || typeof value === "number" || typeof value === "boolean") return { value, cut: false };
  const text = typeof value === "string" ? value : JSON.stringify(value);
  const bytes = new TextEncoder().encode(text);
  if (bytes.length <= FIXTURE_CELL_CAP) return { value, cut: false };
  const shown = new TextDecoder().decode(bytes.slice(0, FIXTURE_CELL_CAP));
  return { value: `${shown}…[truncated: ${FIXTURE_CELL_CAP} of ${bytes.length} bytes shown]`, cut: true };
}

function browse(tables: DbTable[], req: BrowseInput, options: DataFixtureOptions): BrowsePage {
  const exactCap = options.exactCap ?? 10_000;
  const table = tables.find((t) => `${t.schema}.${t.name}` === req.table);
  if (!table) throw new ConnectError(`table ${req.table} not found`, Code.NotFound);
  // Result columns carry the internal type name, not DescribeDatabase's spelling.
  const types = new Map(table.columns.map((c) => [c.name, RESULT_TYPE.get(c.pgType) ?? c.pgType]));
  for (const name of [...req.columns, ...req.filters.map((f) => f.column), ...req.sort.map((s) => s.column)]) {
    if (!types.has(name)) throw new ConnectError(`column ${name} is not readable in ${req.table}`, Code.InvalidArgument);
  }
  const columns = req.columns.length > 0 ? req.columns : table.columns.map((c) => c.name);
  let rows = rowsOf(req.table).filter((row) => req.filters.every((f) => matches(row, f, types.get(f.column)!)));
  const sort = req.sort.length > 0 ? req.sort : table.primaryKey.map((column) => ({ column, desc: false }));
  if (sort.length > 0) {
    rows = [...rows].sort((a, b) => {
      for (const s of sort) {
        const x = a[s.column] ?? null;
        const y = b[s.column] ?? null;
        if (x === y) continue;
        // NULLs last ascending, first descending (Postgres's defaults).
        if (x === null) return s.desc ? -1 : 1;
        if (y === null) return s.desc ? 1 : -1;
        const order = typeof x === "number" && typeof y === "number" ? x - y : (asText(x)! < asText(y)! ? -1 : 1);
        if (order !== 0) return s.desc ? -order : order;
      }
      return 0;
    });
  }
  let page = rows.slice(req.offset, req.offset + req.limit);
  const matched = rows.length;
  // The response-size cap: stop before the row that passes it, keeping at least one.
  let truncated = false;
  if (options.responseByteCap !== undefined) {
    let bytes = 0;
    const kept: Row[] = [];
    for (const row of page) {
      bytes += JSON.stringify(columns.map((name) => row[name] ?? null)).length;
      if (kept.length > 0 && bytes > options.responseByteCap) break;
      kept.push(row);
    }
    truncated = kept.length < page.length;
    page = kept;
  }
  let total: number | null = null;
  let totalIsEstimate = false;
  let totalIsLowerBound = false;
  if (req.count === "estimated") {
    // The planner is close, not exact: a little high for a big table, and it knows nothing of a narrow filter.
    total = req.filters.length > 0 ? Math.max(1, Math.round(matched * 0.8)) : Math.round(matched * 1.02);
    totalIsEstimate = true;
  } else if (req.count === "exact") {
    total = Math.min(matched, exactCap);
    totalIsLowerBound = matched > exactCap;
  }
  const cells = page.map((row) => columns.map((name) => capCell(row[name] ?? null)));
  return {
    columns: columns.map((name) => ({ name, pgType: types.get(name)! })),
    rows: cells.map((row) => row.map((cell) => cell.value)),
    truncatedCells: cells.map((row) => (options.flagCutCells ? row.flatMap((cell, i) => (cell.cut ? [i] : [])) : [])),
    hasMore: req.offset + page.length < matched,
    truncated,
    nextOffset: req.offset + page.length,
    total,
    totalIsEstimate,
    totalIsLowerBound,
    durationMs: 9,
    database: req.database,
    table: req.table,
    replica: "ro",
  };
}
