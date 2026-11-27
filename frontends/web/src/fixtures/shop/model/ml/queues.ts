// Copyright (c) 2026 Reliant Labs

/**
 * The ML fixture world's Queues: GetMlQueues, and the QUEUE / WORKLOAD
 * series and events (plus the org's preemptions). Ids and the story:
 * ./world.ts.
 *
 * THE SCHEDULING STORY. Cohort ml-h100 is the whole h100-train pool (64
 * GPUs): team-research holds 48 nominal and borrows team-serving-batch's 16,
 * all of it held by llama-8b-support-v3 sft attempt 3. So the DPO ablation
 * (32 GPUs, 3h12m) and a sweep trial (8) wait on quota, and once a week
 * team-serving-batch reclaims its 16 — which is what preempted v3 pretrain
 * attempt 1 six days ago (400 steps redone). In the a100 eval queue the
 * release-gate rerun of v2.1-fp8 waits for priya's lower-priority notebook to
 * be preempted, and jon's notebook cannot schedule (no GPU toleration).
 * Pod start is dominated by Kueue waits, then node provisioning and the
 * 9.4 GB vLLM image pull.
 */

import { MlAxis, MlCollection, MlEventKind, MlPendingReason, MlSeverity, MlSubjectKind } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { v3StepTimeMs } from "./curves";
import type { MlDomain } from "./domain";
import type { MlTelemetryDomain } from "./telemetryTypes";
import { INFERENCE_REPLICAS, replicaId } from "./inference";
import {
  at,
  attemptId,
  DAY,
  DPO_PENDING,
  FINDINGS,
  hash,
  HOUR,
  MINUTE,
  noise,
  POOLS,
  PREEMPTOR_WORKLOAD,
  QUEUES,
  rng,
  RUNS,
  secAt,
  SPARE_NODE,
  V3_ATTEMPTS,
  V3_NOW,
  workloadName,
} from "./world";

type Now = () => number;

// ── Workloads that are not a run's attempt ────────────────────────────────────

/** Kueue Workloads with no run behind them (a WORKLOAD subject's id is its name). */
export const QUEUE_WORKLOADS = {
  sweepT5: "llama-8b-support-lr-sweep-t5-a1",
  evalRerun: "eval-release-gate-v7-v21fp8-rerun",
  evalCandidate: "eval-release-gate-v7-v3-sft-40000",
  jonNotebook: "jupyter-jon-a100-0",
  priyaNotebook: "jupyter-priya-a100-0",
  rerankerHpo: "reranker-hpo-trial-7",
  periodic38k: "llama-8b-support-v3-eval-periodic-38000",
  periodic40k: "llama-8b-support-v3-eval-periodic-40000",
} as const;

const workload = (id: string, label = id) => ({ kind: MlSubjectKind.WORKLOAD, id, label });
const attempt = (runId: string, phase: string, n: number, label: string) => ({ kind: MlSubjectKind.ATTEMPT, id: attemptId(runId, phase, n), label });

// ── Preemptions ───────────────────────────────────────────────────────────────

/** GPU-hours v3 pretrain attempt 1 lost: the 400 steps between checkpoint 6,000 and the preemption at 6,400, on 64 GPUs. */
export const V3_PREEMPTION_GPU_HOURS = (400 * (v3StepTimeMs("pretrain", 6200, false) / 1000) * V3_NOW.worldSize) / 3600;

const v3a1 = V3_ATTEMPTS.find((a) => a.phase === "pretrain" && a.n === 1)!;

function preemptions(now: Now) {
  return [
    {
      at: at(-3.3 * HOUR, now),
      preemptor: workload(QUEUE_WORKLOADS.periodic40k, "v3 eval-periodic @ 40k"),
      preempted: workload(QUEUE_WORKLOADS.rerankerHpo),
      reason: "InClusterQueue: lower priority (eval-high 200 > research-low 50)",
      gpuHoursLost: (4 * 25) / 60,
      queueId: QUEUES.eval.id,
    },
    {
      at: at(-8.9 * HOUR, now),
      preemptor: workload(QUEUE_WORKLOADS.periodic38k, "v3 eval-periodic @ 38k"),
      preempted: workload(QUEUE_WORKLOADS.jonNotebook),
      reason: "InClusterQueue: lower priority (eval-high 200 > notebook 10)",
      gpuHoursLost: 0.2,
      queueId: QUEUES.eval.id,
    },
    {
      at: at(v3a1.endedAt!, now),
      preemptor: workload(PREEMPTOR_WORKLOAD, `${PREEMPTOR_WORKLOAD} (team-serving-batch)`),
      preempted: attempt(RUNS.v3.id, "pretrain", 1, `${RUNS.v3.name} · pretrain attempt 1`),
      reason: "InCohortReclamation: team-serving-batch reclaimed the 16 H100s team-research had borrowed",
      gpuHoursLost: V3_PREEMPTION_GPU_HOURS,
      queueId: QUEUES.research.id,
    },
  ];
}

// ── Queues and pending ────────────────────────────────────────────────────────

const GPU = "nvidia.com/gpu";

function clusterQueues() {
  return [
    {
      id: QUEUES.research.id,
      name: QUEUES.research.name,
      clusterId: QUEUES.research.clusterId,
      cohort: QUEUES.research.cohort,
      flavors: [{ flavor: QUEUES.research.flavor, resource: GPU, nominal: QUEUES.research.nominal, used: 64, borrowed: 16, lendable: 0, borrowingLimit: 16 }],
      pending: 2,
      admitted: 1,
      waitP50Seconds: 18 * 60,
      waitP95Seconds: 72 * 60,
      fairShare: 1.12,
      localQueues: [...QUEUES.research.localQueues],
      queueingStrategy: "BestEffortFIFO",
      preemptionPolicy: "LowerPriority",
    },
    {
      id: QUEUES.servingBatch.id,
      name: QUEUES.servingBatch.name,
      clusterId: QUEUES.servingBatch.clusterId,
      cohort: QUEUES.servingBatch.cohort,
      flavors: [{ flavor: QUEUES.servingBatch.flavor, resource: GPU, nominal: QUEUES.servingBatch.nominal, used: 0, borrowed: 0, lendable: 16, borrowingLimit: 0 }],
      pending: 0,
      admitted: 0,
      waitP50Seconds: 2 * 60,
      waitP95Seconds: 9 * 60,
      fairShare: 0,
      localQueues: [...QUEUES.servingBatch.localQueues],
      queueingStrategy: "StrictFIFO",
      preemptionPolicy: "Any",
    },
    {
      id: QUEUES.eval.id,
      name: QUEUES.eval.name,
      clusterId: QUEUES.eval.clusterId,
      cohort: QUEUES.eval.cohort,
      flavors: [{ flavor: QUEUES.eval.flavor, resource: GPU, nominal: QUEUES.eval.nominal, used: 12, borrowed: 0, lendable: 4, borrowingLimit: 0 }],
      pending: 2,
      admitted: 2,
      waitP50Seconds: 4 * 60,
      waitP95Seconds: 31 * 60,
      fairShare: 0.75,
      localQueues: [...QUEUES.eval.localQueues],
      queueingStrategy: "BestEffortFIFO",
      preemptionPolicy: "LowerPriority",
    },
    {
      id: QUEUES.serving.id,
      name: QUEUES.serving.name,
      clusterId: QUEUES.serving.clusterId,
      cohort: QUEUES.serving.cohort,
      flavors: [{ flavor: QUEUES.serving.flavor, resource: GPU, nominal: QUEUES.serving.nominal, used: servingGpus(), borrowed: 0, lendable: QUEUES.serving.nominal - servingGpus(), borrowingLimit: 0 }],
      pending: 0,
      admitted: 3,
      waitP50Seconds: 0,
      waitP95Seconds: 4,
      fairShare: servingGpus() / QUEUES.serving.nominal,
      localQueues: [...QUEUES.serving.localQueues],
      queueingStrategy: "BestEffortFIFO",
      preemptionPolicy: "Never",
    },
  ];
}

/** The L4s the serving queue's replicas hold (prod 8, staging 1, reranker 2). */
function servingGpus(): number {
  return INFERENCE_REPLICAS.reduce((sum, r) => sum + r.gpus.length, 0);
}

export interface PendingDef {
  name: string;
  queueId: string;
  localQueue: string;
  priority: number;
  priorityClass: string;
  gpus: number;
  flavor: string;
  waitingMs: number;
  reason: MlPendingReason;
  message: string;
  position: number;
  subject: { kind: MlSubjectKind; id: string; label: string };
  owner: string;
}

export const PENDING: readonly PendingDef[] = [
  {
    name: workloadName(RUNS.dpo.name, "dpo", 1),
    queueId: DPO_PENDING.queue,
    localQueue: QUEUES.research.localQueues[0],
    priority: DPO_PENDING.priority,
    priorityClass: DPO_PENDING.priorityClass,
    gpus: DPO_PENDING.gpus,
    flavor: "h100",
    waitingMs: DPO_PENDING.waitingMs,
    reason: MlPendingReason.INSUFFICIENT_QUOTA,
    message: `couldn't assign flavors to pod set launcher: insufficient unused quota for ${GPU} in flavor h100, ${DPO_PENDING.gpus} more needed`,
    position: 1,
    subject: attempt(RUNS.dpo.id, "dpo", 1, `${RUNS.dpo.name} · dpo attempt 1`),
    owner: "maya",
  },
  {
    name: QUEUE_WORKLOADS.sweepT5,
    queueId: QUEUES.research.id,
    localQueue: QUEUES.research.localQueues[0],
    priority: 50,
    priorityClass: "research-low",
    gpus: 8,
    flavor: "h100",
    waitingMs: 47 * MINUTE,
    reason: MlPendingReason.INSUFFICIENT_QUOTA,
    message: `couldn't assign flavors to pod set trainer: insufficient unused quota for ${GPU} in flavor h100, 8 more needed`,
    position: 2,
    subject: workload(QUEUE_WORKLOADS.sweepT5, "llama-8b-support-lr-sweep-t5"),
    owner: "maya",
  },
  {
    name: QUEUE_WORKLOADS.evalRerun,
    queueId: QUEUES.eval.id,
    localQueue: QUEUES.eval.localQueues[0],
    priority: 200,
    priorityClass: "eval-high",
    gpus: 8,
    flavor: "a100",
    waitingMs: 6 * MINUTE,
    reason: MlPendingReason.WAITING_FOR_PREEMPTION,
    message: `Pending the preemption of 1 workload(s): ml-notebooks/${QUEUE_WORKLOADS.priyaNotebook} (4 GPUs free, 8 needed)`,
    position: 1,
    subject: workload(QUEUE_WORKLOADS.evalRerun, "release-gate v7 · v2.1-fp8 rerun"),
    owner: "jon",
  },
  {
    name: QUEUE_WORKLOADS.jonNotebook,
    queueId: QUEUES.eval.id,
    localQueue: QUEUES.eval.localQueues[1],
    priority: 10,
    priorityClass: "notebook",
    gpus: 2,
    flavor: "a100",
    waitingMs: 8.9 * HOUR,
    reason: MlPendingReason.UNTOLERATED_TAINT,
    message: "0/2 nodes are available: 2 node(s) had untolerated taint {nvidia.com/gpu: present}. preemption: 0/2 nodes are available: 2 Preemption is not helpful for scheduling.",
    position: 2,
    subject: workload(QUEUE_WORKLOADS.jonNotebook),
    owner: "jon",
  },
];

/** Accelerators free right now: the eval queue's 4 unused A100s, and the L4s neither a replica nor priya's idle notebook (2) holds. */
const GPUS_FREE = QUEUES.eval.nominal - 12 + (POOLS.l4.nodes.length * POOLS.l4.gpusPerNode - servingGpus() - 2);

function pendingMessage(p: PendingDef, now: Now) {
  return {
    name: p.name,
    queueId: p.queueId,
    localQueue: p.localQueue,
    priority: p.priority,
    priorityClass: p.priorityClass,
    gpus: p.gpus,
    flavor: p.flavor,
    createdAt: at(-p.waitingMs, now),
    waitingSeconds: p.waitingMs / 1000,
    reason: p.reason,
    reasonMessage: p.message,
    position: p.position,
    subject: p.subject,
    owner: p.owner,
  };
}

// ── Pod start ─────────────────────────────────────────────────────────────────

const POD_START_STAGES = [
  { stage: "queued", p50: 240, p95: 2460, share: 0.48, cause: "Insufficient quota in flavor h100 (cohort ml-h100 fully borrowed)", finding: FINDINGS.quotaStarved },
  { stage: "scheduling", p50: 4, p95: 41, share: 0.01, cause: "Untolerated taint nvidia.com/gpu (2 notebook pods)", finding: "" },
  { stage: "node provisioning", p50: 190, p95: 540, share: 0.22, cause: "a100-dev scale-up: GCE capacity error in us-central1-b, retried", finding: "" },
  { stage: "sandbox/init", p50: 12, p95: 90, share: 0.04, cause: "storage-initializer pulling weights and checkpoints", finding: FINDINGS.modelLoad },
  { stage: "image pull", p50: 96, p95: 210, share: 0.18, cause: "vllm/vllm-openai and the trainer image (9.4 GB), not cached on new nodes", finding: FINDINGS.imagePull },
  { stage: "model load", p50: 58, p95: 80, share: 0.07, cause: "16 GB from the registry; checkpoint restore for training", finding: FINDINGS.modelLoad },
];

function podStartExamples(now: Now) {
  const a3 = V3_ATTEMPTS.find((a) => a.phase === "sft" && a.n === 3)!;
  const a3Subject = attempt(RUNS.v3.id, "sft", 3, "sft attempt 3");
  const stages = (rows: [string, number, string][]) => {
    let t = 0;
    return rows.map(([stage, seconds, detail]) => {
      const out = { stage, startSeconds: t, seconds, detail };
      t += seconds;
      return out;
    });
  };
  const sftPod = (worker: number, pull: number, pullDetail: string) => ({
    pod: `${workloadName(RUNS.v3.name, "sft", 3)}-w-${worker}`,
    workload: a3Subject,
    createdAt: at(a3.queuedAt, now),
    stages: stages([
      ["queued", 6, "Admitted at once: attempt 2 released team-research's 64 GPUs"],
      ["scheduling", 4, "Topology-aware placement on one NVLink domain"],
      ["node provisioning", 0, worker === 2 ? `Hot spare ${SPARE_NODE} already up` : "Node already up"],
      ["sandbox/init", 14, "storage-initializer: dataset shard index"],
      ["image pull", pull, pullDetail],
      ["model load", 228, "Checkpoint 32,000 restore (weights + optimizer, 96 GB from GCS)"],
      ["ready", 45, "NCCL init across 64 ranks, then the first step"],
    ]),
  });
  const vllm = INFERENCE_REPLICAS[1];
  return [
    sftPod(0, 2, "Image cached on the node"),
    sftPod(2, 168, `trainer image 11.2 GB: not cached on the spare ${SPARE_NODE}`),
    {
      pod: vllm.pod,
      workload: { kind: MlSubjectKind.REPLICA, id: replicaId(vllm), label: "vllm-1" },
      createdAt: at(-vllm.startedAgo - 290 * 1000, now),
      stages: stages([
        ["queued", 0, "Serving queue has quota"],
        ["scheduling", 3, ""],
        ["node provisioning", 0, "Warm pool"],
        ["sandbox/init", 12, "model-downloader (LoRA adapters)"],
        ["image pull", 211, "vllm/vllm-openai:v0.8.5, 9.4 GB"],
        ["model load", 58, "16 GB weights from the registry + CUDA graphs"],
        ["ready", 6, "Readiness probe /health"],
      ]),
    },
  ];
}

// ── Series (7 days, hourly) ───────────────────────────────────────────────────

const HOURS = 168;

function weekGrid(now: Now): number[] {
  const end = secAt(0, now);
  const last = end - (end % 3600);
  return Array.from({ length: HOURS + 1 }, (_, i) => last - (HOURS - i) * 3600);
}

interface QueueTimeline {
  admitted: (offsetMs: number) => number;
  pending: { reason: string; gpus: (offsetMs: number) => number }[];
  limit: number;
  flavor: string;
  waitP95: (offsetMs: number) => number;
}

const within = (t: number, from: number, to: number) => t >= from && t < to;

/** GPUs held by the v3 attempts (all 64) and the queue waits between them, as offsets from now. */
function v3Admitted(t: number): number {
  return V3_ATTEMPTS.some((a) => within(t, a.queuedAt + (a.runningAt - a.queuedAt) * 0.8, a.endedAt ?? 1)) ? 64 : 0;
}
function v3Pending(t: number): number {
  return V3_ATTEMPTS.some((a) => within(t, a.queuedAt, a.queuedAt + (a.runningAt - a.queuedAt) * 0.8)) ? 64 : 0;
}

function timeline(queueId: string): QueueTimeline | undefined {
  switch (queueId) {
    case QUEUES.research.id:
      return {
        admitted: v3Admitted,
        pending: [{ reason: "insufficient quota", gpus: (t) => v3Pending(t) + (t >= -DPO_PENDING.waitingMs ? DPO_PENDING.gpus : 0) + (t >= -47 * MINUTE ? 8 : 0) }],
        limit: QUEUES.research.nominal + 16,
        flavor: QUEUES.research.flavor,
        waitP95: (t) => (v3Pending(t) || t >= -DPO_PENDING.waitingMs ? 3600 + (t >= -DPO_PENDING.waitingMs ? -t / 1000 : 0) : 1080),
      };
    case QUEUES.servingBatch.id:
      return {
        admitted: (t) => (within(t, v3a1.endedAt! - 0.1 * HOUR, v3a1.endedAt! + 1.8 * HOUR) || within(t, -3 * DAY - 2 * HOUR, -3 * DAY) ? 16 : 0),
        pending: [{ reason: "waiting for preemption", gpus: (t) => (within(t, v3a1.endedAt! - 0.4 * HOUR, v3a1.endedAt! - 0.1 * HOUR) ? 16 : 0) }],
        limit: QUEUES.servingBatch.nominal,
        flavor: QUEUES.servingBatch.flavor,
        waitP95: () => 540,
      };
    case QUEUES.eval.id:
      return {
        admitted: (t) => 4 + (within(t, -3.3 * HOUR, -2.3 * HOUR) || within(t, -8.9 * HOUR, -7.9 * HOUR) ? 8 : 0) + (t >= -2 * HOUR ? 8 : 0) + (t < -8.9 * HOUR ? 2 : 0),
        pending: [
          { reason: "waiting for preemption", gpus: (t) => (t >= -6 * MINUTE ? 8 : 0) },
          { reason: "untolerated taint", gpus: (t) => (t >= -8.9 * HOUR ? 2 : 0) },
        ],
        limit: QUEUES.eval.nominal,
        flavor: QUEUES.eval.flavor,
        waitP95: (t) => 600 + 1500 * Math.max(0, Math.sin((t / DAY) * 2 * Math.PI)),
      };
    case QUEUES.serving.id:
      return {
        admitted: (t) => (within(t, -21 * HOUR, -9.5 * HOUR) ? servingGpus() - 2 : servingGpus()),
        pending: [],
        limit: QUEUES.serving.nominal,
        flavor: QUEUES.serving.flavor,
        waitP95: () => 4,
      };
    default:
      return undefined;
  }
}

const QUEUE_KEYS: Record<string, { label: string; unit: string }> = {
  "queue.demand_gpus": { label: "GPU demand vs capacity", unit: "count" },
  "queue.usage": { label: "Quota used", unit: "count" },
  "queue.wait_p95": { label: "Admission wait p95", unit: "s" },
  "queue.pending": { label: "Pending GPUs", unit: "count" },
};

function queueSeries(queueId: string, keys: readonly string[], now: Now) {
  const tl = timeline(queueId);
  const q = Object.values(QUEUES).find((x) => x.id === queueId);
  if (!tl || !q) return [];
  const xs = weekGrid(now);
  const offset = (x: number) => x * 1000 - now();
  const subject = { kind: MlSubjectKind.QUEUE, id: queueId, label: q.name };
  const mk = (key: string, group: string, y: number[], stat = "") => ({ key, label: QUEUE_KEYS[key].label, unit: QUEUE_KEYS[key].unit, axis: MlAxis.TIME, x: xs, y, group, stat, resolution: 3600, collection: MlCollection.COLLECTED, subject });
  const wanted = keys.length ? keys : Object.keys(QUEUE_KEYS);
  const r = rng(hash(queueId));
  return wanted.flatMap((key) => {
    switch (key) {
      case "queue.demand_gpus":
        return [
          mk(key, "admitted", xs.map((x) => tl.admitted(offset(x)))),
          ...tl.pending.map((p) => mk(key, `pending · ${p.reason}`, xs.map((x) => p.gpus(offset(x))))),
          mk(key, "nominal + borrowing", xs.map(() => tl.limit), "limit"),
        ];
      case "queue.usage":
        return [mk(key, `${tl.flavor} used`, xs.map((x) => tl.admitted(offset(x)))), mk(key, `${tl.flavor} quota`, xs.map(() => q.nominal), "limit")];
      case "queue.wait_p95":
        return [mk(key, "", xs.map((x) => Math.max(0, tl.waitP95(offset(x)) * (1 + 0.1 * noise(r)))))];
      case "queue.pending":
        return [mk(key, "", xs.map((x) => tl.pending.reduce((s, p) => s + p.gpus(offset(x)), 0)))];
      default:
        return [];
    }
  });
}

// ── Events ────────────────────────────────────────────────────────────────────

function queueEvents(now: Now) {
  const pre = preemptions(now).map((p, k) => ({
    id: `ev-queue-preemption-${k}`,
    at: p.at,
    kind: MlEventKind.PREEMPTION,
    severity: p.gpuHoursLost > 5 ? MlSeverity.WARNING : MlSeverity.INFO,
    title: `${p.preempted.label} preempted by ${p.preemptor.label}`,
    detail: `${p.reason}. ${p.gpuHoursLost.toFixed(1)} GPU-h lost.`,
    subject: p.preempted,
    related: [p.preemptor, { kind: MlSubjectKind.QUEUE, id: p.queueId, label: p.queueId.split("/").pop()! }, ...(k === 2 ? [{ kind: MlSubjectKind.FINDING, id: FINDINGS.preemptionChurn, label: "preemption churn" }] : [])],
    source: "kueue",
    attributes: { queue: p.queueId.split("/").pop()!, gpu_hours_lost: p.gpuHoursLost.toFixed(1) },
    hookEventType: "workload.preempted",
  }));
  const a3 = V3_ATTEMPTS.find((a) => a.phase === "sft" && a.n === 3)!;
  const admitted = {
    id: "ev-queue-admitted-v3-sft-a3",
    at: at(a3.queuedAt + 6000, now),
    kind: MlEventKind.QUEUE_ADMITTED,
    severity: MlSeverity.INFO,
    title: `${workloadName(RUNS.v3.name, "sft", 3)} admitted (64 × h100, 16 borrowed)`,
    detail: "team-research: 48 nominal + 16 borrowed from team-serving-batch in cohort ml-h100",
    subject: attempt(RUNS.v3.id, "sft", 3, "sft attempt 3"),
    related: [{ kind: MlSubjectKind.QUEUE, id: QUEUES.research.id, label: QUEUES.research.name }],
    source: "kueue",
  };
  const pending = PENDING.map((p) => ({
    id: `ev-queue-pending-${p.name}`,
    at: at(-p.waitingMs, now),
    kind: MlEventKind.SCHEDULING_FAILED,
    severity: p.reason === MlPendingReason.UNTOLERATED_TAINT ? MlSeverity.WARNING : MlSeverity.INFO,
    title: `${p.name} pending`,
    detail: p.message,
    subject: p.subject,
    related: [{ kind: MlSubjectKind.QUEUE, id: p.queueId, label: p.queueId.split("/").pop()! }],
    source: p.reason === MlPendingReason.UNTOLERATED_TAINT ? "scheduler" : "kueue",
  }));
  return { pre, admitted, pending };
}

function eventsFor(subject: { kind: MlSubjectKind; id: string }, now: Now) {
  const { pre, admitted, pending } = queueEvents(now);
  const all = [...pre, admitted, ...pending];
  switch (subject.kind) {
    case MlSubjectKind.ORG:
      return pre;
    case MlSubjectKind.QUEUE:
      return all.filter((e) => e.related.some((r) => r.kind === MlSubjectKind.QUEUE && r.id === subject.id));
    case MlSubjectKind.WORKLOAD:
      return all.filter((e) => e.subject.id === subject.id || e.related.some((r) => r.id === subject.id));
    default:
      return [];
  }
}

// ── The domain ────────────────────────────────────────────────────────────────

export function queuesResponse(now: Now) {
  const pre = preemptions(now);
  const day = pre.filter((p) => now() - Number(p.at.seconds) * 1000 <= DAY);
  return {
    summary: {
      pending: PENDING.length,
      gpusDemanded: PENDING.reduce((s, p) => s + p.gpus, 0),
      gpusFree: GPUS_FREE,
      waitP50Seconds: 12 * 60,
      waitP95Seconds: 72 * 60,
      preemptions24h: day.length,
      gpuHoursLost24h: day.reduce((s, p) => s + p.gpuHoursLost, 0),
    },
    queues: clusterQueues(),
    pending: PENDING.map((p) => pendingMessage(p, now)),
    preemptions: pre,
    podStart: {
      workload: "all accelerator pods (7d)",
      sampleCount: 214,
      stages: POD_START_STAGES.map((s) => ({ stage: s.stage, p50Seconds: s.p50, p95Seconds: s.p95, share: s.share, topCause: s.cause, findingId: s.finding })),
    },
    podStartExamples: podStartExamples(now),
  };
}

export const queuesAnswers: MlDomain = {
  getMlQueues: (req, now) => {
    const res = queuesResponse(now);
    if (!req.clusterId) return res;
    return { ...res, queues: res.queues.filter((q) => q.clusterId === req.clusterId), pending: res.pending.filter((p) => p.queueId.startsWith(`${req.clusterId}/`)) };
  },
};

/** Kueue's history (demand, admission waits) and events, served by ./telemetry.ts. */
export const queuesTelemetry: MlTelemetryDomain = {
  series: (req, now) => {
    if (req.subject?.kind !== MlSubjectKind.QUEUE) return undefined;
    const series = queueSeries(req.subject.id, req.keys, now);
    return series.length ? { series } : undefined;
  },
  events: (req, now) => {
    if (!req.subject || ![MlSubjectKind.ORG, MlSubjectKind.QUEUE, MlSubjectKind.WORKLOAD].includes(req.subject.kind)) return undefined;
    const events = eventsFor(req.subject, now).filter((e) => req.kinds.length === 0 || req.kinds.includes(e.kind));
    return events.length ? { events } : undefined;
  },
};
