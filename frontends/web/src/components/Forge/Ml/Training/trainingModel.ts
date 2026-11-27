// Copyright (c) 2026 Reliant Labs

/**
 * The Training pages' view-models: pure functions from the contract's
 * messages (MlRun, GetMlRunResponse, MlEvent, …) to what a page draws. No
 * React and no fetching here, so every number a page shows is tested in
 * __tests__/trainingModel.test.ts.
 */

import {
  MlAttemptEndReason,
  MlCheckpointStatus,
  MlCostCategory,
  MlPhaseState,
  MlRunState,
  type GetMlRunResponse,
  type MlAttempt,
  type MlCheckpoint,
  type MlCost,
  type MlCostCell,
  type MlEvent,
  type MlExperiment,
  type MlPhase,
  type MlRank,
  type MlRun,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { num, tsMs } from "../kit/format";
import { costCategoryOf, endReasonOf, type MlTone } from "../kit/labels";

// ── Attempts and phases ───────────────────────────────────────────────────────

export const isRunning = (a: MlAttempt) => a.endReason === MlAttemptEndReason.UNSPECIFIED && a.state !== MlPhaseState.QUEUED;
export const isFailure = (a: MlAttempt) =>
  a.endReason !== MlAttemptEndReason.UNSPECIFIED && a.endReason !== MlAttemptEndReason.COMPLETED && a.endReason !== MlAttemptEndReason.CANCELLED;
export const activeRun = (r: MlRun) => r.state === MlRunState.RUNNING || r.state === MlRunState.DEGRADED;

/** The milliseconds of [a, b] that fall in [from, to]. */
export function overlapMs(a: number, b: number, from: number, to: number): number {
  return Math.max(0, Math.min(b, to) - Math.max(a, from));
}

/** The attempt's end, or `now` while it lasts. */
export const attemptEndMs = (a: MlAttempt, now: number) => tsMs(a.endedAt) ?? now;

// ── The list ──────────────────────────────────────────────────────────────────

export interface ListKpis {
  activeRuns: number;
  queuedRuns: number;
  gpusInUse: number;
  goodput: number;
  gpuHours24h: number;
  usd24h: number;
  failures7d: number;
  restarts7d: number;
}

/**
 * The Training list's header numbers. GPU-hours in the last 24h are every
 * span that held accelerators (anything but queued) overlapping the window,
 * priced at its attempt's own GPU-hour price.
 */
export function listKpis(runs: readonly MlRun[], now: number): ListKpis {
  const dayAgo = now - 24 * 3600_000;
  const weekAgo = now - 7 * 24 * 3600_000;
  let gpuHours = 0;
  let usd = 0;
  let failures = 0;
  let restarts = 0;
  let gpus = 0;
  let productive = 0;
  let allocated = 0;
  for (const run of runs) {
    for (const phase of run.phases) {
      for (const a of phase.attempts) {
        const price = a.cost && a.cost.gpuHours > 0 ? a.cost.usd / a.cost.gpuHours : 0;
        let ms = 0;
        for (const s of a.spans) {
          if (s.state === "queued" || s.state === "pending") continue;
          const start = tsMs(s.start);
          if (start === null) continue;
          ms += overlapMs(start, tsMs(s.end) ?? now, dayAgo, now);
        }
        const h = (ms / 3600_000) * a.worldSize;
        gpuHours += h;
        usd += h * price;
        const ended = tsMs(a.endedAt);
        if (ended !== null && ended >= weekAgo && isFailure(a)) failures++;
        const queued = tsMs(a.queuedAt);
        if (a.n > 1 && queued !== null && queued >= weekAgo) restarts++;
        if (isRunning(a) && activeRun(run) && a.spans.some((s) => !s.end && s.state !== "queued")) gpus += a.worldSize;
      }
    }
    if (activeRun(run) && run.cost) {
      productive += run.cost.byCategory.find((c) => c.category === MlCostCategory.PRODUCTIVE)?.gpuHours ?? 0;
      allocated += run.cost.gpuHours;
    }
  }
  return {
    activeRuns: runs.filter(activeRun).length,
    queuedRuns: runs.filter((r) => r.state === MlRunState.QUEUED).length,
    gpusInUse: gpus,
    goodput: allocated > 0 ? productive / allocated : Number.NaN,
    gpuHours24h: gpuHours,
    usd24h: usd,
    failures7d: failures,
    restarts7d: restarts,
  };
}

/** Runs matching the list's filters: an experiment (`filter`) and a free-text search over name, owner, tags, params and sweep. */
export function filterRuns(runs: readonly MlRun[], filter: string | undefined, q: string | undefined): MlRun[] {
  const needle = (q ?? "").trim().toLowerCase();
  return runs.filter((r) => {
    if (filter && r.experimentId !== filter) return false;
    if (!needle) return true;
    const hay = [r.name, r.owner, r.sweepId, r.template, ...r.tags, ...Object.entries(r.params).map(([k, v]) => `${k}=${v}`)].join(" ").toLowerCase();
    return needle.split(/\s+/).every((t) => hay.includes(t));
  });
}

export interface RunGroup {
  key: string;
  label: string;
  runs: MlRun[];
}

/** Runs grouped by experiment (in the experiments' order), or one group. */
export function groupRuns(runs: readonly MlRun[], experiments: readonly MlExperiment[], group: string | undefined): RunGroup[] {
  if (group !== "experiment") return [{ key: "all", label: "", runs: [...runs] }];
  const order = experiments.map((e) => e.id);
  const byExp = new Map<string, MlRun[]>();
  for (const r of runs) byExp.set(r.experimentId, [...(byExp.get(r.experimentId) ?? []), r]);
  return [...byExp.entries()]
    .sort(([a], [b]) => (order.indexOf(a) === -1 ? 99 : order.indexOf(a)) - (order.indexOf(b) === -1 ? 99 : order.indexOf(b)))
    .map(([key, rs]) => ({ key, label: experiments.find((e) => e.id === key)?.name ?? key, runs: rs }));
}

/** A run's mini-Gantt: each attempt as a share of the run's lifetime [first queue, end or now]. */
export interface MiniSegment {
  phaseKey: string;
  phaseIndex: number;
  n: number;
  left: number;
  width: number;
  failed: boolean;
  running: boolean;
  queued: boolean;
  endLabel: string;
}

export function miniGantt(run: MlRun, now: number): MiniSegment[] {
  const attempts = run.phases.flatMap((p, pi) => p.attempts.map((a) => ({ p, pi, a })));
  const starts = attempts.map(({ a }) => tsMs(a.queuedAt)).filter((v): v is number => v !== null);
  if (!starts.length) return [];
  const from = Math.min(...starts);
  const to = Math.max(from + 1, ...attempts.map(({ a }) => attemptEndMs(a, now)));
  return attempts.map(({ p, pi, a }) => {
    const s = tsMs(a.queuedAt) ?? from;
    const e = attemptEndMs(a, now);
    return {
      phaseKey: p.key,
      phaseIndex: pi,
      n: a.n,
      left: (s - from) / (to - from),
      width: Math.max(0.004, (e - s) / (to - from)),
      failed: isFailure(a),
      running: isRunning(a),
      queued: a.state === MlPhaseState.QUEUED,
      endLabel: endReasonOf(a.endReason).label,
    };
  });
}

// ── The run detail ────────────────────────────────────────────────────────────

export interface FailureCard {
  phaseKey: string;
  failed: MlAttempt;
  next: MlAttempt;
  stepsLost: number;
  stages: { stage: string; seconds: number; detail: string }[];
  totalSeconds: number;
  gpuHoursLost: number;
  usdLost: number;
}

/** Every attempt that ended in failure and the attempt that recovered from it, oldest first. */
export function failureCards(run: MlRun): FailureCard[] {
  const out: FailureCard[] = [];
  for (const p of run.phases) {
    const sorted = [...p.attempts].sort((a, b) => a.n - b.n);
    sorted.forEach((a, i) => {
      const next = sorted[i + 1];
      if (!next?.recovery || !isFailure(a)) return;
      const r = next.recovery;
      out.push({ phaseKey: p.key, failed: a, next, stepsLost: num(a.stepsLost), stages: r.stages.map((s) => ({ stage: s.stage, seconds: s.seconds, detail: s.detail })), totalSeconds: r.totalSeconds, gpuHoursLost: r.gpuHoursLost, usdLost: r.usdLost });
    });
  }
  return out;
}

/** What failures cost the run: the sum of every recovery's GPU-hours and dollars lost. */
export function lostToFailures(run: MlRun): { gpuHours: number; usd: number; count: number } {
  const cards = failureCards(run);
  return { gpuHours: cards.reduce((s, c) => s + c.gpuHoursLost, 0), usd: cards.reduce((s, c) => s + c.usdLost, 0), count: cards.length };
}

/** The order "where the time went" draws categories in: productive first, then waste by kind. */
export const COST_ORDER: readonly MlCostCategory[] = [
  MlCostCategory.PRODUCTIVE,
  MlCostCategory.QUEUED_HOLDING,
  MlCostCategory.STARTUP,
  MlCostCategory.RESTART_LOST,
  MlCostCategory.CHECKPOINT,
  MlCostCategory.STALLED,
  MlCostCategory.IDLE_ALLOCATED,
];

const COST_COLOR: Partial<Record<MlCostCategory, number>> = {
  [MlCostCategory.PRODUCTIVE]: 0,
  [MlCostCategory.QUEUED_HOLDING]: 5,
  [MlCostCategory.STARTUP]: 1,
  [MlCostCategory.RESTART_LOST]: 4,
  [MlCostCategory.CHECKPOINT]: 2,
  [MlCostCategory.STALLED]: 3,
  [MlCostCategory.IDLE_ALLOCATED]: 5,
};

export interface CostSegment {
  category: MlCostCategory;
  label: string;
  gpuHours: number;
  usd: number;
  share: number;
  color: number;
  tone?: MlTone;
}

/** A cost's categories in COST_ORDER with their shares (goodput is the productive share). */
export function costSegments(cost: MlCost | undefined): CostSegment[] {
  if (!cost) return [];
  const total = cost.byCategory.reduce((s, c) => s + c.gpuHours, 0);
  return COST_ORDER.flatMap((cat) => {
    const slice = cost.byCategory.find((c) => c.category === cat);
    if (!slice || slice.gpuHours <= 0) return [];
    return [{ category: cat, label: costCategoryOf(cat).label, gpuHours: slice.gpuHours, usd: slice.usd, share: total > 0 ? slice.gpuHours / total : 0, color: COST_COLOR[cat] ?? 5 }];
  });
}

export interface CostRow {
  key: string;
  phaseKey: string;
  attempt: number;
  byCategory: Partial<Record<MlCostCategory, { gpuHours: number; usd: number }>>;
  gpuHours: number;
  usd: number;
}

/** The cost table: one row per phase × attempt, in the run's phase order, plus the categories it uses. */
export function costTable(cells: readonly MlCostCell[], phaseOrder: readonly string[]): { rows: CostRow[]; categories: MlCostCategory[]; total: { gpuHours: number; usd: number } } {
  const rows = new Map<string, CostRow>();
  for (const c of cells) {
    const key = `${c.phaseKey}/${c.attempt}`;
    const row = rows.get(key) ?? { key, phaseKey: c.phaseKey, attempt: c.attempt, byCategory: {}, gpuHours: 0, usd: 0 };
    const prev = row.byCategory[c.category] ?? { gpuHours: 0, usd: 0 };
    row.byCategory[c.category] = { gpuHours: prev.gpuHours + c.gpuHours, usd: prev.usd + c.usd };
    row.gpuHours += c.gpuHours;
    row.usd += c.usd;
    rows.set(key, row);
  }
  const used = new Set(cells.map((c) => c.category));
  const sorted = [...rows.values()].sort((a, b) => phaseOrder.indexOf(a.phaseKey) - phaseOrder.indexOf(b.phaseKey) || a.attempt - b.attempt);
  return {
    rows: sorted,
    categories: COST_ORDER.filter((c) => used.has(c)),
    total: { gpuHours: sorted.reduce((s, r) => s + r.gpuHours, 0), usd: sorted.reduce((s, r) => s + r.usd, 0) },
  };
}

/** The cost a scope draws: the attempt's, the phase's, or the run's. */
export function scopeCost(run: MlRun, phase: MlPhase | null, attempt: MlAttempt | null, phaseScoped: boolean): MlCost | undefined {
  if (attempt) return attempt.cost;
  if (phase && phaseScoped) return phase.cost;
  return run.cost;
}

// ── Checkpoints ───────────────────────────────────────────────────────────────

export interface CheckpointFacts {
  lastDurableStep: number | null;
  lastDurableAt: number | null;
  atRiskSteps: number | null;
  cadenceSteps: number | null;
  cadenceSeconds: number | null;
  storageBytes: number;
  durable: number;
  partial: number;
}

/** The checkpoint facts of a phase: the last durable one, the steps at risk now, the cadence, storage held. */
export function checkpointFacts(checkpoints: readonly MlCheckpoint[], phase: MlPhase | null): CheckpointFacts {
  const mine = checkpoints.filter((c) => !phase || c.phaseKey === phase.key);
  const durable = mine.filter((c) => c.status === MlCheckpointStatus.DURABLE).sort((a, b) => num(b.step) - num(a.step));
  const last = durable[0];
  const diffs: number[] = [];
  const tdiffs: number[] = [];
  for (let i = 0; i + 1 < Math.min(durable.length, 8); i++) {
    diffs.push(num(durable[i].step) - num(durable[i + 1].step));
    const a = tsMs(durable[i].savedAt);
    const b = tsMs(durable[i + 1].savedAt);
    if (a !== null && b !== null) tdiffs.push((a - b) / 1000);
  }
  const med = (xs: number[]) => (xs.length ? [...xs].sort((x, y) => x - y)[Math.floor(xs.length / 2)] : null);
  const current = phase ? num(phase.step) : null;
  return {
    lastDurableStep: last ? num(last.step) : null,
    lastDurableAt: last ? tsMs(last.savedAt) : null,
    atRiskSteps: last && current !== null && phase?.state === MlPhaseState.RUNNING ? Math.max(0, current - num(last.step)) : null,
    cadenceSteps: med(diffs),
    cadenceSeconds: med(tdiffs),
    storageBytes: mine.filter((c) => c.status !== MlCheckpointStatus.DELETED).reduce((s, c) => s + num(c.sizeBytes), 0),
    durable: durable.length,
    partial: mine.filter((c) => c.status === MlCheckpointStatus.PARTIAL).length,
  };
}

/** The checkpoints a scope lists: the phase's (when scoped), the attempt's (when one is). */
export function scopeCheckpoints(checkpoints: readonly MlCheckpoint[], phase: MlPhase | null, attempt: MlAttempt | null, phaseScoped: boolean): MlCheckpoint[] {
  return checkpoints.filter((c) => (!phaseScoped || !phase || c.phaseKey === phase.key) && (!attempt || c.attempt === attempt.n));
}

/** The evaluated checkpoints of the run, newest first (they carry eval scores). */
export function evaluatedCheckpoints(checkpoints: readonly MlCheckpoint[]): MlCheckpoint[] {
  return checkpoints.filter((c) => Object.keys(c.evalScores).length > 0).sort((a, b) => num(b.step) - num(a.step));
}

/** Eval score vs step per task, from the evaluated checkpoints (oldest first). */
export function evalCurves(checkpoints: readonly MlCheckpoint[]): { task: string; x: number[]; y: number[] }[] {
  const evaluated = evaluatedCheckpoints(checkpoints).reverse();
  const tasks = [...new Set(evaluated.flatMap((c) => Object.keys(c.evalScores)))];
  return tasks.map((task) => {
    const pts = evaluated.filter((c) => Number.isFinite(c.evalScores[task]));
    return { task, x: pts.map((c) => num(c.step)), y: pts.map((c) => c.evalScores[task]) };
  });
}

// ── Mesh ──────────────────────────────────────────────────────────────────────

export interface MeshCell {
  rank: number;
  dp: number;
  tp: number;
  rankRow?: MlRank;
  straggler: boolean;
  /** Step compute vs the median, as a ratio (1.18 = 18% slower). */
  computeRatio: number | null;
}

/** The dp × tp grid of a mesh, each cell its rank (dp-major: rank = dp × tp_size + tp). */
export function meshGrid(dp: number, tp: number, ranks: readonly MlRank[]): MeshCell[][] {
  const computes = ranks.map((r) => r.computeMsP50).filter((v) => Number.isFinite(v) && v > 0).sort((a, b) => a - b);
  const median = computes.length ? computes[Math.floor(computes.length / 2)] : 0;
  return Array.from({ length: Math.max(1, dp) }, (_, d) =>
    Array.from({ length: Math.max(1, tp) }, (_, t) => {
      const rank = d * tp + t;
      const row = ranks.find((r) => r.rank === rank);
      return { rank, dp: d, tp: t, rankRow: row, straggler: !!row?.straggler, computeRatio: row && median ? row.computeMsP50 / median : null };
    }),
  );
}

/** The ranks of the scope: the attempt's, else the latest attempt of the phase that carries ranks. */
export function scopeRanks(phase: MlPhase | null, attempt: MlAttempt | null): { attempt: MlAttempt | null; ranks: MlRank[] } {
  if (attempt?.ranks.length) return { attempt, ranks: attempt.ranks };
  const withRanks = [...(phase?.attempts ?? [])].filter((a) => a.ranks.length).sort((a, b) => b.n - a.n)[0];
  return { attempt: withRanks ?? null, ranks: withRanks?.ranks ?? [] };
}

// ── Events ────────────────────────────────────────────────────────────────────

/**
 * One timeline from several domains' answers: the same moment reported twice
 * (the run's Xid and the fleet's) is kept once, newest first.
 */
export function mergeEvents(events: readonly MlEvent[]): MlEvent[] {
  const seen = new Set<string>();
  const out: MlEvent[] = [];
  for (const e of [...events].sort((a, b) => (tsMs(b.at) ?? 0) - (tsMs(a.at) ?? 0))) {
    const key = `${e.kind}|${e.subject?.id ?? ""}|${Math.round((tsMs(e.at) ?? 0) / 120_000)}`;
    if (seen.has(key) || seen.has(e.id)) continue;
    seen.add(key);
    if (e.id) seen.add(e.id);
    out.push(e);
  }
  return out;
}

// ── Leaderboard ───────────────────────────────────────────────────────────────

export interface ExperimentRow {
  experiment: MlExperiment;
  best: MlRun | undefined;
  runs: number;
}

export function leaderboard(experiments: readonly MlExperiment[], runs: readonly MlRun[]): ExperimentRow[] {
  return experiments.map((e) => ({ experiment: e, best: runs.find((r) => r.id === e.bestRunId), runs: e.runCount }));
}

/** The scope's phase key for series reads: a run-wide scope reads its current phase (steps restart per phase). */
export function seriesPhase(detail: GetMlRunResponse, phase: MlPhase | null): MlPhase | null {
  return phase ?? detail.run?.phases.find((p) => p.key === detail.run?.currentPhaseKey) ?? null;
}

/** What the run page's header numbers are about: the whole run, one phase, or one attempt. */
export interface ScopedNumbers {
  /** "whole run", "sft (all attempts)", "sft attempt 3". */
  scopeLabel: string;
  step: number;
  totalSteps: number;
  /** Seconds the scope ran (an attempt or a finished phase); null for a live run or phase, which has an ETA instead. */
  ranSeconds: number | null;
  etaSeconds: number | null;
  accelerators: number;
  goodput: number;
  gpuHours: number;
  usd: number;
  lost: { gpuHours: number; usd: number; count: number };
}

/**
 * The header's numbers for `scope` (runScope.ts): the run's own totals, a
 * phase's (its attempts summed, its failures), or an attempt's (its steps, its
 * world size, its cost, and the failure that ended it).
 */
export function scopedNumbers(run: MlRun, phase: MlPhase | null, attempt: MlAttempt | null, phaseScoped: boolean, now: number): ScopedNumbers {
  const cards = failureCards(run);
  const sum = (cs: FailureCard[]) => ({ gpuHours: cs.reduce((s, c) => s + c.gpuHoursLost, 0), usd: cs.reduce((s, c) => s + c.usdLost, 0), count: cs.length });
  if (phase && attempt) {
    const start = tsMs(attempt.runningAt) ?? tsMs(attempt.queuedAt);
    const end = tsMs(attempt.endedAt);
    const productive = attempt.cost?.byCategory.find((c) => c.category === MlCostCategory.PRODUCTIVE)?.gpuHours ?? Number.NaN;
    const live = end === null;
    return {
      scopeLabel: `${phase.key} attempt ${attempt.n}`,
      step: num(attempt.endStep),
      totalSteps: num(phase.totalSteps),
      ranSeconds: start ? ((end ?? now) - start) / 1000 : null,
      etaSeconds: live && run.currentPhaseKey === phase.key && run.etaSeconds !== undefined ? run.etaSeconds : null,
      accelerators: attempt.worldSize || phase.accelerators,
      goodput: attempt.cost && attempt.cost.gpuHours > 0 ? productive / attempt.cost.gpuHours : Number.NaN,
      gpuHours: attempt.cost?.gpuHours ?? Number.NaN,
      usd: attempt.cost?.usd ?? Number.NaN,
      lost: sum(cards.filter((c) => c.phaseKey === phase.key && c.failed.n === attempt.n)),
    };
  }
  if (phase && phaseScoped) {
    const start = tsMs(phase.startedAt);
    const end = tsMs(phase.endedAt);
    return {
      scopeLabel: `${phase.key} (${phase.attempts.length} attempt${phase.attempts.length === 1 ? "" : "s"})`,
      step: num(phase.step),
      totalSteps: num(phase.totalSteps),
      ranSeconds: start && end ? (end - start) / 1000 : null,
      etaSeconds: run.currentPhaseKey === phase.key && run.etaSeconds !== undefined ? run.etaSeconds : null,
      accelerators: phase.accelerators,
      goodput: phase.goodput,
      gpuHours: phase.cost?.gpuHours ?? Number.NaN,
      usd: phase.cost?.usd ?? Number.NaN,
      lost: sum(cards.filter((c) => c.phaseKey === phase.key)),
    };
  }
  return {
    scopeLabel: "whole run",
    step: num(run.step),
    totalSteps: num(run.totalSteps),
    ranSeconds: null,
    etaSeconds: run.etaSeconds ?? null,
    accelerators: run.accelerators,
    goodput: run.goodput,
    gpuHours: run.cost?.gpuHours ?? Number.NaN,
    usd: run.cost?.usd ?? Number.NaN,
    lost: sum(cards),
  };
}
