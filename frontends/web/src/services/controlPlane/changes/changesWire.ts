// Copyright (c) 2025 Reliant Labs

/**
 * controlplane.v1.ChangesService's answers, read into the console's types
 * (changesTypes.ts). The calls are the generated hooks
 * (hooks/controlplane/changes-service-hooks_gen.ts, via
 * hooks/changes-queries.ts); the requests are changesRequests.ts.
 *
 * The readers take a message's protojson form (`toJson`: camelCase fields,
 * enums by name, int64 as strings, timestamps as RFC 3339), the form the
 * control plane's JSON answers are in. Every field has a reader default, so
 * a field the server leaves unset (proto3 omits defaults) reads as empty,
 * never as a crash.
 */

import { toJson } from "@bufbuild/protobuf";

import {
  ChangeSchema,
  GetChangeReadoutResponseSchema,
  GetChangesDeliveryStatusResponseSchema,
  ListChangeEventsResponseSchema,
  ListChangesResponseSchema,
  RequestChangeApprovalResponseSchema,
  type Change as ChangeMessage,
  type GetChangeReadoutResponse,
  type GetChangesDeliveryStatusResponse,
  type ListChangeEventsResponse,
  type ListChangesResponse,
  type RequestChangeApprovalResponse,
} from "@/gen/controlplane/services/changes/v1/changes_pb";

import type {
  Change,
  ChangeReadout,
  ChangeReadoutArm,
  ChangeReadoutComparison,
  ChangeReadoutEndReason,
  ChangeReadoutReason,
  ChangeReadoutVerdict,
  ChangeApprovalCode,
  ChangeAudience,
  ChangeDeclaration,
  ChangeDeliveryTargetKind,
  ChangesDeliveryStatus,
  ChangeEvent,
  ChangeEventPage,
  ChangeList,
  ChangeMetric,
  ChangeMode,
  ChangePreviewEntry,
  ChangeRun,
  ChangeSplitArm,
  ChangeUnit,
  ChangeVariant,
} from "./changesTypes";

// ── protojson → console types ───────────────────────────────────────────────

type Json = Record<string, unknown>;

const obj = (v: unknown): Json => (v && typeof v === "object" && !Array.isArray(v) ? (v as Json) : {});
const arr = (v: unknown): unknown[] => (Array.isArray(v) ? v : []);
const str = (v: unknown): string => (typeof v === "string" ? v : "");
const num = (v: unknown): number => {
  if (typeof v === "number") return v;
  if (typeof v === "string" && v.trim() !== "") {
    const n = Number(v);
    return Number.isFinite(n) ? n : 0;
  }
  return 0;
};
const date = (v: unknown): Date | null => {
  if (typeof v !== "string" || !v) return null;
  const d = new Date(v);
  return Number.isNaN(d.getTime()) ? null : d;
};

const MODES: readonly ChangeMode[] = ["off", "preview", "everyone", "rollout", "experiment"];
const UNITS: readonly ChangeUnit[] = ["user", "account", "visitor"];

export function modeOf(v: unknown): ChangeMode {
  const word = str(v).replace(/^CHANGE_MODE_/, "").toLowerCase();
  return (MODES as readonly string[]).includes(word) ? (word as ChangeMode) : "off";
}

export function unitOf(v: unknown): ChangeUnit {
  const word = str(v).replace(/^CHANGE_UNIT_/, "").toLowerCase();
  return (UNITS as readonly string[]).includes(word) ? (word as ChangeUnit) : "user";
}

function audienceOf(v: unknown): ChangeAudience | null {
  const a = obj(v);
  const attribute = str(a.attribute);
  return attribute ? { attribute, in: arr(a.in).map(str) } : null;
}

function splitOf(v: unknown): ChangeSplitArm[] {
  return arr(v).map((x) => ({ variant: str(obj(x).variant), weightBp: num(obj(x).weightBp) }));
}

function metricOf(v: unknown): ChangeMetric | null {
  const m = obj(v);
  const event = str(m.event);
  if (!event) return null;
  return {
    event,
    prop: str(m.prop),
    propValue: str(m.propValue),
    windowHours: num(m.windowHours),
    minSamplePerArm: num(m.minSamplePerArm),
    controlVariant: str(m.controlVariant),
  };
}

function variantsOf(v: unknown): ChangeVariant[] {
  return arr(v).map((x) => ({ name: str(obj(x).name), valueJson: str(obj(x).valueJson) }));
}

function previewOf(v: unknown): ChangePreviewEntry[] {
  return arr(v).map((x) => ({ unitHash: str(obj(x).unitHash), variant: str(obj(x).variant), label: str(obj(x).label) }));
}

function runOf(v: unknown): ChangeRun | null {
  const r = obj(v);
  if (!str(r.id)) return null;
  return {
    id: str(r.id),
    kind: str(r.kind),
    unit: unitOf(r.unit),
    audience: audienceOf(r.audience),
    split: splitOf(r.split),
    variants: variantsOf(r.variants),
    defaultVariant: str(r.defaultVariant),
    metric: metricOf(r.metric),
    startedAt: date(r.startedAt),
    endedAt: date(r.endedAt),
    endedReason: str(r.endedReason),
    startedBy: str(r.startedBy),
    approvedBy: str(r.approvedBy),
    approvalMethod: str(r.approvalMethod),
  };
}

function declarationOf(v: unknown): ChangeDeclaration | null {
  if (!v || typeof v !== "object") return null;
  const d = obj(v);
  return { type: str(d.type), defaultVariant: str(d.defaultVariant), variants: variantsOf(d.variants), description: str(d.description) };
}

export function changeOf(v: unknown): Change {
  const c = obj(v);
  return {
    id: str(c.id),
    orgId: str(c.orgId),
    environmentId: str(c.environmentId),
    key: str(c.key),
    description: str(c.description),
    mode: modeOf(c.mode),
    unit: unitOf(c.unit),
    audience: audienceOf(c.audience),
    percentBp: num(c.percentBp),
    split: splitOf(c.split),
    everyoneVariant: str(c.everyoneVariant),
    rolloutVariant: str(c.rolloutVariant),
    preview: previewOf(c.preview),
    metric: metricOf(c.metric),
    currentRun: runOf(c.currentRun),
    revision: num(c.revision),
    createdAt: date(c.createdAt),
    updatedAt: date(c.updatedAt),
    createdBy: str(c.createdBy),
    updatedBy: str(c.updatedBy),
    declaration: declarationOf(c.declaration),
    approvalRequired: c.approvalRequired === true,
  };
}

export function changeEventOf(v: unknown): ChangeEvent {
  const e = obj(v);
  const diffJson = str(e.diffJson);
  let diff: ChangeEvent["diff"] = null;
  if (diffJson) {
    try {
      const parsed: unknown = JSON.parse(diffJson);
      diff = parsed && typeof parsed === "object" && !Array.isArray(parsed) ? (parsed as ChangeEvent["diff"]) : null;
    } catch {
      diff = null;
    }
  }
  return {
    id: str(e.id),
    changeId: str(e.changeId),
    revision: num(e.revision),
    actorKind: str(e.actorKind),
    actorId: str(e.actorId),
    action: str(e.action),
    diff,
    runId: str(e.runId),
    approvalMethod: str(e.approvalMethod),
    approvedBy: str(e.approvedBy),
    createdAt: date(e.createdAt),
  };
}

// ── the readout ─────────────────────────────────────────────────────────────

const VERDICTS: readonly ChangeReadoutVerdict[] = ["no_data", "srm_failed", "inconclusive", "treatment_better", "treatment_worse"];
const REASONS: readonly ChangeReadoutReason[] = ["below_min_sample", "window_not_complete", "interval_spans_zero"];
const END_REASONS: readonly ChangeReadoutEndReason[] = ["now", "left_experiment", "run_ended"];

export function verdictOf(v: unknown): ChangeReadoutVerdict {
  const word = str(v).replace(/^CHANGE_READOUT_VERDICT_/, "").toLowerCase();
  return (VERDICTS as readonly string[]).includes(word) ? (word as ChangeReadoutVerdict) : "no_data";
}

const reasonOf = (v: unknown): ChangeReadoutReason | "" => ((REASONS as readonly string[]).includes(str(v)) ? (str(v) as ChangeReadoutReason) : "");

function armOf(v: unknown): ChangeReadoutArm {
  const a = obj(v);
  return {
    variant: str(a.variant),
    weightBp: num(a.weightBp),
    assigned: num(a.assigned),
    windowComplete: num(a.windowComplete),
    conversions: num(a.conversions),
    rate: num(a.rate),
    ciLow: num(a.ciLow),
    ciHigh: num(a.ciHigh),
    pending: num(a.pending),
    expectedAssigned: num(a.expectedAssigned),
  };
}

function comparisonOf(v: unknown): ChangeReadoutComparison {
  const c = obj(v);
  return {
    treatment: str(c.treatment),
    control: str(c.control),
    absLift: num(c.absLift),
    relLift: c.relLift === undefined || c.relLift === null ? null : num(c.relLift),
    diffCiLow: num(c.diffCiLow),
    diffCiHigh: num(c.diffCiHigh),
    pValue: num(c.pValue),
    alpha: num(c.alpha),
    significant: c.significant === true,
    verdict: verdictOf(c.verdict),
    reason: reasonOf(c.reason),
  };
}

export function readoutOf(v: unknown): ChangeReadout {
  const r = obj(v);
  const srm = obj(r.srm);
  const end = str(r.readoutEndReason);
  return {
    changeId: str(r.changeId),
    key: str(r.key),
    run: runOf(r.run),
    verdict: verdictOf(r.verdict),
    reason: reasonOf(r.reason),
    bestVariant: str(r.bestVariant),
    arms: arr(r.arms).map(armOf),
    comparisons: arr(r.comparisons).map(comparisonOf),
    srm: { pValue: num(srm.pValue), chiSquare: num(srm.chiSquare), failed: srm.failed === true, skipped: srm.skipped === true, unplannedVariants: arr(srm.unplannedVariants).map(str) },
    conflicted: num(r.conflicted),
    assigned: num(r.assigned),
    pending: num(r.pending),
    undeclaredVariants: arr(r.undeclaredVariants).map(str),
    readoutStart: date(r.readoutStart),
    readoutEnd: date(r.readoutEnd),
    readoutEndReason: (END_REASONS as readonly string[]).includes(end) ? (end as ChangeReadoutEndReason) : "",
    final: r.final === true,
    metric: metricOf(r.metric),
    alpha: num(r.alpha),
    horizonNote: str(r.horizonNote),
    truncatedByRetention: r.truncatedByRetention === true,
  };
}

// ── generated messages → console types ──────────────────────────────────────

/** A change as the console reads it; an answer without one reads as an empty change. */
export function changeFrom(change: ChangeMessage | undefined): Change {
  return changeOf(change ? toJson(ChangeSchema, change) : undefined);
}

export function changeListFrom(resp: ListChangesResponse): ChangeList {
  const r = obj(toJson(ListChangesResponseSchema, resp));
  return { changes: arr(r.changes).map(changeOf), nextPageToken: str(r.nextPageToken) };
}

export function changeEventPageFrom(resp: ListChangeEventsResponse): ChangeEventPage {
  const r = obj(toJson(ListChangeEventsResponseSchema, resp));
  return { events: arr(r.events).map(changeEventOf), nextPageToken: str(r.nextPageToken) };
}

export function readoutFrom(resp: GetChangeReadoutResponse): ChangeReadout {
  return readoutOf(toJson(GetChangeReadoutResponseSchema, resp));
}

const DELIVERY_KINDS: Record<string, ChangeDeliveryTargetKind> = {
  CHANGE_DELIVERY_TARGET_KIND_STATIC_SITE: "static-site",
  CHANGE_DELIVERY_TARGET_KIND_CONFIG_MAP: "config-map",
};

export function deliveryStatusOf(v: unknown): ChangesDeliveryStatus {
  const r = obj(v);
  return {
    revision: num(r.revision),
    targets: arr(r.targets).map((x) => {
      const t = obj(x);
      return { kind: DELIVERY_KINDS[str(t.kind)] ?? "unknown", name: str(t.name), location: str(t.location), revision: num(t.revision), deliveredAt: date(t.deliveredAt), current: t.current === true };
    }),
  };
}

export function deliveryStatusFrom(resp: GetChangesDeliveryStatusResponse): ChangesDeliveryStatus {
  return deliveryStatusOf(toJson(GetChangesDeliveryStatusResponseSchema, resp));
}

export function approvalCodeFrom(resp: RequestChangeApprovalResponse): ChangeApprovalCode {
  const r = obj(toJson(RequestChangeApprovalResponseSchema, resp));
  return { sentTo: str(r.sentTo), expiresAt: date(r.expiresAt) };
}
