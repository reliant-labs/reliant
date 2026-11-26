// Copyright (c) 2026 Reliant Labs

/**
 * The view models of a run's observability tabs (Metrics, System, Logs,
 * Traces, Profiles): pure functions over the generated messages, so each is
 * tested without rendering.
 */

import {
  MlAxis,
  MlCollection,
  MlProfileKind,
  MlSubjectKind,
  MlTraceKind,
  type MlAttempt,
  type MlCollectiveState,
  type MlLogLine,
  type MlPhase,
  type MlRank,
  type MlSeries,
  type MlSubject,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { attemptId } from "../../kit/ids";
import type { MlBand } from "../../kit/charts";
import type { RunScope } from "../runScope";

// ── Scope ─────────────────────────────────────────────────────────────────────

/** The attempt a per-attempt panel reads: the scoped one, else the phase's latest. */
export function attemptOfScope(scope: RunScope): MlAttempt | null {
  if (scope.attempt) return scope.attempt;
  const attempts = scope.phase?.attempts ?? [];
  return attempts.length ? attempts.reduce((a, b) => (b.n > a.n ? b : a)) : null;
}

/** The subject of one attempt (the System tab's heatmap reads one attempt's pods). */
export function attemptSubjectOf(scope: RunScope): Pick<MlSubject, "kind" | "id" | "label"> | null {
  const a = attemptOfScope(scope);
  if (!a || !scope.phase) return null;
  if (scope.rank !== null && scope.attempt) return scope.subject;
  return { kind: MlSubjectKind.ATTEMPT, id: attemptId(scope.run.id, scope.phase.key, a.n), label: `${scope.phase.key} attempt ${a.n}` };
}

export const axisOf = (axis: string | undefined): MlAxis => (axis === "tokens" ? MlAxis.TOKENS : axis === "time" ? MlAxis.TIME : MlAxis.STEP);

// ── Metrics workspace ─────────────────────────────────────────────────────────

export interface MetricTile {
  title: string;
  /** Series keys drawn together. A key ending "/*" reads its family. */
  keys: readonly string[];
  /** Draw the system key on the step axis (it is natively per time). */
  system?: boolean;
}

export interface MetricSection {
  id: string;
  title: string;
  note?: string;
  tiles: readonly MetricTile[];
}

export const EVAL_TILE_TASKS = ["mmlu_pro", "gsm8k", "ifeval", "arc_challenge", "truthfulqa", "long_context_ruler", "support_tickets_v3", "safety_refusals"] as const;

/** The workspace: sections of small multiples, in reading order (02-views §3.4). */
export const METRIC_SECTIONS: readonly MetricSection[] = [
  { id: "loss", title: "Loss", tiles: [{ title: "Train loss", keys: ["train/loss"] }, { title: "LM loss", keys: ["train/lm_loss"] }, { title: "Val loss", keys: ["val/loss"] }] },
  {
    id: "optim",
    title: "Optimizer",
    tiles: [{ title: "Learning rate", keys: ["optim/lr"] }, { title: "Grad norm (pre-clip)", keys: ["optim/grad_norm"] }, { title: "Steps clipped", keys: ["optim/grad_norm_clipped_ratio"] }, { title: "Update / param ratio", keys: ["optim/update_ratio"] }],
  },
  {
    id: "stability",
    title: "Stability",
    tiles: [{ title: "Loss spike detector (z)", keys: ["stability/loss_zscore"] }, { title: "Skipped steps", keys: ["stability/skipped_steps"] }, { title: "NaN / Inf", keys: ["stability/nan_count"] }, { title: "Loss scale", keys: ["stability/loss_scale"] }],
  },
  {
    id: "throughput",
    title: "Throughput",
    tiles: [{ title: "Tokens/s", keys: ["throughput/tokens_per_s"] }, { title: "Tokens/s per GPU", keys: ["throughput/tokens_per_s_per_gpu"] }, { title: "MFU", keys: ["throughput/mfu"] }, { title: "HFU", keys: ["throughput/hfu"] }, { title: "TFLOP/s per GPU", keys: ["throughput/tflops_per_gpu"] }],
  },
  {
    id: "timers",
    title: "Timers",
    tiles: [
      { title: "Step time p50 / p95", keys: ["timers/step_ms"] },
      { title: "Data wait", keys: ["timers/data_ms"] },
      { title: "Forward", keys: ["timers/forward_ms"] },
      { title: "Backward", keys: ["timers/backward_ms"] },
      { title: "Optimizer", keys: ["timers/optimizer_ms"] },
      { title: "Comm exposed", keys: ["timers/comm_exposed_ms"] },
      { title: "Checkpoint (blocking)", keys: ["timers/checkpoint_ms"] },
    ],
  },
  {
    id: "memory",
    title: "Memory",
    note: "p5 / p50 / p95 across ranks",
    tiles: [{ title: "Allocated", keys: ["memory/allocated_gb"] }, { title: "Reserved", keys: ["memory/reserved_gb"] }, { title: "Peak", keys: ["memory/peak_gb"] }],
  },
  {
    id: "data",
    title: "Data",
    tiles: [{ title: "Tokens seen", keys: ["data/tokens_seen"] }, { title: "Loader wait", keys: ["data/loader_wait_ratio"] }, { title: "Mixture by source", keys: ["data/tokens_by_source/*"] }],
  },
  {
    id: "vitals",
    title: "Model vitals",
    tiles: [{ title: "Activation RMS", keys: ["vitals/activation_rms"] }, { title: "Weight norm", keys: ["vitals/weight_norm"] }, { title: "Max attention logit by layers", keys: ["vitals/max_attn_logit"] }],
  },
  { id: "eval", title: "Evals", note: "eval-periodic, every 2,000 steps (release-gate-lite)", tiles: EVAL_TILE_TASKS.map((t) => ({ title: t, keys: [`eval/${t}`] })) },
  { id: "moe", title: "MoE", tiles: [{ title: "Expert load", keys: ["moe/expert_load"] }, { title: "Aux loss", keys: ["moe/aux_loss"] }] },
  { id: "rl", title: "RL", tiles: [{ title: "Reward", keys: ["rl/reward_mean"] }, { title: "KL to reference", keys: ["rl/kl"] }, { title: "Rollout time", keys: ["rl/rollout_ms"] }] },
  {
    id: "system",
    title: "System on step",
    note: "per-GPU means of the attempt's pods",
    tiles: [{ title: "SM active", keys: ["system/sm_active"], system: true }, { title: "Power", keys: ["system/power_w"], system: true }, { title: "GPU temp (max)", keys: ["system/gpu_temp_c"], system: true }, { title: "NVLink tx", keys: ["system/nvlink_tx_gbps"], system: true }],
  },
];

/** Every key the workspace reads, split into step-axis training keys and system keys. */
export function workspaceKeys(sections: readonly MetricSection[] = METRIC_SECTIONS): { train: string[]; system: string[] } {
  const train = new Set<string>();
  const system = new Set<string>();
  for (const s of sections) for (const t of s.tiles) for (const k of t.keys) (t.system ? system : train).add(k);
  return { train: [...train], system: [...system] };
}

/** Sections whose tiles match a search of keys and titles; a section with no match drops out. */
export function filterSections(sections: readonly MetricSection[], q: string): MetricSection[] {
  const needle = q.trim().toLowerCase();
  if (!needle) return [...sections];
  return sections
    .map((s) => ({ ...s, tiles: s.tiles.filter((t) => s.title.toLowerCase().includes(needle) || t.title.toLowerCase().includes(needle) || t.keys.some((k) => k.toLowerCase().includes(needle))) }))
    .filter((s) => s.tiles.length > 0);
}

/** The series of a tile from everything read: exact keys, and "prefix/*" families. */
export function seriesForTile(all: readonly MlSeries[], tile: MetricTile): MlSeries[] {
  return all.filter((s) => tile.keys.some((k) => (k.endsWith("/*") ? s.key.startsWith(k.slice(0, -1)) : s.key === k)));
}

/** Exponential moving average with debiasing (W&B / TensorBoard style); NaN stays a gap. `weight` 0 is raw. */
export function ema(values: readonly number[], weight: number): number[] {
  if (weight <= 0) return [...values];
  let last = 0;
  let n = 0;
  return values.map((v) => {
    if (!Number.isFinite(v)) return v;
    last = last * weight + (1 - weight) * v;
    n++;
    return last / (1 - Math.pow(weight, n));
  });
}

/** The steps an attempt redid: from where it resumed to where the previous one died (drawn as a band). */
export function redoneBands(phase: MlPhase | null): MlBand[] {
  if (!phase) return [];
  const attempts = [...phase.attempts].sort((a, b) => a.n - b.n);
  const out: MlBand[] = [];
  for (let i = 1; i < attempts.length; i++) {
    const prev = attempts[i - 1];
    const next = attempts[i];
    const from = Number(next.startStep);
    const to = Number(prev.endStep);
    if (to > from) out.push({ from, to, label: `redone by attempt ${next.n} (${(to - from).toLocaleString("en-US")} steps)`, tone: "warning" });
  }
  return out;
}

/** The last collected value in a series list for a key (the tile header). */
export function latestOf(series: readonly MlSeries[]): number | null {
  for (let i = series.length - 1; i >= 0; i--) {
    const s = series[i];
    for (let j = s.y.length - 1; j >= 0; j--) if (Number.isFinite(s.y[j])) return s.y[j];
  }
  return null;
}

// ── System ────────────────────────────────────────────────────────────────────

export interface HeatRow {
  rank: number;
  cells: (number | null)[];
}

/** Rank × time cells of one per-rank system key ("rank 37" groups); the columns are the first series' x. */
export function rankHeatmap(series: readonly MlSeries[]): { rows: HeatRow[]; xs: number[] } {
  const rows: HeatRow[] = [];
  let xs: number[] = [];
  for (const s of series) {
    const m = /^rank (\d+)$/.exec(s.group);
    if (!m || s.collection !== MlCollection.COLLECTED) continue;
    if (xs.length === 0) xs = [...s.x];
    rows.push({ rank: Number(m[1]), cells: s.y.map((v) => (Number.isFinite(v) ? v : null)) });
  }
  rows.sort((a, b) => a.rank - b.rank);
  return { rows, xs };
}

/** The straggler(s) among the ranks, slowest first. */
export function stragglersOf(ranks: readonly MlRank[]): MlRank[] {
  return ranks.filter((r) => r.straggler).sort((a, b) => b.stepTimeMsP50 - a.stepTimeMsP50 || b.computeMsP50 - a.computeMsP50);
}

/** When a per-rank ratio series first crossed `threshold` (epoch seconds), or null. */
export function firstCrossing(s: MlSeries | undefined, threshold: number): number | null {
  if (!s) return null;
  const i = s.y.findIndex((v) => Number.isFinite(v) && v >= threshold);
  return i >= 0 ? s.x[i] : null;
}

// ── Logs ──────────────────────────────────────────────────────────────────────

export const HIGHLIGHT_LABELS: Record<string, string> = {
  nccl: "NCCL",
  watchdog: "Watchdog",
  xid: "Xid",
  traceback: "Traceback",
  shm: "Bus error / shm",
  oom: "OOM",
  nan: "NaN",
  loss_spike: "Loss spike",
  straggler: "Straggler",
};

/** Highlight kinds with their line counts, most first. */
export function highlightCounts(lines: readonly MlLogLine[]): { highlight: string; label: string; count: number }[] {
  const counts = new Map<string, number>();
  for (const l of lines) if (l.highlight) counts.set(l.highlight, (counts.get(l.highlight) ?? 0) + 1);
  return [...counts.entries()].map(([highlight, count]) => ({ highlight, label: HIGHLIGHT_LABELS[highlight] ?? highlight, count })).sort((a, b) => b.count - a.count || a.label.localeCompare(b.label));
}

/** The first error line of each attempt (pinned above the log). */
export function firstErrors(lines: readonly MlLogLine[]): MlLogLine[] {
  const seen = new Map<number, MlLogLine>();
  for (const l of lines) if (l.level === "error" && !seen.has(l.attempt)) seen.set(l.attempt, l);
  return [...seen.values()].sort((a, b) => a.attempt - b.attempt);
}

export interface LogFilter {
  rank: number | null;
  errorsOnly: boolean;
  highlight: string | null;
  q: string;
}

export function filterLogs(lines: readonly MlLogLine[], f: LogFilter): MlLogLine[] {
  const q = f.q.trim().toLowerCase();
  return lines.filter(
    (l) =>
      (f.rank === null || l.rank === f.rank) &&
      (!f.errorsOnly || l.level === "error") &&
      (!f.highlight || (f.highlight === "*" ? !!l.highlight : l.highlight === f.highlight)) &&
      (!q || l.message.toLowerCase().includes(q) || l.pod.toLowerCase().includes(q)),
  );
}

/** "error" | "warn" | ... as Observability's Severity. */
export function severityOf(level: string): "error" | "warn" | "info" | "debug" | "unknown" {
  return level === "error" || level === "warn" || level === "info" || level === "debug" ? level : "unknown";
}

// ── Traces ────────────────────────────────────────────────────────────────────

export const TRACE_KINDS: readonly { id: string; label: string; kind: MlTraceKind | undefined }[] = [
  { id: "all", label: "All", kind: undefined },
  { id: "step", label: "Train steps", kind: MlTraceKind.TRAIN_STEP },
  { id: "checkpoint", label: "Checkpoints", kind: MlTraceKind.CHECKPOINT },
  { id: "data", label: "Data pipeline", kind: MlTraceKind.DATA_PIPELINE },
  { id: "pod", label: "Pod start", kind: MlTraceKind.POD_START },
];

export const traceKindLabel = (k: MlTraceKind): string => TRACE_KINDS.find((t) => t.kind === k)?.label.replace(/s$/, "") ?? "Trace";

// ── Profiles ──────────────────────────────────────────────────────────────────

export const PROFILE_KIND_LABELS: Record<number, string> = {
  [MlProfileKind.PYTORCH_PROFILER]: "PyTorch profiler (Kineto)",
  [MlProfileKind.NSIGHT_SYSTEMS]: "Nsight Systems",
  [MlProfileKind.NSIGHT_COMPUTE]: "Nsight Compute",
  [MlProfileKind.MEMORY_SNAPSHOT]: "Memory snapshot",
  [MlProfileKind.FLIGHT_RECORDER]: "NCCL flight recorder",
  [MlProfileKind.PY_SPY]: "py-spy",
  [MlProfileKind.CPU_CONTINUOUS]: "Continuous CPU (eBPF)",
  [MlProfileKind.GPU_CONTINUOUS]: "Continuous GPU (CUPTI)",
};

/** The flight recorder's collectives: mismatched ranks first, then by rank. */
export function collectiveRows(collectives: readonly MlCollectiveState[]): MlCollectiveState[] {
  return [...collectives].sort((a, b) => Number(b.mismatched) - Number(a.mismatched) || a.rank - b.rank);
}

/** The one-line verdict of a flight recorder dump: which seq and which ranks did not join it. */
export function flightVerdict(collectives: readonly MlCollectiveState[]): string {
  if (collectives.length === 0) return "No collectives recorded";
  const maxSeq = collectives.reduce((m, c) => (c.lastSeq > m ? c.lastSeq : m), collectives[0].lastSeq);
  const behind = collectives.filter((c) => c.mismatched || c.lastSeq < maxSeq).map((c) => c.rank);
  if (behind.length === 0) return `All ${collectives.length} ranks reached collective ${maxSeq}`;
  return `Not all ranks joined collective ${maxSeq} (${collectives.find((c) => c.lastSeq === maxSeq)?.op ?? ""}): missing rank${behind.length > 1 ? "s" : ""} ${behind.join(", ")}`;
}
