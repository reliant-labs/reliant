// Copyright (c) 2026 Reliant Labs

/**
 * The ML fixture world's Models: ListMlModels, GetMlModel,
 * GetMlModelVersion, and the events of MODEL / MODEL_VERSION subjects
 * (plus the org's alias moves and promotions), which ./telemetry.ts serves
 * through ObservabilityService.
 *
 * The registry (world.ts MODELS, VERSIONS):
 *   support-assistant
 *     v1        pushed by hand 46 days ago; production until v2.
 *     v2        trained by llama-8b-support-v2 (sft, attempt 1, final
 *               checkpoint); alias production → prod, 90% of prod traffic.
 *     v2.1-fp8  trained by the same run's quantize phase (FP8 E4M3); alias
 *               staging; 10% of prod through the Changes arm. Its promotion
 *               to prod is blocked by the release gate (safety_refusals 0.979).
 *     The next candidate is the v3 SFT checkpoint 40,000: not a version yet
 *     (aliases name versions only); the registry shows it from the eval
 *     matrix's "candidate" row.
 *   bge-reranker
 *     v4        imported from Hugging Face; serves reranker-prod.
 *
 * Serving placements match the inference domain's revisions (REVISIONS):
 * prod v2 at 0.9 on prodCurrent, v2.1-fp8 at 0.1 on prodFp8 and 1.0 on
 * staging, reranker v4 at 1.0.
 */

import { MlEventKind, MlSeverity, MlSubjectKind, MlVersionSource } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { prodRps } from "./curves";
import type { MlDomain } from "./domain";
import type { EventsQuery, MlTelemetryDomain } from "./telemetryTypes";
import { fakeDigest, filterEvents } from "./evals";
import { at, BASE_MODEL, DAY, HOUR, CLUSTERS, EVAL_RUNS, ML_PEOPLE, ML_PROJECT, MODELS, REVISIONS, RUNS, SERVICES, V21_FP8_CHECKPOINT, V2_FINAL_CHECKPOINT, VERSIONS } from "./world";


/** A Timestamp at an offset, on a whole millisecond (protobuf nanos are integers). */
const ts = (offsetMs: number, now: () => number) => at(Math.round(offsetMs), now);
// ── Files ─────────────────────────────────────────────────────────────────────

const WEIGHT = "application/vnd.cncf.model.weight.v1.raw";
const WEIGHT_CONFIG = "application/vnd.cncf.model.weight.config.v1.raw";
const DOC = "application/vnd.cncf.model.doc.v1.raw";

interface FileDef {
  path: string;
  size: number;
  mediaType: string;
}

const LLAMA_SMALL_FILES: readonly FileDef[] = [
  { path: "config.json", size: 826, mediaType: WEIGHT_CONFIG },
  { path: "generation_config.json", size: 185, mediaType: WEIGHT_CONFIG },
  { path: "tokenizer.json", size: 9_085_657, mediaType: WEIGHT_CONFIG },
  { path: "tokenizer_config.json", size: 50_977, mediaType: WEIGHT_CONFIG },
  { path: "special_tokens_map.json", size: 296, mediaType: WEIGHT_CONFIG },
  { path: "README.md", size: 4_812, mediaType: DOC },
  { path: "LICENSE", size: 7_627, mediaType: DOC },
];

const BF16_WEIGHTS: readonly FileDef[] = [
  { path: "model-00001-of-00004.safetensors", size: 4_976_698_672, mediaType: WEIGHT },
  { path: "model-00002-of-00004.safetensors", size: 4_999_802_720, mediaType: WEIGHT },
  { path: "model-00003-of-00004.safetensors", size: 4_915_916_176, mediaType: WEIGHT },
  { path: "model-00004-of-00004.safetensors", size: 1_168_138_808, mediaType: WEIGHT },
  { path: "model.safetensors.index.json", size: 23_950, mediaType: WEIGHT_CONFIG },
];

const FP8_WEIGHTS: readonly FileDef[] = [
  { path: "model-00001-of-00002.safetensors", size: 4_982_469_144, mediaType: WEIGHT },
  { path: "model-00002-of-00002.safetensors", size: 3_515_046_136, mediaType: WEIGHT },
  { path: "model.safetensors.index.json", size: 31_204, mediaType: WEIGHT_CONFIG },
  { path: "recipe.yaml", size: 412, mediaType: WEIGHT_CONFIG },
];

const RERANKER_FILES: readonly FileDef[] = [
  { path: "model.safetensors", size: 1_135_554_214, mediaType: WEIGHT },
  { path: "config.json", size: 801, mediaType: WEIGHT_CONFIG },
  { path: "tokenizer.json", size: 17_082_987, mediaType: WEIGHT_CONFIG },
  { path: "sentencepiece.bpe.model", size: 5_069_051, mediaType: WEIGHT_CONFIG },
  { path: "tokenizer_config.json", size: 1_173, mediaType: WEIGHT_CONFIG },
  { path: "special_tokens_map.json", size: 964, mediaType: WEIGHT_CONFIG },
  { path: "README.md", size: 8_204, mediaType: DOC },
];

/** The bytes of a version's weights (what the card's size says). */
const weightBytes = (files: readonly FileDef[]) => files.filter((f) => f.mediaType === WEIGHT).reduce((sum, f) => sum + f.size, 0);

// ── Versions ──────────────────────────────────────────────────────────────────

interface GateCheckDef {
  name: string;
  status: "pass" | "fail" | "pending" | "skipped";
  detail: string;
  evidence?: { kind: MlSubjectKind; id: string; label: string };
}

interface PerfDef {
  hardware: string;
  runtime: string;
  ttft: number;
  itl: number;
  tps: number;
  usdPerM: number;
  serviceId: string;
}

interface VersionDef {
  id: string;
  modelId: string;
  version: string;
  digest: string;
  createdAt: number;
  source: MlVersionSource;
  runId: string;
  phaseKey: string;
  attempt: number;
  checkpointId: string;
  baseModel: string;
  createdBy: string;
  card: {
    architecture: string;
    paramsTotal: bigint;
    contextLength: number;
    dtype: string;
    quantization: string;
    tokenizer: string;
    license: string;
    layers: number;
    shards: number;
    trainingData: string;
    intendedUse: string;
  };
  files: readonly FileDef[];
  aliases: readonly string[];
  serving: readonly { serviceId: string; env: string; revisionId: string; trafficShare: number }[];
  gate: readonly GateCheckDef[];
  perf: readonly PerfDef[];
  pulls: readonly { clusterId: string; seconds: number; source: "registry" | "cache" | "peer" }[];
}

const evalEvidence = (id: string, label: string) => ({ kind: MlSubjectKind.EVAL_RUN, id, label });
const revisionEvidence = (id: string) => ({ kind: MlSubjectKind.REVISION, id, label: `revision ${id.split("/").slice(1).join("/")}` });

const LLAMA_CARD = {
  architecture: "LlamaForCausalLM (Llama 3.1)",
  paramsTotal: 8_030_261_248n,
  contextLength: 131_072,
  tokenizer: "llama3 (tiktoken BPE, 128,256 tokens)",
  license: "Llama 3.1 Community License",
  layers: 32,
  intendedUse: "Answers for the shop's customer-support chat (en, de, es, ja). Not for refunds over $500 or account changes without verification.",
};

export const VERSION_DEFS: readonly VersionDef[] = [
  {
    id: VERSIONS.v1.id,
    modelId: MODELS.support.id,
    version: VERSIONS.v1.version,
    digest: VERSIONS.v1.digest,
    createdAt: -46 * DAY,
    source: MlVersionSource.PUSHED,
    runId: "",
    phaseKey: "",
    attempt: 0,
    checkpointId: "",
    baseModel: BASE_MODEL,
    createdBy: ML_PEOPLE.admin,
    card: { ...LLAMA_CARD, dtype: "bf16", quantization: "", shards: 4, trainingData: "Fine-tuned outside the platform (a notebook run on support-sft v1); pushed with `reliant forge ml push`." },
    files: [...BF16_WEIGHTS, ...LLAMA_SMALL_FILES],
    aliases: [],
    serving: [],
    gate: [
      { name: "Release gate (release-gate v7)", status: "skipped", detail: "Pushed by hand before release-gate v7 was declared. Evaluated later for comparison: it would fail (support tickets −6.1 pt).", evidence: evalEvidence(EVAL_RUNS.v1, "release-gate v7 on v1") },
      { name: "Approval", status: "skipped", detail: "No delivery policy then: jon pushed it." },
    ],
    perf: [{ hardware: "NVIDIA L4 ×2 (TP 2)", runtime: "vLLM 0.8.5", ttft: 415, itl: 38, tps: 1_822, usdPerM: 0.41, serviceId: "" }],
    pulls: [],
  },
  {
    id: VERSIONS.v2.id,
    modelId: MODELS.support.id,
    version: VERSIONS.v2.version,
    digest: VERSIONS.v2.digest,
    createdAt: -21.2 * DAY,
    source: MlVersionSource.TRAINED,
    runId: RUNS.v2.id,
    phaseKey: "sft",
    attempt: 1,
    checkpointId: V2_FINAL_CHECKPOINT,
    baseModel: BASE_MODEL,
    createdBy: ML_PEOPLE.trainer,
    card: { ...LLAMA_CARD, dtype: "bf16", quantization: "", shards: 4, trainingData: "support-sft v2 (resolved support conversations, en/de/es/ja), SFT on Llama 3.1 8B." },
    files: [...BF16_WEIGHTS, ...LLAMA_SMALL_FILES],
    aliases: ["production"],
    serving: [{ serviceId: SERVICES.prod.id, env: "prod", revisionId: REVISIONS.prodCurrent, trafficShare: 0.9 }],
    gate: [
      { name: "No capability task regresses > 1 pt (95%)", status: "pass", detail: "Against v1, every capability task improved (+2.4 pt).", evidence: evalEvidence(EVAL_RUNS.v2, "release-gate v7 on v2") },
      { name: "safety_refusals ≥ 0.98", status: "pass", detail: "0.991 (n = 1,800)", evidence: evalEvidence(EVAL_RUNS.v2, "release-gate v7 on v2") },
      { name: "support_tickets_v3 ≥ production (95%)", status: "pass", detail: "0.871 against v1's 0.810 (+6.1 pt, p < 0.001)", evidence: evalEvidence(EVAL_RUNS.v2, "release-gate v7 on v2") },
      { name: "Approval (delivery policy ml-admins)", status: "pass", detail: "Approved by jon 19 days ago." },
      { name: "Inference obs check on support-assistant-prod", status: "pass", detail: "TTFT p95, ITL p95 and error rate held against the previous revision.", evidence: revisionEvidence(REVISIONS.prodCurrent) },
    ],
    perf: [
      { hardware: "NVIDIA L4 ×2 (TP 2)", runtime: "vLLM 0.8.5", ttft: 411, itl: 38, tps: 1_834, usdPerM: 0.41, serviceId: SERVICES.prod.id },
      { hardware: "NVIDIA H100 80GB ×1 (benchmark)", runtime: "vLLM 0.8.5", ttft: 118, itl: 12, tps: 8_950, usdPerM: 0.245, serviceId: "" },
      { hardware: "NVIDIA A100 40GB ×1 (benchmark)", runtime: "vLLM 0.8.5", ttft: 191, itl: 21, tps: 4_100, usdPerM: 0.31, serviceId: "" },
    ],
    pulls: [
      { clusterId: CLUSTERS.serve.id, seconds: 214, source: "registry" },
      { clusterId: CLUSTERS.serve.id, seconds: 41, source: "peer" },
      { clusterId: CLUSTERS.train.id, seconds: 88, source: "registry" },
    ],
  },
  {
    id: VERSIONS.v21fp8.id,
    modelId: MODELS.support.id,
    version: VERSIONS.v21fp8.version,
    digest: VERSIONS.v21fp8.digest,
    createdAt: -21 * DAY,
    source: MlVersionSource.TRAINED,
    runId: RUNS.v2.id,
    phaseKey: "quantize",
    attempt: 1,
    checkpointId: V21_FP8_CHECKPOINT,
    baseModel: VERSIONS.v2.id,
    createdBy: ML_PEOPLE.trainer,
    card: {
      ...LLAMA_CARD,
      dtype: "fp8 e4m3",
      quantization: "FP8 E4M3: per-channel weights, dynamic per-token activations (llm-compressor FP8_DYNAMIC)",
      shards: 2,
      trainingData: "v2's weights, quantized; no further training. Calibration: none (dynamic).",
    },
    files: [...FP8_WEIGHTS, ...LLAMA_SMALL_FILES],
    aliases: ["staging"],
    serving: [
      { serviceId: SERVICES.prod.id, env: "prod", revisionId: REVISIONS.prodFp8, trafficShare: 0.1 },
      { serviceId: SERVICES.staging.id, env: "staging", revisionId: REVISIONS.staging, trafficShare: 1 },
    ],
    gate: [
      { name: "No capability task regresses > 1 pt (95%)", status: "pass", detail: "Largest drop RULER 32k −1.1 pt, not significant (p = 0.08).", evidence: evalEvidence(EVAL_RUNS.v21fp8, "release-gate v7 on v2.1-fp8") },
      { name: "safety_refusals ≥ 0.98", status: "fail", detail: "0.979 (n = 1,800): −1.2 pt against production, significant. The losses are mostly privacy requests.", evidence: evalEvidence(EVAL_RUNS.v21fp8, "release-gate v7 on v2.1-fp8") },
      { name: "support_tickets_v3 ≥ production (95%)", status: "pass", detail: "0.869 against 0.871 (−0.2 pt, not significant)", evidence: evalEvidence(EVAL_RUNS.v21fp8, "release-gate v7 on v2.1-fp8") },
      { name: "Inference obs check on support-assistant-staging", status: "pass", detail: "Staging SLIs held for 12 days.", evidence: revisionEvidence(REVISIONS.staging) },
      { name: "Changes arm in prod (10%)", status: "pending", detail: "support-assistant-fp8 is collecting online evals at 10% of prod traffic.", evidence: revisionEvidence(REVISIONS.prodFp8) },
      { name: "Approval (delivery policy ml-admins)", status: "pending", detail: "Waits for the eval gate; then jon approves." },
    ],
    perf: [
      { hardware: "NVIDIA L4 ×2 (TP 2)", runtime: "vLLM 0.8.5", ttft: 352, itl: 29, tps: 2_410, usdPerM: 0.31, serviceId: SERVICES.prod.id },
      { hardware: "NVIDIA L4 ×2 (TP 2)", runtime: "vLLM 0.8.5", ttft: 344, itl: 28, tps: 2_385, usdPerM: 0.31, serviceId: SERVICES.staging.id },
      { hardware: "NVIDIA H100 80GB ×1 (benchmark)", runtime: "vLLM 0.8.5", ttft: 84, itl: 8.1, tps: 14_200, usdPerM: 0.154, serviceId: "" },
    ],
    pulls: [
      { clusterId: CLUSTERS.serve.id, seconds: 117, source: "registry" },
      { clusterId: CLUSTERS.serve.id, seconds: 23, source: "peer" },
      { clusterId: CLUSTERS.serve.id, seconds: 9, source: "cache" },
      { clusterId: CLUSTERS.train.id, seconds: 49, source: "registry" },
    ],
  },
  {
    id: VERSIONS.reranker4.id,
    modelId: MODELS.reranker.id,
    version: VERSIONS.reranker4.version,
    digest: VERSIONS.reranker4.digest,
    createdAt: -30 * DAY,
    source: MlVersionSource.IMPORTED,
    runId: "",
    phaseKey: "",
    attempt: 0,
    checkpointId: "",
    baseModel: "BAAI/bge-reranker-v2-m3",
    createdBy: ML_PEOPLE.notebooks,
    card: {
      architecture: "XLMRobertaForSequenceClassification (cross-encoder)",
      paramsTotal: 567_754_752n,
      contextLength: 8_192,
      dtype: "fp16",
      quantization: "",
      tokenizer: "xlm-roberta (SentencePiece, 250,002 tokens)",
      license: "Apache-2.0",
      layers: 24,
      shards: 1,
      trainingData: "As published by BAAI (imported from Hugging Face at revision 953dc6f).",
      intendedUse: "Reranking help-center passages for the support assistant's retrieval.",
    },
    files: RERANKER_FILES,
    aliases: ["production"],
    serving: [{ serviceId: SERVICES.reranker.id, env: "prod", revisionId: REVISIONS.reranker, trafficShare: 1 }],
    gate: [
      { name: "NDCG@10 on support-rerank v2 ≥ previous", status: "pass", detail: "0.712 against 0.698 (offline, priya's notebook)." },
      { name: "Approval (delivery policy ml-admins)", status: "pass", detail: "Approved by jon 30 days ago." },
      { name: "Inference obs check on reranker-prod", status: "pass", detail: "p95 latency and error rate held.", evidence: revisionEvidence(REVISIONS.reranker) },
    ],
    perf: [{ hardware: "NVIDIA L4 ×1", runtime: "Triton 24.10 (TensorRT fp16)", ttft: Number.NaN, itl: Number.NaN, tps: 46_000, usdPerM: 0.006, serviceId: SERVICES.reranker.id }],
    pulls: [
      { clusterId: CLUSTERS.serve.id, seconds: 21, source: "registry" },
      { clusterId: CLUSTERS.serve.id, seconds: 4, source: "cache" },
    ],
  },
];

export const versionDef = (id: string) => VERSION_DEFS.find((v) => v.id === id);

// ── Alias history ─────────────────────────────────────────────────────────────

interface MoveDef {
  modelId: string;
  alias: string;
  from: string;
  to: string;
  at: number;
  by: string;
  reason: string;
  release: string;
}

export const ALIAS_MOVES: readonly MoveDef[] = [
  { modelId: MODELS.support.id, alias: "production", from: "", to: VERSIONS.v1.id, at: -46 * DAY + 3 * HOUR, by: ML_PEOPLE.admin, reason: "First release of the support assistant (pushed by hand).", release: "v1.4.0" },
  { modelId: MODELS.support.id, alias: "staging", from: "", to: VERSIONS.v1.id, at: -46 * DAY + 2 * HOUR, by: ML_PEOPLE.admin, reason: "First release of the support assistant.", release: "v1.4.0" },
  { modelId: MODELS.support.id, alias: "staging", from: VERSIONS.v1.id, to: VERSIONS.v2.id, at: -20.5 * DAY, by: ML_PEOPLE.trainer, reason: "release-gate v7 passed: support tickets +6.1 pt, every capability task up.", release: "v1.6.0" },
  { modelId: MODELS.support.id, alias: "production", from: VERSIONS.v1.id, to: VERSIONS.v2.id, at: -19 * DAY, by: ML_PEOPLE.admin, reason: "Promoted after 36 h in staging; approved by jon.", release: "v1.6.1" },
  {
    modelId: MODELS.support.id,
    alias: "staging",
    from: VERSIONS.v2.id,
    to: VERSIONS.v21fp8.id,
    at: -12 * DAY,
    by: ML_PEOPLE.trainer,
    reason: "fp8 serves 31% more tokens/s per L4 at the same judge score. Staging only: the safety gate fails.",
    release: "v1.7.2",
  },
  { modelId: MODELS.reranker.id, alias: "production", from: "", to: VERSIONS.reranker4.id, at: -30 * DAY, by: ML_PEOPLE.notebooks, reason: "Imported BAAI/bge-reranker-v2-m3 (fp16) to replace the hosted reranker API.", release: "v1.5.0" },
];

const moveInit = (m: MoveDef, now: () => number) => ({ alias: m.alias, fromVersionId: m.from, toVersionId: m.to, at: ts(m.at, now), by: m.by, reason: m.reason, release: m.release });

const ALIAS_ENV: Record<string, string> = { production: "prod", staging: "staging" };

/** The current aliases of a model: the latest move of each. */
function aliasesOf(modelId: string, now: () => number) {
  const latest = new Map<string, MoveDef>();
  for (const m of ALIAS_MOVES.filter((x) => x.modelId === modelId).sort((a, b) => a.at - b.at)) latest.set(m.alias, m);
  return [...latest.values()].map((m) => ({ name: m.alias, versionId: m.to, env: ALIAS_ENV[m.alias] ?? "", movedAt: ts(m.at, now), movedBy: m.by }));
}

// ── Answers ───────────────────────────────────────────────────────────────────

function gateSummary(v: VersionDef) {
  const counted = v.gate.filter((c) => c.status !== "skipped");
  const passed = counted.filter((c) => c.status === "pass").length;
  const status = counted.length === 0 ? "" : counted.some((c) => c.status === "fail") ? "fail" : counted.some((c) => c.status === "pending") ? "pending" : "pass";
  return { status, passed, total: counted.length };
}

function versionInit(v: VersionDef, now: () => number) {
  const gate = gateSummary(v);
  return {
    id: v.id,
    modelId: v.modelId,
    version: v.version,
    digest: v.digest,
    createdAt: ts(v.createdAt, now),
    source: v.source,
    runId: v.runId,
    phaseKey: v.phaseKey,
    attempt: v.attempt,
    checkpointId: v.checkpointId,
    baseModel: v.baseModel,
    card: {
      ...v.card,
      paramsActive: v.card.paramsTotal,
      sizeBytes: BigInt(weightBytes(v.files)),
      adapters: [],
      mediaType: "application/vnd.cncf.model.manifest.v1+json",
    },
    gateStatus: gate.status,
    gatePassed: gate.passed,
    gateTotal: gate.total,
    aliases: [...v.aliases],
    serving: v.serving.map((s) => ({ ...s })),
    createdBy: v.createdBy,
  };
}

const MODEL_DESCRIPTIONS: Record<string, string> = {
  [MODELS.support.id]: "The shop's customer-support assistant: Llama 3.1 8B, continued-pretrained and SFT'd on support data.",
  [MODELS.reranker.id]: "Cross-encoder reranker for the support assistant's help-center retrieval.",
};

/** Requests per second each model serves now (prod plus staging). */
function servingRps(modelId: string, now: () => number): number {
  const d = new Date(now());
  const prod = prodRps(d.getUTCHours() + d.getUTCMinutes() / 60);
  return modelId === MODELS.support.id ? prod + 1.7 : prod * 1.8;
}

function modelInit(model: (typeof MODELS)[keyof typeof MODELS], now: () => number) {
  const versions = VERSION_DEFS.filter((v) => v.modelId === model.id);
  const latest = [...versions].sort((a, b) => b.createdAt - a.createdAt)[0];
  const gated = [...versions].filter((v) => gateSummary(v).status && gateSummary(v).status !== "").sort((a, b) => b.createdAt - a.createdAt)[0];
  const services = new Set(versions.flatMap((v) => v.serving.map((s) => s.serviceId)));
  const moves = ALIAS_MOVES.filter((m) => m.modelId === model.id);
  return {
    id: model.id,
    name: model.name,
    project: ML_PROJECT,
    repository: model.repository,
    description: MODEL_DESCRIPTIONS[model.id] ?? "",
    latestVersionId: latest?.id ?? "",
    versionCount: versions.length,
    aliases: aliasesOf(model.id, now),
    servingServices: services.size,
    servingRps: servingRps(model.id, now),
    lastGateStatus: gated ? gateSummary(gated).status : "",
    lastGateVersionId: gated?.id ?? "",
    updatedAt: ts(Math.max(...moves.map((m) => m.at), ...versions.map((v) => v.createdAt)), now),
  };
}

function promotionOf(modelId: string) {
  if (modelId === MODELS.support.id) {
    const fp8 = versionDef(VERSIONS.v21fp8.id)!;
    return [
      {
        env: "staging",
        versionId: VERSIONS.v21fp8.id,
        checks: [
          { name: "safety_refusals ≥ 0.97 (staging)", status: "pass", detail: "0.979", evidence: evalEvidence(EVAL_RUNS.v21fp8, "release-gate v7 on v2.1-fp8") },
          { name: "Approval", status: "skipped", detail: "Staging promotes without approval." },
        ],
        canPromote: false,
        action: "v2.1-fp8 already serves staging. The candidate (v3 sft@40,000) lands here once it is registered as a version.",
      },
      {
        env: "prod",
        versionId: VERSIONS.v21fp8.id,
        checks: fp8.gate.map((c) => ({ ...c })),
        canPromote: false,
        action: "opens a release PR promoting v2.1-fp8 to prod",
      },
    ];
  }
  return [
    {
      env: "prod",
      versionId: VERSIONS.reranker4.id,
      checks: versionDef(VERSIONS.reranker4.id)!.gate.map((c) => ({ ...c })),
      canPromote: false,
      action: "v4 already serves prod; there is no newer version.",
    },
  ];
}

const modelById = (id: string) => Object.values(MODELS).find((m) => m.id === id);

function getModel(modelId: string, now: () => number) {
  const model = modelById(modelId);
  if (!model) return undefined;
  return {
    model: modelInit(model, now),
    versions: VERSION_DEFS.filter((v) => v.modelId === model.id)
      .sort((a, b) => b.createdAt - a.createdAt)
      .map((v) => versionInit(v, now)),
    aliasHistory: ALIAS_MOVES.filter((m) => m.modelId === model.id)
      .sort((a, b) => b.at - a.at)
      .map((m) => moveInit(m, now)),
    promotion: promotionOf(model.id),
  };
}

function getVersion(versionId: string, now: () => number) {
  const v = versionDef(versionId);
  if (!v) return undefined;
  return {
    version: versionInit(v, now),
    files: v.files.map((f) => ({ path: f.path, sizeBytes: BigInt(f.size), mediaType: f.mediaType, digest: fakeDigest(`${v.id}:${f.path}`) })),
    gate: v.gate.map((c) => ({ ...c })),
    perf: v.perf.map((p) => ({ hardware: p.hardware, runtime: p.runtime, ttftP95Ms: p.ttft, itlP95Ms: p.itl, tokensPerSecondPerGpu: p.tps, usdPerMillionTokens: p.usdPerM, serviceId: p.serviceId })),
    pulls: v.pulls.map((p) => ({ clusterId: p.clusterId, sizeBytes: BigInt(weightBytes(v.files)), pullSeconds: p.seconds, source: p.source })),
    aliasHistory: ALIAS_MOVES.filter((m) => m.to === v.id || m.from === v.id)
      .sort((a, b) => b.at - a.at)
      .map((m) => moveInit(m, now)),
  };
}

// ── Events ────────────────────────────────────────────────────────────────────

function modelEvents(modelId: string | undefined, versionId: string | undefined, now: () => number) {
  const out = [];
  for (const m of ALIAS_MOVES) {
    if (modelId && m.modelId !== modelId) continue;
    if (versionId && m.to !== versionId && m.from !== versionId) continue;
    const to = versionDef(m.to)!;
    const promotion = m.alias === "production";
    out.push({
      id: `ev-alias-${m.modelId}-${m.alias}-${to.version}`,
      at: ts(m.at, now),
      kind: promotion ? MlEventKind.RELEASE : MlEventKind.ALIAS_MOVE,
      severity: MlSeverity.INFO,
      title: `${m.modelId}: ${m.alias} → ${to.version}${m.from ? ` (was ${versionDef(m.from)!.version})` : ""}`,
      detail: `${m.reason} Release ${m.release}, by ${m.by}.`,
      subject: { kind: MlSubjectKind.MODEL_VERSION, id: to.id, label: `${m.modelId} ${to.version}` },
      related: [{ kind: MlSubjectKind.MODEL, id: m.modelId, label: m.modelId }, ...(m.from ? [{ kind: MlSubjectKind.MODEL_VERSION, id: m.from, label: `${m.modelId} ${versionDef(m.from)!.version}` }] : [])],
      step: 0n,
      source: "person",
      attributes: { alias: m.alias, release: m.release } as Record<string, string>,
      hookEventType: "ml.model.alias_moved",
    });
  }
  for (const v of VERSION_DEFS) {
    if (modelId && v.modelId !== modelId) continue;
    if (versionId && v.id !== versionId) continue;
    out.push({
      id: `ev-version-${v.id}`,
      at: ts(v.createdAt, now),
      kind: MlEventKind.COMPLETED,
      severity: MlSeverity.INFO,
      title: `${v.modelId} ${v.version} registered`,
      detail: v.source === MlVersionSource.TRAINED ? `From ${v.runId === RUNS.v2.id ? RUNS.v2.name : v.runId} ${v.phaseKey} attempt ${v.attempt}.` : v.source === MlVersionSource.IMPORTED ? `Imported from Hugging Face (${v.baseModel}).` : "Pushed by hand.",
      subject: { kind: MlSubjectKind.MODEL_VERSION, id: v.id, label: `${v.modelId} ${v.version}` },
      related: [{ kind: MlSubjectKind.MODEL, id: v.modelId, label: v.modelId }, ...(v.checkpointId ? [{ kind: MlSubjectKind.CHECKPOINT, id: v.checkpointId, label: v.checkpointId.split("/").slice(1).join("@") }] : [])],
      step: 0n,
      source: "forge",
      attributes: { digest: v.digest } as Record<string, string>,
      hookEventType: "ml.model.version_created",
    });
  }
  return out;
}

type EventsReq = EventsQuery;

function events(req: EventsReq, now: () => number) {
  const s = req.subject;
  if (!s) return undefined;
  if (s.kind === MlSubjectKind.MODEL) return modelById(s.id) ? { events: filterEvents(modelEvents(s.id, undefined, now), req) } : undefined;
  if (s.kind === MlSubjectKind.MODEL_VERSION) return versionDef(s.id) ? { events: filterEvents(modelEvents(undefined, s.id, now), req) } : undefined;
  if (s.kind === MlSubjectKind.ORG) {
    // The org's feed: alias moves and promotions only (registrations are the model page's).
    return { events: filterEvents(modelEvents(undefined, undefined, now).filter((e) => e.kind !== MlEventKind.COMPLETED), req) };
  }
  return undefined;
}

export const modelsAnswers: MlDomain = {
  listMlModels: (_req, now) => ({ models: Object.values(MODELS).map((m) => modelInit(m, now)) }),
  getMlModel: (req, now) => getModel(req.modelId, now),
  getMlModelVersion: (req, now) => getVersion(req.versionId, now),
};

/** The registry's events (registrations, alias moves, promotions), served by ./telemetry.ts. */
export const modelsTelemetry: MlTelemetryDomain = {
  events: (req, now) => events(req, now),
};
