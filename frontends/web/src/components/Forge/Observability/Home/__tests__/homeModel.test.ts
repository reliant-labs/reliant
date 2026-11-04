// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import { alertService, firingCounts, ratioSeries } from "../homeModel";

describe("the health strip's figures", () => {
  it("derives the error rate and the firing count per bucket", () => {
    expect(ratioSeries([1, null, 3], [10, 0, 30])).toEqual([0.1, null, 0.1]);
    expect(firingCounts([0, 60, 120], [{ startSec: 50, endSec: 130, label: "a" }, { startSec: 125, endSec: 200, label: "b" }])).toEqual([1, 1, 2]);
  });

  it("names an alert's service by the first label the rule packs use", () => {
    expect(alertService({ labels: { service_name: "api", job: "x" } })).toBe("api");
    expect(alertService({ labels: {} })).toBeUndefined();
  });
});
