// Copyright (c) 2026 Reliant Labs

/**
 * The ML fixture world's Inference: ListMlInferenceServices,
 * GetMlInferenceService, and the INFERENCE_SERVICE / REVISION / REPLICA
 * series, histograms, events, alerts and request traces (plus the org's
 * notable serving events and alerts). Ids and the story: ./world.ts.
 *
 * THE SERVING STORY. support-assistant-prod runs vLLM on eks l4-serve, four
 * TP-2 replicas (8 L4). Release v1.8.3 rolled out 40 minutes ago and moved
 * vLLM 0.8.4 → 0.8.5: every replica pulled the 9.4 GB image again (3m31s, the
 * cold start's top stage) and TTFT p95 rose 4% (395 → 411 ms). vllm-0..2 serve
 * the control (model v2, 90%); vllm-3 is the arm of the fp8 Changes
 * experiment (v2.1-fp8, 10%, three days in): faster (TTFT −14%, ITL −23%,
 * tokens/s/GPU +31%) and a hair worse on guardrails (+0.4pt, significant),
 * with the release gate failing v2.1-fp8 on safety_refusals — so it stays an
 * experiment. KV cache runs 50–93%: vllm-1 peaks at saturation every
 * afternoon (preemptions, waiting requests, a liveness-probe restart 18 min
 * ago) → finding f-kv-cache-saturation-prod. The reranker (Triton, no token
 * generation) reports no tracing → f-inference-tracing-off-reranker.
 *
 * Replica pod names and their L4 placement are Fleet's (fleet.ts allocates
 * the devices to these REPLICA ids), so links resolve both ways.
 */

import type { MessageInitShape } from "@bufbuild/protobuf";

import {
  MlAlertState,
  MlAxis,
  MlCollection,
  MlCostCategory,
  MlEventKind,
  MlReleaseVerdict,
  MlSeverity,
  MlSpanStatus,
  MlSubjectKind,
  MlTraceKind,
  type MlHistogramSchema,
  type MlSpanSchema,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { prodRps } from "./curves";
import type { MlDomain } from "./domain";
import {
  ALERTS,
  at,
  CHANGES_FP8,
  CLUSTERS,
  DAY,
  FINDINGS,
  gpuUuid,
  hash,
  HOUR,
  MINUTE,
  ML_PROJECT,
  MODELS,
  noise,
  POOLS,
  RELEASE_PREV,
  RELEASE_PROD,
  RELEASE_STAGING,
  REVISIONS,
  revisionId,
  rng,
  secAt,
  SERVICES,
  TRACES,
  VERSIONS,
} from "./world";

type Now = () => number;

// ── Who serves what, where ────────────────────────────────────────────────────

const L4 = POOLS.l4;
const L4_NODE = (i: number) => L4.nodes[i];
const serveNodeId = (node: string) => `${CLUSTERS.serve.id}/${node}`;

/** The fp8 arm's revision while v1.8.2 was the release (it rides each release). */
export const PREV_FP8_REVISION = revisionId(SERVICES.prod.id, `${RELEASE_PREV}+fp8`);

/** The rollout of v1.8.3 (and the end of v1.8.2). */
export const RELEASE_AGO = 40 * MINUTE;
/** The fp8 Changes experiment ramped to 10%. */
export const RAMP_AGO = 3 * DAY;

export interface ReplicaDef {
  serviceId: string;
  pod: string;
  node: string;
  gpus: readonly number[];
  /** The revision it serves now. */
  revision: string;
  /** The revision it served before the release (prod only). */
  prevRevision?: string;
  /** Started (the release restarted it). */
  startedAgo: number;
  /** Absent from the window between these offsets (scaled down). */
  scaledDown?: readonly [number, number];
  /** Of its service's traffic. */
  share: number;
  /** KV cache ratio offset from the service mean (the hot one is +). */
  kvBias: number;
  restarts: number;
}

/** Every serving replica (Fleet's names and devices). */
export const INFERENCE_REPLICAS: readonly ReplicaDef[] = [
  { serviceId: SERVICES.prod.id, pod: "support-assistant-prod-vllm-0", node: L4_NODE(0), gpus: [0, 1], revision: REVISIONS.prodCurrent, prevRevision: REVISIONS.prodPrev, startedAgo: 38 * MINUTE, share: 0.3, kvBias: 0, restarts: 0 },
  { serviceId: SERVICES.prod.id, pod: "support-assistant-prod-vllm-1", node: L4_NODE(0), gpus: [2, 3], revision: REVISIONS.prodCurrent, prevRevision: REVISIONS.prodPrev, startedAgo: 36 * MINUTE, share: 0.3, kvBias: 0.05, restarts: 1 },
  { serviceId: SERVICES.prod.id, pod: "support-assistant-prod-vllm-2", node: L4_NODE(1), gpus: [0, 1], revision: REVISIONS.prodCurrent, prevRevision: REVISIONS.prodPrev, startedAgo: 34 * MINUTE, scaledDown: [21 * HOUR, 9.5 * HOUR], share: 0.3, kvBias: -0.03, restarts: 0 },
  { serviceId: SERVICES.prod.id, pod: "support-assistant-prod-vllm-3", node: L4_NODE(1), gpus: [2, 3], revision: REVISIONS.prodFp8, prevRevision: PREV_FP8_REVISION, startedAgo: 31 * MINUTE, share: 0.1, kvBias: -0.14, restarts: 0 },
  { serviceId: SERVICES.staging.id, pod: "support-assistant-staging-vllm-0", node: L4_NODE(2), gpus: [0], revision: REVISIONS.staging, startedAgo: 3 * HOUR, share: 1, kvBias: 0, restarts: 0 },
  { serviceId: SERVICES.reranker.id, pod: "reranker-prod-triton-0", node: L4_NODE(2), gpus: [1, 2], revision: REVISIONS.reranker, startedAgo: 2 * DAY + 4 * HOUR, share: 1, kvBias: 0, restarts: 0 },
];

export const replicaId = (r: Pick<ReplicaDef, "serviceId" | "pod">) => `${r.serviceId}/${r.pod}`;
export const replicaDevices = (r: ReplicaDef) => r.gpus.map((i) => gpuUuid(r.node, i));
const replicasOf = (serviceId: string) => INFERENCE_REPLICAS.filter((r) => r.serviceId === serviceId);
/** The short name a chart's group shows: "vllm-1". */
const podShort = (pod: string) => pod.split("-").slice(-2).join("-");

interface ServiceDef {
  id: string;
  name: string;
  env: string;
  modelId: string;
  versionId: string;
  revision: string;
  release: string;
  runtime: string;
  generative: boolean;
  gpusPerReplica: number;
  replicasDesired: number;
  /** Headlines over the last 24h. */
  ttftP95: number;
  itlP95: number;
  e2eP95: number;
  queueP95: number;
  errorRatio: number;
  sloAttainment: number;
  sloTarget: number;
  verdict: MlReleaseVerdict;
  comparing: boolean;
  tracing: boolean;
}

const SERVICE_DEFS: readonly ServiceDef[] = [
  {
    id: SERVICES.prod.id,
    name: SERVICES.prod.name,
    env: SERVICES.prod.env,
    modelId: MODELS.support.id,
    versionId: VERSIONS.v2.id,
    revision: REVISIONS.prodCurrent,
    release: RELEASE_PROD,
    runtime: "vLLM 0.8.5",
    generative: true,
    gpusPerReplica: 2,
    replicasDesired: 4,
    ttftP95: 411,
    itlP95: 38,
    e2eP95: 4100,
    queueP95: 27,
    errorRatio: 0.002,
    sloAttainment: 0.991,
    sloTarget: 0.99,
    verdict: MlReleaseVerdict.PASS,
    comparing: true,
    tracing: true,
  },
  {
    id: SERVICES.staging.id,
    name: SERVICES.staging.name,
    env: SERVICES.staging.env,
    modelId: MODELS.support.id,
    versionId: VERSIONS.v21fp8.id,
    revision: REVISIONS.staging,
    release: RELEASE_STAGING,
    runtime: "vLLM 0.8.5",
    generative: true,
    gpusPerReplica: 1,
    replicasDesired: 1,
    ttftP95: 298,
    itlP95: 31,
    e2eP95: 3020,
    queueP95: 3,
    errorRatio: 0.001,
    sloAttainment: 0.998,
    sloTarget: 0.99,
    verdict: MlReleaseVerdict.NOT_PROVEN,
    comparing: false,
    tracing: true,
  },
  {
    id: SERVICES.reranker.id,
    name: SERVICES.reranker.name,
    env: SERVICES.reranker.env,
    modelId: MODELS.reranker.id,
    versionId: VERSIONS.reranker4.id,
    revision: REVISIONS.reranker,
    release: RELEASE_PROD,
    runtime: "Triton 24.10",
    generative: false,
    gpusPerReplica: 2,
    replicasDesired: 1,
    ttftP95: NaN,
    itlP95: NaN,
    e2eP95: 46,
    queueP95: 3,
    errorRatio: 0.0004,
    sloAttainment: 0.9995,
    sloTarget: 0.999,
    verdict: MlReleaseVerdict.PASS,
    comparing: false,
    tracing: false,
  },
];

const serviceDef = (id: string) => SERVICE_DEFS.find((s) => s.id === id);

/** Prompt and output tokens per request (prod): 24h blended $/1M tokens comes out at ~$0.41. */
const TOKENS_IN_PER_REQ = 150.6;
const TOKENS_OUT_PER_REQ = 79.4;
/** Of the GPU-hour cost, the share attributed to prefill (input tokens). */
const INPUT_COST_SHARE = 0.3;

// ── Revisions ─────────────────────────────────────────────────────────────────

interface RevisionDef {
  id: string;
  serviceId: string;
  release: string;
  versionId: string;
  runtime: string;
  startedAgo: number;
  /** Null while it serves. */
  endedAgo: number | null;
  share: number;
  verdict: MlReleaseVerdict;
  verdictDetail: string;
  commit: string;
  fp8: boolean;
  spec: boolean;
}

const REVISION_DEFS: readonly RevisionDef[] = [
  {
    id: REVISIONS.prodCurrent,
    serviceId: SERVICES.prod.id,
    release: RELEASE_PROD,
    versionId: VERSIONS.v2.id,
    runtime: "vLLM 0.8.5",
    startedAgo: RELEASE_AGO,
    endedAgo: null,
    share: 1 - CHANGES_FP8.trafficShare,
    verdict: MlReleaseVerdict.PASS,
    verdictDetail: "obs check passed: TTFT p95 +4% (within the 10% budget), error rate and SLO attainment unchanged",
    commit: "8c41f2e",
    fp8: false,
    spec: false,
  },
  {
    id: REVISIONS.prodFp8,
    serviceId: SERVICES.prod.id,
    release: `${RELEASE_PROD}+fp8`,
    versionId: VERSIONS.v21fp8.id,
    runtime: "vLLM 0.8.5",
    startedAgo: RELEASE_AGO,
    endedAgo: null,
    share: CHANGES_FP8.trafficShare,
    verdict: MlReleaseVerdict.PENDING,
    verdictDetail: `Changes ${CHANGES_FP8.name} is still running (3 days); the release gate fails v2.1-fp8 on safety_refusals 0.979 < 0.98, so it stays at 10%`,
    commit: "8c41f2e",
    fp8: true,
    spec: true,
  },
  {
    id: REVISIONS.prodPrev,
    serviceId: SERVICES.prod.id,
    release: RELEASE_PREV,
    versionId: VERSIONS.v2.id,
    runtime: "vLLM 0.8.4",
    startedAgo: 2 * DAY + 6 * HOUR,
    endedAgo: RELEASE_AGO,
    share: 0,
    verdict: MlReleaseVerdict.PASS,
    verdictDetail: "obs check passed 2 days ago; replaced by v1.8.3",
    commit: "3a9d07b",
    fp8: false,
    spec: false,
  },
  {
    id: PREV_FP8_REVISION,
    serviceId: SERVICES.prod.id,
    release: `${RELEASE_PREV}+fp8`,
    versionId: VERSIONS.v21fp8.id,
    runtime: "vLLM 0.8.4",
    startedAgo: RAMP_AGO,
    endedAgo: RELEASE_AGO,
    share: 0,
    verdict: MlReleaseVerdict.PENDING,
    verdictDetail: `The fp8 arm of ${CHANGES_FP8.name} under v1.8.2; carried into v1.8.3`,
    commit: "3a9d07b",
    fp8: true,
    spec: true,
  },
  {
    id: REVISIONS.staging,
    serviceId: SERVICES.staging.id,
    release: RELEASE_STAGING,
    versionId: VERSIONS.v21fp8.id,
    runtime: "vLLM 0.8.5",
    startedAgo: 3 * HOUR,
    endedAgo: null,
    share: 1,
    verdict: MlReleaseVerdict.NOT_PROVEN,
    verdictDetail: "Too few requests in the after window to prove it (synthetic traffic only)",
    commit: "8c41f2e",
    fp8: true,
    spec: true,
  },
  {
    id: REVISIONS.reranker,
    serviceId: SERVICES.reranker.id,
    release: RELEASE_PROD,
    versionId: VERSIONS.reranker4.id,
    runtime: "Triton 24.10",
    startedAgo: 2 * DAY + 4 * HOUR,
    endedAgo: null,
    share: 1,
    verdict: MlReleaseVerdict.PASS,
    verdictDetail: "obs check passed (latency and errors unchanged)",
    commit: "8c41f2e",
    fp8: false,
    spec: false,
  },
];

const revisionDef = (id: string) => REVISION_DEFS.find((r) => r.id === id);

function revisionConfig(r: RevisionDef): { label: string; value: string }[] {
  if (r.serviceId === SERVICES.reranker.id) {
    return [
      { label: "backend", value: "onnxruntime (TensorRT EP)" },
      { label: "instance_group", value: "2 × KIND_GPU" },
      { label: "max_batch_size", value: "64" },
      { label: "dynamic_batching", value: "max_queue_delay 2 ms" },
      { label: "precision", value: "fp16" },
    ];
  }
  return [
    { label: "tensor_parallel_size", value: r.serviceId === SERVICES.staging.id ? "1" : "2" },
    { label: "max_num_seqs", value: "256" },
    { label: "max_model_len", value: "32768" },
    { label: "gpu_memory_utilization", value: "0.92" },
    { label: "dtype", value: r.fp8 ? "fp8 (e4m3 weights)" : "bfloat16" },
    { label: "kv_cache_dtype", value: r.fp8 ? "fp8" : "auto (bf16)" },
    { label: "enable_prefix_caching", value: "true" },
    { label: "speculative decoding", value: r.spec ? "ngram, num_speculative_tokens 4" : "off" },
    { label: "enable_lora", value: "true · max_loras 4 (support-de, support-ja)" },
    { label: "block_size", value: "16" },
  ];
}

// ── Deterministic curves ──────────────────────────────────────────────────────

const STEP_SEC = 5 * 60;
const POINTS = 288;

/** The 24h time grid (epoch seconds), 5-minute resolution, ending now. */
export function serveGrid(now: Now): number[] {
  const end = secAt(0, now);
  const last = end - (end % STEP_SEC);
  return Array.from({ length: POINTS + 1 }, (_, i) => last - (POINTS - i) * STEP_SEC);
}

const jit = (key: string, i: number) => noise(rng(hash(`${key}#${i}`)));
const hourOf = (x: number) => ((x % 86_400) + 86_400) % 86_400 / 3600;

/** A service's request rate at x (epoch seconds). */
function rpsAt(serviceId: string, x: number, i: number): number {
  const h = hourOf(x);
  if (serviceId === SERVICES.prod.id) return Math.max(2, prodRps(h));
  if (serviceId === SERVICES.staging.id) return 0.8 + 0.25 * Math.sin((h / 24) * 2 * Math.PI) + 0.05 * jit("stg-rps", i);
  return 18 * (0.45 + (0.55 * Math.max(2, prodRps(h))) / 42) + 0.6 * jit("rr-rps", i);
}

/** Load 0–1.1 (1 is the afternoon peak). */
const loadAt = (serviceId: string, x: number, i: number) => (serviceId === SERVICES.prod.id ? Math.max(2, prodRps(hourOf(x))) / 42 : serviceId === SERVICES.staging.id ? 0.06 : rpsAt(serviceId, x, i) / 30);

interface Latency {
  ttft: [number, number, number];
  itl: [number, number, number];
  e2e: [number, number, number];
  queue: [number, number, number];
}

/** p50 / p95 / p99 in ms of a service (or one revision of it) at load L. */
function latencyAt(def: ServiceDef, L: number, i: number, opts: { afterRelease: boolean; fp8: boolean }): Latency {
  const j = (k: string) => 1 + 0.03 * jit(`${def.id}-${k}`, i);
  if (!def.generative) {
    const e50 = (18 + 10 * L) * j("e50");
    const e95 = (34 + 16 * L) * j("e95");
    const q95 = (1 + 3 * L) * j("q95");
    return { ttft: [NaN, NaN, NaN], itl: [NaN, NaN, NaN], e2e: [e50, e95, e95 * 1.4], queue: [q95 * 0.4, q95, q95 * 1.8] };
  }
  const rel = opts.afterRelease ? 1.04 : 1;
  const f = opts.fp8 ? { ttft: 0.86, itl: 0.77, e2e: 0.8 } : { ttft: 1, itl: 1, e2e: 1 };
  const t50 = (150 + 110 * L * L) * rel * f.ttft * j("t50");
  const t95 = (250 + 205 * L * L) * rel * f.ttft * j("t95");
  const i50 = (7 + 4 * L) * f.itl * j("i50");
  const i95 = (26 + 13 * L) * f.itl * j("i95");
  const e50 = (1500 + 600 * L) * f.e2e * j("e50");
  const e95 = (3400 + 820 * L) * f.e2e * j("e95");
  const q50 = (2 + 6 * L ** 3) * j("q50");
  const q95 = (4 + 34 * L ** 3) * j("q95");
  return { ttft: [t50, t95, t95 * 1.6], itl: [i50, i95, i95 * 1.5], e2e: [e50, e95, e95 * 1.35], queue: [q50, q95, q95 * 2.2] };
}

/** The KV cache ratio at load L: ~50% at night, ~89% at the afternoon peak (vllm-1 ~94%). */
const kvAt = (L: number, bias: number, i: number, key: string) => Math.min(0.97, Math.max(0.05, 0.46 + 0.4 * L + bias + 0.012 * jit(key, i)));

// ── Series ────────────────────────────────────────────────────────────────────

interface SeriesInit {
  key: string;
  label: string;
  unit: string;
  axis: MlAxis;
  x: number[];
  y: number[];
  group?: string;
  resolution: number;
  collection: MlCollection;
  missingSource?: string;
  fixFindingId?: string;
  stat?: string;
  subject: { kind: MlSubjectKind; id: string; label: string };
}

const KEY_META: Record<string, { label: string; unit: string }> = {
  "serve.rps": { label: "Requests/s", unit: "" },
  "serve.ttft": { label: "Time to first token", unit: "ms" },
  "serve.itl": { label: "Inter-token latency", unit: "ms" },
  "serve.e2e": { label: "End-to-end latency", unit: "ms" },
  "serve.queue_time": { label: "Queue wait", unit: "ms" },
  "serve.time_split": { label: "Where time goes (p50)", unit: "ms" },
  "serve.tokens_in_per_s": { label: "Input tokens/s", unit: "tokens/s" },
  "serve.tokens_out_per_s": { label: "Output tokens/s", unit: "tokens/s" },
  "serve.error_ratio": { label: "Error rate", unit: "ratio" },
  "serve.replicas": { label: "Replicas", unit: "count" },
  "serve.cost_usd_per_hour": { label: "Cost", unit: "usd" },
  "engine.running": { label: "Running requests", unit: "count" },
  "engine.waiting": { label: "Waiting requests", unit: "count" },
  "engine.batch_size": { label: "Batch size (requests per step)", unit: "count" },
  "engine.kv_cache_ratio": { label: "KV cache usage", unit: "ratio" },
  "engine.preemptions": { label: "Preemptions per 5 min", unit: "count" },
  "engine.prefix_cache_hit_ratio": { label: "Prefix cache hit rate", unit: "ratio" },
  "engine.spec_decode_acceptance": { label: "Speculative decoding acceptance", unit: "ratio" },
  "gpu.util": { label: "GPU utilisation", unit: "ratio" },
};

export const INFERENCE_SERIES_KEYS = Object.keys(KEY_META);

/** One series' frame: what every series of a request shares. */
interface Frame {
  def: ServiceDef;
  subject: SeriesInit["subject"];
  xs: number[];
  /** Traffic share of the whole service this subject carries at x (0 outside its life). */
  share: (x: number, i: number) => number;
  /** Is this subject's traffic the fp8 arm's at x? */
  fp8: (x: number) => boolean;
  /** Replicas of this subject at x (for per-replica means). */
  replicas: (x: number) => number;
  kvBias: number;
  releaseSec: number;
  specOn: boolean;
}

function base(frame: Frame, key: string, extra: Partial<SeriesInit> = {}): SeriesInit {
  const meta = KEY_META[key] ?? { label: key, unit: "" };
  return { key, label: meta.label, unit: meta.unit, axis: MlAxis.TIME, x: frame.xs, y: [], resolution: STEP_SEC, collection: MlCollection.COLLECTED, subject: frame.subject, ...extra };
}

const notApplicable = (frame: Frame, key: string, why: string, extra: Partial<SeriesInit> = {}): SeriesInit => ({ ...base(frame, key, extra), x: [], y: [], collection: MlCollection.NOT_APPLICABLE, missingSource: why });

/** y for each x, NaN where the subject did not exist. */
function ys(frame: Frame, f: (x: number, i: number, L: number, share: number) => number): number[] {
  return frame.xs.map((x, i) => {
    const share = frame.share(x, i);
    if (share <= 0) return NaN;
    return f(x, i, loadAt(frame.def.id, x, i), share);
  });
}

function seriesForKey(frame: Frame, key: string, groupBy: string | undefined): SeriesInit[] {
  const { def } = frame;
  const lat = (x: number, i: number, L: number) => latencyAt(def, L, i, { afterRelease: x >= frame.releaseSec, fp8: frame.fp8(x) });
  const notGen = "Triton cross-encoder: no token generation, so no TTFT, ITL or KV cache";
  switch (key) {
    case "serve.rps": {
      if (groupBy === "finish_reason") {
        if (!def.generative) return [notApplicable(frame, key, notGen)];
        return FINISH_REASONS.map(([name, share]) => base(frame, key, { group: name, y: ys(frame, (x, i, _L, s) => rpsAt(def.id, x, i) * s * share) }));
      }
      if (groupBy === "status") {
        const statuses: [string, number][] = [
          ["2xx", 1 - def.errorRatio - 0.004],
          ["4xx", 0.004],
          ["5xx", def.errorRatio],
        ];
        return statuses.map(([name, share]) => base(frame, key, { group: name, y: ys(frame, (x, i, _L, s) => rpsAt(def.id, x, i) * s * share) }));
      }
      return [base(frame, key, { y: ys(frame, (x, i, _L, s) => rpsAt(def.id, x, i) * s) })];
    }
    case "serve.ttft":
    case "serve.itl":
    case "serve.e2e":
    case "serve.queue_time": {
      const field = key === "serve.ttft" ? "ttft" : key === "serve.itl" ? "itl" : key === "serve.e2e" ? "e2e" : "queue";
      if (!def.generative && (field === "ttft" || field === "itl")) return [notApplicable(frame, key, notGen)];
      return (["p50", "p95", "p99"] as const).map((stat, k) => base(frame, key, { stat, group: stat, y: ys(frame, (x, i, L) => lat(x, i, L)[field][k]) }));
    }
    case "serve.time_split": {
      if (!def.generative) {
        return [
          base(frame, key, { group: "queue", y: ys(frame, (x, i, L) => lat(x, i, L).queue[0]) }),
          base(frame, key, { group: "inference", y: ys(frame, (x, i, L) => lat(x, i, L).e2e[0] - lat(x, i, L).queue[0]) }),
        ];
      }
      return [
        base(frame, key, { group: "queue", y: ys(frame, (x, i, L) => lat(x, i, L).queue[0]) }),
        base(frame, key, { group: "prefill", y: ys(frame, (x, i, L) => lat(x, i, L).ttft[0] - lat(x, i, L).queue[0]) }),
        base(frame, key, { group: "decode", y: ys(frame, (x, i, L) => lat(x, i, L).e2e[0] - lat(x, i, L).ttft[0]) }),
      ];
    }
    case "serve.tokens_in_per_s":
    case "serve.tokens_out_per_s": {
      if (!def.generative && key === "serve.tokens_out_per_s") return [notApplicable(frame, key, notGen)];
      const per = key === "serve.tokens_in_per_s" ? (def.generative ? TOKENS_IN_PER_REQ : 420) : TOKENS_OUT_PER_REQ;
      return [base(frame, key, { y: ys(frame, (x, i, _L, s) => rpsAt(def.id, x, i) * s * per) })];
    }
    case "serve.error_ratio":
      return [base(frame, key, { y: ys(frame, (_x, i) => def.errorRatio * (1 + 0.25 * jit(`${def.id}-err`, i))) })];
    case "serve.replicas": {
      const ready = ys(frame, (x) => frame.replicas(x));
      const desired = frame.xs.map((x, i) => (Number.isFinite(ready[i]) ? frame.replicas(x + STEP_SEC) : NaN));
      return [base(frame, key, { group: "ready", y: ready }), base(frame, key, { group: "", stat: "desired", y: desired })];
    }
    case "serve.cost_usd_per_hour":
      return [base(frame, key, { y: ys(frame, (x) => frame.replicas(x) * def.gpusPerReplica * L4.usdPerGpuHour) })];
    case "engine.running":
      return [base(frame, key, { y: ys(frame, (x, i, L, s) => Math.round((def.generative ? 18 + 135 * L : 4 + 12 * L) * Math.max(1, frame.replicas(x)) * (s < 1 ? Math.min(1, s * 3.4) : 1) * (1 + 0.05 * jit(`${frame.subject.id}-run`, i)))) })];
    case "engine.waiting":
      if (!def.generative) return [base(frame, key, { y: ys(frame, () => 0) })];
      return [base(frame, key, { y: ys(frame, (_x, i, L) => Math.max(0, Math.round((kvAt(L, frame.kvBias, i, `${frame.subject.id}-kv`) - 0.84) * 260))) })];
    case "engine.batch_size":
      return [base(frame, key, { y: ys(frame, (_x, i, L) => (def.generative ? 18 + 135 * L : 4 + 12 * L) * (1 + 0.04 * jit(`${frame.subject.id}-bs`, i))) })];
    case "engine.kv_cache_ratio": {
      if (!def.generative) return [notApplicable(frame, key, notGen)];
      return [base(frame, key, { y: ys(frame, (_x, i, L) => kvAt(L, frame.kvBias, i, `${frame.subject.id}-kv`)) })];
    }
    case "engine.preemptions":
      if (!def.generative) return [notApplicable(frame, key, notGen)];
      return [base(frame, key, { y: ys(frame, (_x, i, L) => Math.max(0, Math.round((kvAt(L, frame.kvBias, i, `${frame.subject.id}-kv`) - 0.9) * 700))) })];
    case "engine.prefix_cache_hit_ratio":
      if (!def.generative) return [notApplicable(frame, key, notGen)];
      return [base(frame, key, { y: ys(frame, (_x, i) => 0.61 + 0.04 * jit(`${def.id}-pfx`, i)) })];
    case "engine.spec_decode_acceptance":
      if (!def.generative) return [notApplicable(frame, key, notGen)];
      if (!frame.specOn) return [notApplicable(frame, key, `Speculative decoding is off on ${frame.subject.label} (no speculative_config); only the fp8 arm runs ngram drafts`)];
      return [base(frame, key, { y: ys(frame, (x, i) => (frame.fp8(x) ? 0.58 + 0.04 * jit(`${def.id}-spec`, i) : NaN)) })];
    case "gpu.util":
      return [base(frame, key, { y: ys(frame, (_x, i, L) => Math.min(0.99, Math.max(0.02, (def.generative ? 0.12 + 0.5 * L : 0.04 + 0.12 * L) + frame.kvBias * 0.6 + 0.03 * jit(`${frame.subject.id}-util`, i)))) })];
    default:
      return [];
  }
}

const FINISH_REASONS: readonly [string, number][] = [
  ["stop", 0.81],
  ["length", 0.15],
  ["abort", 0.038],
  ["error", 0.002],
];

const ADAPTERS: readonly [string, number][] = [
  ["base (support-assistant)", 0.7],
  ["support-de", 0.18],
  ["support-ja", 0.12],
];

const exists = (x: number, startSec: number, endSec: number | null) => x >= startSec && (endSec === null || x < endSec);

/** Replicas of `serviceId` at x: prod scaled 4 → 3 at -21h and back to 4 at -9.5h. */
function serviceReplicasAt(serviceId: string, x: number, now: Now): number {
  return replicasOf(serviceId).filter((r) => !(r.scaledDown && x >= secAt(-r.scaledDown[0], now) && x < secAt(-r.scaledDown[1], now))).length;
}

function serviceFrame(def: ServiceDef, now: Now): Frame {
  return {
    def,
    subject: { kind: MlSubjectKind.INFERENCE_SERVICE, id: def.id, label: `${def.name} (${def.env})` },
    xs: serveGrid(now),
    share: () => 1,
    fp8: () => false,
    replicas: (x) => serviceReplicasAt(def.id, x, now),
    kvBias: 0,
    releaseSec: def.id === SERVICES.prod.id ? secAt(-RELEASE_AGO, now) : Infinity,
    specOn: def.id === SERVICES.staging.id,
  };
}

function revisionFrame(rev: RevisionDef, now: Now): Frame {
  const def = serviceDef(rev.serviceId)!;
  const start = secAt(-rev.startedAgo, now);
  const end = rev.endedAgo === null ? null : secAt(-rev.endedAgo, now);
  const share = rev.share || (rev.fp8 ? CHANGES_FP8.trafficShare : 1 - CHANGES_FP8.trafficShare);
  const reps = replicasOf(rev.serviceId).filter((r) => r.revision === rev.id || r.prevRevision === rev.id);
  return {
    def,
    subject: { kind: MlSubjectKind.REVISION, id: rev.id, label: rev.release },
    xs: serveGrid(now),
    share: (x) => (exists(x, start, end) ? share : 0),
    fp8: () => rev.fp8,
    replicas: (x) => reps.filter((r) => !(r.scaledDown && x >= secAt(-r.scaledDown[0], now) && x < secAt(-r.scaledDown[1], now))).length,
    kvBias: rev.fp8 ? -0.14 : 0,
    releaseSec: rev.release === RELEASE_PROD ? -Infinity : Infinity,
    specOn: rev.spec,
  };
}

function replicaFrame(r: ReplicaDef, now: Now): Frame {
  const def = serviceDef(r.serviceId)!;
  const releaseSec = secAt(-RELEASE_AGO, now);
  const isFp8 = r.revision === REVISIONS.prodFp8 || r.revision === REVISIONS.staging;
  return {
    def,
    subject: { kind: MlSubjectKind.REPLICA, id: replicaId(r), label: podShort(r.pod) },
    xs: serveGrid(now),
    share: (x) => {
      if (r.scaledDown && x >= secAt(-r.scaledDown[0], now) && x < secAt(-r.scaledDown[1], now)) return 0;
      // The other control replicas take vllm-2's share while it is scaled down.
      if (r.serviceId === SERVICES.prod.id && !isFp8 && r.pod !== "support-assistant-prod-vllm-2" && x >= secAt(-21 * HOUR, now) && x < secAt(-9.5 * HOUR, now)) return 0.45;
      return r.share;
    },
    fp8: () => isFp8,
    replicas: () => 1,
    kvBias: r.kvBias,
    releaseSec,
    specOn: isFp8,
  };
}

/** The per-replica (or per-arm, per-revision) breakdown of a service read. */
function groupedFrames(def: ServiceDef, groupBy: string, now: Now): Frame[] {
  if (groupBy === "replica") return replicasOf(def.id).map((r) => replicaFrame(r, now));
  if (groupBy === "revision") return REVISION_DEFS.filter((r) => r.serviceId === def.id).map((r) => revisionFrame(r, now));
  if (groupBy === "arm" && def.id === SERVICES.prod.id) {
    const control = serviceFrame(def, now);
    return [
      { ...control, subject: { kind: MlSubjectKind.REVISION, id: REVISIONS.prodCurrent, label: "control · v2" }, share: () => 1 - CHANGES_FP8.trafficShare, replicas: (x) => serviceReplicasAt(def.id, x, now) - 1 },
      { ...control, subject: { kind: MlSubjectKind.REVISION, id: REVISIONS.prodFp8, label: "fp8 · v2.1-fp8" }, share: () => CHANGES_FP8.trafficShare, fp8: () => true, replicas: () => 1, kvBias: -0.14, specOn: true },
    ];
  }
  return [];
}

function querySeries(frame: Frame, keys: readonly string[], groupBy: string | undefined, now: Now): SeriesInit[] {
  const wanted = keys.length === 0 ? INFERENCE_SERIES_KEYS : keys.flatMap((k) => (k.endsWith("/*") || k.endsWith(".*") ? INFERENCE_SERIES_KEYS.filter((kk) => kk.startsWith(k.slice(0, -1))) : [k]));
  const per = groupBy && groupBy !== "status" && groupBy !== "finish_reason" && frame.subject.kind === MlSubjectKind.INFERENCE_SERVICE ? groupedFrames(frame.def, groupBy, now) : [];
  if (per.length > 0) {
    return wanted.flatMap((key) =>
      per.flatMap((f) =>
        seriesForKey(f, key, undefined).map((s) => ({ ...s, group: s.stat && s.stat !== "p95" ? `${f.subject.label} ${s.stat}` : f.subject.label, stat: s.stat === "p95" ? "" : s.stat })),
      ),
    );
  }
  return wanted.flatMap((key) => seriesForKey(frame, key, groupBy));
}

// ── Headline numbers ──────────────────────────────────────────────────────────

function mean(values: number[]): number {
  const f = values.filter(Number.isFinite);
  return f.length ? f.reduce((a, b) => a + b, 0) / f.length : NaN;
}

/** A service's 24h numbers, from the same curves its charts draw. */
export function serviceHeadline(id: string, now: Now) {
  const def = serviceDef(id)!;
  const frame = serviceFrame(def, now);
  const rps = seriesForKey(frame, "serve.rps", undefined)[0].y;
  const rpsNow = rps[rps.length - 1];
  const avgRps = mean(rps);
  const replicas = replicasOf(id).length;
  const gpus = replicas * def.gpusPerReplica;
  const usdPerHour = gpus * L4.usdPerGpuHour;
  const tokIn = def.generative ? avgRps * TOKENS_IN_PER_REQ : NaN;
  const tokOut = def.generative ? avgRps * TOKENS_OUT_PER_REQ : NaN;
  const usdPerMillion = def.generative ? usdPerHour / (((tokIn + tokOut) * 3600) / 1e6) : NaN;
  const kv = def.generative ? mean(seriesForKey(frame, "engine.kv_cache_ratio", undefined)[0].y) : NaN;
  const busy = mean(seriesForKey(frame, "gpu.util", undefined)[0].y);
  return { def, rps, rpsNow, avgRps, replicas, gpus, usdPerHour, tokIn, tokOut, usdPerMillion, kv, busy, requests24h: avgRps * 86_400 };
}

function serviceMessage(id: string, now: Now) {
  const h = serviceHeadline(id, now);
  const { def } = h;
  const gpuHours = h.gpus * 24;
  // A 15-minute sparkline of the same curve.
  const grid = serveGrid(now);
  const sx: number[] = [];
  const sy: number[] = [];
  grid.forEach((x, i) => {
    if (i % 3 === 0) {
      sx.push(x);
      sy.push(h.rps[i]);
    }
  });
  return {
    id: def.id,
    name: def.name,
    project: ML_PROJECT,
    env: def.env,
    clusterId: CLUSTERS.serve.id,
    poolId: L4.id,
    modelId: def.modelId,
    modelVersionId: def.versionId,
    revisionId: def.revision,
    release: def.release,
    runtime: def.runtime,
    accelerators: h.gpus,
    acceleratorProduct: L4.product,
    replicas: h.replicas,
    replicasDesired: def.replicasDesired,
    rps: h.rpsNow,
    ttftP95Ms: def.ttftP95,
    itlP95Ms: def.itlP95,
    e2eP95Ms: def.e2eP95,
    queueP95Ms: def.queueP95,
    errorRatio: def.errorRatio,
    sloAttainment: def.sloAttainment,
    sloTarget: def.sloTarget,
    kvCacheRatio: h.kv,
    busyRatio: h.busy,
    tokensPerSecond: h.tokOut,
    usdPerMillionTokens: h.usdPerMillion,
    cost: {
      gpuHours,
      usd: gpuHours * L4.usdPerGpuHour,
      priced: true,
      byCategory: [{ category: MlCostCategory.PRODUCTIVE, gpuHours: gpuHours * h.busy, usd: gpuHours * h.busy * L4.usdPerGpuHour }, { category: MlCostCategory.IDLE_ALLOCATED, gpuHours: gpuHours * (1 - h.busy), usd: gpuHours * (1 - h.busy) * L4.usdPerGpuHour }],
    },
    verdict: def.verdict,
    rpsSeries: { key: "serve.rps", label: "Requests/s", unit: "", axis: MlAxis.TIME, x: sx, y: sy, resolution: 15 * 60, collection: MlCollection.COLLECTED, subject: { kind: MlSubjectKind.INFERENCE_SERVICE, id: def.id, label: def.name } },
    comparing: def.comparing,
  };
}

// ── Detail ────────────────────────────────────────────────────────────────────

const SLI_BEFORE_AFTER: Record<string, { label: string; unit: string; before: number; after: number; significant: boolean; regression: boolean }[]> = {
  [REVISIONS.prodCurrent]: [
    { label: "TTFT p95", unit: "ms", before: 395, after: 411, significant: true, regression: true },
    { label: "ITL p95", unit: "ms", before: 37.6, after: 38, significant: false, regression: false },
    { label: "e2e p95", unit: "ms", before: 4050, after: 4100, significant: false, regression: false },
    { label: "Queue p95", unit: "ms", before: 26, after: 27, significant: false, regression: false },
    { label: "Error rate", unit: "ratio", before: 0.0021, after: 0.002, significant: false, regression: false },
    { label: "SLO attainment", unit: "ratio", before: 0.992, after: 0.991, significant: false, regression: false },
  ],
  [REVISIONS.prodFp8]: [
    { label: "TTFT p95", unit: "ms", before: 411, after: 353, significant: true, regression: false },
    { label: "ITL p95", unit: "ms", before: 38, after: 29.3, significant: true, regression: false },
    { label: "Tokens/s per GPU", unit: "tokens/s", before: 229, after: 300, significant: true, regression: false },
    { label: "Error rate", unit: "ratio", before: 0.002, after: 0.002, significant: false, regression: false },
    { label: "Guardrail trigger rate", unit: "ratio", before: 0.031, after: 0.035, significant: true, regression: true },
  ],
  [REVISIONS.prodPrev]: [
    { label: "TTFT p95", unit: "ms", before: 398, after: 395, significant: false, regression: false },
    { label: "Error rate", unit: "ratio", before: 0.0022, after: 0.0021, significant: false, regression: false },
  ],
  [REVISIONS.staging]: [{ label: "TTFT p95", unit: "ms", before: 341, after: 298, significant: false, regression: false }],
  [REVISIONS.reranker]: [{ label: "e2e p95", unit: "ms", before: 47, after: 46, significant: false, regression: false }],
};

const sliKey = (label: string) => `serve.${label.toLowerCase().replace(/[^a-z0-9]+/g, "_").replace(/_$/, "")}`;

function revisionMessage(r: RevisionDef, now: Now) {
  return {
    id: r.id,
    release: r.release,
    modelVersionId: r.versionId,
    runtime: r.runtime,
    config: revisionConfig(r),
    startedAt: at(-r.startedAgo, now),
    ...(r.endedAgo !== null ? { endedAt: at(-r.endedAgo, now) } : {}),
    trafficShare: r.share,
    verdict: r.verdict,
    verdictDetail: r.verdictDetail,
    deltas: (SLI_BEFORE_AFTER[r.id] ?? []).map((d) => ({ key: sliKey(d.label), ...d })),
    commit: r.commit,
  };
}

/** Each replica's live numbers (now). */
function replicaMessage(r: ReplicaDef, now: Now) {
  const def = serviceDef(r.serviceId)!;
  const xs = serveGrid(now);
  const i = xs.length - 1;
  const L = loadAt(def.id, xs[i], i);
  const kv = def.generative ? kvAt(L, r.kvBias, i, `${replicaId(r)}-kv`) : NaN;
  const lat = latencyAt(def, L, i, { afterRelease: true, fp8: r.revision === REVISIONS.prodFp8 || r.revision === REVISIONS.staging });
  const running = Math.round((def.generative ? 18 + 135 * L : 4 + 12 * L) * (r.share < 1 ? Math.min(1, r.share * 3.4) : 1));
  return {
    id: replicaId(r),
    pod: r.pod,
    nodeId: serveNodeId(r.node),
    deviceUuids: replicaDevices(r),
    revisionId: r.revision,
    state: "ready",
    busyRatio: Math.min(0.99, (def.generative ? 0.12 + 0.5 * L : 0.04 + 0.12 * L) + r.kvBias * 0.6),
    kvCacheRatio: kv,
    running,
    waiting: def.generative ? Math.max(0, Math.round((kv - 0.84) * 260)) : 0,
    ttftP95Ms: lat.ttft[1] * (1 + r.kvBias),
    restarts: r.restarts,
    startedAt: at(-r.startedAgo, now),
    rps: rpsAt(def.id, xs[i], i) * r.share,
  };
}

/** The cold start of a serving replica, stage by stage (seconds). */
const COLD_START: readonly { stage: string; p50: number; p95: number; cause: string; finding?: string }[] = [
  { stage: "queued", p50: 0, p95: 2, cause: "Admitted at once (serving queue has quota)" },
  { stage: "scheduling", p50: 3, p95: 6, cause: "—" },
  { stage: "node provisioning", p50: 0, p95: 0, cause: "Warm pool: no node added" },
  { stage: "sandbox/init", p50: 12, p95: 19, cause: "model-downloader init container (adapters)" },
  { stage: "image pull", p50: 211, p95: 236, cause: "vllm/vllm-openai:v0.8.5, 9.4 GB, not cached on the nodes", finding: FINDINGS.imagePull },
  { stage: "model load", p50: 58, p95: 71, cause: "16 GB of weights from the registry, then CUDA graph capture", finding: FINDINGS.modelLoad },
  { stage: "ready", p50: 6, p95: 9, cause: "Readiness probe /health" },
];

function coldStart(def: ServiceDef) {
  const stages = def.generative ? COLD_START : COLD_START.map((s) => (s.stage === "image pull" ? { ...s, p50: 74, p95: 88, cause: "nvcr.io/nvidia/tritonserver:24.10, 6.1 GB" } : s.stage === "model load" ? { ...s, p50: 9, p95: 12, cause: "1.1 GB ONNX model from the registry", finding: undefined } : s));
  const total = stages.reduce((a, s) => a + s.p50, 0);
  return {
    workload: def.id,
    sampleCount: def.id === SERVICES.prod.id ? 14 : 3,
    stages: stages.map((s) => ({ stage: s.stage, p50Seconds: s.p50, p95Seconds: s.p95, share: total ? s.p50 / total : 0, topCause: s.cause, findingId: s.finding ?? "" })),
  };
}

function coldStartWaterfall(r: ReplicaDef, now: Now) {
  const def = serviceDef(r.serviceId)!;
  const cs = coldStart(def).stages;
  const h = hash(r.pod);
  let t = 0;
  const stages = cs.map((s, k) => {
    // Each replica varies a little around the p50; the first one pulled from a cold node.
    const seconds = s.p50Seconds === 0 ? 0 : s.p50Seconds * (0.9 + ((h >> (k * 3)) % 20) / 100);
    const out = { stage: s.stage, startSeconds: t, seconds, detail: s.topCause };
    t += seconds;
    return out;
  });
  return {
    pod: r.pod,
    workload: { kind: MlSubjectKind.REPLICA, id: replicaId(r), label: podShort(r.pod) },
    createdAt: at(Math.round(-r.startedAgo - t * 1000), now),
    stages,
  };
}

function servingCost(id: string, now: Now) {
  const h = serviceHeadline(id, now);
  const idle = id === SERVICES.reranker.id ? 1 : 0;
  return {
    gpuHours24h: h.gpus * 24,
    usdPerHour: h.usdPerHour,
    usdPerMillionInputTokens: h.def.generative ? (h.usdPerHour * INPUT_COST_SHARE) / ((h.tokIn * 3600) / 1e6) : NaN,
    usdPerMillionOutputTokens: h.def.generative ? (h.usdPerHour * (1 - INPUT_COST_SHARE)) / ((h.tokOut * 3600) / 1e6) : NaN,
    idleReplicas: idle,
    idleUsdPerDay: idle ? L4.usdPerGpuHour * 24 : 0,
  };
}

function comparison(now: Now) {
  const m = (key: string, label: string, unit: string, value: number, lo: number, hi: number, delta: number, significant: boolean, better: boolean) => ({ key, label, unit, value, ciLow: lo, ciHigh: hi, deltaRatio: delta, significant, better });
  return {
    id: CHANGES_FP8.id,
    name: CHANGES_FP8.name,
    status: "running",
    startedAt: at(-RAMP_AGO, now),
    controlArm: "control",
    decision: "",
    arms: [
      {
        arm: "control",
        revisionId: REVISIONS.prodCurrent,
        modelVersionId: VERSIONS.v2.id,
        trafficShare: 1 - CHANGES_FP8.trafficShare,
        requests: 5_382_410n,
        metrics: [
          m("serve.ttft_p95", "TTFT p95", "ms", 411, 404, 418, 0, false, false),
          m("serve.itl_p95", "ITL p95", "ms", 38, 37.4, 38.6, 0, false, false),
          m("serve.tokens_per_s_per_gpu", "Tokens/s per GPU", "tokens/s", 229, 226, 232, 0, false, false),
          m("serve.error_ratio", "Error rate", "ratio", 0.002, 0.0018, 0.0022, 0, false, false),
          m("eval.judge_score", "Judge score (1–5)", "", 4.21, 4.18, 4.24, 0, false, false),
          m("guardrail.trigger_ratio", "Guardrail trigger rate", "ratio", 0.031, 0.029, 0.033, 0, false, false),
        ],
      },
      {
        arm: "fp8",
        revisionId: REVISIONS.prodFp8,
        modelVersionId: VERSIONS.v21fp8.id,
        trafficShare: CHANGES_FP8.trafficShare,
        requests: 598_046n,
        metrics: [
          m("serve.ttft_p95", "TTFT p95", "ms", 353, 341, 366, -0.14, true, true),
          m("serve.itl_p95", "ITL p95", "ms", 29.3, 28.6, 30.1, -0.23, true, true),
          m("serve.tokens_per_s_per_gpu", "Tokens/s per GPU", "tokens/s", 300, 293, 307, 0.31, true, true),
          m("serve.error_ratio", "Error rate", "ratio", 0.002, 0.0015, 0.0026, 0, false, false),
          m("eval.judge_score", "Judge score (1–5)", "", 4.185, 4.11, 4.26, -0.006, false, false),
          m("guardrail.trigger_ratio", "Guardrail trigger rate", "ratio", 0.035, 0.032, 0.038, 0.129, true, false),
        ],
      },
    ],
  };
}

function detail(id: string, now: Now) {
  const def = serviceDef(id);
  if (!def) return undefined;
  const h = serviceHeadline(id, now);
  const revisions = REVISION_DEFS.filter((r) => r.serviceId === id);
  const current = revisionDef(def.revision)!;
  const reps = replicasOf(id);
  const shares = (rows: readonly [string, number][]) => rows.map(([name, share]) => ({ name, value: Math.round(h.requests24h * share), share }));
  const findings: string[] =
    id === SERVICES.prod.id ? [FINDINGS.kvSaturation, FINDINGS.imagePull, FINDINGS.modelLoad, FINDINGS.fp8SafetyGate, FINDINGS.dcgmProfilingOff, FINDINGS.noSpareServe] : id === SERVICES.reranker.id ? [FINDINGS.inferenceTracingOff, FINDINGS.rerankerOverprovisioned] : [FINDINGS.imagePull];
  return {
    service: serviceMessage(id, now),
    revisions: revisions.map((r) => revisionMessage(r, now)),
    replicas: reps.map((r) => replicaMessage(r, now)),
    coldStart: coldStart(def),
    coldStartReplicas: reps.map((r) => coldStartWaterfall(r, now)),
    finishReasons: def.generative ? shares(FINISH_REASONS) : [],
    adapters: def.generative && id === SERVICES.prod.id ? shares(ADAPTERS) : [],
    servingCost: servingCost(id, now),
    ...(id === SERVICES.prod.id
      ? {
          comparison: comparison(now),
          onlineEvals: [
            { revisionId: REVISIONS.prodCurrent, arm: "control", feedbackPositiveRatio: 0.86, taskSuccessRatio: 0.79, guardrailRatio: 0.031, judgeScore: 4.21, samples: 12_400n },
            { revisionId: REVISIONS.prodFp8, arm: "fp8", feedbackPositiveRatio: 0.85, taskSuccessRatio: 0.78, guardrailRatio: 0.035, judgeScore: 4.185, samples: 1_380n },
          ],
        }
      : {}),
    config: [{ label: "engine", value: def.runtime }, ...revisionConfig(current), { label: "replicas", value: `${reps.length} × ${def.gpusPerReplica} ${L4.product.replace("NVIDIA ", "")}` }, { label: "autoscaling", value: def.generative && id === SERVICES.prod.id ? "HPA on vllm:num_requests_waiting (3–6 replicas)" : "fixed" }],
    findingIds: findings,
  };
}

// ── Histograms ────────────────────────────────────────────────────────────────

/** The standard normal CDF (Abramowitz–Stegun 7.1.26). */
function phi(z: number): number {
  const t = 1 / (1 + 0.3275911 * Math.abs(z / Math.SQRT2));
  const y = 1 - ((((1.061405429 * t - 1.453152027) * t + 1.421413741) * t - 0.284496736) * t + 0.254829592) * t * Math.exp(-(z * z) / 2);
  return z >= 0 ? (1 + y) / 2 : (1 - y) / 2;
}

/** Buckets of a log-normal with this median and p95, `count` samples over [0, hi). */
export function lognormalBuckets(median: number, p95: number, count: number, hi: number, n = 30) {
  const sigma = Math.log(p95 / median) / 1.645;
  const cdf = (v: number) => (v <= 0 ? 0 : phi((Math.log(v) - Math.log(median)) / sigma));
  const width = hi / n;
  return Array.from({ length: n }, (_, k) => ({ lower: k * width, upper: (k + 1) * width, count: BigInt(Math.round(count * (cdf((k + 1) * width) - cdf(k * width)))) }));
}

const quantile = (median: number, p95: number, q: number) => median * Math.exp((Math.log(p95 / median) / 1.645) * q);

function histograms(subject: { kind: MlSubjectKind; id: string }, keys: readonly string[], now: Now) {
  const serviceId = subject.kind === MlSubjectKind.INFERENCE_SERVICE ? subject.id : subject.id.split("/")[0];
  const def = serviceDef(serviceId);
  if (!def) return [];
  const h = serviceHeadline(serviceId, now);
  const rev = subject.kind === MlSubjectKind.REVISION ? revisionDef(subject.id) : undefined;
  const fp8 = rev?.fp8 ?? false;
  const count = h.requests24h * (rev ? rev.share || 0.9 : 1);
  const wanted = keys.length ? keys : ["serve.ttft", "serve.itl", "serve.e2e"];
  return wanted.flatMap((key): MessageInitShape<typeof MlHistogramSchema>[] => {
    const meta = KEY_META[key];
    if (!meta) return [];
    if (!def.generative && key !== "serve.e2e") return [{ key, label: meta.label, unit: "ms", collection: MlCollection.NOT_APPLICABLE, missingSource: "Triton cross-encoder: no token generation", buckets: [], compareBuckets: [], count: 0n, p50: NaN, p95: NaN, p99: NaN }];
    const f = fp8 ? (key === "serve.ttft" ? 0.86 : key === "serve.itl" ? 0.77 : 0.8) : 1;
    const p95 = (key === "serve.ttft" ? def.ttftP95 : key === "serve.itl" ? def.itlP95 : def.e2eP95) * f;
    const median = p95 * (key === "serve.ttft" ? 0.48 : key === "serve.itl" ? 0.24 : 0.44);
    const hi = p95 * 2.2;
    const prev = def.id === SERVICES.prod.id && !fp8 ? (key === "serve.ttft" ? 395 : key === "serve.itl" ? 37.6 : 4050) : null;
    return [
      {
        key,
        label: meta.label,
        unit: "ms",
        buckets: lognormalBuckets(median, p95, count, hi),
        p50: median,
        p95,
        p99: quantile(median, p95, 2.326),
        count: BigInt(Math.round(count)),
        collection: MlCollection.COLLECTED,
        ...(prev ? { compareLabel: `${RELEASE_PREV} (previous revision)`, compareBuckets: lognormalBuckets(prev * (median / p95), prev, count, hi) } : {}),
      },
    ];
  });
}

// ── Events and alerts ─────────────────────────────────────────────────────────

const svcSubject = (def: ServiceDef) => ({ kind: MlSubjectKind.INFERENCE_SERVICE, id: def.id, label: `${def.name} (${def.env})` });

function serviceEvents(id: string, now: Now) {
  const def = serviceDef(id);
  if (!def) return [];
  const s = svcSubject(def);
  if (id === SERVICES.prod.id) {
    const hot = INFERENCE_REPLICAS[1];
    return [
      {
        id: "ev-serve-release-v183",
        at: at(-RELEASE_AGO, now),
        kind: MlEventKind.RELEASE,
        severity: MlSeverity.INFO,
        title: `Release ${RELEASE_PROD} rolled out`,
        detail: "vLLM 0.8.4 → 0.8.5; model v2 unchanged (90%), fp8 arm carried over (10%). obs check: Pass.",
        subject: s,
        related: [
          { kind: MlSubjectKind.RELEASE, id: RELEASE_PROD, label: RELEASE_PROD },
          { kind: MlSubjectKind.REVISION, id: REVISIONS.prodCurrent, label: RELEASE_PROD },
          { kind: MlSubjectKind.REVISION, id: REVISIONS.prodPrev, label: RELEASE_PREV },
        ],
        source: "forge",
        hookEventType: "release.deployed",
      },
      {
        id: "ev-serve-image-pull-v183",
        at: at(-RELEASE_AGO + 4 * MINUTE, now),
        kind: MlEventKind.IMAGE_PULL,
        severity: MlSeverity.WARNING,
        title: "Pulled vllm/vllm-openai:v0.8.5 (9.4 GB) in 3m31s",
        detail: "Every replica pulled the new image: the nodes cache only the previous tag.",
        subject: s,
        related: [{ kind: MlSubjectKind.FINDING, id: FINDINGS.imagePull, label: "cold start: image pull" }],
        source: "kubelet",
      },
      {
        id: "ev-serve-restart-vllm1",
        at: at(-18 * MINUTE, now),
        kind: MlEventKind.RESTART,
        severity: MlSeverity.WARNING,
        title: "vllm-1 restarted: liveness probe failed",
        detail: "GET /health timed out after 10 s while the engine was saturated (KV cache 96%, 31 waiting). Not an OOM.",
        subject: { kind: MlSubjectKind.REPLICA, id: replicaId(hot), label: podShort(hot.pod) },
        related: [s, { kind: MlSubjectKind.NODE, id: serveNodeId(hot.node), label: hot.node }],
        source: "kubelet",
        attributes: { reason: "Unhealthy", probe: "liveness", exit_code: "137" },
      },
      {
        id: "ev-serve-finding-kv",
        at: at(-2 * HOUR, now),
        kind: MlEventKind.FINDING_OPENED,
        severity: MlSeverity.WARNING,
        title: "Finding opened: KV cache saturates on vllm-1 at peak",
        detail: "KV cache > 90% for 47 min/day; preemptions and waiting requests follow.",
        subject: s,
        related: [{ kind: MlSubjectKind.FINDING, id: FINDINGS.kvSaturation, label: "KV cache saturation" }],
        source: "forge",
        hookEventType: "ml.finding.opened",
      },
      {
        id: "ev-serve-scale-up",
        at: at(-9.5 * HOUR, now),
        kind: MlEventKind.SCALE,
        severity: MlSeverity.INFO,
        title: "Scaled 3 → 4 replicas",
        detail: "HPA: vllm:num_requests_waiting > 8 for 3 min.",
        subject: s,
        related: [{ kind: MlSubjectKind.REPLICA, id: replicaId(INFERENCE_REPLICAS[2]), label: "vllm-2" }],
        source: "kubelet",
        attributes: { from: "3", to: "4" },
      },
      {
        id: "ev-serve-scale-down",
        at: at(-21 * HOUR, now),
        kind: MlEventKind.SCALE,
        severity: MlSeverity.INFO,
        title: "Scaled 4 → 3 replicas",
        detail: "HPA: vllm:num_requests_waiting < 2 for 15 min.",
        subject: s,
        related: [{ kind: MlSubjectKind.REPLICA, id: replicaId(INFERENCE_REPLICAS[2]), label: "vllm-2" }],
        source: "kubelet",
        attributes: { from: "4", to: "3" },
      },
      {
        id: "ev-serve-changes-ramp",
        at: at(-RAMP_AGO, now),
        kind: MlEventKind.CHANGES_RAMP,
        severity: MlSeverity.INFO,
        title: `Changes ${CHANGES_FP8.name}: fp8 arm ramped to 10%`,
        detail: "Arms: control (v2) 90% · fp8 (v2.1-fp8) 10%. Decision pending.",
        subject: s,
        related: [
          { kind: MlSubjectKind.REVISION, id: REVISIONS.prodFp8, label: `${RELEASE_PROD}+fp8` },
          { kind: MlSubjectKind.MODEL_VERSION, id: VERSIONS.v21fp8.id, label: "v2.1-fp8" },
        ],
        source: "person",
        attributes: { by: "jon" },
        hookEventType: "changes.ramped",
      },
      {
        id: "ev-serve-alias-canary",
        at: at(-RAMP_AGO - 5 * MINUTE, now),
        kind: MlEventKind.ALIAS_MOVE,
        severity: MlSeverity.INFO,
        title: "Alias canary → v2.1-fp8",
        detail: "support-assistant:canary moved from v2 to v2.1-fp8 by jon.",
        subject: s,
        related: [{ kind: MlSubjectKind.MODEL_VERSION, id: VERSIONS.v21fp8.id, label: "v2.1-fp8" }],
        source: "person",
        hookEventType: "model.alias_moved",
      },
    ];
  }
  if (id === SERVICES.staging.id) {
    return [
      {
        id: "ev-serve-staging-release",
        at: at(-3 * HOUR, now),
        kind: MlEventKind.RELEASE,
        severity: MlSeverity.INFO,
        title: `Release ${RELEASE_STAGING} rolled out to staging`,
        detail: "Serving v2.1-fp8. obs check: not proven (too little traffic).",
        subject: s,
        related: [{ kind: MlSubjectKind.REVISION, id: REVISIONS.staging, label: RELEASE_STAGING }],
        source: "forge",
        hookEventType: "release.deployed",
      },
    ];
  }
  return [
    {
      id: "ev-serve-reranker-finding",
      at: at(-26 * HOUR, now),
      kind: MlEventKind.FINDING_OPENED,
      severity: MlSeverity.INFO,
      title: "Finding opened: reranker sends no traces",
      detail: "Triton's OTel tracing is off; latency comes from its Prometheus histograms only.",
      subject: s,
      related: [{ kind: MlSubjectKind.FINDING, id: FINDINGS.inferenceTracingOff, label: "inference tracing off" }],
      source: "forge",
    },
  ];
}

function eventsFor(subject: { kind: MlSubjectKind; id: string }, now: Now) {
  switch (subject.kind) {
    case MlSubjectKind.ORG:
      return serviceEvents(SERVICES.prod.id, now).filter((e) => e.kind === MlEventKind.RELEASE || e.kind === MlEventKind.FINDING_OPENED || e.kind === MlEventKind.CHANGES_RAMP || e.kind === MlEventKind.RESTART);
    case MlSubjectKind.INFERENCE_SERVICE:
      return serviceEvents(subject.id, now);
    case MlSubjectKind.REVISION: {
      const rev = revisionDef(subject.id);
      if (!rev) return [];
      return serviceEvents(rev.serviceId, now).filter((e) => e.related.some((r) => r.id === rev.id) || (rev.fp8 && e.kind === MlEventKind.CHANGES_RAMP));
    }
    case MlSubjectKind.REPLICA: {
      const rep = INFERENCE_REPLICAS.find((r) => replicaId(r) === subject.id);
      if (!rep) return [];
      const started = {
        id: `ev-serve-started-${rep.pod}`,
        at: at(-rep.startedAgo, now),
        kind: MlEventKind.POD_STARTED,
        severity: MlSeverity.INFO,
        title: `${podShort(rep.pod)} ready`,
        detail: `Serving ${revisionDef(rep.revision)?.release ?? rep.revision} on ${rep.node} GPUs ${rep.gpus.join(", ")}`,
        subject: { kind: MlSubjectKind.REPLICA, id: subject.id, label: podShort(rep.pod) },
        related: [{ kind: MlSubjectKind.NODE, id: serveNodeId(rep.node), label: rep.node }],
        source: "kubelet",
      };
      return [started, ...serviceEvents(rep.serviceId, now).filter((e) => e.subject.id === subject.id || e.related.some((r) => r.id === subject.id))];
    }
    default:
      return [];
  }
}

const ALERT_KCL = "deploy/kcl/prod/main.k: support_assistant.alerts";

function alertsFor(subject: { kind: MlSubjectKind; id: string }, now: Now) {
  const prod = serviceDef(SERVICES.prod.id)!;
  const reranker = serviceDef(SERVICES.reranker.id)!;
  const prodAlerts = [
    {
      id: ALERTS.kvSaturation,
      name: "KV cache saturation",
      expression: "max by (replica) (vllm:kv_cache_usage_perc) > 0.90 for 15m",
      severity: MlSeverity.WARNING,
      state: MlAlertState.FIRING,
      subject: svcSubject(prod),
      declaredIn: "forge default",
      lastFiredAt: at(-2 * HOUR, now),
      fired7d: 6,
      routedTo: "Slack #support-ml-oncall",
      lastAction: "Notified Slack #support-ml-oncall; opened finding f-kv-cache-saturation-prod",
    },
    {
      id: ALERTS.ttftSlo,
      name: "TTFT SLO burn",
      expression: "TTFT p95 > 450 ms for 10m (SLO: 99% of requests under 500 ms, 30d)",
      severity: MlSeverity.WARNING,
      state: MlAlertState.PENDING,
      subject: svcSubject(prod),
      declaredIn: ALERT_KCL,
      lastFiredAt: at(-26 * HOUR, now),
      fired7d: 2,
      routedTo: "Slack #support-ml-oncall · Inbox",
      lastAction: "Pending for 6 min; will notify and halt promotion of the next release if it fires",
    },
    {
      id: "alert-serve-error-rate",
      name: "Error rate",
      expression: "rate(vllm:request_success{finished_reason=\"error\"}) / rate(vllm:request_success) > 1% for 5m",
      severity: MlSeverity.CRITICAL,
      state: MlAlertState.OK,
      subject: svcSubject(prod),
      declaredIn: "forge default",
      fired7d: 0,
      routedTo: "PagerDuty support-ml",
      lastAction: "",
    },
    {
      id: "alert-serve-release-health",
      name: "Release health (obs check)",
      expression: "obs check verdict = Regressed for the newest revision",
      severity: MlSeverity.CRITICAL,
      state: MlAlertState.OK,
      subject: svcSubject(prod),
      declaredIn: "forge default",
      lastFiredAt: at(-12 * DAY, now),
      fired7d: 0,
      routedTo: "Inbox",
      lastAction: `Last: v1.8.0 regressed 12 days ago; promotion halted, v1.7.9 kept serving`,
    },
  ];
  const rerankerAlerts = [
    {
      id: "alert-reranker-latency",
      name: "Reranker latency",
      expression: "histogram_quantile(0.95, nv_inference_request_duration_us) > 80 ms for 10m",
      severity: MlSeverity.WARNING,
      state: MlAlertState.OK,
      subject: svcSubject(reranker),
      declaredIn: "forge default",
      fired7d: 0,
      routedTo: "Slack #search-oncall",
      lastAction: "",
    },
  ];
  if (subject.kind === MlSubjectKind.ORG) return prodAlerts.filter((a) => a.state !== MlAlertState.OK);
  if (subject.kind === MlSubjectKind.INFERENCE_SERVICE) return subject.id === prod.id ? prodAlerts : subject.id === reranker.id ? rerankerAlerts : [];
  return [];
}

// ── Request traces ────────────────────────────────────────────────────────────

const TENANTS = ["acme-retail", "northwind", "globex", "initech", "umbrella-eu", "hooli"];

interface RequestDef {
  traceId: string;
  agoMs: number;
  replica: ReplicaDef;
  prompt: number;
  cached: number;
  out: number;
  queueMs: number;
  prefillMs: number;
  itlP50: number;
  preempted: number;
  finish: string;
  adapter: string;
  tenant: string;
  bucket: string;
  error?: string;
}

const traceHex = (seed: string) => Array.from({ length: 4 }, (_, k) => hash(`${seed}#${k}`).toString(16).padStart(8, "0")).join("");

/** decode ms of a request: tokens × ITL p50, plus the tail gaps (and a preemption's recompute). */
const decodeMs = (r: RequestDef) => r.out * r.itlP50 * 1.03 + r.preempted * 420;
const vllmMs = (r: RequestDef) => r.queueMs + r.prefillMs + decodeMs(r) + 25;
const rootMs = (r: RequestDef) => vllmMs(r) + 250;

/** The slowest requests of the last 24h (prod), worst first, plus the one normal one. */
export function requestDefs(): RequestDef[] {
  const reps = replicasOf(SERVICES.prod.id);
  const slow: RequestDef = {
    traceId: TRACES.slowRequest,
    agoMs: 22 * MINUTE,
    replica: reps[1],
    prompt: 2104,
    cached: 1536,
    out: 512,
    queueMs: 27,
    prefillMs: 310,
    itlP50: 7,
    preempted: 0,
    finish: "length",
    adapter: "support-de",
    tenant: "acme-retail",
    bucket: "e2e > p99",
  };
  const r = rng(hash("slow-requests"));
  const buckets = ["e2e > p99", "TTFT > 1 s", "queue > 100 ms", "preempted", "finish: length", "finish: abort"];
  const others: RequestDef[] = Array.from({ length: 19 }, (_, k) => {
    const bucket = buckets[k % buckets.length];
    const rep = reps[k % 4 === 3 ? 3 : k % 3];
    const peak = bucket === "queue > 100 ms" || bucket === "preempted" || bucket === "TTFT > 1 s";
    const prompt = Math.round(bucket === "TTFT > 1 s" ? 9000 + r() * 14000 : 900 + r() * 2600);
    const out = Math.round(bucket === "finish: length" ? 512 : bucket === "finish: abort" ? 60 + r() * 200 : 280 + r() * 230);
    return {
      traceId: traceHex(`req-${k}`),
      agoMs: (peak ? 6 * HOUR + r() * 4 * HOUR : 30 * MINUTE + r() * 23 * HOUR) | 0,
      replica: rep,
      prompt,
      cached: Math.round(prompt * (0.3 + r() * 0.5)),
      out,
      queueMs: Math.round(bucket === "queue > 100 ms" ? 110 + r() * 260 : 8 + r() * 30),
      prefillMs: Math.round(prompt * (bucket === "TTFT > 1 s" ? 0.09 : 0.12) * (rep === reps[3] ? 0.85 : 1)),
      itlP50: +(rep === reps[3] ? 5.3 + r() : 6.6 + r() * 1.4).toFixed(1),
      preempted: bucket === "preempted" ? 1 + (k % 2) : 0,
      finish: bucket === "finish: length" ? "length" : bucket === "finish: abort" ? "abort" : k === 7 ? "error" : "stop",
      adapter: ADAPTERS[k % 3][0].startsWith("base") ? "base" : ADAPTERS[k % 3][0],
      tenant: TENANTS[k % TENANTS.length],
      bucket,
      ...(k === 7 ? { error: "upstream reset: replica restarted (liveness probe)" } : {}),
    };
  });
  return [slow, ...others].sort((a, b) => rootMs(b) - rootMs(a));
}

const NORMAL_REQUEST = (): RequestDef => ({
  traceId: TRACES.normalRequest,
  agoMs: 9 * MINUTE,
  replica: replicasOf(SERVICES.prod.id)[0],
  prompt: 640,
  cached: 512,
  out: 96,
  queueMs: 4,
  prefillMs: 62,
  itlP50: 6.8,
  preempted: 0,
  finish: "stop",
  adapter: "base",
  tenant: "northwind",
  bucket: "normal",
});

const MEDIAN_E2E_MS = 1650;

function traceSummary(r: RequestDef, now: Now) {
  const rev = revisionDef(r.replica.revision)!;
  return {
    traceId: r.traceId,
    kind: MlTraceKind.INFERENCE_REQUEST,
    title: `POST /chat · ${(rootMs(r) / 1000).toFixed(1)} s`,
    start: at(-r.agoMs, now),
    durationMs: rootMs(r),
    vsMedian: rootMs(r) / MEDIAN_E2E_MS,
    dominantSpan: r.queueMs > r.prefillMs && r.queueMs > decodeMs(r) ? "queued" : r.prefillMs > decodeMs(r) ? "prefill" : "decode",
    subject: { kind: MlSubjectKind.REPLICA, id: replicaId(r.replica), label: podShort(r.replica.pod) },
    error: !!r.error,
    attributes: {
      model: r.adapter === "base" ? "support-assistant" : r.adapter,
      adapter: r.adapter,
      finish_reason: r.finish,
      revision: rev.release,
      tenant: r.tenant,
      bucket: r.bucket,
      prompt_tokens: String(r.prompt),
      output_tokens: String(r.out),
      ttft_ms: String(Math.round(r.queueMs + r.prefillMs)),
    },
  };
}

/** A request's spans: app → gen_ai client → gateway → endpoint picker → vLLM (queued, prefill, decode, stream) + the GPUs' DCGM samples. */
function requestSpans(r: RequestDef, now: Now): MessageInitShape<typeof MlSpanSchema>[] {
  const t0 = -r.agoMs;
  const rep = r.replica;
  const node = serveNodeId(rep.node);
  const devices = replicaDevices(rep);
  const rev = revisionDef(rep.revision)!;
  const model = rev.versionId.split("/").pop()!;
  const ts = (offsetMs: number) => at(Math.round(t0 + offsetMs), now);
  const vStart = 150;
  const dec = decodeMs(r);
  const ttft = r.queueMs + r.prefillMs;
  const usd = ((r.prompt - r.cached) * 0.19 + r.out * 0.83) / 1e6;
  const err = !!r.error;
  const status = err ? MlSpanStatus.ERROR : MlSpanStatus.OK;
  const serveJoins = { nodeId: node, deviceUuid: devices[0], pod: rep.pod, rank: -1 };
  const id = (s: string) => hash(`${r.traceId}-${s}`).toString(16).padStart(8, "0") + "00000000";
  const spans: MessageInitShape<typeof MlSpanSchema>[] = [
    {
      spanId: id("root"),
      parentSpanId: "",
      name: "POST /chat",
      service: "support-api",
      kind: "server",
      start: ts(0),
      durationMs: rootMs(r),
      status,
      statusMessage: err ? "502 Bad Gateway" : "",
      attributes: { "http.route": "/chat", "http.response.status_code": err ? "502" : "200", tenant: r.tenant, "ml.cost.usd": usd.toFixed(6) },
      rank: -1,
    },
    {
      spanId: id("genai"),
      parentSpanId: id("root"),
      name: `chat ${r.adapter === "base" ? "support-assistant" : r.adapter}`,
      service: "support-api",
      kind: "client",
      start: ts(40),
      durationMs: vllmMs(r) + 95,
      status,
      statusMessage: err ? (r.error ?? "") : "",
      attributes: {
        "gen_ai.operation.name": "chat",
        "gen_ai.provider.name": "vllm",
        "gen_ai.request.model": r.adapter === "base" ? "support-assistant" : r.adapter,
        "gen_ai.response.model": `support-assistant:${model}`,
        "gen_ai.request.max_tokens": "512",
        "gen_ai.request.temperature": "0.3",
        "gen_ai.usage.input_tokens": String(r.prompt),
        "gen_ai.usage.cache_read.input_tokens": String(r.cached),
        "gen_ai.usage.output_tokens": String(r.out),
        "gen_ai.response.finish_reasons": `["${r.finish}"]`,
        "gen_ai.conversation.id": `conv_${r.traceId.slice(0, 10)}`,
      },
      rank: -1,
    },
    {
      spanId: id("gw"),
      parentSpanId: id("genai"),
      name: "inference-gateway POST /v1/chat/completions",
      service: "inference-gateway",
      kind: "server",
      start: ts(60),
      durationMs: vllmMs(r) + 70,
      status,
      statusMessage: "",
      attributes: { "inference.pool": `${SERVICES.prod.id}`, "inference.objective": "chat-interactive", "inference.criticality": "Critical", route: "support-assistant" },
      rank: -1,
    },
    {
      spanId: id("epp"),
      parentSpanId: id("gw"),
      name: "epp.schedule",
      service: "endpoint-picker",
      kind: "internal",
      start: ts(64),
      durationMs: 3,
      status: MlSpanStatus.OK,
      statusMessage: "",
      attributes: {
        "epp.candidates": "4",
        "epp.chosen_pod": rep.pod,
        "epp.reason": `KV-aware: lowest KV cache among prefix matches (prefix match ${r.cached.toLocaleString("en-US")} tok)`,
        "epp.scores": rep === INFERENCE_REPLICAS[1] ? "kv 0.38 · queue 0.71 · prefix 0.94 · lora 1.0" : "kv 0.62 · queue 0.80 · prefix 0.71 · lora 1.0",
        "epp.filter_chain": "lora-affinity → least-queue → kv-cache → prefix",
      },
      rank: -1,
    },
    {
      spanId: id("vllm"),
      parentSpanId: id("gw"),
      name: "llm_request",
      service: "vllm",
      kind: "server",
      start: ts(vStart),
      durationMs: vllmMs(r),
      status,
      statusMessage: err ? (r.error ?? "") : "",
      attributes: {
        "gen_ai.request.id": `cmpl-${r.traceId.slice(0, 16)}`,
        "gen_ai.response.model": `support-assistant:${model}`,
        "gen_ai.usage.prompt_tokens": String(r.prompt),
        "gen_ai.usage.completion_tokens": String(r.out),
        "gen_ai.latency.time_in_queue": (r.queueMs / 1000).toFixed(3),
        "gen_ai.latency.time_to_first_token": (ttft / 1000).toFixed(3),
        "gen_ai.latency.time_in_model_prefill": (r.prefillMs / 1000).toFixed(3),
        "gen_ai.latency.time_in_model_decode": (dec / 1000).toFixed(3),
        "ml.revision": rev.release,
        "ml.replica": podShort(rep.pod),
        "lora.adapter": r.adapter,
        finish_reason: r.finish,
      },
      ...serveJoins,
    },
    {
      spanId: id("queued"),
      parentSpanId: id("vllm"),
      name: "queued",
      service: "vllm",
      kind: "internal",
      start: ts(vStart + 2),
      durationMs: r.queueMs,
      status: MlSpanStatus.OK,
      statusMessage: "",
      attributes: { synthetic: "true", "vllm.num_requests_waiting": String(Math.round(r.queueMs / 4)) },
      ...serveJoins,
    },
    {
      spanId: id("prefill"),
      parentSpanId: id("vllm"),
      name: "prefill",
      service: "vllm",
      kind: "internal",
      start: ts(vStart + 2 + r.queueMs),
      durationMs: r.prefillMs,
      status: MlSpanStatus.OK,
      statusMessage: "",
      attributes: { synthetic: "true", prompt_tokens: String(r.prompt), prefix_cache_hit_tokens: String(r.cached), computed_tokens: String(r.prompt - r.cached) },
      ...serveJoins,
    },
    {
      spanId: id("decode"),
      parentSpanId: id("vllm"),
      name: "decode",
      service: "vllm",
      kind: "internal",
      start: ts(vStart + 2 + ttft),
      durationMs: dec,
      status,
      statusMessage: err ? (r.error ?? "") : "",
      attributes: { synthetic: "true", output_tokens: String(r.out), itl_p50_ms: String(r.itlP50), itl_max_ms: String(Math.round(r.itlP50 * (r.preempted ? 60 : 9))), preemptions: String(r.preempted), spec_decode: rev.spec ? "ngram (acceptance 0.58)" : "off" },
      events: [
        { time: ts(vStart + 2 + ttft), name: "first_token", attributes: { ttft_ms: String(Math.round(ttft)) } },
        ...(r.preempted ? [{ time: ts(vStart + 2 + ttft + dec * 0.6), name: "preempted", attributes: { reason: "kv_cache_full", recompute: "true" } }] : []),
        { time: ts(vStart + 2 + ttft + dec), name: "last_token", attributes: { finish_reason: r.finish } },
      ],
      ...serveJoins,
    },
    {
      spanId: id("stream"),
      parentSpanId: id("vllm"),
      name: "detokenize/stream",
      service: "vllm",
      kind: "internal",
      start: ts(vStart + 2 + ttft),
      durationMs: Math.max(1, dec - 5),
      status: MlSpanStatus.OK,
      statusMessage: "",
      attributes: { synthetic: "true", stream: "true", chunks: String(r.out) },
      ...serveJoins,
    },
    ...devices.map((uuid, k) => ({
      spanId: id(`dcgm-${k}`),
      parentSpanId: id("vllm"),
      name: "dcgm.sample",
      service: "dcgm-exporter",
      kind: "internal",
      start: ts(vStart + 2 + ttft),
      durationMs: Math.min(dec, 1000),
      status: MlSpanStatus.OK,
      statusMessage: "",
      attributes: {
        "gpu.index": String(rep.gpus[k]),
        "tp.rank": String(k),
        DCGM_FI_DEV_GPU_UTIL: rep === INFERENCE_REPLICAS[1] ? "97" : "71",
        DCGM_FI_DEV_FB_USED: rep === INFERENCE_REPLICAS[1] ? "21.9 GiB" : "20.4 GiB",
        DCGM_FI_DEV_POWER_USAGE: rep === INFERENCE_REPLICAS[1] ? "69 W" : "58 W",
        DCGM_FI_DEV_GPU_TEMP: "61",
        "vllm:kv_cache_usage_perc": rep === INFERENCE_REPLICAS[1] ? "0.93" : "0.68",
        DCGM_FI_PROF_SM_ACTIVE: `not collected (${FINDINGS.dcgmProfilingOff})`,
      },
      nodeId: node,
      deviceUuid: uuid,
      pod: rep.pod,
      rank: -1,
    })),
  ];
  return spans.map((s) => ({ events: [], runId: "", phaseKey: "", attempt: 0, nodeId: "", deviceUuid: "", pod: "", ...s }));
}

function tracesFor(subject: { kind: MlSubjectKind; id: string }, limit: number | undefined, now: Now) {
  const all = requestDefs();
  let rows: RequestDef[];
  if (subject.kind === MlSubjectKind.INFERENCE_SERVICE) {
    if (subject.id === SERVICES.reranker.id) return { traces: [], missingFindingId: FINDINGS.inferenceTracingOff };
    rows = subject.id === SERVICES.prod.id ? all : [];
  } else if (subject.kind === MlSubjectKind.REVISION) {
    rows = all.filter((r) => r.replica.revision === subject.id);
  } else if (subject.kind === MlSubjectKind.REPLICA) {
    rows = all.filter((r) => replicaId(r.replica) === subject.id);
  } else return undefined;
  return { traces: rows.slice(0, limit || 50).map((r) => traceSummary(r, now)) };
}

function traceById(traceId: string, now: Now) {
  const def = traceId === TRACES.normalRequest ? NORMAL_REQUEST() : requestDefs().find((r) => r.traceId === traceId);
  if (!def) return undefined;
  return { summary: traceSummary(def, now), spans: requestSpans(def, now), truncated: false };
}

// ── The domain ────────────────────────────────────────────────────────────────

const SERVING_KINDS = new Set([MlSubjectKind.INFERENCE_SERVICE, MlSubjectKind.REVISION, MlSubjectKind.REPLICA]);

function frameOf(subject: { kind: MlSubjectKind; id: string }, now: Now): Frame | undefined {
  if (subject.kind === MlSubjectKind.INFERENCE_SERVICE) {
    const def = serviceDef(subject.id);
    return def && serviceFrame(def, now);
  }
  if (subject.kind === MlSubjectKind.REVISION) {
    const rev = revisionDef(subject.id);
    return rev && revisionFrame(rev, now);
  }
  if (subject.kind === MlSubjectKind.REPLICA) {
    const rep = INFERENCE_REPLICAS.find((r) => replicaId(r) === subject.id);
    return rep && replicaFrame(rep, now);
  }
  return undefined;
}

export const inferenceAnswers: MlDomain = {
  listMlInferenceServices: (req, now) => {
    if (req.project && req.project !== ML_PROJECT) return { services: [] };
    return { services: SERVICE_DEFS.map((s) => serviceMessage(s.id, now)) };
  },
  getMlInferenceService: (req, now) => detail(req.serviceId, now),
  queryMlSeries: (req, now) => {
    if (!req.subject || !SERVING_KINDS.has(req.subject.kind)) return undefined;
    const frame = frameOf(req.subject, now);
    if (!frame) return undefined;
    return { series: querySeries(frame, req.keys, req.groupBy, now) };
  },
  queryMlHistograms: (req, now) => {
    if (!req.subject || !SERVING_KINDS.has(req.subject.kind)) return undefined;
    return { histograms: histograms(req.subject, req.keys, now) };
  },
  listMlEvents: (req, now) => {
    if (!req.subject) return undefined;
    const events = eventsFor(req.subject, now).filter((e) => req.kinds.length === 0 || req.kinds.includes(e.kind));
    return events.length ? { events } : undefined;
  },
  listMlAlerts: (req, now) => {
    if (!req.subject) return undefined;
    const alerts = alertsFor(req.subject, now);
    return alerts.length ? { alerts } : undefined;
  },
  listMlTraces: (req, now) => (req.subject ? tracesFor(req.subject, req.limit, now) : undefined),
  getMlTrace: (req, now) => traceById(req.traceId, now),
};
