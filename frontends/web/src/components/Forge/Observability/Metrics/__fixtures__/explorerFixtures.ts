// Copyright (c) 2025 Reliant Labs

/**
 * The explorer's fixture API: the area's (createFixtureApi — the metric
 * catalog, a metric's detail, exemplars, dashboards) plus what only the
 * explorer exercises:
 *
 *   group-by     a grouped metrics panel answers one series per value
 *                (status → ok / error / timeout, rpc.method → the orders rpcs…),
 *                so a breakdown has small multiples to draw.
 *   reductions   number, table and bar panels answer values and rows.
 *   explain      QueryPanel{explain} answers the statements a panel compiles
 *                to, shaped like obsquery's (positional {pN:Type} parameters,
 *                the window as fromUnixTimestamp64Nano, the step).
 *   cases        the series cap cutting the result (seriesTotal), warnings,
 *                and a dashboard someone else keeps saving
 *                (version_conflict).
 *
 * Deterministic, so a test and the __preview__ see the same screens.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { CompiledStatement, Panel, PanelResult, QueryPanelRequest, Series } from "@/services/controlPlane/observability/types";

import { createFixtureApi, type FixtureOptions } from "../../__fixtures__/fixtureApi";
import { fixtureMetricEntry, fixtureMetricPanel } from "../../__fixtures__/metricFixtures";

export interface ExplorerFixtureOptions extends FixtureOptions {
  /** The series cap cut the result: QueryPanel reports this many series before the cap. */
  seriesTotal?: number;
  /** Warnings every metrics panel comes back with. */
  warnings?: string[];
  /** The next N SaveDashboard calls lose a race: Aborted, version_conflict. */
  saveConflicts?: number;
}

/** What a group-by field's values are in the fixture app. */
export function fixtureValuesOf(field: string): string[] {
  const key = field.replace(/^(metric|resource)\.attr\./, "");
  switch (key) {
    case "status":
      return ["ok", "error", "timeout"];
    case "rpc.method":
      return ["CreateOrder", "GetOrder", "ListOrders", "CancelOrder", "Checkout"];
    case "service.name":
      return ["api", "worker", "web"];
    default:
      return ["v1", "v2", "v3", "v4"];
  }
}

/** Every combination of the group-by fields' values, at most `cap`. */
function groupsOf(groupBy: readonly string[], cap = 50): Record<string, string>[] {
  let groups: Record<string, string>[] = [{}];
  for (const field of groupBy) groups = groups.flatMap((g) => fixtureValuesOf(field).map((value) => ({ ...g, [field]: value })));
  return groups.slice(0, cap);
}

const round = (v: number) => Math.round(v * 1000) / 1000;

/** A metrics panel's answer over the fixture catalog, grouped and reduced as its kind asks. */
export function explorerMetricPanel(panel: Panel, req: QueryPanelRequest, options: ExplorerFixtureOptions = {}): PanelResult {
  const base = fixtureMetricPanel({ ...panel, kind: "line" }, req.window, req.window.endMs);
  const groupBy = req.groupBy?.length ? req.groupBy : (panel.groupBy ?? []);
  const groups = groupBy.length ? groupsOf(groupBy) : [{}];
  const series: Series[] = base.series.flatMap((s) =>
    groups.map((group, g) => ({ ...s, group, values: s.values.map((v) => (v === null ? null : round(v / (g + 1)))) }))
  );
  const mean = (values: (number | null)[]) => {
    const present = values.filter((v): v is number => v !== null);
    return present.length ? round(present.reduce((a, b) => a + b, 0) / present.length) : null;
  };
  const extras = { seriesTotal: options.seriesTotal ?? groups.length, warnings: options.warnings ?? [], truncated: false };
  if (panel.kind === "number") {
    return { ...base, ...extras, kind: "number", series: series.map((s) => ({ ...s, timestamps: [], values: [], partial: undefined, value: mean(s.values) })) };
  }
  if (panel.kind === "table" || panel.kind === "bar") {
    let rows = groups.map((group, g) => ({ group, values: base.series.map((s) => mean(s.values.map((v) => (v === null ? null : v / (g + 1))))) }));
    if (panel.limit) rows = rows.slice(0, panel.limit);
    return { ...base, ...extras, kind: panel.kind, series: [], columns: panel.series.map((s) => s.label), rows };
  }
  return { ...base, ...extras, series };
}

/** The statements obsquery compiles a metrics panel to, as explain returns them. */
export function explorerExplain(panel: Panel, req: QueryPanelRequest): CompiledStatement[] {
  const startNs = String(BigInt(Math.round(req.window.startMs)) * 1_000_000n);
  const endNs = String(BigInt(Math.round(req.window.endMs)) * 1_000_000n);
  const lookbackNs = String(BigInt(Math.round(req.window.startMs - 120_000)) * 1_000_000n);
  const envs = req.envIds.length ? `[${req.envIds.map((id) => `'${id}'`).join(",")}]` : "";
  return panel.series.flatMap((s) => {
    const entry = fixtureMetricEntry(s.field ?? "");
    const table = entry?.kind === "histogram" ? "otel_metrics_histogram" : entry?.kind === "gauge" ? "otel_metrics_gauge" : "otel_metrics_sum";
    const envCond = envs ? " AND has({p6:Array(String)}, ResourceAttributes['reliant.env.id'])" : "";
    const lookup: CompiledStatement = {
      sql: `SELECT any(MetricUnit) AS unit, max(IsMonotonic) AS monotonic, uniq(cityHash64(ResourceAttributes, Attributes)) AS series\nFROM t_org_fixture.${table}\nWHERE MetricName = {p1:String} AND TimeUnix >= fromUnixTimestamp64Nano({p2:Int64}) AND TimeUnix < fromUnixTimestamp64Nano({p3:Int64})`,
      params: { p1: s.field ?? "", p2: startNs, p3: endNs },
    };
    const data: CompiledStatement = {
      sql: `SELECT toStartOfInterval(TimeUnix, toIntervalSecond({p4:UInt32})) AS t, ${s.agg === "quantile" ? `quantile(${s.quantile ?? 0.95})(Value)` : "sum(Value)"} AS value\nFROM t_org_fixture.${table}\nWHERE MetricName = {p1:String}\n  AND TimeUnix >= fromUnixTimestamp64Nano({p5:Int64}) AND TimeUnix < fromUnixTimestamp64Nano({p3:Int64})${envCond}\nGROUP BY t ORDER BY t`,
      params: { p1: s.field ?? "", p3: endNs, p4: "60", p5: lookbackNs, ...(envs ? { p6: envs } : {}) },
    };
    return [lookup, data];
  });
}

export function createExplorerFixtureApi(options: ExplorerFixtureOptions = {}): ObservabilityApi {
  const base = createFixtureApi(options);
  return {
    ...base,
    queryPanel: async (req) => {
      const metrics = req.panel.signal === "metrics" && req.panel.kind !== "sql" && req.panel.series.some((s) => fixtureMetricEntry(s.field ?? ""));
      if (!metrics || options.failWith !== undefined) return base.queryPanel(req);
      options.onCall?.("QueryPanel", req);
      if (req.explain) {
        return { ...explorerMetricPanel(req.panel, req, options), series: [], rows: [], compiledSql: explorerExplain(req.panel, req) };
      }
      return explorerMetricPanel(req.panel, req, options);
    },
    saveDashboard: async (req) => {
      if ((options.saveConflicts ?? 0) > 0) {
        options.saveConflicts! -= 1;
        options.onCall?.("SaveDashboard", req);
        throw new ConnectError("version_conflict", Code.Aborted);
      }
      return base.saveDashboard(req);
    },
  };
}
