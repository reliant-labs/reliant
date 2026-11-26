// Copyright (c) 2026 Reliant Labs

/**
 * THE ML FIXTURE WORLD: one story, one set of ids, every domain file of
 * ./ (fleet, setup, queues, findings, training, trainingTelemetry, evals,
 * models, lineage, inference) builds its answers from what is declared here,
 * so every id any answer names resolves in another answer
 * (__tests__/mlWorld.test.ts walks them). Only the demo carries this data;
 * the real service never invents any (FEATURE_NOT_ENTITLED or
 * Unimplemented).
 *
 * The org is acme (shopStory DEMO_ORG), the project `shop`, the people maya
 * (trains the support assistant), priya (reranker, notebooks) and jon
 * (admin: promotes models). Times are offsets from demoNow() (the page load):
 * `at(-26 * HOUR)` is 26 hours ago. Nothing happens after now.
 *
 * ── THE STORY ────────────────────────────────────────────────────────────────
 *
 * Two clusters. gke-us-central1-train (GKE) trains: pool h100-train, 8 nodes
 * × 8 H100 80GB (64 GPUs, reserved, $2.85/GPU-h), a one-node hot spare pool
 * h100-spare, and a100-dev (2 × 8 A100 40GB, on-demand $1.65) for evals and
 * notebooks. eks-us-east-1-serve (EKS) serves: l4-serve, 4 × 4 L4 24GB
 * ($0.98).
 *
 * llama-8b-support-v3 (maya, experiment support-assistant-8b) is the hero
 * run: continued pre-training → SFT → GRPO, with eval-periodic beside SFT, on
 * all 64 H100s (8 nodes, world size 64: dp 8 × tp 8 inside a node).
 *   pretrain a1  preempted by Kueue (InCohortReclamation: team-serving-batch
 *                took back the 16 H100s team-research had borrowed) at step
 *                6,400; last checkpoint 6,000 (400 steps redone).
 *   pretrain a2  resumed at 6,000 after 1h50m waiting for quota; completed at
 *                20,000.
 *   sft a1       NCCL hang at step 12,400 (allreduce seq 88213, rank 37 never
 *                arrived); watchdog fired after 10 min; checkpoint 12,000.
 *   sft a2       resumed at 12,000; Xid 79 (GPU fell off the bus) on
 *                gke-train-h100-r4vb GPU 5 at step 32,750; checkpoint 32,000.
 *                The opt-in alert alert-xid-fatal cordoned r4vb, swapped in
 *                hot spare gke-train-h100-s01, and started attempt 3.
 *   sft a3       running from 32,000, now at 41,200 of 60,000. Rank 37
 *                (gke-train-h100-c8dw GPU 5, local rank 5) is a straggler since
 *                12h ago: HW slowdown (thermal) at 87°C, HBM 95°C, so every
 *                step waits ~18% longer in allreduce. State: DEGRADED.
 *   eval-periodic every 2,000 SFT steps on a100-dev (release-gate-lite).
 *   rl (GRPO)    pending on SFT.
 * llama-8b-support-v2 succeeded 21 days ago and produced support-assistant v2
 * (production) and, through its quantize phase, v2.1-fp8 (staging, and the
 * 10% arm of the fp8 Changes experiment in prod — which FAILS the release
 * gate on safety_refusals 0.979 < 0.98). llama-8b-support-v1 failed (Bus
 * error in a DataLoader worker: 64Mi /dev/shm). Four lr-sweep trials, one
 * queued DPO ablation waiting on quota, and an observed, untracked reranker
 * nightly job complete the list.
 *
 * Serving: support-assistant-prod (vLLM 0.8.5 on l4-serve, TP 2, 4 replicas;
 * revision RELEASE_PROD with model v2 at 90%, and the fp8 arm at 10%),
 * support-assistant-staging (v2.1-fp8), reranker-prod (Triton). priya's
 * notebook has held 2 L4s at 0% for 19h.
 */

import { timestampFromMs } from "@bufbuild/protobuf/wkt";

import { DAY, demoNow, HOUR, MINUTE, RUNNING } from "../../shopStory";

export { DAY, HOUR, MINUTE };

// ── Clock ─────────────────────────────────────────────────────────────────────

/** Epoch ms of an offset from now (negative is the past). */
/** Rounded to the ms: a Timestamp's nanos must be an integer on the wire, and story offsets are often fractional (hours × steps/hour). */
export const msAt = (offsetMs: number, now: () => number = demoNow) => Math.round(now() + offsetMs);
/** A Timestamp at an offset from now. */
export const at = (offsetMs: number, now: () => number = demoNow) => timestampFromMs(msAt(offsetMs, now));
/** Epoch seconds at an offset (a time-axis MlSeries x). */
export const secAt = (offsetMs: number, now: () => number = demoNow) => Math.floor(msAt(offsetMs, now) / 1000);

// ── Deterministic noise ───────────────────────────────────────────────────────

/** A seeded PRNG (mulberry32): the same seed gives the same story on every load. */
export function rng(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

/** A string's 32-bit hash (FNV-1a), for seeding from an id. */
export function hash(s: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}

/** Gaussian-ish noise in [-1, 1] (sum of three uniforms). */
export const noise = (r: () => number) => (r() + r() + r()) / 1.5 - 1;

// ── People ────────────────────────────────────────────────────────────────────

export const ML_PEOPLE = { trainer: "maya", notebooks: "priya", admin: "jon" } as const;
export const ML_PROJECT = "shop";

// ── Fleet ─────────────────────────────────────────────────────────────────────

export const CLUSTERS = {
  train: { id: "gke-us-central1-train", name: "gke-us-central1-train", provider: "gke", location: "us-central1", kubernetesVersion: "1.31.4-gke.1256000" },
  serve: { id: "eks-us-east-1-serve", name: "eks-us-east-1-serve", provider: "eks", location: "us-east-1", kubernetesVersion: "1.31.3-eks-7b2b1c4" },
} as const;

export interface PoolDef {
  id: string;
  clusterId: string;
  name: string;
  product: string;
  memoryGb: number;
  gpusPerNode: number;
  usdPerGpuHour: number;
  capacity: "on-demand" | "spot" | "reserved";
  purpose: "training" | "serving" | "dev" | "spare";
  instanceType: string;
  zone: string;
  nodes: readonly string[];
  driver: string;
  cuda: string;
}

export const POOLS = {
  h100: {
    id: `${CLUSTERS.train.id}/h100-train`,
    clusterId: CLUSTERS.train.id,
    name: "h100-train",
    product: "NVIDIA H100 80GB HBM3",
    memoryGb: 80,
    gpusPerNode: 8,
    usdPerGpuHour: 2.85,
    capacity: "reserved",
    purpose: "training",
    instanceType: "a3-highgpu-8g",
    zone: "us-central1-a",
    nodes: ["gke-train-h100-7f2k", "gke-train-h100-9qzc", "gke-train-h100-r4vb", "gke-train-h100-m1xp", "gke-train-h100-c8dw", "gke-train-h100-t5hn", "gke-train-h100-w2jl", "gke-train-h100-b6ve"],
    driver: "550.127.05",
    cuda: "12.4",
  },
  spare: {
    id: `${CLUSTERS.train.id}/h100-spare`,
    clusterId: CLUSTERS.train.id,
    name: "h100-spare",
    product: "NVIDIA H100 80GB HBM3",
    memoryGb: 80,
    gpusPerNode: 8,
    usdPerGpuHour: 2.85,
    capacity: "reserved",
    purpose: "spare",
    instanceType: "a3-highgpu-8g",
    zone: "us-central1-a",
    nodes: ["gke-train-h100-s01"],
    driver: "550.127.05",
    cuda: "12.4",
  },
  a100: {
    id: `${CLUSTERS.train.id}/a100-dev`,
    clusterId: CLUSTERS.train.id,
    name: "a100-dev",
    product: "NVIDIA A100-SXM4-40GB",
    memoryGb: 40,
    gpusPerNode: 8,
    usdPerGpuHour: 1.65,
    capacity: "on-demand",
    purpose: "dev",
    instanceType: "a2-highgpu-8g",
    zone: "us-central1-b",
    nodes: ["gke-dev-a100-k3p9", "gke-dev-a100-x7qd"],
    driver: "550.127.05",
    cuda: "12.4",
  },
  l4: {
    id: `${CLUSTERS.serve.id}/l4-serve`,
    clusterId: CLUSTERS.serve.id,
    name: "l4-serve",
    product: "NVIDIA L4",
    memoryGb: 24,
    gpusPerNode: 4,
    usdPerGpuHour: 0.98,
    capacity: "on-demand",
    purpose: "serving",
    instanceType: "g6.12xlarge",
    zone: "us-east-1a",
    nodes: ["ip-10-40-1-21", "ip-10-40-1-37", "ip-10-40-2-14", "ip-10-40-2-58"],
    driver: "550.90.07",
    cuda: "12.4",
  },
} as const satisfies Record<string, PoolDef>;

export const ALL_POOLS: readonly PoolDef[] = Object.values(POOLS);

/** A node's id: "<cluster_id>/<node_name>". */
export const nodeId = (pool: PoolDef, name: string) => `${pool.clusterId}/${name}`;

/** The pool a node name belongs to. */
export function poolOfNode(name: string): PoolDef | undefined {
  return ALL_POOLS.find((p) => (p.nodes as readonly string[]).includes(name));
}

/** A device's UUID, stable per (node, index): "GPU-1a2b3c4d-…". */
export function gpuUuid(node: string, index: number): string {
  const h = (n: number) => hash(`${node}#${index}#${n}`).toString(16).padStart(8, "0");
  const a = h(1);
  const b = h(2);
  const c = h(3);
  return `GPU-${a}-${b.slice(0, 4)}-${b.slice(4, 8)}-${c.slice(0, 4)}-${c.slice(4, 8)}${h(4).slice(0, 4)}`;
}

/** The faulty board: Xid 79 (fell off the bus), cordoned by the alert action. */
export const BAD_NODE = "gke-train-h100-r4vb";
export const BAD_GPU_INDEX = 5;
/** The straggler's node: thermal HW slowdown on GPU 5 → rank 37. */
export const HOT_NODE = "gke-train-h100-c8dw";
export const HOT_GPU_INDEX = 5;
export const STRAGGLER_RANK = 37;
/** Row remap pending (needs a GPU reset at the next drain). */
export const REMAP_NODE = "gke-train-h100-m1xp";
export const REMAP_GPU_INDEX = 2;
/** NVLink CRC errors rising between GPU 1 and GPU 3. */
export const NVLINK_NODE = "gke-train-h100-t5hn";
/** The hot spare swapped in for r4vb. */
export const SPARE_NODE = "gke-train-h100-s01";
/** priya's notebook: 2 L4 at 0% for 19h. */
export const IDLE_NODE = "ip-10-40-2-58";

// ── Queues (Kueue) ────────────────────────────────────────────────────────────

export const QUEUES = {
  research: { id: `${CLUSTERS.train.id}/team-research`, name: "team-research", clusterId: CLUSTERS.train.id, cohort: "ml-h100", flavor: "h100", nominal: 48, localQueues: ["ml-research/train"] },
  servingBatch: { id: `${CLUSTERS.train.id}/team-serving-batch`, name: "team-serving-batch", clusterId: CLUSTERS.train.id, cohort: "ml-h100", flavor: "h100", nominal: 16, localQueues: ["ml-serving/batch"] },
  eval: { id: `${CLUSTERS.train.id}/eval`, name: "eval", clusterId: CLUSTERS.train.id, cohort: "ml-a100", flavor: "a100", nominal: 16, localQueues: ["ml-research/eval", "ml-notebooks/default"] },
  serving: { id: `${CLUSTERS.serve.id}/serving`, name: "serving", clusterId: CLUSTERS.serve.id, cohort: "", flavor: "l4", nominal: 16, localQueues: ["ml-serving/online"] },
} as const;

// ── Work: experiments, runs, phases, attempts ─────────────────────────────────

export const EXPERIMENTS = {
  support: { id: "exp_01JD2SUPPORT8B", name: "support-assistant-8b", objectiveKey: "val/loss", lowerIsBetter: true },
  reranker: { id: "exp_01JD2RERANKER", name: "reranker", objectiveKey: "eval/ndcg@10", lowerIsBetter: false },
} as const;

/** Run ids (`run_<ulid>`) and names; a RUN subject's label is its name, and URLs use it. */
export const RUNS = {
  v3: { id: "run_01JDV3SUPPORT8B", name: "llama-8b-support-v3" },
  v2: { id: "run_01JC92SUPPORT8B", name: "llama-8b-support-v2" },
  v1: { id: "run_01JBX1SUPPORT8B", name: "llama-8b-support-v1" },
  sweep1: { id: "run_01JD4SWEEPLR01", name: "llama-8b-support-lr-sweep-t1" },
  sweep2: { id: "run_01JD4SWEEPLR02", name: "llama-8b-support-lr-sweep-t2" },
  sweep3: { id: "run_01JD4SWEEPLR03", name: "llama-8b-support-lr-sweep-t3" },
  sweep4: { id: "run_01JD4SWEEPLR04", name: "llama-8b-support-lr-sweep-t4" },
  dpo: { id: "run_01JDDPOABLATE01", name: "llama-8b-support-v3-dpo-ablation" },
  reranker: { id: "run_01JDRERANKNIGHT", name: "reranker-nightly-20261125" },
} as const;

export const SWEEP_ID = "sweep_lr_2611";

/** A phase id: "<run_id>/<phase_key>". */
export const phaseId = (runId: string, phase: string) => `${runId}/${phase}`;
/** An attempt id: "<run_id>/<phase_key>/<n>". */
export const attemptId = (runId: string, phase: string, n: number) => `${runId}/${phase}/${n}`;
/** A rank id: "<run_id>/<phase_key>/<n>/<rank>". */
export const rankId = (runId: string, phase: string, n: number, rank: number) => `${runId}/${phase}/${n}/${rank}`;
/** A checkpoint id: "<run_id>/<phase_key>/<step>". */
export const checkpointId = (runId: string, phase: string, step: number) => `${runId}/${phase}/${step}`;

export type AttemptEnd =
  | "completed"
  | "preempted_by_queue"
  | "nccl_timeout"
  | "gpu_error"
  | "user_code_error"
  | "rewind"
  | "preempted_by_scheduler"
  | "running";

/** One attempt of the hero run, in the story's terms (offsets from now). */
export interface AttemptDef {
  phase: string;
  n: number;
  queuedAt: number;
  runningAt: number;
  /** null while running. */
  endedAt: number | null;
  startStep: number;
  endStep: number;
  lastCheckpointStep: number;
  end: AttemptEnd;
  endDetail: string;
  nodes: readonly string[];
  /** Steps per hour while running. */
  stepsPerHour: number;
}

const H100_NODES = POOLS.h100.nodes;
const WITH_SPARE = H100_NODES.map((n) => (n === BAD_NODE ? SPARE_NODE : n));

/** The hero run's attempts (llama-8b-support-v3), oldest first. */
export const V3_ATTEMPTS: readonly AttemptDef[] = [
  {
    phase: "pretrain",
    n: 1,
    queuedAt: -164 * HOUR,
    runningAt: -162.8 * HOUR,
    endedAt: -150 * HOUR,
    startStep: 0,
    endStep: 6400,
    lastCheckpointStep: 6000,
    end: "preempted_by_queue",
    endDetail: "Kueue InCohortReclamation: team-serving-batch reclaimed the 16 H100s team-research had borrowed",
    nodes: H100_NODES,
    stepsPerHour: 500,
  },
  {
    phase: "pretrain",
    n: 2,
    queuedAt: -150 * HOUR,
    runningAt: -148 * HOUR,
    endedAt: -120 * HOUR,
    startStep: 6000,
    endStep: 20000,
    lastCheckpointStep: 20000,
    end: "completed",
    endDetail: "Completed at step 20,000",
    nodes: H100_NODES,
    stepsPerHour: 500,
  },
  {
    phase: "sft",
    n: 1,
    queuedAt: -119.5 * HOUR,
    runningAt: -118.6 * HOUR,
    endedAt: -84 * HOUR,
    startStep: 0,
    endStep: 12400,
    lastCheckpointStep: 12000,
    end: "nccl_timeout",
    endDetail: "NCCL watchdog: allreduce seq 88213 timed out after 600 s; rank 37 never arrived",
    nodes: H100_NODES,
    stepsPerHour: 360,
  },
  {
    phase: "sft",
    n: 2,
    queuedAt: -84 * HOUR,
    runningAt: -83.6 * HOUR,
    endedAt: -26 * HOUR,
    startStep: 12000,
    endStep: 32750,
    lastCheckpointStep: 32000,
    end: "gpu_error",
    endDetail: `Xid 79 (GPU has fallen off the bus) on ${BAD_NODE} GPU ${BAD_GPU_INDEX}`,
    nodes: H100_NODES,
    stepsPerHour: 360,
  },
  {
    phase: "sft",
    n: 3,
    queuedAt: -26 * HOUR,
    // The recovery after the Xid: drain 95 s + reschedule 38 s + NCCL init 64 s + checkpoint load 318 s (training.ts).
    runningAt: -26 * HOUR + 515 * 1000,
    endedAt: null,
    startStep: 32000,
    endStep: 41200,
    lastCheckpointStep: 41000,
    end: "running",
    endDetail: "",
    nodes: WITH_SPARE,
    stepsPerHour: 359,
  },
];

export const V3_PHASES = {
  pretrain: { key: "pretrain", totalSteps: 20000, checkpointEvery: 2000 },
  sft: { key: "sft", totalSteps: 60000, checkpointEvery: 1000 },
  evalPeriodic: { key: "eval-periodic", everySteps: 2000 },
  rl: { key: "rl", totalSteps: 3000 },
} as const;

/** The SFT step now, and the run's parallelism. */
export const V3_NOW = { phase: "sft", attempt: 3, step: 41200, worldSize: 64, dp: 8, tp: 8, pp: 1, cp: 1, ep: 1, tokensPerStep: 4_194_304 } as const;

/** The straggler began this long ago. */
export const STRAGGLER_SINCE = -12 * HOUR;

// ── Checkpoints, models, versions ─────────────────────────────────────────────

export const MODELS = {
  support: { id: "support-assistant", name: "support-assistant", repository: "us-docker.pkg.dev/acme-ml/models/support-assistant" },
  reranker: { id: "bge-reranker", name: "bge-reranker", repository: "us-docker.pkg.dev/acme-ml/models/bge-reranker" },
} as const;

/** A model version's id: "<model_id>/<version>". */
export const versionId = (model: string, version: string) => `${model}/${version}`;

export const VERSIONS = {
  v1: { id: versionId(MODELS.support.id, "v1"), version: "v1", digest: "sha256:9a1c4f07e2b85d36c1aa0f4e3b7d92c58e61f0a4b3c2d1e0f9a8b7c6d5e4f3a2" },
  v2: { id: versionId(MODELS.support.id, "v2"), version: "v2", digest: "sha256:56ad1e9f8c7b6a5d4e3f2a1b0c9d8e7f6a5b4c3d2e1f0a9b8c7d6e5f4a3b2c1d" },
  v21fp8: { id: versionId(MODELS.support.id, "v2.1-fp8"), version: "v2.1-fp8", digest: "sha256:a1f30b7c2e9d4f8a6b5c3d1e0f2a4b6c8d0e1f3a5b7c9d2e4f6a8b0c1d3e5f7a" },
  reranker4: { id: versionId(MODELS.reranker.id, "v4"), version: "v4", digest: "sha256:3e7b9c1d5f2a8e4b6c0d9f1a3e5b7c2d4f6a8e0b1c3d5f7a9e2b4c6d8f0a1e3b" },
} as const;

/** The v3 checkpoint evaluated as the next candidate. */
export const V3_CANDIDATE_CHECKPOINT = checkpointId(RUNS.v3.id, "sft", 40000);

// ── Evals ─────────────────────────────────────────────────────────────────────

export const EVAL_SUITES = {
  gate: { id: "release-gate-v7", name: "release-gate", version: "v7" },
  lite: { id: "release-gate-lite-v2", name: "release-gate-lite", version: "v2" },
} as const;

export const EVAL_TASKS = ["mmlu_pro", "gsm8k", "ifeval", "arc_challenge", "truthfulqa", "long_context_ruler", "support_tickets_v3", "safety_refusals"] as const;

export const EVAL_RUNS = {
  v2: "eval_01JC9GATEV2",
  v21fp8: "eval_01JDAGATEV21FP8",
  v3ckpt: "eval_01JDVGATEV3SFT40K",
  v1: "eval_01JBXGATEV1",
  external: "eval_01JDAGATEGPT4OMINI",
} as const;

/** eval-periodic's run of the lite suite on a v3 SFT checkpoint. */
export const periodicEvalRunId = (step: number) => `eval_01JDVPERIODIC${step}`;

// ── Serving ───────────────────────────────────────────────────────────────────

/** The shop release prod runs (shopStory RUNNING), which the prod service's revision is. */
export const RELEASE_PROD = RUNNING["env-prod"].version;
export const RELEASE_PREV = "v1.8.2";
export const RELEASE_STAGING = RUNNING["env-staging"].version;

export const SERVICES = {
  prod: { id: "support-assistant-prod", name: "support-assistant", env: "prod" },
  staging: { id: "support-assistant-staging", name: "support-assistant", env: "staging" },
  reranker: { id: "reranker-prod", name: "reranker", env: "prod" },
} as const;

/** A revision's id: "<service_id>/<release>". The fp8 arm is "<release>+fp8". */
export const revisionId = (service: string, release: string) => `${service}/${release}`;

export const REVISIONS = {
  prodCurrent: revisionId(SERVICES.prod.id, RELEASE_PROD),
  prodFp8: revisionId(SERVICES.prod.id, `${RELEASE_PROD}+fp8`),
  prodPrev: revisionId(SERVICES.prod.id, RELEASE_PREV),
  staging: revisionId(SERVICES.staging.id, RELEASE_STAGING),
  reranker: revisionId(SERVICES.reranker.id, RELEASE_PROD),
} as const;

export const CHANGES_FP8 = { id: "chg_support_fp8", name: "support-assistant-fp8", trafficShare: 0.1 } as const;

// ── Traces, profiles, alerts ──────────────────────────────────────────────────

/** Trace ids share one namespace with shop's (ObservabilityService.GetTrace answers both): none may be one of shop's. */
export const TRACES = {
  slowStep: "6d3e1b9a0f2c4e7a8b5d1c3f9e0a2b4d",
  normalStep: "5c0a1f3e9b2d4c6a8e0f1a3b5c7d9e2f",
  checkpoint: "7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d",
  dataPipeline: "1f2e3d4c5b6a79881f2e3d4c5b6a7988",
  slowRequest: "9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b",
  normalRequest: "2b4d6f8a0c1e3a5c7e9b0d2f4a6c8e1b",
  podStart: "c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8",
} as const;

export const PROFILES = {
  kineto: "prof_v3_sft_a3_kineto_40800",
  flightRecorder: "prof_v3_sft_a1_nccl_flight_recorder",
  memory: "prof_v3_sft_a3_memory_snapshot",
  cpu: "prof_v3_sft_a3_cpu_continuous",
  gpu: "prof_v3_sft_a3_gpu_continuous",
  nsight: "prof_v3_sft_a3_nsys_41000",
} as const;

export const ALERTS = {
  xidFatal: "alert-xid-fatal",
  ncclHang: "alert-nccl-hang",
  lossSpike: "alert-loss-spike",
  stepStall: "alert-step-stall",
  straggler: "alert-straggler",
  gpuThermal: "alert-gpu-thermal",
  ttftSlo: "alert-ttft-slo",
  kvSaturation: "alert-kv-saturation",
} as const;

// ── Findings (Recommendations) ────────────────────────────────────────────────

/**
 * Every finding id, so any page can link one (a not-collected chart's fix, a
 * run's health check). findings.ts gives each its full body.
 */
export const FINDINGS = {
  // Health
  xid79: "f-xid79-r4vb-gpu5",
  thermal: "f-thermal-c8dw-gpu5",
  rowRemap: "f-row-remap-m1xp-gpu2",
  nvlinkCrc: "f-nvlink-crc-t5hn",
  // Waste
  idleNotebook: "f-idle-notebook-l4",
  idleEvalHolding: "f-idle-eval-a100",
  rerankerOverprovisioned: "f-reranker-overprovisioned",
  // Scheduling
  quotaStarved: "f-quota-starved-research",
  preemptionChurn: "f-preemption-cohort-reclaim",
  // Startup
  imagePull: "f-cold-start-image-pull",
  modelLoad: "f-model-load-registry",
  // Training efficiency
  straggler: "f-straggler-v3-rank37",
  checkpointBlocking: "f-checkpoint-blocking-v3",
  dataloaderShm: "f-dataloader-shm-64mi",
  // Inference
  fp8SafetyGate: "f-fp8-safety-gate",
  kvSaturation: "f-kv-cache-saturation-prod",
  // Coverage
  dcgmProfilingOff: "f-dcgm-profiling-off-serve",
  kubeletTracingOff: "f-kubelet-tracing-off",
  inferenceTracingOff: "f-inference-tracing-off-reranker",
  trainingUntracked: "f-runs-untracked-reranker",
  npdGpuRulesMissing: "f-npd-gpu-rules-serve",
  // Reliability
  checkpointInterval: "f-checkpoint-interval-pretrain",
  ncclTimeout: "f-nccl-timeout-default",
  noSpareServe: "f-no-hot-spare-serve",
} as const;

// ── Datasets ──────────────────────────────────────────────────────────────────

export const DATASETS = {
  domain: { id: "ds_support_corpus_v4", name: "support-corpus", version: "v4", tokens: 84_000_000_000 },
  sft: { id: "ds_support_sft_v3", name: "support-sft", version: "v3", tokens: 251_000_000_000 },
  prefs: { id: "ds_support_prefs_v2", name: "support-prefs", version: "v2", tokens: 4_200_000_000 },
  code: { id: "ds_stack_v2_dedup", name: "the-stack-v2-dedup", version: "2024-04", tokens: 12_000_000_000 },
} as const;

export const BASE_MODEL = "meta-llama/Llama-3.1-8B";

// ── Joins the domains share ───────────────────────────────────────────────────

/** A phase attempt's Kueue Workload and pod-group name: "llama-8b-support-v3-sft-a3". */
export const workloadName = (runName: string, phase: string, n: number) => `${runName}-${phase}-a${n}`;
/** A rank's pod: "llama-8b-support-v3-sft-a3-w-4" (one pod per node, 8 ranks each). */
export const rankPod = (runName: string, phase: string, n: number, rank: number, gpusPerNode = 8) => `${workloadName(runName, phase, n)}-w-${Math.floor(rank / gpusPerNode)}`;

/** v2's final SFT checkpoint (became support-assistant v2) and its FP8 quantization (became v2.1-fp8). */
export const V2_FINAL_CHECKPOINT = checkpointId(RUNS.v2.id, "sft", 60000);
export const V21_FP8_CHECKPOINT = checkpointId(RUNS.v2.id, "quantize", 1);

/** The workload that preempted v3 pretrain attempt 1 (team-serving-batch reclaiming its quota). */
export const PREEMPTOR_WORKLOAD = "batch-embed-refresh-1124";

/** The DPO ablation waits here: insufficient quota in team-research (its phase "dpo", attempt 1). */
export const DPO_PENDING = { queue: QUEUES.research.id, gpus: 32, priority: 100, priorityClass: "research-normal", waitingMs: 3 * HOUR + 12 * MINUTE } as const;

/** priya's notebook pod (idle-allocated: 2 L4 at 0% for 19h). */
export const NOTEBOOK_POD = { namespace: "ml-notebooks", pod: "jupyter-priya-0", gpus: 2, idleHours: 19 } as const;
