// Copyright (c) 2026 Reliant Labs

/**
 * The ML fixture world's Training: experiments, runs (phases, attempts,
 * recoveries, Kueue workloads, ranks, checkpoints), the run detail
 * (health, step breakdown, config, cost, insights, layer stats, tokens,
 * hooks, spares), and the events and alert rules of run subjects
 * (RUN, PHASE, ATTEMPT, RANK, CHECKPOINT, EXPERIMENT) plus Training's
 * notable ORG events and rules.
 *
 * Everything is computed in offsets from now (ms) and turned into Timestamps
 * at the edge, so every GPU-hour and step below is the same number on every
 * load. The hero run follows world.ts V3_ATTEMPTS exactly; the recovery
 * stages after each failure add up to the gap between the failed attempt's
 * end and the next attempt's first running moment
 * (__tests__ in components/Forge/Ml/Training hold this to it).
 *
 * Series, logs, traces and profiles of these subjects are
 * trainingTelemetry.ts's; device and node events are fleet.ts's (the Xid
 * and the thermal throttle appear here too, as the run saw them).
 */

import type { DescMessage, MessageInitShape } from "@bufbuild/protobuf";

import {
  MlAttemptEndReason,
  MlAxis,
  MlCheckpointStatus,
  MlCollection,
  MlCostCategory,
  MlEventKind,
  MlParamSource,
  MlPhaseKind,
  MlPhaseState,
  MlRunBadge,
  MlRunState,
  MlSeverity,
  MlSubjectKind,
  type MlAttemptSchema,
  type MlCheckpointSchema,
  type MlCostSchema,
  type MlEventSchema,
  type MlExperimentSchema,
  type MlHookSchema,
  type MlKueueWorkloadSchema,
  type MlPhaseSchema,
  type MlRankSchema,
  type MlRecoverySchema,
  type MlRunConfigSchema,
  type MlCostCellSchema,
  type MlLayerStatSchema,
  type MlRunSchema,
  type MlSubjectSchema,
  type MlTimeSpanSchema,
  type GetMlRunResponseSchema,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { evalScore, v3LossCurve, v3Mfu, v3StepTimeMs, v3TokensPerSecond, v3TrainLoss, v3ValLoss, V3_STRAGGLER_STEP, type EvalTask } from "./curves";
import type { MlDomain } from "./domain";
import { MlAlertState, type AlertRuleInit, type MlTelemetryDomain } from "./telemetryTypes";
import {
  ALERTS,
  at,
  attemptId,
  BAD_GPU_INDEX,
  BAD_NODE,
  BASE_MODEL,
  checkpointId,
  CLUSTERS,
  DATASETS,
  DAY,
  DPO_PENDING,
  EVAL_RUNS,
  EVAL_TASKS,
  EXPERIMENTS,
  FINDINGS,
  gpuUuid,
  hash,
  HOT_GPU_INDEX,
  HOT_NODE,
  HOUR,
  MINUTE,
  ML_PEOPLE,
  ML_PROJECT,
  nodeId,
  periodicEvalRunId,
  phaseId,
  poolOfNode,
  POOLS,
  PREEMPTOR_WORKLOAD,
  PROFILES,
  QUEUES,
  rankId,
  rankPod,
  rng,
  RUNS,
  SPARE_NODE,
  STRAGGLER_RANK,
  STRAGGLER_SINCE,
  SWEEP_ID,
  V21_FP8_CHECKPOINT,
  V2_FINAL_CHECKPOINT,
  V3_ATTEMPTS,
  V3_NOW,
  V3_PHASES,
  VERSIONS,
  workloadName,
  type AttemptDef,
  type AttemptEnd,
  type PoolDef,
} from "./world";

type Init<S extends DescMessage> = MessageInitShape<S>;
type RunInit = Init<typeof MlRunSchema>;
type PhaseInit = Init<typeof MlPhaseSchema>;
type AttemptInit = Init<typeof MlAttemptSchema>;
type CostInit = Init<typeof MlCostSchema>;
type CheckpointInit = Init<typeof MlCheckpointSchema>;
type EventInit = Exclude<Init<typeof MlEventSchema>, { $typeName: string }>;
type SubjectInit = Init<typeof MlSubjectSchema>;
type DetailInit = Init<typeof GetMlRunResponseSchema>;
type Now = () => number;

const SEC = 1000;
const TEMPLATE = "support-assistant-train";
/** The shop release that carried the training template when v3 launched (shopStory: v1.8.1 ran everywhere 9 days ago). */
const TEMPLATE_RELEASE = "v1.8.1";
const COMMIT = "4e7a9c21b0d3";
const PREV_COMMIT = "b81f03aa7c55";
const IMAGE_REPO = "us-docker.pkg.dev/acme-ml/train/support-assistant-train";

/** A stable hex string from a seed. */
function hex(seed: string, len = 64): string {
  let out = "";
  for (let i = 0; out.length < len; i++) out += hash(`${seed}#${i}`).toString(16).padStart(8, "0");
  return out.slice(0, len);
}

const IMAGE_DIGEST = `sha256:${hex("train-image-v1.8.1")}`;
const IMAGE_DIGEST_V2 = `sha256:${hex("train-image-v1.7.4")}`;

const subj = (kind: MlSubjectKind, id: string, label: string): SubjectInit => ({ kind, id, label });
const nodeIdOf = (name: string) => nodeId(poolOfNode(name) as PoolDef, name);
const nodeSubject = (name: string) => subj(MlSubjectKind.NODE, nodeIdOf(name), name);
const findingSubject = (id: string) => subj(MlSubjectKind.FINDING, id, id);

// ── Cost ──────────────────────────────────────────────────────────────────────

export type CostParts = Partial<Record<MlCostCategory, number>>;

function addParts(a: CostParts, b: CostParts): CostParts {
  const out: CostParts = { ...a };
  for (const [k, v] of Object.entries(b)) out[Number(k) as MlCostCategory] = (out[Number(k) as MlCostCategory] ?? 0) + (v ?? 0);
  return out;
}

/** GPU-hours by category, priced per GPU-hour (one pool's price, or a per-category usd map). */
function mkCost(parts: CostParts, usdPerGpuHour: number, usdParts?: CostParts): CostInit {
  const byCategory = Object.entries(parts)
    .filter(([, v]) => (v ?? 0) > 0)
    .map(([c, v]) => ({ category: Number(c) as MlCostCategory, gpuHours: v!, usd: usdParts ? (usdParts[Number(c) as MlCostCategory] ?? 0) : v! * usdPerGpuHour }));
  const gpuHours = byCategory.reduce((s, x) => s + x.gpuHours, 0);
  const usd = byCategory.reduce((s, x) => s + x.usd, 0);
  return { gpuHours, usd, priced: true, byCategory };
}

// ── Attempt specs ─────────────────────────────────────────────────────────────

/** A recovery, stage by stage. The stages after `detect` sum to (runningAt − previous end); `first step` follows running. */
interface RecoveryDef {
  stages: readonly (readonly [stage: string, seconds: number, detail: string])[];
  remediation: string;
  automatic: boolean;
  alertId: string;
  cordoned: readonly string[];
  spare: string;
  /** Whether the reschedule stage held the GPUs (billed as queued-holding). */
  holding: boolean;
}

/** One attempt, as every run of the world declares it (V3_ATTEMPTS plus what the views need). */
export interface AttemptSpec extends AttemptDef {
  gpus: number;
  pool: PoolDef;
  /** First attempts only: when Kueue admitted it (offset ms). */
  admittedAt?: number;
  checkpointEvery: number;
  /** Seconds the loop blocks per save. */
  blockingSeconds: number;
  recovery?: RecoveryDef;
  /** A stretch at the end in which it made no progress (a hang), seconds. */
  stalledTailSeconds?: number;
  /** When a straggler began slowing it (offset ms), and the share of throughput lost. */
  slowedSince?: number;
  slowdown?: number;
  /** Share of running time lost to stalls (data-loader waits, eval syncs) on runs without a story of their own. */
  stallShare?: number;
  /** A person cancelled it (AttemptEnd has no word for that). */
  cancelled?: boolean;
  localQueue: string;
  clusterQueue: string;
  cohort: string;
  flavor: string;
  borrowedGpus: number;
  evicted?: { reason: string; preemptions: number; requeues: number };
}

const RESEARCH = { localQueue: "ml-research/train", clusterQueue: QUEUES.research.name, cohort: QUEUES.research.cohort, flavor: QUEUES.research.flavor };
const EVALQ = { localQueue: "ml-research/eval", clusterQueue: QUEUES.eval.name, cohort: QUEUES.eval.cohort, flavor: QUEUES.eval.flavor };

/** The recoveries of v3, keyed "<phase>/<n>" (the attempt that came back). */
export const V3_RECOVERIES: Record<string, RecoveryDef> = {
  "pretrain/2": {
    stages: [
      ["detect", 1, "Kueue evicted the Workload (Preempted, InCohortReclamation): no detection delay"],
      ["drain", 118, "SIGTERM to 8 pods; the emergency checkpoint at step 6,400 did not finish in the 120 s grace period (partial)"],
      ["reschedule", 6600, "Requeued in team-research; waited 1h 50m for 64 H100s of quota in cohort ml-h100"],
      ["image pull", 0, "Image still cached on all 8 nodes"],
      ["NCCL init", 68, "64 ranks, GPUDirect-TCPX rings + NVLS"],
      ["checkpoint load", 414, "Step 6,000: 212 GB sharded DCP from GCS"],
      ["first step", 14, "Recompile + first optimizer step"],
    ],
    remediation: "Kueue requeued the workload; it waited 1h 50m for team-research quota in cohort ml-h100, then resumed pretrain attempt 2 from checkpoint step 6,000 on the same 8 nodes.",
    automatic: false,
    alertId: "",
    cordoned: [],
    spare: "",
    holding: false,
  },
  "sft/2": {
    stages: [
      ["detect", 600, "NCCL watchdog timeout (600 s, the default): allreduce seq 88213, rank 37 never arrived"],
      ["drain", 186, "Flight-recorder dump from 64 ranks to GCS (74 s), then pod teardown"],
      ["reschedule", 842, "DCGM diag r2 on all 8 nodes before restarting (passed: c8dw's thermal slowdown does not fail a diag); Kueue kept the quota"],
      ["image pull", 0, "Image cached on the same nodes"],
      ["NCCL init", 71, "64 ranks on the same nodes"],
      ["checkpoint load", 341, "Step 12,000: 212 GB sharded DCP from GCS"],
      ["first step", 52, "Recompile + first optimizer step"],
    ],
    remediation: "alert-nccl-hang dumped the NCCL flight recorder (64 ranks), ran DCGM diag r2 on all 8 nodes and restarted sft as attempt 2 on the same nodes, from checkpoint step 12,000.",
    automatic: true,
    alertId: ALERTS.ncclHang,
    cordoned: [],
    spare: "",
    holding: true,
  },
  "sft/3": {
    stages: [
      ["detect", 41, "DCGM Xid 79 event → alert-xid-fatal fired"],
      ["drain", 95, "Cordon r4vb; the 7 healthy pods write no emergency checkpoint (32,000 is 750 steps back) and exit; r4vb's pod is force-deleted at once (the GPU fell off the bus)"],
      ["reschedule", 38, "Kueue requeue + admission; hot spare gke-train-h100-s01 bound in place of r4vb"],
      ["image pull", 0, "Pre-pulled on the hot spare (a cold node pulls 18.4 GB in 212 s)"],
      ["NCCL init", 64, "64 ranks, new ring through s01"],
      ["checkpoint load", 318, "Step 32,000: 212 GB sharded DCP from GCS at 0.67 GB/s"],
      ["first step", 47, "Recompile + first optimizer step"],
    ],
    remediation: `alert-xid-fatal cordoned ${BAD_NODE}, swapped in hot spare ${SPARE_NODE}, resumed sft attempt 3 from checkpoint step 32,000.`,
    automatic: true,
    alertId: ALERTS.xidFatal,
    cordoned: [nodeIdOf(BAD_NODE)],
    spare: nodeIdOf(SPARE_NODE),
    holding: false,
  },
};

/** V3_ATTEMPTS with what the views need. */
export const V3_SPECS: readonly AttemptSpec[] = V3_ATTEMPTS.map((def) => {
  const pretrain = def.phase === "pretrain";
  const spec: AttemptSpec = {
    ...def,
    gpus: V3_NOW.worldSize,
    pool: POOLS.h100,
    checkpointEvery: pretrain ? V3_PHASES.pretrain.checkpointEvery : V3_PHASES.sft.checkpointEvery,
    blockingSeconds: pretrain ? 9 : 21,
    recovery: V3_RECOVERIES[`${def.phase}/${def.n}`],
    ...RESEARCH,
    // team-research's nominal quota is 48 H100s: a 64-GPU attempt borrows 16 from the cohort.
    borrowedGpus: 16,
  };
  if (def.n === 1) spec.admittedAt = def.runningAt - (pretrain ? 14 : 9) * MINUTE;
  if (def.end === "nccl_timeout") spec.stalledTailSeconds = 600;
  if (def.end === "running") {
    spec.slowedSince = STRAGGLER_SINCE;
    spec.slowdown = 0.15;
  }
  if (def.end === "preempted_by_queue") spec.evicted = { reason: "Preempted", preemptions: 1, requeues: 1 };
  return spec;
});

const END_REASON: Record<AttemptEnd, MlAttemptEndReason> = {
  completed: MlAttemptEndReason.COMPLETED,
  preempted_by_queue: MlAttemptEndReason.PREEMPTED_BY_QUEUE,
  preempted_by_scheduler: MlAttemptEndReason.PREEMPTED_BY_SCHEDULER,
  nccl_timeout: MlAttemptEndReason.NCCL_TIMEOUT,
  gpu_error: MlAttemptEndReason.GPU_ERROR,
  user_code_error: MlAttemptEndReason.USER_CODE_ERROR,
  rewind: MlAttemptEndReason.REWIND,
  running: MlAttemptEndReason.UNSPECIFIED,
};

const stepSeconds = (a: AttemptSpec) => 3600 / a.stepsPerHour;
/** When an attempt reached a step (offset ms). */
export const timeOfStep = (a: AttemptSpec, step: number) => a.runningAt + ((step - a.startStep) / a.stepsPerHour) * HOUR;
/** The attempt end, or now (0) while it runs. */
const endOf = (a: AttemptSpec) => a.endedAt ?? 0;

const failed = (a: AttemptSpec) => a.end !== "completed" && a.end !== "running" && !a.cancelled;
const stepsLost = (a: AttemptSpec) => (failed(a) ? a.endStep - a.lastCheckpointStep : 0);

/** Recovery stage seconds after `detect` (drain .. checkpoint load). */
export const recoveryGapSeconds = (r: RecoveryDef) => r.stages.filter(([s]) => s !== "detect" && s !== "first step").reduce((sum, [, v]) => sum + v, 0);
const stageSeconds = (r: RecoveryDef, stage: string) => r.stages.find(([s]) => s === stage)?.[1] ?? 0;
export const recoveryTotalSeconds = (r: RecoveryDef) => r.stages.reduce((sum, [, v]) => sum + v, 0);

/** The steps an attempt checkpointed durably. */
function durableSteps(a: AttemptSpec): number[] {
  const out: number[] = [];
  for (let s = a.startStep + a.checkpointEvery; s <= a.lastCheckpointStep; s += a.checkpointEvery) if (s % a.checkpointEvery === 0) out.push(s);
  return out;
}

/** When each queued / starting stretch of an attempt began and ended (offset ms). */
function preRunWindows(a: AttemptSpec, prev: AttemptSpec | undefined) {
  if (a.recovery && prev) {
    const drainEnd = a.queuedAt + stageSeconds(a.recovery, "drain") * SEC;
    const admitted = drainEnd + stageSeconds(a.recovery, "reschedule") * SEC;
    return { drain: [a.queuedAt, drainEnd] as const, queued: [drainEnd, admitted] as const, starting: [admitted, a.runningAt] as const, admitted };
  }
  const admitted = a.admittedAt ?? a.queuedAt;
  return { drain: null, queued: [a.queuedAt, admitted] as const, starting: [admitted, a.runningAt] as const, admitted };
}

/** The attempt's cost, by category (GPU-hours). */
export function attemptCostParts(a: AttemptSpec, prev: AttemptSpec | undefined): CostParts {
  const g = a.gpus / 3600;
  const w = preRunWindows(a, prev);
  const parts: CostParts = {};
  if (w.drain) parts[MlCostCategory.RESTART_LOST] = ((w.drain[1] - w.drain[0]) / SEC) * g;
  if (a.recovery?.holding) parts[MlCostCategory.QUEUED_HOLDING] = ((w.queued[1] - w.queued[0]) / SEC) * g;
  parts[MlCostCategory.STARTUP] = ((w.starting[1] - w.starting[0]) / SEC) * g;
  const runSec = (endOf(a) - a.runningAt) / SEC;
  const ckpt = durableSteps(a).length * a.blockingSeconds;
  let stalled = a.stalledTailSeconds ?? 0;
  if (a.slowedSince !== undefined) stalled += ((endOf(a) - a.slowedSince) / SEC) * (a.slowdown ?? 0);
  if (a.stallShare) stalled += runSec * a.stallShare;
  const detect = a.end === "gpu_error" ? 41 : 0;
  const lost = stepsLost(a) * stepSeconds(a) + detect;
  parts[MlCostCategory.CHECKPOINT] = ckpt * g;
  if (stalled) parts[MlCostCategory.STALLED] = stalled * g;
  if (lost) parts[MlCostCategory.RESTART_LOST] = (parts[MlCostCategory.RESTART_LOST] ?? 0) + lost * g;
  parts[MlCostCategory.PRODUCTIVE] = (runSec - ckpt - stalled - lost) * g;
  return parts;
}

/** The running stretch of an attempt as spans: running, checkpointing at each save, stalled for a hang or a straggler. */
function runningSpans(a: AttemptSpec, now: Now, label: string): Init<typeof MlTimeSpanSchema>[] {
  const end = endOf(a);
  const cuts: { start: number; end: number; state: string; label: string }[] = durableSteps(a).map((s) => {
    const t = timeOfStep(a, s);
    return { start: t, end: t + a.blockingSeconds * SEC, state: "checkpointing", label: `checkpoint ${s.toLocaleString("en-US")}` };
  });
  if (a.stalledTailSeconds) cuts.push({ start: end - a.stalledTailSeconds * SEC, end, state: "stalled", label: "hung in allreduce" });
  cuts.sort((x, y) => x.start - y.start);
  const spans: Init<typeof MlTimeSpanSchema>[] = [];
  const base = (from: number, to: number) => {
    if (to <= from) return;
    if (a.slowedSince !== undefined && a.slowedSince > from && a.slowedSince < to) {
      spans.push({ state: "running", start: at(from, now), end: at(a.slowedSince, now), label });
      spans.push({ state: "stalled", start: at(a.slowedSince, now), end: to === 0 ? undefined : at(to, now), label: "slowed: rank 37 straggler" });
    } else if (a.slowedSince !== undefined && from >= a.slowedSince) {
      spans.push({ state: "stalled", start: at(from, now), end: to === 0 ? undefined : at(to, now), label: "slowed: rank 37 straggler" });
    } else spans.push({ state: "running", start: at(from, now), end: to === 0 && a.endedAt === null ? undefined : at(to, now), label });
  };
  let cursor = a.runningAt;
  for (const c of cuts) {
    base(cursor, c.start);
    spans.push({ state: c.state, start: at(c.start, now), end: at(c.end, now), label: c.label });
    cursor = c.end;
  }
  base(cursor, end);
  return spans;
}

function attemptSpans(a: AttemptSpec, prev: AttemptSpec | undefined, now: Now): Init<typeof MlTimeSpanSchema>[] {
  const w = preRunWindows(a, prev);
  const spans: Init<typeof MlTimeSpanSchema>[] = [];
  if (w.drain) spans.push({ state: "recovering", start: at(w.drain[0], now), end: at(w.drain[1], now), label: "drain" });
  if (w.queued[1] > w.queued[0]) spans.push({ state: "queued", start: at(w.queued[0], now), end: at(w.queued[1], now), label: a.recovery?.holding ? "held: pre-restart diag" : "waiting for quota" });
  if (a.endedAt === null && a.runningAt === 0) return spans;
  spans.push({ state: "starting", start: at(w.starting[0], now), end: at(w.starting[1], now), label: "pod start" });
  return [...spans, ...runningSpans(a, now, `a${a.n}`)];
}

function podStart(a: AttemptSpec, runName: string): Init<typeof MlAttemptSchema>["podStart"] {
  const r = a.recovery;
  const total = (a.runningAt - (a.admittedAt ?? a.runningAt)) / SEC;
  const stages = r
    ? [
        ["scheduling", stageSeconds(r, "reschedule"), ""],
        ["image pull", stageSeconds(r, "image pull"), ""],
        ["sandbox/init", stageSeconds(r, "NCCL init"), "NCCL init"],
        ["model load", stageSeconds(r, "checkpoint load"), "checkpoint load from GCS"],
      ]
    : [
        ["scheduling", 9, ""],
        ["image pull", 212, "18.4 GB image, cold nodes"],
        ["sandbox/init", 70, "NCCL init"],
        ["model load", Math.max(0, total - 291), "base weights / checkpoint load"],
      ];
  const sum = stages.reduce((s, x) => s + (x[1] as number), 0) || 1;
  return {
    workload: workloadName(runName, a.phase, a.n),
    sampleCount: Math.max(1, Math.ceil(a.gpus / a.pool.gpusPerNode)),
    stages: stages.map(([stage, p50, cause]) => ({ stage: stage as string, p50Seconds: p50 as number, p95Seconds: (p50 as number) * 1.15, share: (p50 as number) / sum, topCause: cause as string, findingId: stage === "image pull" && (p50 as number) > 100 ? FINDINGS.imagePull : "" })),
  };
}

function workload(a: AttemptSpec, prev: AttemptSpec | undefined, runName: string, now: Now): Init<typeof MlKueueWorkloadSchema> {
  const w = preRunWindows(a, prev);
  const created = w.queued[0];
  const name = workloadName(runName, a.phase, a.n);
  return {
    name,
    uid: `${hex(name, 8)}-${hex(`${name}u`, 4)}-${hex(`${name}v`, 4)}-${hex(`${name}w`, 12)}`,
    localQueue: a.localQueue,
    clusterQueue: a.clusterQueue,
    cohort: a.cohort,
    priority: 100,
    priorityClass: "research-normal",
    createdAt: at(created, now),
    quotaReservedAt: a.runningAt ? at(w.admitted - 2 * SEC, now) : undefined,
    admittedAt: a.runningAt ? at(w.admitted, now) : undefined,
    evictedAt: a.evicted && a.endedAt !== null ? at(a.endedAt, now) : undefined,
    evictionReason: a.evicted?.reason ?? "",
    flavor: a.flavor,
    gpus: a.gpus,
    borrowedGpus: a.borrowedGpus,
    preemptions: a.evicted?.preemptions ?? 0,
    requeues: a.evicted?.requeues ?? 0,
    admissionChecks: a.recovery?.spare ? [`hot-spare: ${SPARE_NODE} bound for ${BAD_NODE}`] : [],
    topology: a.gpus >= 16 ? "block" : "",
  };
}

function recovery(a: AttemptSpec, prev: AttemptSpec, runId: string): Init<typeof MlRecoverySchema> | undefined {
  const r = a.recovery;
  if (!r) return undefined;
  const total = recoveryTotalSeconds(r);
  const lost = ((total + stepsLost(prev) * stepSeconds(prev)) * a.gpus) / 3600;
  return {
    stages: r.stages.map(([stage, seconds, detail]) => ({ stage, seconds, detail })),
    totalSeconds: total,
    remediation: r.remediation,
    automatic: r.automatic,
    cordonedNodeIds: [...r.cordoned],
    hotSpareNodeId: r.spare,
    resumedFromCheckpointId: checkpointId(runId, a.phase, a.startStep),
    resumedStep: BigInt(a.startStep),
    gpuHoursLost: lost,
    usdLost: lost * a.pool.usdPerGpuHour,
    alertId: r.alertId,
  };
}

/** The 64 ranks of v3 sft attempt 3 (rank r: node floor(r/8), local rank r%8, dp floor(r/8), tp r%8). */
export function v3Ranks(a: AttemptSpec): Init<typeof MlRankSchema>[] {
  const t0 = v3StepTimeMs("sft", V3_NOW.step, false);
  const ts = v3StepTimeMs("sft", V3_NOW.step, true);
  const mfu = v3Mfu("sft", V3_NOW.step, true);
  return Array.from({ length: V3_NOW.worldSize }, (_, r) => {
    const node = a.nodes[Math.floor(r / 8)];
    const j = rng(hash(`rank-${r}`));
    const straggler = r === STRAGGLER_RANK;
    const sameNode = Math.floor(r / 8) === Math.floor(STRAGGLER_RANK / 8);
    const compute = 0.77 * t0 * (straggler ? 1.18 : 1 + 0.006 * (j() - 0.5));
    const data = 0.03 * t0 * (1 + 0.2 * (j() - 0.5));
    const comm = Math.max(0, ts - compute - data);
    return {
      rank: r,
      localRank: r % 8,
      pod: rankPod(RUNS.v3.name, a.phase, a.n, r),
      nodeId: nodeIdOf(node),
      deviceUuid: gpuUuid(node, r % 8),
      deviceIndex: r % 8,
      stepTimeMsP50: ts,
      computeMsP50: compute,
      commExposedMsP50: comm,
      dataWaitMsP50: data,
      smActive: straggler ? 0.69 : sameNode ? 0.74 : 0.77 + 0.02 * (j() - 0.5),
      straggler,
      stragglerReason: straggler ? `HW slowdown (thermal) on ${HOT_NODE} GPU ${HOT_GPU_INDEX}: 87°C, HBM 95°C` : "",
      dp: Math.floor(r / 8),
      tp: r % 8,
      pp: 0,
      cp: 0,
      ep: 0,
      tflops: ((mfu * 989 * ts) / compute) * (straggler ? 1 : 1),
      memoryAllocatedGb: 61.2 + (r === 0 ? 1.1 : 0) + 0.4 * (j() - 0.5),
      memoryReservedGb: 68.5 + 0.6 * (j() - 0.5),
      memoryPeakGb: 72.8 + (r === 0 ? 1.1 : 0) + 0.5 * (j() - 0.5),
      worldSize: V3_NOW.worldSize,
    };
  });
}

function buildAttempt(a: AttemptSpec, prev: AttemptSpec | undefined, runId: string, runName: string, now: Now, withRanks: boolean): AttemptInit {
  const queuedOnly = a.runningAt === 0 && a.endedAt === null;
  const state =
    a.end === "running"
      ? queuedOnly
        ? MlPhaseState.QUEUED
        : MlPhaseState.RUNNING
      : a.end === "completed"
        ? MlPhaseState.SUCCEEDED
        : a.end === "user_code_error" || a.end === "nccl_timeout" || a.end === "gpu_error"
          ? MlPhaseState.FAILED
          : MlPhaseState.CANCELLED;
  const nodes = a.nodes;
  const parts = queuedOnly ? {} : attemptCostParts(a, prev);
  return {
    n: a.n,
    state: a.end === "preempted_by_queue" ? MlPhaseState.FAILED : state,
    endReason: a.cancelled ? MlAttemptEndReason.CANCELLED : END_REASON[a.end],
    endDetail: a.endDetail,
    queuedAt: at(a.queuedAt, now),
    admittedAt: queuedOnly ? undefined : at(preRunWindows(a, prev).admitted, now),
    runningAt: queuedOnly ? undefined : at(a.runningAt, now),
    endedAt: a.endedAt === null ? undefined : at(a.endedAt, now),
    startStep: BigInt(a.startStep),
    endStep: BigInt(a.endStep),
    stepsLost: BigInt(stepsLost(a)),
    resumedFromCheckpointId: a.n > 1 && a.startStep > 0 ? checkpointId(runId, a.phase, a.startStep) : "",
    worldSize: a.gpus,
    nodeIds: nodes.map(nodeIdOf),
    deviceUuids: nodes.flatMap((n) => Array.from({ length: Math.min(a.pool.gpusPerNode, a.gpus) }, (_, i) => gpuUuid(n, i))),
    imageDigest: runId === RUNS.v3.id ? IMAGE_DIGEST : IMAGE_DIGEST_V2,
    spans: attemptSpans(a, prev, now),
    podStart: queuedOnly ? undefined : podStart(a, runName),
    cost: mkCost(parts, a.pool.usdPerGpuHour),
    rewoundToStep: 0n,
    ranks: withRanks && runId === RUNS.v3.id && a.phase === V3_NOW.phase && a.n === V3_NOW.attempt ? v3Ranks(a) : [],
    endEventId: failed(a) || a.end === "completed" ? `ev-${runId}-${a.phase}-${a.n}-end` : "",
    workload: workload(a, prev, runName, now),
    recovery: prev ? recovery(a, prev, runId) : undefined,
  };
}

// ── Phases and runs ───────────────────────────────────────────────────────────

interface PhaseSpec {
  key: string;
  kind: MlPhaseKind;
  label: string;
  state: MlPhaseState;
  dependsOn?: string[];
  pool: PoolDef;
  queueId: string;
  orchestrator: string;
  method: string;
  totalSteps: number;
  attempts: readonly AttemptSpec[];
  segments?: { name: string; from: number; to: number; note: string }[];
  input?: SubjectInit;
  evaluatedSteps?: number[];
  /** For an eval phase: its cost and spans, built apart. */
  custom?: (now: Now) => { attempts: AttemptInit[]; cost: CostParts; startedAt?: number; endedAt?: number | null };
  accelerators: number;
  summary?: Record<string, number>;
}

interface PhaseBuilt {
  init: PhaseInit;
  parts: CostParts;
  usdParts: CostParts;
}

function buildPhase(p: PhaseSpec, runId: string, runName: string, now: Now, withRanks: boolean): PhaseBuilt {
  let attempts: AttemptInit[];
  let parts: CostParts = {};
  let startedAt: number | undefined;
  let endedAt: number | null | undefined;
  if (p.custom) {
    const c = p.custom(now);
    attempts = c.attempts;
    parts = c.cost;
    startedAt = c.startedAt;
    endedAt = c.endedAt;
  } else {
    attempts = p.attempts.map((a, i) => buildAttempt(a, p.attempts[i - 1], runId, runName, now, withRanks));
    p.attempts.forEach((a, i) => {
      if (!(a.runningAt === 0 && a.endedAt === null)) parts = addParts(parts, attemptCostParts(a, p.attempts[i - 1]));
    });
    startedAt = p.attempts[0]?.queuedAt;
    const last = p.attempts[p.attempts.length - 1];
    endedAt = last ? last.endedAt : undefined;
  }
  const usdParts: CostParts = {};
  for (const [k, v] of Object.entries(parts)) usdParts[Number(k) as MlCostCategory] = (v ?? 0) * p.pool.usdPerGpuHour;
  const last = p.attempts[p.attempts.length - 1];
  const total = Object.values(parts).reduce((s, v) => s + (v ?? 0), 0);
  const productive = parts[MlCostCategory.PRODUCTIVE] ?? 0;
  return {
    parts,
    usdParts,
    init: {
      key: p.key,
      kind: p.kind,
      label: p.label,
      state: p.state,
      dependsOn: p.dependsOn ?? [],
      clusterId: p.pool.clusterId,
      queueId: p.queueId,
      orchestrator: p.orchestrator,
      method: p.method,
      startedAt: startedAt !== undefined ? at(startedAt, now) : undefined,
      endedAt: endedAt !== undefined && endedAt !== null && p.state !== MlPhaseState.RUNNING ? at(endedAt, now) : undefined,
      fromStep: 0n,
      step: BigInt(last?.endStep ?? 0),
      totalSteps: BigInt(p.totalSteps),
      input: p.input,
      attempts,
      segments: (p.segments ?? []).map((s) => ({ name: s.name, fromStep: BigInt(s.from), toStep: BigInt(s.to), note: s.note })),
      accelerators: p.accelerators,
      worldSize: p.accelerators,
      goodput: total > 0 ? productive / total : Number.NaN,
      cost: mkCost(parts, p.pool.usdPerGpuHour),
      evaluatedSteps: (p.evaluatedSteps ?? []).map(BigInt),
      summary: p.summary ?? {},
    },
  };
}

interface RunSpec {
  id: string;
  name: string;
  experimentId: string;
  owner: string;
  sweepId?: string;
  trial?: number;
  tags: string[];
  state: MlRunState;
  badges: MlRunBadge[];
  template: string;
  templateRelease: string;
  commit: string;
  createdAt: number;
  endedAt?: number;
  currentPhase: string;
  phases: PhaseSpec[];
  tokensPerStep: number;
  etaSeconds?: number;
  accelerators: number;
  product: string;
  summary: Record<string, number>;
  params: Record<string, string>;
  objective: { x: number[]; y: number[] };
  launchedVia: string;
}

interface RunBuilt {
  init: RunInit;
  phases: PhaseBuilt[];
  spec: RunSpec;
}

function buildRun(spec: RunSpec, now: Now, withRanks = false): RunBuilt {
  const phases = spec.phases.map((p) => buildPhase(p, spec.id, spec.name, now, withRanks));
  let parts: CostParts = {};
  let usdParts: CostParts = {};
  for (const p of phases) {
    parts = addParts(parts, p.parts);
    usdParts = addParts(usdParts, p.usdParts);
  }
  const total = Object.values(parts).reduce((s, v) => s + (v ?? 0), 0);
  const current = spec.phases.find((p) => p.key === spec.currentPhase) ?? spec.phases[0];
  const curStep = current?.attempts[current.attempts.length - 1]?.endStep ?? 0;
  const allAttempts = spec.phases.flatMap((p) => p.attempts);
  const restarts = allAttempts.filter((a) => a.n > 1).length;
  const preemptions = allAttempts.filter((a) => a.end === "preempted_by_queue" || a.end === "preempted_by_scheduler").length;
  const runSubject = subj(MlSubjectKind.RUN, spec.id, spec.name);
  const started = allAttempts.find((a) => a.runningAt !== 0)?.runningAt;
  return {
    spec,
    phases,
    init: {
      id: spec.id,
      name: spec.name,
      experimentId: spec.experimentId,
      project: ML_PROJECT,
      owner: spec.owner,
      sweepId: spec.sweepId ?? "",
      trial: spec.trial ?? 0,
      tags: spec.tags,
      state: spec.state,
      badges: spec.badges,
      template: spec.template,
      templateRelease: spec.templateRelease,
      commit: spec.commit,
      createdAt: at(spec.createdAt, now),
      startedAt: started !== undefined ? at(started, now) : undefined,
      endedAt: spec.endedAt !== undefined ? at(spec.endedAt, now) : undefined,
      currentPhaseKey: spec.currentPhase,
      step: BigInt(curStep),
      totalSteps: BigInt(current?.totalSteps ?? 0),
      tokensSeen: BigInt(curStep * spec.tokensPerStep),
      tokensPlanned: BigInt((current?.totalSteps ?? 0) * spec.tokensPerStep),
      etaSeconds: spec.etaSeconds,
      accelerators: spec.accelerators,
      acceleratorProduct: spec.product,
      goodput: total > 0 ? (parts[MlCostCategory.PRODUCTIVE] ?? 0) / total : Number.NaN,
      cost: mkCost(parts, 0, usdParts),
      restarts,
      preemptions,
      summary: spec.summary,
      params: spec.params,
      objectiveKey: spec.experimentId === EXPERIMENTS.reranker.id ? EXPERIMENTS.reranker.objectiveKey : EXPERIMENTS.support.objectiveKey,
      objective: {
        key: spec.experimentId === EXPERIMENTS.reranker.id ? EXPERIMENTS.reranker.objectiveKey : "val/loss",
        label: spec.experimentId === EXPERIMENTS.reranker.id ? "nDCG@10" : "val loss",
        unit: "",
        axis: MlAxis.STEP,
        x: spec.objective.x,
        y: spec.objective.y,
        resolution: 2000,
        collection: spec.objective.x.length ? MlCollection.COLLECTED : MlCollection.NOT_COLLECTED,
        missingSource: spec.objective.x.length ? "" : "the Reliant ML SDK in the training loop (an observed job reports no metrics)",
        fixFindingId: spec.objective.x.length ? "" : FINDINGS.trainingUntracked,
        subject: runSubject,
      },
      phases: phases.map((p) => p.init),
      clusterIds: [...new Set(spec.phases.map((p) => p.pool.clusterId))],
      launchedVia: spec.launchedVia,
    },
  };
}

// ── The hero run: llama-8b-support-v3 ─────────────────────────────────────────

const v3Attempts = (phase: string) => V3_SPECS.filter((a) => a.phase === phase);
const V3_SFT = v3Attempts("sft");
const V3_PRETRAIN = v3Attempts("pretrain");
const V3_SFT_NOW = V3_SFT[V3_SFT.length - 1];
const V3_SFT_PER_HOUR = V3_SFT_NOW.stepsPerHour;

/** The SFT steps eval-periodic evaluated, and when each ran (offset ms of the checkpoint's save). */
export const V3_EVAL_STEPS: readonly number[] = Array.from({ length: 20 }, (_, i) => (i + 1) * V3_PHASES.evalPeriodic.everySteps);

function attemptOfStep(attempts: readonly AttemptSpec[], step: number): AttemptSpec {
  return attempts.find((a) => step > a.startStep && step <= a.lastCheckpointStep) ?? attempts.find((a) => step > a.startStep && step <= a.endStep) ?? attempts[attempts.length - 1];
}

const EVAL_RUN_MINUTES = 22;
const EVAL_START_MINUTES = 3;
const EVAL_GPUS = 8;
const EVAL_NODE = POOLS.a100.nodes[0];

/** The eval-periodic phase: one long-lived attempt that wakes for each checkpoint every 2,000 steps. */
function v3EvalPhase(now: Now): ReturnType<NonNullable<PhaseSpec["custom"]>> {
  const spans: Init<typeof MlTimeSpanSchema>[] = [];
  let startup = 0;
  let productive = 0;
  const g = EVAL_GPUS / 60;
  // The eval job holds its 8 A100s between evals (FINDINGS.idleEvalHolding): idle-allocated.
  let cursor = timeOfStep(V3_SFT[0], 2000) + 2.5 * MINUTE;
  for (const step of V3_EVAL_STEPS) {
    const saved = timeOfStep(attemptOfStep(V3_SFT, step), step);
    const t = saved + 2.5 * MINUTE;
    if (t > cursor) spans.push({ state: "idle", start: at(cursor, now), end: at(t, now), label: "held idle (8 × A100)" });
    cursor = t + (EVAL_START_MINUTES + EVAL_RUN_MINUTES) * MINUTE;
    spans.push({ state: "starting", start: at(t, now), end: at(t + EVAL_START_MINUTES * MINUTE, now), label: `eval ${step / 1000}k` });
    spans.push({ state: "running", start: at(t + EVAL_START_MINUTES * MINUTE, now), end: at(t + (EVAL_START_MINUTES + EVAL_RUN_MINUTES) * MINUTE, now), label: `eval ${step / 1000}k` });
    startup += EVAL_START_MINUTES * g;
    productive += EVAL_RUN_MINUTES * g;
  }
  spans.push({ state: "idle", start: at(cursor, now), label: "held idle (8 × A100)" });
  const first = timeOfStep(V3_SFT[0], 2000) + 2.5 * MINUTE;
  const held = ((0 - first) / HOUR) * EVAL_GPUS;
  const parts: CostParts = { [MlCostCategory.STARTUP]: startup, [MlCostCategory.PRODUCTIVE]: productive, [MlCostCategory.IDLE_ALLOCATED]: held - startup - productive };
  const name = workloadName(RUNS.v3.name, "eval-periodic", 1);
  return {
    startedAt: first,
    endedAt: null,
    cost: parts,
    attempts: [
      {
        n: 1,
        state: MlPhaseState.RUNNING,
        endReason: MlAttemptEndReason.UNSPECIFIED,
        queuedAt: at(first, now),
        admittedAt: at(first + 20 * SEC, now),
        runningAt: at(first + EVAL_START_MINUTES * MINUTE, now),
        startStep: 0n,
        endStep: BigInt(V3_EVAL_STEPS[V3_EVAL_STEPS.length - 1]),
        stepsLost: 0n,
        worldSize: EVAL_GPUS,
        nodeIds: [nodeIdOf(EVAL_NODE)],
        deviceUuids: Array.from({ length: EVAL_GPUS }, (_, i) => gpuUuid(EVAL_NODE, i)),
        imageDigest: IMAGE_DIGEST,
        spans,
        cost: mkCost(parts, POOLS.a100.usdPerGpuHour),
        workload: {
          name,
          uid: `${hex(name, 8)}-${hex(`${name}u`, 4)}-${hex(`${name}v`, 4)}-${hex(`${name}w`, 12)}`,
          localQueue: EVALQ.localQueue,
          clusterQueue: EVALQ.clusterQueue,
          cohort: EVALQ.cohort,
          priority: 50,
          priorityClass: "eval-low",
          createdAt: at(first, now),
          quotaReservedAt: at(first + 18 * SEC, now),
          admittedAt: at(first + 20 * SEC, now),
          flavor: EVALQ.flavor,
          gpus: EVAL_GPUS,
        },
      },
    ],
  };
}

const V3_SFT_ETA = ((V3_PHASES.sft.totalSteps - V3_NOW.step) / V3_SFT_PER_HOUR) * 3600;

/** v3's val loss at every eval step: its objective sparkline. */
const v3Objective = () => {
  const x = [...V3_PHASES.sft.totalSteps ? Array.from({ length: Math.floor(V3_NOW.step / 2000) }, (_, i) => (i + 1) * 2000) : []];
  return { x, y: x.map((s) => v3ValLoss("sft", s)) };
};

/** The best (lowest) SFT val loss so far, and its step (every 500 steps logged). */
export function v3BestValLoss(): { step: number; value: number } {
  let best = { step: 0, value: Infinity };
  for (let s = 500; s <= V3_NOW.step; s += 500) {
    const v = v3ValLoss("sft", s);
    if (v < best.value) best = { step: s, value: v };
  }
  return best;
}

const V3_TOKENS_PER_STEP = V3_NOW.tokensPerStep;

function v3Spec(): RunSpec {
  const best = v3BestValLoss();
  return {
    id: RUNS.v3.id,
    name: RUNS.v3.name,
    experimentId: EXPERIMENTS.support.id,
    owner: ML_PEOPLE.trainer,
    tags: ["support", "llama-3.1-8b", "candidate"],
    state: MlRunState.DEGRADED,
    badges: [MlRunBadge.STRAGGLER],
    template: TEMPLATE,
    templateRelease: TEMPLATE_RELEASE,
    commit: COMMIT,
    createdAt: V3_PRETRAIN[0].queuedAt - 2 * MINUTE,
    currentPhase: "sft",
    tokensPerStep: V3_TOKENS_PER_STEP,
    etaSeconds: V3_SFT_ETA,
    accelerators: V3_NOW.worldSize,
    product: POOLS.h100.product,
    summary: {
      "val/loss": best.value,
      "train/loss": v3TrainLoss("sft", V3_NOW.step),
      "train/lm_loss": v3TrainLoss("sft", V3_NOW.step) - 0.006,
      mfu: v3Mfu("sft", V3_NOW.step, true),
      "eval/mmlu_pro": evalScore("mmlu_pro", "v3@40000"),
      "throughput/tokens_per_s": v3TokensPerSecond("sft", V3_NOW.step, true),
    },
    params: { lr: "2e-5", warmup_ratio: "0.02", global_batch: "512", seq_len: "8192", "data.mix": "support-sft v3 0.8 / the-stack-v2 0.2" },
    objective: v3Objective(),
    launchedVia: "forge ml run",
    phases: [
      {
        key: "pretrain",
        kind: MlPhaseKind.PRETRAIN,
        label: "Continued pre-training",
        state: MlPhaseState.SUCCEEDED,
        pool: POOLS.h100,
        queueId: QUEUES.research.id,
        orchestrator: "JobSet",
        method: "FSDP2 + TP",
        totalSteps: V3_PHASES.pretrain.totalSteps,
        attempts: V3_PRETRAIN,
        accelerators: 64,
        input: subj(MlSubjectKind.MODEL, BASE_MODEL, BASE_MODEL),
        segments: [
          { name: "warmup", from: 0, to: 400, note: "LR linear warmup over 2%" },
          { name: "support-corpus v4", from: 400, to: 18000, note: "Domain corpus, 84B tokens" },
          { name: "anneal", from: 18000, to: 20000, note: "High-quality subset, LR to 10%" },
        ],
        summary: { "train/loss": v3LossCurve("pretrain", 20000), "val/loss": v3ValLoss("pretrain", 20000) },
      },
      {
        key: "sft",
        kind: MlPhaseKind.SFT,
        label: "SFT",
        state: MlPhaseState.RUNNING,
        dependsOn: ["pretrain"],
        pool: POOLS.h100,
        queueId: QUEUES.research.id,
        orchestrator: "JobSet",
        method: "FSDP2 + TP",
        totalSteps: V3_PHASES.sft.totalSteps,
        attempts: V3_SFT,
        accelerators: 64,
        input: subj(MlSubjectKind.CHECKPOINT, checkpointId(RUNS.v3.id, "pretrain", 20000), "pretrain step 20,000"),
        segments: [
          { name: "warmup", from: 0, to: 1200, note: "LR linear warmup over 2%" },
          { name: "mix: support-sft 80% / code 20%", from: 1200, to: 38600, note: "Packed 8k sequences" },
          { name: "shard 1184", from: 38600, to: 39100, note: "Loss spike traced to data shard 1184 of support-sft v3" },
          { name: "mix: support-sft 80% / code 20%", from: 39100, to: 60000, note: "" },
        ],
        summary: { "train/loss": v3TrainLoss("sft", V3_NOW.step), "val/loss": best.value },
      },
      {
        key: "eval-periodic",
        kind: MlPhaseKind.EVAL,
        label: "Eval every 2k",
        state: MlPhaseState.RUNNING,
        dependsOn: ["sft"],
        pool: POOLS.a100,
        queueId: QUEUES.eval.id,
        orchestrator: "Job",
        method: "release-gate-lite",
        totalSteps: 0,
        attempts: [],
        accelerators: EVAL_GPUS,
        evaluatedSteps: [...V3_EVAL_STEPS],
        custom: v3EvalPhase,
      },
      {
        key: "rl",
        kind: MlPhaseKind.RL,
        label: "RL (GRPO)",
        state: MlPhaseState.PENDING,
        dependsOn: ["sft"],
        pool: POOLS.h100,
        queueId: QUEUES.research.id,
        orchestrator: "JobSet",
        method: "GRPO",
        totalSteps: V3_PHASES.rl.totalSteps,
        attempts: [],
        accelerators: 64,
      },
    ],
  };
}

// ── The other runs ────────────────────────────────────────────────────────────

const H100_8 = POOLS.h100.nodes;

function simpleAttempt(o: Partial<AttemptSpec> & Pick<AttemptSpec, "phase" | "queuedAt" | "runningAt" | "endedAt" | "endStep" | "end" | "stepsPerHour" | "gpus" | "pool">): AttemptSpec {
  const nodes = o.nodes ?? o.pool.nodes.slice(0, Math.max(1, Math.ceil(o.gpus / o.pool.gpusPerNode)));
  const every = o.checkpointEvery ?? 2000;
  return {
    n: 1,
    startStep: 0,
    lastCheckpointStep: Math.floor(o.endStep / every) * every,
    endDetail: "",
    blockingSeconds: 12,
    admittedAt: o.runningAt - 6 * MINUTE,
    localQueue: o.pool === POOLS.a100 ? EVALQ.localQueue : RESEARCH.localQueue,
    clusterQueue: o.pool === POOLS.a100 ? EVALQ.clusterQueue : RESEARCH.clusterQueue,
    cohort: o.pool === POOLS.a100 ? EVALQ.cohort : RESEARCH.cohort,
    flavor: o.pool === POOLS.a100 ? EVALQ.flavor : RESEARCH.flavor,
    borrowedGpus: o.pool === POOLS.h100 && o.gpus > QUEUES.research.nominal ? o.gpus - QUEUES.research.nominal : 0,
    ...o,
    nodes,
    checkpointEvery: every,
  };
}

const V2_END = -21 * DAY;
const V2_SFT_HOURS = 60000 / 360;

function v2Spec(): RunSpec {
  const sft = simpleAttempt({
    phase: "sft",
    queuedAt: V2_END - 1.6 * HOUR - V2_SFT_HOURS * HOUR - 0.5 * HOUR,
    runningAt: V2_END - 1.6 * HOUR - V2_SFT_HOURS * HOUR,
    endedAt: V2_END - 1.6 * HOUR,
    endStep: 60000,
    lastCheckpointStep: 60000,
    end: "completed",
    endDetail: "Completed at step 60,000",
    stepsPerHour: 360,
    gpus: 64,
    pool: POOLS.h100,
    nodes: H100_8,
    checkpointEvery: 2000,
    blockingSeconds: 19,
    stallShare: 0.05,
  });
  const quant = simpleAttempt({
    phase: "quantize",
    queuedAt: V2_END - 1.3 * HOUR,
    runningAt: V2_END - 1.2 * HOUR,
    endedAt: V2_END,
    endStep: 1,
    lastCheckpointStep: 1,
    checkpointEvery: 1,
    end: "completed",
    endDetail: "FP8 (E4M3) weights + KV cache scales written",
    stepsPerHour: 1 / 1.2,
    gpus: 8,
    pool: POOLS.h100,
    nodes: [H100_8[0]],
    blockingSeconds: 0,
  });
  const x = Array.from({ length: 30 }, (_, i) => (i + 1) * 2000);
  return {
    id: RUNS.v2.id,
    name: RUNS.v2.name,
    experimentId: EXPERIMENTS.support.id,
    owner: ML_PEOPLE.trainer,
    tags: ["support", "llama-3.1-8b", "production"],
    state: MlRunState.SUCCEEDED,
    badges: [],
    template: TEMPLATE,
    templateRelease: "v1.7.4",
    commit: PREV_COMMIT,
    createdAt: sft.queuedAt - 3 * MINUTE,
    endedAt: V2_END,
    currentPhase: "sft",
    tokensPerStep: V3_TOKENS_PER_STEP,
    accelerators: 64,
    product: POOLS.h100.product,
    summary: { "val/loss": 1.894, "train/loss": 1.851, mfu: 0.335, "eval/mmlu_pro": evalScore("mmlu_pro", "v2") },
    params: { lr: "3e-5", warmup_ratio: "0.03", global_batch: "512", seq_len: "8192", "data.mix": "support-sft v2 0.85 / the-stack-v2 0.15" },
    objective: { x, y: x.map((s) => 1.894 + 0.55 * Math.pow(1 + s / 1500, -0.5) - 0.55 * Math.pow(1 + 60000 / 1500, -0.5)) },
    launchedVia: "forge ml run",
    phases: [
      { key: "sft", kind: MlPhaseKind.SFT, label: "SFT", state: MlPhaseState.SUCCEEDED, pool: POOLS.h100, queueId: QUEUES.research.id, orchestrator: "JobSet", method: "FSDP2 + TP", totalSteps: 60000, attempts: [sft], accelerators: 64, input: subj(MlSubjectKind.MODEL, BASE_MODEL, BASE_MODEL) },
      {
        key: "eval-periodic",
        kind: MlPhaseKind.EVAL,
        label: "Eval every 10k",
        state: MlPhaseState.SUCCEEDED,
        dependsOn: ["sft"],
        pool: POOLS.a100,
        queueId: QUEUES.eval.id,
        orchestrator: "Job",
        method: "release-gate",
        totalSteps: 0,
        attempts: [],
        accelerators: 8,
        evaluatedSteps: [10000, 20000, 30000, 40000, 50000, 60000],
        custom: () => ({ attempts: [], cost: { [MlCostCategory.PRODUCTIVE]: 6 * (25 / 60) * 8 }, startedAt: sft.runningAt + 28 * HOUR, endedAt: sft.endedAt! + HOUR }),
      },
      { key: "quantize", kind: MlPhaseKind.QUANTIZE, label: "Quantize (FP8)", state: MlPhaseState.SUCCEEDED, dependsOn: ["sft"], pool: POOLS.h100, queueId: QUEUES.research.id, orchestrator: "Job", method: "FP8 (llm-compressor)", totalSteps: 1, attempts: [quant], accelerators: 8, input: subj(MlSubjectKind.CHECKPOINT, V2_FINAL_CHECKPOINT, "sft step 60,000") },
    ],
  };
}

const V1_END = -44 * DAY;

function v1Spec(): RunSpec {
  const hours = 22140 / 360;
  const sft = simpleAttempt({
    phase: "sft",
    queuedAt: V1_END - hours * HOUR - 0.4 * HOUR,
    runningAt: V1_END - hours * HOUR,
    endedAt: V1_END,
    endStep: 22140,
    lastCheckpointStep: 22000,
    end: "user_code_error",
    endDetail: "Bus error in DataLoader worker 3 (/dev/shm 64Mi)",
    stepsPerHour: 360,
    gpus: 32,
    pool: POOLS.h100,
    nodes: H100_8.slice(0, 4),
    stallShare: 0.12,
  });
  const x = Array.from({ length: 11 }, (_, i) => (i + 1) * 2000);
  return {
    id: RUNS.v1.id,
    name: RUNS.v1.name,
    experimentId: EXPERIMENTS.support.id,
    owner: ML_PEOPLE.trainer,
    tags: ["support", "llama-3.1-8b"],
    state: MlRunState.FAILED,
    badges: [],
    template: TEMPLATE,
    templateRelease: "v1.6.0",
    commit: "77c0de9e1a42",
    createdAt: sft.queuedAt - 2 * MINUTE,
    endedAt: V1_END,
    currentPhase: "sft",
    tokensPerStep: 256 * 8192,
    accelerators: 32,
    product: POOLS.h100.product,
    summary: { "val/loss": 1.978, "train/loss": 1.941 },
    params: { lr: "5e-5", warmup_ratio: "0.03", global_batch: "256", seq_len: "8192", "data.mix": "support-sft v1 1.0" },
    objective: { x, y: x.map((s) => 1.978 + 0.6 * Math.pow(1 + s / 1500, -0.5) - 0.6 * Math.pow(1 + 22000 / 1500, -0.5)) },
    launchedVia: "forge ml run",
    phases: [{ key: "sft", kind: MlPhaseKind.SFT, label: "SFT", state: MlPhaseState.FAILED, pool: POOLS.h100, queueId: QUEUES.research.id, orchestrator: "PyTorchJob", method: "FSDP", totalSteps: 60000, attempts: [sft], accelerators: 32 }],
  };
}

interface SweepDef {
  run: { id: string; name: string };
  trial: number;
  lr: string;
  warmup: string;
  start: number;
  end: number | null;
  endStep: number;
  state: MlRunState;
  endKind: AttemptEnd;
  detail: string;
  final: number;
}

export const SWEEP_TRIALS: readonly SweepDef[] = [
  { run: RUNS.sweep1, trial: 1, lr: "1e-5", warmup: "0.02", start: -30 * HOUR, end: -18.6 * HOUR, endStep: 3000, state: MlRunState.SUCCEEDED, endKind: "completed", detail: "Completed at step 3,000", final: 2.031 },
  { run: RUNS.sweep2, trial: 2, lr: "2e-5", warmup: "0.02", start: -30 * HOUR, end: -18.6 * HOUR, endStep: 3000, state: MlRunState.SUCCEEDED, endKind: "completed", detail: "Completed at step 3,000", final: 1.998 },
  { run: RUNS.sweep3, trial: 3, lr: "4e-5", warmup: "0.03", start: -7 * HOUR, end: null, endStep: 1850, state: MlRunState.RUNNING, endKind: "running", detail: "", final: 2.012 },
  { run: RUNS.sweep4, trial: 4, lr: "8e-5", warmup: "0.03", start: -18.4 * HOUR, end: -14 * HOUR, endStep: 1160, state: MlRunState.CANCELLED, endKind: "rewind", detail: "Cancelled by maya: val loss rising since step 900 (lr 8e-5 too high)", final: 2.214 },
];

const SWEEP_PER_HOUR = 3000 / 11;

function sweepSpec(t: SweepDef): RunSpec {
  const runningAt = t.start + 0.25 * HOUR;
  const a = simpleAttempt({
    phase: "sft",
    queuedAt: t.start,
    runningAt,
    endedAt: t.end,
    endStep: t.endStep,
    end: t.endKind,
    endDetail: t.detail,
    stepsPerHour: SWEEP_PER_HOUR,
    gpus: 8,
    pool: POOLS.a100,
    nodes: [POOLS.a100.nodes[1]],
    checkpointEvery: 500,
    blockingSeconds: 6,
    stallShare: 0.03,
    cancelled: t.endKind === "rewind",
  });
  const x = Array.from({ length: Math.floor(t.endStep / 250) }, (_, i) => (i + 1) * 250);
  const diverge = t.trial === 4;
  const y = x.map((s) => (diverge && s > 900 ? t.final - 0.1 + 0.0004 * (s - 900) : t.final + 0.45 * Math.pow(1 + s / 300, -0.6) - 0.45 * Math.pow(1 + 3000 / 300, -0.6)));
  return {
    id: t.run.id,
    name: t.run.name,
    experimentId: EXPERIMENTS.support.id,
    owner: ML_PEOPLE.trainer,
    sweepId: SWEEP_ID,
    trial: t.trial,
    tags: ["sweep", "proxy"],
    state: t.state,
    badges: [],
    template: TEMPLATE,
    templateRelease: RUNNING_TEMPLATE_RELEASE,
    commit: "c3d9e8f1a2b4",
    createdAt: t.start - MINUTE,
    endedAt: t.end ?? undefined,
    currentPhase: "sft",
    tokensPerStep: 64 * 4096,
    etaSeconds: t.end === null ? ((3000 - t.endStep) / SWEEP_PER_HOUR) * 3600 : undefined,
    accelerators: 8,
    product: POOLS.a100.product,
    summary: { "val/loss": t.endKind === "running" ? y[y.length - 1] : diverge ? Math.min(...y) : t.final },
    params: { lr: t.lr, warmup_ratio: t.warmup, global_batch: "64", seq_len: "4096", steps: "3000" },
    objective: { x, y },
    launchedVia: "forge ml sweep",
    phases: [{ key: "sft", kind: MlPhaseKind.SFT, label: "SFT (proxy)", state: t.endKind === "running" ? MlPhaseState.RUNNING : t.endKind === "completed" ? MlPhaseState.SUCCEEDED : MlPhaseState.CANCELLED, pool: POOLS.a100, queueId: QUEUES.eval.id, orchestrator: "JobSet", method: "FSDP2", totalSteps: 3000, attempts: [a], accelerators: 8 }],
  };
}
/** The trials ran the release that is running now (shopStory: staging and prod run v1.8.3). */
const RUNNING_TEMPLATE_RELEASE = "v1.8.3";

function dpoSpec(): RunSpec {
  const a: AttemptSpec = {
    ...simpleAttempt({ phase: "dpo", queuedAt: -DPO_PENDING.waitingMs, runningAt: 0, endedAt: null, endStep: 0, end: "running", stepsPerHour: 400, gpus: DPO_PENDING.gpus, pool: POOLS.h100, nodes: [] }),
    lastCheckpointStep: 0,
    borrowedGpus: 0,
  };
  return {
    id: RUNS.dpo.id,
    name: RUNS.dpo.name,
    experimentId: EXPERIMENTS.support.id,
    owner: ML_PEOPLE.trainer,
    tags: ["ablation", "dpo"],
    state: MlRunState.QUEUED,
    badges: [],
    template: "support-assistant-dpo",
    templateRelease: RUNNING_TEMPLATE_RELEASE,
    commit: "c3d9e8f1a2b4",
    createdAt: -DPO_PENDING.waitingMs - MINUTE,
    currentPhase: "dpo",
    tokensPerStep: 128 * 4096,
    accelerators: DPO_PENDING.gpus,
    product: POOLS.h100.product,
    summary: {},
    params: { beta: "0.1", lr: "5e-7", global_batch: "128", dataset: "support-prefs v2" },
    objective: { x: [], y: [] },
    launchedVia: "forge ml run",
    phases: [
      { key: "dpo", kind: MlPhaseKind.DPO, label: "DPO", state: MlPhaseState.QUEUED, pool: POOLS.h100, queueId: QUEUES.research.id, orchestrator: "JobSet", method: "DPO", totalSteps: 2400, attempts: [a], accelerators: DPO_PENDING.gpus, input: subj(MlSubjectKind.CHECKPOINT, checkpointId(RUNS.v3.id, "sft", 40000), "v3 sft step 40,000") },
    ],
  };
}

function rerankerSpec(): RunSpec {
  const a = simpleAttempt({ phase: "job", queuedAt: -10.3 * HOUR, runningAt: -10.2 * HOUR, endedAt: -9 * HOUR, endStep: 0, lastCheckpointStep: 0, end: "completed", endDetail: "Job succeeded (exit 0)", stepsPerHour: 1, gpus: 4, pool: POOLS.a100, nodes: [POOLS.a100.nodes[0]], checkpointEvery: 1, blockingSeconds: 0 });
  return {
    id: RUNS.reranker.id,
    name: RUNS.reranker.name,
    experimentId: EXPERIMENTS.reranker.id,
    owner: ML_PEOPLE.notebooks,
    tags: ["nightly"],
    state: MlRunState.SUCCEEDED,
    badges: [MlRunBadge.UNTRACKED],
    template: "",
    templateRelease: "",
    commit: "",
    createdAt: -10.3 * HOUR,
    endedAt: -9 * HOUR,
    currentPhase: "job",
    tokensPerStep: 0,
    accelerators: 4,
    product: POOLS.a100.product,
    summary: {},
    params: {},
    objective: { x: [], y: [] },
    launchedVia: "observed",
    phases: [{ key: "job", kind: MlPhaseKind.CUSTOM, label: "Job (observed)", state: MlPhaseState.SUCCEEDED, pool: POOLS.a100, queueId: QUEUES.eval.id, orchestrator: "PyTorchJob", method: "", totalSteps: 0, attempts: [a], accelerators: 4 }],
  };
}

/** Every run of the world, newest first. */
function allSpecs(): RunSpec[] {
  return [v3Spec(), sweepSpec(SWEEP_TRIALS[2]), dpoSpec(), rerankerSpec(), sweepSpec(SWEEP_TRIALS[3]), sweepSpec(SWEEP_TRIALS[0]), sweepSpec(SWEEP_TRIALS[1]), v2Spec(), v1Spec()];
}

/** Every run, built (the list shape: no ranks). Exported for the tests. */
export function trainingRuns(now: Now, withRanks = false): RunBuilt[] {
  return allSpecs().map((s) => buildRun(s, now, withRanks && s.id === RUNS.v3.id));
}

// ── Checkpoints ───────────────────────────────────────────────────────────────

const CKPT_BYTES = 212_000_000_000;

function v3Checkpoints(now: Now): CheckpointInit[] {
  const out: CheckpointInit[] = [];
  const mk = (a: AttemptSpec, step: number, status: MlCheckpointStatus, over: Partial<CheckpointInit> = {}): CheckpointInit => {
    const j = rng(hash(`ckpt-${a.phase}-${step}`));
    const pretrain = a.phase === "pretrain";
    const id = checkpointId(RUNS.v3.id, a.phase, step);
    const evals = !pretrain && step % 2000 === 0 && step <= V3_EVAL_STEPS[V3_EVAL_STEPS.length - 1];
    return {
      id,
      runId: RUNS.v3.id,
      phaseKey: a.phase,
      attempt: a.n,
      step: BigInt(step),
      kind: "sharded-dcp",
      sizeBytes: BigInt(Math.round(CKPT_BYTES * (1 + 0.004 * (j() - 0.5)))),
      saveSeconds: 38 + 6 * j(),
      uploadSeconds: 132 + 20 * j(),
      async: true,
      blockingSeconds: a.blockingSeconds + 2 * (j() - 0.5),
      metrics: { "val/loss": v3ValLoss(a.phase, step), "train/loss": v3TrainLoss(a.phase, step) },
      evalScores: evals ? Object.fromEntries(EVAL_TASKS.map((t) => [t, evalScore(t as EvalTask, `v3@${step}`)])) : {},
      status,
      uri: `gs://acme-ml-checkpoints/${RUNS.v3.name}/${a.phase}/step-${String(step).padStart(6, "0")}/`,
      digest: `sha256:${hex(id)}`,
      savedAt: at(timeOfStep(a, step), now),
      modelVersionId: "",
      evalRunIds: evals ? [periodicEvalRunId(step), ...(step === 40000 ? [EVAL_RUNS.v3ckpt] : [])] : [],
      ...over,
    };
  };
  for (const a of V3_PRETRAIN) for (const s of durableSteps(a)) out.push(mk(a, s, MlCheckpointStatus.DURABLE));
  // The emergency checkpoint on SIGTERM that did not finish within the grace period.
  out.push(mk(V3_PRETRAIN[0], 6400, MlCheckpointStatus.PARTIAL, { saveSeconds: 118, uploadSeconds: 0, blockingSeconds: 118, savedAt: at(V3_PRETRAIN[0].endedAt! - 118 * SEC, now) }));
  for (const a of V3_SFT) {
    for (const s of durableSteps(a)) {
      // Retention: the last 10 and every 2,000th are kept.
      const kept = s % 2000 === 0 || s > V3_NOW.step - 10_000;
      out.push(mk(a, s, kept ? MlCheckpointStatus.DURABLE : MlCheckpointStatus.DELETED));
    }
  }
  return out.sort((x, y) => Number(y.step ?? 0n) - Number(x.step ?? 0n) || (x.phaseKey === "sft" ? -1 : 1));
}

function v2Checkpoints(now: Now, run: RunBuilt): CheckpointInit[] {
  const sft = run.spec.phases[0].attempts[0];
  const q = run.spec.phases[2].attempts[0];
  const ck = (a: AttemptSpec, step: number, extra: Partial<CheckpointInit>): CheckpointInit => {
    const id = checkpointId(RUNS.v2.id, a.phase, step);
    return {
      id,
      runId: RUNS.v2.id,
      phaseKey: a.phase,
      attempt: 1,
      step: BigInt(step),
      kind: "sharded-dcp",
      sizeBytes: BigInt(CKPT_BYTES),
      saveSeconds: 41,
      uploadSeconds: 146,
      async: true,
      blockingSeconds: 19,
      metrics: {},
      evalScores: {},
      status: MlCheckpointStatus.DURABLE,
      uri: `gs://acme-ml-checkpoints/${RUNS.v2.name}/${a.phase}/step-${String(step).padStart(6, "0")}/`,
      digest: `sha256:${hex(id)}`,
      savedAt: at(timeOfStep(a, step), now),
      modelVersionId: "",
      evalRunIds: [],
      ...extra,
    };
  };
  const out = [10000, 20000, 30000, 40000, 50000].map((s) => ck(sft, s, {}));
  out.push(ck(sft, 60000, { modelVersionId: VERSIONS.v2.id, evalRunIds: [EVAL_RUNS.v2], digest: VERSIONS.v2.digest, evalScores: Object.fromEntries(EVAL_TASKS.map((t) => [t, evalScore(t as EvalTask, "v2")])), metrics: { "val/loss": 1.894 } }));
  out.push(ck(q, 1, { kind: "weights-only", sizeBytes: 8_600_000_000n, saveSeconds: 22, uploadSeconds: 14, blockingSeconds: 0, modelVersionId: VERSIONS.v21fp8.id, evalRunIds: [EVAL_RUNS.v21fp8], digest: VERSIONS.v21fp8.digest, savedAt: at(V2_END, now), evalScores: Object.fromEntries(EVAL_TASKS.map((t) => [t, evalScore(t as EvalTask, "v2.1-fp8")])) }));
  void V21_FP8_CHECKPOINT;
  return out.reverse();
}

function genericCheckpoints(now: Now, run: RunBuilt): CheckpointInit[] {
  return run.spec.phases.flatMap((p) =>
    p.attempts.flatMap((a) =>
      durableSteps(a)
        .slice(-6)
        .map((s) => {
          const id = checkpointId(run.spec.id, a.phase, s);
          return {
            id,
            runId: run.spec.id,
            phaseKey: a.phase,
            attempt: a.n,
            step: BigInt(s),
            kind: "sharded-dcp",
            sizeBytes: BigInt(a.pool === POOLS.a100 ? 96_000_000_000 : 106_000_000_000),
            saveSeconds: 24,
            uploadSeconds: 80,
            async: true,
            blockingSeconds: a.blockingSeconds,
            metrics: {},
            evalScores: {},
            status: MlCheckpointStatus.DURABLE,
            uri: `gs://acme-ml-checkpoints/${run.spec.name}/${a.phase}/step-${String(s).padStart(6, "0")}/`,
            digest: `sha256:${hex(id)}`,
            savedAt: at(timeOfStep(a, s), now),
            modelVersionId: "",
            evalRunIds: [],
          } satisfies CheckpointInit;
        })
        .reverse(),
    ),
  );
}

// ── The run detail ────────────────────────────────────────────────────────────

const v3Subject = subj(MlSubjectKind.RUN, RUNS.v3.id, RUNS.v3.name);
const sftSubject = subj(MlSubjectKind.PHASE, phaseId(RUNS.v3.id, "sft"), "sft");
const sftA3Subject = subj(MlSubjectKind.ATTEMPT, attemptId(RUNS.v3.id, "sft", 3), "sft attempt 3");
const rank37Subject = subj(MlSubjectKind.RANK, rankId(RUNS.v3.id, "sft", 3, STRAGGLER_RANK), `rank ${STRAGGLER_RANK}`);

/** The SFT step-time breakdown now (straggling), in ms, from curves.ts. */
export function v3StepBreakdown() {
  const t0 = v3StepTimeMs("sft", V3_NOW.step, false);
  const t = v3StepTimeMs("sft", V3_NOW.step, true);
  const ckpt = (V3_SFT_NOW.blockingSeconds * 1000) / V3_PHASES.sft.checkpointEvery;
  const fixed: [string, number][] = [
    ["data_wait", 0.03 * t0],
    ["forward", 0.25 * t0],
    ["backward", 0.47 * t0],
    ["optimizer", 0.05 * t0],
  ];
  const used = fixed.reduce((s, [, v]) => s + v, 0) + ckpt;
  const stages: [string, number][] = [...fixed, ["comm_exposed", Math.max(0, t - used)], ["checkpoint", ckpt]];
  return stages.map(([stage, ms]) => ({ stage, msP50: ms, share: ms / t }));
}

function v3Health(): DetailInit["health"] {
  return [
    { key: "straggler", status: "warn", title: `Straggler: rank ${STRAGGLER_RANK} (${HOT_NODE} GPU ${HOT_GPU_INDEX})`, detail: `Compute +18% since step ${V3_STRAGGLER_STEP.toLocaleString("en-US")} (12h ago): HW slowdown (thermal) at 87°C, HBM 95°C. The other 63 ranks wait in allreduce: about 15% of throughput.`, findingId: FINDINGS.straggler, subject: rank37Subject },
    { key: "nan", status: "ok", title: "No NaN or Inf", detail: "Loss, grads and optimizer state finite on every rank since launch.", findingId: "", subject: v3Subject },
    { key: "grad_norm", status: "ok", title: "Grad norm stable", detail: "One spike at step 38,710 (4.4 pre-clip); recovered within 300 steps.", findingId: "", subject: sftSubject },
    { key: "loss_spike", status: "warn", title: "Loss spike at 38,600–39,100", detail: "Traced to data shard 1184 of support-sft v3; recovered without a rewind (rewind-on-spike is opt-in and off).", findingId: "", subject: sftSubject },
    { key: "data_wait", status: "ok", title: "Data loader wait 3%", detail: "12 workers per rank, prefetch 4; no rank above 4%.", findingId: "", subject: sftA3Subject },
    { key: "checkpoint_blocking", status: "warn", title: "Checkpoints block the loop 21 s each", detail: "Async staging waits for the previous upload (140 s) before it copies; move to pinned-memory staging.", findingId: FINDINGS.checkpointBlocking, subject: sftSubject },
    { key: "at_risk", status: "ok", title: "200 steps at risk", detail: "Last durable checkpoint at 41,000 (33 min ago); cadence 1,000 steps (2h 47m).", findingId: "", subject: subj(MlSubjectKind.CHECKPOINT, checkpointId(RUNS.v3.id, "sft", 41000), "sft step 41,000") },
    { key: "eval_idle", status: "warn", title: "eval-periodic holds 8 A100s between evals", detail: "Each eval runs 25 min every ~2h 47m; the job keeps its GPUs in between (93% idle). Launch one Job per checkpoint instead.", findingId: FINDINGS.idleEvalHolding, subject: subj(MlSubjectKind.PHASE, phaseId(RUNS.v3.id, "eval-periodic"), "eval-periodic") },
    { key: "profiling", status: "ok", title: "Profiling counters collected", detail: "DCGM profiling (SM active, tensor active, NVLink) on gke-us-central1-train.", findingId: "", subject: subj(MlSubjectKind.CLUSTER, CLUSTERS.train.id, CLUSTERS.train.name) },
  ];
}

function v3Insights(): DetailInit["insights"] {
  return [
    {
      id: "ins-v3-loss-spike-shard-1184",
      severity: MlSeverity.WARNING,
      title: "Loss spike at 38,600–39,100 traced to data shard 1184",
      detail: "train/loss rose 0.38 and grad norm to 4.4 while shard 1184 of support-sft v3 was read (duplicated, malformed ticket threads). Both recovered by 39,100.",
      phaseKey: "sft",
      fromStep: 38600n,
      toStep: 39100n,
      seriesKeys: ["train/loss", "optim/grad_norm"],
      suggestion: "Skip shard 1184 in the next epoch (data.skip_shards); no rewind needed, the run recovered.",
      findingId: "",
    },
    {
      id: "ins-v3-attn-logit-growth",
      severity: MlSeverity.WARNING,
      title: "Attention logit growth in layers 28–31",
      detail: "Max attention logit in layers 28–31 grew from 31 to 47 over SFT (others stay under 30). Unchecked growth precedes loss divergence.",
      phaseKey: "sft",
      fromStep: 20000n,
      toStep: BigInt(V3_NOW.step),
      seriesKeys: ["model/max_attention_logit"],
      suggestion: "Enable QK-norm or logit soft-capping (50) for the RL phase.",
      findingId: "",
    },
    {
      id: "ins-v3-straggler-throughput",
      severity: MlSeverity.WARNING,
      title: "Rank 37's straggler costs ~15% throughput",
      detail: `Since step ${V3_STRAGGLER_STEP.toLocaleString("en-US")}, every step waits ~18% longer for rank 37 (${HOT_NODE} GPU ${HOT_GPU_INDEX}, thermal slowdown).`,
      phaseKey: "sft",
      fromStep: BigInt(V3_STRAGGLER_STEP),
      toStep: BigInt(V3_NOW.step),
      seriesKeys: ["timers/comm_exposed_ms", "throughput/tokens_per_s"],
      suggestion: `Swap ${HOT_NODE} for the hot spare at the next checkpoint (41,000 → restart costs ~25 min; the straggler costs ~1.6 h a day).`,
      findingId: FINDINGS.straggler,
    },
  ];
}

function v3LayerStats(): Init<typeof MlLayerStatSchema>[] {
  const rows: Init<typeof MlLayerStatSchema>[] = [];
  const push = (layer: string, gn: number, wn: number, act: number, ur: number, logit: number, status = "ok", note = "") =>
    rows.push({ layer, gradNorm: gn, weightNorm: wn, activationRms: act, updateRatio: ur, maxAttentionLogit: logit, status, note });
  push("embed", 0.021, 128.4, 0.98, 2.1e-4, Number.NaN);
  for (const l of [0, 8, 16, 24]) {
    push(`layers.${l}.attn`, 0.031 + l * 0.0008, 41.2 + l * 0.3, 1.1 + l * 0.02, 1.8e-4, 18 + l * 0.4);
    push(`layers.${l}.mlp`, 0.044 + l * 0.0009, 63.5 + l * 0.4, 1.4 + l * 0.03, 1.6e-4, Number.NaN);
  }
  for (const l of [28, 29, 30, 31]) {
    push(`layers.${l}.attn`, 0.071 + (l - 28) * 0.006, 48.9 + (l - 28) * 0.8, 2.3 + (l - 28) * 0.2, 2.4e-4, 41 + (l - 28) * 2, "warn", "max attention logit rising");
  }
  push("layers.31.mlp", 0.081, 74.2, 2.9, 2.2e-4, Number.NaN);
  push("lm_head", 0.052, 131.7, 3.4, 1.9e-4, Number.NaN);
  return rows;
}

function v3Config(): Init<typeof MlRunConfigSchema> {
  const T = MlParamSource.TEMPLATE;
  const L = MlParamSource.LAUNCH;
  const P = MlParamSource.PHASE;
  return {
    params: [
      { key: "model.base", value: BASE_MODEL, source: T },
      { key: "precision", value: "bf16 (fp32 master weights)", source: T },
      { key: "optim", value: "AdamW (β 0.9, 0.95; ε 1e-8)", source: T },
      { key: "weight_decay", value: "0.1", source: T },
      { key: "grad_clip", value: "1.0", source: T },
      { key: "seq_len", value: "8192", source: T },
      { key: "activation_checkpointing", value: "selective (every 2nd layer)", source: T },
      { key: "checkpoint.interval", value: "1000", source: T },
      { key: "lr", value: "2e-5", source: L },
      { key: "lr.schedule", value: "warmup 2% → cosine to 10%", source: T },
      { key: "warmup_ratio", value: "0.02", source: L },
      { key: "global_batch", value: "512", source: L },
      { key: "data.mix", value: "support-sft v3 0.8 / the-stack-v2-dedup 0.2", source: L },
      { key: "lr", value: "3e-4", source: P, phaseKey: "pretrain" },
      { key: "checkpoint.interval", value: "2000", source: P, phaseKey: "pretrain" },
      { key: "data.mix", value: "support-corpus v4 1.0", source: P, phaseKey: "pretrain" },
      { key: "eval.suite", value: "release-gate-lite v2 every 2000 steps", source: P, phaseKey: "eval-periodic" },
      { key: "rl.method", value: "GRPO", source: P, phaseKey: "rl" },
      { key: "rl.group_size", value: "16", source: P, phaseKey: "rl" },
      { key: "rl.kl_coef", value: "0.04", source: P, phaseKey: "rl" },
    ],
    parallelism: { data: 8, tensor: 8, pipeline: 1, context: 1, expert: 1, zeroStage: 3, microBatch: 8, globalBatch: 512, gradAccumulation: 8, framework: "torchtitan (FSDP2 + TP)", meshOrder: "dp,tp", worldSize: 64, gpusPerNode: 8 },
    environment: [
      { label: "Image", value: `${IMAGE_REPO}:${TEMPLATE_RELEASE}` },
      { label: "CUDA", value: "12.4" },
      { label: "Driver", value: POOLS.h100.driver },
      { label: "NCCL", value: "2.21.5" },
      { label: "PyTorch", value: "2.5.1" },
      { label: "torchtitan", value: "0.0.2+4e7a9c2" },
      { label: "NCCL_NET", value: "GPUDirectTCPX_v7" },
      { label: "NCCL_GPUDIRECTTCPX_SOCKET_IFNAME", value: "eth1,eth2,eth3,eth4" },
      { label: "NCCL_NVLS_ENABLE", value: "1" },
      { label: "TORCH_NCCL_ASYNC_ERROR_HANDLING", value: "3" },
      { label: "TORCH_NCCL_TRACE_BUFFER_SIZE", value: "20000 (flight recorder)" },
      { label: "TORCH_NCCL_DUMP_ON_TIMEOUT", value: "1" },
      { label: "NCCL timeout", value: "600 s (default)" },
    ],
    hardware: [
      { label: "Accelerators", value: `64 × ${POOLS.h100.product}` },
      { label: "Nodes", value: `8 × ${POOLS.h100.instanceType} (${POOLS.h100.zone})` },
      { label: "Pool", value: `${POOLS.h100.name} + hot spare ${POOLS.spare.name}` },
      { label: "Intra-node", value: "NVLink 4 / NVSwitch, 900 GB/s" },
      { label: "Inter-node", value: "GPUDirect-TCPX, 4 × 200 Gbps per node" },
      { label: "Placement", value: "compact placement policy (block)" },
      { label: "Cluster", value: CLUSTERS.train.name },
    ],
    datasets: [
      { id: DATASETS.domain.id, name: DATASETS.domain.name, version: DATASETS.domain.version, digest: `sha256:${hex(DATASETS.domain.id)}`, tokens: BigInt(DATASETS.domain.tokens), mixtureWeight: 1, uri: "gs://acme-ml-data/support-corpus/v4/" },
      { id: DATASETS.sft.id, name: DATASETS.sft.name, version: DATASETS.sft.version, digest: `sha256:${hex(DATASETS.sft.id)}`, tokens: BigInt(DATASETS.sft.tokens), mixtureWeight: 0.8, uri: "gs://acme-ml-data/support-sft/v3/" },
      { id: DATASETS.code.id, name: DATASETS.code.name, version: DATASETS.code.version, digest: `sha256:${hex(DATASETS.code.id)}`, tokens: BigInt(DATASETS.code.tokens), mixtureWeight: 0.2, uri: "gs://acme-ml-data/the-stack-v2-dedup/2024-04/" },
    ],
    code: {
      repository: "github.com/acme/shop",
      commit: COMMIT,
      commitMessage: "train: packed 8k sequences, FSDP2 + TP mesh for support-assistant",
      release: TEMPLATE_RELEASE,
      imageDigest: IMAGE_DIGEST,
      diffSummary: "vs v2's commit: torchtitan replaces the FSDP1 trainer; packing on; LR 3e-5 → 2e-5; code mix 15% → 20%; continued pre-training phase added.",
      previousCommit: PREV_COMMIT,
    },
  };
}

function hookPayload(eventType: string, extra: Record<string, unknown>): string {
  return JSON.stringify({ type: eventType, run_id: RUNS.v3.id, run: RUNS.v3.name, experiment: EXPERIMENTS.support.name, ...extra }, null, 2);
}

function v3Hooks(now: Now): DetailInit["hooks"] {
  const xidAt = V3_SFT[1].endedAt!;
  const lastCkpt = timeOfStep(V3_SFT_NOW, 41000);
  const spikeAt = timeOfStep(V3_SFT_NOW, 38650);
  const evalAt = timeOfStep(V3_SFT_NOW, 40000) + 28 * MINUTE;
  const hooks: Init<typeof MlHookSchema>[] = [
    {
      eventType: "ml.attempt.failed",
      description: "An attempt ended for any reason other than completion: preemption, a GPU error, an NCCL timeout, OOM, a code error.",
      emitted7d: 3,
      lastEmittedAt: at(xidAt, now),
      targets: [
        { kind: "workflow", name: "triage-training-failure", lastResult: "succeeded", lastRunAt: at(xidAt + 2 * MINUTE, now), declaredIn: ".reliant/workflows/triage-training-failure.yaml" },
        { kind: "slack", name: "#ml-oncall", lastResult: "succeeded", lastRunAt: at(xidAt + 5 * SEC, now), declaredIn: "forge.yaml (ml.notify)" },
      ],
      examplePayload: hookPayload("ml.attempt.failed", { phase: "sft", attempt: 2, step: 32750, end_reason: "GPU_ERROR", end_detail: `Xid 79 on ${BAD_NODE} GPU ${BAD_GPU_INDEX}`, last_checkpoint_step: 32000, steps_lost: 750, node: BAD_NODE, device_uuid: gpuUuid(BAD_NODE, BAD_GPU_INDEX) }),
    },
    {
      eventType: "ml.checkpoint.saved",
      description: "A checkpoint became durable (uploaded and verified).",
      emitted7d: 47,
      lastEmittedAt: at(lastCkpt + 3 * MINUTE, now),
      targets: [{ kind: "workflow", name: "eval-on-checkpoint", lastResult: "succeeded", lastRunAt: at(timeOfStep(V3_SFT_NOW, 40000) + 3 * MINUTE, now), declaredIn: ".reliant/workflows/eval-on-checkpoint.yaml (every 2,000 steps)" }],
      examplePayload: hookPayload("ml.checkpoint.saved", { phase: "sft", attempt: 3, step: 41000, checkpoint_id: checkpointId(RUNS.v3.id, "sft", 41000), uri: `gs://acme-ml-checkpoints/${RUNS.v3.name}/sft/step-041000/`, size_bytes: CKPT_BYTES, val_loss: Number(v3ValLoss("sft", 41000).toFixed(4)) }),
    },
    {
      eventType: "ml.loss.spike",
      description: "train/loss z-score above 6 for 3 steps (the loss-spike detector).",
      emitted7d: 1,
      lastEmittedAt: at(spikeAt, now),
      targets: [{ kind: "workflow", name: "rewind-on-spike", lastResult: "succeeded", lastRunAt: at(spikeAt + 40 * MINUTE, now), declaredIn: ".reliant/workflows/rewind-on-spike.yaml (opt-in: skipped, the run recovered)" }],
      examplePayload: hookPayload("ml.loss.spike", { phase: "sft", attempt: 3, step: 38650, z_score: 7.4, loss: 2.21, data_shard: 1184, last_good_checkpoint: checkpointId(RUNS.v3.id, "sft", 38000) }),
    },
    {
      eventType: "ml.phase.succeeded",
      description: "A phase completed; the next phase in the DAG may start.",
      emitted7d: 1,
      lastEmittedAt: at(V3_PRETRAIN[1].endedAt!, now),
      targets: [{ kind: "workflow", name: "launch-next-phase", lastResult: "succeeded", lastRunAt: at(V3_PRETRAIN[1].endedAt! + MINUTE, now), declaredIn: ".reliant/workflows/launch-next-phase.yaml" }],
      examplePayload: hookPayload("ml.phase.succeeded", { phase: "pretrain", attempt: 2, step: 20000, checkpoint_id: checkpointId(RUNS.v3.id, "pretrain", 20000), next_phases: ["sft"] }),
    },
    {
      eventType: "ml.straggler.detected",
      description: "A rank's step time stayed more than 10% above the median for 200 steps.",
      emitted7d: 1,
      lastEmittedAt: at(STRAGGLER_SINCE, now),
      targets: [{ kind: "webhook", name: "https://hooks.acme.dev/ml/stragglers", lastResult: "succeeded", lastRunAt: at(STRAGGLER_SINCE + 2 * SEC, now), declaredIn: "deploy/kcl/prod/ml.k (ml.webhooks)" }],
      examplePayload: hookPayload("ml.straggler.detected", { phase: "sft", attempt: 3, step: V3_STRAGGLER_STEP, rank: STRAGGLER_RANK, local_rank: 5, node: HOT_NODE, device_uuid: gpuUuid(HOT_NODE, HOT_GPU_INDEX), compute_ms_excess_pct: 18, reason: "HW slowdown (thermal)" }),
    },
    {
      eventType: "ml.eval.regressed",
      description: "An evaluated checkpoint scored significantly below the production model on a gated task.",
      emitted7d: 1,
      lastEmittedAt: at(evalAt, now),
      targets: [{ kind: "workflow", name: "open-regression-issue", lastResult: "succeeded", lastRunAt: at(evalAt + MINUTE, now), declaredIn: ".reliant/workflows/open-regression-issue.yaml" }],
      examplePayload: hookPayload("ml.eval.regressed", { phase: "eval-periodic", step: 40000, checkpoint_id: checkpointId(RUNS.v3.id, "sft", 40000), eval_run_id: EVAL_RUNS.v3ckpt, task: "gsm8k", score: Number(evalScore("gsm8k", "v3@40000").toFixed(3)), baseline: "v2", baseline_score: Number(evalScore("gsm8k", "v2").toFixed(3)) }),
    },
  ];
  return hooks;
}

function v3Tokens(): DetailInit["tokens"] {
  const seen = V3_NOW.step * V3_TOKENS_PER_STEP;
  const tps = v3TokensPerSecond("sft", V3_NOW.step, true);
  return {
    tokensSeen: BigInt(seen),
    tokensPlanned: BigInt(V3_PHASES.sft.totalSteps * V3_TOKENS_PER_STEP),
    tokensPerSecond: tps,
    tokensPerSecondPerGpu: tps / V3_NOW.worldSize,
    paddingRatio: 0.031,
    sequenceLength: 8192,
    bySource: [
      { name: `${DATASETS.sft.name} ${DATASETS.sft.version}`, value: seen * 0.8, share: 0.8 },
      { name: `${DATASETS.code.name} ${DATASETS.code.version}`, value: seen * 0.2, share: 0.2 },
    ],
  };
}

function v3KeyNumbers(): DetailInit["keyNumbers"] {
  const best = v3BestValLoss();
  const bd = v3StepBreakdown();
  const comm = bd.find((s) => s.stage === "comm_exposed")!;
  return [
    { label: "Best val loss", value: `${best.value.toFixed(3)} @ ${(best.step / 1000).toFixed(1)}k` },
    { label: "Last eval (40k) MMLU-Pro", value: `${(evalScore("mmlu_pro", "v3@40000") * 100).toFixed(1)} (${evalScore("mmlu_pro", "v3@40000") >= evalScore("mmlu_pro", "v2") ? "+" : ""}${((evalScore("mmlu_pro", "v3@40000") - evalScore("mmlu_pro", "v2")) * 100).toFixed(1)} vs v2)` },
    { label: "MFU", value: `${(v3Mfu("sft", V3_NOW.step, true) * 100).toFixed(0)}% (${(v3Mfu("sft", V3_NOW.step, false) * 100).toFixed(0)}% before the straggler)` },
    { label: "Step time p50", value: `${(v3StepTimeMs("sft", V3_NOW.step, true) / 1000).toFixed(1)} s` },
    { label: "Comm exposed", value: `${(comm.share * 100).toFixed(0)}% of the step` },
    { label: "Tokens/s", value: `${Math.round(v3TokensPerSecond("sft", V3_NOW.step, true) / 1000)}k (${Math.round(v3TokensPerSecond("sft", V3_NOW.step, true) / 64).toLocaleString("en-US")}/GPU)` },
  ];
}

function runCost(run: RunBuilt, experimentMedianUsd: number, projectedUsd: number): DetailInit["cost"] {
  const cells: Init<typeof MlCostCellSchema>[] = [];
  run.spec.phases.forEach((p, pi) => {
    if (p.custom) {
      const built = run.phases[pi];
      for (const [c, v] of Object.entries(built.parts)) if (v) cells.push({ phaseKey: p.key, attempt: 1, category: Number(c) as MlCostCategory, gpuHours: v, usd: v * p.pool.usdPerGpuHour });
      return;
    }
    p.attempts.forEach((a, i) => {
      if (a.runningAt === 0 && a.endedAt === null) return;
      for (const [c, v] of Object.entries(attemptCostParts(a, p.attempts[i - 1]))) if (v) cells.push({ phaseKey: p.key, attempt: a.n, category: Number(c) as MlCostCategory, gpuHours: v, usd: v * a.pool.usdPerGpuHour });
    });
  });
  const usd = cells.reduce((s, c) => s + (c.usd ?? 0), 0);
  const tokens = run.spec.phases.reduce((s, p) => s + (p.attempts[p.attempts.length - 1]?.endStep ?? 0) * run.spec.tokensPerStep, 0);
  return { cells, usdPerBillionTokens: tokens > 0 ? usd / (tokens / 1e9) : Number.NaN, projectedUsdToFinish: projectedUsd, experimentMedianUsd: experimentMedianUsd };
}

function median(xs: number[]): number {
  const s = [...xs].sort((a, b) => a - b);
  if (!s.length) return Number.NaN;
  const m = Math.floor(s.length / 2);
  return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2;
}

/** Projected spend to finish v3: the rest of SFT at the current rate, then RL (GRPO ~30 s/step with rollouts). */
export function v3ProjectedUsd(): number {
  const sftHours = (V3_PHASES.sft.totalSteps - V3_NOW.step) / V3_SFT_PER_HOUR;
  const rlHours = (V3_PHASES.rl.totalSteps * 30) / 3600;
  return (sftHours + rlHours) * V3_NOW.worldSize * POOLS.h100.usdPerGpuHour;
}

function experimentOf(id: string, runs: RunBuilt[], now: Now): Init<typeof MlExperimentSchema> | undefined {
  const def = Object.values(EXPERIMENTS).find((e) => e.id === id);
  if (!def) return undefined;
  const mine = runs.filter((r) => r.spec.experimentId === id);
  let parts: CostParts = {};
  let usdParts: CostParts = {};
  for (const r of mine) for (const p of r.phases) {
    parts = addParts(parts, p.parts);
    usdParts = addParts(usdParts, p.usdParts);
  }
  const scored = mine.filter((r) => Number.isFinite(r.spec.summary[def.objectiveKey]));
  const best = scored.sort((a, b) => (def.lowerIsBetter ? a.spec.summary[def.objectiveKey] - b.spec.summary[def.objectiveKey] : b.spec.summary[def.objectiveKey] - a.spec.summary[def.objectiveKey]))[0];
  const active = mine.filter((r) => [MlRunState.RUNNING, MlRunState.DEGRADED, MlRunState.QUEUED].includes(r.spec.state));
  const last = Math.max(...mine.map((r) => r.spec.endedAt ?? 0));
  return {
    id: def.id,
    name: def.name,
    project: ML_PROJECT,
    owner: id === EXPERIMENTS.reranker.id ? ML_PEOPLE.notebooks : ML_PEOPLE.trainer,
    description: id === EXPERIMENTS.reranker.id ? "The bge reranker's nightly retrain, observed from the cluster (not tracked)." : "The support assistant on Llama 3.1 8B: continued pre-training on support tickets, SFT, then GRPO.",
    runCount: mine.length,
    activeRuns: active.length,
    objectiveKey: def.objectiveKey,
    objectiveLowerIsBetter: def.lowerIsBetter,
    bestRunId: best?.spec.id ?? "",
    bestObjective: best ? best.spec.summary[def.objectiveKey] : Number.NaN,
    lastActivityAt: at(active.length ? 0 : last, now),
    cost: mkCost(parts, 0, usdParts),
  };
}

function detailOf(run: RunBuilt, runs: RunBuilt[], now: Now): DetailInit {
  const exp = experimentOf(run.spec.experimentId, runs, now);
  // The experiment's median full run (sweep trials are proxies, not runs to compare spend with).
  const peers = runs.filter((r) => r.spec.experimentId === run.spec.experimentId && r.spec.id !== run.spec.id && !r.spec.sweepId && (r.init.cost?.usd ?? 0) > 0).map((r) => r.init.cost!.usd!);
  const med = median(peers);
  if (run.spec.id === RUNS.v3.id) {
    return {
      run: run.init,
      checkpoints: v3Checkpoints(now),
      health: v3Health(),
      stepBreakdown: v3StepBreakdown(),
      config: v3Config(),
      cost: runCost(run, med, v3ProjectedUsd()),
      keyNumbers: v3KeyNumbers(),
      experiment: exp,
      findingIds: [FINDINGS.straggler, FINDINGS.checkpointBlocking, FINDINGS.thermal, FINDINGS.ncclTimeout, FINDINGS.idleEvalHolding],
      modelVersionIds: [],
      insights: v3Insights(),
      layerStats: v3LayerStats(),
      tokens: v3Tokens(),
      hooks: v3Hooks(now),
      spares: [
        { label: "Hot spare pool", value: `${POOLS.spare.name}: 1 node, 8 × H100` },
        { label: "Spare node", value: SPARE_NODE },
        { label: "Used", value: `once: sft attempt 3, replacing ${BAD_NODE} (Xid 79)` },
        { label: "Available now", value: "0 nodes (s01 is in the run)" },
        { label: "Swap policy", value: "alert-xid-fatal: cordon + swap + new attempt" },
      ],
    };
  }
  const isV2 = run.spec.id === RUNS.v2.id;
  const untracked = run.spec.badges.includes(MlRunBadge.UNTRACKED);
  const health: DetailInit["health"] = untracked
    ? [{ key: "tracking", status: "not-collected", title: "Not tracked", detail: "Observed from the cluster: no step, loss or throughput. Add the Reliant ML SDK callback to track it.", findingId: FINDINGS.trainingUntracked, subject: subj(MlSubjectKind.RUN, run.spec.id, run.spec.name) }]
    : run.spec.id === RUNS.v1.id
      ? [{ key: "dataloader", status: "fail", title: "DataLoader crashed: /dev/shm 64Mi", detail: "Bus error in DataLoader worker 3: the pod's /dev/shm is the container default 64 MiB; 12 workers with prefetch 4 need ~9 GiB.", findingId: FINDINGS.dataloaderShm, subject: subj(MlSubjectKind.ATTEMPT, attemptId(run.spec.id, "sft", 1), "sft attempt 1") }]
      : [{ key: "nan", status: "ok", title: "No NaN or Inf", detail: "", findingId: "", subject: subj(MlSubjectKind.RUN, run.spec.id, run.spec.name) }];
  return {
    run: run.init,
    checkpoints: isV2 ? v2Checkpoints(now, run) : genericCheckpoints(now, run),
    health,
    stepBreakdown: untracked || run.spec.state === MlRunState.QUEUED ? [] : [
      { stage: "data_wait", msP50: run.spec.id === RUNS.v1.id ? 1400 : 300, share: run.spec.id === RUNS.v1.id ? 0.12 : 0.03 },
      { stage: "forward", msP50: 2500, share: 0.25 },
      { stage: "backward", msP50: 4700, share: 0.47 },
      { stage: "optimizer", msP50: 500, share: 0.05 },
      { stage: "comm_exposed", msP50: 1900, share: 0.19 },
      { stage: "checkpoint", msP50: 10, share: 0.001 },
    ],
    config: untracked
      ? { params: [], environment: [{ label: "Image", value: "us-docker.pkg.dev/acme-ml/jobs/reranker-train:nightly" }], hardware: [{ label: "Accelerators", value: `4 × ${POOLS.a100.product}` }], datasets: [] }
      : {
          params: Object.entries(run.spec.params).map(([key, value]) => ({ key, value, source: MlParamSource.LAUNCH })),
          parallelism: { data: run.spec.accelerators, tensor: 1, pipeline: 1, context: 1, expert: 1, zeroStage: 3, microBatch: 4, globalBatch: Number(run.spec.params.global_batch ?? 0), gradAccumulation: 1, framework: run.spec.id === RUNS.v1.id ? "PyTorch FSDP1" : "torchtitan (FSDP2)", meshOrder: "dp", worldSize: run.spec.accelerators, gpusPerNode: 8 },
          environment: [
            { label: "CUDA", value: "12.4" },
            { label: "NCCL", value: isV2 ? "2.20.5" : "2.21.5" },
            { label: "PyTorch", value: isV2 ? "2.4.1" : "2.5.1" },
          ],
          hardware: [{ label: "Accelerators", value: `${run.spec.accelerators} × ${run.spec.product}` }],
          datasets: [{ id: DATASETS.sft.id, name: DATASETS.sft.name, version: isV2 ? "v2" : DATASETS.sft.version, digest: `sha256:${hex(`${DATASETS.sft.id}${run.spec.id}`)}`, tokens: BigInt(DATASETS.sft.tokens), mixtureWeight: 1, uri: "" }],
          code: { repository: "github.com/acme/shop", commit: run.spec.commit, release: run.spec.templateRelease, imageDigest: isV2 ? IMAGE_DIGEST_V2 : IMAGE_DIGEST, commitMessage: "", diffSummary: "", previousCommit: "" },
        },
    cost: runCost(run, med, run.spec.etaSeconds ? (run.spec.etaSeconds / 3600) * run.spec.accelerators * run.spec.phases[0].pool.usdPerGpuHour : 0),
    keyNumbers: Object.entries(run.spec.summary).map(([k, v]) => ({ label: k, value: k === "mfu" ? `${(v * 100).toFixed(0)}%` : v.toFixed(3) })),
    experiment: exp,
    findingIds: untracked ? [FINDINGS.trainingUntracked] : run.spec.id === RUNS.v1.id ? [FINDINGS.dataloaderShm] : [],
    modelVersionIds: isV2 ? [VERSIONS.v2.id, VERSIONS.v21fp8.id] : [],
    insights: [],
    layerStats: [],
    hooks: [],
    spares: [],
  };
}

// ── Events ────────────────────────────────────────────────────────────────────

interface Tag {
  runId: string;
  phase?: string;
  attempt?: number;
  rank?: number;
  checkpoint?: string;
  /** Shown on the org's (and the experiment's) timeline. */
  notable?: boolean;
}

interface TaggedEvent {
  tag: Tag;
  /** Offset ms. */
  t: number;
  end?: number;
  ev: Omit<EventInit, "at" | "endAt">;
}

const attemptSubject = (runId: string, phase: string, n: number) => subj(MlSubjectKind.ATTEMPT, attemptId(runId, phase, n), `${phase} attempt ${n}`);
const workloadSubject = (name: string) => subj(MlSubjectKind.WORKLOAD, name, name);
const fmtN = (n: number) => n.toLocaleString("en-US");

function v3Events(): TaggedEvent[] {
  const run = RUNS.v3;
  const out: TaggedEvent[] = [];
  const runS = subj(MlSubjectKind.RUN, run.id, run.name);
  let seq = 0;
  const id = (k: string) => `ev-v3-${k}-${seq++}`;
  const add = (tag: Omit<Tag, "runId">, t: number, ev: Omit<EventInit, "at" | "endAt" | "id"> & { id?: string }, end?: number) => out.push({ tag: { runId: run.id, ...tag }, t, end, ev: { id: ev.id ?? id(String(ev.kind)), ...ev } });

  add({ notable: true }, V3_PRETRAIN[0].queuedAt - MINUTE, { kind: MlEventKind.LAUNCH, severity: MlSeverity.INFO, title: `maya launched ${run.name}`, detail: `forge ml run support-assistant-train@${TEMPLATE_RELEASE}: pretrain → sft → rl, 64 × H100`, subject: runS, source: "person", step: 0n });

  V3_SPECS.forEach((a, i) => {
    const prev = V3_SPECS[i - 1]?.phase === a.phase ? V3_SPECS[i - 1] : undefined;
    const aS = attemptSubject(run.id, a.phase, a.n);
    const wl = workloadName(run.name, a.phase, a.n);
    const w = preRunWindows(a, prev);
    const tag = { phase: a.phase, attempt: a.n };
    if (a.n === 1 && a.phase === "sft") add({ ...tag, notable: true }, a.queuedAt, { kind: MlEventKind.LAUNCH, severity: MlSeverity.INFO, title: "launch-next-phase started sft", detail: "Triggered by ml.phase.succeeded (pretrain)", subject: subj(MlSubjectKind.PHASE, phaseId(run.id, "sft"), "sft"), related: [runS], source: "forge", step: 0n });
    add(tag, w.admitted, { kind: MlEventKind.QUEUE_ADMITTED, severity: MlSeverity.INFO, title: `Kueue admitted ${wl}`, detail: `team-research (cohort ml-h100), 64 × h100, borrowing 16${a.n > 1 && a.end !== "running" ? "" : ""}${prev && a.recovery && !a.recovery.holding ? ` after ${(stageSeconds(a.recovery, "reschedule") / 60).toFixed(0)} min` : ""}`, subject: aS, related: [workloadSubject(wl), runS], source: "kueue", step: BigInt(a.startStep) });
    if (a.n === 1) add(tag, w.admitted + 20 * SEC, { kind: MlEventKind.IMAGE_PULL, severity: MlSeverity.INFO, title: "Pulled the training image on 8 nodes", detail: `${IMAGE_REPO}:${TEMPLATE_RELEASE} (18.4 GB) in 212 s`, subject: aS, related: [runS], source: "kubelet", step: BigInt(a.startStep), attributes: { bytes: "18400000000", seconds: "212" } });
    add(tag, a.runningAt - 2 * SEC, { kind: MlEventKind.POD_STARTED, severity: MlSeverity.INFO, title: `8 pods running (${a.nodes.length} nodes)`, detail: a.nodes.join(", "), subject: aS, related: [runS], source: "kubelet", step: BigInt(a.startStep) });
    // Checkpoints: the resume points and the last two of each attempt.
    const steps = durableSteps(a);
    for (const s of steps.filter((s, k) => k >= steps.length - 2 || [6000, 12000, 20000, 32000].includes(s))) {
      const cid = checkpointId(run.id, a.phase, s);
      add({ ...tag, checkpoint: cid }, timeOfStep(a, s) + 3 * MINUTE, { kind: MlEventKind.CHECKPOINT, severity: MlSeverity.INFO, title: `Checkpoint ${fmtN(s)} durable`, detail: "212 GB sharded DCP, saved in 41 s (async), uploaded in 140 s", subject: subj(MlSubjectKind.CHECKPOINT, cid, `${a.phase} step ${fmtN(s)}`), related: [aS], source: "trainer", step: BigInt(s), hookEventType: "ml.checkpoint.saved" });
    }
    if (a.end === "preempted_by_queue") {
      const t = a.endedAt!;
      add({ ...tag, checkpoint: checkpointId(run.id, a.phase, 6400) }, t - 118 * SEC, { kind: MlEventKind.CHECKPOINT, severity: MlSeverity.WARNING, title: "Emergency checkpoint 6,400 partial", detail: "SIGTERM checkpoint did not finish within the 120 s grace period; resume falls back to 6,000", subject: subj(MlSubjectKind.CHECKPOINT, checkpointId(run.id, a.phase, 6400), "pretrain step 6,400"), related: [aS], source: "trainer", step: 6400n });
      add({ ...tag, notable: true }, t, { id: `ev-${run.id}-${a.phase}-${a.n}-end`, kind: MlEventKind.QUEUE_EVICTED, severity: MlSeverity.WARNING, title: `Kueue evicted ${wl}: Preempted`, detail: a.endDetail, subject: aS, related: [workloadSubject(wl), workloadSubject(PREEMPTOR_WORKLOAD), runS, findingSubject(FINDINGS.preemptionChurn)], source: "kueue", step: BigInt(a.endStep), hookEventType: "ml.attempt.failed", attributes: { reason: "InCohortReclamation", preemptor: PREEMPTOR_WORKLOAD } });
      add(tag, t + 2 * SEC, { kind: MlEventKind.PREEMPTION, severity: MlSeverity.WARNING, title: `Preempted by ${PREEMPTOR_WORKLOAD}`, detail: "team-serving-batch reclaimed the 16 H100s team-research had borrowed in cohort ml-h100; 400 steps lost", subject: aS, related: [workloadSubject(PREEMPTOR_WORKLOAD), runS], source: "kueue", step: BigInt(a.endStep) });
    }
    if (a.end === "completed") add({ ...tag, notable: true }, a.endedAt!, { id: `ev-${run.id}-${a.phase}-${a.n}-end`, kind: MlEventKind.COMPLETED, severity: MlSeverity.INFO, title: `${a.phase} completed at step ${fmtN(a.endStep)}`, detail: "Phase succeeded; launch-next-phase subscribed", subject: subj(MlSubjectKind.PHASE, phaseId(run.id, a.phase), a.phase), related: [aS, runS], source: "trainer", step: BigInt(a.endStep), hookEventType: "ml.phase.succeeded" });
    if (a.end === "nccl_timeout") {
      const t = a.endedAt!;
      add({ ...tag }, t - 600 * SEC, { kind: MlEventKind.HANG, severity: MlSeverity.WARNING, title: "Step stalled at 12,400", detail: "No step for 120 s (alert-step-stall pending); allreduce seq 88213 in flight", subject: aS, related: [runS], source: "trainer", step: 12400n }, t);
      add({ ...tag, rank: STRAGGLER_RANK, notable: true }, t, { id: `ev-${run.id}-${a.phase}-${a.n}-end`, kind: MlEventKind.NCCL_TIMEOUT, severity: MlSeverity.CRITICAL, title: "NCCL watchdog: allreduce seq 88213 timed out", detail: a.endDetail, subject: aS, related: [subj(MlSubjectKind.RANK, rankId(run.id, a.phase, a.n, STRAGGLER_RANK), `rank ${STRAGGLER_RANK}`), nodeSubject(HOT_NODE), runS, findingSubject(FINDINGS.ncclTimeout)], source: "trainer", step: BigInt(a.endStep), hookEventType: "ml.attempt.failed", attributes: { seq: "88213", timeout_s: "600", missing_rank: "37" } });
      add(tag, t + 74 * SEC, { kind: MlEventKind.PROFILE_CAPTURE, severity: MlSeverity.INFO, title: "NCCL flight recorder dumped (64 ranks)", detail: "alert-nccl-hang captured the flight recorder before restarting", subject: aS, related: [runS], source: "forge", step: BigInt(a.endStep), attributes: { profile_id: PROFILES.flightRecorder } });
    }
    if (a.end === "gpu_error") {
      const t = a.endedAt!;
      const dev = subj(MlSubjectKind.DEVICE, gpuUuid(BAD_NODE, BAD_GPU_INDEX), `${BAD_NODE} GPU ${BAD_GPU_INDEX}`);
      add({ ...tag, notable: true }, t - 41 * SEC, { kind: MlEventKind.XID, severity: MlSeverity.CRITICAL, title: "Xid 79: GPU has fallen off the bus", detail: `${BAD_NODE} GPU ${BAD_GPU_INDEX} (rank ${2 * 8 + BAD_GPU_INDEX}) at step 32,750`, subject: dev, related: [runS, aS, nodeSubject(BAD_NODE), findingSubject(FINDINGS.xid79)], source: "dcgm", step: BigInt(a.endStep), attributes: { xid: "79" } });
      add({ ...tag }, t, { id: `ev-${run.id}-${a.phase}-${a.n}-end`, kind: MlEventKind.RESTART, severity: MlSeverity.CRITICAL, title: "sft attempt 2 failed: GPU error", detail: "750 steps since checkpoint 32,000 lost; alert-xid-fatal cordoning the node", subject: aS, related: [dev, runS], source: "forge", step: BigInt(a.endStep), hookEventType: "ml.attempt.failed" });
    }
    if (a.recovery && prev) {
      add({ ...tag }, a.queuedAt + MINUTE, { kind: MlEventKind.RESTART, severity: MlSeverity.WARNING, title: `Restarting as ${a.phase} attempt ${a.n}`, detail: a.recovery.remediation, subject: aS, related: [runS, ...(a.recovery.spare ? [nodeSubject(SPARE_NODE), nodeSubject(BAD_NODE)] : [])], source: a.recovery.automatic ? "forge" : "kueue", step: BigInt(a.startStep), attributes: a.recovery.alertId ? { alert_id: a.recovery.alertId } : {} });
    }
  });

  // sft attempt 3's story.
  const a3 = V3_SFT_NOW;
  const t3 = { phase: "sft", attempt: 3 };
  const hotDev = subj(MlSubjectKind.DEVICE, gpuUuid(HOT_NODE, HOT_GPU_INDEX), `${HOT_NODE} GPU ${HOT_GPU_INDEX}`);
  add(t3, STRAGGLER_SINCE - 6 * MINUTE, { kind: MlEventKind.THROTTLE, severity: MlSeverity.WARNING, title: "HW slowdown (thermal) on GPU 5", detail: "87°C core, HBM 95°C; SM clock 1,980 → 1,410 MHz", subject: hotDev, related: [runS, nodeSubject(HOT_NODE), findingSubject(FINDINGS.thermal)], source: "dcgm", step: BigInt(V3_STRAGGLER_STEP - 40) });
  add({ ...t3, rank: STRAGGLER_RANK, notable: true }, STRAGGLER_SINCE, { kind: MlEventKind.STRAGGLER, severity: MlSeverity.WARNING, title: "Straggler detected: rank 37", detail: "Compute +18% vs the median for 200 steps; others wait in allreduce", subject: rank37Subject, related: [runS, hotDev, nodeSubject(HOT_NODE), findingSubject(FINDINGS.straggler)], source: "forge", step: BigInt(V3_STRAGGLER_STEP), hookEventType: "ml.straggler.detected" });
  add({ ...t3, notable: true }, timeOfStep(a3, 38650), { kind: MlEventKind.LOSS_SPIKE, severity: MlSeverity.WARNING, title: "Loss spike: z-score 7.4", detail: "train/loss 2.21 at step 38,650 (data shard 1184); recovered by 39,100, no rewind", subject: sftSubject, related: [runS, sftA3Subject], source: "trainer", step: 38650n, hookEventType: "ml.loss.spike" }, timeOfStep(a3, 39100));
  add(t3, timeOfStep(a3, 40800), { kind: MlEventKind.PROFILE_CAPTURE, severity: MlSeverity.INFO, title: "maya captured a Kineto trace (steps 40,800–40,803)", detail: "8 ranks, 4 steps", subject: sftA3Subject, related: [runS], source: "person", step: 40800n, attributes: { profile_id: PROFILES.kineto } });
  add(t3, timeOfStep(a3, 41000) + 10 * MINUTE, { kind: MlEventKind.PROFILE_CAPTURE, severity: MlSeverity.INFO, title: "Nsight Systems capture (step 41,000)", detail: "rank 37 and rank 36 for comparison", subject: sftA3Subject, related: [runS, rank37Subject], source: "person", step: 41000n, attributes: { profile_id: PROFILES.nsight } });

  // Evals of eval-periodic, one per checkpoint.
  for (const s of V3_EVAL_STEPS) {
    const a = attemptOfStep(V3_SFT, s);
    const cid = checkpointId(run.id, "sft", s);
    const regressed = s === 40000;
    add({ phase: "eval-periodic", attempt: 1, checkpoint: cid, notable: regressed }, timeOfStep(a, s) + 28 * MINUTE, {
      kind: MlEventKind.EVAL,
      severity: regressed ? MlSeverity.WARNING : MlSeverity.INFO,
      title: `Eval of ${fmtN(s)}: MMLU-Pro ${(evalScore("mmlu_pro", `v3@${s}`) * 100).toFixed(1)}`,
      detail: regressed ? `gsm8k ${(evalScore("gsm8k", "v3@40000") * 100).toFixed(1)} vs v2 ${(evalScore("gsm8k", "v2") * 100).toFixed(1)}: regressed` : "release-gate-lite v2 on a100-dev",
      subject: subj(MlSubjectKind.CHECKPOINT, cid, `sft step ${fmtN(s)}`),
      related: [subj(MlSubjectKind.EVAL_RUN, periodicEvalRunId(s), periodicEvalRunId(s)), ...(regressed ? [subj(MlSubjectKind.EVAL_RUN, EVAL_RUNS.v3ckpt, EVAL_RUNS.v3ckpt)] : []), runS],
      source: "forge",
      step: BigInt(s),
      hookEventType: regressed ? "ml.eval.regressed" : "",
    });
  }
  return out;
}

function otherEvents(): TaggedEvent[] {
  const out: TaggedEvent[] = [];
  const mk = (runId: string, name: string, t: number, ev: Omit<EventInit, "at" | "endAt" | "subject"> & { subject?: SubjectInit }, tag: Partial<Tag> = {}) =>
    out.push({ tag: { runId, notable: true, ...tag }, t, ev: { subject: subj(MlSubjectKind.RUN, runId, name), ...ev } });
  const v2 = v2Spec();
  mk(RUNS.v2.id, RUNS.v2.name, v2.createdAt, { id: "ev-v2-launch", kind: MlEventKind.LAUNCH, severity: MlSeverity.INFO, title: `maya launched ${RUNS.v2.name}`, detail: "sft → quantize", source: "person" });
  mk(RUNS.v2.id, RUNS.v2.name, V2_END, { id: `ev-${RUNS.v2.id}-quantize-1-end`, kind: MlEventKind.COMPLETED, severity: MlSeverity.INFO, title: `${RUNS.v2.name} succeeded`, detail: "Produced support-assistant v2 and v2.1-fp8", related: [subj(MlSubjectKind.MODEL_VERSION, VERSIONS.v2.id, "v2"), subj(MlSubjectKind.MODEL_VERSION, VERSIONS.v21fp8.id, "v2.1-fp8")], source: "trainer", step: 60000n, hookEventType: "ml.phase.succeeded" }, { phase: "quantize", attempt: 1 });
  mk(RUNS.v1.id, RUNS.v1.name, V1_END, { id: `ev-${RUNS.v1.id}-sft-1-end`, kind: MlEventKind.OOM, severity: MlSeverity.CRITICAL, title: "sft attempt 1 failed: Bus error in DataLoader worker 3", detail: "/dev/shm is 64 MiB (container default)", subject: attemptSubject(RUNS.v1.id, "sft", 1), related: [subj(MlSubjectKind.RUN, RUNS.v1.id, RUNS.v1.name), findingSubject(FINDINGS.dataloaderShm)], source: "trainer", step: 22140n, hookEventType: "ml.attempt.failed" }, { phase: "sft", attempt: 1 });
  mk(RUNS.dpo.id, RUNS.dpo.name, -DPO_PENDING.waitingMs, { id: "ev-dpo-launch", kind: MlEventKind.LAUNCH, severity: MlSeverity.INFO, title: `maya launched ${RUNS.dpo.name}`, detail: "Waiting for 32 H100s in team-research (insufficient quota)", related: [subj(MlSubjectKind.QUEUE, DPO_PENDING.queue, "team-research")], source: "person" }, { phase: "dpo", attempt: 1 });
  for (const t of SWEEP_TRIALS) {
    mk(t.run.id, t.run.name, t.start, { id: `ev-${t.run.id}-launch`, kind: MlEventKind.LAUNCH, severity: MlSeverity.INFO, title: `${SWEEP_ID} trial ${t.trial} launched (lr ${t.lr})`, detail: "forge ml sweep: 8 × A100 proxy, 3,000 steps", source: "forge" }, { notable: t.trial === 1 });
    if (t.endKind === "rewind") mk(t.run.id, t.run.name, t.end!, { id: `ev-${t.run.id}-sft-1-end`, kind: MlEventKind.CANCEL, severity: MlSeverity.INFO, title: `maya cancelled trial ${t.trial}`, detail: t.detail, source: "person", step: BigInt(t.endStep) }, { phase: "sft", attempt: 1 });
    if (t.endKind === "completed") mk(t.run.id, t.run.name, t.end!, { id: `ev-${t.run.id}-sft-1-end`, kind: MlEventKind.COMPLETED, severity: MlSeverity.INFO, title: `trial ${t.trial} completed: val loss ${t.final.toFixed(3)}`, detail: "", source: "trainer", step: BigInt(t.endStep) }, { phase: "sft", attempt: 1, notable: false });
  }
  mk(RUNS.reranker.id, RUNS.reranker.name, -9 * HOUR, { id: `ev-${RUNS.reranker.id}-job-1-end`, kind: MlEventKind.COMPLETED, severity: MlSeverity.INFO, title: "reranker nightly job succeeded (observed)", detail: "PyTorchJob ml-research/reranker-nightly-20261125, 4 × A100, 1h 12m", source: "kubelet" }, { phase: "job", attempt: 1, notable: false });
  return out;
}

export function trainingEvents(): TaggedEvent[] {
  return [...v3Events(), ...otherEvents()];
}

function runOfKey(key: string) {
  return Object.values(RUNS).find((r) => r.id === key || r.name === key);
}

function eventMatches(e: TaggedEvent, kind: MlSubjectKind, id: string): boolean {
  const parts = id.split("/");
  const run = runOfKey(parts[0]);
  switch (kind) {
    case MlSubjectKind.ORG:
      return !!e.tag.notable;
    case MlSubjectKind.EXPERIMENT: {
      const spec = allSpecs().find((s) => s.id === e.tag.runId);
      return spec?.experimentId === id && (!!e.tag.notable || e.tag.runId !== RUNS.v3.id);
    }
    case MlSubjectKind.RUN:
      return !!run && e.tag.runId === run.id;
    case MlSubjectKind.PHASE:
      return !!run && e.tag.runId === run.id && e.tag.phase === parts[1];
    case MlSubjectKind.ATTEMPT:
      return !!run && e.tag.runId === run.id && e.tag.phase === parts[1] && e.tag.attempt === Number(parts[2]);
    case MlSubjectKind.RANK:
      return !!run && e.tag.runId === run.id && e.tag.phase === parts[1] && e.tag.attempt === Number(parts[2]) && (e.tag.rank === Number(parts[3]) || e.tag.rank === undefined && e.tag.checkpoint === undefined && [MlEventKind.RESTART, MlEventKind.QUEUE_ADMITTED, MlEventKind.POD_STARTED].includes(e.ev.kind ?? MlEventKind.UNSPECIFIED));
    case MlSubjectKind.CHECKPOINT:
      return e.tag.checkpoint === id;
    default:
      return false;
  }
}

const OWNED_KINDS = new Set([MlSubjectKind.ORG, MlSubjectKind.EXPERIMENT, MlSubjectKind.RUN, MlSubjectKind.PHASE, MlSubjectKind.ATTEMPT, MlSubjectKind.RANK, MlSubjectKind.CHECKPOINT]);

// ── Alert rules ───────────────────────────────────────────────────────────────

function alertRules(now: Now, subject: SubjectInit, org: boolean): AlertRuleInit[] {
  const xidAt = V3_SFT[1].endedAt!;
  const hangAt = V3_SFT[0].endedAt!;
  const rules: (AlertRuleInit & { orgToo: boolean })[] = [
    { orgToo: false, id: ALERTS.xidFatal, name: "GPU fatal Xid", expression: "dcgm Xid in (48, 63, 64, 74, 79, 94, 95) on a node of the run", severity: MlSeverity.CRITICAL, state: MlAlertState.OK, subject, declaredIn: "forge default", lastFiredAt: at(xidAt - 41 * SEC, now), fired7d: 1, autoAction: "cordon the node and start a new attempt from the last checkpoint (swap in a hot spare when one is free)", autoActionEnabled: true, routedTo: "Slack #ml-oncall", lastAction: `cordoned ${BAD_NODE}, swapped in ${SPARE_NODE}, started sft attempt 3 from 32,000` },
    { orgToo: true, id: ALERTS.ncclHang, name: "NCCL hang", expression: "NCCL watchdog timeout or no collective progress for 600 s", severity: MlSeverity.CRITICAL, state: MlAlertState.OK, subject, declaredIn: "forge default", lastFiredAt: at(hangAt, now), fired7d: 1, autoAction: "restart the attempt after dumping the flight recorder", autoActionEnabled: true, routedTo: "Slack #ml-oncall", lastAction: "dumped the flight recorder (64 ranks), ran DCGM diag r2, restarted sft attempt 2 from 12,000" },
    { orgToo: true, id: ALERTS.lossSpike, name: "Loss spike", expression: "train/loss z-score > 6 for 3 steps", severity: MlSeverity.WARNING, state: MlAlertState.OK, subject, declaredIn: "deploy/kcl/prod/ml.k", lastFiredAt: at(timeOfStep(V3_SFT_NOW, 38650), now), fired7d: 1, autoAction: "rewind to the last good checkpoint and skip the batch", autoActionEnabled: false, routedTo: "Inbox", lastAction: "notified only (auto action is opt-in and off); the run recovered by 39,100" },
    { orgToo: true, id: ALERTS.stepStall, name: "Step stall", expression: "no optimizer step for 120 s while running", severity: MlSeverity.WARNING, state: MlAlertState.OK, subject, declaredIn: "forge default", lastFiredAt: at(hangAt - 480 * SEC, now), fired7d: 1, autoAction: "", autoActionEnabled: false, routedTo: "Slack #ml-oncall", lastAction: "notified" },
    { orgToo: true, id: ALERTS.straggler, name: "Straggler", expression: "a rank's step compute > 1.1 × median for 200 steps", severity: MlSeverity.WARNING, state: MlAlertState.FIRING, subject, declaredIn: "forge default", lastFiredAt: at(STRAGGLER_SINCE, now), fired7d: 1, autoAction: "", autoActionEnabled: false, routedTo: "Inbox", lastAction: "notified; ml.straggler.detected emitted" },
    { orgToo: false, id: ALERTS.gpuThermal, name: "GPU thermal slowdown", expression: "DCGM clock throttle reason HW_THERMAL for 5 min", severity: MlSeverity.WARNING, state: MlAlertState.FIRING, subject, declaredIn: "forge default", lastFiredAt: at(STRAGGLER_SINCE - 6 * MINUTE, now), fired7d: 1, autoAction: "", autoActionEnabled: false, routedTo: "Inbox", lastAction: "notified" },
  ];
  return rules.filter((r) => !org || r.orgToo).map(({ orgToo: _o, ...r }) => r);
}

// ── Answers ───────────────────────────────────────────────────────────────────

function findRun(key: string, now: Now, withRanks: boolean): { run: RunBuilt; runs: RunBuilt[] } | undefined {
  const runs = trainingRuns(now, withRanks);
  const run = runs.find((r) => r.spec.id === key || r.spec.name === key);
  return run ? { run, runs } : undefined;
}

export const trainingAnswers: MlDomain = {
  listMlExperiments: (req, now) => {
    if (req.project && req.project !== ML_PROJECT) return { experiments: [] };
    const runs = trainingRuns(now);
    return { experiments: Object.values(EXPERIMENTS).map((e) => experimentOf(e.id, runs, now)!).filter(Boolean) };
  },
  listMlRuns: (req, now) => {
    if (req.project && req.project !== ML_PROJECT) return { runs: [] };
    const runs = trainingRuns(now).filter((r) => !req.experimentId || r.spec.experimentId === req.experimentId);
    return { runs: runs.map((r) => r.init) };
  },
  getMlRun: (req, now) => {
    const found = findRun(req.run, now, true);
    return found ? detailOf(found.run, found.runs, now) : undefined;
  },
};

/** The trainer's and Kueue's events on a run, and the alert rules that cover it; served by ./telemetry.ts. */
export const trainingEventsAndAlerts: MlTelemetryDomain = {
  events: (req, now) => {
    const s = req.subject;
    if (!s || !OWNED_KINDS.has(s.kind)) return undefined;
    const from = req.from ? Number(req.from.seconds) * 1000 : -Infinity;
    const to = req.to ? Number(req.to.seconds) * 1000 : Infinity;
    const nowMs = now();
    let events = trainingEvents()
      .filter((e) => eventMatches(e, s.kind, s.id))
      .filter((e) => !req.kinds.length || req.kinds.includes(e.ev.kind ?? MlEventKind.UNSPECIFIED))
      .filter((e) => nowMs + e.t >= from && nowMs + e.t <= to)
      .sort((a, b) => b.t - a.t);
    if (req.limit) events = events.slice(0, req.limit);
    return { events: events.map((e) => ({ ...e.ev, at: at(e.t, now), endAt: e.end !== undefined ? at(e.end, now) : undefined })) };
  },
  alerts: (req, now) => {
    const s = req.subject;
    if (!s) return undefined;
    // The org's list holds every rule on the run: AlertingService lists each alert once, labelled with the run.
    if (s.kind === MlSubjectKind.ORG) return { alerts: alertRules(now, v3Subject, false) };
    const runKey = s.id.split("/")[0];
    if ([MlSubjectKind.RUN, MlSubjectKind.PHASE, MlSubjectKind.ATTEMPT, MlSubjectKind.RANK].includes(s.kind) && runOfKey(runKey)?.id === RUNS.v3.id) return { alerts: alertRules(now, v3Subject, false) };
    if (s.kind === MlSubjectKind.EXPERIMENT && s.id === EXPERIMENTS.support.id) return { alerts: alertRules(now, v3Subject, false) };
    return undefined;
  },
};
