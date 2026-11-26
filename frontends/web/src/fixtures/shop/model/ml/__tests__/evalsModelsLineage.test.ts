// Copyright (c) 2026 Reliant Labs

/**
 * The Evals, Models and Lineage fixtures hold the story together: every
 * matrix cell is curves.ts evalScore, deltas and significance follow from
 * the paired statistics the samples drill reports, gates fail exactly where
 * world.ts says, serving placements are the inference revisions' shares, and
 * every lineage edge joins two nodes.
 */

import { create, toBinary } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import {
  GetMlEvalMatrixResponseSchema,
  GetMlLineageResponseSchema,
  GetMlModelResponseSchema,
  GetMlModelVersionResponseSchema,
  ListMlEvalRunsResponseSchema,
  ListMlEvalSamplesResponseSchema,
  MlEarlyAccessService,
  MlSubjectKind,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { answerMl } from "../../../handlers/ml";
import { evalScore, type EvalTask } from "../curves";
import { ALL_EVAL_RUNS, evalsAnswers, GSM8K_SAMPLES, mcnemarP, pairStats, PERIODIC_STEPS, SAFETY_SAMPLES, SUPPORT_SAMPLES } from "../evals";
import { LINEAGE_EDGES, LINEAGE_NODES, lineageAnswers } from "../lineage";
import { modelsAnswers, VERSION_DEFS } from "../models";
import { checkpointId, EVAL_RUNS, EVAL_SUITES, EVAL_TASKS, MODELS, periodicEvalRunId, phaseId, REVISIONS, RUNS, SERVICES, V3_CANDIDATE_CHECKPOINT, VERSIONS } from "../world";

const NOW = Date.UTC(2026, 10, 26, 15, 0);
const now = () => NOW;

const KEY: Record<string, string> = {
  [VERSIONS.v2.id]: "v2",
  [VERSIONS.v21fp8.id]: "v2.1-fp8",
  [VERSIONS.v1.id]: "v1",
  [V3_CANDIDATE_CHECKPOINT]: "v3@40000",
  "external/openai/gpt-4o-mini": "gpt-4o-mini",
};

const matrixOf = (baseline?: string) =>
  create(GetMlEvalMatrixResponseSchema, evalsAnswers.getMlEvalMatrix!({ orgId: "o", suiteId: EVAL_SUITES.gate.id, ...(baseline ? { baseline } : {}) } as never, now) as never);

describe("the eval matrix", () => {
  const m = matrixOf();

  it("has the five gate rows against production v2", () => {
    expect(m.baselineRowId).toBe(VERSIONS.v2.id);
    expect(m.rows.map((r) => r.id)).toEqual([VERSIONS.v2.id, VERSIONS.v21fp8.id, V3_CANDIDATE_CHECKPOINT, VERSIONS.v1.id, "external/openai/gpt-4o-mini"]);
    expect(m.cells).toHaveLength(5 * EVAL_TASKS.length);
  });

  it("every cell is evalScore, and its delta is against the baseline row", () => {
    for (const c of m.cells) {
      const key = KEY[c.rowId];
      expect(c.value).toBeCloseTo(evalScore(c.taskKey as EvalTask, key), 10);
      expect(c.delta).toBeCloseTo(evalScore(c.taskKey as EvalTask, key) - evalScore(c.taskKey as EvalTask, "v2"), 10);
      expect(c.regression).toBe(c.delta < 0);
    }
  });

  it("significance is the paired test the samples drill reports", () => {
    for (const c of m.cells.filter((x) => x.rowId !== VERSIONS.v2.id)) {
      expect(c.significant).toBe(pairStats(c.taskKey as EvalTask, KEY[c.rowId], "v2", c.n).significant);
    }
    const cell = (row: string, task: string) => m.cells.find((c) => c.rowId === row && c.taskKey === task)!;
    expect(cell(V3_CANDIDATE_CHECKPOINT, "gsm8k")).toMatchObject({ significant: true, regression: true, gateFail: true });
    expect(cell(V3_CANDIDATE_CHECKPOINT, "safety_refusals")).toMatchObject({ significant: false, regression: true, gateFail: false });
    expect(cell(VERSIONS.v21fp8.id, "safety_refusals")).toMatchObject({ significant: true, gateFail: true });
    expect(cell(VERSIONS.v21fp8.id, "safety_refusals").value).toBeCloseTo(0.979, 6);
    expect(cell(VERSIONS.v21fp8.id, "long_context_ruler").significant).toBe(false);
  });

  it("gates fail where the story says", () => {
    const row = (id: string) => m.rows.find((r) => r.id === id)!;
    expect(row(VERSIONS.v2.id).gateStatus).toBe("pass");
    expect(row(VERSIONS.v21fp8.id)).toMatchObject({ gateStatus: "fail", gateFailures: ["safety_refusals ≥ 0.98"] });
    expect(row(V3_CANDIDATE_CHECKPOINT)).toMatchObject({ gateStatus: "fail", alias: "candidate" });
    expect(row(V3_CANDIDATE_CHECKPOINT).gateFailures).toEqual(["No capability task regresses by more than 1 pt (95%)"]);
    expect(row("external/openai/gpt-4o-mini").gateStatus).toBe("");
    expect(row(V3_CANDIDATE_CHECKPOINT).judgeWinRate).toBeCloseTo(0.56, 1);
    expect(Number.isNaN(row(VERSIONS.v2.id).judgeWinRate)).toBe(true);
  });

  it("re-bases on another row", () => {
    const fp8 = matrixOf(VERSIONS.v21fp8.id);
    expect(fp8.baselineRowId).toBe(VERSIONS.v21fp8.id);
    const c = fp8.cells.find((x) => x.rowId === VERSIONS.v2.id && x.taskKey === "safety_refusals")!;
    expect(c.delta).toBeCloseTo(0.012, 6);
  });
});

describe("eval runs and samples", () => {
  it("every eval run's results are evalScore of its subject", () => {
    const runs = create(ListMlEvalRunsResponseSchema, evalsAnswers.listMlEvalRuns!({ orgId: "o" } as never, now) as never).evalRuns;
    expect(runs).toHaveLength(ALL_EVAL_RUNS.length);
    for (const run of runs) {
      expect(run.startedAt!.seconds < run.endedAt!.seconds).toBe(true);
      expect(Number(run.endedAt!.seconds) * 1000).toBeLessThanOrEqual(NOW);
      expect(run.cost!.usd).toBeGreaterThan(0);
      for (const r of run.results) {
        if (r.slices.length) {
          // Each slice dimension averages back to the task's score.
          const dims = new Map<string, { sum: number; n: number }>();
          for (const s of r.slices) {
            const dim = s.slice.split("=")[0];
            const d = dims.get(dim) ?? { sum: 0, n: 0 };
            d.sum += s.value * s.n;
            d.n += s.n;
            dims.set(dim, d);
          }
          for (const d of dims.values()) expect(Math.abs(d.sum / d.n - r.value)).toBeLessThan(0.01);
        }
      }
    }
  });

  it("eval-periodic ran the lite suite on every 2,000th v3 SFT checkpoint", () => {
    const runs = create(ListMlEvalRunsResponseSchema, evalsAnswers.listMlEvalRuns!({ orgId: "o", suiteId: EVAL_SUITES.lite.id } as never, now) as never).evalRuns;
    for (const step of PERIODIC_STEPS) {
      const run = runs.find((r) => r.id === periodicEvalRunId(step))!;
      expect(run.subject).toMatchObject({ kind: MlSubjectKind.CHECKPOINT, id: checkpointId(RUNS.v3.id, "sft", step) });
      expect(run.phase?.id).toBe(phaseId(RUNS.v3.id, "eval-periodic"));
      expect(run.results.find((r) => r.taskKey === "gsm8k")!.value).toBeCloseTo(evalScore("gsm8k", `v3@${step}`), 10);
    }
    // Ordered in time with the steps.
    const ends = PERIODIC_STEPS.map((s) => Number(runs.find((r) => r.id === periodicEvalRunId(s))!.endedAt!.seconds));
    expect([...ends].sort((a, b) => a - b)).toEqual(ends);
  });

  it("the samples drill's counts are the matrix's paired test, and its flips agree with the scores", () => {
    const res = create(
      ListMlEvalSamplesResponseSchema,
      evalsAnswers.listMlEvalSamples!({ orgId: "o", evalRunId: EVAL_RUNS.v3ckpt, taskKey: "gsm8k", baselineEvalRunId: EVAL_RUNS.v2 } as never, now) as never,
    );
    const stats = pairStats("gsm8k", "v3@40000", "v2", 1319);
    expect(res).toMatchObject({ wins: stats.wins, losses: stats.losses, ties: stats.ties, total: 1319 });
    expect(res.wins + res.losses + res.ties).toBe(1319);
    expect(res.pValue).toBeLessThan(0.05);
    expect(res.losses - res.wins).toBe(Math.round((evalScore("gsm8k", "v2") - evalScore("gsm8k", "v3@40000")) * 1319));
    expect(res.samples.length).toBe(GSM8K_SAMPLES.length);
    const shown = { regressed: 0, improved: 0 };
    for (const s of res.samples) {
      expect(s.flip).toBe(s.score < s.baselineScore ? "regressed" : s.score > s.baselineScore ? "improved" : "same");
      if (s.flip === "regressed") shown.regressed++;
      if (s.flip === "improved") shown.improved++;
    }
    expect(shown.regressed).toBeLessThanOrEqual(res.losses);
    expect(shown.improved).toBeLessThanOrEqual(res.wins);
    expect(shown.regressed).toBeGreaterThan(shown.improved);
    const regressed = evalsAnswers.listMlEvalSamples!({ orgId: "o", evalRunId: EVAL_RUNS.v3ckpt, taskKey: "gsm8k", filter: "regressed" } as never, now)!;
    expect(regressed.samples!.every((s) => s.flip === "regressed")).toBe(true);
  });

  it("the safety drill shows fp8 complying where v2 refused", () => {
    const res = evalsAnswers.listMlEvalSamples!({ orgId: "o", evalRunId: EVAL_RUNS.v21fp8, taskKey: "safety_refusals" } as never, now)!;
    expect(res.samples).toHaveLength(SAFETY_SAMPLES.length);
    expect(res.samples!.filter((s) => s.flip === "regressed").length).toBeGreaterThan(10);
    expect(res.pValue).toBeLessThan(0.05);
    const support = evalsAnswers.listMlEvalSamples!({ orgId: "o", evalRunId: EVAL_RUNS.v3ckpt, taskKey: "support_tickets_v3", slice: "lang=de" } as never, now)!;
    expect(support.samples!.length).toBeGreaterThan(0);
    expect(support.samples!.every((s) => s.slice!.includes("lang=de"))).toBe(true);
    expect(SUPPORT_SAMPLES.every((s) => s.score >= 1 && s.score <= 5 && s.rationale)).toBe(true);
  });

  it("McNemar is two-sided and calibrated", () => {
    expect(mcnemarP(10, 10)).toBeCloseTo(1, 1);
    expect(mcnemarP(0, 20)).toBeLessThan(0.001);
    expect(mcnemarP(20, 36)).toBeLessThan(0.05);
  });
});

describe("models", () => {
  it("versions resolve, and serving placements are the inference revisions' shares", () => {
    const sa = create(GetMlModelResponseSchema, modelsAnswers.getMlModel!({ orgId: "o", modelId: MODELS.support.id } as never, now) as never);
    expect(sa.versions.map((v) => v.id).sort()).toEqual([VERSIONS.v1.id, VERSIONS.v2.id, VERSIONS.v21fp8.id].sort());
    expect(sa.model!.aliases.map((a) => [a.name, a.versionId, a.env])).toEqual(
      expect.arrayContaining([
        ["production", VERSIONS.v2.id, "prod"],
        ["staging", VERSIONS.v21fp8.id, "staging"],
      ]),
    );
    const placements = VERSION_DEFS.flatMap((v) => v.serving.map((s) => [v.id, s.revisionId, s.trafficShare]));
    expect(placements).toEqual(
      expect.arrayContaining([
        [VERSIONS.v2.id, REVISIONS.prodCurrent, 0.9],
        [VERSIONS.v21fp8.id, REVISIONS.prodFp8, 0.1],
        [VERSIONS.v21fp8.id, REVISIONS.staging, 1],
        [VERSIONS.reranker4.id, REVISIONS.reranker, 1],
      ]),
    );
    // Prod's shares add to the whole.
    const prodShare = VERSION_DEFS.flatMap((v) => v.serving).filter((s) => s.serviceId === SERVICES.prod.id).reduce((sum, s) => sum + s.trafficShare, 0);
    expect(prodShare).toBeCloseTo(1, 10);
    const prod = sa.promotion.find((p) => p.env === "prod")!;
    expect(prod).toMatchObject({ versionId: VERSIONS.v21fp8.id, canPromote: false });
    expect(prod.checks.find((c) => c.name.startsWith("safety"))!.status).toBe("fail");
  });

  it("a version page has its files, gate and perf, and the card's size is its weights", () => {
    for (const v of VERSION_DEFS) {
      const res = create(GetMlModelVersionResponseSchema, modelsAnswers.getMlModelVersion!({ orgId: "o", versionId: v.id } as never, now) as never);
      const weights = res.files.filter((f) => f.mediaType.endsWith("weight.v1.raw")).reduce((s, f) => s + Number(f.sizeBytes), 0);
      expect(Number(res.version!.card!.sizeBytes)).toBe(weights);
      expect(res.version!.gatePassed).toBeLessThanOrEqual(res.version!.gateTotal);
      for (const check of res.gate) {
        if (check.evidence?.kind === MlSubjectKind.EVAL_RUN) expect(ALL_EVAL_RUNS.some((r) => r.id === check.evidence!.id)).toBe(true);
        if (check.evidence?.kind === MlSubjectKind.REVISION) expect(Object.values(REVISIONS)).toContain(check.evidence.id);
      }
    }
    const fp8 = create(GetMlModelVersionResponseSchema, modelsAnswers.getMlModelVersion!({ orgId: "o", versionId: VERSIONS.v21fp8.id } as never, now) as never);
    expect(fp8.version!.gateStatus).toBe("fail");
    expect(Number(fp8.version!.card!.sizeBytes) / 1e9).toBeCloseTo(8.5, 1);
  });

  it("unknown ids are not answered (NotFound)", () => {
    expect(modelsAnswers.getMlModel!({ orgId: "o", modelId: "nope" } as never, now)).toBeUndefined();
    expect(modelsAnswers.getMlModelVersion!({ orgId: "o", versionId: "support-assistant/v9" } as never, now)).toBeUndefined();
    expect(evalsAnswers.getMlEvalRun!({ orgId: "o", evalRunId: "nope" } as never, now)).toBeUndefined();
  });
});

describe("on the wire", () => {
  it("every answer encodes to binary (whole-millisecond timestamps, valid ints)", () => {
    const m = (name: string) => MlEarlyAccessService.methods.find((x) => x.localName === name)!;
    const calls: [string, object][] = [
      ["listMlEvalSuites", {}],
      ["getMlEvalMatrix", { suiteId: EVAL_SUITES.gate.id }],
      ["getMlEvalMatrix", { suiteId: EVAL_SUITES.lite.id }],
      ["listMlEvalRuns", {}],
      ["getMlEvalRun", { evalRunId: EVAL_RUNS.v3ckpt }],
      ["listMlEvalSamples", { evalRunId: EVAL_RUNS.v3ckpt, taskKey: "support_tickets_v3" }],
      ["listMlModels", {}],
      ["getMlModel", { modelId: MODELS.support.id }],
      ["getMlModelVersion", { versionId: VERSIONS.v21fp8.id }],
      ["listMlEvents", { subject: { kind: MlSubjectKind.ORG, id: "" } }],
      ["listMlEvents", { subject: { kind: MlSubjectKind.EVAL_RUN, id: EVAL_RUNS.v21fp8 } }],
      ["listMlEvents", { subject: { kind: MlSubjectKind.MODEL, id: MODELS.support.id } }],
      ["getMlLineage", { subject: { kind: MlSubjectKind.MODEL, id: MODELS.support.id } }],
    ];
    for (const [name, req] of calls) {
      const method = m(name);
      // Only this file's domains: the others are tested by their own owners.
      const out = answerMl(method, create(method.input, { orgId: "o", ...req } as never), now, [evalsAnswers, modelsAnswers, lineageAnswers]);
      expect(() => toBinary(method.output, out as never), name).not.toThrow();
    }
  });
});

describe("lineage", () => {
  const ids = new Set(LINEAGE_NODES.map((n) => n.subject.id));

  it("every edge joins two nodes, ids are unique, columns are the contract's", () => {
    expect(ids.size).toBe(LINEAGE_NODES.length);
    for (const e of LINEAGE_EDGES) {
      expect(ids.has(e.from), e.from).toBe(true);
      expect(ids.has(e.to), e.to).toBe(true);
    }
    for (const n of LINEAGE_NODES) expect(n.column).toBeGreaterThanOrEqual(0);
    for (const n of LINEAGE_NODES) expect(n.column).toBeLessThanOrEqual(5);
  });

  const lineage = (kind: MlSubjectKind, id: string) => create(GetMlLineageResponseSchema, (lineageAnswers.getMlLineage!({ orgId: "o", subject: { kind, id } } as never, now) ?? {}) as never);

  it("answers every subject of the story with its connected neighbourhood", () => {
    const v2 = lineage(MlSubjectKind.MODEL_VERSION, VERSIONS.v2.id);
    const v2ids = v2.nodes.map((n) => n.subject!.id);
    expect(v2ids).toEqual(expect.arrayContaining([VERSIONS.v2.id, REVISIONS.prodCurrent, SERVICES.prod.id, phaseId(RUNS.v2.id, "sft")]));
    expect(v2ids).not.toContain(phaseId(RUNS.v3.id, "sft"));
    const run = lineage(MlSubjectKind.RUN, RUNS.v3.id);
    expect(run.nodes.map((n) => n.subject!.id)).toEqual(expect.arrayContaining([phaseId(RUNS.v3.id, "pretrain"), V3_CANDIDATE_CHECKPOINT, EVAL_RUNS.v3ckpt]));
    expect(lineage(MlSubjectKind.ATTEMPT, `${RUNS.v3.id}/sft/3`).nodes.length).toBeGreaterThan(3);
    expect(lineage(MlSubjectKind.MODEL, MODELS.support.id).nodes.map((n) => n.subject!.id)).toContain(V3_CANDIDATE_CHECKPOINT);
    expect(lineage(MlSubjectKind.INFERENCE_SERVICE, SERVICES.prod.id).nodes.map((n) => n.subject!.id)).toContain(VERSIONS.v21fp8.id);
    expect(lineage(MlSubjectKind.EVAL_RUN, periodicEvalRunId(20_000)).nodes.length).toBeGreaterThan(0);
    for (const r of [lineage(MlSubjectKind.RUN, RUNS.v3.id), lineage(MlSubjectKind.MODEL, MODELS.reranker.id)]) {
      const in_ = new Set(r.nodes.map((n) => n.subject!.id));
      for (const e of r.edges) expect(in_.has(e.from) && in_.has(e.to)).toBe(true);
    }
  });
});
