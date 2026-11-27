// Copyright (c) 2026 Reliant Labs

/** The ML kit's pure parts: number formats (not collected is "—", never 0), links for every subject kind, series alignment. */

import "@/components/Forge/Ml/__tests__/jsdomShims";
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { MlAxis, MlCollection, MlSeriesSchema, MlSubjectKind } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { ML_STEP_KEY } from "@/services/controlPlane/ml/telemetry";

import { alignSeries, ticks } from "../charts";
import { COMPARE_MAX, toggled } from "../compareTray";
import { fmtBytes, fmtCompact, fmtDuration, fmtGpuHours, fmtMs, fmtPct, fmtStep, fmtUnit, fmtUsd, noNegativeZero } from "../format";
import { mlHref, searchString, subjectHref } from "../links";
import { onStepAxis, xAxisOf } from "../telemetry";
import { alignRuns } from "../../Training/compare/compareModel";

describe("ML number formats", () => {
  it("never write a missing value as zero", () => {
    for (const f of [fmtPct, fmtUsd, fmtGpuHours, fmtMs, fmtDuration]) expect(f(Number.NaN)).toBe("—");
    expect(fmtUnit(Number.NaN, "ratio")).toBe("—");
  });

  it("read as a person reads them", () => {
    expect(fmtCompact(41_200)).toBe("41.2k");
    expect(fmtCompact(2_700_000_000)).toBe("2.7B");
    expect(fmtStep(41_200)).toBe("41.2k");
    expect(fmtStep(6_400)).toBe("6,400");
    expect(fmtUsd(5_100)).toBe("$5.1k");
    expect(fmtUsd(0.41)).toBe("$0.41");
    expect(fmtGpuHours(1204.3)).toBe("1,204 GPU-h");
    expect(fmtPct(0.834)).toBe("83%");
    expect(fmtBytes(212 * 1024 ** 3)).toBe("212 GiB");
    expect(fmtDuration(22_200)).toBe("6h 10m");
    expect(fmtMs(4100)).toBe("4.1 s");
  });

  it("never write a sign on a value that rounds to zero", () => {
    expect(fmtUnit(-0.04, "GB/s")).toBe("0.0 GB/s");
    expect(noNegativeZero("-0 W")).toBe("0 W");
    expect(noNegativeZero("-0.5 GB/s")).toBe("-0.5 GB/s");
    expect(noNegativeZero("-0.05%")).toBe("-0.05%");
  });
});

describe("ML links", () => {
  it("open every subject kind's page", () => {
    expect(subjectHref({ kind: MlSubjectKind.RUN, id: "run_1", label: "llama-8b-support-v3" })).toBe("/forge/ml/runs/llama-8b-support-v3");
    expect(subjectHref({ kind: MlSubjectKind.PHASE, id: "run_1/sft" })).toBe("/forge/ml/runs/run_1/phases/sft");
    expect(subjectHref({ kind: MlSubjectKind.ATTEMPT, id: "run_1/sft/3" })).toBe("/forge/ml/runs/run_1/phases/sft?attempt=3");
    expect(subjectHref({ kind: MlSubjectKind.RANK, id: "run_1/sft/3/37" })).toBe("/forge/ml/runs/run_1/phases/sft?attempt=3&rank=37&tab=system");
    expect(subjectHref({ kind: MlSubjectKind.NODE, id: "gke-a/node-1" })).toBe("/forge/ml/fleet/gke-a/node-1");
    expect(subjectHref({ kind: MlSubjectKind.DEVICE, id: "GPU-1" })).toBe("/forge/ml/fleet/gpus/GPU-1");
    expect(subjectHref({ kind: MlSubjectKind.MODEL_VERSION, id: "support-assistant/v2" })).toBe("/forge/ml/models/support-assistant/v2");
    expect(subjectHref({ kind: MlSubjectKind.FINDING, id: "f-1" })).toBe("/forge/ml/recommendations?sel=f-1");
    expect(subjectHref({ kind: MlSubjectKind.DATASET, id: "ds" })).toBeNull();
    expect(subjectHref({ kind: MlSubjectKind.MODEL, id: "meta-llama/Llama-3.1-8B" })).toBe("https://huggingface.co/meta-llama/Llama-3.1-8B");
  });

  it("encode segments and drop empty search values", () => {
    expect(mlHref("models", ["support-assistant", "v2.1-fp8"])).toBe("/forge/ml/models/support-assistant/v2.1-fp8");
    expect(mlHref("inference", ["svc/x"])).toBe("/forge/ml/inference/svc%2Fx");
    expect(searchString({ tab: "metrics", sel: "", attempt: undefined })).toBe("?tab=metrics");
  });
});

describe("ML charts", () => {
  it("align series on the union of their x values, a gap where one has none", () => {
    const a = create(MlSeriesSchema, { key: "a", x: [1, 2, 3], y: [10, 20, 30] });
    const b = create(MlSeriesSchema, { key: "b", x: [2, 4], y: [5, Number.NaN] });
    expect(alignSeries([a, b])).toEqual({ xs: [1, 2, 3, 4], ys: [[10, 20, 30, null], [null, 5, null, null]] });
  });

  it("space ticks evenly", () => {
    expect(ticks(0, 100, 5)).toEqual([0, 25, 50, 75, 100]);
  });
});

describe("the compare tray", () => {
  const run = (n: number) => ({ id: `run_${n}`, name: `r${n}` });
  it("adds at the end, removes on a second toggle, and holds at most four", () => {
    let tray = toggled([], run(1));
    tray = toggled(tray, run(2));
    expect(tray.map((r) => r.id)).toEqual(["run_1", "run_2"]);
    expect(toggled(tray, run(1)).map((r) => r.id)).toEqual(["run_2"]);
    for (let i = 3; i <= 6; i++) tray = toggled(tray, run(i));
    expect(tray).toHaveLength(COMPARE_MAX);
    expect(tray.map((r) => r.id)).not.toContain("run_5");
  });
});

describe("the step axis", () => {
  const series = (key: string, group: string, x: number[], y: number[]) => create(MlSeriesSchema, { key, group, x, y, collection: MlCollection.COLLECTED, axis: MlAxis.TIME });

  it("re-plots each series against its own group's steps, and drops the step series", () => {
    const out = onStepAxis([
      series("train/loss", "attempt 1", [100, 150, 200], [3, 2, 1]),
      series("train/loss", "attempt 2", [300, 400], [1.5, 1.2]),
      series(ML_STEP_KEY, "attempt 1", [100, 200], [0, 1000]),
      // Attempt 2 resumed at step 600: its points land on steps attempt 1 already took.
      series(ML_STEP_KEY, "attempt 2", [300, 400], [600, 800]),
    ])!;
    expect(out.map((s) => [s.group, s.axis, s.x])).toEqual([
      ["attempt 1", MlAxis.STEP, [0, 500, 1000]],
      ["attempt 2", MlAxis.STEP, [600, 800]],
    ]);
  });

  it("drops points outside the steps reported, and is null when nothing reports the step", () => {
    const out = onStepAxis([series("train/loss", "", [50, 150], [2, 1]), series(ML_STEP_KEY, "", [100, 200], [10, 20])])!;
    expect(out[0].x).toEqual([15]);
    expect(out[0].y).toEqual([1]);
    expect(onStepAxis([series("train/loss", "", [1, 2], [2, 1])])).toBeNull();
  });

  it("reads ?axis as step unless it says time", () => {
    expect([xAxisOf(undefined), xAxisOf("step"), xAxisOf("time"), xAxisOf("bogus")]).toEqual(["step", "step", "time", "step"]);
  });

  it("compares runs by step when every run reports it, else by time since each started", () => {
    const a = [series("train/loss", "a", [1000, 1100], [2, 1]), series(ML_STEP_KEY, "a", [1000, 1100], [0, 100])];
    const b = [series("train/loss", "b", [5000, 5050], [2, 1.5]), series(ML_STEP_KEY, "b", [5000, 5050], [0, 50])];
    const stepped = alignRuns(new Map([["a", a], ["b", b]]));
    expect(stepped.byStep).toBe(true);
    expect(stepped.series.get("b")!.map((s) => s.x)).toEqual([[0, 50]]);
    const mixed = alignRuns(new Map([["a", a], ["c", [series("train/loss", "c", [9000, 9030], [2, 1])]]]));
    expect(mixed.byStep).toBe(false);
    expect(mixed.series.get("a")!.map((s) => [s.key, s.x])).toEqual([["train/loss", [0, 100]]]);
    expect(mixed.series.get("c")![0].x).toEqual([0, 30]);
    // A run that collected nothing has no say in the axis.
    const empty = create(MlSeriesSchema, { key: "train/loss", collection: MlCollection.NOT_COLLECTED });
    const withEmpty = alignRuns(new Map([["a", a], ["d", [empty]]]));
    expect(withEmpty.byStep).toBe(true);
    expect(withEmpty.series.get("d")!.map((s) => s.collection)).toEqual([MlCollection.NOT_COLLECTED]);
  });
});
