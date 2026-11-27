// Copyright (c) 2026 Reliant Labs

/**
 * The ML fixture world's telemetry, served where every telemetry read goes:
 * ObservabilityService (SearchLogs, ListTraces, GetTrace, GetFlamegraph,
 * QueryPanel) and AlertingService (ListAlerts). withMlTelemetry wraps the
 * pack's model-level ObservabilityApi: a read whose scope names an ML
 * subject by its attributes (ml.run.id, k8s.node.name, UUID, service.name of
 * an inference service, ...; services/controlPlane/ml/telemetry.ts decodes
 * them), asks for an `ml.*` metric, or asks for ML event records
 * (`event.name: ml.*`) is answered from the domains' records
 * (./telemetryTypes.ts); every other read is shop's, unchanged. ListAlerts
 * answers shop's alerts plus the ML rules that are pending or firing, each
 * labelled with its subject's attributes.
 */

import { timestampMs, type Timestamp } from "@bufbuild/protobuf/wkt";

import { MlAxis, MlCollection, MlSeverity, MlSubjectKind } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import type { AlertInstance } from "@/services/controlPlane/observability/alertingTypes";
import { NO_CLIENT } from "@/services/controlPlane/observability/client";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { AttributeFilter, Flamegraph, LogRecord, ObsScope, PanelResult, Series, Severity, Span, TimeWindow, TraceSummary } from "@/services/controlPlane/observability/types";
import type { SeriesMeta } from "@/services/controlPlane/observability/metricsTypes";
import {
  alertLabel,
  attrOfFilterKey,
  EVENT_NAME_ATTR,
  eventKindsOfQuery,
  filterKey,
  groupByOfAttr,
  ML_ATTR,
  ML_EVENT_ATTR,
  mlEventName,
  mlMetricName,
  subjectAttributes,
  subjectKindName,
  subjectOfFilters,
  type SubjectRef,
} from "@/services/controlPlane/ml/telemetry";

import { ML_TELEMETRY } from "./index";
import { MlAlertState, MlSpanStatus, type EventInit, type LogLineInit, type MlTelemetryDomain, type SpanInit, type SubjectArg, type TraceSummaryInit } from "./telemetryTypes";
import { ALL_POOLS, CLUSTERS, gpuUuid, SERVICES } from "./world";

type Now = () => number;
type Domains = readonly MlTelemetryDomain[];

const ML_ONLY_ATTRS: ReadonlySet<string> = new Set([ML_ATTR.gpu, ML_ATTR.queue, ML_ATTR.workload, ...Object.values(ML_ATTR).filter((a) => a.startsWith("ml."))]);
const ML_SERVICES: ReadonlySet<string> = new Set(Object.values(SERVICES).map((s) => s.id));
const ML_CLUSTERS: ReadonlySet<string> = new Set(Object.values(CLUSTERS).map((c) => c.id));
const ML_NODES: ReadonlySet<string> = new Set(ALL_POOLS.flatMap((p) => p.nodes as readonly string[]));

/** Whether a scope's filters name an ML subject (not one of shop's services or pods). */
function namesMl(filters: readonly AttributeFilter[]): boolean {
  return filters.some((f) => {
    const attr = attrOfFilterKey(f.key);
    if (ML_ONLY_ATTRS.has(attr)) return true;
    const v = f.value ?? "";
    return (attr === ML_ATTR.service && ML_SERVICES.has(v)) || (attr === ML_ATTR.cluster && ML_CLUSTERS.has(v)) || (attr === ML_ATTR.node && ML_NODES.has(v));
  });
}

/** The subject a scope names, as the domains take it; the org when it names none. */
function subjectArg(filters: readonly AttributeFilter[]): SubjectArg {
  const s = subjectOfFilters(filters);
  return s ? { kind: s.kind, id: s.id, label: "" } : { kind: MlSubjectKind.ORG, id: "", label: "" };
}

const msOf = (t: Timestamp | undefined): number => (t ? timestampMs(t) : 0);
const inWindow = (ms: number, w: TimeWindow) => ms >= w.startMs && ms < w.endMs;

/** Every domain's answer, the lists concatenated (a run's events are the trainer's and its nodes'). */
function ask<K extends keyof MlTelemetryDomain, R>(domains: Domains, key: K, call: (fn: NonNullable<MlTelemetryDomain[K]>) => R | undefined): R[] {
  const out: R[] = [];
  for (const d of domains) {
    const fn = d[key];
    if (!fn) continue;
    const r = call(fn as NonNullable<MlTelemetryDomain[K]>);
    if (r !== undefined) out.push(r);
  }
  return out;
}

// ── Series ────────────────────────────────────────────────────────────────────

const STAT_OF_QUANTILE: Record<string, string> = { "0.05": "p5", "0.25": "p25", "0.5": "p50", "0.75": "p75", "0.9": "p90", "0.95": "p95", "0.99": "p99" };
const PERCENTILE = /^(p\d+|mean|min|max)$/;

/** A generated group label as the raw value of its group-by attribute ("rank 37" → "37", a node's "GPU 3" → its UUID). */
function groupValue(attr: string, label: string, subject: SubjectArg): string {
  if (attr === ML_ATTR.rank || attr === ML_ATTR.attempt) return label.replace(/^(rank|attempt)\s+/, "");
  if (attr === ML_ATTR.gpu && subject.kind === MlSubjectKind.NODE) {
    const m = /^GPU (\d+)$/.exec(label);
    if (m) return gpuUuid(subject.id.split("/").pop() ?? subject.id, Number(m[1]));
  }
  return label;
}

/**
 * A QueryPanel read of ML metrics: each series intent is labelled with its
 * neutral key (kit/telemetry.ts seriesPanel), its quantile picks a stat; the
 * domains' series become the panel's, grouped by the asked attribute, clipped
 * to the window; a key with nothing to show is a `metric_absent` warning.
 */
function mlPanel(domains: Domains, req: Parameters<ObservabilityApi["queryPanel"]>[0], now: Now): PanelResult {
  const subject = subjectArg(req.filters);
  const groupAttrs = (req.groupBy?.length ? req.groupBy : (req.panel.groupBy ?? [])).map(attrOfFilterKey);
  const groupAttr = groupAttrs.find((a) => a !== ML_ATTR.key);
  const groupBy = groupAttr ? (groupByOfAttr(groupAttr) ?? groupAttr) : undefined;
  const intents = req.panel.series.map((s) => {
    const stat = s.agg === "quantile" ? (STAT_OF_QUANTILE[String(s.quantile ?? 0.5)] ?? "p50") : "";
    const key = stat && s.label.endsWith(` ${stat}`) ? s.label.slice(0, -(stat.length + 1)) : s.label;
    return { label: s.label, key, stat, field: s.field ?? mlMetricName(key), agg: s.agg, quantile: s.quantile };
  });
  const keys = [...new Set(intents.map((i) => i.key))];
  const generated = ask(domains, "series", (fn) => fn({ subject, keys, axis: MlAxis.TIME, groupBy, maxPoints: 300 }, now)).flatMap((r) => r.series);
  const series: Series[] = [];
  const seriesMeta: SeriesMeta[] = [];
  const warnings: string[] = [];
  let step = 0;
  for (const intent of intents) {
    const family = intent.key.endsWith("/*") ? intent.key.slice(0, -1) : null;
    const ofKey = generated.filter((s) => (family ? (s.key ?? "").startsWith(family) : s.key === intent.key));
    const picked = ofKey.filter((s) => (intent.stat ? s.stat === intent.stat : !s.stat || s.stat === "p50" || s.stat === "mean" || !PERCENTILE.test(s.stat)));
    const shown = picked.filter((s) => s.collection !== MlCollection.NOT_COLLECTED && s.collection !== MlCollection.NOT_APPLICABLE && (s.x?.length ?? 0) > 0);
    if (shown.length === 0) {
      const missing = ofKey.find((s) => s.collection === MlCollection.NOT_APPLICABLE || s.collection === MlCollection.NOT_COLLECTED);
      const reason = missing?.collection === MlCollection.NOT_APPLICABLE ? `not applicable: ${missing.missingSource ?? ""}` : (missing?.missingSource ?? "");
      warnings.push(`metric_absent: ${intent.field}${reason ? `: ${reason}` : ""}`);
      continue;
    }
    seriesMeta.push({ label: intent.label, metric: intent.field, kind: "gauge", agg: intent.agg, quantile: intent.quantile, valueUnit: shown[0].unit ?? "" });
    for (const s of shown) {
      step = step || (s.resolution ?? 0);
      const group: Record<string, string> = {};
      if (family) group[filterKey(ML_ATTR.key, "metrics")] = s.key ?? "";
      let label = s.group ?? "";
      if (s.stat && label.endsWith(` ${s.stat}`)) label = label.slice(0, -(s.stat.length + 1));
      if (label === s.stat) label = "";
      if (!intent.stat && s.stat && !PERCENTILE.test(s.stat)) label = label || s.stat;
      if (label) {
        // Unasked, a series per attempt or rank still carries that dimension (a phase's loss is one line per attempt).
        const attr = groupAttr ?? (/^attempt \d+$/.test(label) ? ML_ATTR.attempt : /^rank \d+$/.test(label) ? ML_ATTR.rank : undefined);
        group[attr ? filterKey(attr, "metrics") : "ml.series"] = attr ? groupValue(attr, label, subject) : label;
      }
      const timestamps: number[] = [];
      const values: (number | null)[] = [];
      (s.x ?? []).forEach((x, i) => {
        if (!inWindow(x * 1000, req.window)) return;
        const y = s.y?.[i];
        timestamps.push(x);
        values.push(y === undefined || !Number.isFinite(y) ? null : y);
      });
      if (timestamps.length) series.push({ label: intent.label, group, timestamps, values, value: null });
    }
  }
  return {
    kind: "line",
    unit: seriesMeta[0]?.valueUnit ?? "",
    series,
    columns: [],
    rows: [],
    stepSeconds: step,
    truncated: false,
    funnelSteps: [],
    issues: [],
    sessions: [],
    seriesMeta,
    seriesTotal: series.length,
    warnings,
    compiledSql: [],
    window: req.window,
  };
}

/** Whether a panel reads ML metrics. */
const readsMl = (req: Parameters<ObservabilityApi["queryPanel"]>[0]) => req.panel.signal === "metrics" && req.panel.series.some((s) => (s.field ?? "").startsWith("ml."));

// ── Logs and events ───────────────────────────────────────────────────────────

const LEVEL: Record<string, Severity> = { error: "error", warn: "warn", warning: "warn", info: "info", debug: "debug" };
const SEVERITY_OF_EVENT: Partial<Record<MlSeverity, Severity>> = { [MlSeverity.CRITICAL]: "error", [MlSeverity.WARNING]: "warn", [MlSeverity.INFO]: "info" };

/** The attributes a subject's records carry, from an init (an event's subject). */
const attrsOf = (s: { kind?: number; id?: string } | undefined): Record<string, string> => (s?.kind && s.id !== undefined ? subjectAttributes({ kind: s.kind, id: s.id }) : {});

/** One ML event as the log record a server stores it as: `event.name` ml.<kind>, its subject's attributes and its own. */
function eventRecord(e: EventInit): LogRecord {
  const subject = e.subject;
  const attributes: Record<string, string> = {
    ...(e.attributes ?? {}),
    [EVENT_NAME_ATTR]: mlEventName(e.kind ?? 0),
    ...(subject ? { [ML_EVENT_ATTR.subjectKind]: subjectKindName(subject.kind ?? 0), [ML_EVENT_ATTR.subjectId]: subject.id ?? "", [ML_EVENT_ATTR.subjectLabel]: subject.label ?? "" } : {}),
    ...(e.related?.length ? { [ML_EVENT_ATTR.related]: JSON.stringify(e.related.map((r) => ({ kind: subjectKindName(r.kind ?? 0), id: r.id ?? "", label: r.label ?? "" }))) } : {}),
    ...(e.detail ? { [ML_EVENT_ATTR.detail]: e.detail } : {}),
    ...(e.endAt ? { [ML_EVENT_ATTR.endMs]: String(msOf(e.endAt as Timestamp)) } : {}),
    ...(e.step ? { [ML_EVENT_ATTR.step]: String(e.step) } : {}),
    ...(e.source ? { [ML_EVENT_ATTR.source]: e.source } : {}),
    ...(e.severity ? { [ML_EVENT_ATTR.severity]: (MlSeverity[e.severity] ?? "").toLowerCase() } : {}),
    ...(e.hookEventType ? { [ML_EVENT_ATTR.hookEventType]: e.hookEventType } : {}),
  };
  const severity = SEVERITY_OF_EVENT[e.severity ?? MlSeverity.UNSPECIFIED] ?? "info";
  return {
    id: e.id ?? `${attributes[EVENT_NAME_ATTR]}-${msOf(e.at as Timestamp)}`,
    timeMs: msOf(e.at as Timestamp),
    severity,
    severityText: severity.toUpperCase(),
    service: e.source || "ml",
    body: e.title ?? "",
    traceId: "",
    spanId: "",
    attributes,
    resource: attrsOf(subject),
    source: "",
    operatorData: false,
  };
}

/** One pod's line as a log record: the trainer's ml.* attributes on its resource. */
function logRecord(l: LogLineInit, i: number): LogRecord {
  const node = l.nodeId?.split("/").pop();
  const severity = LEVEL[l.level ?? ""] ?? "info";
  return {
    id: `ml-log-${l.runId}-${l.attempt}-${msOf(l.time)}-${i}`,
    timeMs: msOf(l.time),
    severity,
    severityText: (l.level ?? "info").toUpperCase(),
    service: l.container || "trainer",
    body: l.message ?? "",
    traceId: l.traceId ?? "",
    spanId: "",
    attributes: { ...(l.highlight ? { "ml.log.highlight": l.highlight } : {}), ...(l.eventId ? { "ml.event.id": l.eventId } : {}) },
    resource: {
      ...(l.runId ? { [ML_ATTR.run]: l.runId } : {}),
      ...(l.phaseKey ? { [ML_ATTR.phase]: l.phaseKey } : {}),
      ...(l.attempt ? { [ML_ATTR.attempt]: String(l.attempt) } : {}),
      ...(l.rank !== undefined && l.rank >= 0 ? { [ML_ATTR.rank]: String(l.rank) } : {}),
      ...(l.pod ? { [ML_ATTR.pod]: l.pod } : {}),
      ...(node ? { [ML_ATTR.node]: node } : {}),
      ...(l.container ? { "k8s.container.name": l.container } : {}),
    },
    source: "",
    operatorData: false,
  };
}

/** The server's query grammar, enough of it: `attr.k:v` / `res.k:v` / `severity:x` / `service:x` terms, the rest words of the body. */
function matchesQuery(r: LogRecord, query: string): boolean {
  const terms = query.replace(/[()]/g, " ").split(/\s+/).filter((t) => t && t !== "OR" && t !== "AND");
  return terms.every((term) => {
    const neg = term.startsWith("-");
    const t = neg ? term.slice(1) : term;
    const m = /^(attr|res)\.(.+?):(.*)$/.exec(t);
    let hit: boolean;
    if (m) {
      const v = (m[1] === "attr" ? r.attributes : r.resource)[m[2]];
      hit = m[3] === "*" ? v !== undefined : m[3].endsWith("*") ? (v ?? "").startsWith(m[3].slice(0, -1)) : v === m[3];
    } else if (t.startsWith("severity:")) hit = r.severity === t.slice(9);
    else if (t.startsWith("service:")) hit = t.endsWith("*") ? r.service.startsWith(t.slice(8, -1)) : r.service === t.slice(8);
    else hit = r.body.toLowerCase().includes(t.replace(/^"|"$/g, "").toLowerCase());
    return neg ? !hit : hit;
  });
}

function searchMlLogs(domains: Domains, req: Parameters<ObservabilityApi["searchLogs"]>[0], now: Now) {
  const subject = subjectArg(req.filters);
  const kinds = eventKindsOfQuery(req.query);
  let records: LogRecord[];
  let query = req.query;
  if (kinds) {
    records = ask(domains, "events", (fn) => fn({ subject, kinds }, now))
      .flatMap((r) => r.events)
      .map(eventRecord);
    query = query.replace(/\(?\s*attr\.event\.name:ml\.[a-z_*]+(\s+OR\s+attr\.event\.name:ml\.[a-z_*]+)*\s*\)?/g, " ").trim();
  } else {
    records = ask(domains, "logs", (fn) => fn({ subject, limit: 5000 }, now))
      .flatMap((r) => r.lines)
      .map(logRecord);
  }
  const seen = new Set<string>();
  records = records
    .filter((r) => (req.traceId ? r.traceId === req.traceId : inWindow(r.timeMs, req.window)))
    .filter((r) => !req.services.length || req.services.includes(r.service))
    .filter((r) => !req.severities.length || req.severities.includes(r.severity))
    .filter((r) => !query || matchesQuery(r, query))
    .filter((r) => (seen.has(r.id) ? false : (seen.add(r.id), true)))
    .sort((a, b) => b.timeMs - a.timeMs);
  const from = Number(req.cursor?.replace(/^ml:/, "") ?? 0) || 0;
  const limit = req.limit > 0 ? req.limit : 200;
  return { records: records.slice(from, from + limit), nextCursor: from + limit < records.length ? `ml:${from + limit}` : "" };
}

// ── Traces ────────────────────────────────────────────────────────────────────

/** A span as Observability's: its join keys among its attributes, as every ML span carries them. */
function spanOfInit(s: SpanInit): Span {
  const node = s.nodeId?.split("/").pop();
  const joins: Record<string, string> = {
    ...(s.runId ? { [ML_ATTR.run]: s.runId } : {}),
    ...(s.phaseKey ? { [ML_ATTR.phase]: s.phaseKey } : {}),
    ...(s.attempt ? { [ML_ATTR.attempt]: String(s.attempt) } : {}),
    ...(s.runId && s.rank !== undefined && s.rank >= 0 && (s.attempt || s.phaseKey) ? { [ML_ATTR.rank]: String(s.rank) } : {}),
    ...(node ? { [ML_ATTR.node]: node } : {}),
    ...(s.deviceUuid ? { [ML_ATTR.gpu]: s.deviceUuid } : {}),
    ...(s.pod ? { [ML_ATTR.pod]: s.pod } : {}),
  };
  return {
    spanId: s.spanId ?? "",
    parentSpanId: s.parentSpanId ?? "",
    name: s.name ?? "",
    service: s.service ?? "",
    kind: s.kind || "internal",
    startMs: msOf(s.start),
    durationMs: s.durationMs ?? 0,
    status: s.status === MlSpanStatus.ERROR ? "error" : s.status === MlSpanStatus.OK ? "ok" : "unset",
    statusMessage: s.statusMessage ?? "",
    attributes: { ...joins, ...(s.attributes ?? {}) },
    resource: { "service.name": s.service ?? "" },
    events: (s.events ?? []).map((e) => ({ timeMs: msOf(e.time), name: e.name ?? "", attributes: { ...(e.attributes ?? {}) } })),
    source: "",
    operatorData: false,
  };
}

function summaryOf(t: TraceSummaryInit, spans: readonly SpanInit[]): TraceSummary {
  const root = spans.find((s) => !s.parentSpanId) ?? spans[0];
  const services = [...new Set(spans.map((s) => s.service ?? "").filter(Boolean))];
  return {
    traceId: t.traceId ?? "",
    rootService: root?.service ?? services[0] ?? "",
    rootOperation: t.title || root?.name || "",
    startMs: msOf(t.start),
    durationMs: t.durationMs ?? 0,
    rootDurationMs: root?.durationMs ?? t.durationMs ?? 0,
    spanCount: spans.length,
    errorCount: spans.filter((s) => s.status === MlSpanStatus.ERROR).length || (t.error ? 1 : 0),
    services,
    userId: "",
    sessionId: "",
    country: "",
    region: "",
    city: "",
    client: NO_CLIENT,
    frontendVersion: "",
    backendBuilds: [],
    source: "",
    operatorData: false,
  };
}

const quantile = (sorted: readonly number[], q: number) => (sorted.length ? sorted[Math.min(sorted.length - 1, Math.floor(q * sorted.length))] : 0);

function listTracesOf(domains: Domains, req: Parameters<ObservabilityApi["listTraces"]>[0], now: Now) {
  const subject = subjectArg(req.filters);
  const kept = ask(domains, "traces", (fn) => fn({ subject, limit: 500 }, now)).flatMap((r) => r.traces);
  let all = kept.map((t) => {
    const built = ask(domains, "trace", (fn) => fn({ traceId: t.traceId ?? "" }, now))[0];
    return summaryOf(t, built?.spans ?? []);
  });
  all = all.filter((t) => inWindow(t.startMs, req.window)).sort((a, b) => b.startMs - a.startMs);
  if (req.rootSpanName) all = all.filter((t) => t.rootOperation.toLowerCase().includes(req.rootSpanName.toLowerCase()));
  if (req.services?.length) all = all.filter((t) => t.services.some((s) => req.services.includes(s)));
  if (req.errorsOnly) all = all.filter((t) => t.errorCount > 0);
  if (req.minDurationMs) all = all.filter((t) => t.durationMs >= req.minDurationMs);
  const durations = all.map((t) => t.durationMs).sort((a, b) => a - b);
  const seconds = Math.max(1, (req.window.endMs - req.window.startMs) / 1000);
  const errors = all.filter((t) => t.errorCount > 0).length;
  const from = Number(req.cursor?.replace(/^ml:/, "") ?? 0) || 0;
  const limit = req.limit > 0 ? req.limit : 100;
  return {
    traces: all.slice(from, from + limit),
    nextCursor: from + limit < all.length ? `ml:${from + limit}` : "",
    red: { traceCount: all.length, errorTraceCount: errors, ratePerSecond: all.length / seconds, errorRate: all.length ? errors / all.length : 0, p50Ms: quantile(durations, 0.5), p95Ms: quantile(durations, 0.95), p99Ms: quantile(durations, 0.99) },
    redGroups: [],
  };
}

// ── Profiles ──────────────────────────────────────────────────────────────────

/** Which captures a profile type reads: CPU from the continuous profiler and py-spy, GPU from the CUPTI sampler and the PyTorch profiler. */
const PROFILE_TYPES: Record<string, readonly number[]> = { cpu: [7, 6], gpu: [8, 1] };

function mlFlamegraph(domains: Domains, req: Parameters<ObservabilityApi["getFlamegraph"]>[0], now: Now): Flamegraph {
  const subject = subjectArg(req.filters);
  const captures = ask(domains, "profiles", (fn) => fn({ subject }, now)).flatMap((r) => r.profiles);
  const available = Object.keys(PROFILE_TYPES).filter((t) => captures.some((p) => PROFILE_TYPES[t].includes(p.kind ?? 0)));
  const pick = captures.find((p) => (PROFILE_TYPES[req.profileType] ?? []).includes(p.kind ?? 0));
  const body = pick?.id ? ask(domains, "profile", (fn) => fn({ profileId: pick.id! }, now))[0]?.profile : undefined;
  const json = body?.flamegraphJson ?? "";
  let stackCount = 0;
  let totalValue = 0;
  let unit = "";
  if (json) {
    const file = JSON.parse(json) as { profiles?: { unit?: string; samples?: unknown[]; weights?: number[] }[] };
    const p = file.profiles?.[0];
    stackCount = p?.samples?.length ?? 0;
    totalValue = (p?.weights ?? []).reduce((a, b) => a + b, 0);
    unit = p?.unit ?? "";
  }
  return { speedscopeJson: json, totalValue, unit, stackCount, availableProfileTypes: available, truncated: false };
}

// ── Alerts ────────────────────────────────────────────────────────────────────

const SEVERITY_LABEL: Partial<Record<MlSeverity, string>> = { [MlSeverity.CRITICAL]: "critical", [MlSeverity.WARNING]: "warning", [MlSeverity.INFO]: "info" };

/** The ML rules pending or firing, as AlertingService alerts labelled with their subject's attributes. */
function mlAlerts(domains: Domains, now: Now): AlertInstance[] {
  const org: SubjectArg = { kind: MlSubjectKind.ORG, id: "", label: "" };
  const seen = new Set<string>();
  const out: AlertInstance[] = [];
  for (const rule of ask(domains, "alerts", (fn) => fn({ subject: org }, now)).flatMap((r) => r.alerts)) {
    if (rule.state !== MlAlertState.FIRING && rule.state !== MlAlertState.PENDING) continue;
    const key = `${rule.id}|${rule.subject?.id ?? ""}`;
    if (seen.has(key)) continue;
    seen.add(key);
    const subject: SubjectRef | undefined = rule.subject?.kind ? { kind: rule.subject.kind, id: rule.subject.id ?? "" } : undefined;
    const labels: Record<string, string> = {
      alertname: rule.name ?? rule.id ?? "",
      severity: SEVERITY_LABEL[rule.severity ?? MlSeverity.UNSPECIFIED] ?? "info",
      ...(subject ? Object.fromEntries(Object.entries(subjectAttributes(subject)).map(([k, v]) => [alertLabel(k), v])) : {}),
      ...(rule.subject?.label ? { ml_subject_label: rule.subject.label } : {}),
    };
    const firedMs = msOf(rule.lastFiredAt) || null;
    out.push({
      envId: "",
      rule: rule.name ?? rule.id ?? "",
      fingerprint: rule.id ?? key,
      labels,
      annotations: {
        summary: rule.expression ?? "",
        ...(rule.autoAction ? { auto_action: `${rule.autoAction}${rule.autoActionEnabled ? "" : " (off: opt-in)"}` } : {}),
        ...(rule.routedTo ? { routed_to: rule.routedTo } : {}),
        ...(rule.lastAction ? { last_action: rule.lastAction } : {}),
      },
      state: rule.state === MlAlertState.FIRING ? "firing" : "pending",
      activeAtMs: firedMs,
      firingAtMs: rule.state === MlAlertState.FIRING ? firedMs : null,
      lastEvalAtMs: now() - 20_000,
      lastValue: 0,
    });
  }
  return out;
}

// ── The decorator ─────────────────────────────────────────────────────────────

const isMlScope = (scope: ObsScope) => namesMl(scope.filters);

/**
 * `base` with the ML world's telemetry: ML-scoped reads answered from
 * `domains` (the world's, by default), the rest passed through.
 */
export function withMlTelemetry(base: ObservabilityApi, now: Now, domains: Domains = ML_TELEMETRY): ObservabilityApi {
  return {
    ...base,
    queryPanel: async (req, opts) => (readsMl(req) ? mlPanel(domains, req, now) : base.queryPanel(req, opts)),
    searchLogs: async (req, opts) => (isMlScope(req) || eventKindsOfQuery(req.query) ? searchMlLogs(domains, req, now) : base.searchLogs(req, opts)),
    listTraces: async (req, opts) => (isMlScope(req) ? listTracesOf(domains, req, now) : base.listTraces(req, opts)),
    getTrace: async (req, opts) => {
      const built = ask(domains, "trace", (fn) => fn({ traceId: req.traceId }, now))[0];
      return built ? { traceId: req.traceId, spans: built.spans.map(spanOfInit), truncated: built.truncated ?? false } : base.getTrace(req, opts);
    },
    getFlamegraph: async (req, opts) => (isMlScope(req) ? mlFlamegraph(domains, req, now) : base.getFlamegraph(req, opts)),
    getServiceOverview: async (req, opts) => (isMlScope(req) ? { services: [], trendStepSeconds: 60 } : base.getServiceOverview(req, opts)),
    getServiceSignals: async (req, opts) => (isMlScope(req) ? { services: [], truncated: false } : base.getServiceSignals(req, opts)),
    listAlerts: async (orgId, envId, opts) => {
      const shop = await base.listAlerts(orgId, envId, opts);
      return envId ? shop : [...shop, ...mlAlerts(domains, now)];
    },
  };
}
