// Copyright (c) 2026 Reliant Labs

/**
 * The Runs and Flaky tests pages' fixtures (Tests W3): shopfront's recorded
 * runs, one run's failures, its flaky tests and one test's record, as WIRE
 * messages (services/controlPlane/tests/wire.ts) like the rest of
 * fixtures.ts, whose `fixtureTests` answers ListTestRuns, GetTestRun,
 * ListFlakyTests and GetTest from here.
 *
 * The story matches the Overview's: main's commit 9f3c… ran unit,
 * integration and e2e in CI #4411 (integration passed only after a retry of
 * TestSyncJob); a feature branch failed one real test, one host failure (a
 * port in use) and three ClickHouse-harness failures grouped as one infra
 * event; four tests are flaky, three suspected, two quarantined (one past its
 * expiry), one fixed.
 *
 * Everything here is built inside functions: fixtures.ts imports this module
 * and this module imports fixtures.ts, so nothing may read an import while
 * the modules load.
 */

import { Code, ConnectError } from "@connectrpc/connect";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";

import type { FlakyRequest, RunRequest, RunsRequest, TestRequest } from "@/services/controlPlane/tests/types";
import {
  WireCaseStatus,
  WireFlakeSignal,
  WireFlakeState,
  WireFlakeVerdict,
  WireIngestState,
  WireRunOutcome,
  WireRunSource,
  type WireFailure,
  type WireFailureSignature,
  type WireFlake,
  type WireFlakeEvent,
  type WireListFlakyResponse,
  type WireListRunsResponse,
  type WireOutcome,
  type WireRunResponse,
  type WireRunSummary,
  type WireTestKey,
  type WireTestResponse,
  type WireTiming,
} from "@/services/controlPlane/tests/wire";

import { runMatchesFilters } from "./runFilterFixture";
import { FIXTURE_COMMIT, FIXTURE_LAST_RUN, FIXTURE_NOW, FIXTURE_RELEASES, fixtureSummaryResponse, type TestsScenario } from "./fixtures";

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;
const ts = (ms: number) => timestampFromMs(ms);

const MODULE = "github.com/acme/shopfront";

/** A test's key: `go:<import path>:<name>`, the address GetTest and `forge test quarantine add` take. */
export function fixtureTestKey(dir: string, name: string, line = 0): WireTestKey {
  const pkg = `${MODULE}/${dir}`;
  const file = line > 0 ? `${dir}/${dir.split("/").pop()}_test.go` : "";
  return { id: `go:${pkg}:${name}`, lang: "go", package: pkg, name, file, line };
}

export const FIXTURE_PR_BRANCH = "feat/refund-partial";
export const FIXTURE_PR_COMMIT = "1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d";
const PENDING_COMMIT = "b7e4d2c1a09f8e7d6c5b4a3928170f6e5d4c3b2a";
const CONFIRM_COMMIT = "c0ffee1234567890abcdef1234567890abcdef12";
export const FIXTURE_PREVIEW_ENV_ID = "env-shopfront-pr88";

/** The runs' ids, by what each is. */
export const FIXTURE_RUN_IDS = {
  pending: "01JD7R0A3C6E9G2J5L8N1Q4S7U",
  e2e: "01JD7Q4XK2M8N5P3R6T9V1W4Y7",
  integration: "01JD7Q2B9F3H6K8M1P4R7T0W3Y",
  unit: "01JD7PZ5C8E1G4J7L0N3Q6S9V2",
  previewE2e: "01JD6N7R0T3V6X9Z2B5D8F1H4K",
  branchUnit: "01JD6M3N6Q9S2U5W8Y1A4C7E0G",
  local: "01JD6K1M4P7R0T3W6Y9A2C5E8G",
  confirm: "01JD5H8K1M4P7R0T3V6X9Z2B5D",
  release: "01JCZ4F7H0K3M6P9R2T5W8Y1A4",
} as const;

/** How many runs the first page of the list holds; the rest come with "Load more". */
export const FIXTURE_RUNS_FIRST_PAGE = 6;
const SECOND_PAGE_TOKEN = "fixture-page-2";

const totals = (passed: number, failed = 0, skipped = 0, retryPassed = 0) => ({
  passed: BigInt(passed),
  failed: BigInt(failed),
  skipped: BigInt(skipped),
  retryPassed: BigInt(retryPassed),
});

/** A run `ago` ms before now that took `took` ms. */
function run(ago: number, took: number, fields: Partial<WireRunSummary> & Pick<WireRunSummary, "runId" | "lane">): WireRunSummary {
  const base = FIXTURE_LAST_RUN;
  return {
    ...base,
    startedAt: ts(FIXTURE_NOW - ago - took),
    finishedAt: ts(FIXTURE_NOW - ago),
    createdAt: ts(FIXTURE_NOW - ago),
    ...fields,
  };
}

/** The release FIXTURE_RELEASES[1] was cut from this commit (the Overview's release marker). */
function releaseCommit(): string {
  return fixtureSummaryResponse().releaseMarkers[1]!.gitCommit;
}

/** Every run recorded for shopfront, newest first. */
export function fixtureRuns(): WireRunSummary[] {
  const ci = (runId: string) => ({ provider: "github-actions", runId, runUrl: `https://github.com/acme/shopfront/actions/runs/${runId}`, attempt: 1 });
  const ids = FIXTURE_RUN_IDS;
  return [
    run(MINUTE, 0, {
      runId: ids.pending,
      lane: "unit",
      gitCommit: PENDING_COMMIT,
      ci: ci("4412"),
      outcome: WireRunOutcome.UNSPECIFIED,
      ingestState: WireIngestState.PENDING,
      totals: totals(0),
      coveragePercent: undefined,
      startedAt: undefined,
    }),
    { ...FIXTURE_LAST_RUN },
    run(14 * MINUTE, 11 * MINUTE + 40_000, { runId: ids.integration, lane: "integration", totals: totals(388, 0, 0, 1), coveragePercent: 38.0 }),
    run(17 * MINUTE, 4 * MINUTE + 12_000, { runId: ids.unit, lane: "unit", totals: totals(810, 0, 34), coveragePercent: 61.4 }),
    run(3 * HOUR, 7 * MINUTE + 5_000, {
      runId: ids.previewE2e,
      lane: "e2e",
      gitCommit: FIXTURE_PR_COMMIT,
      branch: FIXTURE_PR_BRANCH,
      baseSha: FIXTURE_COMMIT,
      source: WireRunSource.ENV,
      environmentId: FIXTURE_PREVIEW_ENV_ID,
      ci: undefined,
      totals: totals(212, 0, 5),
      coveragePercent: 51.9,
      uploadedBy: "forge-collect",
    }),
    run(4 * HOUR, 3 * MINUTE + 58_000, {
      runId: ids.branchUnit,
      lane: "unit",
      gitCommit: FIXTURE_PR_COMMIT,
      branch: FIXTURE_PR_BRANCH,
      baseSha: FIXTURE_COMMIT,
      ci: ci("4409"),
      exitCode: 1,
      outcome: WireRunOutcome.FAILED,
      totals: totals(806, 5, 34),
      coveragePercent: 61.7,
    }),
    run(5 * HOUR, 2 * MINUTE + 31_000, {
      runId: ids.local,
      lane: "unit",
      gitCommit: FIXTURE_PR_COMMIT,
      branch: FIXTURE_PR_BRANCH,
      dirty: true,
      source: WireRunSource.LOCAL,
      ci: undefined,
      exitCode: 1,
      outcome: WireRunOutcome.FAILED,
      totals: totals(809, 1, 34),
      coveragePercent: undefined,
      uploadedBy: "dana@acme.dev",
    }),
    run(DAY + 2 * HOUR, 48_000, {
      runId: ids.confirm,
      lane: "confirm",
      gitCommit: CONFIRM_COMMIT,
      source: WireRunSource.CONFIRM,
      ci: ci("4380"),
      totals: totals(3),
      coveragePercent: undefined,
    }),
    run(2 * DAY + HOUR, 4 * MINUTE + 2_000, { runId: ids.release, lane: "unit", gitCommit: releaseCommit(), ci: ci("4371"), totals: totals(804, 0, 34), coveragePercent: 61.1 }),
  ];
}

function matches(r: WireRunSummary, filters: RunsRequest["filters"]): boolean {
  return runMatchesFilters(r, filters, (version) => (version === FIXTURE_RELEASES[1] ? releaseCommit() : null));
}

/** ListTestRuns: the scenario's runs that match, in two pages when there are more than the first holds. */
export function fixtureRunsWire(scenario: TestsScenario, req: RunsRequest): WireListRunsResponse {
  const all = runsOf(scenario).filter((r) => matches(r, req.filters));
  if (req.pageToken === SECOND_PAGE_TOKEN) return { runs: all.slice(FIXTURE_RUNS_FIRST_PAGE), nextPageToken: "" };
  if (req.pageToken) throw new ConnectError("page token from another request", Code.InvalidArgument);
  return all.length > FIXTURE_RUNS_FIRST_PAGE ? { runs: all.slice(0, FIXTURE_RUNS_FIRST_PAGE), nextPageToken: SECOND_PAGE_TOKEN } : { runs: all, nextPageToken: "" };
}

function runsOf(scenario: TestsScenario): WireRunSummary[] {
  // The Overview's last run of each scenario is FIXTURE_LAST_RUN's id, so its "Open run" link finds it here.
  switch (scenario) {
    case "no-runs":
    case "not-configured":
    case "denied":
      return [];
    case "pending":
      return [{ ...fixtureRuns()[0]!, runId: FIXTURE_LAST_RUN.runId, gitCommit: FIXTURE_COMMIT }];
    case "unit-only":
      return [{ ...fixtureRuns()[3]!, runId: FIXTURE_LAST_RUN.runId, gitCommit: FIXTURE_COMMIT }];
    default:
      return fixtureRuns();
  }
}

// ── One run ─────────────────────────────────────────────────────────────────

function failure(key: WireTestKey, output: string, verdict: number, attempts = 1, signature = ""): WireFailure {
  return { test: key, output, file: key.file, line: key.line, failureSignature: signature || `sha256:${key.name.toLowerCase().replace(/[^a-z]/g, "").padEnd(12, "0")}`, attempts, verdict };
}

const REFUND_OUTPUT = `=== RUN   TestRefund/partial
    refund_test.go:64: Refund(order 42, partial) = 1250, want 1200
        remaining after refund: got 50, want 0
--- FAIL: TestRefund/partial (0.00s)`;

const PORT_OUTPUT = `=== RUN   TestWebhookDeliver
    deliver_test.go:31: start receiver: listen tcp 127.0.0.1:8089: bind: address already in use
--- FAIL: TestWebhookDeliver (0.01s)`;

const harnessOutput = (name: string) => `=== RUN   ${name}
    testsrv.go:118: clickhouse: container not ready after 30s (testsrv/clickhouse)
--- FAIL: ${name} (30.02s)`;

const SLOWEST = (): WireTiming[] => [
  { test: fixtureTestKey("internal/orders", "TestOrderExport", 40), durationMs: 30_020n },
  { test: fixtureTestKey("internal/checkout", "TestCheckoutTimeout", 77), durationMs: 12_480n },
  { test: fixtureTestKey("internal/jobs", "TestSyncJob", 88), durationMs: 2_013n },
  { test: fixtureTestKey("internal/billing", "TestInvoiceBatch", 120), durationMs: 1_870n },
  { test: fixtureTestKey("internal/search", "TestReindex", 54), durationMs: 1_102n },
];

/** GetTestRun for one of the scenario's runs; NotFound for any other id. */
export function fixtureRunWire(scenario: TestsScenario, req: RunRequest): WireRunResponse {
  const found = runsOf(scenario).find((r) => r.runId === req.runId);
  if (!found) throw new ConnectError(`no run ${req.runId}`, Code.NotFound);
  const empty: WireRunResponse = { run: found, failures: [], retryPasses: [], slowest: [], zeroEvidencePackages: [], packageCount: 0 };
  if (found.ingestState !== WireIngestState.INGESTED) return empty;
  const ids = FIXTURE_RUN_IDS;
  const packageCount = found.lane === "unit" ? 41 : found.lane === "confirm" ? 2 : 12;
  const detail: WireRunResponse = { ...empty, slowest: SLOWEST(), packageCount };
  switch (found.runId) {
    case ids.branchUnit:
      return {
        ...detail,
        failures: [
          failure(fixtureTestKey("internal/billing", "TestRefund/partial", 64), REFUND_OUTPUT, WireFlakeVerdict.REAL, 2),
          failure(fixtureTestKey("internal/webhooks", "TestWebhookDeliver", 31), PORT_OUTPUT, WireFlakeVerdict.HOST, 1, "sha256:addrinuse0001"),
          ...["TestOrderExport", "TestOrderSearch", "TestOrderArchive"].map((name) =>
            failure(fixtureTestKey("internal/orders", name, 40), harnessOutput(name), WireFlakeVerdict.CORRELATED, 1, "sha256:clickhousenr1")
          ),
        ],
        zeroEvidencePackages: ["internal/legacyexport", "cmd/migrate"],
      };
    case ids.local:
      return { ...detail, failures: [failure(fixtureTestKey("internal/billing", "TestRefund/partial", 64), REFUND_OUTPUT, WireFlakeVerdict.UNSPECIFIED)] };
    case ids.integration:
      return { ...detail, retryPasses: [fixtureTestKey("internal/jobs", "TestSyncJob", 88)] };
    case ids.confirm:
      return { ...detail, slowest: SLOWEST().slice(2, 4), retryPasses: [fixtureTestKey("internal/checkout", "TestCheckoutTimeout", 77)] };
    default:
      return detail;
  }
}

// ── Flaky tests ─────────────────────────────────────────────────────────────

interface FlakeSpec {
  dir: string;
  name: string;
  line: number;
  state: number;
  score: number;
  runs: number;
  events: number;
  signals: number[];
  lastEventAgo: number | null;
  issue?: number;
  quarantine?: { expiresIn: number; reason: string };
}

const SIG = WireFlakeSignal;
const STATE = WireFlakeState;

/** Highest flake rate first, then the most recent event: the server's order. */
const FLAKES: FlakeSpec[] = [
  { dir: "internal/jobs", name: "TestSyncJob", line: 88, state: STATE.FLAKY, score: 0.0112, runs: 412, events: 3, signals: [SIG.SAME_TREE, SIG.RETRY_PASS], lastEventAgo: 14 * MINUTE, issue: 212 },
  {
    dir: "internal/inventory",
    name: "TestInventoryReserve",
    line: 51,
    state: STATE.QUARANTINED,
    score: 0.0098,
    runs: 408,
    events: 3,
    signals: [SIG.RETRY_PASS, SIG.HISTORY],
    lastEventAgo: 9 * DAY,
    issue: 198,
    quarantine: { expiresIn: -2 * DAY, reason: "times out under -race" },
  },
  { dir: "internal/pricing", name: "TestPriceRounding", line: 140, state: STATE.FLAKY, score: 0.0071, runs: 395, events: 2, signals: [SIG.SAME_TREE], lastEventAgo: 2 * DAY, issue: 215 },
  { dir: "internal/stock", name: "TestStockSync", line: 33, state: STATE.FLAKY, score: 0.0052, runs: 410, events: 2, signals: [SIG.RETRY_PASS], lastEventAgo: 4 * DAY },
  {
    dir: "internal/auth",
    name: "TestSessionRefresh",
    line: 72,
    state: STATE.QUARANTINED,
    score: 0.0049,
    runs: 401,
    events: 2,
    signals: [SIG.CONFIRM],
    lastEventAgo: 6 * DAY,
    issue: 207,
    quarantine: { expiresIn: 30 * HOUR, reason: "races the token clock" },
  },
  { dir: "internal/orders", name: "TestOrderExport", line: 40, state: STATE.FLAKY, score: 0.0048, runs: 380, events: 2, signals: [SIG.SAME_TREE, SIG.HISTORY], lastEventAgo: 5 * DAY },
  { dir: "internal/checkout", name: "TestCheckoutTimeout", line: 77, state: STATE.SUSPECTED, score: 0.0024, runs: 418, events: 1, signals: [SIG.RETRY_PASS], lastEventAgo: 3 * HOUR, issue: 231 },
  { dir: "internal/cart", name: "TestCartMerge", line: 19, state: STATE.SUSPECTED, score: 0.0023, runs: 415, events: 1, signals: [SIG.SAME_TREE], lastEventAgo: DAY },
  { dir: "internal/webhooks", name: "TestWebhookSign/rotated key", line: 96, state: STATE.SUSPECTED, score: 0.0021, runs: 22, events: 1, signals: [SIG.CONFIRM], lastEventAgo: 2 * DAY },
  { dir: "internal/mail", name: "TestEmailDigest", line: 58, state: STATE.FIXED, score: 0, runs: 402, events: 0, signals: [SIG.HISTORY], lastEventAgo: 24 * DAY, issue: 176 },
];

function flakeOfSpec(spec: FlakeSpec): WireFlake {
  const key = fixtureTestKey(spec.dir, spec.name, spec.line);
  const quarantine = spec.quarantine
    ? {
        path: `ci/quarantine/go/${spec.dir}/${spec.name.split("/")[0]}.yaml`,
        issueUrl: spec.issue ? `https://github.com/acme/shopfront/issues/${spec.issue}` : "",
        reason: spec.quarantine.reason,
        signature: "sha256:9c1f0d2e3b4a",
        expiresAt: ts(FIXTURE_NOW + spec.quarantine.expiresIn),
        overdue: spec.quarantine.expiresIn < 0,
      }
    : undefined;
  return {
    test: key,
    state: spec.state,
    score: spec.score,
    scoreRuns: BigInt(spec.runs),
    confirmedEvents30d: spec.events,
    signals: spec.signals,
    firstSeenAt: ts(FIXTURE_NOW - 28 * DAY),
    lastEventAt: spec.lastEventAgo === null ? undefined : ts(FIXTURE_NOW - spec.lastEventAgo),
    quarantinedAt: spec.quarantine ? ts(FIXTURE_NOW - 12 * DAY) : undefined,
    quarantine,
    issueUrl: spec.issue ? `https://github.com/acme/shopfront/issues/${spec.issue}` : "",
  };
}

export function fixtureFlakes(): WireFlake[] {
  return FLAKES.map(flakeOfSpec);
}

/** ListFlakyTests: the scenario's flaky tests in the state asked for (every state but healthy when none). */
export function fixtureFlakyWire(scenario: TestsScenario, req: FlakyRequest): WireListFlakyResponse {
  if (scenario !== "report") return { tests: [], nextPageToken: "" };
  const STATES = { healthy: STATE.HEALTHY, suspected: STATE.SUSPECTED, flaky: STATE.FLAKY, quarantined: STATE.QUARANTINED, fixed: STATE.FIXED, removed: STATE.REMOVED } as const;
  const wanted = req.filters.state ? STATES[req.filters.state] : null;
  return { tests: fixtureFlakes().filter((flake) => (wanted === null ? flake.state !== STATE.HEALTHY : flake.state === wanted)), nextPageToken: "" };
}

// ── One test ────────────────────────────────────────────────────────────────

const SYNC_TIMEOUT = `=== RUN   TestSyncJob
    sync_test.go:88: job 7 did not finish within 2s: context deadline exceeded
--- FAIL: TestSyncJob (2.01s)`;

/** The last 50 outcomes of a test, newest first: passes, with a retry pass at each of `retries` and a failure at each of `fails`. */
function outcomes(retries: number[], fails: number[] = []): WireOutcome[] {
  return Array.from({ length: 50 }, (_, i) => {
    const retry = retries.includes(i);
    const fail = fails.includes(i);
    return {
      runId: i === 0 ? FIXTURE_RUN_IDS.integration : `01JDFIXTURE${String(i).padStart(15, "0")}`,
      gitCommit: i === 0 ? FIXTURE_COMMIT : `${(0xb000 + i * 131).toString(16)}d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9`.slice(0, 40),
      branch: "main",
      lane: "integration",
      status: fail ? WireCaseStatus.FAIL : WireCaseStatus.PASS,
      attempts: retry ? 2 : 1,
      retryPassed: retry,
      durationMs: BigInt(1_400 + ((i * 97) % 600)),
      at: ts(FIXTURE_NOW - 14 * MINUTE - i * 14 * HOUR),
    };
  });
}

function signatureRow(signature: string, sampleOutput: string, count: number, verdict: number, firstAgo: number, lastAgo: number): WireFailureSignature {
  return { signature, sampleOutput, count, verdict, firstSeenAt: ts(FIXTURE_NOW - firstAgo), lastSeenAt: ts(FIXTURE_NOW - lastAgo) };
}

function event(signal: number, verdict: number, gitCommit: string, ago: number, runIds: string[], failureSignature: string, infraGroup = ""): WireFlakeEvent {
  return { signal, verdict, gitCommit, runIds, failureSignature, at: ts(FIXTURE_NOW - ago), infraGroup };
}

/** TestSyncJob: three confirmed events on three commits (so flaky), plus a host failure and an infra group that never counted. */
function syncJobDetail(flake: WireFlake): WireTestResponse {
  const out = outcomes([0, 12, 25], [17, 31]);
  const commitAt = (i: number) => out[i]!.gitCommit;
  return {
    flake,
    outcomes: out,
    events: [
      event(SIG.RETRY_PASS, WireFlakeVerdict.CONFIRMED, commitAt(0), 14 * MINUTE, [out[0]!.runId], "sha256:deadline0088"),
      event(SIG.SAME_TREE, WireFlakeVerdict.CONFIRMED, commitAt(12), 7 * DAY, [out[12]!.runId, "01JD3ABCDEF0123456789XYZAB"], "sha256:deadline0088"),
      // `forge test flakes confirm` reran it and matched a host signature: never scored.
      event(SIG.CONFIRM, WireFlakeVerdict.HOST, commitAt(17), 10 * DAY, [out[17]!.runId], "sha256:addrinuse0001"),
      event(SIG.RETRY_PASS, WireFlakeVerdict.CONFIRMED, commitAt(25), 14 * DAY, [out[25]!.runId], "sha256:deadline0088"),
      event(SIG.SAME_TREE, WireFlakeVerdict.CORRELATED, commitAt(31), 18 * DAY, [out[31]!.runId], "sha256:pgtemplate01", "infra:pgtest-template-busy:7f3a"),
    ],
    signatures: [
      signatureRow("sha256:deadline0088", SYNC_TIMEOUT, 3, WireFlakeVerdict.CONFIRMED, 14 * DAY, 14 * MINUTE),
      signatureRow(
        "sha256:addrinuse0001",
        "=== RUN   TestSyncJob\n    sync_test.go:21: start worker: listen tcp 127.0.0.1:7233: bind: address already in use\n--- FAIL: TestSyncJob (0.00s)",
        1,
        WireFlakeVerdict.HOST,
        10 * DAY,
        10 * DAY
      ),
      signatureRow(
        "sha256:pgtemplate01",
        "=== RUN   TestSyncJob\n    pgtest.go:204: create database from template: source database \"tmpl_shopfront\" is being accessed by other users\n--- FAIL: TestSyncJob (5.00s)",
        1,
        WireFlakeVerdict.CORRELATED,
        18 * DAY,
        18 * DAY
      ),
    ],
  };
}

/** GetTest: a flaky test's record, a never-flaky test the runs name (state healthy), or NotFound. */
export function fixtureTestWire(scenario: TestsScenario, req: TestRequest): WireTestResponse {
  const flakes = scenario === "report" ? fixtureFlakes() : [];
  const flake = flakes.find((f) => f.test!.id === req.testId);
  if (flake?.test?.name === "TestSyncJob") return syncJobDetail(flake);
  if (flake) {
    const retries = Array.from({ length: flake.confirmedEvents30d }, (_, i) => 3 + i * 11);
    const out = outcomes(retries);
    return {
      flake,
      outcomes: out,
      events: retries.map((i) => event(flake.signals[0] ?? SIG.RETRY_PASS, WireFlakeVerdict.CONFIRMED, out[i]!.gitCommit, (i + 1) * 14 * HOUR, [out[i]!.runId], "sha256:9c1f0d2e3b4a")),
      signatures: retries.length
        ? [signatureRow("sha256:9c1f0d2e3b4a", `--- FAIL: ${flake.test!.name}\n    timed out waiting for the fake clock`, retries.length, WireFlakeVerdict.CONFIRMED, 20 * DAY, HOUR)]
        : [],
    };
  }
  const named = [...runsOf(scenario).flatMap((r) => fixtureRunDetailKeys(scenario, r.runId))].find((key) => key.id === req.testId);
  if (!named) throw new ConnectError(`no test ${req.testId}`, Code.NotFound);
  return {
    flake: { test: named, state: STATE.HEALTHY, score: 0, scoreRuns: 398n, confirmedEvents30d: 0, signals: [], issueUrl: "" },
    outcomes: outcomes([]),
    events: [],
    signatures: [],
  };
}

function fixtureRunDetailKeys(scenario: TestsScenario, runId: string): WireTestKey[] {
  const detail = fixtureRunWire(scenario, { orgId: null, project: "", runId });
  return [...detail.failures.map((f) => f.test!), ...detail.retryPasses, ...detail.slowest.map((t) => t.test!)];
}
