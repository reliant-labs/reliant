// Copyright (c) 2025 Reliant Labs

/**
 * THE WIRE TO controlplane.v1.ChangesService, hand-written for now: the
 * protojson readers (the call itself is changesTransport.ts).
 *
 * WHY NOT A GENERATED CLIENT. Every other control-plane call goes through
 * `getControlPlaneClient(Service)` over clients generated from the vendored
 * protos (proto-vendor/controlplane, synced from control-plane main's
 * proto/public-api.txt). ChangesService is not on control-plane main yet, and
 * `task contract:controlplane:check` refuses a vendored proto its SOURCE
 * commit does not export — so it cannot be vendored ahead of the merge. Until
 * it is, this module speaks the Connect protocol's unary JSON form itself
 * (POST /controlplane.v1.ChangesService/<Method>, protojson bodies: camelCase
 * fields, enums by name, int64 as strings), with the same base URL and bearer
 * token the shared transport uses.
 *
 * WHEN THE PROTO IS VENDORED, replace changesTransport.ts's `callChanges` with the generated
 * client (`getControlPlaneClient(ChangesService)`) and the readers here
 * with the generated messages. ChangesApi (changesApi.ts) and everything
 * above it stays as it is.
 *
 * A failure is a ConnectError carrying the response headers as metadata, so
 * `hasErrorReason(err, "change_revision_conflict")` reads the server's
 * `x-forge-error-reason` exactly as it does for a generated client.
 */

import type {
  Change,
  ChangeAudience,
  ChangeDeclaration,
  ChangeEvent,
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

export function unitEnum(unit: ChangeUnit): string {
  return `CHANGE_UNIT_${unit.toUpperCase()}`;
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
