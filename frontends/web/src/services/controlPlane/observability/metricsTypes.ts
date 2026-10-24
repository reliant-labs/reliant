// Copyright (c) 2025 Reliant Labs

/**
 * The metrics explorer's model of control-plane's metric catalog, metric
 * detail and exemplars (ObservabilityService.ListMetrics / GetMetric /
 * ListMetricExemplars), and of the per-series metadata QueryPanel returns for a
 * metrics panel. metricsApi.ts is the only file that maps the wire into these.
 */

/** The OTel kind a metric is stored as. A newer server's kind this build does not know is mapped to `unknown`. */
export type MetricKind = "gauge" | "sum" | "histogram" | "exp_histogram" | "summary" | "unknown";

export type MetricTemporality = "delta" | "cumulative" | "unknown";

/** Where the catalog lists a metric. Mirrors obsquery's builtin rule (see catalogGroup). */
export type MetricGroup = "declared" | "runtime" | "infra" | "other";

export interface MetricCatalogEntry {
  name: string;
  kind: MetricKind;
  unit: string;
  description: string;
  /** Sums only: a counter (true) or an up-down counter (false). */
  monotonic: boolean;
  temporality: MetricTemporality;
  /** Up to 10 services that emit it. */
  services: string[];
  serviceCount: number;
  /** Newest data point in the catalog range; null for a declared metric never seen. */
  lastSeenMs: number | null;
  seriesEstimate: number;
  /** false: the evaluator cannot run this kind yet. */
  queryable: boolean;
  builtin: boolean;
  declared: boolean;
  seen: boolean;
  /** "stable" | "experimental" | "deprecated", empty when not declared. */
  stability: string;
  group: MetricGroup;
}

export interface MetricAttribute {
  key: string;
  /** A resource attribute (service.name, k8s.*), not a data-point attribute. */
  resource: boolean;
  distinctEstimate: number;
  /** The declared closed value set; empty when open. */
  enumerated: string[];
  /** Grouping by it could exceed the series cap. */
  highCardinality: boolean;
}

export interface MetricDetail {
  info: MetricCatalogEntry;
  /** Estimated export interval: the floor of a useful step. 0 when unknown. */
  intervalSeconds: number;
  mixedTemporality: boolean;
  attributes: MetricAttribute[];
  /** Keys worth grouping by, best first. */
  suggestedBreakdowns: string[];
}

export interface MetricExemplar {
  timeMs: number;
  value: number;
  traceId: string;
  spanId: string;
  attributes: Record<string, string>;
  /** false: the trace was sampled out and the trace view would be empty. */
  traceStored: boolean;
}

/** What one series of a metrics panel measures — enough to label and format it without re-deriving the metric's semantics. */
export interface SeriesMeta {
  /** The series' label: matches `Series.label` of the entries it describes. */
  label: string;
  /** Empty for a formula entry. */
  metric: string;
  kind: MetricKind;
  /** The dashspec agg after defaulting (rate, increase, sum, avg, min, max, last, count, quantile). */
  agg: string;
  /** In (0, 1] for a quantile; undefined otherwise. */
  quantile?: number;
  /** `<unit>/s` for rate, `{count}` for count, else the metric's unit. */
  valueUnit: string;
}

export interface ListMetricCatalogRequest {
  name?: string;
  limit?: number;
}

export interface ListMetricCatalogResult {
  metrics: MetricCatalogEntry[];
  truncated: boolean;
}

export interface GetMetricRequest {
  name: string;
  kind?: MetricKind;
}

export interface ListMetricExemplarsRequest {
  name: string;
  kind?: MetricKind;
  where?: { field: string; op: string; value?: string; values?: string[] }[];
  /** One series group: attribute key → value. */
  group?: Record<string, string>;
  /** The bucket the user clicked, inside the scope's window. Absent: the whole window. */
  range?: { startMs: number; endMs: number };
  /** Only exemplars at or above this value (the tail of a latency histogram). */
  minValue?: number;
  /** At most 20. */
  limit?: number;
}

/** Prefixes obsquery's IsBuiltinMetric treats as forge's own runtime metrics (rpc., go., forge.). */
const BUILTIN_PREFIXES = ["rpc.", "go.", "forge."] as const;
/** Node-agent and kubelet telemetry (k8s.*, container.*): what the catalog files under Infra. */
const INFRA_PREFIXES = ["k8s.", "container."] as const;

/**
 * Which catalog group a metric belongs to: a declared one first (the release's
 * own schema), then forge's built-ins, then infrastructure, then everything
 * else. Mirrors obsquery's IsBuiltinMetric (pkg/obsquery/catalog.go): the
 * `rpc.`, `go.` and `forge.` prefixes, matched on the dot so `rpcx.thing` is not
 * one, and followed by a name — a bare `rpc.` is not a built-in.
 */
export function catalogGroup(name: string, declared: boolean): MetricGroup {
  if (BUILTIN_PREFIXES.some((p) => name.startsWith(p) && name.length > p.length)) return "runtime";
  if (declared) return "declared";
  if (INFRA_PREFIXES.some((p) => name.startsWith(p))) return "infra";
  return "other";
}

