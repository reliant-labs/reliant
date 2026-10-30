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

import type { SeriesMeta } from "./metricsTypes";

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
  /** OTel links are causal references, not parent/child edges. */
  links?: { traceId: string; spanId: string }[];
}

export interface Trace {
  traceId: string;
  spans: Span[];
  /** The server capped the span count; the waterfall is a lower bound. */
  truncated: boolean;
  /** Spans in other traces that link to this trace, within the same authorized scope. */
  relatedTraces?: { traceId: string; spanId: string; linkedSpanId: string }[];
  relatedTracesTruncated?: boolean;
}

// ── Panels ───────────────────────────────────────────────────────────────────

/** One line. `timestamps` are epoch SECONDS (uPlot's x unit); a null value is a gap. */
export interface Series {
  label: string;
  /** For a grouped panel, the group-by field → value (`{"service.name": "api"}`). */
  group: Record<string, string>;
  timestamps: number[];
  values: (number | null)[];
  /** Metrics panels: which points' buckets are still open (draw dashed). Absent on every other signal. */
  partial?: boolean[];
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
  /** Compile the panel without running it: the result carries `compiledSql` and no data. */
  explain?: boolean;
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
  /** A cap cut the result (50 series, 200 rows, 50 issues or sessions). */
  truncated: boolean;
  /** funnel panels: one entry per step, in the funnel's order. */
  funnelSteps: FunnelStepResult[];
  /** issues panels: errors grouped by distinct stack, most events first. */
  issues: PanelIssue[];
  /** sessions panels: browser sessions that started in the window, newest first. */
  sessions: PanelSession[];
  /** Metrics panels: what each entry of `series` measures, in order. Empty for every other signal. */
  seriesMeta: SeriesMeta[];
  /** Series (groups) that matched before the cap; 0 when unknown. Compare with `series.length`. */
  seriesTotal: number;
  /**
   * Non-fatal notes, "<code>" or "<code>: <detail>": mixed_bounds, mixed_temporality, step_raised,
   * metric_stored_as_n_kinds, metric_kind_mismatch, and metric_absent (a series' metric has no data
   * in the scope and range; its series is empty).
   */
  warnings: string[];
  /** With `explain`: the statements the panel compiles to. Empty otherwise. */
  compiledSql: CompiledStatement[];
  /** The window this answers, so a chart's x axis is the window asked for, not the extent of what came back. */
  window?: TimeWindow;
  /** sql time series: the width of one bucket in seconds as the statement produced it; 0 or absent when unknown. */
  bucketSeconds?: number;
  /** sql time series: "UTC" when the points are calendar dates (labelled as that date, never converted); absent for instants. */
  bucketTimezone?: string;
}

/** One statement a panel compiles to: SQL with {name:Type} placeholders and the parameter values. */
export interface CompiledStatement {
  sql: string;
  params: Record<string, string>;
}

/** One step of a funnel panel: how many journeys reached it (and every step before it). */
export interface FunnelStepResult {
  event: string;
  count: number;
  /** count / the first step's count; 0 when the first step is 0. */
  rateFromFirst: number;
  /** count / the previous step's count (the first step: 1 when it has any entrant). */
  rateFromPrevious: number;
}

/** One error issue of an issues panel: one distinct stack of one service. */
export interface PanelIssue {
  serviceName: string;
  /** A UInt64 as decimal text, so it is never rounded. */
  stackHash: string;
  exceptionType: string;
  /** The most recent event's message, volatile parts masked. */
  message: string;
  urlPath: string;
  serviceVersion: string;
  events: number;
  sessions: number;
  firstSeenMs: number;
  lastSeenMs: number;
  sampleTraceId: string;
  sampleSessionId: string;
}

/** One browser session of a sessions panel. */
export interface PanelSession {
  sessionId: string;
  serviceName: string;
  serviceVersion: string;
  envId: string;
  /** Empty when anonymous. */
  userId: string;
  entryPath: string;
  startMs: number;
  endMs: number;
  pageViews: number;
  interactions: number;
  errors: number;
  hasReplay: boolean;
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
  /** Grades the series' values good / needs improvement / poor (web vitals). */
  thresholds?: { good: number; poor: number };
}

/** traces | metrics | logs */
export type Signal = string;
/** number | line | table | bar | heatmap | sql | search | funnel | issues | sessions */
export type PanelKind = string;

/**
 * What a funnel panel draws (forge.Funnel): product event names in order,
 * within a window, counted per session or per user.
 */
export interface PanelFunnel {
  steps: string[];
  /** "1h", "7d"… Empty is 1h. */
  window?: string;
  /** "session" (default) or "user". */
  by?: string;
}
export type Unit = "" | "count" | "ms" | "bytes" | "percent" | "s";

export interface Panel {
  id: string;
  /** Optional dashboard section; viewer visibility never edits this association. */
  section?: string;
  title: string;
  description?: string;
  kind: PanelKind;
  signal: Signal;
  series: SeriesIntent[];
  ratio?: boolean;
  where?: Predicate[];
  groupBy?: string[];
  columns?: string[];
  /** funnel panels only. */
  funnel?: PanelFunnel;
  /** sessions panels only: keep the sessions that hit an error. */
  hasError?: boolean;
  /** line and number panels: plot this expression over the series ("A / B * 100"; A is series[0]). */
  formula?: string;
  /** table and bar panels: keep the top `limit` groups by the first series. */
  limit?: number;
  /** sql panels only: one read-only SELECT, under the alert-rule guard and limits. */
  sql?: string;
  /** sql panels only: the narrowest bucket the statement produces ("1d"); a window shorter than it is widened. */
  minStep?: string;
  unit?: Unit;
  /** Grid columns out of 24. */
  width?: number;
  height?: number;
}

export interface DashboardFilter {
  /** Empty or absent targets all panels. Never infer targeting from panel titles. */
  panelIds?: string[];
  id: string;
  label: string;
  field: string;
  /** The telemetry the dropdown reads values from; empty is traces. */
  signal?: Signal;
}

/**
 * A marker series a dashboard declares over its line panels. `releases` (the
 * only source today) is a marker for every promotion of a release into the
 * environment, drawn from the release ledger (DeliveryInsightsService).
 */
export interface DashboardAnnotation {
  source: string;
  label?: string;
}

export interface Dashboard {
  /**
   * Which telemetry the panels read. `project` (absent): the selected
   * environments of the route's project. `org`: every project's environments.
   * `platform`: the signals belong to no environment, so none is filtered on.
   */
  scope?: "project" | "org" | "platform";
  sections?: { id: string; title: string }[];
  id: string;
  title: string;
  description?: string;
  filters: DashboardFilter[];
  /** Declared marker series; empty when the dashboard declares none. */
  annotations?: DashboardAnnotation[];
  /** The range the dashboard opens on when the URL names none ("7d"), rounded up to a preset. */
  defaultRange?: string;
  panels: Panel[];
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

// ── Service signals ──────────────────────────────────────────────────────────

/** The signals a service has a view of its own for (GetServiceSignals). */
export const SERVICE_SIGNALS = ["logs", "traces", "profiles"] as const;
export type ServiceSignal = (typeof SERVICE_SIGNALS)[number];

export interface GetServiceSignalsRequest extends ObsScope {
  window: TimeWindow;
}

/** One service: what it sends, and what the current release of its environments switches on. */
export interface ServiceSignals {
  service: string;
  /** Signals with data from the service in the window. */
  emitting: ServiceSignal[];
  /** Signals the release declares on; meaningful only when `declarationKnown`. */
  declared: ServiceSignal[];
  /** False: no environment in scope records the workload's spec, so nothing is known of what it declares. */
  declarationKnown: boolean;
}

export interface ServiceSignalsResult {
  /** By service name. */
  services: ServiceSignals[];
  /** More services sent data than the server lists (500). */
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
  /**
   * The reason in plain language ("Waiting for capacity"). Empty when there is
   * none, and then `reason` is shown as it is. `reason` stays the raw Kubernetes
   * word so a row can disclose it and a filter can match it.
   */
  plainReason: string;
}

/** How a workload runs, from the environment's recorded bundle; "unknown" when no bundle declares it. */
export type WorkloadKind = "deployment" | "statefulset" | "daemonset" | "cronjob" | "job" | "unknown";

/**
 * The ONE server-side verdict (WorkloadHealth in the proto): the list, the
 * detail page and every badge read this, never a client-side re-derivation.
 */
export type WorkloadHealth = "healthy" | "degraded" | "crash_looping" | "pending" | "suspended" | "unknown";

/**
 * Whose side a fault is on. "platform" is ours (no capacity, our registry, a node
 * or volume fault): the console says so instead of asking the user to fix it.
 */
export type WorkloadCause = "none" | "workload" | "platform" | "undetermined";

/**
 * How `releaseVersion` was established: "digest" — the observed image digest
 * is the one a promotion pinned (verified); "promotion" — the newest promotion
 * that moved the workload (declared, not verified); "none" — not established.
 */
export type WorkloadReleaseBasis = "digest" | "promotion" | "none";

/** One pod's aggregate over the window. GetWorkload only. */
export interface WorkloadPod {
  name: string;
  uid: string;
  /** "running": reported in the last 5 minutes; "gone": replaced, evicted or finished. */
  state: "running" | "gone" | "unknown";
  restarts: number;
  lastExitReason: string;
  lastExitAtMs: number | null;
  /** k8s.node.name; empty until the node agent reports it. */
  node: string;
  firstSeenMs: number | null;
  lastSeenMs: number | null;
  /** The pod is older than the window, so its age is at least this. */
  ageIsLowerBound: boolean;
  latestWarning: WorkloadEvent | null;
}

export interface DeclaredPort {
  name: string;
  port: number;
  protocol: string;
}

/**
 * What the environment's recorded bundle declares for the workload — names and
 * numbers only (environment variables by NAME, never values). GetWorkload only.
 */
export interface DeclaredWorkload {
  name: string;
  kind: WorkloadKind;
  /** False for a bundle recorded before snapshots: only name, kind and replicas are known. */
  specRecorded: boolean;
  replicas: number | null;
  /** 0 = not declared. */
  cpuRequestMillicores: number;
  cpuLimitMillicores: number;
  memoryRequestBytes: number;
  memoryLimitBytes: number;
  ports: DeclaredPort[];
  envNames: string[];
  image: string;
  /** The environment's entry point (deploy/kcl/<env>/main.k); no line number is recorded. */
  sourcePath: string;
  sourceRepo: string;
  sourceCommit: string;
  bundleId: string;
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
  kind: WorkloadKind;
  health: WorkloadHealth;
  /** Why, in one plain sentence ("1 of 2 replicas ready. It ran out of memory"). Empty when healthy. */
  healthReason: string;
  /** Whose side the fault is on; "none" when nothing is wrong. */
  healthCause: WorkloadCause;
  /** The raw machinery behind healthReason, for a "details" disclosure. Never the headline. */
  healthDetail: string;
  /** The deploy ledger's observed ready count, else the pods reporting. */
  readyReplicas: number | null;
  /** Null when neither the ledger nor the bundle knows it: render ready alone. */
  desiredReplicas: number | null;
  /** The release it runs ("v1.7.44"), or "" when it cannot be established. */
  releaseVersion: string;
  releaseBasis: WorkloadReleaseBasis;
  /** GetWorkload only; the list leaves it empty. */
  pods: WorkloadPod[];
  /** GetWorkload only; null in the list, and when no recorded bundle declares it. */
  declared: DeclaredWorkload | null;
}

export type WorkloadJobResult = "running" | "succeeded" | "failed" | "unknown";

/** One Kubernetes Job run (a migration, a provisioning step), from the Job's own events. */
export interface WorkloadJob {
  envId: string;
  envName: string;
  /** The run, e.g. control-plane-migrate-b9e1b9d6de. */
  name: string;
  /** The declared workload it belongs to (control-plane-migrate). */
  workload: string;
  result: WorkloadJobResult;
  /** BackoffLimitExceeded, DeadlineExceeded… for a failed run. */
  reason: string;
  /** The release in effect when the run started; "" when unknown. */
  releaseVersion: string;
  startedAtMs: number | null;
  finishedAtMs: number | null;
  /** 0 while running, or when either end is unknown. */
  durationSeconds: number;
}

export interface ListWorkloadsResult {
  /** By env, then name. */
  workloads: Workload[];
  truncated: boolean;
  /** Newest first, at most 200. */
  jobs: WorkloadJob[];
  jobsTruncated: boolean;
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
  /** One summary per environment in scope that reports the workload, with its pods and declared config. */
  workloads: Workload[];
}
