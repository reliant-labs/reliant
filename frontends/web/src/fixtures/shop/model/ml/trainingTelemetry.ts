// Copyright (c) 2026 Reliant Labs

/**
 * The ML fixture world's TRAINING TELEMETRY: what the instrumented trainer,
 * DCGM, node-exporter, the pod logs, the step tracer and the profilers report
 * for runs, phases, attempts, ranks, checkpoints and experiments
 * (QueryMlSeries, QueryMlHistograms, ListMlLogs, ListMlTraces, GetMlTrace,
 * ListMlProfiles, GetMlProfile). Ids and the story: ./world.ts; the shared
 * numbers (loss, LR, grad norm, tokens/s, MFU, step time, eval scores):
 * ./curves.ts, so a point on a chart is the number the checkpoints table and
 * the eval matrix show.
 *
 * Step-axis series are continuous across attempts: a PHASE subject answers
 * one series per attempt (group "attempt n"), so a resume overlays the steps
 * the previous attempt redid after its last checkpoint. A RUN reads its
 * current phase. System series (system/*) are on the time axis, per rank or
 * node, over the attempt's pods; on the step axis they map each sample to the
 * step running then.
 *
 * Every series, span and log line carries the training id (run, phase,
 * attempt, and rank where it has one), and every span its node, GPU UUID and
 * pod: one trace joins the trainer's step with DCGM, node-problem-detector,
 * kubelet and Kueue.
 */

import type { MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";

import {
  MlAxis,
  MlCollection,
  MlProfileKind,
  MlSpanStatus,
  MlSubjectKind,
  MlTraceKind,
  type MlCollectiveStateSchema,
  type MlHistogramSchema,
  type MlLogLineSchema,
  type MlProfileSchema,
  type MlSeries,
  type MlSeriesSchema,
  type MlSpanSchema,
  type MlSubjectSchema,
  type MlTraceSummarySchema,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { evalScore, V3_STRAGGLER_STEP, v3GradNorm, v3LmLoss, v3LossCurve, v3Lr, v3Mfu, v3StepTimeMs, v3TokensPerSecond, v3TrainLoss, v3ValLoss } from "./curves";
import type { MlDomain } from "./domain";
import {
  BAD_GPU_INDEX,
  BAD_NODE,
  DAY,
  EVAL_TASKS,
  EXPERIMENTS,
  FINDINGS,
  gpuUuid,
  hash,
  HOT_NODE,
  HOUR,
  MINUTE,
  msAt,
  noise,
  nodeId,
  poolOfNode,
  POOLS,
  PREEMPTOR_WORKLOAD,
  PROFILES,
  rankPod,
  rng,
  RUNS,
  SPARE_NODE,
  STRAGGLER_RANK,
  STRAGGLER_SINCE,
  TRACES,
  V3_ATTEMPTS,
  V3_NOW,
  V3_PHASES,
  workloadName,
  type AttemptEnd,
} from "./world";

/** A series as a plain init (never the message itself), so spreads and overrides stay inits. */
type SeriesInit = Exclude<MessageInitShape<typeof MlSeriesSchema>, MlSeries>;
type SubjectInit = MessageInitShape<typeof MlSubjectSchema>;
type LogInit = MessageInitShape<typeof MlLogLineSchema>;
type SpanInit = MessageInitShape<typeof MlSpanSchema>;
type SummaryInit = MessageInitShape<typeof MlTraceSummarySchema>;
type ProfileInit = MessageInitShape<typeof MlProfileSchema>;
type HistogramInit = MessageInitShape<typeof MlHistogramSchema>;
type CollectiveInit = MessageInitShape<typeof MlCollectiveStateSchema>;

/** Smooth deterministic jitter in [-1, 1] for (key, x): curves.ts's, for this file's own series. */
const jit = (key: string, x: number) => noise(rng(hash(`${key}@${Math.round(x)}`)));

/** A 32-hex trace id (or 16-hex span id) from a seed. */
function hexId(seed: string, words = 4): string {
  return Array.from({ length: words }, (_, i) => hash(`${seed}#${i}`).toString(16).padStart(8, "0")).join("");
}

// ── The runs this file reports on ─────────────────────────────────────────────

export interface TAttempt {
  phase: string;
  n: number;
  runningAt: number;
  endedAt: number | null;
  startStep: number;
  endStep: number;
  lastCheckpointStep: number;
  end: AttemptEnd;
  nodes: readonly string[];
  stepsPerHour: number;
  worldSize: number;
  gpusPerNode: number;
}

interface TRun {
  key: string;
  id: string;
  name: string;
  experimentId: string;
  currentPhase: string;
  attempts: readonly TAttempt[];
  /** Phases that never ran (rl, a queued dpo): their keys are "phase has not started". */
  pendingPhases: readonly string[];
  /** Training keys reported at all (false: an observed job nobody instrumented). */
  instrumented: boolean;
  /** The hero run reports the whole key set; the others only SIMPLE_KEYS. */
  full: boolean;
  tokensPerStep: number;
  totalSteps: Record<string, number>;
  checkpointEvery: Record<string, number>;
  /** Blocking seconds per checkpoint save. */
  blockingSeconds: number;
  lrPeak: number;
  /** A non-hero run's val loss at a step (training.ts's objective curve, so the sparkline and the chart agree). */
  val: (step: number) => number;
  tpsBase: number;
  /** Val loss rises after this step (the lr 8e-5 sweep trial). */
  divergeAfter?: number;
}

const H100 = POOLS.h100.nodes;
const v3Attempts: TAttempt[] = V3_ATTEMPTS.map((a) => ({ ...a, worldSize: V3_NOW.worldSize, gpusPerNode: 8 }));

// The non-hero runs as training.ts declares them (v2Spec, v1Spec, SWEEP_TRIALS, dpoSpec, rerankerSpec).
const V2_END = -21 * DAY - 1.6 * HOUR;
const V2_HOURS = 60_000 / 360;
const V1_END = -44 * DAY;
const V1_HOURS = 22_140 / 360;
const SWEEP_PER_HOUR = 3000 / 11;

interface SweepDef {
  key: "sweep1" | "sweep2" | "sweep3" | "sweep4";
  lr: number;
  start: number;
  end: number | null;
  endStep: number;
  final: number;
  end_: AttemptEnd;
}

const SWEEPS: readonly SweepDef[] = [
  { key: "sweep1", lr: 1e-5, start: -30 * HOUR, end: -18.6 * HOUR, endStep: 3000, final: 2.031, end_: "completed" },
  { key: "sweep2", lr: 2e-5, start: -30 * HOUR, end: -18.6 * HOUR, endStep: 3000, final: 1.998, end_: "completed" },
  { key: "sweep3", lr: 4e-5, start: -7 * HOUR, end: null, endStep: 1850, final: 2.012, end_: "running" },
  { key: "sweep4", lr: 8e-5, start: -18.4 * HOUR, end: -14 * HOUR, endStep: 1160, final: 2.214, end_: "rewind" },
];

function sweepRun(t: SweepDef): TRun {
  const runningAt = t.start + 0.25 * HOUR;
  const diverge = t.key === "sweep4";
  return {
    key: t.key,
    id: RUNS[t.key].id,
    name: RUNS[t.key].name,
    experimentId: EXPERIMENTS.support.id,
    currentPhase: "sft",
    attempts: [
      { phase: "sft", n: 1, runningAt, endedAt: t.end, startStep: 0, endStep: t.endStep, lastCheckpointStep: Math.floor(t.endStep / 500) * 500, end: t.end_, nodes: [POOLS.a100.nodes[1]], stepsPerHour: SWEEP_PER_HOUR, worldSize: 8, gpusPerNode: 8 },
    ],
    pendingPhases: [],
    instrumented: true,
    full: false,
    tokensPerStep: 64 * 4096,
    totalSteps: { sft: 3000 },
    checkpointEvery: { sft: 500 },
    blockingSeconds: 6,
    lrPeak: t.lr,
    val: (s) => (diverge && s > 900 ? t.final - 0.1 + 0.0004 * (s - 900) : t.final + 0.45 * Math.pow(1 + s / 300, -0.6) - 0.45 * Math.pow(1 + 3000 / 300, -0.6)),
    tpsBase: (64 * 4096 * SWEEP_PER_HOUR) / 3600,
    ...(diverge ? { divergeAfter: 900 } : {}),
  };
}

const TRUNS: readonly TRun[] = [
  {
    key: "v3",
    id: RUNS.v3.id,
    name: RUNS.v3.name,
    experimentId: EXPERIMENTS.support.id,
    currentPhase: V3_NOW.phase,
    attempts: v3Attempts,
    pendingPhases: [V3_PHASES.rl.key],
    instrumented: true,
    full: true,
    tokensPerStep: V3_NOW.tokensPerStep,
    totalSteps: { pretrain: V3_PHASES.pretrain.totalSteps, sft: V3_PHASES.sft.totalSteps, rl: V3_PHASES.rl.totalSteps },
    checkpointEvery: { pretrain: V3_PHASES.pretrain.checkpointEvery, sft: V3_PHASES.sft.checkpointEvery },
    blockingSeconds: 21,
    lrPeak: 2e-5,
    val: (s) => v3ValLoss("sft", s),
    tpsBase: 419_000,
  },
  {
    key: "v2",
    id: RUNS.v2.id,
    name: RUNS.v2.name,
    experimentId: EXPERIMENTS.support.id,
    currentPhase: "sft",
    attempts: [{ phase: "sft", n: 1, runningAt: V2_END - V2_HOURS * HOUR, endedAt: V2_END, startStep: 0, endStep: 60_000, lastCheckpointStep: 60_000, end: "completed", nodes: H100, stepsPerHour: 360, worldSize: 64, gpusPerNode: 8 }],
    pendingPhases: [],
    instrumented: true,
    full: false,
    tokensPerStep: V3_NOW.tokensPerStep,
    totalSteps: { sft: 60_000 },
    checkpointEvery: { sft: 2000 },
    blockingSeconds: 19,
    lrPeak: 3e-5,
    val: (s) => 1.894 + 0.55 * Math.pow(1 + s / 1500, -0.5) - 0.55 * Math.pow(1 + 60_000 / 1500, -0.5),
    tpsBase: (V3_NOW.tokensPerStep * 360) / 3600,
  },
  {
    key: "v1",
    id: RUNS.v1.id,
    name: RUNS.v1.name,
    experimentId: EXPERIMENTS.support.id,
    currentPhase: "sft",
    attempts: [{ phase: "sft", n: 1, runningAt: V1_END - V1_HOURS * HOUR, endedAt: V1_END, startStep: 0, endStep: 22_140, lastCheckpointStep: 22_000, end: "user_code_error", nodes: H100.slice(0, 4), stepsPerHour: 360, worldSize: 32, gpusPerNode: 8 }],
    pendingPhases: [],
    instrumented: true,
    full: false,
    tokensPerStep: 256 * 8192,
    totalSteps: { sft: 60_000 },
    checkpointEvery: { sft: 1000 },
    blockingSeconds: 12,
    lrPeak: 5e-5,
    val: (s) => 1.978 + 0.6 * Math.pow(1 + s / 1500, -0.5) - 0.6 * Math.pow(1 + 22_000 / 1500, -0.5),
    tpsBase: (256 * 8192 * 360) / 3600,
  },
  ...SWEEPS.map(sweepRun),
  {
    key: "dpo",
    id: RUNS.dpo.id,
    name: RUNS.dpo.name,
    experimentId: EXPERIMENTS.support.id,
    currentPhase: "dpo",
    attempts: [],
    pendingPhases: ["dpo"],
    instrumented: true,
    full: false,
    tokensPerStep: 128 * 4096,
    totalSteps: { dpo: 2400 },
    checkpointEvery: { dpo: 500 },
    blockingSeconds: 0,
    lrPeak: 5e-7,
    val: () => NaN,
    tpsBase: 0,
  },
  {
    key: "reranker",
    id: RUNS.reranker.id,
    name: RUNS.reranker.name,
    experimentId: EXPERIMENTS.reranker.id,
    currentPhase: "job",
    attempts: [{ phase: "job", n: 1, runningAt: -10.2 * HOUR, endedAt: -9 * HOUR, startStep: 0, endStep: 0, lastCheckpointStep: 0, end: "completed", nodes: [POOLS.a100.nodes[0]], stepsPerHour: 0, worldSize: 4, gpusPerNode: 8 }],
    pendingPhases: [],
    instrumented: false,
    full: false,
    tokensPerStep: 0,
    totalSteps: {},
    checkpointEvery: {},
    blockingSeconds: 0,
    lrPeak: 0,
    val: () => NaN,
    tpsBase: 0,
  },
];

const runByIdOrName = (s: string) => TRUNS.find((r) => r.id === s || r.name === s);

/** Epoch ms at which an attempt was at `step`. */
export function timeOfStep(a: TAttempt, step: number, now: () => number): number {
  const hours = a.stepsPerHour > 0 ? (step - a.startStep) / a.stepsPerHour : 0;
  return msAt(a.runningAt + hours * HOUR, now);
}

/** The step an attempt was at, at epoch ms `t`. */
function stepAtTime(a: TAttempt, t: number, now: () => number): number {
  const hours = (t - msAt(a.runningAt, now)) / HOUR;
  return Math.max(a.startStep, Math.min(a.endStep, Math.round(a.startStep + hours * a.stepsPerHour)));
}

const attemptEndMs = (a: TAttempt, now: () => number) => (a.endedAt === null ? now() : msAt(a.endedAt, now));
const isV3 = (r: TRun) => r.key === "v3";
/** Rank 37's thermal slowdown slows every step of sft attempt 3 after V3_STRAGGLER_STEP. */
const stragglingAt = (r: TRun, a: TAttempt, step: number) => isV3(r) && a.phase === "sft" && a.n === 3 && step >= V3_STRAGGLER_STEP;

const nodeOfRank = (a: TAttempt, rank: number) => a.nodes[Math.floor(rank / a.gpusPerNode)] ?? a.nodes[0];
function nodeIdOf(name: string): string {
  const pool = poolOfNode(name) ?? POOLS.h100;
  return nodeId(pool, name);
}
const deviceOfRank = (a: TAttempt, rank: number) => gpuUuid(nodeOfRank(a, rank), rank % a.gpusPerNode);

// ── Subjects ──────────────────────────────────────────────────────────────────

interface Scope {
  run: TRun;
  phase: string;
  /** The attempts the subject covers (one for an attempt or a rank). */
  attempts: readonly TAttempt[];
  rank: number | null;
  /** Whether the subject is one attempt (or a rank of it). */
  single: boolean;
}

const attemptSubject = (run: TRun, a: TAttempt): SubjectInit => ({ kind: MlSubjectKind.ATTEMPT, id: `${run.id}/${a.phase}/${a.n}`, label: `${a.phase} attempt ${a.n}` });
const rankSubject = (run: TRun, a: TAttempt, rank: number): SubjectInit => ({ kind: MlSubjectKind.RANK, id: `${run.id}/${a.phase}/${a.n}/${rank}`, label: `rank ${rank}` });
const phaseSubject = (run: TRun, phase: string): SubjectInit => ({ kind: MlSubjectKind.PHASE, id: `${run.id}/${phase}`, label: phase });
const runSubject = (run: TRun): SubjectInit => ({ kind: MlSubjectKind.RUN, id: run.id, label: run.name });

const TRAINING_KINDS = new Set<MlSubjectKind>([MlSubjectKind.RUN, MlSubjectKind.PHASE, MlSubjectKind.ATTEMPT, MlSubjectKind.RANK, MlSubjectKind.CHECKPOINT]);

/** The scope a training subject names, or undefined when it is not one of this file's. */
export function scopeOf(subject: { kind: MlSubjectKind; id: string } | undefined): Scope | undefined {
  if (!subject || !TRAINING_KINDS.has(subject.kind)) return undefined;
  const [runPart, phasePart, third, fourth] = subject.id.split("/");
  const run = runByIdOrName(runPart);
  if (!run) return undefined;
  const phase = subject.kind === MlSubjectKind.RUN ? run.currentPhase : phasePart;
  if (!phase) return undefined;
  const all = run.attempts.filter((a) => a.phase === phase);
  if (subject.kind === MlSubjectKind.ATTEMPT || subject.kind === MlSubjectKind.RANK) {
    const a = all.find((x) => x.n === Number(third));
    if (!a) return undefined;
    const rank = subject.kind === MlSubjectKind.RANK ? Number(fourth) : null;
    if (rank !== null && !(rank >= 0 && rank < a.worldSize)) return undefined;
    return { run, phase, attempts: [a], rank, single: true };
  }
  return { run, phase, attempts: all, rank: null, single: false };
}

// ── Resolution ────────────────────────────────────────────────────────────────

const NICE_STEPS = [1, 2, 5, 10, 20, 25, 50, 100, 200, 250, 500, 1000, 2000, 2500, 5000];
const NICE_SECONDS = [30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 14_400];

export function niceStep(raw: number): number {
  return NICE_STEPS.find((s) => s >= raw) ?? 10_000;
}
function niceSeconds(raw: number): number {
  return NICE_SECONDS.find((s) => s >= raw) ?? 28_800;
}

/** The steps of an attempt at resolution `res`: every multiple inside it, plus both ends. */
function stepsOf(a: TAttempt, res: number): number[] {
  const out = [a.startStep];
  for (let s = Math.ceil((a.startStep + 1) / res) * res; s < a.endStep; s += res) out.push(s);
  if (a.endStep > a.startStep) out.push(a.endStep);
  return out;
}

// ── Training keys ─────────────────────────────────────────────────────────────

interface Pt {
  run: TRun;
  a: TAttempt;
  step: number;
  straggling: boolean;
  rank: number | null;
  res: number;
}

interface KeyDef {
  key: string;
  label: string;
  unit: string;
  gen?: (p: Pt, variant: string) => number;
  /** Sparse: only at multiples of this (val loss every 500, evals every 2,000). */
  every?: number;
  /** Sparse points stop at the attempt's last durable checkpoint (they come from checkpoints). */
  fromCheckpoints?: boolean;
  /** One series per stat ("p50", "p95"). */
  stats?: readonly string[];
  /** One series per group (layer groups). */
  groups?: readonly string[];
  /** One series for the whole phase, not one per attempt. */
  phaseWide?: boolean;
  /** Reported by the simpler (non-hero) runs too. */
  simple?: boolean;
  /** Differs by rank (group_by rank answers per rank). */
  perRank?: boolean;
  /** Not applicable to this model, with why. */
  na?: string;
}

const LAYER_GROUPS = ["layers 0–7", "layers 8–15", "layers 16–23", "layers 24–27", "layers 28–31"] as const;
const SOURCES = ["web", "code", "math", "support"] as const;

function simpleLossCurve(r: TRun, step: number): number {
  return r.val(step) - 0.043;
}
function simpleLr(r: TRun, phase: string, step: number): number {
  const total = r.totalSteps[phase] ?? 1;
  const warm = total * 0.02;
  if (step < warm) return (r.lrPeak * step) / warm;
  const p = (step - warm) / (total - warm);
  return r.lrPeak * (0.1 + 0.9 * 0.5 * (1 + Math.cos(Math.PI * Math.min(1, p))));
}

const lossOf = (p: Pt) => (isV3(p.run) ? v3TrainLoss(p.a.phase, p.step) : simpleLossCurve(p.run, p.step) + 0.012 * jit(`${p.run.key}-loss`, p.step));
const lrOf = (p: Pt) => (isV3(p.run) ? v3Lr(p.a.phase, p.step) : simpleLr(p.run, p.a.phase, p.step));
const tpsOf = (p: Pt) => (isV3(p.run) ? v3TokensPerSecond(p.a.phase, p.step, p.straggling) : p.run.tpsBase * (1 + 0.015 * jit(`${p.run.key}-tps`, p.step)));
const mfuOf = (p: Pt) => (isV3(p.run) ? v3Mfu(p.a.phase, p.step, p.straggling) : (tpsOf(p) * 6 * 8.03e9) / (p.a.worldSize * 989e12));
const stepMsOf = (p: Pt, straggling = p.straggling) => (isV3(p.run) ? v3StepTimeMs(p.a.phase, p.step, straggling) : (p.run.tokensPerStep / tpsOf(p)) * 1000);
const spikeAt = (p: Pt) => (isV3(p.run) && p.a.phase === "sft" ? v3LossCurve("sft", p.step) - (1.72 + 0.62 * Math.pow(1 + p.step / 1600, -0.55)) : 0);

/** Step-time shares of an unslowed step (training.ts v3StepBreakdown); exposed comm is what is left, so the straggler's extra time lands there on every rank. */
const SHARE = { data: 0.03, forward: 0.25, backward: 0.47, optimizer: 0.05 } as const;
const LATE = 1.18;

function timer(p: Pt, part: keyof typeof SHARE | "comm"): number {
  const base = stepMsOf(p, false);
  const actual = stepMsOf(p);
  const f = p.straggling && p.rank === STRAGGLER_RANK ? LATE : 1;
  const j = 1 + 0.02 * jit(`${p.run.key}-${part}-${p.rank ?? "all"}`, p.step);
  if (part === "comm") {
    const ckpt = (p.run.blockingSeconds * 1000) / (p.run.checkpointEvery[p.a.phase] ?? 1000);
    return Math.max(0, actual - base * (SHARE.data + (SHARE.forward + SHARE.backward + SHARE.optimizer) * f) - ckpt) * j;
  }
  return base * SHARE[part] * (part === "data" ? 1 : f) * j;
}

function memory(p: Pt, kind: "allocated" | "reserved" | "peak", variant: string): number {
  const base = { allocated: 61.2, reserved: 68.5, peak: 72.8 }[kind];
  const warm = p.step - p.a.startStep < 150 ? -6 : 0;
  if (variant === "p5") return base + warm - 1.1;
  if (variant === "p95") return base + warm + 1.4;
  if (p.rank !== null) return base + warm + (p.rank === 0 ? 1.1 : (((p.rank * 7) % 10) / 10 - 0.5) * 0.5);
  return base + warm + 0.15 * jit(`mem-${kind}`, p.step);
}

const sourceShare = (phase: string, step: number, src: (typeof SOURCES)[number]) => {
  if (phase === "pretrain") return { web: 0.45, code: 0.25, math: 0.1, support: 0.2 }[src];
  const late = step >= 24_000;
  return { web: late ? 0.29 : 0.25, code: 0.12, math: late ? 0.04 : 0.08, support: 0.55 }[src];
};

/** Skipped steps in the bucket ending at the point's step (bf16 NaN guard skipped three at the spike). */
const SKIPPED = [38_710, 38_720, 38_731];

export const TRAIN_KEYS: readonly KeyDef[] = [
  { key: "train/loss", label: "Train loss", unit: "", simple: true, gen: lossOf },
  { key: "train/lm_loss", label: "LM loss", unit: "", simple: true, gen: (p) => (isV3(p.run) ? v3LmLoss(p.a.phase, p.step) : lossOf(p) - 0.006) },
  {
    key: "val/loss",
    label: "Val loss",
    unit: "",
    every: 500,
    simple: true,
    gen: (p) => (isV3(p.run) ? v3ValLoss(p.a.phase, p.step) : p.run.val(p.step) + 0.003 * jit(`${p.run.key}-val`, p.step)),
  },
  { key: "optim/lr", label: "Learning rate", unit: "", simple: true, gen: lrOf },
  { key: "optim/grad_norm", label: "Grad norm (pre-clip)", unit: "", simple: true, gen: (p) => (isV3(p.run) ? v3GradNorm(p.a.phase, p.step) : 0.3 + 0.5 * Math.pow(1 + p.step / 4000, -0.25) + 0.04 * jit(`${p.run.key}-gn`, p.step)) },
  {
    key: "optim/grad_norm_clipped_ratio",
    label: "Steps clipped",
    unit: "ratio",
    gen: (p) => Math.max(0, 0.004 + 0.002 * jit("clip", p.step) + (p.a.phase === "sft" ? 0.62 * Math.exp(-Math.pow((p.step - 38_710) / 110, 2)) : 0)),
  },
  { key: "optim/update_ratio", label: "Update / param ratio", unit: "", gen: (p) => (lrOf(p) / (p.a.phase === "pretrain" ? 3e-4 : 2e-5)) * 1.1e-3 * (1 + 0.05 * jit("upd", p.step)) },
  { key: "stability/loss_scale", label: "Loss scale", unit: "", na: "bf16 training: no loss scaling" },
  { key: "stability/skipped_steps", label: "Skipped steps", unit: "count", gen: (p) => (p.a.phase === "sft" ? SKIPPED.filter((s) => s > p.step - p.res && s <= p.step).length : 0) },
  { key: "stability/nan_count", label: "NaN / Inf", unit: "count", gen: () => 0 },
  { key: "stability/loss_zscore", label: "Loss spike detector (z)", unit: "", gen: (p) => jit("z", p.step) * 1.1 + spikeAt(p) / 0.055 },
  { key: "throughput/tokens_per_s", label: "Tokens/s", unit: "tokens/s", simple: true, gen: tpsOf },
  { key: "throughput/tokens_per_s_per_gpu", label: "Tokens/s per GPU", unit: "tokens/s", gen: (p) => tpsOf(p) / p.a.worldSize },
  { key: "throughput/mfu", label: "MFU", unit: "ratio", simple: true, gen: mfuOf },
  { key: "throughput/hfu", label: "HFU", unit: "ratio", gen: (p) => mfuOf(p) * 1.14 },
  { key: "throughput/tflops_per_gpu", label: "TFLOP/s per GPU", unit: "TFLOP/s", perRank: true, gen: (p) => (mfuOf(p) * 989 * stepMsOf(p)) / (stepMsOf(p, false) * (SHARE.forward + SHARE.backward + SHARE.optimizer) * (p.straggling && p.rank === STRAGGLER_RANK ? LATE : 1)) },
  {
    key: "timers/step_ms",
    label: "Step time",
    unit: "ms",
    stats: ["p50", "p95"],
    simple: true,
    perRank: true,
    gen: (p, stat) => stepMsOf(p) * (stat === "p95" ? 1.035 + 0.01 * jit("p95", p.step) : 1),
  },
  { key: "timers/data_ms", label: "Data wait", unit: "ms", perRank: true, gen: (p) => timer(p, "data") },
  { key: "timers/forward_ms", label: "Forward", unit: "ms", perRank: true, gen: (p) => timer(p, "forward") },
  { key: "timers/backward_ms", label: "Backward", unit: "ms", perRank: true, gen: (p) => timer(p, "backward") },
  { key: "timers/optimizer_ms", label: "Optimizer", unit: "ms", perRank: true, gen: (p) => timer(p, "optimizer") },
  { key: "timers/comm_exposed_ms", label: "Comm exposed", unit: "ms", perRank: true, gen: (p) => timer(p, "comm") },
  { key: "timers/checkpoint_ms", label: "Checkpoint (blocking)", unit: "ms", fromCheckpoints: true, gen: (p) => (p.a.phase === "pretrain" ? 9_000 : p.run.blockingSeconds * 1000) + 1_000 * jit("ckpt", p.step) },
  { key: "memory/allocated_gb", label: "Memory allocated", unit: "GB", stats: ["p5", "p50", "p95"], perRank: true, gen: (p, v) => memory(p, "allocated", v) },
  { key: "memory/reserved_gb", label: "Memory reserved", unit: "GB", stats: ["p5", "p50", "p95"], perRank: true, gen: (p, v) => memory(p, "reserved", v) },
  { key: "memory/peak_gb", label: "Memory peak", unit: "GB", stats: ["p5", "p50", "p95"], perRank: true, gen: (p, v) => memory(p, "peak", v) },
  { key: "data/tokens_seen", label: "Tokens seen", unit: "count", gen: (p) => p.step * p.run.tokensPerStep },
  { key: "data/loader_wait_ratio", label: "Loader wait", unit: "ratio", gen: (p) => 0.012 + 0.002 * jit("lw", p.step) },
  ...SOURCES.map<KeyDef>((src) => ({ key: `data/tokens_by_source/${src}`, label: `${src} share`, unit: "ratio", gen: (p) => sourceShare(p.a.phase, p.step, src) })),
  ...EVAL_TASKS.map<KeyDef>((task) => ({
    key: `eval/${task}`,
    label: task,
    unit: "ratio",
    every: 2000,
    fromCheckpoints: true,
    phaseWide: true,
    gen: (p) => evalScore(task, `v3@${p.step}`),
  })),
  { key: "vitals/activation_rms", label: "Activation RMS", unit: "", gen: (p) => 1.02 + 0.08 * Math.log1p(p.step / 10_000) + spikeAt(p) * 0.8 + 0.005 * jit("arms", p.step) },
  { key: "vitals/weight_norm", label: "Weight norm", unit: "", gen: (p) => 182 + (p.a.phase === "sft" ? 4 : 0) + 6 * (p.step / 60_000) + 0.05 * jit("wn", p.step) },
  {
    key: "vitals/max_attn_logit",
    label: "Max attention logit",
    unit: "",
    groups: LAYER_GROUPS,
    gen: (p, g) => {
      const i = LAYER_GROUPS.indexOf(g as (typeof LAYER_GROUPS)[number]);
      if (i === 4 && p.a.phase === "sft") return 30 + 31 * Math.pow(Math.max(0, p.step - 20_000) / 21_200, 1.3) + 0.6 * jit("al4", p.step);
      return 18 + i * 3 + 0.5 * jit(`al${i}`, p.step);
    },
  },
  ...["aux_loss", "expert_load", "tokens_dropped", "router_z_loss"].map<KeyDef>((k) => ({ key: `moe/${k}`, label: k.replace(/_/g, " "), unit: "", na: "Dense model: no experts" })),
  ...["reward_mean", "kl", "entropy", "clip_fraction", "response_length", "rollout_ms"].map<KeyDef>((k) => ({ key: `rl/${k}`, label: k.replace(/_/g, " "), unit: "", na: "Only the rl phase samples rollouts" })),
];

// ── System keys ───────────────────────────────────────────────────────────────

interface SysPt {
  run: TRun;
  a: TAttempt;
  rank: number;
  node: string;
  t: number;
  /** 0–1: how far rank 37's thermal slowdown has ramped. */
  hot: number;
  straggling: boolean;
  /** Within the last 10 minutes of a hang (sft a1): every rank but 37 spins in NCCL. */
  hang: boolean;
  /** The device fell off the bus (sft a2, r4vb GPU 5, at its end). */
  lost: boolean;
}

interface SysDef {
  key: string;
  label: string;
  unit: string;
  /** Reported per node (host metrics), not per GPU. */
  node?: boolean;
  /** How ranks aggregate into one line. */
  agg?: "mean" | "max";
  gen: (p: SysPt) => number;
}

const sj = (p: SysPt, k: string) => jit(`${k}-${p.run.key}-${p.rank}`, p.t / 60_000);
const a100 = (p: SysPt) => poolOfNode(p.node)?.product.includes("A100") ?? false;

export const SYS_KEYS: readonly SysDef[] = [
  {
    key: "system/sm_active",
    label: "SM active",
    unit: "ratio",
    gen: (p) =>
      p.lost ? NaN : p.hang ? (p.rank === STRAGGLER_RANK ? 0.02 : 0.99) : a100(p) ? 0.55 + 0.03 * sj(p, "sm") : p.hot > 0 ? 0.77 - 0.08 * p.hot + 0.005 * sj(p, "sm") : (p.straggling && Math.floor(p.rank / 8) === Math.floor(STRAGGLER_RANK / 8) ? 0.74 : 0.77) + 0.01 * sj(p, "sm"),
  },
  { key: "system/gpu_util", label: "GPU util", unit: "ratio", gen: (p) => (p.lost ? NaN : p.hang && p.rank === STRAGGLER_RANK ? 0 : Math.min(1, (a100(p) ? 0.83 : 0.97) + 0.01 * sj(p, "util"))) },
  { key: "system/gpu_mem_used_gb", label: "GPU memory used", unit: "GB", gen: (p) => (p.lost ? NaN : a100(p) ? 31.4 + 0.3 * sj(p, "mem") : 72.4 + (p.rank === 0 ? 1.2 : 0) + 0.3 * sj(p, "mem")) },
  {
    key: "system/power_w",
    label: "Power",
    unit: "W",
    gen: (p) => (p.lost ? NaN : p.hang ? (p.rank === STRAGGLER_RANK ? 92 : 168) : a100(p) ? 262 + 8 * sj(p, "pw") : p.hot > 0 ? 610 - 155 * p.hot + 6 * sj(p, "pw") : (p.straggling ? 540 : 610) + 10 * sj(p, "pw")),
  },
  { key: "system/power_limit_w", label: "Power limit", unit: "W", agg: "max", gen: (p) => (a100(p) ? 400 : 700) },
  { key: "system/gpu_temp_c", label: "GPU temp", unit: "C", agg: "max", gen: (p) => (p.lost ? NaN : p.hot > 0 ? 66 + 21 * p.hot + 0.4 * sj(p, "t") : (a100(p) ? 57 : 64) + (p.rank % 8) * 0.6 + 0.8 * sj(p, "t")) },
  { key: "system/hbm_temp_c", label: "HBM temp", unit: "C", agg: "max", gen: (p) => (p.lost ? NaN : p.hot > 0 ? 72 + 23 * p.hot + 0.4 * sj(p, "ht") : (a100(p) ? 61 : 70) + 0.8 * sj(p, "ht")) },
  { key: "system/nvlink_tx_gbps", label: "NVLink tx", unit: "GB/s", gen: (p) => (p.lost ? NaN : p.hang ? 0 : a100(p) ? 64 + 3 * sj(p, "nv") : (p.straggling ? 150 : 182) + 5 * sj(p, "nv")) },
  { key: "system/ib_tx_gbps", label: "Network tx", unit: "GB/s", node: true, gen: (p) => (p.hang ? 0.1 : a100(p) ? 0.4 : (p.straggling ? 35 : 41) + 2 * sj(p, "ib")) },
  { key: "system/host_cpu", label: "Host CPU", unit: "ratio", node: true, gen: (p) => (a100(p) ? 0.22 : 0.34) + 0.03 * sj(p, "cpu") },
  { key: "system/host_mem", label: "Host memory", unit: "ratio", node: true, gen: (p) => (a100(p) ? 0.28 : 0.41) + 0.01 * sj(p, "hm") },
  { key: "system/shm_used_gb", label: "/dev/shm used", unit: "GB", node: true, gen: (p) => (p.run.key === "v1" ? 0.0625 : a100(p) ? 2.1 : 18.5 + 0.4 * sj(p, "shm")) },
  { key: "system/clock_event_hw_slowdown", label: "HW slowdown (share of time)", unit: "ratio", agg: "max", gen: (p) => (p.hot > 0 ? 0.64 * p.hot + 0.03 * sj(p, "hw") : 0) },
];

const ALL_KEYS: readonly string[] = [...TRAIN_KEYS.map((k) => k.key), ...SYS_KEYS.map((k) => k.key)];

/** A request's keys with "prefix/*" expanded; empty means every training (step-axis) key. */
export function expandKeys(keys: readonly string[]): string[] {
  if (keys.length === 0) return TRAIN_KEYS.map((k) => k.key);
  const out: string[] = [];
  for (const k of keys) {
    if (k.endsWith("/*")) out.push(...ALL_KEYS.filter((x) => x.startsWith(k.slice(0, -1))));
    else out.push(k);
  }
  return [...new Set(out)];
}

// ── QueryMlSeries ─────────────────────────────────────────────────────────────

interface SeriesReq {
  axis: MlAxis;
  groupBy: string;
  maxPoints: number;
  fromMs: number | null;
  toMs: number | null;
}

function missing(key: string, label: string, unit: string, axis: MlAxis, subject: SubjectInit, collection: MlCollection, source = "", findingId = ""): SeriesInit {
  return { key, label, unit, axis, x: [], y: [], collection, missingSource: source, fixFindingId: findingId, subject };
}

function xOf(a: TAttempt, run: TRun, step: number, axis: MlAxis, now: () => number): number {
  if (axis === MlAxis.TIME) return Math.floor(timeOfStep(a, step, now) / 1000);
  if (axis === MlAxis.TOKENS) return step * run.tokensPerStep;
  return step;
}

function trainSeries(scopeIn: Scope, def: KeyDef, subject: SubjectInit, req: SeriesReq, now: () => number): SeriesInit[] {
  // eval-periodic's scores are of the SFT checkpoints: the phase answers them on SFT's steps.
  const scope = scopeIn.run.full && scopeIn.phase === V3_PHASES.evalPeriodic.key && def.key.startsWith("eval/") ? { ...scopeIn, phase: "sft", attempts: scopeIn.run.attempts.filter((a) => a.phase === "sft") } : scopeIn;
  const { run, phase } = scope;
  const axis = req.axis;
  if (!run.instrumented) return [missing(def.key, def.label, def.unit, axis, subject, MlCollection.NOT_COLLECTED, "training instrumentation (ml/instrument skill, MLflow or W&B)", FINDINGS.trainingUntracked)];
  if (def.key.startsWith("rl/") && phase === V3_PHASES.rl.key) return [missing(def.key, def.label, def.unit, axis, subject, MlCollection.NOT_COLLECTED, "phase has not started")];
  if (run.pendingPhases.includes(phase) || scope.attempts.length === 0) return [missing(def.key, def.label, def.unit, axis, subject, MlCollection.NOT_COLLECTED, "phase has not started")];
  if (def.na) return [missing(def.key, def.label, def.unit, axis, subject, MlCollection.NOT_APPLICABLE, def.na)];
  if (!run.full && !def.simple) return [missing(def.key, def.label, def.unit, axis, subject, MlCollection.NOT_COLLECTED, "a newer trainer callback (this run's trainer did not log it)")];
  if (def.key.startsWith("eval/") && phase !== "sft") return [missing(def.key, def.label, def.unit, axis, subject, MlCollection.NOT_APPLICABLE, "eval-periodic evaluates SFT checkpoints")];
  if (!def.gen) return [];

  const lo = Math.min(...scope.attempts.map((a) => a.startStep));
  const hi = Math.max(...scope.attempts.map((a) => a.endStep));
  const res = def.every ?? niceStep((hi - lo) / Math.max(10, req.maxPoints));
  const gen = def.gen;

  const build = (attempts: readonly TAttempt[], rank: number | null, variant: string, extra: Partial<SeriesInit>): SeriesInit => {
    const x: number[] = [];
    const y: number[] = [];
    for (const a of attempts) {
      const steps = def.every
        ? Array.from({ length: Math.floor(a.endStep / def.every) + 1 }, (_, i) => i * def.every!).filter((s) => s > a.startStep && s <= (def.fromCheckpoints ? a.lastCheckpointStep : a.endStep) && (!def.key.startsWith("eval/") || s <= 40_000))
        : def.fromCheckpoints
          ? Array.from({ length: Math.floor(a.lastCheckpointStep / (run.checkpointEvery[a.phase] ?? 1000)) + 1 }, (_, i) => i * (run.checkpointEvery[a.phase] ?? 1000)).filter((s) => s > a.startStep && s <= a.lastCheckpointStep)
          : stepsOf(a, res);
      for (const step of steps) {
        const xv = xOf(a, run, step, axis, now);
        if (axis === MlAxis.TIME && ((req.fromMs !== null && xv * 1000 < req.fromMs) || (req.toMs !== null && xv * 1000 > req.toMs))) continue;
        x.push(xv);
        y.push(gen({ run, a, step, straggling: stragglingAt(run, a, step), rank, res }, variant));
      }
    }
    return { key: def.key, label: def.label, unit: def.unit, axis, x, y, resolution: def.every ?? res, collection: MlCollection.COLLECTED, subject, ...extra };
  };

  const variants = def.stats ?? def.groups ?? [""];
  const out: SeriesInit[] = [];
  const groupOf = (v: string) => (def.groups ? { group: v } : def.stats ? { stat: v } : {});

  if (def.phaseWide) {
    // One line for the phase: the checkpoints every attempt left behind.
    for (const v of variants) out.push(build(scope.attempts, null, v, { ...groupOf(v), subject: scope.single ? subject : phaseSubject(run, phase) }));
    return out;
  }
  if (req.groupBy === "rank" && def.perRank && scope.rank === null) {
    const a = scope.attempts[scope.attempts.length - 1];
    for (let r = 0; r < a.worldSize; r++) out.push(build([a], r, def.stats ? "p50" : "", { group: `rank ${r}`, attempt: a.n, subject: rankSubject(run, a, r) }));
    return out;
  }
  for (const a of scope.attempts) {
    for (const v of variants) {
      const perAttempt = !scope.single;
      out.push(
        build([a], scope.rank, v, {
          ...groupOf(v),
          ...(perAttempt ? { group: def.groups ? `${v} · attempt ${a.n}` : `attempt ${a.n}` } : {}),
          attempt: a.n,
          subject: scope.rank !== null ? rankSubject(run, a, scope.rank) : attemptSubject(run, a),
        }),
      );
    }
  }
  return out;
}

function sysPoint(def: SysDef, run: TRun, a: TAttempt, rank: number, t: number, now: () => number): number {
  const since = msAt(STRAGGLER_SINCE, now);
  const sft3 = isV3(run) && a.phase === "sft" && a.n === 3;
  const end = attemptEndMs(a, now);
  const node = nodeOfRank(a, rank);
  const p: SysPt = {
    run,
    a,
    rank,
    node,
    t,
    hot: sft3 && rank === STRAGGLER_RANK && t >= since ? Math.min(1, (t - since) / (30 * MINUTE)) : 0,
    straggling: sft3 && t >= since,
    hang: isV3(run) && a.phase === "sft" && a.n === 1 && t >= end - 10 * MINUTE,
    lost: isV3(run) && a.phase === "sft" && a.n === 2 && node === BAD_NODE && rank % a.gpusPerNode === BAD_GPU_INDEX && t >= end - 2 * MINUTE,
  };
  return def.gen(p);
}

function systemSeries(scope: Scope, def: SysDef, subject: SubjectInit, req: SeriesReq, now: () => number): SeriesInit[] {
  const { run } = scope;
  const axis = req.axis === MlAxis.STEP && run.instrumented ? MlAxis.STEP : MlAxis.TIME;
  if (scope.attempts.length === 0) return [missing(def.key, def.label, def.unit, axis, subject, MlCollection.NOT_COLLECTED, "phase has not started")];
  const grouped = req.groupBy === "rank" || req.groupBy === "node";
  const attempts = scope.single || !grouped ? scope.attempts : [scope.attempts[scope.attempts.length - 1]];
  const out: SeriesInit[] = [];
  for (const a of attempts) {
    const start = msAt(a.runningAt, now);
    const end = attemptEndMs(a, now);
    const from = Math.max(start, req.fromMs ?? start);
    const to = Math.min(end, req.toMs ?? end);
    if (to <= from) continue;
    const resSec = niceSeconds((to - from) / 1000 / Math.max(8, req.maxPoints));
    const ts: number[] = [];
    for (let t = Math.ceil(from / (resSec * 1000)) * resSec * 1000; t <= to; t += resSec * 1000) ts.push(t);
    const xs = ts.map((t) => (axis === MlAxis.STEP ? stepAtTime(a, t, now) : Math.floor(t / 1000)));
    const ranks = scope.rank !== null ? [scope.rank] : Array.from({ length: a.worldSize }, (_, r) => r);
    const nodes = [...new Set(ranks.map((r) => Math.floor(r / a.gpusPerNode)))];
    const base = { key: def.key, label: def.label, unit: def.unit, axis, resolution: axis === MlAxis.STEP ? a.stepsPerHour * (resSec / 3600) : resSec, collection: MlCollection.COLLECTED, attempt: a.n };
    const line = (rs: readonly number[]) =>
      ts.map((t) => {
        const vs = rs.map((r) => sysPoint(def, run, a, r, t, now));
        const finite = vs.filter((v) => Number.isFinite(v));
        if (finite.length === 0) return NaN;
        return def.agg === "max" ? Math.max(...finite) : finite.reduce((s, v) => s + v, 0) / finite.length;
      });
    const nodeRanks = (ni: number) => (def.node ? [ni * a.gpusPerNode] : ranks.filter((r) => Math.floor(r / a.gpusPerNode) === ni));
    if (req.groupBy === "rank" && !def.node && scope.rank === null) {
      for (const r of ranks) out.push({ ...base, x: xs, y: line([r]), group: `rank ${r}`, subject: rankSubject(run, a, r) });
    } else if ((req.groupBy === "node" || req.groupBy === "rank") && scope.rank === null) {
      for (const ni of nodes) {
        const name = a.nodes[ni];
        out.push({ ...base, x: xs, y: line(nodeRanks(ni)), group: name.replace(/^gke-(train|dev)-/, ""), subject: { kind: MlSubjectKind.NODE, id: nodeIdOf(name), label: name } });
      }
    } else {
      const rs = def.node ? nodes.map((ni) => ni * a.gpusPerNode) : ranks;
      out.push({
        ...base,
        x: xs,
        y: line(rs),
        group: scope.single ? "" : `attempt ${a.n}`,
        subject: scope.rank !== null ? rankSubject(run, a, scope.rank) : attemptSubject(run, a),
        ...(rs.length > 1 ? { stat: def.agg === "max" ? "max" : "" } : {}),
      });
    }
  }
  return out;
}

/** Every run of an experiment, one line each (the latest attempt wins where attempts overlap). */
function experimentSeries(experimentId: string, keys: readonly string[], req: SeriesReq, now: () => number): SeriesInit[] | undefined {
  const runs = TRUNS.filter((r) => r.experimentId === experimentId);
  if (runs.length === 0) return undefined;
  const out: SeriesInit[] = [];
  for (const run of runs) {
    const scope: Scope = { run, phase: run.full ? "sft" : run.currentPhase, attempts: run.attempts.filter((a) => a.phase === (run.full ? "sft" : run.currentPhase)), rank: null, single: false };
    for (const key of keys) {
      const def = TRAIN_KEYS.find((k) => k.key === key);
      if (!def) continue;
      const parts = trainSeries(scope, def, runSubject(run), { ...req, groupBy: "" }, now);
      out.push(mergeLines(parts, run));
    }
  }
  return out;
}

/** Per-attempt lines of one key as one line: a later attempt's points replace an earlier one's at and after its first x. */
export function mergeLines(parts: readonly SeriesInit[], run: TRun): SeriesInit {
  const head = parts[0];
  const collected = parts.filter((p) => p.collection === MlCollection.COLLECTED && (p.x?.length ?? 0) > 0);
  if (collected.length === 0) return { ...head, group: run.name, subject: runSubject(run) };
  const x: number[] = [];
  const y: number[] = [];
  for (const p of [...collected].sort((a, b) => (a.attempt ?? 0) - (b.attempt ?? 0))) {
    const first = p.x![0];
    while (x.length && x[x.length - 1] >= first) {
      x.pop();
      y.pop();
    }
    x.push(...p.x!);
    y.push(...p.y!);
  }
  return { ...collected[0], x, y, group: run.name, attempt: 0, subject: runSubject(run) };
}

function querySeries(req: { subject?: { kind: MlSubjectKind; id: string; label: string }; keys: string[]; axis: MlAxis; groupBy?: string; maxPoints?: number; from?: { seconds: bigint; nanos: number }; to?: { seconds: bigint; nanos: number } }, now: () => number): SeriesInit[] | undefined {
  const sreq: SeriesReq = {
    axis: req.axis || MlAxis.STEP,
    groupBy: req.groupBy ?? "",
    maxPoints: req.maxPoints && req.maxPoints > 0 ? Math.min(req.maxPoints, 1000) : 300,
    fromMs: req.from ? Number(req.from.seconds) * 1000 : null,
    toMs: req.to ? Number(req.to.seconds) * 1000 : null,
  };
  const keys = expandKeys(req.keys);
  if (req.subject?.kind === MlSubjectKind.EXPERIMENT) return experimentSeries(req.subject.id, keys, sreq, now);
  const scope = scopeOf(req.subject);
  if (!scope || !req.subject) return undefined;
  const subject: SubjectInit = { kind: req.subject.kind, id: req.subject.id, label: req.subject.label };
  const out: SeriesInit[] = [];
  for (const key of keys) {
    const sys = SYS_KEYS.find((k) => k.key === key);
    if (sys) {
      out.push(...systemSeries(scope, sys, subject, sreq, now));
      continue;
    }
    const def = TRAIN_KEYS.find((k) => k.key === key);
    if (def) out.push(...trainSeries(scope, def, subject, sreq, now));
    else out.push(missing(key, key, "", sreq.axis, subject, MlCollection.NOT_COLLECTED, scope.run.instrumented ? "nothing logs this key for this run" : "training instrumentation (ml/instrument skill, MLflow or W&B)", scope.run.instrumented ? "" : FINDINGS.trainingUntracked));
  }
  return out;
}

// ── QueryMlHistograms ─────────────────────────────────────────────────────────

function gauss(r: () => number): number {
  const u = Math.max(1e-9, r());
  return Math.sqrt(-2 * Math.log(u)) * Math.cos(2 * Math.PI * r());
}

function pct(sorted: readonly number[], q: number): number {
  if (sorted.length === 0) return NaN;
  return sorted[Math.min(sorted.length - 1, Math.floor(q * sorted.length))];
}

function bucketsOf(values: readonly number[], lo: number, hi: number, width: number, scale = 1) {
  const n = Math.ceil((hi - lo) / width);
  const counts = new Array<number>(n).fill(0);
  for (const v of values) counts[Math.max(0, Math.min(n - 1, Math.floor((v - lo) / width)))]++;
  return counts.map((c, i) => ({ lower: lo + i * width, upper: lo + (i + 1) * width, count: BigInt(Math.round(c * scale)) }));
}

/** Step times (ms) of an attempt: one sample per step, the straggler's mode after it began. */
function stepTimeSamples(run: TRun, a: TAttempt): number[] {
  const r = rng(hash(`steps-${run.key}-${a.phase}-${a.n}`));
  const out: number[] = [];
  const stride = Math.max(1, Math.round((a.endStep - a.startStep) / 6000));
  for (let s = a.startStep + 1; s <= a.endStep; s += stride) {
    const straggling = stragglingAt(run, a, s);
    const base = isV3(run) ? v3StepTimeMs(a.phase, s, straggling) : (run.tokensPerStep / run.tpsBase) * 1000;
    let v = base * (1 + (straggling ? 0.022 : 0.014) * gauss(r));
    if (r() < (straggling ? 0.03 : 0.008)) v += base * (0.08 + 0.25 * r()); // data stalls, GC pauses, the straggler's worst steps
    out.push(v);
  }
  return out;
}

function histogram(key: string, label: string, unit: string, values: number[], lo: number, hi: number, width: number, compare?: { label: string; values: number[] }): HistogramInit {
  const sorted = [...values].sort((a, b) => a - b);
  return {
    key,
    label,
    unit,
    buckets: bucketsOf(values, lo, hi, width),
    p50: pct(sorted, 0.5),
    p95: pct(sorted, 0.95),
    p99: pct(sorted, 0.99),
    count: BigInt(values.length),
    ...(compare ? { compareLabel: compare.label, compareBuckets: bucketsOf(compare.values, lo, hi, width, values.length / Math.max(1, compare.values.length)) } : {}),
    collection: MlCollection.COLLECTED,
  };
}

function queryHistograms(scope: Scope, keys: readonly string[]): HistogramInit[] {
  const { run } = scope;
  const wanted = keys.length ? keys : ["train.step_time", "ckpt.save_time", "data.loader_wait"];
  const a = scope.attempts[scope.attempts.length - 1];
  const prev = scope.single ? run.attempts.find((x) => x.phase === a?.phase && x.n === (a?.n ?? 0) - 1) : scope.attempts[scope.attempts.length - 2];
  const out: HistogramInit[] = [];
  for (const key of wanted) {
    const label = key === "train.step_time" ? "Step time" : key === "ckpt.save_time" ? "Checkpoint save time" : key === "data.loader_wait" ? "Data loader wait" : key;
    if (!run.instrumented) {
      out.push({ key, label, unit: "ms", collection: MlCollection.NOT_COLLECTED, missingSource: "training instrumentation (ml/instrument skill, MLflow or W&B)" });
      continue;
    }
    if (!a) {
      out.push({ key, label, unit: "ms", collection: MlCollection.NOT_COLLECTED, missingSource: "phase has not started" });
      continue;
    }
    if (key === "train.step_time") {
      const values = stepTimeSamples(run, a);
      const base = isV3(run) ? v3StepTimeMs(a.phase, a.endStep, false) : (run.tokensPerStep / run.tpsBase) * 1000;
      out.push(histogram(key, label, "ms", values, base * 0.9, base * 1.45, base * 0.0125, prev ? { label: `attempt ${prev.n}`, values: stepTimeSamples(run, prev) } : undefined));
    } else if (key === "ckpt.save_time") {
      const every = run.checkpointEvery[a.phase] ?? 1000;
      const sample = (x: TAttempt) => {
        const r = rng(hash(`ckpt-${run.key}-${x.phase}-${x.n}`));
        return Array.from({ length: Math.max(1, Math.floor((x.lastCheckpointStep - x.startStep) / every)) }, () => 41 + 1.8 * gauss(r) + (r() < 0.05 ? 9 : 0));
      };
      out.push(histogram(key, label, "s", sample(a), 34, 56, 1, prev ? { label: `attempt ${prev.n}`, values: sample(prev) } : undefined));
    } else if (key === "data.loader_wait") {
      const r = rng(hash(`lw-${run.key}-${a.phase}-${a.n}`));
      const values = Array.from({ length: 4000 }, () => Math.exp(Math.log(run.key === "v1" ? 210 : 40) + 0.45 * gauss(r)));
      out.push(histogram(key, label, "ms", values, 0, run.key === "v1" ? 800 : 300, run.key === "v1" ? 25 : 10));
    } else {
      out.push({ key, label, unit: "", collection: MlCollection.NOT_COLLECTED, missingSource: "nothing reports this histogram for training" });
    }
  }
  return out;
}

// ── ListMlLogs ────────────────────────────────────────────────────────────────

const LEVELS = ["debug", "info", "warn", "error"] as const;
const levelRank = (l: string) => Math.max(0, LEVELS.indexOf(l as (typeof LEVELS)[number]));

const fmtN = (n: number) => Math.round(n).toLocaleString("en-US");

/** The generated pod logs of one attempt, oldest first. */
function attemptLogs(run: TRun, a: TAttempt, now: () => number): LogInit[] {
  const lines: LogInit[] = [];
  const wl = workloadName(run.name, a.phase, a.n);
  const launcher = `${wl}-launcher`;
  const nodes = a.nodes.length;
  const add = (t: number, rank: number, level: string, message: string, opts: { highlight?: string; traceId?: string; container?: string; node?: string; pod?: string } = {}) => {
    const node = opts.node ?? (rank >= 0 ? nodeOfRank(a, rank) : a.nodes[0]);
    lines.push({
      time: timestampFromMs(Math.round(t)),
      rank,
      pod: opts.pod ?? (rank >= 0 ? rankPod(run.name, a.phase, a.n, rank, a.gpusPerNode) : launcher),
      nodeId: nodeIdOf(node),
      container: opts.container ?? (rank >= 0 ? "trainer" : "launcher"),
      level,
      message,
      highlight: opts.highlight ?? "",
      attempt: a.n,
      runId: run.id,
      phaseKey: a.phase,
      traceId: opts.traceId ?? "",
    });
  };
  const t0 = msAt(a.runningAt, now);
  const end = attemptEndMs(a, now);
  const st = (step: number) => timeOfStep(a, step, now);

  // Startup: Kueue, torchrun, torchtitan, NCCL, the checkpoint load.
  // Startup, backwards from the first step: checkpoint load and NCCL init as training.ts's recoveries time them.
  const STARTUP: Record<string, [nccl: number, load: number]> = { "pretrain/2": [68, 414], "sft/2": [71, 341], "sft/3": [64, 318] };
  const [ncclS, loadS] = STARTUP[`${a.phase}/${a.n}`] ?? [66, a.startStep > 0 ? 300 : 0];
  const loadStart = t0 - loadS * 1000;
  const ncclStart = loadStart - ncclS * 1000 - 2000;
  const boot = ncclStart - 40_000;
  if (run.instrumented) {
    add(boot - 30_000, -1, "info", `[kueue] Workload ${wl} admitted by ClusterQueue team-research (flavor h100, ${a.worldSize} GPUs)`, { container: "kueue" });
    add(boot - 20_000, -1, "info", `torchrun --nnodes=${nodes} --nproc-per-node=${a.gpusPerNode} --rdzv-backend=c10d --rdzv-endpoint=${wl}-w-0:29400 -m torchtitan.train --job.config_file=/config/${a.phase}.toml`);
    add(boot, 0, "info", `torchtitan: Starting job: ${run.name} ${a.phase} (attempt ${a.n})`, a.phase === "sft" && a.n === 3 && isV3(run) ? { traceId: TRACES.podStart } : {});
    add(boot + 3_000, 0, "info", `Building llama3 8B with ModelArgs(dim=4096, n_layers=32, n_heads=32, n_kv_heads=8, vocab_size=128256, max_seq_len=${a.phase === "pretrain" ? 8192 : 4096})`);
    add(boot + 6_000, 0, "info", "Model llama3 8B size: 8,030,261,248 total parameters");
    add(boot + 9_000, 0, "info", `Applied FSDP to the model (dp_shard=${Math.max(1, a.worldSize / 8)}), tensor parallel degree 8, activation checkpointing: selective`);
    add(ncclStart, 0, "info", "NCCL INFO NCCL version 2.21.5+cuda12.4", { container: "trainer" });
    add(ncclStart + 1_000, 0, "info", "NCCL INFO NET/GPUDirectTCPX : Using 8 NICs [0]eth1 ... [7]eth8; OOB eth0");
    add(ncclStart + 2_000, 0, "info", `NCCL INFO comm 0x7f3a2c000000 rank 0 nRanks ${a.worldSize} nNodes ${nodes} localRanks 8 localRank 0 MNNVL 0`);
    if (a.worldSize > 37) add(ncclStart + 2_500, STRAGGLER_RANK, "info", `NCCL INFO comm 0x7f61f8000000 rank 37 nRanks ${a.worldSize} nNodes ${nodes} localRanks 8 localRank 5 MNNVL 0`);
    add(loadStart - 2000, 0, "info", `NCCL INFO Init COMPLETE in ${ncclS.toFixed(1)} s (nRanks ${a.worldSize})`);
    if (a.startStep > 0) {
      add(loadStart, 0, "info", `Loading checkpoint from gs://acme-ml-checkpoints/${run.name}/${a.phase}/step-${String(a.startStep).padStart(6, "0")}/ (sharded-dcp, 212 GB)`);
      add(t0 - 500, 0, "info", `Finished loading the checkpoint in ${loadS.toFixed(1)} seconds. Resuming at step ${fmtN(a.startStep + 1)}`);
    }
    add(t0, 0, "info", `Training starts at step ${fmtN(a.startStep + 1)}`);
  } else {
    add(boot, -1, "info", "python -m rerank.train --config nightly.yaml (pod started by CronJob reranker-nightly)", { container: "rerank" });
    add(t0, 0, "info", "loading bge-reranker base weights from gs://acme-ml-models/bge-reranker/v4", { container: "rerank" });
    add(t0 + 2 * MINUTE, 0, "info", "epoch 1/3 ...", { container: "rerank" });
    add(t0 + 80 * MINUTE, 0, "info", "epoch 2/3 ...", { container: "rerank" });
    add(t0 + 160 * MINUTE, 0, "info", "epoch 3/3 ...", { container: "rerank" });
    add(end - 2 * MINUTE, 0, "info", "wrote gs://acme-ml-models/bge-reranker/nightly-20261125 (no metrics logged)", { container: "rerank" });
    return lines;
  }

  // Step lines from rank 0 every N steps (torchtitan's format: tps is per GPU).
  const every = niceStep((a.endStep - a.startStep) / 40);
  for (let s = Math.ceil((a.startStep + 1) / every) * every; s <= a.endStep; s += every) {
    const p: Pt = { run, a, step: s, straggling: stragglingAt(run, a, s), rank: null, res: every };
    const tps = tpsOf(p) / a.worldSize;
    const mfu = mfuOf(p);
    const mem = memory(p, "reserved", "");
    add(st(s), 0, "info", `step: ${fmtN(s)}  loss: ${lossOf(p).toFixed(4)}  memory: ${mem.toFixed(2)}GiB(${((mem / 80) * 100).toFixed(2)}%)  tps: ${fmtN(tps)}  tflops: ${(mfu * 989).toFixed(2)}  mfu: ${(mfu * 100).toFixed(2)}%`);
  }

  // Checkpoints.
  const ckEvery = run.checkpointEvery[a.phase] ?? 1000;
  for (let s = Math.ceil((a.startStep + 1) / ckEvery) * ckEvery; s <= a.lastCheckpointStep; s += ckEvery) {
    const r = rng(hash(`ckpt-${a.phase}-${s}`));
    const save = 38 + 6 * r();
    const upload = 132 + 20 * r();
    const blocking = (a.phase === "pretrain" ? 9 : run.blockingSeconds) + 2 * (r() - 0.5);
    const traced = isV3(run) && a.phase === "sft" && s === 41_000 ? TRACES.checkpoint : s === 40_000 && isV3(run) && a.phase === "sft" ? CHECKPOINT_40K : undefined;
    const uri = `gs://acme-ml-checkpoints/${run.name}/${a.phase}/step-${String(s).padStart(6, "0")}/`;
    add(st(s) + 300, 0, "info", `Saving the checkpoint (async) at step ${fmtN(s)} → ${uri}`, traced ? { traceId: traced } : {});
    add(st(s) + 300 + blocking * 1000, 0, "info", `Staged the checkpoint in ${blocking.toFixed(2)} seconds (training resumed; async save continues)`, traced ? { traceId: traced } : {});
    if (s <= a.lastCheckpointStep) add(st(s) + (save + upload) * 1000, 0, "info", `Checkpoint step ${fmtN(s)} durable: 212 GB, saved in ${save.toFixed(1)} s, uploaded in ${upload.toFixed(0)} s`, traced ? { traceId: traced } : {});
  }

  if (isV3(run) && a.phase === "sft" && a.n === 3) {
    add(st(36_412), 0, "debug", "step 36,412 sampled for tracing (1 in 250 steps): 10.02 s", { traceId: TRACES.normalStep });
    const since = msAt(STRAGGLER_SINCE, now);
    add(since, -1, "warn", `[forge] straggler detected: rank 37 step compute +18% vs median for 20 min (HW slowdown on ${HOT_NODE} GPU 5, 87°C, HBM 95°C); alert alert-straggler firing`, { highlight: "straggler", container: "forge" });
    add(since + 4 * MINUTE, STRAGGLER_RANK, "warn", "[rank37] torch.cuda: clocks throttled: SM 1410 MHz of 1980 MHz (clock_event_reasons=HW_SLOWDOWN|HW_THERMAL_SLOWDOWN)", { highlight: "straggler" });
    for (let s = 37_000; s <= a.endStep; s += 1000) add(st(s) + 2000, 0, "warn", `[rank0] slowest rank 37: step compute 1.18x median over the last 1,000 steps; other ranks wait ${(timer({ run, a, step: s, straggling: true, rank: 0, res: 1 }, "comm") / 1000).toFixed(2)} s/step in allreduce`, { highlight: "straggler" });
    add(st(38_720), 0, "warn", "[rank0] loss spike detected at step 38,720: loss 2.1905 (z=6.8), grad_norm 4.43 (clipped); data shard support-sft-v3/shard-01184", { highlight: "loss_spike" });
    add(st(38_720) + 1500, 0, "warn", "[rank0] NaN guard: skipped optimizer step 38,720 (non-finite grads in layers.30.attention.wq)", { highlight: "nan" });
    add(st(38_722), 0, "info", "[rank0] loss recovered to 1.83 by step 39,050 (spike lasted 330 steps)");
    add(st(41_150), 0, "debug", "dataloader: batch for step 41,150 traced: 4 workers, fetch p99 112 ms, tokenize 18 ms", { traceId: TRACES.dataPipeline });
    add(st(41_187), 0, "warn", "[rank0] step 41,187 took 11.6 s (1.16x the pre-straggler median): rank 37 arrived 1.8 s late at nccl:all_reduce", { highlight: "straggler", traceId: TRACES.slowStep });
  }
  if (isV3(run) && a.phase === "sft" && a.n === 1) {
    const t = end - 10 * MINUTE;
    add(t - 1500, STRAGGLER_RANK, "warn", "[rank37] NCCL WARN last enqueued collective SeqNum=88212 (ALLREDUCE) completed; rank 37 has not launched SeqNum=88213", { highlight: "nccl" });
    for (const r of [0, 36, 38]) {
      add(end - 2000 + r, r, "error", `[rank${r}] NCCL WARN Watchdog caught collective operation timeout: WorkNCCL(SeqNum=88213, OpType=ALLREDUCE, NumelIn=1, NumelOut=1, Timeout(ms)=600000) ran for 600012 milliseconds before timing out.`, { highlight: "watchdog", traceId: HANG_TRACE });
    }
    add(end - 1500, 0, "error", "[rank0] ... 60 more ranks reported the same timeout; rank 37 is the only rank not in SeqNum=88213", { highlight: "watchdog" });
    add(end - 1000, 0, "error", "Traceback (most recent call last):", { highlight: "traceback" });
    add(end - 999, 0, "error", '  File "/app/torchtitan/train.py", line 412, in train_step', { highlight: "traceback" });
    add(end - 998, 0, "error", "    torch.nn.utils.clip_grad_norm_(model.parameters(), max_norm=1.0)", { highlight: "traceback" });
    add(end - 997, 0, "error", "torch.distributed.DistBackendError: [Rank 0] Watchdog caught collective operation timeout: WorkNCCL(SeqNum=88213, OpType=ALLREDUCE, Timeout(ms)=600000)", { highlight: "traceback" });
    add(end - 800, 0, "error", "[rank0] Flight recorder dump written to /fr/nccl_trace_rank_0 (TORCH_NCCL_DUMP_ON_TIMEOUT=1); collected by forge", { highlight: "nccl" });
    add(end, -1, "error", "torchrun: worker group failed: rank 0 (local_rank 0) exited with code -6 (SIGABRT); tearing down all 64 workers");
  }
  if (isV3(run) && a.phase === "sft" && a.n === 2) {
    const badRank = H100.indexOf(BAD_NODE) * 8 + BAD_GPU_INDEX;
    add(end - 90_000, -1, "error", `NVRM: Xid (PCI:0000:9a:00): 79, pid=48213, GPU has fallen off the bus.`, { highlight: "xid", container: "dmesg", node: BAD_NODE, pod: `node/${BAD_NODE}` });
    add(end - 89_000, badRank, "error", `[rank${badRank}] RuntimeError: CUDA error: unspecified launch failure (cudaErrorLaunchFailure); device ${gpuUuid(BAD_NODE, BAD_GPU_INDEX)} is lost`, { highlight: "xid" });
    add(end - 88_900, badRank, "error", "Traceback (most recent call last):", { highlight: "traceback" });
    add(end - 88_800, badRank, "error", '  File "/app/torchtitan/train.py", line 398, in train_step', { highlight: "traceback" });
    add(end - 88_700, badRank, "error", "    loss.backward()", { highlight: "traceback" });
    add(end - 80_000, 0, "error", `[rank0] NCCL WARN Cuda failure 'unspecified launch failure' from rank ${badRank}; aborting communicator`, { highlight: "nccl" });
    add(end - 60_000, -1, "warn", `[forge] alert alert-xid-fatal fired: cordoned ${BAD_NODE}, swapping in hot spare ${SPARE_NODE}, attempt 3 resumes from step 32,000`, { container: "forge" });
    add(end, -1, "error", "torchrun: worker group failed (CUDA error on rank 21); exit code 1");
  }
  if (isV3(run) && a.phase === "pretrain" && a.n === 1) {
    add(end - 60_000, -1, "warn", `[kueue] Workload ${wl} evicted: Preempted (InCohortReclamation by team-serving-batch/${PREEMPTOR_WORKLOAD})`, { container: "kueue" });
    add(end - 118_000, 0, "warn", "[rank0] received SIGTERM at step 6,400: starting an emergency checkpoint (grace period 120 s)");
    add(end - 2_000, 0, "warn", "[rank0] emergency checkpoint at step 6,400 incomplete when the grace period ended (PARTIAL); last durable checkpoint 6,000: 400 steps will be redone");
    add(end, -1, "info", "torchrun: all workers exited after SIGTERM (graceful termination 30 s)");
  }
  if (a.end === "completed") add(end, 0, "info", `Training completed at step ${fmtN(a.endStep)}`);
  if (run.key === "v1") {
    add(end - 4000, 0, "error", "Bus error (core dumped)", { highlight: "shm" });
    add(end - 3000, 0, "error", "RuntimeError: DataLoader worker (pid 4123) is killed by signal: Bus error. It is possible that dataloader's workers are out of shared memory. Please try to raise your shared memory limit.", { highlight: "shm" });
    add(end - 2900, 0, "error", "Traceback (most recent call last):", { highlight: "traceback" });
    add(end - 2800, 0, "error", '  File "/usr/lib/python3.11/site-packages/torch/utils/data/dataloader.py", line 1133, in _try_get_data', { highlight: "traceback" });
    add(end - 2000, -1, "warn", "[forge] /dev/shm is a 64Mi emptyDir (sizeLimit unset on the pod): 8 DataLoader workers need ~16 GiB", { container: "forge", highlight: "shm" });
    add(end, -1, "error", "torchrun: worker group failed: rank 0 exited with code 1");
  }
  if (run.divergeAfter) add(st(run.divergeAfter + 100), 0, "warn", `[rank0] val loss rising since step ${fmtN(run.divergeAfter)} at lr ${run.lrPeak.toExponential(1)} (now ${run.val(run.divergeAfter + 100).toFixed(3)})`, { highlight: "loss_spike" });
  return lines;
}

function listLogs(scope: Scope, req: { rank?: number; minLevel?: string; query?: string; highlightsOnly?: boolean; limit?: number; from?: { seconds: bigint }; to?: { seconds: bigint } }, now: () => number) {
  let lines = scope.attempts.flatMap((a) => attemptLogs(scope.run, a, now));
  const tms = (l: LogInit) => Number(l.time!.seconds) * 1000 + Math.floor((l.time!.nanos ?? 0) / 1e6);
  const rank = req.rank ?? scope.rank;
  if (rank !== undefined && rank !== null) lines = lines.filter((l) => l.rank === rank || (scope.rank !== null && l.rank === -1));
  if (req.minLevel) lines = lines.filter((l) => levelRank(l.level ?? "info") >= levelRank(req.minLevel!));
  if (req.query) {
    const q = req.query.toLowerCase();
    lines = lines.filter((l) => (l.message ?? "").toLowerCase().includes(q));
  }
  if (req.highlightsOnly) lines = lines.filter((l) => l.highlight);
  if (req.from) lines = lines.filter((l) => tms(l) >= Number(req.from!.seconds) * 1000);
  if (req.to) lines = lines.filter((l) => tms(l) <= Number(req.to!.seconds) * 1000);
  lines.sort((a, b) => tms(a) - tms(b));
  const limit = req.limit && req.limit > 0 ? req.limit : 1000;
  return { lines: lines.slice(-limit), truncated: lines.length > limit };
}

// ── Traces ────────────────────────────────────────────────────────────────────

const HANG_TRACE = hexId("v3-sft-a1-hang-12400");
const XID_TRACE = hexId("v3-sft-a2-xid-32750");
const CHECKPOINT_40K = hexId("v3-sft-a3-ckpt-40000");

interface BuiltTrace {
  summary: SummaryInit;
  spans: SpanInit[];
}

class SpanBuilder {
  spans: SpanInit[] = [];
  private n = 0;
  private readonly seed: string;
  private readonly run: TRun;
  private readonly a: TAttempt;
  constructor(seed: string, run: TRun, a: TAttempt) {
    this.seed = seed;
    this.run = run;
    this.a = a;
  }
  /** A span; `rank` sets every join key (pod, node, GPU) from the attempt's placement. -1 for pod- or workload-level spans. */
  add(o: { name: string; parent?: string; start: number; ms: number; service?: string; kind?: string; rank?: number; node?: string; pod?: string; device?: string; attrs?: Record<string, string>; error?: string; events?: { t: number; name: string; attrs?: Record<string, string> }[] }): string {
    const id = hexId(`${this.seed}/${this.n++}`, 2);
    const rank = o.rank ?? -1;
    const node = o.node ?? (rank >= 0 ? nodeOfRank(this.a, rank) : "");
    this.spans.push({
      spanId: id,
      parentSpanId: o.parent ?? "",
      name: o.name,
      service: o.service ?? "trainer",
      kind: o.kind ?? "internal",
      start: timestampFromMs(Math.round(o.start)),
      durationMs: o.ms,
      status: o.error ? MlSpanStatus.ERROR : MlSpanStatus.OK,
      statusMessage: o.error ?? "",
      attributes: o.attrs ?? {},
      events: (o.events ?? []).map((e) => ({ time: timestampFromMs(Math.round(e.t)), name: e.name, attributes: e.attrs ?? {} })),
      runId: this.run.id,
      phaseKey: this.a.phase,
      attempt: this.a.n,
      rank,
      nodeId: node ? nodeIdOf(node) : "",
      deviceUuid: o.device ?? (rank >= 0 ? deviceOfRank(this.a, rank) : ""),
      pod: o.pod ?? (rank >= 0 ? rankPod(this.run.name, this.a.phase, this.a.n, rank, this.a.gpusPerNode) : ""),
    });
    return id;
  }
}

const seqAt = (step: number) => 88_213 + (step - 12_400) * 7;

/**
 * One training step across a few ranks' lanes: data → forward (per layer
 * group, FSDP all-gathers) → backward (reduce-scatters) → the grad-norm
 * all-reduce → optimizer. A late rank's compute is slower, so every other
 * rank waits in the all-reduce; a hang never completes it; a lost GPU errors.
 */
function stepTrace(o: { traceId: string; run: TRun; a: TAttempt; step: number; ranks: readonly number[]; late?: number; lateFactor?: number; mode?: "normal" | "hang" | "xid"; context?: boolean; title: string }, now: () => number): BuiltTrace {
  const { run, a, step } = o;
  const b = new SpanBuilder(o.traceId, run, a);
  const t0 = timeOfStep(a, step, now);
  const base = v3StepTimeMs(a.phase, step, false);
  const straggling = stragglingAt(run, a, step);
  const f = (r: number) => (r === o.late ? (o.lateFactor ?? 1.244) : 1);
  const data = base * SHARE.data;
  const fwd = (r: number) => base * SHARE.forward * f(r);
  // Backward includes the exposed tail of the FSDP reduce-scatters (~17% of a step).
  const bwd = (r: number) => base * (SHARE.backward * f(r) + 0.17);
  const arrive = (r: number) => data + fwd(r) + bwd(r);
  const lastArrive = Math.max(...o.ranks.map(arrive));
  const HANG_MS = 600_000;
  const lost = o.mode === "xid" ? o.late : undefined;
  const allreduceEnd = o.mode === "hang" ? arrive(o.ranks[0]) + HANG_MS : lastArrive + base * 0.012;
  const opt = base * SHARE.optimizer;
  const total = o.mode === "normal" || !o.mode ? allreduceEnd + opt : o.mode === "hang" ? allreduceEnd : lastArrive + 2000;
  const root = b.add({
    name: "train.step",
    start: t0,
    ms: total,
    rank: 0,
    attrs: { "ml.step": String(step), "ml.world_size": String(a.worldSize), "ml.tokens": String(run.tokensPerStep), "ml.straggling": String(straggling), "ml.mesh": "dp=8,tp=8" },
    ...(o.mode === "hang" ? { error: "NCCL watchdog timeout (SeqNum=88213)" } : o.mode === "xid" ? { error: "CUDA error on rank 21 (Xid 79)" } : {}),
  });
  const seq = seqAt(step);
  for (const r of o.ranks) {
    const isLate = r === o.late;
    const coords = { "ml.rank": String(r), "ml.local_rank": String(r % 8), "ml.dp": String(Math.floor(r / 8)), "ml.tp": String(r % 8) };
    const laneEnd = o.mode === "hang" ? (isLate ? HANG_MS : allreduceEnd) : o.mode === "xid" && isLate ? arrive(r) * 0.6 : allreduceEnd + opt;
    const lane = b.add({ name: `train.step [rank ${r}]`, parent: root, start: t0, ms: laneEnd, rank: r, attrs: { ...coords, "ml.step": String(step) }, ...(isLate && o.mode === "xid" ? { error: "CUDA error: unspecified launch failure" } : {}) });
    b.add({ name: "data.fetch", parent: lane, start: t0, ms: data * (1 + 0.05 * jit(`df${r}`, step)), rank: r, attrs: { "data.batch_tokens": String(run.tokensPerStep / a.worldSize) } });
    const fs = t0 + data;
    const fwdId = b.add({ name: "forward", parent: lane, start: fs, ms: fwd(r), rank: r, attrs: { ...coords, ...(isLate && straggling ? { "gpu.sm_clock_mhz": "1410", "gpu.clock_event_reasons": "HW_SLOWDOWN" } : {}) } });
    let cursor = fs;
    const parts = [0.22, 0.22, 0.22, 0.22, 0.12];
    ["TransformerBlock[0:8].forward", "TransformerBlock[8:16].forward", "TransformerBlock[16:24].forward", "TransformerBlock[24:32].forward", "lm_head + cross_entropy"].forEach((name, i) => {
      const ms = fwd(r) * parts[i];
      b.add({ name, parent: fwdId, start: cursor, ms, rank: r, attrs: { "nn.module": name.split(".")[0] } });
      if (i < 2) b.add({ name: "nccl:all_gather", parent: fwdId, start: cursor, ms: ms * 0.35, rank: r, kind: "client", attrs: { "nccl.op": "AllGather", "nccl.pg": "dp_shard", "nccl.bucket": String(i), "nccl.bytes": String(1_004_000_000), "nccl.exposed_ms": "0" } });
      cursor += ms;
    });
    const bs = fs + fwd(r);
    const bwdMs = lost === r ? bwd(r) * 0.3 : o.mode === "hang" && isLate ? HANG_MS - (bs - t0) : bwd(r);
    const bwdId = b.add({
      name: "backward",
      parent: lane,
      start: bs,
      ms: bwdMs,
      rank: r,
      attrs: coords,
      ...(lost === r ? { error: "CUDA error: unspecified launch failure" } : o.mode === "hang" && isLate ? { error: "never completed: process aborted by the watchdog" } : {}),
    });
    cursor = bs;
    ["TransformerBlock[24:32].backward", "TransformerBlock[16:24].backward", "TransformerBlock[8:16].backward", "TransformerBlock[0:8].backward"].forEach((name, i) => {
      const ms = (o.mode === "hang" && isLate ? bwd(r) : bwdMs) * 0.25;
      if (lost === r && i > 0) return;
      b.add({ name, parent: bwdId, start: cursor, ms, rank: r, attrs: { "nn.module": name.split(".")[0] } });
      b.add({ name: "nccl:reduce_scatter", parent: bwdId, start: cursor + ms * 0.5, ms: ms * 0.45, rank: r, kind: "client", attrs: { "nccl.op": "ReduceScatter", "nccl.pg": "dp_shard", "nccl.bucket": String(i), "nccl.bytes": String(2_008_000_000), "nccl.seq": String(seq - 4 + i) } });
      cursor += ms;
    });
    if (lost !== undefined && lost !== r) {
      b.add({ name: "nccl:reduce_scatter", parent: bwdId, start: bs + bwd(r) * 0.5, ms: 1200, rank: r, kind: "client", error: `NCCL error: remote process (rank ${lost}) exited`, attrs: { "nccl.op": "ReduceScatter", "nccl.seq": String(seq - 2) } });
      continue;
    }
    if (lost === r) continue;
    if (o.mode === "hang" && isLate) continue;
    const arStart = t0 + arrive(r);
    const arMs = allreduceEnd - arrive(r);
    b.add({
      name: "nccl:all_reduce",
      parent: lane,
      start: arStart,
      ms: arMs,
      rank: r,
      kind: "client",
      attrs: { "nccl.op": "AllReduce", "nccl.pg": "default", "nccl.algo": "Ring", "nccl.proto": "LL", "nccl.bytes": "4", "nccl.seq": String(seq), "nccl.wait_ms": (arMs - base * 0.012).toFixed(0), "ml.purpose": "clip_grad_norm_" },
      ...(o.mode === "hang" ? { error: "Watchdog caught collective operation timeout: WorkNCCL(SeqNum=88213, OpType=ALLREDUCE, Timeout(ms)=600000)" } : {}),
    });
    if (o.mode !== "hang") b.add({ name: "optimizer.step", parent: lane, start: t0 + allreduceEnd, ms: opt, rank: r, attrs: { "optim.kind": "AdamW (fused)", "optim.lr": lrOf({ run, a, step, straggling, rank: r, res: 1 }).toExponential(2) } });
  }

  // The same moment from DCGM, node-problem-detector and the kubelet, joined by node, GPU and pod.
  if (o.context) {
    const hot = o.late ?? STRAGGLER_RANK;
    b.add({
      name: "dcgm.sample",
      parent: root,
      start: t0 + 2000,
      ms: 1,
      rank: hot,
      service: "dcgm-exporter",
      attrs: {
        DCGM_FI_DEV_CLOCK_EVENT_REASONS: straggling ? "0x8 (HW_SLOWDOWN) | 0x40 (HW_THERMAL_SLOWDOWN)" : "0x0",
        DCGM_FI_DEV_GPU_TEMP: straggling ? "87" : "66",
        DCGM_FI_DEV_MEMORY_TEMP: straggling ? "95" : "72",
        DCGM_FI_DEV_POWER_USAGE: straggling ? "455" : "610",
        DCGM_FI_DEV_SM_CLOCK: straggling ? "1410" : "1980",
        DCGM_FI_PROF_SM_ACTIVE: straggling ? "0.93" : "0.78",
        DCGM_FI_PROF_PIPE_TENSOR_ACTIVE: straggling ? "0.41" : "0.47",
      },
    });
    const peer = o.ranks.find((r) => r !== hot && Math.floor(r / 8) === Math.floor(hot / 8)) ?? 0;
    b.add({ name: "dcgm.sample", parent: root, start: t0 + 2000, ms: 1, rank: peer, service: "dcgm-exporter", attrs: { DCGM_FI_DEV_CLOCK_EVENT_REASONS: "0x0", DCGM_FI_DEV_GPU_TEMP: "65", DCGM_FI_DEV_MEMORY_TEMP: "71", DCGM_FI_DEV_POWER_USAGE: "540", DCGM_FI_DEV_SM_CLOCK: "1980", DCGM_FI_PROF_SM_ACTIVE: "0.66" } });
    if (straggling) {
      b.add({ name: "node_problem_detector.check", parent: root, start: t0 + 500, ms: 3, rank: hot, service: "node-problem-detector", attrs: { condition: "GPUThermalSlowdown", status: "True", reason: "HWThermalSlowdown", message: "GPU 5 clock_event_reasons=HW_THERMAL_SLOWDOWN for 11h52m" } });
    }
    b.add({ name: "kubelet.pod_stats", parent: root, start: t0 + 1000, ms: 2, rank: hot, service: "kubelet", attrs: { "container.cpu.throttled_periods": "0", "container.memory.working_set_gb": "212.4", "pod.qos": "Guaranteed", "pod.phase": "Running" } });
  }
  if (o.mode === "xid" && lost !== undefined) {
    b.add({ name: "dcgm.xid", parent: root, start: t0 + bwd(lost) * 0.3, ms: 1, rank: lost, service: "dcgm-exporter", error: "Xid 79: GPU has fallen off the bus", attrs: { DCGM_FI_DEV_XID_ERRORS: "79", "pci.bus_id": "0000:9a:00.0" } });
  }

  const dominant = o.mode === "hang" ? "nccl:all_reduce" : o.late !== undefined && straggling ? "nccl:all_reduce (waiting for rank 37)" : "backward";
  const median = base;
  return {
    summary: {
      traceId: o.traceId,
      kind: MlTraceKind.TRAIN_STEP,
      title: o.title,
      start: timestampFromMs(Math.round(t0)),
      durationMs: total,
      vsMedian: total / median,
      dominantSpan: dominant,
      subject: attemptSubject(run, a),
      step: BigInt(step),
      rank: o.late ?? 0,
      error: o.mode === "hang" || o.mode === "xid",
      attributes: { ranks: o.ranks.join(","), "ml.step": String(step) },
    },
    spans: b.spans,
  };
}

/** One checkpoint save: the blocking stage (D2H into pinned memory), then the async shard writes, the upload and the commit. */
function checkpointTrace(traceId: string, run: TRun, a: TAttempt, step: number, now: () => number): BuiltTrace {
  const b = new SpanBuilder(traceId, run, a);
  const t0 = timeOfStep(a, step, now) + 300;
  const total = 212_000_000_000;
  const shards = a.nodes.length;
  const shardBytes = total / shards;
  const r = rng(hash(`ckpt-${a.phase}-${step}`));
  const save = (38 + 6 * r()) * 1000;
  const upload = (132 + 20 * r()) * 1000;
  const blocking = (run.blockingSeconds + 2 * (r() - 0.5)) * 1000;
  const commit = 900;
  const uri = `gs://acme-ml-checkpoints/${run.name}/${a.phase}/step-${String(step).padStart(6, "0")}/`;
  const root = b.add({ name: "checkpoint.save", start: t0, ms: save + upload + commit, rank: 0, attrs: { "ml.step": String(step), "checkpoint.kind": "sharded-dcp", "checkpoint.bytes": String(total), "checkpoint.blocking_ms": String(Math.round(blocking)), "checkpoint.async": "true", "checkpoint.uri": uri } });
  b.add({ name: "checkpoint.stage (D2H, blocking)", parent: root, start: t0, ms: blocking, rank: 0, attrs: { bytes: String(Math.round(shardBytes / 8)), pinned_memory: "true", "blocks_training": "true" } });
  b.add({ name: "checkpoint.serialize", parent: root, start: t0 + blocking, ms: 2100, rank: 0, attrs: { format: "torch.distributed.checkpoint", planner: "DefaultSavePlanner", thread: "async checkpointer" } });
  a.nodes.forEach((node, i) => {
    const ms = save - blocking - 2100 - 1500 * Math.abs(jit(`w${i}`, step)) - (node === HOT_NODE ? 0 : 900);
    b.add({ name: `checkpoint.write_shard [${i}]`, parent: root, start: t0 + blocking + 2100, ms, rank: i * a.gpusPerNode, kind: "client", attrs: { bytes: String(Math.round(shardBytes)), throughput_gbps: ((shardBytes / 1e9 / (ms / 1000)) * 8).toFixed(1), path: `/ckpt-local/step-${step}/__${i}_0.distcp` } });
  });
  b.add({ name: "checkpoint.upload", parent: root, start: t0 + save, ms: upload, rank: 0, kind: "client", attrs: { bytes: String(total), throughput_gbps: ((total / 1e9 / (upload / 1000)) * 8).toFixed(1), destination: uri } });
  b.add({ name: "checkpoint.commit", parent: root, start: t0 + save + upload, ms: commit, rank: 0, attrs: { writes: ".metadata", status: "DURABLE" } });
  const dur = save + upload + commit;
  return {
    summary: { traceId, kind: MlTraceKind.CHECKPOINT, title: `Checkpoint save at step ${fmtN(step)}`, start: timestampFromMs(Math.round(t0)), durationMs: dur, vsMedian: dur / 183_000, dominantSpan: "checkpoint.upload", subject: attemptSubject(run, a), step: BigInt(step), rank: 0, attributes: { ranks: a.nodes.map((_, i) => i * a.gpusPerNode).join(","), blocking_ms: String(Math.round(blocking)) } },
    spans: b.spans,
  };
}

function dataPipelineTrace(run: TRun, a: TAttempt, step: number, now: () => number): BuiltTrace {
  const traceId = TRACES.dataPipeline;
  const b = new SpanBuilder(traceId, run, a);
  const t0 = timeOfStep(a, step, now) - 60;
  const workers = [0, 1, 2, 3].map((w) => {
    const fetch = 14 + 9 * Math.abs(jit(`f${w}`, step)) + (w === 2 ? 70 : 0);
    return { w, fetch, decode: 4.2, tokenize: 17.5 + jit(`t${w}`, step), pack: 3.1 };
  });
  const longest = Math.max(...workers.map((w) => w.fetch + w.decode + w.tokenize + w.pack));
  const collate = 2.4;
  const pin = 3.8;
  const h2d = 5.6;
  const root = b.add({ name: "data.next_batch", start: t0, ms: longest + collate + pin + h2d, rank: 0, attrs: { "ml.step": String(step), "data.dataset": "support-sft-v3", "data.workers": "4", "data.prefetch_factor": "2" } });
  for (const w of workers) {
    const wid = b.add({ name: `dataloader.worker[${w.w}]`, parent: root, start: t0, ms: w.fetch + w.decode + w.tokenize + w.pack, rank: 0, attrs: { "worker.pid": String(4100 + w.w), shard: `support-sft-v3/shard-0${1300 + w.w * 7}` } });
    let c = t0;
    b.add({ name: "fetch", parent: wid, start: c, ms: w.fetch, rank: 0, kind: "client", attrs: { source: "gs://acme-ml-data/support-sft-v3", bytes: String(18_400_000 + w.w * 120_000), ...(w.w === 2 ? { "gcs.retry": "1" } : {}) } });
    c += w.fetch;
    b.add({ name: "decode", parent: wid, start: c, ms: w.decode, rank: 0, attrs: { format: "arrow + zstd" } });
    c += w.decode;
    b.add({ name: "tokenize", parent: wid, start: c, ms: w.tokenize, rank: 0, attrs: { tokenizer: "llama3 (tiktoken)", samples: "64" } });
    c += w.tokenize;
    b.add({ name: "pack", parent: wid, start: c, ms: w.pack, rank: 0, attrs: { seq_len: "4096", padding_ratio: "0.031" } });
  }
  b.add({ name: "collate", parent: root, start: t0 + longest, ms: collate, rank: 0 });
  b.add({ name: "pin_memory", parent: root, start: t0 + longest + collate, ms: pin, rank: 0 });
  b.add({ name: "h2d_copy", parent: root, start: t0 + longest + collate + pin, ms: h2d, rank: 0, attrs: { bytes: String(run.tokensPerStep / a.worldSize * 8) } });
  const total = longest + collate + pin + h2d;
  return {
    summary: { traceId, kind: MlTraceKind.DATA_PIPELINE, title: `Data pipeline for step ${fmtN(step)} (4 workers)`, start: timestampFromMs(Math.round(t0)), durationMs: total, vsMedian: total / 46, dominantSpan: "dataloader.worker[2] fetch (GCS retry)", subject: attemptSubject(run, a), step: BigInt(step), rank: 0, attributes: { ranks: "0" } },
    spans: b.spans,
  };
}

/**
 * sft attempt 3's start, stage by stage as training.ts V3_RECOVERIES["sft/3"]
 * declares it: detect (Xid 79 → alert) → drain → Kueue requeue and admission
 * with the hot spare bound → pod start (image pre-pulled) → NCCL init →
 * checkpoint load → first step.
 */
function podStartTrace(run: TRun, a: TAttempt, now: () => number): BuiltTrace {
  const traceId = TRACES.podStart;
  const b = new SpanBuilder(traceId, run, a);
  const S = 1000;
  const prevEnd = msAt(v3Attempt("sft", 2).endedAt ?? 0, now);
  const t0 = prevEnd - 41 * S;
  const running = msAt(a.runningAt, now);
  const wl = workloadName(run.name, a.phase, a.n);
  const total = running + 47 * S - t0;
  const root = b.add({ name: "attempt.start", start: t0, ms: total, service: "forge", attrs: { "ml.workload": wl, "ml.resumed_from_step": String(a.startStep), "ml.remediation": `cordoned ${BAD_NODE}, swapped in hot spare ${SPARE_NODE}`, "ml.alert": "alert-xid-fatal" } });
  b.add({ name: "detect: dcgm Xid 79 → alert-xid-fatal", parent: root, start: t0, ms: 41 * S, service: "forge", node: BAD_NODE, device: gpuUuid(BAD_NODE, BAD_GPU_INDEX), attrs: { "dcgm.xid": "79", "alert.rule": "alert-xid-fatal", action: "cordon + new attempt" } });
  let c = prevEnd;
  b.add({ name: "pods.drain (attempt 2)", parent: root, start: c, ms: 95 * S, service: "kubelet", node: BAD_NODE, pod: `${workloadName(run.name, "sft", 2)}-w-${H100.indexOf(BAD_NODE)}`, attrs: { "node.cordoned": BAD_NODE, reason: "healthy pods exit without an emergency checkpoint; r4vb's pod force-deleted at once (the GPU fell off the bus)" } });
  c += 95 * S;
  const kq = b.add({
    name: "kueue.workload",
    parent: root,
    start: c,
    ms: 38 * S,
    service: "kueue",
    attrs: { "kueue.workload": wl, "kueue.local_queue": "ml-research/train", "kueue.cluster_queue": "team-research", "kueue.cohort": "ml-h100", "kueue.flavor": "h100", "kueue.gpus": "64", "kueue.admission_check": `hot-spare: ${SPARE_NODE} bound for ${BAD_NODE}` },
  });
  b.add({ name: "kueue.quota_reserved", parent: kq, start: c, ms: 29 * S, service: "kueue" });
  b.add({ name: "kueue.admission", parent: kq, start: c + 29 * S, ms: 9 * S, service: "kueue", attrs: { admission_checks: "hot-spare: Ready" } });
  c += 38 * S;
  const podStart = c;
  a.nodes.forEach((node, i) => {
    const pod = `${wl}-w-${i}`;
    const sched = 0.8 * S + i * 90;
    b.add({ name: "kube-scheduler.bind", parent: root, start: podStart, ms: sched, service: "kube-scheduler", node, pod, attrs: { pod, node, ...(node === SPARE_NODE ? { note: "hot spare (taint ml.reliant.dev/spare tolerated)" } : {}) } });
    const ps = b.add({ name: "kubelet.pod_start", parent: root, start: podStart + sched, ms: 7 * S, service: "kubelet", node, pod, attrs: { "gpu.uuids": Array.from({ length: 8 }, (_, d) => gpuUuid(node, d)).join(","), "pod.qos": "Guaranteed" } });
    b.add({ name: "kubelet.image_pull", parent: ps, start: podStart + sched, ms: 400, service: "kubelet", node, pod, attrs: { image: "us-docker.pkg.dev/acme-ml/train/torchtitan@sha256:7c1e…", bytes: "18400000000", cached: "true", ...(node === SPARE_NODE ? { note: "pre-pulled on the hot spare" } : {}) } });
    b.add({ name: "kubelet.container_start", parent: ps, start: podStart + sched + 400, ms: 6.6 * S, service: "kubelet", node, pod, attrs: { container: "trainer", init_containers: "nccl-env, fr-volume" } });
  });
  c = podStart + 8 * S;
  b.add({ name: "nccl.init", parent: root, start: c, ms: 56 * S, rank: 0, kind: "client", attrs: { "nccl.version": "2.21.5", "nccl.nranks": "64", "nccl.net": "GPUDirect-TCPX, new ring through s01" } });
  c += 56 * S;
  b.add({ name: "checkpoint.load", parent: root, start: c, ms: running - c, rank: 0, kind: "client", attrs: { uri: `gs://acme-ml-checkpoints/${run.name}/sft/step-${String(a.startStep).padStart(6, "0")}/`, bytes: "212000000000", throughput_gbps: (0.67 * 8).toFixed(1) } });
  b.add({ name: "train.first_step", parent: root, start: running, ms: 47 * S, rank: 0, attrs: { "ml.step": String(a.startStep + 1), note: "recompile + first optimizer step" } });
  return {
    summary: { traceId, kind: MlTraceKind.POD_START, title: `sft attempt ${a.n} start: Xid 79 detected → first step`, start: timestampFromMs(Math.round(t0)), durationMs: total, vsMedian: total / (11 * 60 * S), dominantSpan: "pods.drain (stuck Terminating)", subject: attemptSubject(run, a), step: BigInt(a.startStep + 1), rank: -1, attributes: { ranks: "0" } },
    spans: b.spans,
  };
}

const V3 = TRUNS[0];
const v3Attempt = (phase: string, n: number) => V3.attempts.find((a) => a.phase === phase && a.n === n)!;

/** The slow steps of sft attempt 3 the tracer kept (every step over p99, and rank lanes 0, 36, 37, 38). */
const SLOW_STEPS = [41_187, 41_163, 41_102, 40_977, 40_615, 40_208, 39_904, 39_388, 38_155, 37_433] as const;

/** Every trace this file answers, by id, built on demand. */
function traceRegistry(): Map<string, { kind: MlTraceKind; attempt: TAttempt; ranks: readonly number[]; build: (now: () => number) => BuiltTrace }> {
  const a1 = v3Attempt("sft", 1);
  const a2 = v3Attempt("sft", 2);
  const a3 = v3Attempt("sft", 3);
  const m = new Map<string, { kind: MlTraceKind; attempt: TAttempt; ranks: readonly number[]; build: (now: () => number) => BuiltTrace }>();
  SLOW_STEPS.forEach((step, i) => {
    const id = i === 0 ? TRACES.slowStep : hexId(`v3-sft-a3-slow-${step}`);
    const ranks = [0, 36, 37, 38];
    m.set(id, { kind: MlTraceKind.TRAIN_STEP, attempt: a3, ranks, build: (now) => stepTrace({ traceId: id, run: V3, a: a3, step, ranks, late: STRAGGLER_RANK, lateFactor: 1.244 + 0.01 * jit("lf", step), context: true, title: `Step ${fmtN(step)}: rank 37 late at all_reduce` }, now) });
  });
  m.set(TRACES.normalStep, { kind: MlTraceKind.TRAIN_STEP, attempt: a3, ranks: [0, 37], build: (now) => stepTrace({ traceId: TRACES.normalStep, run: V3, a: a3, step: 36_412, ranks: [0, 37], context: true, title: "Step 36,412 (before the straggler)" }, now) });
  m.set(TRACES.checkpoint, { kind: MlTraceKind.CHECKPOINT, attempt: a3, ranks: [0, 8, 16, 24, 32, 40, 48, 56], build: (now) => checkpointTrace(TRACES.checkpoint, V3, a3, 41_000, now) });
  m.set(CHECKPOINT_40K, { kind: MlTraceKind.CHECKPOINT, attempt: a3, ranks: [0, 8, 16, 24, 32, 40, 48, 56], build: (now) => checkpointTrace(CHECKPOINT_40K, V3, a3, 40_000, now) });
  m.set(TRACES.dataPipeline, { kind: MlTraceKind.DATA_PIPELINE, attempt: a3, ranks: [0], build: (now) => dataPipelineTrace(V3, a3, 41_150, now) });
  m.set(TRACES.podStart, { kind: MlTraceKind.POD_START, attempt: a3, ranks: [0], build: (now) => podStartTrace(V3, a3, now) });
  m.set(HANG_TRACE, { kind: MlTraceKind.TRAIN_STEP, attempt: a1, ranks: [0, 36, 37, 38], build: (now) => stepTrace({ traceId: HANG_TRACE, run: V3, a: a1, step: 12_400, ranks: [0, 36, 37, 38], late: STRAGGLER_RANK, lateFactor: 1, mode: "hang", title: "Step 12,400: allreduce seq 88213 never completed (rank 37 missing)" }, now) });
  const badRank = H100.indexOf(BAD_NODE) * 8 + BAD_GPU_INDEX;
  m.set(XID_TRACE, { kind: MlTraceKind.TRAIN_STEP, attempt: a2, ranks: [0, badRank - 1, badRank, badRank + 1], build: (now) => stepTrace({ traceId: XID_TRACE, run: V3, a: a2, step: 32_750, ranks: [0, badRank - 1, badRank, badRank + 1], late: badRank, lateFactor: 1, mode: "xid", title: `Step 32,750: Xid 79 on ${BAD_NODE} GPU 5 (rank ${badRank})` }, now) });
  return m;
}

let registry: ReturnType<typeof traceRegistry> | null = null;
const traces = () => (registry ??= traceRegistry());

function listTraces(scope: Scope, kind: MlTraceKind | undefined, limit: number | undefined, now: () => number) {
  if (!scope.run.instrumented) return { traces: [], missingFindingId: FINDINGS.trainingUntracked };
  const inScope = [...traces().entries()].filter(([, t]) => scope.attempts.includes(t.attempt) && (!kind || t.kind === kind) && (scope.rank === null || t.ranks.includes(scope.rank)));
  const summaries = inScope.map(([, t]) => t.build(now).summary).sort((x, y) => (y.durationMs ?? 0) - (x.durationMs ?? 0));
  return { traces: summaries.slice(0, limit && limit > 0 ? limit : 100), missingFindingId: "" };
}

// ── Profiles ──────────────────────────────────────────────────────────────────

interface Frame {
  name: string;
  file?: string;
  self?: number;
  children?: Frame[];
}

/** A speedscope "sampled" profile from a call tree: one sample per node with self weight. */
export function speedscope(name: string, unit: string, roots: readonly Frame[]): string {
  const frames: { name: string; file?: string }[] = [];
  const index = new Map<string, number>();
  const samples: number[][] = [];
  const weights: number[] = [];
  const frameOf = (f: Frame) => {
    const k = `${f.name}|${f.file ?? ""}`;
    if (!index.has(k)) {
      index.set(k, frames.length);
      frames.push(f.file ? { name: f.name, file: f.file } : { name: f.name });
    }
    return index.get(k)!;
  };
  const walk = (f: Frame, stack: number[]) => {
    const s = [...stack, frameOf(f)];
    if (f.self && f.self > 0) {
      samples.push(s);
      weights.push(Math.round(f.self));
    }
    for (const c of f.children ?? []) walk(c, s);
  };
  roots.forEach((r) => walk(r, []));
  const total = weights.reduce((s, w) => s + w, 0);
  return JSON.stringify({
    $schema: "https://www.speedscope.app/file-format-schema.json",
    shared: { frames },
    profiles: [{ type: "sampled", name, unit, startValue: 0, endValue: total, samples, weights }],
    name,
    activeProfileIndex: 0,
    exporter: "reliant forge ml (demo)",
  });
}

const TITAN = "torchtitan/train.py";

/** The training loop on rank 37 over steps 40,800–40,810 (ms of wall time, Python + native frames). */
function kinetoFlamegraph(): string {
  return speedscope("rank 37 · steps 40,800–40,810", "milliseconds", [
    {
      name: "main",
      file: TITAN,
      children: [
        {
          name: "train_step",
          file: TITAN,
          self: 410,
          children: [
            {
              name: "Transformer.forward",
              file: "torchtitan/models/llama/model.py",
              self: 220,
              children: [
                {
                  name: "TransformerBlock.forward",
                  file: "torchtitan/models/llama/model.py",
                  self: 180,
                  children: [
                    { name: "Attention.forward", file: "torchtitan/models/llama/model.py", self: 160, children: [{ name: "flash_attn_func", file: "flash_attn/flash_attn_interface.py", children: [{ name: "flash_fwd_kernel (native)", self: 7_900 }] }, { name: "aten::mm → sm90_xmma_gemm_bf16bf16_bf16f32", self: 9_300 }, { name: "apply_rotary_emb", self: 610 }] },
                    { name: "FeedForward.forward", file: "torchtitan/models/llama/model.py", self: 120, children: [{ name: "aten::mm → sm90_xmma_gemm_bf16bf16_bf16f32", self: 13_100 }, { name: "aten::silu_ / aten::mul (elementwise)", self: 1_400 }] },
                    { name: "RMSNorm.forward", self: 900 },
                    { name: "c10d::all_gather_into_tensor (FSDP)", self: 1_100 },
                  ],
                },
                { name: "cross_entropy_loss", file: "torchtitan/components/loss.py", self: 1_250 },
              ],
            },
            {
              name: "torch.autograd.backward",
              file: "torch/autograd/__init__.py",
              children: [
                {
                  name: "autograd::engine::evaluate_function",
                  self: 300,
                  children: [
                    { name: "FlashAttnFuncBackward", children: [{ name: "flash_bwd_kernel (native)", self: 17_600 }] },
                    { name: "MmBackward0 → sm90_xmma_gemm_bf16bf16_bf16f32", self: 40_200 },
                    { name: "CheckpointFunctionBackward (recompute)", self: 9_800 },
                    { name: "c10d::reduce_scatter_tensor (FSDP)", self: 2_300 },
                  ],
                },
              ],
            },
            { name: "clip_grad_norm_", file: "torch/nn/utils/clip_grad.py", children: [{ name: "c10d::allreduce_ → ncclDevKernel_AllReduce_RING_LL", self: 1_150 }] },
            { name: "AdamW.step", file: "torch/optim/adamw.py", children: [{ name: "_fused_adamw → multi_tensor_apply_kernel", self: 3_600 }] },
            { name: "_MultiProcessingDataLoaderIter.__next__", file: "torch/utils/data/dataloader.py", children: [{ name: "_next_data → pin_memory", self: 1_380 }] },
          ],
        },
      ],
    },
  ]);
}

function cpuFlamegraph(): string {
  return speedscope("sft attempt 3 · CPU (eBPF, all ranks)", "milliseconds", [
    { name: "MainThread", children: [{ name: "train_step", file: TITAN, self: 1_900, children: [{ name: "cudaLaunchKernel", self: 6_400 }, { name: "cudaStreamSynchronize (waiting for the GPU)", self: 21_000 }, { name: "torch._C._autograd backward (dispatcher)", self: 3_100 }, { name: "PyEval_EvalFrame (python overhead)", self: 2_200 }] }] },
    { name: "pt_autograd_0", children: [{ name: "autograd::engine::thread_main", self: 4_900 }] },
    {
      name: "DataLoader worker ×8",
      children: [
        { name: "tokenizers::encode_batch (tiktoken)", self: 9_800 },
        { name: "pyarrow::ReadRecordBatch", self: 3_300, children: [{ name: "ZSTD_decompressStream", self: 2_700 }] },
        { name: "pack_sequences", file: "support_data/packing.py", self: 1_800 },
        { name: "gcsfs read (ssl_read)", self: 1_500 },
      ],
    },
    { name: "pt_pin_memory_thread", children: [{ name: "cudaHostAlloc / memcpy", self: 2_100 }] },
    { name: "NCCL proxy thread", children: [{ name: "ncclProxyProgress (ibv_poll_cq)", self: 7_600 }] },
    { name: "python GC", self: 640 },
  ]);
}

function gpuFlamegraph(): string {
  return speedscope("sft attempt 3 · GPU kernel time (CUPTI sampling, all ranks)", "milliseconds", [
    {
      name: "GPU time",
      children: [
        { name: "gemm", children: [{ name: "sm90_xmma_gemm_bf16bf16_bf16f32_f32_tn_n_tilesize128x256x64_cutlass", self: 41_000 }, { name: "sm90_xmma_gemm_bf16bf16_bf16f32_f32_nt_n_tilesize128x128x64_cutlass", self: 12_500 }] },
        { name: "attention", children: [{ name: "flash_bwd_dq_dk_dv_loop_seqk_parallel_kernel", self: 11_800 }, { name: "flash_fwd_kernel", self: 6_200 }] },
        { name: "nccl", children: [{ name: "ncclDevKernel_AllReduce_RING_LL", self: 9_400 }, { name: "ncclDevKernel_ReduceScatter_RING_LL", self: 4_100 }, { name: "ncclDevKernel_AllGather_RING_LL", self: 3_200 }] },
        { name: "elementwise", children: [{ name: "vectorized_elementwise_kernel (silu, mul, add)", self: 4_300 }, { name: "rms_norm_kernel", self: 1_700 }] },
        { name: "optimizer", children: [{ name: "multi_tensor_apply_kernel (fused AdamW)", self: 2_600 }] },
        { name: "memcpy", children: [{ name: "Memcpy HtoD (pinned)", self: 900 }, { name: "Memcpy DtoH (checkpoint stage)", self: 600 }] },
        { name: "idle", self: 7_100 },
      ],
    },
  ]);
}

function memoryFlamegraph(): string {
  const GB = 1024 ** 3;
  return speedscope("rank 0 · peak allocation at step 40,805", "bytes", [
    {
      name: "CUDA caching allocator (peak 71.3 GB)",
      children: [
        { name: "parameters (bf16, FSDP shard + TP)", self: 2.1 * GB },
        { name: "gradients (bf16)", self: 2.1 * GB },
        { name: "optimizer state", children: [{ name: "fp32 master weights", self: 4.2 * GB }, { name: "AdamW exp_avg", self: 4.2 * GB }, { name: "AdamW exp_avg_sq", self: 4.2 * GB }] },
        { name: "FSDP all-gather buffers (unsharded params)", self: 8.4 * GB },
        { name: "activations saved for backward", children: [{ name: "TransformerBlock[0:32] (selective checkpointing)", self: 27.6 * GB }, { name: "lm_head logits (128k vocab)", self: 4.1 * GB }] },
        { name: "flash-attn workspace", self: 1.3 * GB },
        { name: "NCCL buffers", self: 1.9 * GB },
        { name: "reserved, not allocated (fragmentation)", self: 11.2 * GB },
      ],
    },
  ]);
}

const KERNELS = [
  { name: "sm90_xmma_gemm_bf16bf16_bf16f32 (cutlass)", share: 0.412, calls: BigInt(186_240), meanUs: 241, category: "gemm" },
  { name: "flash_bwd_dq_dk_dv_loop_seqk_parallel_kernel", share: 0.131, calls: BigInt(10_240), meanUs: 1_402, category: "attention" },
  { name: "ncclDevKernel_AllReduce_RING_LL", share: 0.094, calls: BigInt(2_860), meanUs: 3_610, category: "nccl" },
  { name: "flash_fwd_kernel", share: 0.068, calls: BigInt(10_240), meanUs: 728, category: "attention" },
  { name: "ncclDevKernel_ReduceScatter_RING_LL", share: 0.041, calls: BigInt(5_120), meanUs: 877, category: "nccl" },
  { name: "multi_tensor_apply_kernel (fused AdamW)", share: 0.026, calls: BigInt(640), meanUs: 4_470, category: "elementwise" },
  { name: "vectorized_elementwise_kernel (silu, mul, add)", share: 0.043, calls: BigInt(288_000), meanUs: 16, category: "elementwise" },
  { name: "Memcpy HtoD (pinned)", share: 0.009, calls: BigInt(1_280), meanUs: 77, category: "memcpy" },
];

function collectives(): CollectiveInit[] {
  const a1 = v3Attempt("sft", 1);
  return Array.from({ length: a1.worldSize }, (_, r) => {
    const late = r === STRAGGLER_RANK;
    return { rank: r, lastSeq: BigInt(late ? 88_212 : 88_213), op: "ALLREDUCE", sizeBytes: BigInt(4), state: late ? "completed" : "started", mismatched: late, pod: rankPod(V3.name, "sft", 1, r) };
  });
}

function profileList(now: () => number, withBody: boolean): ProfileInit[] {
  const a1 = v3Attempt("sft", 1);
  const a3 = v3Attempt("sft", 3);
  const at3 = (step: number) => timestampFromMs(Math.round(timeOfStep(a3, step, now)));
  const PERFETTO = "https://ui.perfetto.dev/#!/?url=";
  const bucket = `https://storage.googleapis.com/acme-ml-profiles/${V3.name}/sft/a3`;
  return [
    {
      id: PROFILES.kineto,
      kind: MlProfileKind.PYTORCH_PROFILER,
      trigger: "declared window",
      subject: attemptSubject(V3, a3),
      fromStep: 40_800n,
      toStep: 40_810n,
      ranks: [0, STRAGGLER_RANK],
      sizeBytes: BigInt(1_930_000_000),
      capturedAt: at3(40_810),
      openUrl: `${PERFETTO}${encodeURIComponent(`${bucket}/kineto-40800-rank37.pt.trace.json.gz`)}`,
      openLabel: "Open in Perfetto",
      capturedBy: "maya (ml.profiling.window in the run's KCL)",
      summary: {
        computeShare: 0.71,
        commShare: 0.19,
        memcpyShare: 0.02,
        idleShare: 0.08,
        commOverlap: 0.41,
        launchBound: false,
        topKernels: KERNELS,
        peakMemoryGb: 71.3,
        notes: [
          { label: "Exposed comm", value: "rank 0 waits ~1.8 s/step in ncclDevKernel_AllReduce for rank 37" },
          { label: "Rank 37 clocks", value: "SM 1,410 MHz of 1,980 (HW_SLOWDOWN): its GEMMs run 18% longer" },
          { label: "Overlap", value: "FSDP reduce-scatter overlaps backward (41%); the grad-norm all-reduce is fully exposed" },
          { label: "Launch gaps", value: "median 6 µs between kernels: not launch-bound" },
        ],
      },
      ...(withBody ? { flamegraphJson: kinetoFlamegraph() } : {}),
    },
    {
      id: PROFILES.nsight,
      kind: MlProfileKind.NSIGHT_SYSTEMS,
      trigger: "manual",
      subject: attemptSubject(V3, a3),
      fromStep: 41_000n,
      toStep: 41_005n,
      ranks: [STRAGGLER_RANK],
      sizeBytes: BigInt(3_620_000_000),
      capturedAt: at3(41_005),
      openUrl: `${bucket}/nsys-41000-rank37.nsys-rep`,
      openLabel: "Download .nsys-rep",
      capturedBy: "maya",
      summary: { computeShare: 0.74, commShare: 0.15, memcpyShare: 0.03, idleShare: 0.08, commOverlap: 0.44, launchBound: false, topKernels: KERNELS.slice(0, 4), peakMemoryGb: 71.1, notes: [{ label: "GPU metrics", value: "SM clock sampled at 10 kHz: 1,395–1,425 MHz throughout (thermal cap)" }] },
    },
    {
      id: PROFILES.memory,
      kind: MlProfileKind.MEMORY_SNAPSHOT,
      trigger: "declared window",
      subject: attemptSubject(V3, a3),
      fromStep: 40_805n,
      toStep: 40_805n,
      ranks: [0],
      sizeBytes: BigInt(214_000_000),
      capturedAt: at3(40_805),
      openUrl: "https://pytorch.org/memory_viz",
      openLabel: "Open in memory_viz",
      capturedBy: "maya",
      summary: {
        peakMemoryGb: 71.3,
        notes: [
          { label: "Peak", value: "71.3 GB of 80 GB at the start of backward (step 40,805)" },
          { label: "Largest", value: "activations saved for backward: 27.6 GB (selective checkpointing)" },
          { label: "Fragmentation", value: "11.2 GB reserved but not allocated; expandable_segments would reclaim most" },
          { label: "Headroom", value: "8.7 GB: enough for micro-batch 2 → 3 only with full activation checkpointing" },
        ],
      },
      ...(withBody ? { flamegraphJson: memoryFlamegraph() } : {}),
    },
    {
      id: PROFILES.cpu,
      kind: MlProfileKind.CPU_CONTINUOUS,
      trigger: "always on (eBPF)",
      subject: attemptSubject(V3, a3),
      fromStep: BigInt(a3.startStep),
      toStep: BigInt(a3.endStep),
      ranks: Array.from({ length: a3.worldSize }, (_, r) => r),
      sizeBytes: BigInt(48_000_000),
      capturedAt: timestampFromMs(now()),
      openLabel: "",
      capturedBy: "OTel eBPF profiler (DaemonSet)",
      summary: { notes: [{ label: "Main thread", value: "61% waiting in cudaStreamSynchronize: GPU-bound, as it should be" }, { label: "DataLoader", value: "tokenization is 14% of CPU across 8 workers; loader wait stays at 1.2% of step" }] },
      ...(withBody ? { flamegraphJson: cpuFlamegraph() } : {}),
    },
    {
      id: PROFILES.gpu,
      kind: MlProfileKind.GPU_CONTINUOUS,
      trigger: "always on (CUPTI range sampling)",
      subject: attemptSubject(V3, a3),
      fromStep: BigInt(a3.startStep),
      toStep: BigInt(a3.endStep),
      ranks: Array.from({ length: a3.worldSize }, (_, r) => r),
      sizeBytes: BigInt(12_000_000),
      capturedAt: timestampFromMs(now()),
      openLabel: "",
      capturedBy: "forge GPU profiler (1 Hz)",
      summary: { computeShare: 0.68, commShare: 0.2, memcpyShare: 0.01, idleShare: 0.11, commOverlap: 0.39, launchBound: false, topKernels: KERNELS, notes: [{ label: "Since the straggler", value: "AllReduce share 4% → 9% on every rank but 37" }] },
      ...(withBody ? { flamegraphJson: gpuFlamegraph() } : {}),
    },
    {
      id: PROFILES.flightRecorder,
      kind: MlProfileKind.FLIGHT_RECORDER,
      trigger: "watchdog",
      subject: attemptSubject(V3, a1),
      fromStep: 12_400n,
      toStep: 12_400n,
      ranks: Array.from({ length: a1.worldSize }, (_, r) => r),
      sizeBytes: BigInt(41_000_000),
      capturedAt: timestampFromMs(Math.round(attemptEndMs(a1, now))),
      openLabel: "",
      capturedBy: "TORCH_NCCL_DUMP_ON_TIMEOUT (collected by forge)",
      collectives: collectives(),
      summary: { notes: [{ label: "Analysis", value: "Not all ranks joined collective 88213 (ALLREDUCE): missing rank {37}" }, { label: "Rank 37", value: "last completed 88212; never enqueued 88213" }] },
    },
  ];
}

function listProfiles(scope: Scope, now: () => number): ProfileInit[] {
  if (!isV3(scope.run)) return [];
  return profileList(now, false).filter((p) => {
    const [, phase, n] = (p.subject?.id ?? "").split("/");
    if (phase !== scope.phase) return false;
    if (!scope.attempts.some((a) => a.n === Number(n))) return false;
    return scope.rank === null || (p.ranks ?? []).includes(scope.rank);
  });
}

// ── The answers ───────────────────────────────────────────────────────────────

export const trainingTelemetryAnswers: MlDomain = {
  queryMlSeries: (req, now) => {
    const series = querySeries(req, now);
    return series ? { series } : undefined;
  },
  queryMlHistograms: (req, now) => {
    void now;
    const scope = scopeOf(req.subject);
    return scope ? { histograms: queryHistograms(scope, req.keys) } : undefined;
  },
  listMlLogs: (req, now) => {
    const scope = scopeOf(req.subject);
    return scope ? listLogs(scope, req, now) : undefined;
  },
  listMlTraces: (req, now) => {
    const scope = scopeOf(req.subject);
    return scope ? listTraces(scope, req.kind, req.limit, now) : undefined;
  },
  getMlTrace: (req, now) => {
    const t = traces().get(req.traceId);
    if (!t) return undefined;
    const built = t.build(now);
    return { summary: built.summary, spans: built.spans, truncated: false };
  },
  listMlProfiles: (req, now) => {
    const scope = scopeOf(req.subject);
    return scope ? { profiles: listProfiles(scope, now) } : undefined;
  },
  getMlProfile: (req, now) => {
    const profile = profileList(now, true).find((p) => p.id === req.profileId);
    return profile ? { profile } : undefined;
  },
};

/** For tests and view models: the ids this file serves beyond world.ts's. */
export const TRAINING_TELEMETRY_IDS = { hangTrace: HANG_TRACE, xidTrace: XID_TRACE, checkpoint40k: CHECKPOINT_40K, slowSteps: SLOW_STEPS } as const;
