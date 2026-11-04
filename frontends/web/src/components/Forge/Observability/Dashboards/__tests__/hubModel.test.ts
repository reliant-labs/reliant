// Copyright (c) 2026 Reliant Labs

import { describe, expect, it } from "vitest";

import type { Promotion } from "@/services/controlPlane/observability/deliveryTypes";
import type { WorkloadJob } from "@/services/controlPlane/observability/types";

import { failingJobs, issueCountLabel, recentPromotions } from "../hubModel";

const job = (envName: string, workload: string, result: WorkloadJob["result"], finishedAtMs: number): WorkloadJob => ({
  envId: `env-${envName}`,
  envName,
  name: `${workload}-${finishedAtMs}`,
  workload,
  result,
  reason: result === "failed" ? "BackoffLimitExceeded" : "",
  releaseVersion: "v1",
  startedAtMs: finishedAtMs - 10,
  finishedAtMs,
  durationSeconds: 10,
});

describe("failingJobs", () => {
  // Newest first, as ListWorkloads answers.
  const jobs = [
    job("prod", "reindex", "failed", 900),
    job("prod", "report", "succeeded", 800),
    job("staging", "reindex", "failed", 700),
    job("prod", "reindex", "succeeded", 600),
    job("prod", "report", "failed", 500),
  ];

  it("is the jobs whose latest run failed, one row per job and environment", () => {
    expect(failingJobs(jobs, "all").map((j) => `${j.envName}/${j.workload}`)).toEqual(["prod/reindex", "staging/reindex"]);
  });

  it("narrows to the environment in scope", () => {
    expect(failingJobs(jobs, "prod").map((j) => j.workload)).toEqual(["reindex"]);
    expect(failingJobs(jobs, "dev")).toEqual([]);
  });
});

describe("recentPromotions", () => {
  it("puts the newest first and keeps five", () => {
    const promotions = Array.from({ length: 7 }, (_, i): Promotion => ({
      promotionId: `p${i}`,
      envId: "env-prod",
      envName: "prod",
      releaseId: `r${i}`,
      version: `v1.${i}`,
      gitCommit: "",
      gitTag: "",
      promotedAtMs: i * 1000,
      promotedBy: "ada",
      promotedByUserId: "u",
      kind: "promote",
    }));
    expect(recentPromotions(promotions).map((p) => p.version)).toEqual(["v1.6", "v1.5", "v1.4", "v1.3", "v1.2"]);
  });
});

describe("issueCountLabel", () => {
  it("says when the first page is not all of them", () => {
    expect(issueCountLabel(3, false)).toBe("3");
    expect(issueCountLabel(50, true)).toBe("50+");
  });
});
