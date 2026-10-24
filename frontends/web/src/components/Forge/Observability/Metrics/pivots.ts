// Copyright (c) 2025 Reliant Labs

/**
 * Pivots into the metrics explorer: the panel each page opens it on, and the
 * link that carries it. Pure, so each pivot's panel is tested on its own.
 *
 *   dashboard  the panel as drawn, plus the dashboard's service variable as a
 *              `where`; the dashboard's compare mode rides along.
 *   trace      the span's service (and its rpc.method when it has one): A =
 *              rpc.server.duration p95, B = rpc.server.calls rate.
 *   alert      the SQL tab on the rule's expr over [firing_at − 1h, now], the
 *              instance's labels as filters where the scope allowlists them.
 *   workload   one container metric (CPU or memory) of the workload, by
 *              container.
 *
 * A pivot the explorer cannot express returns null, and the page hides its
 * link: a link to an error is worse than none.
 */

import { useMemo } from "react";

import type { Panel, Predicate } from "@/services/controlPlane/observability/types";
import { DEFAULT_RANGE, RANGE_PRESETS, type RangePreset } from "@/services/controlPlane/observability/window";

import { useForgeConsole, type ForgeConsole } from "../../Console/consoleContext";
import { consolePath, withSearch } from "../../Console/paths";
import { attributeClass, ENV_KEY, isAttributeKeyShaped, SERVICE_KEY } from "../attributes";
import { rangeMs, type CompareMode } from "../Dashboards/dashboardModel";
import { formatFilterToken } from "../obsFilters";
import { emptyPanel, explorerHref, sqlPanelOf, stateFromSearch, type ExplorerState } from "./model/explorerState";
import { attributeField } from "./seriesView";

const SERVICE_FIELD = SERVICE_KEY;
const WORKLOAD_FIELD = attributeField({ key: "k8s.workload.name", resource: true });
const CONTAINER_FIELD = attributeField({ key: "k8s.container.name", resource: true });
const RPC_METHOD_FIELD = attributeField({ key: "rpc.method", resource: false });

/** The panel kinds the explorer draws; anything else (a heatmap, a funnel) has no explorer view. */
const EXPLORABLE_KINDS = new Set(["line", "bar", "number", "table"]);

// ── Windows ──────────────────────────────────────────────────────────────────

/**
 * The smallest range preset that reaches back to `sinceMs` from `nowMs` (the
 * explorer's window is a preset ending now, never two timestamps). Older than
 * the longest preset: the longest, which is as close as it can get.
 */
export function rangeReaching(sinceMs: number, nowMs: number): RangePreset {
  const needed = Math.max(0, nowMs - sinceMs);
  return RANGE_PRESETS.find((preset) => rangeMs(preset) >= needed) ?? RANGE_PRESETS[RANGE_PRESETS.length - 1]!;
}

// ── Dashboard ────────────────────────────────────────────────────────────────

export interface Pivot {
  panel: Panel;
  extra?: Partial<ExplorerState>;
  /** Scope filter tokens (`key:value`), carried as the explorer's `f`. */
  filters?: string[];
}

/** A dashboard panel as an explorer query; null for a panel that is not an explorable metrics panel. */
export function dashboardPivot(panel: Panel, variables: { service?: string; compare?: CompareMode }): Pivot | null {
  if (panel.signal !== "metrics" || !EXPLORABLE_KINDS.has(panel.kind)) return null;
  if (!panel.series.some((s) => s.field)) return null;
  const where: Predicate[] = [...(panel.where ?? [])];
  if (variables.service && !where.some((p) => p.field === SERVICE_FIELD)) where.push({ field: SERVICE_FIELD, op: "eq", value: variables.service });
  const explore: Panel = { ...panel, id: "explore", where: where.length ? where : undefined };
  const compare = variables.compare && variables.compare !== "off" ? variables.compare : undefined;
  return { panel: explore, extra: compare ? { compare } : undefined };
}

// ── Trace ────────────────────────────────────────────────────────────────────

export const RPC_DURATION = "rpc.server.duration";
export const RPC_CALLS = "rpc.server.calls";

/** One span's service: its latency (p95) and call rate, for that RPC when the span names one. */
export function tracePivot(span: { service: string; attributes: Record<string, string> }): Pivot | null {
  if (!span.service) return null;
  const method = span.attributes["rpc.method"];
  const where: Predicate[] = [{ field: SERVICE_FIELD, op: "eq", value: span.service }];
  if (method) where.push({ field: RPC_METHOD_FIELD, op: "eq", value: method });
  return {
    panel: {
      ...emptyPanel(),
      title: method ? `${span.service} ${method}` : span.service,
      where,
      series: [
        { label: "A", agg: "quantile", quantile: 0.95, field: RPC_DURATION, metricType: "histogram" },
        { label: "B", agg: "rate", field: RPC_CALLS, metricType: "sum" },
      ],
    },
  };
}

// ── Alert ────────────────────────────────────────────────────────────────────

/** Labels an alert's query may name a service by. */
const SERVICE_LABELS = new Set(["service", "service_name", SERVICE_KEY]);

/**
 * The scope filter an alert label becomes, or null: a service label is the
 * service; any other label only when its key is one the scope allowlists
 * (`alertname` and `severity` are the rule's, never telemetry attributes). The
 * environment is the explorer's own scope, not a filter.
 */
export function labelFilter(key: string, value: string): string | null {
  if (!value) return null;
  const attribute = SERVICE_LABELS.has(key) ? SERVICE_KEY : key;
  if (attribute === ENV_KEY || !isAttributeKeyShaped(attribute) || attributeClass(attribute) === undefined) return null;
  return formatFilterToken({ key: attribute, op: "eq", value });
}

/** An alert rule's query on the SQL tab, filtered to the instance's labels. */
export function alertPivot(expr: string, labels: Record<string, string> = {}): Pivot | null {
  const sql = expr.trim();
  if (!sql) return null;
  const filters = Object.keys(labels)
    .sort()
    .map((key) => labelFilter(key, labels[key]!))
    .filter((token): token is string => token !== null);
  return { panel: emptyPanel(), extra: { sqlPanel: sqlPanelOf(sql, "Alert query") }, filters: filters.length ? filters : undefined };
}

/** The range an alert's window [firing_at − 1h, now] needs; one hour when it has no firing time. */
export function alertRange(firingAtMs: number | null | undefined, nowMs: number): RangePreset {
  return rangeReaching(firingAtMs ? firingAtMs - 60 * 60_000 : nowMs - 60 * 60_000, nowMs);
}

// ── Workload ─────────────────────────────────────────────────────────────────

export type ContainerMetric = "cpu" | "memory";

/** The container metrics the workload page charts, as the explorer's metric names. */
const CONTAINER_METRICS: Record<ContainerMetric, { metric: string; title: string }> = {
  cpu: { metric: "container.cpu.usage", title: "CPU" },
  memory: { metric: "container.memory.working_set", title: "Memory" },
};

/** One container metric of a workload, a line per container. */
export function workloadPivot(workload: string, metric: ContainerMetric): Pivot {
  const { metric: field, title } = CONTAINER_METRICS[metric];
  return {
    panel: {
      ...emptyPanel(),
      title: `${title} of ${workload}`,
      series: [{ label: "A", agg: "avg", field, metricType: "gauge" }],
      where: [{ field: WORKLOAD_FIELD, op: "eq", value: workload }],
      groupBy: [CONTAINER_FIELD],
    },
  };
}

// ── The link ─────────────────────────────────────────────────────────────────

export interface ExploreOptions {
  range?: RangePreset;
  /** The environment scope; defaults to the console's. */
  env?: string;
}

/** A pivot's href, or null when the explorer cannot carry it. */
export type ExploreHref = (pivot: Pivot | null, options?: ExploreOptions) => string | null;

/**
 * The link builder for the explorer page at `path`: the model's `explorerHref`
 * plus the console scope (`env`, `range`) and the filters, in the router's own
 * search encoding so the explorer page reads them back. Null when the state
 * does not survive the trip through a URL (a panel or statement over the link
 * budget), which is what "cannot be expressed" means here.
 */
export function exploreHrefFor(path: string, consoleEnv: string): ExploreHref {
  return (pivot, options = {}) => {
    if (!pivot) return null;
    const href = explorerHref(path, pivot.panel, pivot.extra);
    const query = href.includes("?") ? href.slice(href.indexOf("?") + 1) : "";
    const params = Object.fromEntries(new URLSearchParams(query));
    const back = stateFromSearch(params);
    if (pivot.extra?.sqlPanel ? back.sqlPanel?.sql !== pivot.extra.sqlPanel.sql : back.panel.series.length !== pivot.panel.series.length) return null;
    const range = options.range && options.range !== DEFAULT_RANGE ? options.range : undefined;
    return withSearch(path, { ...params, env: options.env ?? consoleEnv, range, f: pivot.filters });
  };
}

/** The console's scope, or null outside the console (a preview, a bare test). */
function useConsoleOrNull(): ForgeConsole | null {
  try {
    return useForgeConsole();
  } catch {
    return null;
  }
}

/** The explorer's link builder inside the forge console; null outside it, so the pivot is not offered. */
export function useExploreHref(): ExploreHref | null {
  const console = useConsoleOrNull();
  const project = console?.forgeProject;
  const env = console?.env;
  return useMemo(() => (project && env ? exploreHrefFor(consolePath(project, { kind: "obs-view", view: "metrics" }), env) : null), [project, env]);
}
