// Copyright (c) 2026 Reliant Labs

/** The observability tabs' and the run comparison's view models. */

import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import {
  GetMlRunResponseSchema,
  MlCollection,
  MlRankSchema,
  MlSeriesSchema,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { compareRow, configDiff, delta, deltaTone, mergeAttempts } from "../../compare/compareModel";
import {
  ema,
  filterSections,
  METRIC_SECTIONS,
  rankHeatmap,
  seriesForTile,
  stragglersOf,
  workspaceKeys,
} from "../obsModel";

const series = (key: string, x: number[], y: number[], extra: Record<string, unknown> = {}) => create(MlSeriesSchema, { key, x, y, collection: MlCollection.COLLECTED, ...extra });

describe("metrics workspace", () => {
  it("reads every section's keys, system ones apart", () => {
    const { train, system } = workspaceKeys();
    expect(train).toContain("train/loss");
    expect(train).toContain("data/tokens_by_source/*");
    expect(system.every((k) => k.startsWith("system/"))).toBe(true);
  });

  it("filters tiles by key or title and drops empty sections", () => {
    const out = filterSections(METRIC_SECTIONS, "mfu");
    expect(out.map((s) => s.id)).toEqual(["throughput"]);
    expect(out[0].tiles.map((t) => t.title)).toEqual(["MFU"]);
    expect(filterSections(METRIC_SECTIONS, "timers/")[0].tiles.length).toBe(7);
    expect(filterSections(METRIC_SECTIONS, "")).toHaveLength(METRIC_SECTIONS.length);
  });

  it("matches families by prefix", () => {
    const all = [series("data/tokens_by_source/web", [1], [0.3]), series("data/tokens_by_source/code", [1], [0.1]), series("data/tokens_seen", [1], [5])];
    expect(seriesForTile(all, { title: "Mix", keys: ["data/tokens_by_source/*"] })).toHaveLength(2);
  });

  it("smooths with a debiased EMA and keeps gaps", () => {
    expect(ema([1, 1, 1], 0.9)).toEqual([1, 1, 1].map(() => expect.closeTo(1, 9)));
    const out = ema([0, 10, NaN, 10], 0.5);
    expect(Number.isNaN(out[2])).toBe(true);
    expect(out[1]).toBeGreaterThan(5);
    expect(ema([3, 4], 0)).toEqual([3, 4]);
  });

});

describe("system", () => {
  it("builds rank × time rows from rank-grouped series", () => {
    const s = [series("system/sm_active", [1, 2], [0.7, NaN], { group: "rank 1" }), series("system/sm_active", [1, 2], [0.8, 0.8], { group: "rank 0" })];
    const { rows, xs } = rankHeatmap(s);
    expect(xs).toEqual([1, 2]);
    expect(rows.map((r) => r.rank)).toEqual([0, 1]);
    expect(rows[1].cells).toEqual([0.7, null]);
  });

  it("finds the straggler", () => {
    const ranks = [create(MlRankSchema, { rank: 0, stepTimeMsP50: 10 }), create(MlRankSchema, { rank: 37, stepTimeMsP50: 10, computeMsP50: 9, straggler: true })];
    expect(stragglersOf(ranks).map((r) => r.rank)).toEqual([37]);
  });
});

describe("run comparison", () => {
  const run = (id: string, params: Record<string, string>, valLoss: number) =>
    create(GetMlRunResponseSchema, { run: { id, name: id, objectiveKey: "val/loss", summary: { "val/loss": valLoss, mfu: 0.32 }, params, goodput: 0.8, restarts: 2, tokensSeen: 10n, cost: { gpuHours: 100, usd: 285, priced: true } }, checkpoints: [{ step: 2000n, metrics: { "val/loss": valLoss - 0.01 }, evalScores: { gsm8k: 0.7 } }] });

  it("summarises a run and its best checkpoint", () => {
    const r = compareRow(run("a", {}, 1.9));
    expect(r.bestValLoss).toBeCloseTo(1.89);
    expect(r.bestValStep).toBe(2000);
    expect(r.evals.gsm8k).toBe(0.7);
    expect(Number.isNaN(r.evals.mmlu_pro)).toBe(true);
  });

  it("lists only the config keys that differ", () => {
    const rows = configDiff([run("a", { lr: "2e-5", seq: "8192" }, 1.9), run("b", { lr: "3e-5", seq: "8192" }, 1.8)]);
    expect(rows).toEqual([{ key: "lr", values: ["2e-5", "3e-5"] }]);
  });

  it("colours a delta by whether it is better", () => {
    expect(deltaTone("loss", delta(1.8, 1.9))).toBe("success");
    expect(deltaTone("higher", delta(0.7, 0.8))).toBe("danger");
    expect(deltaTone("loss", delta(NaN, 1))).toBeUndefined();
  });

  it("merges a run's attempts into one line, later attempts winning", () => {
    const m = mergeAttempts([series("train/loss", [0, 100, 200], [3, 2, 1], { attempt: 1 }), series("train/loss", [150, 250], [9, 8], { attempt: 2 })], "train/loss", "v3")!;
    expect(m.x).toEqual([0, 100, 150, 250]);
    expect(m.y).toEqual([3, 2, 9, 8]);
    expect(m.group).toBe("v3");
  });
});
