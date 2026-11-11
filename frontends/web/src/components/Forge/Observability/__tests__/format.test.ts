// Copyright (c) 2026 Reliant Labs

import { describe, expect, it, vi } from "vitest";

import { panelBucketSeconds } from "../Dashboards/SeriesChart";
import { formatFigure } from "../format";

vi.mock("uplot", async () => (await import("./obsMocks")).obsMocks.uplot());

describe("formatFigure", () => {
  it("draws an exact count in full, digits grouped", () => {
    expect(formatFigure(1_284, "count", true)).toBe("1,284");
    expect(formatFigure(1_284_003, "", true)).toBe("1,284,003");
    expect(formatFigure(2.5, "count", true)).toBe("2.5");
  });

  it("abbreviates a count that is not exact", () => {
    expect(formatFigure(1_284, "count")).toBe("1.3k");
  });

  it("keeps the units that carry their own scale as formatValue draws them", () => {
    expect(formatFigure(0.123, "percent", true)).toBe("12%");
    expect(formatFigure(1_500, "ms", true)).toBe("1.5s");
    expect(formatFigure(null, "count", true)).toBe("—");
  });
});

describe("panelBucketSeconds", () => {
  it("is the panel's minStep when it declares one, whatever the server inferred", () => {
    expect(panelBucketSeconds({ minStep: "7d" }, { bucketSeconds: 86_400 })).toBe(604_800);
    expect(panelBucketSeconds({ minStep: "6h" }, {})).toBe(21_600);
  });

  it("is the server's bucket otherwise", () => {
    expect(panelBucketSeconds({}, { bucketSeconds: 86_400 })).toBe(86_400);
    expect(panelBucketSeconds({ minStep: "weekly" }, { bucketSeconds: 3_600 })).toBe(3_600);
  });
});
