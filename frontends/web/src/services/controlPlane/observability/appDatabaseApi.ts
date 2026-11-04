// Copyright (c) 2025 Reliant Labs

/**
 * `controlplane.v1.AppDatabaseService`, the part dashboards read (C3): a
 * postgres panel's statement run live against one environment's database
 * (QueryDatabasePanel), and the databases a panel may read with the tables and
 * columns the dashboard role may SELECT (ListDashboardDatabases, the editor's
 * schema browser). The only file that knows their wire shape.
 *
 * A postgres panel's answer is mapped onto the same PanelResult a sql panel's
 * QueryPanel answer is, so PanelBody draws both the same way: a time series,
 * rows, or one figure.
 *
 * Its post-deploy check and rollback: dev-docs/dashboard-postgres-panels.md.
 */

import { create } from "@bufbuild/protobuf";
import { TimestampSchema, type Timestamp } from "@bufbuild/protobuf/wkt";

import { AppDatabaseService } from "@/gen/controlplane/services/app_database/v1/app_database_pb";
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
