// Copyright (c) 2026 Reliant Labs

/**
 * The ML fixture world's Lineage: GetMlLineage for any subject in the story.
 *
 * One graph holds the whole story, left to right in the contract's columns:
 * inputs (datasets and base models) 0 → run phases 1 (attempts collapsed
 * into the phase; phase → phase edges carry the checkpoint that links them)
 * → checkpoints 2 → model versions (and the candidate's gate eval) 3 →
 * releases (each service revision) 4 → services 5.
 *
 * A subject's answer is its neighbourhood in the DIRECTED sense: everything
 * upstream of it (what it came from) and everything downstream (what it
 * became), never a sibling reached only through a shared input — v3's page
 * shows the base model it started from, not v2's chain from the same base.
 * A subject with no node of its own (an attempt, a rank, a checkpoint that
 * was never promoted) is answered for the phase that holds it.
 */

import { MlLineageEdgeKind, MlSubjectKind } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { evalScore, v3ValLoss } from "./curves";
import type { MlDomain } from "./domain";
import {
  BASE_MODEL,
  checkpointId,
  DATASETS,
  EVAL_RUNS,
  MODELS,
  periodicEvalRunId,
  phaseId,
  RELEASE_PROD,
  RELEASE_STAGING,
  REVISIONS,
  RUNS,
  SERVICES,
  V21_FP8_CHECKPOINT,
  V2_FINAL_CHECKPOINT,
  V3_CANDIDATE_CHECKPOINT,
  V3_NOW,
  VERSIONS,
} from "./world";

export interface LineageNodeDef {
  subject: { kind: MlSubjectKind; id: string; label: string };
  title: string;
  subtitle: string;
  status: string;
  column: number;
}

export interface LineageEdgeDef {
  from: string;
  to: string;
  kind: MlLineageEdgeKind;
  label: string;
}

/** The v2 run's SFT data (an older version of support-sft, local to this file). */
const SFT_V2 = { id: "ds_support_sft_v2", name: "support-sft", version: "v2" };
const HF_BASE = `hf:${BASE_MODEL}`;
const HF_RERANKER = "hf:BAAI/bge-reranker-v2-m3";
/** v3's latest durable checkpoint. */
const V3_LATEST = checkpointId(RUNS.v3.id, "sft", 41_000);

const node = (kind: MlSubjectKind, id: string, label: string, title: string, subtitle: string, status: string, column: number): LineageNodeDef => ({
  subject: { kind, id, label },
  title,
  subtitle,
  status,
  column,
});

/** "llama-8b-support-v3" → "v3": the run's short name, so a phase node reads "v3 · sft". */
const short = (run: { name: string }) => run.name.replace(/^llama-8b-support-/, "");
const phase = (run: { id: string; name: string }, key: string, subtitle: string, status: string, title = key) =>
  node(MlSubjectKind.PHASE, phaseId(run.id, key), `${run.name} ${key}`, `${short(run)} · ${title}`, subtitle, status, 1);

const ckpt = (id: string, title: string, subtitle: string, status = "ok") => node(MlSubjectKind.CHECKPOINT, id, title, title, subtitle, status, 2);

const tokens = (n: number) => `${(n / 1e9).toFixed(n >= 1e10 ? 0 : 1)}B tokens`;

export const LINEAGE_NODES: readonly LineageNodeDef[] = [
  // Inputs
  node(MlSubjectKind.UNSPECIFIED, HF_BASE, "Llama-3.1-8B", "Llama-3.1-8B", "base model · Hugging Face", "", 0),
  node(MlSubjectKind.DATASET, DATASETS.domain.id, "support-corpus v4", `${DATASETS.domain.name} ${DATASETS.domain.version}`, tokens(DATASETS.domain.tokens), "", 0),
  node(MlSubjectKind.DATASET, DATASETS.code.id, "the-stack-v2-dedup", `${DATASETS.code.name} ${DATASETS.code.version}`, tokens(DATASETS.code.tokens), "", 0),
  node(MlSubjectKind.DATASET, DATASETS.sft.id, "support-sft v3", `${DATASETS.sft.name} ${DATASETS.sft.version}`, tokens(DATASETS.sft.tokens), "", 0),
  node(MlSubjectKind.DATASET, DATASETS.prefs.id, "support-prefs v2", `${DATASETS.prefs.name} ${DATASETS.prefs.version}`, tokens(DATASETS.prefs.tokens), "", 0),
  node(MlSubjectKind.DATASET, SFT_V2.id, "support-sft v2", `${SFT_V2.name} ${SFT_V2.version}`, "238B tokens", "", 0),
  node(MlSubjectKind.UNSPECIFIED, HF_RERANKER, "bge-reranker-v2-m3", "bge-reranker-v2-m3", "BAAI · Hugging Face", "", 0),
  // Phases
  phase(RUNS.v3, "pretrain", "2 attempts · 20,000 steps", "ok"),
  phase(RUNS.v3, "sft", `3 attempts · ${V3_NOW.step.toLocaleString("en-US")} / 60,000`, "running"),
  phase(RUNS.v3, "eval-periodic", "every 2,000 steps · release-gate-lite", "running"),
  phase(RUNS.v3, "rl", "pending on sft", "", "rl (GRPO)"),
  phase(RUNS.v2, "sft", "1 attempt · 60,000 steps", "ok"),
  phase(RUNS.v2, "quantize", "FP8 E4M3 · 1 attempt", "ok"),
  node(MlSubjectKind.RUN, RUNS.v1.id, RUNS.v1.name, RUNS.v1.name, "failed: Bus error (/dev/shm 64Mi)", "failed", 1),
  // Checkpoints
  ckpt(V3_CANDIDATE_CHECKPOINT, "v3 sft@40,000", `candidate · val loss ${v3ValLoss("sft", 40_000).toFixed(3)}`),
  ckpt(V3_LATEST, "v3 sft@41,000", `latest · val loss ${v3ValLoss("sft", 41_000).toFixed(3)}`, "running"),
  ckpt(V2_FINAL_CHECKPOINT, "v2 sft@60,000", "final · bf16 16.1 GB"),
  ckpt(V21_FP8_CHECKPOINT, "v2 fp8 export", "quantize · 8.5 GB"),
  // Versions (and the candidate's gate eval, where its version will be)
  node(MlSubjectKind.MODEL_VERSION, VERSIONS.v1.id, "support-assistant v1", "v1", "support-assistant · pushed by hand", "", 3),
  node(MlSubjectKind.MODEL_VERSION, VERSIONS.v2.id, "support-assistant v2", "v2 · production", `support-assistant · ${VERSIONS.v2.digest.slice(7, 13)}`, "serving", 3),
  node(MlSubjectKind.MODEL_VERSION, VERSIONS.v21fp8.id, "support-assistant v2.1-fp8", "v2.1-fp8 · staging", "+10% prod · gate ✕ safety", "serving", 3),
  node(MlSubjectKind.MODEL_VERSION, VERSIONS.reranker4.id, "bge-reranker v4", "bge-reranker v4", "production · imported", "serving", 3),
  node(MlSubjectKind.EVAL_RUN, EVAL_RUNS.v3ckpt, "release-gate v7 eval", "gate eval (v7)", `gate ✕ GSM8K ${((evalScore("gsm8k", "v3@40000") - evalScore("gsm8k", "v2")) * 100).toFixed(1).replace("-", "−")} pt`, "failed", 3),
  // Releases (each service's revision at that release)
  node(MlSubjectKind.REVISION, REVISIONS.prodCurrent, `release ${RELEASE_PROD}`, `release ${RELEASE_PROD}`, "prod · 90%", "serving", 4),
  node(MlSubjectKind.REVISION, REVISIONS.prodFp8, `release ${RELEASE_PROD}+fp8`, `${RELEASE_PROD}+fp8`, "fp8 arm · 10% of prod", "serving", 4),
  node(MlSubjectKind.REVISION, REVISIONS.staging, `release ${RELEASE_STAGING}`, `release ${RELEASE_STAGING}`, "staging · 100%", "serving", 4),
  node(MlSubjectKind.REVISION, REVISIONS.reranker, `release ${RELEASE_PROD}`, `release ${RELEASE_PROD}`, "reranker prod · 100%", "serving", 4),
  // Services
  node(MlSubjectKind.INFERENCE_SERVICE, SERVICES.prod.id, SERVICES.prod.id, "prod service", "support-assistant · vLLM", "serving", 5),
  node(MlSubjectKind.INFERENCE_SERVICE, SERVICES.staging.id, SERVICES.staging.id, "staging service", "support-assistant · vLLM", "serving", 5),
  node(MlSubjectKind.INFERENCE_SERVICE, SERVICES.reranker.id, SERVICES.reranker.id, "prod service", "reranker · Triton", "serving", 5),
];

const e = (from: string, to: string, kind: MlLineageEdgeKind, label = ""): LineageEdgeDef => ({ from, to, kind, label });
const P = (run: { id: string }, key: string) => phaseId(run.id, key);

export const LINEAGE_EDGES: readonly LineageEdgeDef[] = [
  // v3
  e(HF_BASE, P(RUNS.v3, "pretrain"), MlLineageEdgeKind.INITIALIZED_FROM, "initialized from"),
  e(DATASETS.domain.id, P(RUNS.v3, "pretrain"), MlLineageEdgeKind.TRAINED_ON, "85% of the mix"),
  e(DATASETS.code.id, P(RUNS.v3, "pretrain"), MlLineageEdgeKind.TRAINED_ON, "15% of the mix"),
  e(P(RUNS.v3, "pretrain"), P(RUNS.v3, "sft"), MlLineageEdgeKind.INITIALIZED_FROM, "from checkpoint 20,000"),
  e(DATASETS.sft.id, P(RUNS.v3, "sft"), MlLineageEdgeKind.TRAINED_ON, "trained on"),
  e(P(RUNS.v3, "sft"), P(RUNS.v3, "eval-periodic"), MlLineageEdgeKind.EVALUATED, "every 2,000 steps"),
  e(P(RUNS.v3, "sft"), P(RUNS.v3, "rl"), MlLineageEdgeKind.INITIALIZED_FROM, "from the best sft checkpoint (pending)"),
  e(DATASETS.prefs.id, P(RUNS.v3, "rl"), MlLineageEdgeKind.TRAINED_ON, "rollout prompts"),
  e(P(RUNS.v3, "sft"), V3_CANDIDATE_CHECKPOINT, MlLineageEdgeKind.PRODUCED, "attempt 3"),
  e(P(RUNS.v3, "sft"), V3_LATEST, MlLineageEdgeKind.PRODUCED, "attempt 3"),
  e(V3_CANDIDATE_CHECKPOINT, EVAL_RUNS.v3ckpt, MlLineageEdgeKind.EVALUATED, "release-gate v7"),
  // v2
  e(HF_BASE, P(RUNS.v2, "sft"), MlLineageEdgeKind.INITIALIZED_FROM, "initialized from"),
  e(SFT_V2.id, P(RUNS.v2, "sft"), MlLineageEdgeKind.TRAINED_ON, "trained on"),
  e(P(RUNS.v2, "sft"), P(RUNS.v2, "quantize"), MlLineageEdgeKind.INITIALIZED_FROM, "from checkpoint 60,000"),
  e(P(RUNS.v2, "sft"), V2_FINAL_CHECKPOINT, MlLineageEdgeKind.PRODUCED, "attempt 1"),
  e(P(RUNS.v2, "quantize"), V21_FP8_CHECKPOINT, MlLineageEdgeKind.PRODUCED, "attempt 1"),
  e(V2_FINAL_CHECKPOINT, VERSIONS.v2.id, MlLineageEdgeKind.PROMOTED_TO, "registered as v2"),
  e(V21_FP8_CHECKPOINT, VERSIONS.v21fp8.id, MlLineageEdgeKind.PROMOTED_TO, "registered as v2.1-fp8"),
  // v1: the run failed and produced nothing; v1 was pushed by hand.
  e(HF_BASE, RUNS.v1.id, MlLineageEdgeKind.INITIALIZED_FROM, "initialized from"),
  e(SFT_V2.id, RUNS.v1.id, MlLineageEdgeKind.TRAINED_ON, "trained on"),
  e(HF_BASE, VERSIONS.v1.id, MlLineageEdgeKind.BUILT_FROM, "fine-tuned outside the platform, pushed by hand"),
  // Serving
  e(VERSIONS.v2.id, REVISIONS.prodCurrent, MlLineageEdgeKind.SHIPPED_IN, "90% of prod"),
  e(VERSIONS.v21fp8.id, REVISIONS.prodFp8, MlLineageEdgeKind.SHIPPED_IN, "10% of prod (Changes arm)"),
  e(VERSIONS.v21fp8.id, REVISIONS.staging, MlLineageEdgeKind.SHIPPED_IN, "100% of staging"),
  e(REVISIONS.prodCurrent, SERVICES.prod.id, MlLineageEdgeKind.SERVES, "serves"),
  e(REVISIONS.prodFp8, SERVICES.prod.id, MlLineageEdgeKind.SERVES, "serves"),
  e(REVISIONS.staging, SERVICES.staging.id, MlLineageEdgeKind.SERVES, "serves"),
  // Reranker
  e(HF_RERANKER, VERSIONS.reranker4.id, MlLineageEdgeKind.BUILT_FROM, "imported (fp16)"),
  e(VERSIONS.reranker4.id, REVISIONS.reranker, MlLineageEdgeKind.SHIPPED_IN, "100% of prod"),
  e(REVISIONS.reranker, SERVICES.reranker.id, MlLineageEdgeKind.SERVES, "serves"),
];

const NODE_IDS = new Set(LINEAGE_NODES.map((n) => n.subject.id));

/** The graph's node ids that stand for `subject` (its own node, or the phase that holds it). */
export function lineageSeeds(subject: { kind: MlSubjectKind; id: string }): string[] {
  const { kind, id } = subject;
  if (NODE_IDS.has(id)) {
    // A run's own node (v1) or any node named exactly.
    if (kind !== MlSubjectKind.RUN) return [id];
  }
  const parts = id.split("/");
  switch (kind) {
    case MlSubjectKind.RUN:
      return LINEAGE_NODES.filter((n) => n.subject.id === id || n.subject.id.startsWith(`${id}/`)).map((n) => n.subject.id);
    case MlSubjectKind.ATTEMPT:
    case MlSubjectKind.RANK:
    case MlSubjectKind.CHECKPOINT:
    case MlSubjectKind.PHASE: {
      const p = phaseId(parts[0], parts[1] ?? "");
      return NODE_IDS.has(p) ? [p] : NODE_IDS.has(parts[0]) ? [parts[0]] : [];
    }
    case MlSubjectKind.MODEL: {
      const versions = LINEAGE_NODES.filter((n) => n.subject.kind === MlSubjectKind.MODEL_VERSION && n.subject.id.startsWith(`${id}/`)).map((n) => n.subject.id);
      // The support assistant's candidate is part of its family before it is a version.
      return id === MODELS.support.id ? [...versions, V3_CANDIDATE_CHECKPOINT] : versions;
    }
    case MlSubjectKind.EVAL_RUN:
      // eval-periodic's runs belong to the phase.
      return id.startsWith(periodicEvalRunId(0).slice(0, -1)) ? [phaseId(RUNS.v3.id, "eval-periodic")] : [];
    case MlSubjectKind.REPLICA:
      return NODE_IDS.has(parts[0]) ? [parts[0]] : [];
    default:
      return [];
  }
}

/** Everything upstream and downstream of `seeds`. */
export function lineageAround(seeds: readonly string[]): { nodes: LineageNodeDef[]; edges: LineageEdgeDef[] } {
  const walk = (dir: "up" | "down") => {
    const seen = new Set(seeds);
    const queue = [...seeds];
    while (queue.length) {
      const id = queue.shift()!;
      for (const edge of LINEAGE_EDGES) {
        const next = dir === "up" ? (edge.to === id ? edge.from : null) : edge.from === id ? edge.to : null;
        if (next && !seen.has(next)) {
          seen.add(next);
          queue.push(next);
        }
      }
    }
    return seen;
  };
  const keep = new Set([...walk("up"), ...walk("down")]);
  return {
    nodes: LINEAGE_NODES.filter((n) => keep.has(n.subject.id)),
    edges: LINEAGE_EDGES.filter((x) => keep.has(x.from) && keep.has(x.to)),
  };
}

export const lineageAnswers: MlDomain = {
  getMlLineage: (req) => {
    if (!req.subject) return undefined;
    const seeds = lineageSeeds(req.subject);
    if (seeds.length === 0) return undefined;
    const { nodes, edges } = lineageAround(seeds);
    return { nodes: nodes.map((n) => ({ ...n, subject: { ...n.subject } })), edges: edges.map((x) => ({ ...x })) };
  },
};
