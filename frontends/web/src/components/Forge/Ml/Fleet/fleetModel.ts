// Copyright (c) 2026 Reliant Labs

/**
 * The Fleet pages' view-models: pure functions from the contract's messages
 * to what the pages draw (the route, field groups in reading order, a
 * field's value as written, the utilisation heatmap, the NVLink matrix, a
 * node's workloads as Gantt rows, event counts as bars). Tested in
 * __tests__/fleetModel.test.ts.
 */

import {
  MlAxis,
  MlCollection,
  MlEventKind,
  MlFieldGroup,
  MlSubjectKind,
  type MlCluster,
  type MlEvent,
  type MlFieldValue,
  type MlNodeWorkload,
  type MlNvlinkPair,
  type MlSeries,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { fmtCompact, fmtUnit, has, subjectHref, tsMs, type GanttRow, type HeatmapRow, type MlTone } from "../kit";

// ── Routes ────────────────────────────────────────────────────────────────────

export type FleetRoute =
  | { page: "overview" }
  | { page: "pool"; poolId: string; cluster: string; pool: string }
  | { page: "node"; nodeId: string; cluster: string; node: string }
  | { page: "device"; uuid: string };

/** kit/links.ts: [] overview; ["pools", cluster, pool]; [cluster, node]; ["gpus", uuid]. */
export function parseFleetRoute(sub: readonly string[]): FleetRoute {
  if (sub[0] === "pools" && sub[1] && sub[2]) return { page: "pool", cluster: sub[1], pool: sub[2], poolId: `${sub[1]}/${sub[2]}` };
  if (sub[0] === "gpus" && sub[1]) return { page: "device", uuid: sub[1] };
  if (sub.length >= 2 && sub[0] !== "pools" && sub[0] !== "gpus") return { page: "node", cluster: sub[0], node: sub[1], nodeId: `${sub[0]}/${sub[1]}` };
  return { page: "overview" };
}

// ── Fields ────────────────────────────────────────────────────────────────────

export const FIELD_GROUP_LABELS: Partial<Record<MlFieldGroup, string>> = {
  [MlFieldGroup.UTILIZATION]: "Utilisation",
  [MlFieldGroup.PROFILING]: "Profiling pipes (DCP)",
  [MlFieldGroup.MEMORY]: "Memory & BAR1",
  [MlFieldGroup.POWER]: "Power, energy & violations",
  [MlFieldGroup.THERMAL]: "Thermals",
  [MlFieldGroup.CLOCKS]: "Clocks & clock-event reasons",
  [MlFieldGroup.ECC]: "ECC & retired pages",
  [MlFieldGroup.ROW_REMAP]: "Row remapping",
  [MlFieldGroup.PCIE]: "PCIe",
  [MlFieldGroup.NVLINK]: "NVLink",
  [MlFieldGroup.XID]: "Xid",
  [MlFieldGroup.IDENTITY]: "Identity",
  [MlFieldGroup.HOST_CPU]: "CPU",
  [MlFieldGroup.HOST_MEMORY]: "Memory",
  [MlFieldGroup.HOST_DISK]: "Disk & filesystems",
  [MlFieldGroup.HOST_NETWORK]: "Network",
  [MlFieldGroup.INFINIBAND]: "InfiniBand",
  [MlFieldGroup.KUBELET]: "Kubelet",
};

export interface FieldGroup {
  group: MlFieldGroup;
  label: string;
  fields: MlFieldValue[];
  alarms: number;
  notCollected: number;
}

/** Fields grouped in the order the enum declares (reading order), empty groups dropped. */
export function groupFields(fields: readonly MlFieldValue[]): FieldGroup[] {
  const by = new Map<MlFieldGroup, MlFieldValue[]>();
  for (const f of fields) by.set(f.group, [...(by.get(f.group) ?? []), f]);
  return [...by.entries()]
    .sort((a, b) => a[0] - b[0])
    .map(([group, fs]) => ({
      group,
      label: FIELD_GROUP_LABELS[group] ?? "Other",
      fields: fs,
      alarms: fs.filter((f) => f.alarm).length,
      notCollected: fs.filter((f) => f.collection === MlCollection.NOT_COLLECTED).length,
    }));
}

/** A field's value as written: text fields as text, integers without decimals; null when not collected. */
export function fmtField(f: Pick<MlFieldValue, "value" | "text" | "unit" | "collection">): string | null {
  if (f.collection === MlCollection.NOT_COLLECTED || f.collection === MlCollection.NOT_APPLICABLE) return null;
  if (f.text) return f.text;
  if (!has(f.value)) return null;
  if (f.unit === "" || f.unit === "count") return Number.isInteger(f.value) ? f.value.toLocaleString("en-US") : f.unit === "count" ? fmtCompact(f.value) : f.value.toFixed(2);
  if (f.unit === "ms") return `${f.value.toFixed(2)} ms`;
  if (f.unit === "kWh") return `${fmtCompact(f.value)} kWh`;
  if (f.unit === "Gb/s") return `${f.value.toFixed(f.value < 10 ? 1 : 0)} Gb/s`;
  return fmtUnit(f.value, f.unit);
}

export const fieldOf = (fields: readonly MlFieldValue[], key: string) => fields.find((f) => f.key === key);

// ── Utilisation ───────────────────────────────────────────────────────────────

/** A cell's fill for a 0–1 utilisation (one hue, by alpha); null is "no sample". */
export function utilFill(v: number | null | undefined): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return "repeating-linear-gradient(135deg, transparent 0 2px, hsl(var(--border)) 2px 3px)";
  return `hsl(var(--chart-1) / ${(0.08 + 0.92 * Math.min(1, Math.max(0, v))).toFixed(2)})`;
}

/** Series sharing a key, one per group, as heatmap rows (time columns), plus the column labels. */
export function heatmapOf(series: readonly MlSeries[], hrefOf?: (s: MlSeries) => string | null): { rows: HeatmapRow[]; columns: string[] } {
  const first = series.find((s) => s.x.length > 0);
  if (!first) return { rows: [], columns: [] };
  const columns = first.x.map((x) => new Date(x * 1000).toLocaleTimeString("en-US", { hour: "2-digit", minute: "2-digit", hour12: false }));
  const rows = series.map((s) => ({
    id: s.group || s.label,
    label: s.group || s.label,
    cells: first.x.map((x) => {
      const i = s.x.indexOf(x);
      return i >= 0 && Number.isFinite(s.y[i]) ? s.y[i] : null;
    }),
    href: hrefOf?.(s) ?? undefined,
  }));
  return { rows, columns };
}

/** The series of `key` grouped by `group` label (a node's per-device series). */
export function byGroup(series: readonly MlSeries[], key: string): Map<string, MlSeries> {
  return new Map(series.filter((s) => s.key === key).map((s) => [s.group, s]));
}

// ── Events as counts ──────────────────────────────────────────────────────────

const EVENT_BUCKETS: { label: string; kinds: MlEventKind[] }[] = [
  { label: "Xid", kinds: [MlEventKind.XID] },
  { label: "ECC / row remap", kinds: [MlEventKind.ECC, MlEventKind.ROW_REMAP] },
  { label: "Throttle", kinds: [MlEventKind.THROTTLE] },
  { label: "NVLink / NPD", kinds: [MlEventKind.LINK_FLAP, MlEventKind.NPD_CONDITION] },
];

/** Hardware events counted per bucket of `bucketHours` over the last `hours`, one bar series per kind. */
export function eventCountSeries(events: readonly MlEvent[], nowMs: number, hours = 48, bucketHours = 3): MlSeries[] {
  const n = Math.ceil(hours / bucketHours);
  const start = nowMs - hours * 3600_000;
  const x = Array.from({ length: n }, (_, i) => Math.floor((start + i * bucketHours * 3600_000) / 1000));
  return EVENT_BUCKETS.map((b) => {
    const y = new Array<number>(n).fill(0);
    for (const e of events) {
      if (!b.kinds.includes(e.kind)) continue;
      const t = tsMs(e.at);
      if (t === null || t < start || t > nowMs) continue;
      y[Math.min(n - 1, Math.floor((t - start) / (bucketHours * 3600_000)))]++;
    }
    return { key: "fleet.events", label: b.label, group: b.label, unit: "count", axis: MlAxis.TIME, x, y, collection: MlCollection.COLLECTED } as unknown as MlSeries;
  });
}

// ── NVLink matrix ─────────────────────────────────────────────────────────────

export interface NvlinkCell {
  state: "self" | "up" | "degraded" | "down" | "none";
  pair?: MlNvlinkPair;
}

/** n × n cells (symmetric) from the node's pairs. */
export function nvlinkMatrix(pairs: readonly MlNvlinkPair[], n: number): NvlinkCell[][] {
  const m: NvlinkCell[][] = Array.from({ length: n }, (_, a) => Array.from({ length: n }, (_, b) => ({ state: a === b ? "self" : "none" }) as NvlinkCell));
  for (const p of pairs) {
    const state = p.state === "down" ? "down" : p.state === "degraded" ? "degraded" : "up";
    if (p.fromIndex < n && p.toIndex < n) {
      m[p.fromIndex][p.toIndex] = { state, pair: p };
      m[p.toIndex][p.fromIndex] = { state, pair: p };
    }
  }
  return m;
}

export const nvlinkTone = (s: NvlinkCell["state"]): MlTone => (s === "down" ? "danger" : s === "degraded" ? "warning" : "neutral");

// ── Workloads ─────────────────────────────────────────────────────────────────

/** A node's (or device's) workloads as Gantt rows, one per pod, newest last; spans clipped to the window. */
export function workloadRows(workloads: readonly MlNodeWorkload[], fromMs: number): GanttRow[] {
  // One row per pod: a pod's per-GPU records (one per rank) merge, so 8 ranks of one attempt are one bar.
  const byPod = new Map<string, MlNodeWorkload[]>();
  for (const w of workloads) {
    const key = w.pod;
    byPod.set(key, [...(byPod.get(key) ?? []), w]);
  }
  return [...byPod.values()].map((group, i) => {
    const w = group[0];
    const starts = group.map((g) => tsMs(g.start) ?? fromMs);
    const ends = group.map((g) => tsMs(g.end));
    const start = Math.max(fromMs, Math.min(...starts));
    const end = ends.some((e) => e === null) ? null : Math.max(...(ends as number[]));
    // A pod of ranks opens its attempt (a rank id less its last segment).
    const subject = w.subject?.kind === MlSubjectKind.RANK ? { kind: MlSubjectKind.ATTEMPT, id: w.subject.id.split("/").slice(0, -1).join("/") } : w.subject;
    const href = subject ? subjectHref(subject) : null;
    const devices = [...new Set(group.flatMap((g) => g.deviceIndexes))].sort((x, y) => x - y);
    const gpus = devices.length;
    const label = (w.subject?.label || w.pod).replace(/ · rank \d+$/, "").replace(/ rank \d+$/, "");
    return {
      id: `${w.pod}#${i}`,
      label,
      sub: `${w.pod.split("/").pop()} · ${gpus === 8 ? "all 8 GPUs" : `GPU ${devices.join(", ")}`}`,
      spans: [{ start, end, state: GANTT_KIND[w.kind] ?? "other", label: gpus > 1 ? `${gpus} GPUs` : "", title: `${label} (${w.kind})`, href: href ?? undefined }],
    };
  });
}

const GANTT_KIND: Record<string, string> = { training: "training", inference: "inference", eval: "eval", notebook: "notebook", other: "other" };

// ── Fleet summary ─────────────────────────────────────────────────────────────

/** Allocated share, and the money idle allocation costs a day. */
export function allocShare(allocated: number, total: number): number {
  return total > 0 ? allocated / total : 0;
}

/** "1 cluster", "2 pools". */
export const plural = (n: number, word: string): string => `${n} ${word}${n === 1 ? "" : "s"}`;

/** A cluster whose newest inventory row is older than this is stale (the B1 contract: 10 min). */
export const STALE_AFTER_MS = 10 * 60_000;

/** Whether a cluster stopped reporting: last seen more than STALE_AFTER_MS before `nowMs` (never seen counts). */
export function isStale(cluster: Pick<MlCluster, "lastSeenAt">, nowMs: number): boolean {
  const seen = tsMs(cluster.lastSeenAt);
  return seen === null || nowMs - seen > STALE_AFTER_MS;
}

/** Whether a group's fields exist and none was collected: the whole group is "not collected". */
export function groupUncollected(fields: readonly MlFieldValue[], group: MlFieldGroup): boolean {
  const fs = fields.filter((f) => f.group === group);
  return fs.length > 0 && fs.every((f) => f.collection === MlCollection.NOT_COLLECTED);
}

/** Whether every field of `group` is not applicable to this device (an L4 has no NVLink). */
export function groupNotApplicable(fields: readonly MlFieldValue[], group: MlFieldGroup): boolean {
  const fs = fields.filter((f) => f.group === group);
  return fs.length > 0 && fs.every((f) => f.collection === MlCollection.NOT_APPLICABLE);
}

/** Device tile tone in a pool grid: the device's health dot, or none. */
export function deviceTone(health: number): MlTone | undefined {
  return health === 3 ? "danger" : health === 2 ? "warning" : undefined;
}
