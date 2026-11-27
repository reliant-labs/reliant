// Copyright (c) 2026 Reliant Labs

/** The Overview's view-model: needs attention sorts by severity then money and leaves out telemetry gaps; live runs; waste. */

import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import {
  MlCostCategory,
  MlCostSchema,
  MlFindingCategory,
  MlFindingSchema,
  MlFindingState,
  MlRunSchema,
  MlRunState,
  MlFindingSeverity,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { activeRuns, needsAttention, productiveShare, wasteUsd } from "../overviewModel";

const finding = (id: string, severity: MlFindingSeverity, usd: number, category = MlFindingCategory.WASTE, state = MlFindingState.OPEN) =>
  create(MlFindingSchema, { id, severity, category, state, impact: { usdPerDay: usd } });

describe("the ML overview", () => {
  it("lists what needs attention: worst severity, then most money; no telemetry gaps, nothing closed", () => {
    const got = needsAttention([
      finding("cheap-critical", MlFindingSeverity.CRITICAL, 10),
      finding("rich-warning", MlFindingSeverity.WARNING, 2000),
      finding("rich-critical", MlFindingSeverity.CRITICAL, 900),
      finding("gap", MlFindingSeverity.CRITICAL, 0, MlFindingCategory.COVERAGE),
      finding("fixed", MlFindingSeverity.CRITICAL, 5000, MlFindingCategory.HEALTH, MlFindingState.FIXED),
    ]).map((f) => f.id);
    expect(got).toEqual(["rich-critical", "cheap-critical", "rich-warning"]);
  });

  it("keeps live runs, most GPUs first", () => {
    const runs = [
      create(MlRunSchema, { id: "a", state: MlRunState.SUCCEEDED, accelerators: 64 }),
      create(MlRunSchema, { id: "b", state: MlRunState.QUEUED, accelerators: 32 }),
      create(MlRunSchema, { id: "c", state: MlRunState.DEGRADED, accelerators: 64 }),
    ];
    expect(activeRuns(runs).map((r) => r.id)).toEqual(["c", "b"]);
  });

  it("reads waste and productive share from a cost breakdown, and NaN without one", () => {
    const cost = create(MlCostSchema, {
      gpuHours: 100,
      usd: 285,
      byCategory: [
        { category: MlCostCategory.PRODUCTIVE, gpuHours: 80, usd: 228 },
        { category: MlCostCategory.RESTART_LOST, gpuHours: 20, usd: 57 },
      ],
    });
    expect(wasteUsd(cost)).toBe(57);
    expect(productiveShare(cost)).toBe(0.8);
    expect(productiveShare(create(MlCostSchema, { gpuHours: 10 }))).toBeNaN();
  });
});
