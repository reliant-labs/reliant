// Copyright (c) 2026 Reliant Labs

/**
 * TestsService's wire mapping (wire.ts): the responses into plain types, with
 * the rules that keep the pages honest — no rate over zero runs, no verdict
 * before ingest, no delta against a missing base, unknown enums never green —
 * and the requests, whose filters are sent only when set.
 */

import { describe, expect, it } from "vitest";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";

import { FIXTURE_COMMIT, FIXTURE_NOW, FIXTURE_RELEASES, fixtureSummaryResponse, fixtureSummaryWire } from "@/components/Forge/Tests/fixtures";

import {
  coverageRequest,
  evidenceStatusOf,
  fileCoverageOf,
  flakeOf,
  flakyRequest,
  releaseTestsOf,
  runOf,
  runsRequest,
  summaryOf,
  summaryRequest,
  WireFlakeState,
  WireIngestState,
  WireRunOutcome,
  type WireRunSummary,
} from "../wire";

describe("summaryOf", () => {
  it("maps a full summary", () => {
    const summary = summaryOf(fixtureSummaryResponse());
    expect(summary.branch).toBe("main");
    expect(summary.latest?.gitCommit).toBe(FIXTURE_COMMIT);
    expect(summary.latest?.lanes).toEqual(["unit", "integration", "e2e"]);
    expect(summary.latest?.totals).toEqual({ passed: 1412, failed: 0, skipped: 37, retryPassed: 1 });
    expect(summary.latest?.coverage?.combined).toEqual({ statements: 48_210, covered: Math.round(48_210 * 0.742), percent: 74.2 });
    expect(summary.latest?.coverage?.lanes.e2e?.percent).toBe(52.7);
    expect(summary.latest?.coverage?.deltaPercent).toBe(0.6);
    expect(summary.latest?.coverage?.excludedGeneratedStatements).toBe(9_120);
    expect(summary.latest?.updatedMs).toBe(FIXTURE_NOW - 12 * 60_000);
    expect(summary.trend).toHaveLength(30);
    expect(summary.trend.at(-1)).toMatchObject({ gitCommit: FIXTURE_COMMIT, combinedPercent: 74.2, lanesPercent: { unit: 61.4, integration: 38, e2e: 52.7 } });
    expect(summary.releaseMarkers.map((m) => m.releaseVersion)).toEqual([...FIXTURE_RELEASES]);
    expect(summary.passRate).toBe(0.987);
    expect(summary.passRateRuns).toBe(50);
    expect([summary.flaky, summary.quarantined, summary.suspected, summary.hostFailures30d]).toEqual([4, 2, 3, 5]);
    expect(summary.lastRun).toMatchObject({ lane: "e2e", source: "ci", outcome: "passed", ingestState: "ingested", coveragePercent: 52.7 });
    expect(summary.lastRun?.ci).toEqual({ provider: "github-actions", runId: "4411", runUrl: "https://github.com/acme/shopfront/actions/runs/4411", attempt: 1 });
    expect(summary.lowCoverage[0]).toMatchObject({ path: "internal/billing", deltaPercent: -8 });
    expect(summary.lowCoverage[1]!.deltaPercent).toBeNull();
  });

  it("says nothing when nothing was recorded: no rate over zero runs, no report, no run", () => {
    const summary = summaryOf(fixtureSummaryWire("no-runs"));
    expect(summary.passRate).toBeNull();
    expect(summary.latest).toBeNull();
    expect(summary.lastRun).toBeNull();
    expect(summary.trend).toEqual([]);
  });

  it("keeps no delta against a base without a report", () => {
    const summary = summaryOf(fixtureSummaryWire("unit-only"));
    expect(summary.latest?.coverage?.baseHasReport).toBe(false);
    expect(summary.latest?.coverage?.deltaPercent).toBeNull();
    expect(summary.latest?.coverage?.onlyE2e).toBeNull();
  });
});

describe("runOf", () => {
  const base = fixtureSummaryResponse().lastRun!;
  const run = (over: Partial<WireRunSummary>) => runOf({ ...base, ...over });

  it("has no verdict before ingest finishes, whatever the outcome field says", () => {
    expect(run({ ingestState: WireIngestState.PENDING, outcome: WireRunOutcome.PASSED })).toMatchObject({ outcome: "undetermined", ingestState: "pending" });
    expect(run({ ingestState: WireIngestState.FAILED, outcome: WireRunOutcome.PASSED })).toMatchObject({ outcome: "undetermined", ingestState: "failed" });
  });

  it("reads an outcome or ingest state it does not know as not green and not in", () => {
    expect(run({ outcome: 99 }).outcome).toBe("undetermined");
    expect(run({ ingestState: 99 })).toMatchObject({ ingestState: "pending", outcome: "undetermined" });
    expect(run({ source: 99 }).source).toBe("unknown");
  });

  it("reads the zero timestamp as unset, and an absent coverage as null", () => {
    expect(run({ startedAt: timestampFromMs(0), coveragePercent: undefined })).toMatchObject({ startedMs: null, coveragePercent: null });
  });
});

describe("the other reads", () => {
  it("maps a flake's state and signals, unknown ones to unknown", () => {
    const flake = flakeOf({ state: WireFlakeState.QUARANTINED, score: 0.11, scoreRuns: 48n, confirmedEvents30d: 3, signals: [1, 2, 42], issueUrl: "" });
    expect(flake).toMatchObject({ state: "quarantined", scoreRuns: 48, signals: ["same-tree", "retry-pass", "unknown"], quarantine: null });
    expect(flakeOf({ state: 42, score: 0, scoreRuns: 0n, confirmedEvents30d: 0, signals: [], issueUrl: "" }).state).toBe("unknown");
  });

  it("drops a report beside unavailable evidence, and grades unknown evidence as unavailable", () => {
    expect(evidenceStatusOf(42)).toBe("unavailable");
    const tests = releaseTestsOf({
      releaseId: "r",
      releaseVersion: "v1",
      gitCommit: "c",
      status: 3,
      reason: "no run for this commit",
      report: { gitCommit: "c", treeHash: "", lanes: [], runCount: 0, reportDigest: "" },
      previousReleaseVersion: "",
      newFailures: [],
      newFlakes: [],
    });
    expect(tests).toMatchObject({ status: "unavailable", report: null, coverageDeltaPercent: null });
  });

  it("maps a file's hits per lane to numbers", () => {
    const file = fileCoverageOf({
      gitCommit: "c",
      path: "internal/billing/refund.go",
      importPath: "",
      blobId: "b",
      lang: "go",
      granularity: "statement",
      lanes: ["unit"],
      lines: [{ line: 43, statements: 1, hits: { unit: 12n, e2e: 0n }, partial: false, changed: true }],
    });
    expect(file.lines[0]).toEqual({ line: 43, statements: 1, hits: { unit: 12, e2e: 0 }, partial: false, changed: true });
  });
});

describe("requests", () => {
  it("sends only the filters that are set", () => {
    expect(summaryRequest({ orgId: null, project: "shop" })).toEqual({ project: "shop", trendPoints: 0, passRateRuns: 0 });
    expect(summaryRequest({ orgId: "org-1", project: "shop", branch: "main", environmentId: "" })).toEqual({
      project: "shop",
      trendPoints: 0,
      passRateRuns: 0,
      branch: "main",
      orgId: "org-1",
    });
    expect(runsRequest({ orgId: null, project: "shop", filters: { source: "env", outcome: "failed", lane: "e2e" }, pageSize: 50 })).toEqual({
      project: "shop",
      pageSize: 50,
      pageToken: "",
      lane: "e2e",
      source: 3,
      outcome: 2,
    });
    expect(flakyRequest({ orgId: null, project: "shop", filters: { state: "flaky", minScore: 0 } })).toEqual({
      project: "shop",
      pageSize: 0,
      pageToken: "",
      state: 3,
      minScore: 0,
    });
  });

  it("writes the coverage target as the proto oneof", () => {
    expect(coverageRequest({ orgId: null, project: "shop", target: { releaseVersion: "v1.9" }, path: "" }).target).toEqual({ case: "releaseVersion", value: "v1.9" });
    expect(coverageRequest({ orgId: null, project: "shop", target: { gitCommit: "9f3c" }, path: "internal", lane: "unit", changedOnly: true })).toEqual({
      project: "shop",
      target: { case: "gitCommit", value: "9f3c" },
      path: "internal",
      onlyE2e: false,
      changedOnly: true,
      lane: "unit",
    });
  });
});
