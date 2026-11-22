// Copyright (c) 2026 Reliant Labs

/**
 * The Runs and Flaky tests pages' pure parts: what the URL may hold and how
 * it becomes a request, and the words a flake is shown in — a flake rate is a
 * ranking with its n, never a verdict, and host and correlated failures never
 * count.
 */

import { describe, expect, it } from "vitest";

import { forgeConsoleTestsFlakySearchSchema, forgeConsoleTestsRunsSearchSchema } from "@/routeSchemas";
import type { TestOutcome } from "@/services/controlPlane/tests/types";

import { flakeRateWords, formatFlakeRate, issueLabel, outcomeStrip, pickFlakySearch, quarantineCommand, suggestsQuarantine } from "../flakyModel";
import { activeRunsFilterCount, failurePlace, failureVerdictWords, pickRunsSearch, runDurationMs, runsFiltersOf, totalsWords } from "../runsModel";

describe("the runs list's URL", () => {
  it("keeps every filter that fits and drops the rest", () => {
    expect(pickRunsSearch({ environment: "pr-88", branch: "main", commit: "9f3c2a1", release: "v1.9", lane: "unit", source: "ci", status: "failed", env: "prod" })).toEqual({
      environment: "pr-88",
      branch: "main",
      commit: "9f3c2a1",
      release: "v1.9",
      lane: "unit",
      source: "ci",
      status: "failed",
    });
    expect(pickRunsSearch({ source: "jenkins", status: "green", branch: "  ", commit: "x".repeat(65) })).toEqual({});
    // The router parses a numeric-looking value as a number.
    expect(pickRunsSearch({ commit: 1234567 })).toEqual({ commit: "1234567" });
  });

  it("is what the route's schema holds, so a sent link opens the same list", () => {
    const parsed = forgeConsoleTestsRunsSearchSchema.parse({ env: "prod", environment: "pr-88", branch: "main", source: "local", status: "undetermined", lane: "e2e", bogus: "1" });
    // The console's scope and the page's own environment are two keys.
    expect(parsed).toEqual({ env: "prod", environment: "pr-88", branch: "main", source: "local", status: "undetermined", lane: "e2e" });
    expect(forgeConsoleTestsRunsSearchSchema.parse({ source: "jenkins", status: 3 })).toEqual({});
  });

  it("maps onto ListTestRuns' optional fields, the chosen environment's id among them", () => {
    expect(runsFiltersOf(undefined, {})).toEqual({});
    expect(runsFiltersOf("env-1", { branch: "main", commit: "9f3c", release: "v1", lane: "unit", source: "env", status: "passed" })).toEqual({
      environmentId: "env-1",
      branch: "main",
      gitCommit: "9f3c",
      releaseVersion: "v1",
      lane: "unit",
      source: "env",
      outcome: "passed",
    });
    expect(activeRunsFilterCount({ branch: "main", status: "failed" })).toBe(2);
    expect(activeRunsFilterCount({ environment: "pr-88" })).toBe(1);
  });
});

describe("a run in words", () => {
  it("names the counts that are not zero, and no duration without both ends", () => {
    expect(totalsWords({ passed: 1412, failed: 0, skipped: 0 })).toBe("1,412 passed");
    expect(totalsWords({ passed: 806, failed: 5, skipped: 34 })).toBe("806 passed · 5 failed · 34 skipped");
    const run = { startedMs: 1_000, finishedMs: 61_000 } as Parameters<typeof runDurationMs>[0];
    expect(runDurationMs(run)).toBe(60_000);
    expect(runDurationMs({ ...run, startedMs: null })).toBeNull();
  });

  it("says a host or correlated failure never counts, and only a confirmed one does", () => {
    expect(failureVerdictWords("confirmed")).toMatchObject({ counted: true });
    for (const verdict of ["host", "correlated", "real", "raced", "inconclusive", "unattributed"] as const) expect(failureVerdictWords(verdict)?.counted).toBe(false);
    expect(failureVerdictWords("host")?.words).toBe("Host failure: not counted");
    expect(failureVerdictWords("correlated")?.words).toBe("Infra group: not counted");
    expect(failureVerdictWords("unknown")).toBeNull();
  });

  it("places a failure at the output's file and line, else the test's", () => {
    const test = { id: "go:x:TestA", lang: "go", package: "x", name: "TestA", file: "a_test.go", line: 3 };
    expect(failurePlace({ test, output: "", file: "b_test.go", line: 9, failureSignature: "", attempts: 1, verdict: "real", infraGroup: "" })).toBe("b_test.go:9");
    expect(failurePlace({ test, output: "", file: "", line: 0, failureSignature: "", attempts: 1, verdict: "real", infraGroup: "" })).toBe("a_test.go:3");
    expect(failurePlace({ test: { ...test, file: "", line: 0 }, output: "", file: "", line: 0, failureSignature: "", attempts: 1, verdict: "real", infraGroup: "" })).toBe("");
  });
});

describe("the flaky list's URL", () => {
  it("holds one state, and only the states the page offers", () => {
    expect(pickFlakySearch({ state: "suspected" })).toEqual({ state: "suspected" });
    expect(pickFlakySearch({ state: "healthy" })).toEqual({});
    expect(forgeConsoleTestsFlakySearchSchema.parse({ env: "all", state: "quarantined" })).toEqual({ env: "all", state: "quarantined" });
    expect(forgeConsoleTestsFlakySearchSchema.parse({ state: "broad" })).toEqual({});
  });
});

describe("a flake in words", () => {
  it("shows the flake rate with its n, small rates still ranked, and none over no runs", () => {
    expect(formatFlakeRate(0.0112)).toBe("1.1%");
    expect(formatFlakeRate(0.0023)).toBe("0.23%");
    expect(formatFlakeRate(0)).toBe("0%");
    expect(flakeRateWords({ score: 0.0112, scoreRuns: 412 })).toBe("1.1% (412 runs)");
    expect(flakeRateWords({ score: 0.5, scoreRuns: 1 })).toBe("50% (1 run)");
    expect(flakeRateWords({ score: 0.3, scoreRuns: 0 })).toBe("— (no runs yet)");
  });

  it("suggests a quarantine for a suspected or flaky test without one, quoting a subtest's space", () => {
    const test = { id: "go:github.com/acme/shop/internal/x:TestA/rotated key", lang: "go", package: "", name: "TestA/rotated key", file: "", line: 0 };
    expect(quarantineCommand({ test, issueUrl: "" })).toBe("reliant forge test quarantine add 'go:github.com/acme/shop/internal/x:TestA/rotated key'");
    expect(quarantineCommand({ test: { ...test, id: "go:x:TestB" }, issueUrl: "https://github.com/acme/shop/issues/12" })).toBe(
      "reliant forge test quarantine add go:x:TestB --issue https://github.com/acme/shop/issues/12"
    );
    expect(suggestsQuarantine({ state: "suspected", quarantine: null })).toBe(true);
    expect(suggestsQuarantine({ state: "flaky", quarantine: null })).toBe(true);
    expect(suggestsQuarantine({ state: "quarantined", quarantine: { path: "p", issueUrl: "", reason: "", signature: "", expiresMs: null, overdue: false } })).toBe(false);
    expect(suggestsQuarantine({ state: "fixed", quarantine: null })).toBe(false);
    expect(issueLabel("https://github.com/acme/shop/issues/212")).toBe("#212");
    expect(issueLabel("https://linear.app/acme/issue/SHOP-1")).toBe("Issue");
  });

  it("draws the outcome strip oldest first, a retry pass as its own kind", () => {
    const outcome = (status: TestOutcome["status"], retryPassed = false, runId: string = status): TestOutcome => ({
      runId,
      gitCommit: "",
      branch: "main",
      lane: "unit",
      status,
      attempts: retryPassed ? 2 : 1,
      retryPassed,
      durationMs: 0,
      atMs: null,
    });
    const strip = outcomeStrip([outcome("pass", true, "newest"), outcome("fail"), outcome("skip"), outcome("pass", false, "oldest")]);
    expect(strip.cells.map((cell) => cell.runId)).toEqual(["oldest", "skip", "fail", "newest"]);
    expect(strip.cells.at(-1)!.kind).toBe("retry");
    expect(strip.counts).toEqual({ pass: 1, retry: 1, fail: 1, skip: 1, unknown: 0 });
  });
});
