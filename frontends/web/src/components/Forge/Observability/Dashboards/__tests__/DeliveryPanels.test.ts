// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { ReleaseHealth } from "@/services/controlPlane/observability/deliveryTypes";

import { formatSpan, formatThroughput, laneOrder, placeLabels, scorecardOf } from "../DeliveryPanels";

const stats = (requests: number, errorRate: number, p95Ms: number) => ({ requests, errorRate, p50Ms: p95Ms / 3, p95Ms, p99Ms: p95Ms * 2 });

function health(over: Partial<ReleaseHealth> = {}): ReleaseHealth {
  return {
    promotion: { promotionId: "p", version: "v2", gitCommit: "abc", promotedAtMs: 0, previousVersion: "v1" },
    before: { startMs: -15 * 60_000, endMs: 0 },
    after: { startMs: 0, endMs: 15 * 60_000 },
    afterComplete: true,
    services: [
      { service: "api", before: stats(300, 0, 120), after: stats(300, 0.05, 900), versionShare: 1 },
      { service: "worker", before: stats(900, 0, 40), after: stats(900, 0, 40), versionShare: 1 },
    ],
    newIssues: [],
    regressedIssues: [],
    firingAlerts: [],
    workloadFindings: [],
    verdict: "fail",
    findings: [{ code: "error_rate", service: "api", value: 0.05, threshold: 0.01, policy: "fail", message: "api: error rate 0% -> 5%" }],
    thresholds: [],
    notEvaluated: [],
    ...over,
  };
}

describe("scorecardOf", () => {
  it("shows the service a failing finding names, so the figures and the finding agree", () => {
    const card = scorecardOf(health());
    expect(card.service).toBe("api");
    expect(card.before?.errorRate).toBe(0);
    expect(card.after?.errorRate).toBe(0.05);
    expect(card.after?.p95Ms).toBe(900);
    expect(card.after?.perMinute).toBe(20);
  });

  it("otherwise shows the busiest service after the promotion", () => {
    expect(scorecardOf(health({ findings: [], verdict: "pass" })).service).toBe("worker");
  });

  it("has no side when the server sent no window", () => {
    expect(scorecardOf(health({ before: null })).before).toBeNull();
  });
});

describe("the promotion lanes", () => {
  it("run dev, staging, prod, then the rest by name", () => {
    expect(laneOrder([{ name: "prod" }, { name: "qa" }, { name: "dev" }, { name: "staging" }]).map((e) => e.name)).toEqual(["dev", "staging", "qa", "prod"]);
  });

  it("label the newest mark, and only older marks whose label overlaps none kept", () => {
    // A 300px lane, 64px labels: the marks at 10 and 30 collide, so only the newer is named;
    // the newest, at the end of the lane, is named on its left.
    expect(placeLabels([10, 30, 150, 298], 300)).toEqual([null, 39, 159, 225]);
  });

  it("name a rollback before an older promotion it collides with", () => {
    expect(placeLabels([100, 120, 280], 300, 64, [false, true, false])).toEqual([null, 129, 207]);
  });

  it("keep a label at the right edge inside the lane", () => {
    expect(placeLabels([290], 300)).toEqual([217]);
  });
});

describe("formats", () => {
  it("never reads a quiet service as zero", () => {
    expect(formatThroughput(12.4)).toBe("12/min");
    expect(formatThroughput(0.2)).toBe("12/h");
    expect(formatThroughput(0.001)).toBe("<1/h");
  });

  it("say a delivery span aloud", () => {
    expect(formatSpan(4.8 * 3_600_000)).toBe("4.8h");
    expect(formatSpan(3 * 86_400_000)).toBe("3d");
  });
});
