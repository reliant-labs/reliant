// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import { bucketLabel, dashboardDefaultRange, DEFAULT_RANGE, presetCovering, rangeForBucket, spanMs } from "../window";

describe("spec durations", () => {
  it("reads forge's span form and refuses everything else", () => {
    expect(spanMs("90s")).toBe(90_000);
    expect(spanMs("15m")).toBe(900_000);
    expect(spanMs("6h")).toBe(21_600_000);
    expect(spanMs("7d")).toBe(604_800_000);
    for (const bad of [undefined, "", "7", "d", "0d", "-1d", "1.5d", "1w", "7 d"]) expect(spanMs(bad)).toBeUndefined();
  });

  it("rounds a duration up to the nearest preset, to the longest when none reaches", () => {
    expect(presetCovering(1)).toBe("15m");
    expect(presetCovering(spanMs("1h")!)).toBe("1h");
    expect(presetCovering(spanMs("2h")!)).toBe("6h");
    expect(presetCovering(spanMs("1d")!)).toBe("24h");
    expect(presetCovering(spanMs("3d")!)).toBe("7d");
    expect(presetCovering(spanMs("30d")!)).toBe("7d");
  });
});

describe("a dashboard's default range", () => {
  it("is the console's hour when the spec declares none, and the declared range otherwise", () => {
    expect(dashboardDefaultRange(undefined)).toBe(DEFAULT_RANGE);
    expect(dashboardDefaultRange("")).toBe(DEFAULT_RANGE);
    expect(dashboardDefaultRange("garbage")).toBe(DEFAULT_RANGE);
    expect(dashboardDefaultRange("7d")).toBe("7d");
    expect(dashboardDefaultRange("24h")).toBe("24h");
    expect(dashboardDefaultRange("30d")).toBe("7d");
  });
});

describe("a window narrower than a panel's bucket", () => {
  it("is widened to the smallest preset holding two buckets, and says it was", () => {
    expect(rangeForBucket("1h", "1d")).toEqual({ range: "7d", widened: true });
    // 24h is not smaller than a 1-day bucket, and spans two dates unless it starts at midnight: left alone.
    expect(rangeForBucket("24h", "1d")).toEqual({ range: "24h", widened: false });
  });

  it("leaves a window that holds a bucket alone, and a panel with no fixed bucket", () => {
    expect(rangeForBucket("7d", "1d")).toEqual({ range: "7d", widened: false });
    expect(rangeForBucket("1h", undefined)).toEqual({ range: "1h", widened: false });
    expect(rangeForBucket("1h", "6h")).toEqual({ range: "24h", widened: true });
    expect(rangeForBucket("6h", "1h")).toEqual({ range: "6h", widened: false });
  });

  it("names the bucket", () => {
    expect(bucketLabel("1d")).toBe("1-day");
    expect(bucketLabel("6h")).toBe("6-hour");
    expect(bucketLabel(undefined)).toBe("");
  });
});
