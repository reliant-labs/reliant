// Copyright (c) 2025 Reliant Labs

/** Synthetic shop inventory, on the public demo's wire transport. No preview
 * adapter, server, identity provider or credential is involved. IDs are local
 * fixture references; the subject is deliberately NOT the database primary key.
 */
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromMs, ValueSchema } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";

import { IdentityState, IdentityUserState } from "@/gen/controlplane/controlplane/v1/identity_pb";
import {
  AppDatabaseService, BrowsableDatabaseKind, BrowseCount, BrowseFilterOp, DataAccessPolicy, DatabaseTableKind,
  DatabaseTableSchema, type BrowseFilter, type BrowseTableRequest,
} from "@/gen/controlplane/services/app_database/v1/app_database_pb";
import { IdentityService } from "@/gen/controlplane/services/identity/v1/identity_pb";

import { completed } from "./demoServiceDefaults";
import { demoEnvId } from "./demoScope";
import { mockService } from "@reliantlabs/forge-web-runtime/mock-transport";

type Cell = string | number | boolean | null;
type Row = Record<string, Cell>;
type Column = { name: string; pgType: string; nullable?: boolean; comment?: string };
const invalid = (message: string): never => { throw new ConnectError(message, Code.InvalidArgument); };
const missing = (): never => { throw new ConnectError("No such sample resource.", Code.NotFound); };

export const DEMO_SUBJECTS = ["subject-maya", "subject-jon", "subject-priya"] as const;
const names = ["Maya Chen", "Jon Rivera", "Priya Shah"];
export const DEMO_USERS: Row[] = DEMO_SUBJECTS.map((subject, i) => ({
  id: `customer-${i + 1}`, identity_subject: subject, email: `${["maya", "jon", "priya"][i]}@example.com`,
  name: names[i], active: i !== 1, created_at: `2026-01-0${i + 1}T12:00:00Z`,
}));
const machines: Row[] = ["packing", "catalog", "support"].map((name, i) => ({ id: `machine-${i + 1}`, owner_id: `customer-${i + 1}`, name: `${name}-station`, connected: i !== 1 }));
const chats: Row[] = Array.from({ length: 123 }, (_, i) => ({
  id: `chat-${String(i + 1).padStart(3, "0")}`, user_id: `customer-${i % 3 + 1}`, machine_id: `machine-${i % 3 + 1}`,
  title: ["Where is my order?", "Restock the linen tote", "Update the product catalog"][i % 3],
  message_count: (i * 7) % 41, archived: i % 5 === 0,
  closed_at: i % 5 === 0 ? "2026-01-10T12:00:00Z" : null,
}));
const inventory: Row[] = Array.from({ length: 32 }, (_, i) => ({
  id: `sku-${String(i + 1).padStart(3, "0")}`, name: `${["Linen tote", "Ceramic mug", "Desk notebook", "Cotton tee"][i % 4]} / ${i + 1}`,
  quantity: i * 3, price_cents: 1200 + i * 100, available: i > 0,
}));

const text = (name: string): Column => ({ name, pgType: "text" });
const fk = (table: string, column: string, refTable: string) => ({ constraint: `${table}_${column}_fkey`, schema: "public", table, columns: [column], refSchema: "public", refTable, refColumns: ["id"] });
const links = [fk("chats", "user_id", "users"), fk("chats", "machine_id", "machines"), fk("machines", "owner_id", "users")];
const definitions: { name: string; rows: Row[]; columns: Column[] }[] = [
  { name: "users", rows: DEMO_USERS, columns: [text("id"), { ...text("identity_subject"), comment: "forge:identity-subject=id" }, text("email"), text("name"), { name: "active", pgType: "boolean" }, { name: "created_at", pgType: "timestamp with time zone" }] },
  { name: "chats", rows: chats, columns: [text("id"), text("user_id"), text("machine_id"), text("title"), { name: "message_count", pgType: "integer" }, { name: "archived", pgType: "boolean" }, { name: "closed_at", pgType: "timestamp with time zone", nullable: true }] },
  { name: "machines", rows: machines, columns: [text("id"), text("owner_id"), text("name"), { name: "connected", pgType: "boolean" }] },
  { name: "inventory", rows: inventory, columns: [text("id"), text("name"), { name: "quantity", pgType: "integer" }, { name: "price_cents", pgType: "integer" }, { name: "available", pgType: "boolean" }] },
];

export function demoDatabaseTables(): MessageInitShape<typeof DatabaseTableSchema>[] {
  return definitions.map(({ name, columns, rows }) => ({
    schema: "public", name, kind: DatabaseTableKind.TABLE, estimatedRows: BigInt(rows.length),
    comment: `Synthetic shop ${name}; sample data only.`, primaryKey: ["id"],
    columns: columns.map((c) => ({ ...c, isPrimaryKey: c.name === "id" })),
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
  if (column.pgType === "integer") {
    if (!/^[+-]?\d+$/.test(value) || !Number.isSafeInteger(Number(value))) invalid("Enter a whole number.");
    return Number(value);
  }
  if (column.pgType === "boolean") {
    if (value !== "true" && value !== "false") invalid("Enter true or false.");
    return value === "true";
  }
  if (column.pgType === "timestamp with time zone") {
    const time = Date.parse(value);
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
    const lhs = c.pgType === "timestamp with time zone" ? Date.parse(String(raw)) : raw;
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
  const table = definitions.find((t) => `public.${t.name}` === req.table) ?? missing();
  if (req.limit < 1 || req.limit > 500 || req.offset < 0n || req.offset > 1_000_000n) invalid("Page bounds are outside the sample limits.");
  const columns = req.columns.length ? req.columns.map((name) => table.columns.find((c) => c.name === name) ?? invalid("Unknown sample column.")) : table.columns;
  const filters = req.filters.map((filter) => predicate(filter, table.columns));
  const sorts = req.sort.length ? req.sort : [{ column: "id", desc: false }];
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
      describeDatabase: (req) => { authorize(req); return { database: "shop", tables: demoDatabaseTables(), durationMs: 1n, auditId: "demo-local-read" }; },
      browseTable: browse,
    })),
    ...completed(IdentityService, mockService(IdentityService, {
      getIdentityStatus: (req) => demoEnvId(req.environmentId) ? { hasInstance: true, state: IdentityState.READY, ownerEnvironmentId: req.environmentId, issuer: "https://identity.shop.example.com", domain: "identity.shop.example.com", public: false } : {},
      getUser: (req) => ({ user: demoEnvId(req.environmentId) ? identityUsers().find((user) => user.userId === req.userId) : undefined }),
      listUsers: (req) => {
        if (!demoEnvId(req.environmentId)) return { users: [] };
        const users = identityUsers().filter((u) => (!req.query || u.email.toLowerCase().includes(req.query.toLowerCase())) && (req.state === undefined || req.state === u.state));
        const offset = req.pageToken ? Number(req.pageToken) : 0;
        if (!Number.isSafeInteger(offset) || offset < 0 || (req.pageToken && String(offset) !== req.pageToken)) invalid("Invalid sample page token.");
        const size = Math.min(Math.max(req.pageSize || 50, 1), 100);
        return { users: users.slice(offset, offset + size), totalSize: users.length, nextPageToken: offset + size < users.length ? String(offset + size) : "", roles: [{ key: "customer" }] };
      },
    })),
  };
}

function identityUsers() {
  return DEMO_USERS.map((row, i) => ({ userId: String(row.identity_subject), email: String(row.email), displayName: String(row.name), state: IdentityUserState.ACTIVE, roles: ["customer"], createdAt: timestampFromMs(Date.parse(String(row.created_at))), lastSignInAt: timestampFromMs(Date.parse(`2026-01-${10 + i}T12:00:00Z`)) }));
}
