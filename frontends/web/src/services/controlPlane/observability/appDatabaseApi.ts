// Copyright (c) 2025 Reliant Labs

/**
 * `controlplane.v1.AppDatabaseService`, the part dashboards read (C3): a
 * postgres panel's statement run live against one environment's database
 * (QueryDatabasePanel), and the databases a panel may read with the tables and
 * columns the dashboard role may SELECT (ListDashboardDatabases, the editor's
 * schema browser). The only file that knows their wire shape. The data
 * browser's three reads (ListBrowsableDatabases, DescribeDatabase,
 * BrowseTable) are here too, below the panels'.
 *
 * A postgres panel's answer is mapped onto the same PanelResult a sql panel's
 * QueryPanel answer is, so PanelBody draws both the same way: a time series,
 * rows, or one figure.
 *
 * Its post-deploy check and rollback: dev-docs/dashboard-postgres-panels.md.
 */

import { create, toJson, type JsonValue } from "@bufbuild/protobuf";
import { TimestampSchema, ValueSchema, type Timestamp } from "@bufbuild/protobuf/wkt";

import {
  AppDatabaseService,
  BrowsableDatabaseKind,
  BrowseCount,
  BrowseFilterOp,
  DataAccessPolicy,
  DatabaseTableKind,
  type DatabaseForeignKey,
} from "@/gen/controlplane/services/app_database/v1/app_database_pb";
import type { PanelResultPoint } from "@/gen/controlplane/controlplane/v1/shared_pb";

import { getControlPlaneClient } from "../client";
import type { PanelPostgres, PanelResult, TimeWindow } from "./types";

function client() {
  return getControlPlaneClient(AppDatabaseService);
}

function timestamp(ms: number): Timestamp {
  const seconds = Math.floor(ms / 1000);
  return create(TimestampSchema, { seconds: BigInt(seconds), nanos: Math.round((ms - seconds * 1000) * 1e6) });
}

const secondsOf = (ts: Timestamp | undefined) => (ts ? Number(ts.seconds) + ts.nanos / 1e9 : 0);

export interface QueryDatabasePanelInput {
  /** The one control-plane environment whose database the panel reads. */
  environmentId: string;
  window: TimeWindow;
  query: PanelPostgres;
  /** 0 lets the server pick (~120 points). */
  stepSeconds?: number;
  /** Recorded in the audit row and the daily activity event. */
  dashboardRef?: string;
  panelId?: string;
}

/** The answer, plus what only a postgres panel says: which database and replica answered, and how fresh it is. */
export interface DatabasePanelResult extends PanelResult {
  database: string;
  replica: string;
  servedFrom: string;
  durationMs: number;
  /** "rows", "bytes", "series" or "points" when a cap cut the result. */
  truncatedBy: string;
}

function points(list: PanelResultPoint[]) {
  return {
    timestamps: list.map((p) => Math.round(secondsOf(p.time))),
    values: list.map((p) => (p.null || !Number.isFinite(p.value) ? null : p.value)),
  };
}

export async function queryDatabasePanel(req: QueryDatabasePanelInput, options: { signal?: AbortSignal } = {}): Promise<DatabasePanelResult> {
  const res = await client().queryDatabasePanel(
    {
      environmentId: req.environmentId,
      database: req.query.database,
      sql: req.query.sql,
      draw: req.query.draw ?? "",
      timeColumn: req.query.time ?? "",
      valueColumns: req.query.values ?? [],
      groupColumns: req.query.group ?? [],
      from: timestamp(req.window.startMs),
      to: timestamp(req.window.endMs),
      stepSeconds: req.stepSeconds ?? 0,
      dashboardRef: req.dashboardRef ?? "",
      panelId: req.panelId ?? "",
    },
    options.signal ? { signal: options.signal } : undefined
  );
  const line = res.draw === "line";
  return {
    kind: "postgres",
    unit: "",
    stepSeconds: res.stepSeconds,
    bucketSeconds: res.stepSeconds,
    window: res.from && res.to ? { startMs: secondsOf(res.from) * 1000, endMs: secondsOf(res.to) * 1000 } : req.window,
    truncated: res.truncated,
    truncatedBy: res.truncatedBy,
    columns: [...res.columns],
    rows: line
      ? []
      : res.rows.map((r) => {
          const missing = new Set(r.missingValueIndexes);
          return { group: { ...r.group }, values: r.values.map((v, index) => (!missing.has(index) && Number.isFinite(v) ? v : null)) };
        }),
    series: line ? res.series.map((s) => ({ label: s.label, group: { ...s.group }, ...points(s.points), value: Number.isFinite(s.value) ? s.value : null })) : [],
    funnelSteps: [],
    issues: [],
    sessions: [],
    seriesMeta: [],
    seriesTotal: 0,
    warnings: [],
    compiledSql: [],
    database: res.database,
    replica: res.replica,
    servedFrom: res.servedFrom,
    durationMs: Number(res.durationMs),
  };
}

// ── The data browser (ListBrowsableDatabases, DescribeDatabase, BrowseTable) ──
//
// The console's read-only table viewer (components/Forge/Data). Mapped onto
// plain types here so the browser's model speaks strings, not proto enums,
// and a cell is the JSON value it is, not a google.protobuf.Value message.

/** The environment's data_access policy for a read: UNSPECIFIED is refused as deny, so it is its own word. */
export type DataAccessPolicyName = "open" | "audited" | "deny" | "unknown";

/** One database of the environment, as the database selector shows it. */
export interface BrowsableDb {
  name: string;
  kind: "managed" | "external" | "unknown";
  policy: DataAccessPolicyName;
  reachable: boolean;
  /** Why it cannot be reached, for a person. Empty when reachable. */
  unreachableReason: string;
}

export interface DbColumn {
  name: string;
  /** The Postgres type name: "int8", "text", "jsonb", "_text" for an array. */
  pgType: string;
  nullable: boolean;
  comment: string;
  isPrimaryKey: boolean;
}

/** A foreign key, always from the referencing side: columns[i] of schema.table references refColumns[i] of refSchema.refTable. */
export interface DbForeignKey {
  constraint: string;
  schema: string;
  table: string;
  columns: string[];
  refSchema: string;
  refTable: string;
  refColumns: string[];
}

export type DbTableKind = "table" | "view" | "materialized_view" | "foreign_table" | "unknown";

export interface DbTable {
  schema: string;
  name: string;
  kind: DbTableKind;
  /** The planner's estimate; -1 when unknown (a view, a table never analyzed). */
  estimatedRows: number;
  comment: string;
  columns: DbColumn[];
  /** Key order. Empty when the table has none (or is a view): its rows have no address. */
  primaryKey: string[];
  foreignKeys: DbForeignKey[];
  /** Foreign keys of other readable tables that point at this one (the row page's Related). */
  referencedBy: DbForeignKey[];
}

export interface DbDescription {
  database: string;
  tables: DbTable[];
  durationMs: number;
}

export type BrowseOp = "eq" | "neq" | "lt" | "lte" | "gt" | "gte" | "like" | "ilike" | "is_null" | "not_null" | "in";
export type BrowseCountMode = "none" | "estimated" | "exact";

/** What an audited environment needs on every read: confirm_env_id (the environment's own id) and a reason. */
export interface DataAccessConfirmation {
  confirmEnvId: string;
  reason: string;
}

export interface BrowseInput {
  environmentId: string;
  database: string;
  /** Schema-qualified: "public.chats". */
  table: string;
  /** Empty: every readable column, in table order. */
  columns: string[];
  filters: { column: string; op: BrowseOp; values: string[] }[];
  /** Empty: the primary key ascending. */
  sort: { column: string; desc: boolean }[];
  limit: number;
  offset: number;
  count: BrowseCountMode;
  access?: DataAccessConfirmation;
}

/** A cell: the JSON value Postgres's row turned into (null for NULL). */
export type DbCell = JsonValue;

export interface BrowsePage {
  columns: { name: string; pgType: string }[];
  rows: DbCell[][];
  hasMore: boolean;
  /** Null when the request counted nothing. */
  total: number | null;
  totalIsEstimate: boolean;
  totalIsLowerBound: boolean;
  durationMs: number;
  database: string;
  table: string;
  replica: string;
}

const POLICY_NAMES: Record<DataAccessPolicy, DataAccessPolicyName> = {
  [DataAccessPolicy.UNSPECIFIED]: "unknown",
  [DataAccessPolicy.OPEN]: "open",
  [DataAccessPolicy.AUDITED]: "audited",
  [DataAccessPolicy.DENY]: "deny",
};

const DB_KINDS: Record<BrowsableDatabaseKind, BrowsableDb["kind"]> = {
  [BrowsableDatabaseKind.UNSPECIFIED]: "unknown",
  [BrowsableDatabaseKind.MANAGED]: "managed",
  [BrowsableDatabaseKind.EXTERNAL]: "external",
};

const TABLE_KINDS: Record<DatabaseTableKind, DbTableKind> = {
  [DatabaseTableKind.UNSPECIFIED]: "unknown",
  [DatabaseTableKind.TABLE]: "table",
  [DatabaseTableKind.VIEW]: "view",
  [DatabaseTableKind.MATERIALIZED_VIEW]: "materialized_view",
  [DatabaseTableKind.FOREIGN_TABLE]: "foreign_table",
};

const OPS: Record<BrowseOp, BrowseFilterOp> = {
  eq: BrowseFilterOp.EQ,
  neq: BrowseFilterOp.NEQ,
  lt: BrowseFilterOp.LT,
  lte: BrowseFilterOp.LTE,
  gt: BrowseFilterOp.GT,
  gte: BrowseFilterOp.GTE,
  like: BrowseFilterOp.LIKE,
  ilike: BrowseFilterOp.ILIKE,
  is_null: BrowseFilterOp.IS_NULL,
  not_null: BrowseFilterOp.NOT_NULL,
  in: BrowseFilterOp.IN,
};

const COUNTS: Record<BrowseCountMode, BrowseCount> = {
  none: BrowseCount.NONE,
  estimated: BrowseCount.ESTIMATED,
  exact: BrowseCount.EXACT,
};

function accessFields(access: DataAccessConfirmation | undefined) {
  return access ? { confirmEnvId: access.confirmEnvId, reason: access.reason } : {};
}

function foreignKey(fk: DatabaseForeignKey): DbForeignKey {
  return {
    constraint: fk.constraint,
    schema: fk.schema,
    table: fk.table,
    columns: [...fk.columns],
    refSchema: fk.refSchema,
    refTable: fk.refTable,
    refColumns: [...fk.refColumns],
  };
}

export async function listBrowsableDatabases(environmentId: string, options: { signal?: AbortSignal } = {}): Promise<BrowsableDb[]> {
  const res = await client().listBrowsableDatabases({ environmentId }, options.signal ? { signal: options.signal } : undefined);
  return res.databases.map((db) => ({
    name: db.name,
    kind: DB_KINDS[db.kind] ?? "unknown",
    policy: POLICY_NAMES[db.policy] ?? "unknown",
    reachable: db.reachable,
    unreachableReason: db.unreachableReason,
  }));
}

export async function describeDatabase(
  req: { environmentId: string; database: string; access?: DataAccessConfirmation },
  options: { signal?: AbortSignal } = {}
): Promise<DbDescription> {
  const res = await client().describeDatabase(
    { environmentId: req.environmentId, database: req.database, ...accessFields(req.access) },
    options.signal ? { signal: options.signal } : undefined
  );
  return {
    database: res.database,
    durationMs: Number(res.durationMs),
    tables: res.tables.map((t) => ({
      schema: t.schema,
      name: t.name,
      kind: TABLE_KINDS[t.kind] ?? "unknown",
      estimatedRows: Number(t.estimatedRows),
      comment: t.comment,
      columns: t.columns.map((c) => ({ name: c.name, pgType: c.pgType, nullable: c.nullable, comment: c.comment, isPrimaryKey: c.isPrimaryKey })),
      primaryKey: [...t.primaryKey],
      foreignKeys: t.foreignKeys.map(foreignKey),
      referencedBy: t.referencedBy.map(foreignKey),
    })),
  };
}

export async function browseTable(req: BrowseInput, options: { signal?: AbortSignal } = {}): Promise<BrowsePage> {
  const res = await client().browseTable(
    {
      environmentId: req.environmentId,
      database: req.database,
      table: req.table,
      columns: req.columns,
      filters: req.filters.map((f) => ({ column: f.column, op: OPS[f.op], values: f.values })),
      sort: req.sort.map((s) => ({ column: s.column, desc: s.desc })),
      limit: req.limit,
      offset: BigInt(req.offset),
      count: COUNTS[req.count],
      ...accessFields(req.access),
    },
    options.signal ? { signal: options.signal } : undefined
  );
  return {
    columns: res.columns.map((c) => ({ name: c.name, pgType: c.pgType })),
    rows: res.rows.map((r) => r.values.map((v) => toJson(ValueSchema, v))),
    hasMore: res.hasMore,
    total: res.total === undefined ? null : Number(res.total),
    totalIsEstimate: res.totalIsEstimate,
    totalIsLowerBound: res.totalIsLowerBound,
    durationMs: Number(res.durationMs),
    database: res.database,
    table: res.table,
    replica: res.replica,
  };
}

/** One database a postgres panel may read: its limits, and what the dashboard role may SELECT. */
export interface DashboardDatabase {
  name: string;
  rowCap: number;
  timeoutMs: number;
  replica: string;
  tables: { schema: string; name: string; columns: { name: string; type: string }[] }[];
  /** Set when the tables could not be listed (the role is not provisioned yet): the database is still enabled. */
  tablesError: string;
}

export async function listDashboardDatabases(environmentId: string, options: { signal?: AbortSignal } = {}): Promise<DashboardDatabase[]> {
  const res = await client().listDashboardDatabases({ environmentId }, options.signal ? { signal: options.signal } : undefined);
  return res.databases.map((db) => ({
    name: db.name,
    rowCap: db.rowCap,
    timeoutMs: db.timeoutMs,
    replica: db.replica,
    tablesError: db.tablesError,
    tables: db.tables.map((t) => ({ schema: t.schema, name: t.name, columns: t.columns.map((c) => ({ name: c.name, type: c.pgType })) })),
  }));
}
