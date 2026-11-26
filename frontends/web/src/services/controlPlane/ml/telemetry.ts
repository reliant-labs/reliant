// Copyright (c) 2026 Reliant Labs

/**
 * How the ML tab reads telemetry: through ObservabilityService and
 * AlertingService, like every other telemetry view, never through an ML rpc
 * of its own. This file is the vocabulary between an ML subject (a run, a
 * rank, a node, a GPU, an inference service) and the attributes its
 * telemetry carries (forge docs/proposals/ml-workloads.md), so a view asks
 * for "rank 37's step time" as a metric filtered by `ml.run.id`, `ml.phase`,
 * `ml.attempt` and `ml.rank`.
 *
 *   subjectFilters / subjectOfAttributes  a subject as QueryScope filters, and back
 *   mlMetricName                          a neutral key ("train/loss") as a metric name
 *   ML_STEP_KEY                           the optimizer step, the x a training chart plots against
 *   mlEventName / eventKindOfName         an MlEventKind as a log record's `event.name`
 *   subjectAlertLabels / alertMatches     a subject as the labels its alerts carry
 *
 * The fixture world (fixtures/shop/model/ml/telemetry.ts) answers the same
 * reads by decoding these attributes back into the subject, so the two can
 * never drift apart.
 */

import { MlEventKind, MlSubjectKind } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import type { AttributeFilter } from "../observability/types";

/** The attributes ML telemetry carries, by what they name. */
export const ML_ATTR = {
  run: "ml.run.id",
  phase: "ml.phase",
  attempt: "ml.attempt",
  rank: "ml.rank",
  checkpointStep: "ml.checkpoint.step",
  experiment: "ml.experiment.id",
  cluster: "k8s.cluster.name",
  node: "k8s.node.name",
  /** The GPU's UUID, as DCGM exporter labels its metrics. */
  gpu: "UUID",
  pool: "ml.pool",
  service: "service.name",
  revision: "ml.revision",
  pod: "k8s.pod.name",
  queue: "kueue.cluster_queue",
  workload: "kueue.workload",
  model: "ml.model.id",
  modelVersion: "ml.model.version",
  evalRun: "ml.eval.run.id",
  finding: "ml.finding.id",
  dataset: "ml.dataset.id",
  release: "ml.release",
  /** A family member's key ("eval/mmlu" of "eval/*"): the group a family read is split by. */
  key: "ml.key",
} as const;

export type MlAttr = (typeof ML_ATTR)[keyof typeof ML_ATTR];

/** Keys the server filters by name (its allowlist); every other is spelled with its class prefix. */
const ALLOWLISTED: ReadonlySet<string> = new Set([ML_ATTR.cluster, ML_ATTR.node, ML_ATTR.pod, ML_ATTR.service]);

export type MlSignal = "metrics" | "logs" | "traces";

/** A key as a QueryScope filter key: allowlisted keys bare, the GPU as the signal's own attribute, the rest as resource attributes. */
export function filterKey(key: string, signal: MlSignal): string {
  if (ALLOWLISTED.has(key)) return key;
  if (key === ML_ATTR.gpu) return `${signal === "metrics" ? "metric" : signal === "logs" ? "log" : "span"}.attr.${key}`;
  return `resource.attr.${key}`;
}

/** A filter key back to the attribute it names. */
export function attrOfFilterKey(key: string): string {
  return key.replace(/^(resource|metric|log|span)\.attr\./, "");
}

/** The subject a read is about: a kind and its stable id (MlSubject's). */
export interface SubjectRef {
  kind: MlSubjectKind;
  id: string;
}

const split = (id: string) => id.split("/");

/** The attributes (key → value) a subject's telemetry carries. The org carries none. */
export function subjectAttributes(subject: SubjectRef): Record<string, string> {
  const p = split(subject.id);
  const A = ML_ATTR;
  switch (subject.kind) {
    case MlSubjectKind.RUN:
      return { [A.run]: subject.id };
    case MlSubjectKind.PHASE:
      return { [A.run]: p[0], [A.phase]: p[1] };
    case MlSubjectKind.ATTEMPT:
      return { [A.run]: p[0], [A.phase]: p[1], [A.attempt]: p[2] };
    case MlSubjectKind.RANK:
      return { [A.run]: p[0], [A.phase]: p[1], [A.attempt]: p[2], [A.rank]: p[3] };
    case MlSubjectKind.CHECKPOINT:
      return { [A.run]: p[0], [A.phase]: p[1], [A.checkpointStep]: p[2] };
    case MlSubjectKind.EXPERIMENT:
      return { [A.experiment]: subject.id };
    case MlSubjectKind.CLUSTER:
      return { [A.cluster]: subject.id };
    case MlSubjectKind.NODE:
      return { [A.cluster]: p[0], [A.node]: p.slice(1).join("/") };
    case MlSubjectKind.POOL:
      return { [A.cluster]: p[0], [A.pool]: p.slice(1).join("/") };
    case MlSubjectKind.QUEUE:
      return { [A.cluster]: p[0], [A.queue]: p.slice(1).join("/") };
    case MlSubjectKind.DEVICE:
      return { [A.gpu]: subject.id };
    case MlSubjectKind.INFERENCE_SERVICE:
      return { [A.service]: subject.id };
    case MlSubjectKind.REVISION:
      return { [A.service]: p[0], [A.revision]: p.slice(1).join("/") };
    case MlSubjectKind.REPLICA:
      return { [A.service]: p[0], [A.pod]: p.slice(1).join("/") };
    case MlSubjectKind.WORKLOAD:
      return { [A.workload]: subject.id };
    case MlSubjectKind.POD:
      return { [A.pod]: subject.id };
    case MlSubjectKind.MODEL:
      return { [A.model]: subject.id };
    case MlSubjectKind.MODEL_VERSION:
      return { [A.model]: p[0], [A.modelVersion]: p.slice(1).join("/") };
    case MlSubjectKind.EVAL_RUN:
      return { [A.evalRun]: subject.id };
    case MlSubjectKind.FINDING:
      return { [A.finding]: subject.id };
    case MlSubjectKind.DATASET:
      return { [A.dataset]: subject.id };
    case MlSubjectKind.RELEASE:
      return { [A.release]: subject.id };
    default:
      return {};
  }
}

/** A subject as QueryScope filters for one signal: one equality per attribute it carries. */
export function subjectFilters(subject: SubjectRef, signal: MlSignal): AttributeFilter[] {
  return Object.entries(subjectAttributes(subject)).map(([key, value]) => ({ key: filterKey(key, signal), op: "eq", value }));
}

/**
 * The most specific subject a set of attributes names (subjectAttributes run
 * backwards), or undefined when they name none: the org.
 */
export function subjectOfAttributes(attrs: Readonly<Record<string, string | undefined>>): SubjectRef | undefined {
  const A = ML_ATTR;
  const a = (k: string) => attrs[k] || undefined;
  const run = a(A.run);
  if (run) {
    const phase = a(A.phase);
    const attempt = a(A.attempt);
    const rank = a(A.rank);
    const step = a(A.checkpointStep);
    if (phase && attempt && rank !== undefined) return { kind: MlSubjectKind.RANK, id: `${run}/${phase}/${attempt}/${rank}` };
    if (phase && attempt) return { kind: MlSubjectKind.ATTEMPT, id: `${run}/${phase}/${attempt}` };
    if (phase && step) return { kind: MlSubjectKind.CHECKPOINT, id: `${run}/${phase}/${step}` };
    if (phase) return { kind: MlSubjectKind.PHASE, id: `${run}/${phase}` };
    return { kind: MlSubjectKind.RUN, id: run };
  }
  const service = a(A.service);
  if (service) {
    if (a(A.pod)) return { kind: MlSubjectKind.REPLICA, id: `${service}/${a(A.pod)}` };
    if (a(A.revision)) return { kind: MlSubjectKind.REVISION, id: `${service}/${a(A.revision)}` };
    return { kind: MlSubjectKind.INFERENCE_SERVICE, id: service };
  }
  if (a(A.gpu)) return { kind: MlSubjectKind.DEVICE, id: a(A.gpu)! };
  const cluster = a(A.cluster);
  if (cluster && a(A.node)) return { kind: MlSubjectKind.NODE, id: `${cluster}/${a(A.node)}` };
  if (cluster && a(A.pool)) return { kind: MlSubjectKind.POOL, id: `${cluster}/${a(A.pool)}` };
  if (cluster && a(A.queue)) return { kind: MlSubjectKind.QUEUE, id: `${cluster}/${a(A.queue)}` };
  if (a(A.workload)) return { kind: MlSubjectKind.WORKLOAD, id: a(A.workload)! };
  if (a(A.model) && a(A.modelVersion)) return { kind: MlSubjectKind.MODEL_VERSION, id: `${a(A.model)}/${a(A.modelVersion)}` };
  if (a(A.model)) return { kind: MlSubjectKind.MODEL, id: a(A.model)! };
  if (a(A.experiment)) return { kind: MlSubjectKind.EXPERIMENT, id: a(A.experiment)! };
  if (a(A.evalRun)) return { kind: MlSubjectKind.EVAL_RUN, id: a(A.evalRun)! };
  if (a(A.finding)) return { kind: MlSubjectKind.FINDING, id: a(A.finding)! };
  if (a(A.dataset)) return { kind: MlSubjectKind.DATASET, id: a(A.dataset)! };
  if (a(A.release)) return { kind: MlSubjectKind.RELEASE, id: a(A.release)! };
  if (cluster) return { kind: MlSubjectKind.CLUSTER, id: cluster };
  if (a(A.pod)) return { kind: MlSubjectKind.POD, id: a(A.pod)! };
  return undefined;
}

/** QueryScope filters back to the subject they name (equality filters only). */
export function subjectOfFilters(filters: readonly AttributeFilter[]): SubjectRef | undefined {
  const attrs: Record<string, string> = {};
  for (const f of filters) if (f.op === "eq" && f.value !== undefined) attrs[attrOfFilterKey(f.key)] = f.value;
  return subjectOfAttributes(attrs);
}

// ── Metrics ───────────────────────────────────────────────────────────────────

/**
 * A neutral key as the metric that reports it: "train/loss" → "ml.train.loss",
 * "gpu.util" → "ml.gpu.util". A family ("eval/*") is one metric split by
 * ML_ATTR.key.
 */
export function mlMetricName(key: string): string {
  return `ml.${(isFamily(key) ? key.slice(0, -2) : key).replace(/\//g, ".")}`;
}

/**
 * The optimizer step the trainer is on: a gauge (`ml.train.step`) on the
 * same attributes as every training metric. A training chart reads it
 * beside its keys and plots each point at the step running at that point's
 * time (kit/telemetry.ts onStepAxis); a run that does not report it is
 * plotted against time.
 */
export const ML_STEP_KEY = "train/step";

/** A key that reads every key under its prefix ("eval/*"). */
export const isFamily = (key: string) => key.endsWith("/*");

/** The group-by dimensions a series read splits by, as attributes ("rank" → ml.rank). */
export const ML_GROUP_BY: Readonly<Record<string, string>> = {
  rank: ML_ATTR.rank,
  attempt: ML_ATTR.attempt,
  node: ML_ATTR.node,
  device: ML_ATTR.gpu,
  pool: ML_ATTR.pool,
  replica: ML_ATTR.pod,
  revision: ML_ATTR.revision,
  namespace: "k8s.namespace.name",
  arm: "ml.arm",
  status: "ml.status_class",
  finish_reason: "gen_ai.response.finish_reason",
};

/** A group-by attribute back to its dimension ("ml.rank" → "rank"). */
export function groupByOfAttr(attr: string): string | undefined {
  return Object.entries(ML_GROUP_BY).find(([, a]) => a === attr)?.[0];
}

// ── Events ────────────────────────────────────────────────────────────────────

/** The log attribute an event record carries (OpenTelemetry's event name). */
export const EVENT_NAME_ATTR = "event.name";

/** The attributes an ML event record carries beyond its subject's. */
export const ML_EVENT_ATTR = {
  /** The subject it happened to, as an MlSubjectKind name and id ("node", "gke-…/gpu-h100-07"). */
  subjectKind: "ml.subject.kind",
  subjectId: "ml.subject.id",
  subjectLabel: "ml.subject.label",
  /** Everything else it touches, as JSON [{kind, id, label}]. */
  related: "ml.event.related",
  detail: "ml.event.detail",
  /** When a stretch (a throttle episode) ended, epoch ms. */
  endMs: "ml.event.end_ms",
  step: "ml.step",
  source: "ml.event.source",
  severity: "ml.event.severity",
  hookEventType: "ml.hook.event_type",
} as const;

/** An MlEventKind as an `event.name`: CHECKPOINT → "ml.checkpoint". */
export function mlEventName(kind: MlEventKind): string {
  return `ml.${(MlEventKind[kind] ?? "UNSPECIFIED").toLowerCase()}`;
}

/** An `event.name` back to its MlEventKind; UNSPECIFIED for one that is not an ML event. */
export function eventKindOfName(name: string | undefined): MlEventKind {
  if (!name?.startsWith("ml.")) return MlEventKind.UNSPECIFIED;
  const kind = (MlEventKind as unknown as Record<string, number | undefined>)[name.slice(3).toUpperCase()];
  return typeof kind === "number" ? (kind as MlEventKind) : MlEventKind.UNSPECIFIED;
}

/** An MlSubjectKind as the word an event record names it by ("node"), and back. */
export const subjectKindName = (kind: MlSubjectKind) => (MlSubjectKind[kind] ?? "UNSPECIFIED").toLowerCase();
export function subjectKindOfName(name: string | undefined): MlSubjectKind {
  const kind = name ? (MlSubjectKind as unknown as Record<string, number | undefined>)[name.toUpperCase()] : undefined;
  return typeof kind === "number" ? (kind as MlSubjectKind) : MlSubjectKind.UNSPECIFIED;
}

/**
 * The SearchLogs query for ML event records: every one, or only `kinds`
 * (an alternation the server's grammar takes).
 */
export function eventQuery(kinds: readonly MlEventKind[] = []): string {
  if (kinds.length === 0) return `attr.${EVENT_NAME_ATTR}:ml.*`;
  const terms = kinds.map((k) => `attr.${EVENT_NAME_ATTR}:${mlEventName(k)}`);
  return terms.length === 1 ? terms[0] : `(${terms.join(" OR ")})`;
}

/** The kinds an eventQuery asks for (empty: every ML event), or null when it is not one. */
export function eventKindsOfQuery(query: string): MlEventKind[] | null {
  const names = [...query.matchAll(/attr\.event\.name:(ml\.[a-z_*]+)/g)].map((m) => m[1]);
  if (names.length === 0) return null;
  if (names.includes("ml.*")) return [];
  return names.map(eventKindOfName).filter((k) => k !== MlEventKind.UNSPECIFIED);
}

// ── Alerts ────────────────────────────────────────────────────────────────────

/** An attribute as an alert label: alert labels take no dots ("ml.run.id" → "ml_run_id"). */
export const alertLabel = (attr: string) => attr.replace(/[^A-Za-z0-9_]/g, "_");

/** The labels an alert on `subject` carries (alertLabel of each attribute). */
export function subjectAlertLabels(subject: SubjectRef): Record<string, string> {
  return Object.fromEntries(Object.entries(subjectAttributes(subject)).map(([k, v]) => [alertLabel(k), v]));
}

/** Whether an alert's labels put it on `subject` or under it (a run's alert covers its ranks; the org covers every ML alert). */
export function alertMatches(labels: Readonly<Record<string, string>>, subject: SubjectRef): boolean {
  const want = subjectAlertLabels(subject);
  if (Object.keys(want).length === 0) return isMlAlert(labels);
  return Object.entries(want).every(([k, v]) => labels[k] === v);
}

/** Whether an alert is about ML at all: it names an ML subject. */
export function isMlAlert(labels: Readonly<Record<string, string>>): boolean {
  return subjectOfAlertLabels(labels) !== undefined;
}

/** The subject an alert's labels name. */
export function subjectOfAlertLabels(labels: Readonly<Record<string, string>>): SubjectRef | undefined {
  const attrs: Record<string, string> = {};
  for (const attr of Object.values(ML_ATTR)) {
    const v = labels[alertLabel(attr)];
    if (v !== undefined) attrs[attr] = v;
  }
  return subjectOfAttributes(attrs);
}
