// Copyright (c) 2025 Reliant Labs

/** shop's database (`shop`: customers, products, stock, orders, order_items;
 * shop/database.ts) and identity provider, on the public demo's wire
 * transport. No preview adapter, server, identity provider or credential is
 * involved. A customer's identity subject is deliberately NOT the database
 * primary key.
 */
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromMs, ValueSchema } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";

import { IdentityMailDelivery, IdentityState, IdentityUserState } from "@/gen/controlplane/controlplane/v1/identity_pb";
import {
  AppDatabaseService, BrowsableDatabaseKind, BrowseCount, BrowseFilterOp, DataAccessPolicy, DatabaseTableKind,
  DatabaseTableSchema, type BrowseFilter, type BrowseTableRequest,
} from "@/gen/controlplane/services/app_database/v1/app_database_pb";
import { IdentityService } from "@/gen/controlplane/services/identity/v1/identity_pb";

import { completed } from "./demoServiceDefaults";
import { demoEnvId } from "./demoScope";
import { DEMO_IDENTITY_ROLES, demoAudience, type DemoAccount, type DemoAccountState } from "./shop/audience";
import { shopDatabase, type Cell, type Row } from "./shop/database";
import { DEMO_DB_TABLES, demoNow } from "./shopStory";
import { mockService } from "@reliantlabs/forge-web-runtime/mock-transport";

type Column = { name: string; pgType: string; nullable?: boolean; comment?: string };
const invalid = (message: string): never => { throw new ConnectError(message, Code.InvalidArgument); };
const missing = (): never => { throw new ConnectError("No such sample resource.", Code.NotFound); };

/** A Postgres timestamptz cell (database.ts pgTime) as epoch ms. */
const timeOf = (value: string) => Date.parse(/^\d{4}-\d\d-\d\d \d/.test(value) ? value.replace(" ", "T").replace(/\+00$/, "Z") : value);

const NULLABLE = new Set(["orders.paid_at"]);
const COMMENTS: Record<string, string> = {
  "customers.identity_subject": "forge:identity-subject=id",
  "stock.reserved": "Units held for paid orders that have not shipped.",
  "orders.state": "pending, paid, payment_failed, shipped, delivered or cancelled.",
};
const PRIMARY_KEY: Record<string, string> = { stock: "sku" };

const fk = (table: string, column: string, refTable: string, refColumn = "id") => ({ constraint: `${table}_${column}_fkey`, schema: "public", table, columns: [column], refSchema: "public", refTable, refColumns: [refColumn] });
const links = [fk("orders", "customer_id", "customers"), fk("order_items", "order_id", "orders"), fk("order_items", "sku", "products", "sku"), fk("stock", "sku", "products", "sku")];

/** The `shop` database's tables in one environment, as of the page load (database.ts). */
const cache = new Map<string, { name: string; rows: Row[]; columns: Column[]; pk: string }[]>();
function definitionsOf(env: string) {
  let defs = cache.get(env);
  if (!defs) {
    const db = shopDatabase(env, demoNow());
    defs = DEMO_DB_TABLES.map((t) => ({
      name: t.name,
      rows: db[t.name],
      pk: PRIMARY_KEY[t.name] ?? "id",
      columns: t.columns.map(([name, pgType]) => ({ name, pgType, ...(NULLABLE.has(`${t.name}.${name}`) ? { nullable: true } : {}), ...(COMMENTS[`${t.name}.${name}`] ? { comment: COMMENTS[`${t.name}.${name}`] } : {}) })),
    }));
    cache.set(env, defs);
  }
  return defs;
}

/** The identity provider's users: shop's customers who signed up (the subject is NOT the database primary key). */
export function demoIdentityUsers(env = "env-dev") {
  return definitionsOf(env).find((t) => t.name === "customers")!.rows;
}
export const DEMO_SUBJECTS = demoIdentityUsers().slice(0, 3).map((row) => String(row.identity_subject));

export function demoDatabaseTables(env = "env-dev"): MessageInitShape<typeof DatabaseTableSchema>[] {
  return definitionsOf(env).map(({ name, columns, rows, pk }) => ({
    schema: "public", name, kind: DatabaseTableKind.TABLE, estimatedRows: BigInt(rows.length),
    comment: `shop's ${name.replace("_", " ")}. Sample data.`, primaryKey: [pk],
    columns: columns.map((c) => ({ ...c, isPrimaryKey: c.name === pk })),
    foreignKeys: links.filter((key) => key.table === name), referencedBy: links.filter((key) => key.refTable === name),
  }));
}

/** Mirrors the audited production confirmation UX; it records nothing remotely. */
function authorize(req: { environmentId: string; database: string; confirmEnvId?: string; reason?: string }) {
  if (!demoEnvId(req.environmentId) || req.database !== "shop") missing();
  if (req.environmentId === "env-prod" && (req.confirmEnvId !== req.environmentId || !req.reason?.trim())) {
    throw new ConnectError("Confirm prod and enter a reason to read this sample database. No real audit request is sent.", Code.PermissionDenied);
  }
}

function typed(value: string, column: Column): Exclude<Cell, null> {
  if (column.pgType === "integer" || column.pgType === "bigint") {
    if (!/^[+-]?\d+$/.test(value) || !Number.isSafeInteger(Number(value))) invalid("Enter a whole number.");
    return Number(value);
  }
  if (column.pgType === "boolean") {
    if (value !== "true" && value !== "false") invalid("Enter true or false.");
    return value === "true";
  }
  if (column.pgType === "timestamp with time zone") {
    const time = timeOf(value);
    if (!Number.isFinite(time)) invalid("Enter a timestamp.");
    return time;
  }
  return value;
}

/** SQL LIKE wildcards, including backslash escapes, without evaluating SQL. */
function like(pattern: string, insensitive: boolean): RegExp {
  let out = "^";
  for (let i = 0; i < pattern.length; i++) {
    const ch = pattern[i];
    if (ch === "\\") {
      if (++i === pattern.length) invalid("LIKE pattern ends with an escape.");
      out += pattern[i].replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    } else out += ch === "%" ? "[\\s\\S]*" : ch === "_" ? "[\\s\\S]" : ch.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  }
  return new RegExp(`${out}$`, insensitive ? "i" : "");
}

function predicate(filter: BrowseFilter, columns: Column[]): (row: Row) => boolean {
  const c = columns.find((candidate) => candidate.name === filter.column) ?? invalid("Unknown sample column.");
  const { op, values } = filter;
  if (op === BrowseFilterOp.IS_NULL || op === BrowseFilterOp.NOT_NULL) {
    if (values.length) invalid("Null checks take no values.");
    return (row) => op === BrowseFilterOp.IS_NULL ? row[c.name] === null : row[c.name] !== null;
  }
  if (op < BrowseFilterOp.EQ || op > BrowseFilterOp.IN || !values.length || (op !== BrowseFilterOp.IN && values.length !== 1)) invalid("Invalid structured filter.");
  if (op === BrowseFilterOp.LIKE || op === BrowseFilterOp.ILIKE) {
    if (c.pgType !== "text") invalid("LIKE needs a text column.");
    const pattern = like(values[0], op === BrowseFilterOp.ILIKE);
    return (row) => row[c.name] !== null && pattern.test(String(row[c.name]));
  }
  const rhs = values.map((value) => typed(value, c));
  return (row) => {
    const raw = row[c.name];
    if (raw === null) return false; // SQL's UNKNOWN, including NEQ / IN.
    const lhs = c.pgType === "timestamp with time zone" ? timeOf(String(raw)) : raw;
    switch (op) {
      case BrowseFilterOp.EQ: return lhs === rhs[0];
      case BrowseFilterOp.NEQ: return lhs !== rhs[0];
      case BrowseFilterOp.LT: return lhs < rhs[0];
      case BrowseFilterOp.LTE: return lhs <= rhs[0];
      case BrowseFilterOp.GT: return lhs > rhs[0];
      case BrowseFilterOp.GTE: return lhs >= rhs[0];
      case BrowseFilterOp.IN: return rhs.includes(lhs);
      default: return invalid("Unsupported structured filter.");
    }
  };
}

function browse(req: BrowseTableRequest) {
  authorize(req);
  const table = definitionsOf(req.environmentId).find((t) => `public.${t.name}` === req.table) ?? missing();
  if (req.limit < 1 || req.limit > 500 || req.offset < 0n || req.offset > 1_000_000n) invalid("Page bounds are outside the sample limits.");
  const columns = req.columns.length ? req.columns.map((name) => table.columns.find((c) => c.name === name) ?? invalid("Unknown sample column.")) : table.columns;
  const filters = req.filters.map((filter) => predicate(filter, table.columns));
  const sorts = req.sort.length ? req.sort : [{ column: table.pk, desc: false }];
  for (const sort of sorts) if (!table.columns.some((c) => c.name === sort.column)) invalid("Unknown sort column.");
  const matching = table.rows.filter((row) => filters.every((test) => test(row))).sort((a, b) => {
    for (const sort of sorts) {
      const av = a[sort.column]; const bv = b[sort.column];
      const cmp = av === bv ? 0 : av === null ? 1 : bv === null ? -1 : av < bv ? -1 : 1;
      if (cmp) return sort.desc ? -cmp : cmp;
    }
    return 0;
  });
  const offset = Number(req.offset);
  const rows = matching.slice(offset, offset + req.limit);
  return {
    database: "shop", table: req.table, columns: columns.map(({ name, pgType }) => ({ name, pgType })),
    rows: rows.map((row) => ({ values: columns.map((c) => create(ValueSchema, { kind: row[c.name] === null ? { case: "nullValue", value: 0 } : typeof row[c.name] === "number" ? { case: "numberValue", value: row[c.name] as number } : typeof row[c.name] === "boolean" ? { case: "boolValue", value: row[c.name] as boolean } : { case: "stringValue", value: String(row[c.name]) } })) })),
    hasMore: offset + rows.length < matching.length, nextOffset: BigInt(offset + rows.length),
    total: req.count === BrowseCount.EXACT || req.count === BrowseCount.ESTIMATED ? BigInt(matching.length) : undefined,
    totalIsEstimate: req.count === BrowseCount.ESTIMATED, replica: "ro", durationMs: 2n, auditId: "demo-local-read",
  };
}

export function inventoryScenarioHandlers() {
  return {
    ...completed(AppDatabaseService, mockService(AppDatabaseService, {
      listBrowsableDatabases: (req) => ({ databases: demoEnvId(req.environmentId) ? [{ name: "shop", kind: BrowsableDatabaseKind.MANAGED, policy: req.environmentId === "env-prod" ? DataAccessPolicy.AUDITED : DataAccessPolicy.OPEN, reachable: true }] : [] }),
      describeDatabase: (req) => { authorize(req); return { database: "shop", tables: demoDatabaseTables(req.environmentId), durationMs: 1n, auditId: "demo-local-read" }; },
      browseTable: browse,
    })),
    ...completed(IdentityService, mockService(IdentityService, {
      getIdentityStatus: (req) => (demoEnvId(req.environmentId) ? identityStatus(req.environmentId) : {}),
      getUser: (req) => ({ user: demoEnvId(req.environmentId) ? identityUsers(req.environmentId).find((user) => user.userId === req.userId) : undefined }),
      listUsers: (req) => {
        if (!demoEnvId(req.environmentId)) return { users: [] };
        const query = req.query?.toLowerCase();
        const users = identityUsers(req.environmentId).filter((u) => (!query || u.email.toLowerCase().includes(query)) && (req.state === undefined || req.state === u.state));
        const offset = req.pageToken ? Number(req.pageToken) : 0;
        if (!Number.isSafeInteger(offset) || offset < 0 || (req.pageToken && String(offset) !== req.pageToken)) invalid("Invalid sample page token.");
        const size = Math.min(Math.max(req.pageSize || 50, 1), 100);
        return {
          users: users.slice(offset, offset + size),
          totalSize: users.length,
          nextPageToken: offset + size < users.length ? String(offset + size) : "",
          roles: DEMO_IDENTITY_ROLES.map((role) => ({ ...role })),
        };
      },
    })),
  };
}

const USER_STATE: Record<DemoAccountState, IdentityUserState> = {
  active: IdentityUserState.ACTIVE,
  invited: IdentityUserState.INVITED,
  locked: IdentityUserState.LOCKED,
  disabled: IdentityUserState.DISABLED,
};

function toUser(account: DemoAccount) {
  return {
    userId: account.id, email: account.email, displayName: account.name,
    state: USER_STATE[account.state], roles: account.roles,
    createdAt: timestampFromMs(account.createdAtMs),
    lastSignInAt: account.lastSignInAtMs === null ? undefined : timestampFromMs(account.lastSignInAtMs),
  };
}

/** The identity provider's users: shop's audience (shop/audience.ts), the one population every page counts. */
const userCache = new Map<string, ReturnType<typeof toUser>[]>();
function identityUsers(env: string) {
  const key = `${env}@${demoNow()}`;
  let users = userCache.get(key);
  if (!users) {
    users = demoAudience(env).map(toUser);
    userCache.set(key, users);
  }
  return users;
}

/**
 * Each environment's sign-in service: ready everywhere (the incident is the
 * payment provider's, not sign-in's). prod signs customers in on shop's own
 * domain and mails them, as staging does through its own relay;
 * dev sends no email, so its invitations hand back a link.
 */
function identityStatus(env: string) {
  const now = demoNow();
  const slug = env === "env-prod" ? "" : env === "env-staging" ? "shop-staging-7f3k" : "shop-dev-2m9q";
  const issuer = slug ? `https://${slug}.reliantidentity.com` : "https://accounts.shop.example.com";
  return {
    hasInstance: true,
    state: IdentityState.READY,
    ownerEnvironmentId: env,
    issuer,
    domain: slug ? "" : "accounts.shop.example.com",
    public: true,
    apps: [{ name: "shop-web", clientId: env === "env-prod" ? "291846035172864391" : env === "env-staging" ? "291846035172864517" : "291846035172864602", brokerExpiresAt: timestampFromMs(now + 5 * 3_600_000 + 11 * 60_000) }],
    lastReconciledAt: timestampFromMs(now - 2 * 60_000 - 17_000),
    callerCanManage: true,
    exportEnabled: true,
    mailDelivery: env === "env-dev" ? IdentityMailDelivery.NONE : IdentityMailDelivery.SMTP,
  };
}
