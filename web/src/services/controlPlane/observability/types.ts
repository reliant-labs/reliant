// Copyright (c) 2025 Reliant Labs

/**
 * The observability console's own model — what the views render.
 *
 * These are NOT the wire types. The wire is control-plane's
 * `controlplane.v1.ObservabilityService` (proto-vendor/controlplane), and
 * `./api.ts` is the one file that maps it into these shapes. The split is
 * deliberate: the views, their tests and their fixtures speak this model, so a
 * proto field rename or a re-shaped response is a change in api.ts alone.
 *
 * Times are epoch MILLISECONDS as a float. ClickHouse keeps nanoseconds, and a
 * double carries ~0.2µs of fraction at today's epoch, which is enough to order
 * and draw sub-millisecond spans in a waterfall without a bigint in every
 * component.
 */

/** A resolved, absolute time window. `endMs` is exclusive. */
export interface TimeWindow {
  startMs: number;
  endMs: number;
}

/**
 * What every request reads (control-plane QueryScope, contract K1): one
 * organization, optionally one forge project, and any set of its environments.
 * The environment is a FILTER, not a place — an env page is this scope with
 * one id in it, the project's Observability area the same scope with the
 * selection (empty = every environment the caller can see).
 *
 * The org is the caller's current one; control-plane verifies membership, and
 * that every env id belongs to it, server-side, and runs the query as that
 * org's own ClickHouse user.
 */
export interface ObsScope {
  orgId: string;
  /** The forge project (`deploy_environments.project`). Undefined: no project narrowing. */
  projectId?: string;
  /** control-plane environment ids (stamped on telemetry as `reliant.env.id`). Empty: every environment. */
  envIds: string[];
  /** ANDed attribute predicates over allowlisted keys (attributes.ts). */
  filters: AttributeFilter[];
}

/** The operators QueryScope.filters accepts (control-plane AttributeOp). */
export type AttributeOp = "eq" | "neq" | "exists" | "in" | "contains";

export interface AttributeFilter {
  key: string;
  op: AttributeOp;
  /** eq, neq, contains. */
  value?: string;
  /** in. */
  values?: string[];
}

/**
 * Reliant's own operator telemetry (daemon and platform logs) lives in
 * Reliant's org only. Rows of it carry this, and are badged so an operator
 * never mistakes platform data for the app's own.
 */
export interface Provenance {
  /** `daemon`, `platform`, or empty for the org's own data. */
  source: string;
  operatorData: boolean;
}

// ── Logs ─────────────────────────────────────────────────────────────────────

export type Severity = "trace" | "debug" | "info" | "warn" | "error" | "fatal" | "unknown";

/** The severities a user can filter by, most to least verbose. */
export const SEVERITIES: readonly Severity[] = ["trace", "debug", "info", "warn", "error", "fatal"];

export interface LogRecord extends Provenance {
  /** Stable within a result set — the row's key, never shown. */
  id: string;
  timeMs: number;
  severity: Severity;
  /** What the producer wrote (`WARNING`, `E`, `level=warn`…), for display. */
  severityText: string;
  service: string;
  body: string;
  /** Empty when the record was written outside a span. */
  traceId: string;
  spanId: string;
  attributes: Record<string, string>;
  resource: Record<string, string>;
}

export interface SearchLogsRequest extends ObsScope {
  window: TimeWindow;
  /** The free-text box, sent verbatim. The SERVER parses it (grammar in api.ts). */
  query: string;
  services: string[];
  severities: Severity[];
  /** Only the records of this trace (the "logs for this trace/span" links). */
  traceId?: string;
  spanId?: string;
  limit: number;
  cursor?: string;
}

export interface SearchLogsResult {
  records: LogRecord[];
  /** Empty when there is nothing older. */
  nextCursor: string;
}

// ── Traces ───────────────────────────────────────────────────────────────────

export type SpanStatus = "ok" | "error" | "unset";

export interface TraceSummary extends Provenance {
  traceId: string;
  rootService: string;
  rootOperation: string;
  startMs: number;
  /** The whole trace, first span start to last span end. */
  durationMs: number;
  /** The root span alone — the request as its caller experienced it. */
  rootDurationMs: number;
  spanCount: number;
  errorCount: number;
  services: string[];
}

/** Rate, errors and duration over every trace the filters matched in the window — not just one page. */
export interface TraceRed {
  traceCount: number;
  errorTraceCount: number;
  ratePerSecond: number;
  /** Fraction of traces, 0..1. */
  errorRate: number;
  p50Ms: number;
  p95Ms: number;
  p99Ms: number;
}

export interface ListTracesRequest extends ObsScope {
  window: TimeWindow;
  /** Substring of the root span's name. */
  rootSpanName: string;
  services: string[];
  errorsOnly: boolean;
  minDurationMs: number;
  limit: number;
  cursor?: string;
  /** Up to 3 keys: the response gains one RED summary per group. */
  groupBy?: string[];
}

/** One group's RED summary — "prod" against "staging", "v1.7" against "v1.8". */
export interface TraceRedGroup {
  /** group-by key → value. */
  group: Record<string, string>;
  red: TraceRed;
}

export interface ListTracesResult {
  traces: TraceSummary[];
  nextCursor: string;
  red: TraceRed | null;
  /** Empty unless the request grouped. */
  redGroups: TraceRedGroup[];
}

export interface SpanEvent {
  timeMs: number;
  name: string;
  attributes: Record<string, string>;
}

export interface Span extends Provenance {
  spanId: string;
  /** Empty for a root. A parent that was not fetched (sampling, a window edge) makes this span a root too. */
  parentSpanId: string;
  name: string;
  service: string;
  /** server | client | internal | producer | consumer. */
  kind: string;
  startMs: number;
  durationMs: number;
  status: SpanStatus;
  statusMessage: string;
  attributes: Record<string, string>;
  resource: Record<string, string>;
  events: SpanEvent[];
}

export interface Trace {
  traceId: string;
  spans: Span[];
  /** The server capped the span count; the waterfall is a lower bound. */
  truncated: boolean;
}

// ── Panels ───────────────────────────────────────────────────────────────────

/** One line. `timestamps` are epoch SECONDS (uPlot's x unit); a null value is a gap. */
export interface Series {
  label: string;
  /** For a grouped panel, the group-by field → value (`{"service.name": "api"}`). */
  group: Record<string, string>;
  timestamps: number[];
  values: (number | null)[];
  /** A number panel's value. */
  value: number | null;
}

export interface QueryPanelRequest extends ObsScope {
  window: TimeWindow;
  panel: Panel;
  /** The dashboard's service filter — applied by the server as scope.services. */
  services: string[];
  /** 0 lets the server pick (~120 buckets). */
  stepSeconds: number;
  /** Non-empty REPLACES the panel's own group-by (the in-place control). Max 3. */
  groupBy?: string[];
}

/**
 * One panel's data, computed server-side from its intent (QueryPanel):
 * `series` for line panels (one per series × group; a ratio panel, one per
 * group) and number panels (`value`), `columns`/`rows` for tables.
 */
export interface PanelResult {
  kind: string;
  unit: string;
  series: Series[];
  columns: string[];
  rows: { group: Record<string, string>; values: (number | null)[] }[];
  stepSeconds: number;
  /** A cap cut the result (50 series, 200 rows). */
  truncated: boolean;
}

// ── Service overview ─────────────────────────────────────────────────────────

export interface ServiceHealth extends Provenance {
  name: string;
  /** For a grouped overview, this row's group-by key → value; empty otherwise. */
  group: Record<string, string>;
  requestCount: number;
  /** Requests per second over the window. */
  requestRate: number;
  errorCount: number;
  /** Failed / all, in [0, 1]. */
  errorRate: number;
  p50Ms: number;
  p95Ms: number;
  p99Ms: number;
  /** Error-level log records in the window. */
  errorLogCount: number;
  /** Shared x axis (epoch seconds) for the trend series. */
  timestamps: number[];
  /** Requests per second in each trend bucket. */
  rateSeries: (number | null)[];
  /** Errors / requests in each bucket; null where a bucket had no requests. */
  errorRateSeries: (number | null)[];
  /** Raw counts per bucket, for summing across services. */
  requestTrend: (number | null)[];
  errorTrend: (number | null)[];
}

export interface ServiceOverview {
  services: ServiceHealth[];
  trendStepSeconds: number;
}

// ── Dashboards (obsdash.Spec, forge.dashboards/v1) ───────────────────────────

/** A predicate over a semantic OTel field — the obsdash vocabulary (forge/internal/obsdash/spec.go). */
export interface Predicate {
  field: string;
  op: string;
  value?: string;
  values?: string[];
}

export interface SeriesIntent {
  label: string;
  /** count | count_distinct | sum | avg | max | quantile | last */
  agg: string;
  quantile?: number;
  field?: string;
  metricType?: string;
  where?: Predicate[];
}

/** traces | metrics | logs */
export type Signal = string;
/** number | line | table | search */
export type PanelKind = string;
export type Unit = "" | "count" | "ms" | "bytes" | "percent";

export interface Panel {
  id: string;
  title: string;
  description?: string;
  kind: PanelKind;
  signal: Signal;
  series: SeriesIntent[];
  ratio?: boolean;
  where?: Predicate[];
  groupBy?: string[];
  columns?: string[];
  unit?: Unit;
  /** Grid columns out of 24. */
  width?: number;
  height?: number;
}

export interface DashboardFilter {
  id: string;
  label: string;
  field: string;
  /** The telemetry the dropdown reads values from; empty is traces. */
  signal?: Signal;
}

export interface Dashboard {
  id: string;
  title: string;
  description?: string;
  filters: DashboardFilter[];
  panels: Panel[];
}

export interface DashboardSpec {
  version: string;
  project: string;
  dashboards: Dashboard[];
  /** "synthesized": built from the services seen in the data. "release": the environment's own generated spec. */
  source: string;
}

// ── Profiles ─────────────────────────────────────────────────────────────────

export interface ProfileType {
  id: string;
  label: string;
  /** What one sample measures, for captions ("CPU time", "bytes allocated"). */
  unit: string;
}

/**
 * The profile types forge's runtime producer emits for a Go service
 * (research/clickstack-review/profiles-design.md §1): the picker's labels, and
 * its options until the server says which types a service actually has.
 */
export const DEFAULT_PROFILE_TYPES: readonly ProfileType[] = [
  { id: "cpu", label: "CPU", unit: "CPU time" },
  { id: "alloc_space", label: "Allocated bytes", unit: "bytes allocated" },
  { id: "alloc_objects", label: "Allocated objects", unit: "objects allocated" },
  { id: "inuse_space", label: "In-use bytes", unit: "bytes in use" },
  { id: "inuse_objects", label: "In-use objects", unit: "objects in use" },
  { id: "goroutine", label: "Goroutines", unit: "goroutines" },
];

export interface GetFlamegraphRequest extends ObsScope {
  window: TimeWindow;
  service: string;
  profileType: string;
  /** Narrow to the samples whose goroutine served this trace (the raw tier keeps 72 hours). */
  traceId?: string;
}

export interface Flamegraph {
  /** A speedscope file (https://www.speedscope.app/file-format-schema.json), serialized. */
  speedscopeJson: string;
  totalValue: number;
  /** nanoseconds, bytes, count… */
  unit: string;
  /** 0 means there is nothing to draw — rendered as "no samples", not as an empty canvas. */
  stackCount: number;
  /** The types this service has in the window, for the picker. */
  availableProfileTypes: string[];
  /** The heaviest stacks were kept and the rest cut. */
  truncated: boolean;
}

// ── Usage and billing ────────────────────────────────────────────────────────

/** What the gateway does with the org's telemetry right now. */
export type QuotaState = "ok" | "over_100" | "over_150" | "unknown";

export type UsageSignal = "logs" | "traces" | "metrics" | "profiles";

/**
 * The org's month-to-date observability usage against its allowance
 * (GetObservabilityUsage). Bytes are plain numbers: the largest figure is a
 * few TiB, far below 2^53. 1 GB = 2^30 bytes, as everywhere in billing.
 */
export interface ObservabilityUsage {
  periodStartMs: number;
  periodEndMs: number;
  /** The end of the last fully metered hour; null before the first hour is metered. */
  measuredThroughMs: number | null;
  usedBytes: number;
  allowanceBytes: number;
  overageCents: number;
  projectedBytes: number;
  projectedOverageCents: number;
  /** Cents per GB above the allowance, as the server's decimal string. */
  rateCentsPerGb: string;
  /** What the gateway enforces. */
  state: QuotaState;
  /** What usage alone implies; differs from state for an org that pays or is exempt. */
  usageState: QuotaState;
  funded: boolean;
  exempt: boolean;
  spendCapCents: number;
  spendCapReached: boolean;
  browserBytes: number;
  browserBilledBytes: number;
  browserCapBytes: number;
  /** Customer metadata: per-workload container data. Billed, and included in usedBytes. */
  metadataBytes: number;
  bySignal: { signal: UsageSignal; billedBytes: number; storedBytes: number; rows: number }[];
  byService: { service: string; signal: UsageSignal; billedBytes: number }[];
}

// ── Attribute values (filter autocomplete) ───────────────────────────────────

export interface ListAttributeValuesRequest extends ObsScope {
  window: TimeWindow;
  key: string;
  /** What has been typed so far. */
  prefix: string;
  /** Narrow to one signal; unset reads resource keys across all three. */
  signal?: "traces" | "logs" | "metrics";
  limit?: number;
}

export interface AttributeValueCount {
  value: string;
  count: number;
}

export interface ListAttributeValuesResult {
  /** Most frequent first. */
  values: AttributeValueCount[];
  truncated: boolean;
}

// ── Workloads: customer metadata (owner decision 4, contract K2) ─────────────
//
// "Customer metadata" is what the node agent reports about the customer's OWN
// workloads — images, restarts, resource use, Kubernetes events — stamped with
// the tenant from namespace labels. It is billed and opt-out per environment.
// "Infrastructure" (nodes, the cluster itself) is ours and never shown here.

/** CPU in cores, memory in bytes (working set). The worst replica's figures. */
export interface ResourceUsage {
  latest: number;
  p95: number;
  /** Fractions; null when the container sets no limit/request or the agent does not report it. */
  limitUtilizationLatest: number | null;
  limitUtilizationP95: number | null;
  requestUtilizationLatest: number | null;
  requestUtilizationP95: number | null;
  limit: number | null;
  request: number | null;
}

export interface WorkloadContainer {
  container: string;
  imageName: string;
  imageTag: string;
  restartsInWindow: number;
  /** OOMKilled, Error, Completed… Empty when it never terminated. */
  lastTerminatedReason: string;
  lastTerminatedAtMs: number | null;
  cpu: ResourceUsage | null;
  memory: ResourceUsage | null;
}

export interface WorkloadEvent {
  timeMs: number;
  envId: string;
  /** Normal | Warning. */
  type: string;
  /** BackOff, Failed, Evicted, OOMKilling, Pulled… */
  reason: string;
  message: string;
  count: number;
  objectKind: string;
  objectName: string;
  container: string;
}

export interface Workload {
  envId: string;
  envName: string;
  workload: string;
  namespace: string;
  serviceVersion: string;
  replicasObserved: number;
  restartsInWindow: number;
  containers: WorkloadContainer[];
  latestWarning: WorkloadEvent | null;
}

export interface ListWorkloadsResult {
  /** By env, then name. */
  workloads: Workload[];
  truncated: boolean;
}

export type WorkloadMetric =
  | "cpu_usage"
  | "cpu_limit_utilization"
  | "cpu_request_utilization"
  | "memory_working_set"
  | "memory_limit_utilization"
  | "memory_request_utilization"
  | "restarts";

export interface WorkloadSeries {
  envId: string;
  container: string;
  metric: WorkloadMetric;
  unit: string;
  /** Epoch seconds. */
  timestamps: number[];
  values: (number | null)[];
}

export interface ImageChange {
  envId: string;
  container: string;
  imageName: string;
  imageTag: string;
  firstSeenMs: number;
  lastSeenMs: number;
}

export interface VersionChange {
  envId: string;
  serviceVersion: string;
  firstSeenMs: number;
  lastSeenMs: number;
}

export interface GetWorkloadRequest extends ObsScope {
  window: TimeWindow;
  workload: string;
  /** Narrow to one container; empty is all of them. */
  container?: string;
  stepSeconds?: number;
}

export interface WorkloadDetail {
  series: WorkloadSeries[];
  /** Newest first, at most 500. */
  events: WorkloadEvent[];
  imageChanges: ImageChange[];
  versionChanges: VersionChange[];
  stepSeconds: number;
  truncated: boolean;
}
