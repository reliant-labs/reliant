// Copyright (c) 2026 Reliant Labs

/**
 * The Tests pages' fixtures, shaped like control-plane's TestsService read API
 * (control-plane#3286's proto/services/tests/v1/tests.proto) as WIRE messages
 * (services/controlPlane/tests/wire.ts), served on the wire, so the pages map
 * them through the same wire.ts they map control-plane's answers with: a
 * fixture can never say something the wire cannot. ONE module, shared by the page tests and the preview (the
 * visual harness reads the shop pack's Tests story instead, src/fixtures/shop).
 *
 * `fixtureTests(scenario)` is an in-memory TestsService; a test serves it on
 * a mock transport (__tests__/testsTestUtils.tsx). The project is
 * shopfront's: main's newest report merges the unit, integration and e2e
 * lanes of commit 9f3c…, two releases sit on the trend, four tests are flaky
 * (two quarantined).
 *
 * GetTestSummary is answered here (the Overview's one read, W1); ListTestRuns,
 * GetTestRun, ListFlakyTests and GetTest from runFlakeFixtures.ts (the Runs
 * and Flaky tests pages, W3); GetCoverage and GetFileCoverage from
 * coverageFixtures.ts (the Coverage page, W2); GetReleaseTests from
 * releaseTestsFixtures.ts (the release Tests tab and the Releases column, W4).
 */

import { Code, ConnectError } from "@connectrpc/connect";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";

import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { GetTestSummaryResponseSchema } from "@/gen/controlplane/services/tests/v1/tests_pb";
import type {
  GetCoverageRequest,
  GetFileCoverageRequest,
  GetReleaseTestsRequest,
  GetTestRequest,
  GetTestRunRequest,
  GetTestSummaryRequest,
  ListFlakyTestsRequest,
  ListTestRunsRequest,
} from "@/gen/controlplane/services/tests/v1/tests_pb";
import type {
  CoverageRequest,
  CoverageTarget,
  FileCoverageRequest,
  FlakyRequest,
  ReleaseTestsRequest,
  RunRequest,
  RunsRequest,
  SummaryRequest,
  TestRequest,
} from "@/services/controlPlane/tests/types";
import {
  FLAKE_STATE_WIRE,
  RUN_OUTCOME_WIRE,
  RUN_SOURCE_WIRE,
  WireIngestState,
  WireRunOutcome,
  WireRunSource,
  type WireCommitReport,
  type WireCoveragePoint,
  type WireRunSummary,
  type WireStatementCoverage,
  type WireSummaryResponse,
} from "@/services/controlPlane/tests/wire";

import { fixtureFlakyWire, fixtureRunsWire, fixtureRunWire, fixtureTestWire } from "./runFlakeFixtures";
import { fixtureCoverageResponse, fixtureFileCoverageResponse } from "./coverageFixtures";
import { FIXTURE_RELEASE_COMMITS, FIXTURE_RELEASE_VERSIONS } from "./fixtureReleases";
import { fixtureReleaseTestsWire } from "./releaseTestsFixtures";

/**
 * A fixed "now", so relative times do not move: the visual harness's and the
 * preview's frozen clock, FIXED_NOW in e2e/visual/clock.ts (2026-11-18
 * 15:30 UTC). src cannot import e2e/, so it is repeated here and
 * __tests__/testsEnvScope.test.tsx holds the two equal. Every Tests fixture time
 * (this file's, runFlakeFixtures.ts's) is relative to it, so nothing lands
 * in the preview's future.
 */
export const FIXTURE_NOW = Date.UTC(2026, 10, 18, 15, 30, 0);
const MINUTE = 60_000;
const DAY = 86_400_000;

export const FIXTURE_PROJECT = "shopfront";
/** main's newest commit: the one staging's release was cut from (fixtureReleases.ts). */
export const FIXTURE_COMMIT = FIXTURE_RELEASE_COMMITS.staging;
export const FIXTURE_RELEASES = [FIXTURE_RELEASE_VERSIONS.first, FIXTURE_RELEASE_VERSIONS.prod] as const;

const ts = (ms: number) => timestampFromMs(ms);

/** Statement coverage of `statements` at `percent`. */
function stmts(statements: number, percent: number): WireStatementCoverage {
  return { statements: BigInt(statements), covered: BigInt(Math.round((statements * percent) / 100)), percent };
}

const STATEMENTS = 48_210;
const LANES = { unit: 61.4, integration: 38.0, e2e: 52.7 } as const;

/** main's newest report: the three lanes of 9f3c…, merged. */
export const FIXTURE_REPORT: WireCommitReport = {
  gitCommit: FIXTURE_COMMIT,
  treeHash: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
  lanes: ["unit", "integration", "e2e"],
  runCount: 6,
  totals: { passed: 1412n, failed: 0n, skipped: 37n, retryPassed: 1n },
  coverage: {
    granularity: "statement",
    combined: stmts(STATEMENTS, 74.2),
    lanes: { unit: stmts(STATEMENTS, LANES.unit), integration: stmts(STATEMENTS, LANES.integration), e2e: stmts(STATEMENTS, LANES.e2e) },
    onlyE2e: stmts(STATEMENTS, 3.1),
    languages: [],
    deltaPercent: 0.6,
    baseHasReport: true,
    excludedGeneratedStatements: 9_120n,
  },
  reportDigest: "sha256:5e1d0c3b2a19f8e7d6c5b4a39281706f5e4d3c2b1a0f9e8d7c6b5a4938271605",
  updatedAt: ts(FIXTURE_NOW - 12 * MINUTE),
};

/** The run that finished last: main's e2e lane in CI. */
export const FIXTURE_LAST_RUN: WireRunSummary = {
  runId: "01JD7Q4XK2M8N5P3R6T9V1W4Y7",
  project: FIXTURE_PROJECT,
  gitCommit: FIXTURE_COMMIT,
  treeHash: FIXTURE_REPORT.treeHash,
  dirty: false,
  branch: "main",
  baseSha: "",
  lane: "e2e",
  source: WireRunSource.CI,
  environmentId: "",
  ci: { provider: "github-actions", runId: "4411", runUrl: "https://github.com/acme/shopfront/actions/runs/4411", attempt: 1 },
  startedAt: ts(FIXTURE_NOW - 21 * MINUTE),
  finishedAt: ts(FIXTURE_NOW - 12 * MINUTE),
  exitCode: 0,
  outcome: WireRunOutcome.PASSED,
  ingestState: WireIngestState.INGESTED,
  ingestError: "",
  totals: { passed: 214n, failed: 0n, skipped: 3n, retryPassed: 0n },
  coveragePercent: LANES.e2e,
  forgeVersion: "v1.9.0",
  uploadedBy: "ci-shopfront",
  createdAt: ts(FIXTURE_NOW - 12 * MINUTE),
};

/** The trend commits the releases were cut from (by index): the first release on day 16, prod's on day 2. */
const TREND_RELEASE_COMMITS: Record<number, string> = { 13: FIXTURE_RELEASE_COMMITS.first, 27: FIXTURE_RELEASE_COMMITS.prod };

/** 30 commits over 30 days: a dip after the first release, recovered by the second. */
function trend(): WireCoveragePoint[] {
  return Array.from({ length: 30 }, (_, i) => {
    const day = 29 - i;
    const dip = day <= 16 && day >= 10 ? -9 + Math.abs(day - 13) : 0;
    const combined = Math.round((70.1 + i * 0.14 + dip + (i === 29 ? 0.6 : 0)) * 10) / 10;
    const at = FIXTURE_NOW - day * DAY - (i === 29 ? 12 * MINUTE : 3 * 60 * MINUTE);
    const lane = (base: number, swing: number) => Math.round((base - (29 - i) * 0.05 + dip * swing) * 10) / 10;
    return {
      gitCommit: TREND_RELEASE_COMMITS[i] ?? (i === 29 ? FIXTURE_COMMIT : `${(0xa000 + i * 97).toString(16)}e0c1d2a3b4c5d6e7f8091a2b3c4d5e6f708192`.slice(0, 40)),
      at: ts(at),
      combinedPercent: i === 29 ? 74.2 : combined,
      lanesPercent: i === 29 ? { ...LANES } : { unit: lane(LANES.unit, 0.8), integration: lane(LANES.integration, 0.4), e2e: lane(LANES.e2e, 0.6) },
    };
  });
}

/** GetTestSummary for main, with every part filled in. */
export function fixtureSummaryResponse(): WireSummaryResponse {
  const points = trend();
  return {
    branch: "main",
    latest: FIXTURE_REPORT,
    coverageTrend: points,
    releaseMarkers: [
      { releaseVersion: FIXTURE_RELEASES[0], gitCommit: points[13]!.gitCommit, cutAt: ts(FIXTURE_NOW - 16 * DAY) },
      { releaseVersion: FIXTURE_RELEASES[1], gitCommit: points[27]!.gitCommit, cutAt: ts(FIXTURE_NOW - 2 * DAY) },
    ],
    passRate: 0.987,
    passRateRuns: 50,
    suspectedCount: 3,
    flakyCount: 4,
    quarantinedCount: 2,
    lastRun: FIXTURE_LAST_RUN,
    lowCoveragePackages: [
      { path: "internal/billing", lang: "go", combined: stmts(2_310, 58.0), lanes: { unit: stmts(2_310, 51.2) }, deltaPercent: -8.0 },
      { path: "internal/webhooks", lang: "go", combined: stmts(1_140, 61.0), lanes: { unit: stmts(1_140, 55.4) } },
    ],
    hostFailures30d: 5,
  };
}

/**
 * What the Overview can be shown with.
 *
 *   report          main's report, trend, flakes and last run (the default)
 *   no-runs         the service answers, and nothing was ever recorded
 *   pending         one run recorded, its ingest not finished: no figures yet
 *   unit-only       one lane merged so far, and no trend before it
 *   no-flakes       the report's runs, and no test has ever flaked (nor any host failure)
 *   acts-on-suspected  the report, from a project whose flake policy acts at
 *                   `suspected` (forge.yaml tests.flakes.issues / quarantine: pr,
 *                   as forge, control-plane and reliant do): the nav badge and
 *                   Needs attention count suspected tests too
 *   not-configured  a control plane without TestsService (Unimplemented)
 *   denied          no tests:read
 *
 * The Runs and Flaky tests pages read the same scenarios (runFlakeFixtures.ts):
 * the report has nine runs over two pages, a failed branch run and ten flaky
 * tests; pending and unit-only have their one run; no-runs has none.
 */
export type TestsScenario = "report" | "no-runs" | "pending" | "unit-only" | "no-flakes" | "acts-on-suspected" | "not-configured" | "denied";
export const TESTS_SCENARIOS: readonly TestsScenario[] = ["report", "no-runs", "pending", "unit-only", "no-flakes", "acts-on-suspected", "not-configured", "denied"];

/** The scenario every read but the summary answers from: a policy is the summary's alone. */
const dataScenario = (scenario: TestsScenario): TestsScenario => (scenario === "acts-on-suspected" ? "report" : scenario);

export function fixtureSummaryWire(scenario: TestsScenario): WireSummaryResponse {
  const empty: WireSummaryResponse = {
    branch: "main",
    coverageTrend: [],
    releaseMarkers: [],
    passRate: 0,
    passRateRuns: 0,
    suspectedCount: 0,
    flakyCount: 0,
    quarantinedCount: 0,
    lowCoveragePackages: [],
    hostFailures30d: 0,
  };
  switch (scenario) {
    case "no-runs":
      return empty;
    case "pending":
      return {
        ...empty,
        lastRun: {
          ...FIXTURE_LAST_RUN,
          lane: "unit",
          finishedAt: ts(FIXTURE_NOW - MINUTE),
          createdAt: ts(FIXTURE_NOW - MINUTE),
          outcome: WireRunOutcome.UNSPECIFIED,
          ingestState: WireIngestState.PENDING,
          totals: { passed: 0n, failed: 0n, skipped: 0n, retryPassed: 0n },
          coveragePercent: undefined,
        },
      };
    case "unit-only": {
      const unit = stmts(STATEMENTS, LANES.unit);
      const report: WireCommitReport = {
        ...FIXTURE_REPORT,
        lanes: ["unit"],
        runCount: 2,
        totals: { passed: 1104n, failed: 0n, skipped: 31n, retryPassed: 0n },
        coverage: { ...FIXTURE_REPORT.coverage!, combined: unit, lanes: { unit }, onlyE2e: undefined, deltaPercent: undefined, baseHasReport: false },
      };
      return {
        ...empty,
        latest: report,
        coverageTrend: [{ gitCommit: FIXTURE_COMMIT, at: ts(FIXTURE_NOW - 12 * MINUTE), combinedPercent: LANES.unit, lanesPercent: { unit: LANES.unit } }],
        passRate: 1,
        passRateRuns: 2,
        lastRun: { ...FIXTURE_LAST_RUN, lane: "unit", coveragePercent: LANES.unit, totals: report.totals },
      };
    }
    case "no-flakes":
      return { ...fixtureSummaryResponse(), suspectedCount: 0, flakyCount: 0, quarantinedCount: 0, hostFailures30d: 0 };
    case "acts-on-suspected":
      return { ...fixtureSummaryResponse(), actsOnSuspected: true };
    default:
      return fixtureSummaryResponse();
  }
}

/** The reads the fixture answers: TestsService's methods the Tests pages call, by their generated names. */
export type FixtureTestsMethod = "getTestSummary" | "listTestRuns" | "getTestRun" | "getCoverage" | "getFileCoverage" | "getReleaseTests" | "listFlakyTests" | "getTest";

/** One call the fixture answered, for tests that check what was asked: the request as the page built it (types.ts). */
export interface FixtureCall {
  method: FixtureTestsMethod;
  req: unknown;
}

/** A fixture read: the request message in, the response (a wire shape the transport makes a message of) out, or a ConnectError thrown. */
export type FixtureTestsHandler = (req: never) => unknown;

/**
 * An in-memory TestsService on the wire: `handlers` answers each read the
 * Tests pages make (a test serves them on a mock transport, in a
 * TransportScope: __tests__/testsTestUtils.tsx), and `calls` records each
 * request, read back into the plain request the page built. A test may
 * replace a handler.
 */
export interface FixtureTests {
  calls: FixtureCall[];
  handlers: Record<FixtureTestsMethod, FixtureTestsHandler>;
}

// ── Wire → the plain requests (wire.ts's request builders, inverted) ─────────

const inverse = <K extends string>(map: Record<K, number>) => Object.fromEntries(Object.entries(map).map(([name, wire]) => [wire, name])) as Record<number, K>;
const RUN_SOURCE = inverse(RUN_SOURCE_WIRE);
const RUN_OUTCOME = inverse(RUN_OUTCOME_WIRE);
const FLAKE_STATE = inverse(FLAKE_STATE_WIRE);

const base = (req: { project: string; orgId?: string }) => ({ orgId: req.orgId ?? null, project: req.project });
const opt = <T>(value: T | undefined): T | undefined => (value === "" || value === 0 ? undefined : value);

function targetOf(target: GetCoverageRequest["target"]): CoverageTarget {
  switch (target.case) {
    case "gitCommit":
      return { gitCommit: target.value };
    case "releaseVersion":
      return { releaseVersion: target.value };
    case "environmentId":
      return { environmentId: target.value };
    default:
      throw new ConnectError("a coverage target is required", Code.InvalidArgument);
  }
}

const summaryRequestOf = (req: GetTestSummaryRequest): SummaryRequest => ({
  ...base(req),
  branch: req.branch,
  environmentId: req.environmentId,
  trendPoints: opt(req.trendPoints),
  passRateRuns: opt(req.passRateRuns),
});

const runsRequestOf = (req: ListTestRunsRequest): RunsRequest => ({
  ...base(req),
  filters: {
    branch: req.branch,
    gitCommit: req.gitCommit,
    releaseVersion: req.releaseVersion,
    environmentId: req.environmentId,
    lane: req.lane,
    source: req.source === undefined ? undefined : (RUN_SOURCE[req.source] as RunsRequest["filters"]["source"]),
    outcome: req.outcome === undefined ? undefined : RUN_OUTCOME[req.outcome],
  },
  pageSize: req.pageSize,
  pageToken: req.pageToken,
});

const coverageRequestOf = (req: GetCoverageRequest): CoverageRequest => ({
  ...base(req),
  target: targetOf(req.target),
  path: req.path,
  lane: req.lane,
  baseSha: req.baseSha,
  changedOnly: req.changedOnly || undefined,
});

const flakyRequestOf = (req: ListFlakyTestsRequest): FlakyRequest => ({
  ...base(req),
  filters: { state: req.state === undefined ? undefined : (FLAKE_STATE[req.state] as FlakyRequest["filters"]["state"]), minScore: req.minScore, package: req.package },
  pageSize: req.pageSize,
  pageToken: req.pageToken,
});

/**
 * The summary as the response message. `actsOnSuspected` is not in the proto
 * yet (wire.ts), so a transport would drop it: it is set on the message
 * itself, as the field will arrive once it lands, and the transport passes a
 * message through unchanged.
 */
function summaryMessage(wire: WireSummaryResponse) {
  const { actsOnSuspected, ...fields } = wire;
  const message = create(GetTestSummaryResponseSchema, fields as MessageInitShape<typeof GetTestSummaryResponseSchema>);
  return actsOnSuspected === undefined ? message : Object.assign(message, { actsOnSuspected });
}

export function fixtureTests(scenario: TestsScenario = "report"): FixtureTests {
  const calls: FixtureCall[] = [];
  const data = dataScenario(scenario);
  // A read the scenario refuses, or the fixture's answer (a NotFound or a bad page token it throws).
  function answer<R, P, T>(method: FixtureTestsMethod, plain: (req: R) => P, build: (req: P) => T): (req: R) => T {
    return (wire) => {
      const req = plain(wire);
      calls.push({ method, req });
      if (scenario === "denied") throw new ConnectError("missing tests:read", Code.PermissionDenied);
      if (scenario === "not-configured") throw new ConnectError("TestsService is not available", Code.Unimplemented);
      return build(req);
    };
  }
  const noReport = (req: CoverageRequest | FileCoverageRequest) => {
    if (data === "no-runs" || data === "pending") throw new ConnectError("no report for this commit", Code.NotFound);
    return req;
  };
  return {
    calls,
    handlers: {
      getTestSummary: answer("getTestSummary", summaryRequestOf, () => summaryMessage(fixtureSummaryWire(scenario))),
      listTestRuns: answer("listTestRuns", runsRequestOf, (req) => fixtureRunsWire(data, req)),
      getTestRun: answer("getTestRun", (req: GetTestRunRequest): RunRequest => ({ ...base(req), runId: req.runId }), (req) => fixtureRunWire(data, req)),
      getCoverage: answer("getCoverage", coverageRequestOf, (req) => fixtureCoverageResponse(noReport(req) as CoverageRequest)),
      getFileCoverage: answer(
        "getFileCoverage",
        (req: GetFileCoverageRequest): FileCoverageRequest => ({ ...base(req), target: targetOf(req.target), path: req.path, baseSha: req.baseSha }),
        (req) => fixtureFileCoverageResponse(noReport(req) as FileCoverageRequest)
      ),
      getReleaseTests: answer("getReleaseTests", (req: GetReleaseTestsRequest): ReleaseTestsRequest => ({ ...base(req), version: req.version }), (req) => fixtureReleaseTestsWire(data, req)),
      listFlakyTests: answer("listFlakyTests", flakyRequestOf, (req) => fixtureFlakyWire(data, req)),
      getTest: answer("getTest", (req: GetTestRequest): TestRequest => ({ ...base(req), testId: req.testId }), (req) => fixtureTestWire(data, req)),
    },
  };
}
