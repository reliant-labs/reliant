// Copyright (c) 2026 Reliant Labs

/** The Evals view-models over the fixture world's answers. */

import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { GetMlEvalMatrixResponseSchema, ListMlEvalRunsResponseSchema, ListMlEvalSuitesResponseSchema } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { evalsAnswers, LITE_V2_RUN, PERIODIC_STEPS } from "@/fixtures/shop/model/ml/evals";
import { EVAL_RUNS, EVAL_SUITES, phaseId, RUNS, V3_CANDIDATE_CHECKPOINT, VERSIONS } from "@/fixtures/shop/model/ml/world";

import {
  buildMatrix,
  candidatesEvaluated,
  cellHref,
  cellTone,
  fmtCi,
  fmtDeltaPt,
  fmtP,
  fmtSampleScore,
  fmtScore,
  newestCandidate,
  resultsVm,
  sampleFilterOf,
  scoreVsStep,
  significantRegressions,
  slicesVm,
  stepOfCheckpoint,
} from "../evalsModel";

const now = () => Date.UTC(2026, 10, 26, 15, 0);
const matrix = create(GetMlEvalMatrixResponseSchema, evalsAnswers.getMlEvalMatrix!({ suiteId: EVAL_SUITES.gate.id } as never, now) as never);
const runs = create(ListMlEvalRunsResponseSchema, evalsAnswers.listMlEvalRuns!({} as never, now) as never).evalRuns;
const suites = create(ListMlEvalSuitesResponseSchema, evalsAnswers.listMlEvalSuites!({} as never, now) as never).suites;

describe("numbers", () => {
  it("writes scores, deltas, intervals and p-values in points", () => {
    expect(fmtScore(0.70049)).toBe("70.0");
    expect(fmtDeltaPt(-0.0115)).toBe("−1.1");
    expect(fmtDeltaPt(0.008)).toBe("+0.8");
    expect(fmtDeltaPt(Number.NaN)).toBe("—");
    expect(fmtCi(0.0126)).toBe("±2.5");
    expect(fmtP(0.0004)).toBe("p < 0.001");
    expect(fmtP(0.031)).toBe("p = 0.03");
    expect(fmtSampleScore(1, false)).toBe("✓");
    expect(fmtSampleScore(4, true)).toBe("4 / 5");
  });

  it("colours a cell by significance, never by the raw delta", () => {
    expect(cellTone({ significant: false, regression: true })).toBe("neutral");
    expect(cellTone({ significant: true, regression: true })).toBe("danger");
    expect(cellTone({ significant: true, regression: false })).toBe("success");
  });
});

describe("the matrix", () => {
  it("lays out rows × tasks and filters columns by category", () => {
    const vm = buildMatrix(matrix);
    expect(vm.columns).toHaveLength(8);
    expect(vm.baseline?.id).toBe(VERSIONS.v2.id);
    expect(vm.rows.find((r) => r.isBaseline)?.row.id).toBe(VERSIONS.v2.id);
    expect(vm.rows.every((r) => r.cells.size === 8)).toBe(true);
    expect(vm.categories).toEqual(expect.arrayContaining(["math", "safety", "internal", "knowledge"]));
    expect(buildMatrix(matrix, "math").columns.map((c) => c.key)).toEqual(["gsm8k"]);
  });

  it("opens a cell's drill on its eval run against the baseline's", () => {
    const cand = matrix.rows.find((r) => r.id === V3_CANDIDATE_CHECKPOINT)!;
    const href = cellHref(cand, "gsm8k", EVAL_RUNS.v2);
    expect(href).toContain(`/evals/runs/${EVAL_RUNS.v3ckpt}`);
    expect(href).toContain("task=gsm8k");
    expect(href).toContain(`baseline=${EVAL_RUNS.v2}`);
    expect(cellHref(matrix.rows[0], "gsm8k", matrix.rows[0].evalRunId)).not.toContain("baseline=");
  });

  it("finds the newest candidate and the significant regressions", () => {
    expect(newestCandidate(matrix.rows)?.id).toBe(V3_CANDIDATE_CHECKPOINT);
    const regs = significantRegressions(matrix);
    expect(regs.some((c) => c.rowId === V3_CANDIDATE_CHECKPOINT && c.taskKey === "gsm8k")).toBe(true);
    expect(regs.some((c) => c.rowId === VERSIONS.v21fp8.id && c.taskKey === "safety_refusals")).toBe(true);
    expect(regs.every((c) => c.rowId !== matrix.baselineRowId)).toBe(true);
    expect(candidatesEvaluated(matrix)).toBe(3);
  });
});

describe("score vs step", () => {
  it("draws eval-periodic's checkpoints on a step axis against production", () => {
    const lite = suites.find((s) => s.id === EVAL_SUITES.lite.id)!;
    const ref = runs.find((r) => r.id === LITE_V2_RUN);
    const panels = scoreVsStep(runs, lite, phaseId(RUNS.v3.id, "eval-periodic"), ref);
    expect(panels).toHaveLength(lite.tasks.length);
    const gsm = panels.find((p) => p.task.key === "gsm8k")!;
    expect(gsm.series[0].x).toEqual([...PERIODIC_STEPS]);
    expect(gsm.series[1].y.every((y) => y === gsm.reference)).toBe(true);
    expect(gsm.latest).toBeCloseTo(gsm.series[0].y.at(-1)!, 10);
    expect(stepOfCheckpoint(`${RUNS.v3.id}/sft/40000`)).toBe(40000);
    expect(stepOfCheckpoint("support-assistant/v2")).toBeNull();
  });
});

describe("an eval run", () => {
  it("pairs each result with the baseline's and the matrix cell", () => {
    const run = runs.find((r) => r.id === EVAL_RUNS.v3ckpt)!;
    const base = runs.find((r) => r.id === EVAL_RUNS.v2)!;
    const suite = suites.find((s) => s.id === run.suiteId);
    const vm = resultsVm(run, suite, base, matrix.cells.filter((c) => c.rowId === V3_CANDIDATE_CHECKPOINT));
    const gsm = vm.find((r) => r.key === "gsm8k")!;
    expect(gsm.delta).toBeCloseTo(gsm.value - gsm.baseline!, 10);
    expect(gsm.cell?.gateFail).toBe(true);
    expect(gsm.task?.name).toBe("GSM8K");
    const slices = slicesVm(run, base, "gsm8k");
    expect(slices.map((s) => s.value)).toEqual(["1-3", "4-6", "7+"]);
    // v3's GSM8K loss is in the long problems.
    expect(slices.find((s) => s.value === "7+")!.delta!).toBeLessThan(slices.find((s) => s.value === "1-3")!.delta!);
  });

  it("reads an unknown samples filter as all", () => {
    expect(sampleFilterOf("regressed")).toBe("regressed");
    expect(sampleFilterOf("bogus")).toBe("all");
    expect(sampleFilterOf(undefined)).toBe("all");
  });
});
