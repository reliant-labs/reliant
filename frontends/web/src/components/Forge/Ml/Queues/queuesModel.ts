// Copyright (c) 2026 Reliant Labs

/**
 * What the Queues page draws, as pure functions of GetMlQueues: the header
 * numbers, one row per cluster queue × flavor, the pending workloads by
 * classified reason, and pod-start stages as bar segments and Gantt rows
 * (also used by Inference's cold start).
 */

import type { MlClusterQueue, MlPendingReason, MlPendingWorkload, MlPodStart, MlPodStartWaterfall, MlQueuesSummary } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import type { BarSegment, GanttRow, GanttStateStyle } from "../kit";
import { pendingReasonOf } from "../kit/labels";

/** A wait, short: "0s", "41s", "3m 10s", "18m", "1h 12m", "2d 4h". */
export function fmtWait(seconds: number | undefined | null): string {
  if (seconds === undefined || seconds === null || !Number.isFinite(seconds)) return "—";
  const s = Math.max(0, Math.round(seconds));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 10) return s % 60 ? `${m}m ${s % 60}s` : `${m}m`;
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 48) return m % 60 ? `${h}h ${m % 60}m` : `${h}h`;
  return `${Math.floor(h / 24)}d ${h % 24}h`;
}

export interface QueueRow {
  key: string;
  queue: MlClusterQueue;
  flavor: string;
  resource: string;
  nominal: number;
  used: number;
  borrowed: number;
  lendable: number;
  borrowingLimit: number;
  /** used / (nominal + borrowing limit): how full it can get. */
  fill: number;
}

/** One row per (queue, flavor): most queues have one flavor. */
export function queueRows(queues: readonly MlClusterQueue[]): QueueRow[] {
  return queues.flatMap((q) =>
    q.flavors.map((f) => ({
      key: q.flavors.length > 1 ? `${q.id}#${f.flavor}` : q.id,
      queue: q,
      flavor: f.flavor,
      resource: f.resource,
      nominal: f.nominal,
      used: f.used,
      borrowed: f.borrowed,
      lendable: f.lendable,
      borrowingLimit: f.borrowingLimit,
      fill: f.nominal + f.borrowingLimit > 0 ? f.used / (f.nominal + f.borrowingLimit) : 0,
    })),
  );
}

/** The stable key a reason filter uses in the URL (`?filter=insufficient-quota`). */
export const reasonKey = (reason: MlPendingReason) => pendingReasonOf(reason).label.toLowerCase().replace(/\s+/g, "-");

export interface ReasonCount {
  reason: MlPendingReason;
  key: string;
  label: string;
  workloads: number;
  gpus: number;
}

/** Pending workloads by classified reason, most GPUs first. */
export function reasonCounts(pending: readonly MlPendingWorkload[]): ReasonCount[] {
  const by = new Map<MlPendingReason, ReasonCount>();
  for (const p of pending) {
    const row = by.get(p.reason) ?? { reason: p.reason, key: reasonKey(p.reason), label: pendingReasonOf(p.reason).label, workloads: 0, gpus: 0 };
    row.workloads++;
    row.gpus += p.gpus;
    by.set(p.reason, row);
  }
  return [...by.values()].sort((a, b) => b.gpus - a.gpus);
}

/** The pending workloads a reason filter keeps (all when unset or unknown). */
export function filterPending(pending: readonly MlPendingWorkload[], filter: string | undefined): MlPendingWorkload[] {
  if (!filter || !pending.some((p) => reasonKey(p.reason) === filter)) return [...pending];
  return pending.filter((p) => reasonKey(p.reason) === filter);
}

/** What the selection (`?sel`) names: a queue, a pending workload, or nothing. */
export function selectionOf(sel: string | undefined, queues: readonly MlClusterQueue[], pending: readonly MlPendingWorkload[]): { queue?: MlClusterQueue; workload?: MlPendingWorkload } {
  if (!sel) return {};
  const queue = queues.find((q) => q.id === sel);
  if (queue) return { queue };
  const workload = pending.find((p) => p.subject?.id === sel || p.name === sel);
  return workload ? { workload, queue: queues.find((q) => q.id === workload.queueId) } : {};
}

export function summaryNumbers(s: MlQueuesSummary | undefined) {
  return {
    pending: s?.pending ?? 0,
    demanded: s?.gpusDemanded ?? 0,
    free: s?.gpusFree ?? 0,
    shortfall: Math.max(0, (s?.gpusDemanded ?? 0) - (s?.gpusFree ?? 0)),
    waitP50: s?.waitP50Seconds ?? NaN,
    waitP95: s?.waitP95Seconds ?? NaN,
    preemptions: s?.preemptions24h ?? 0,
    lost: s?.gpuHoursLost24h ?? NaN,
  };
}

// ── Pod start ─────────────────────────────────────────────────────────────────

/** The stages' colours, in the order a pod goes through them. */
export const POD_START_STATES: Record<string, GanttStateStyle> = {
  queued: { label: "Queued (Kueue)", fill: "hsl(var(--muted-foreground) / 0.35)", hatched: true },
  scheduling: { label: "Scheduling", fill: "hsl(var(--chart-6))" },
  "node provisioning": { label: "Node provisioning", fill: "hsl(var(--chart-5))" },
  "sandbox/init": { label: "Sandbox / init", fill: "hsl(var(--chart-4))" },
  "image pull": { label: "Image pull", fill: "hsl(var(--chart-3))" },
  "model load": { label: "Model load", fill: "hsl(var(--chart-2))" },
  ready: { label: "Ready", fill: "hsl(var(--chart-1))" },
};

const STAGE_COLOR: Record<string, number> = { scheduling: 5, "node provisioning": 4, "sandbox/init": 3, "image pull": 2, "model load": 1, ready: 0 };

/** Pod-start stages as bar segments by p50 (empty stages dropped). */
export function podStartSegments(ps: MlPodStart | undefined): BarSegment[] {
  if (!ps) return [];
  return ps.stages
    .filter((s) => s.p50Seconds > 0)
    .map((s) => ({ label: s.stage, value: s.p50Seconds, ...(s.stage === "queued" ? { tone: "neutral" as const } : { color: STAGE_COLOR[s.stage] ?? 0 }), title: `${s.stage}: p50 ${s.p50Seconds}s, p95 ${s.p95Seconds}s — ${s.topCause}` }));
}

/** The stage that costs most at p50. */
export function topStage(ps: MlPodStart | undefined) {
  if (!ps || ps.stages.length === 0) return undefined;
  return [...ps.stages].sort((a, b) => b.p50Seconds - a.p50Seconds)[0];
}

/** Waterfalls as Gantt rows on a seconds axis (offsets from each pod's creation). */
export function podStartGantt(waterfalls: readonly MlPodStartWaterfall[], labelOf?: (w: MlPodStartWaterfall) => { label: string; sub?: string; href?: string }): { rows: GanttRow[]; to: number } {
  let to = 1;
  const rows = waterfalls.map((w) => {
    const l = labelOf?.(w) ?? { label: w.pod };
    const spans = w.stages
      .filter((s) => s.seconds > 0)
      .map((s) => {
        to = Math.max(to, s.startSeconds + s.seconds);
        return { start: s.startSeconds, end: s.startSeconds + s.seconds, state: s.stage in POD_START_STATES ? s.stage : "ready", title: `${s.stage}: ${Math.round(s.seconds)}s${s.detail ? ` — ${s.detail}` : ""}`, href: l.href };
      });
    return { id: w.pod, label: l.label, sub: l.sub, spans };
  });
  return { rows, to };
}
