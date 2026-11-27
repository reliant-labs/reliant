// Copyright (c) 2026 Reliant Labs

/**
 * The plain types the Tests pages (components/Forge/Tests) speak: a forge
 * project's test record — runs, coverage and flaky tests per commit, branch,
 * release and environment — as control-plane's `controlplane.v1.TestsService`
 * reports it (control-plane proto/services/tests/v1/tests.proto, whose header
 * is the behaviour spec; design: forge docs/proposals/tests-coverage-flakes.md
 * §2–3). Times are epoch ms (null when the server sent none), enums are names,
 * int64 counts are numbers, and nothing here knows the wire: wire.ts maps it.
 *
 * NO SOURCE CODE. Coverage is paths, positions, hit counts and each file's git
 * blob id; a viewer fetches source from the linked repository at view time.
 *
 * UNKNOWN IS NEVER GREEN. A run whose ingest has not finished is "undetermined",
 * a figure with no lanes behind it is absent (null), and a pass rate over zero
 * runs is null, never 100%.
 */

/** Where a run was produced. */
export type TestRunSource = "ci" | "local" | "env" | "confirm" | "unknown";

/** A run's verdict, as forge's `test report` exit codes define it. Coverage never decides it. */
export type TestRunOutcome = "passed" | "failed" | "undetermined";

/** How far the ingest worker has processed a run: its counts and coverage fill in at "ingested". */
export type TestIngestState = "pending" | "ingested" | "failed";

export type TestCaseStatus = "pass" | "fail" | "skip" | "unknown";

/**
 * A test's flake state (forge pkg/testreport).
 *
 *   healthy      no confirmed event in the window
 *   suspected    one confirmed event: the actionable state (delete on first flake)
 *   flaky        2+ confirmed events on distinct commits in 30 days
 *   quarantined  a quarantine entry exists on the default branch
 *   fixed        was flaky; its file changed and 14 quiet days passed
 *   removed      was flaky or quarantined, and no longer runs
 */
export type TestFlakeState = "healthy" | "suspected" | "flaky" | "quarantined" | "fixed" | "removed" | "unknown";

/** What detected a flake event. */
export type TestFlakeSignal = "same-tree" | "retry-pass" | "confirm" | "history" | "unknown";

/** How one failure was classified. Only "confirmed" scores toward a flake state; "host" and "correlated" never do. */
export type TestFlakeVerdict = "confirmed" | "real" | "raced" | "inconclusive" | "unattributed" | "host" | "correlated" | "unknown";

/** How complete a release's test evidence is. */
export type TestEvidenceStatus = "ok" | "partial" | "unavailable";

export type CoverageNodeKind = "package" | "directory" | "file" | "unknown";

/** One test: (language, package, name). `id` is the GetTest address ("go:<pkg>:TestRefund/partial"). */
export interface TestKey {
  id: string;
  lang: string;
  package: string;
  name: string;
  /** Repo-relative, when known; "" otherwise. */
  file: string;
  /** 0 when unknown. */
  line: number;
}

export interface TestTotals {
  passed: number;
  failed: number;
  skipped: number;
  /** Failed at least once, then passed on a retry within the run. */
  retryPassed: number;
}

/** Covered over instrumented statements (a line counts as one with "line" granularity). */
export interface StatementCoverage {
  statements: number;
  covered: number;
  /** 0–100; 0 with no statements. */
  percent: number;
}

/** Coverage of the executable lines changed between a base and the commit. */
export interface DiffCoverage {
  baseSha: string;
  lines: number;
  covered: number;
  percent: number;
}

/** One language's share. Languages are kept apart: a Go statement and a TypeScript one are not the same unit. */
export interface LanguageCoverage {
  lang: string;
  combined: StatementCoverage | null;
  lanes: Record<string, StatementCoverage>;
}

/** The coverage of a commit report, or of a path in one. Generated code is left out of every figure (its count is beside them). */
export interface Coverage {
  /** "statement" or "line" (only LCOV or Cobertura input). */
  granularity: string;
  /** A statement counts as covered if any lane covered it. Null when the report has no coverage. */
  combined: StatementCoverage | null;
  lanes: Record<string, StatementCoverage>;
  /** Statements only e2e reaches. */
  onlyE2e: StatementCoverage | null;
  languages: LanguageCoverage[];
  /** Combined percent minus the base report's. Null when the base has no report: never against an older one. */
  deltaPercent: number | null;
  baseHasReport: boolean;
  diff: DiffCoverage | null;
  excludedGeneratedStatements: number;
}

/** The accumulated report of one commit's clean tree: the merge of every run recorded for it. */
export interface CommitReport {
  gitCommit: string;
  treeHash: string;
  /** The lanes merged so far, so "62%, unit only" is never mistaken for a full figure. */
  lanes: string[];
  runCount: number;
  totals: TestTotals;
  coverage: Coverage | null;
  reportDigest: string;
  updatedMs: number | null;
}

export interface TestRunCi {
  provider: string;
  runId: string;
  runUrl: string;
  attempt: number;
}

/** One recorded run (one lane on one tree), as lists show it. */
export interface TestRun {
  runId: string;
  project: string;
  gitCommit: string;
  treeHash: string;
  dirty: boolean;
  branch: string;
  baseSha: string;
  lane: string;
  source: TestRunSource;
  environmentId: string;
  ci: TestRunCi | null;
  startedMs: number | null;
  finishedMs: number | null;
  exitCode: number;
  /** "undetermined" until ingest finishes. */
  outcome: TestRunOutcome;
  ingestState: TestIngestState;
  ingestError: string;
  totals: TestTotals;
  /** This run's own combined coverage; null when it carried none. */
  coveragePercent: number | null;
  forgeVersion: string;
  uploadedBy: string;
  createdMs: number | null;
}

/** A release placed on a coverage trend. */
export interface ReleaseMarker {
  releaseVersion: string;
  gitCommit: string;
  cutMs: number | null;
}

/** One commit on a coverage trend. */
export interface CoveragePoint {
  gitCommit: string;
  atMs: number | null;
  combinedPercent: number;
  lanesPercent: Record<string, number>;
}

export interface PackageCoverage {
  path: string;
  lang: string;
  combined: StatementCoverage | null;
  lanes: Record<string, StatementCoverage>;
  deltaPercent: number | null;
}

/** A quarantine entry on the default branch (`ci/quarantine/…`). */
export interface TestQuarantine {
  path: string;
  issueUrl: string;
  reason: string;
  signature: string;
  expiresMs: number | null;
  /** Past its expiry: still applied, reported as overdue. */
  overdue: boolean;
}

/** One test's flake record. */
export interface TestFlake {
  test: TestKey;
  state: TestFlakeState;
  /** The flake rate (Wilson lower bound): a ranking shown with its n, never a trigger. */
  score: number;
  /** The rate's n: completed default-branch runs of the test's lane in which it ran. */
  scoreRuns: number;
  confirmedEvents30d: number;
  signals: TestFlakeSignal[];
  firstSeenMs: number | null;
  lastEventMs: number | null;
  quarantinedMs: number | null;
  quarantine: TestQuarantine | null;
  issueUrl: string;
}

export interface TestFlakeEvent {
  signal: TestFlakeSignal;
  verdict: TestFlakeVerdict;
  gitCommit: string;
  runIds: string[];
  failureSignature: string;
  atMs: number | null;
  /** When the verdict is "correlated": the infra event the failure belongs to. */
  infraGroup: string;
}

/**
 * The flake state a project acts at (forge.yaml): "suspected" when it deletes
 * or quarantines on the first confirmed flake (tests.flakes.issues on, or
 * quarantine: pr; forge, control-plane and reliant), "flaky" otherwise (the
 * customer default, design §3.1). It decides what the nav badge and "Needs
 * attention" count.
 */
export type TestsFlakePolicy = "suspected" | "flaky";

/** GetTestSummary: one branch's test health — what the Tests Overview draws. */
export interface TestSummary {
  /** The branch summarised (the default branch when none was asked for). */
  branch: string;
  /** The newest commit report on the branch; null when none exists. */
  latest: CommitReport | null;
  /** Oldest first. */
  trend: CoveragePoint[];
  releaseMarkers: ReleaseMarker[];
  /** Runs passed over runs judged, 0–1. Null when no run was judged: never 100% of nothing. */
  passRate: number | null;
  /** The pass rate's n. */
  passRateRuns: number;
  suspected: number;
  flaky: number;
  quarantined: number;
  /** Null when no run was recorded. */
  lastRun: TestRun | null;
  /** The lowest-covered packages among those changed recently, lowest first. */
  lowCoverage: PackageCoverage[];
  /** Failures classified "host" in 30 days: host health, never scored against a test. */
  hostFailures30d: number;
  /** Those failures by host signature (network, disk, port, registry, …), most events first as the server orders them. */
  hostHealth: HostHealth[];
  /** The state the project acts at. "flaky" while the server does not say (control-plane#3331 item 5). */
  flakePolicy: TestsFlakePolicy;
}

/** One host signature's failures in the summary's window: never scored against a test. */
export interface HostHealth {
  /** The host rule's name: "network", "disk", "port", "registry", … */
  signature: string;
  events: number;
  /** The runs they happened in, first seen first. */
  runs: string[];
  /** The tests that failed this way (TestKey.id). */
  tests: string[];
}

export interface TestRunPage {
  runs: TestRun[];
  nextPageToken: string;
  /** Runs matching the filters across every page; null when the server did not count them. */
  totalCount: number | null;
}

export interface TestFailure {
  test: TestKey;
  /** Scrubbed of known secret shapes, truncated to 8 KiB. */
  output: string;
  file: string;
  line: number;
  failureSignature: string;
  attempts: number;
  verdict: TestFlakeVerdict;
  /** When verdict is "correlated": the infra event it belongs to, shared by the co-failing tests. "" otherwise. */
  infraGroup: string;
}

export interface TestTiming {
  test: TestKey;
  durationMs: number;
}

/** GetTestRun: one run's failures, retry passes, slowest tests and zero-evidence packages. */
export interface TestRunDetail {
  run: TestRun | null;
  failures: TestFailure[];
  retryPasses: TestKey[];
  slowest: TestTiming[];
  zeroEvidencePackages: string[];
  packageCount: number;
}

/** Whose coverage: a commit's report, a release's commit's, or an environment's current release's. */
export type CoverageTarget = { gitCommit: string } | { releaseVersion: string } | { environmentId: string };

export interface CoverageNode {
  path: string;
  kind: CoverageNodeKind;
  lang: string;
  combined: StatementCoverage | null;
  lanes: Record<string, StatementCoverage>;
  diff: DiffCoverage | null;
  changed: boolean;
  /** A file whose blob came from an uncommitted tree: no source to fetch ("uncommitted", not "gone"). False for packages and directories. */
  dirty: boolean;
}

/** GetCoverage: one path's totals and its children (packages or directories first, then files). */
export interface CoverageTree {
  gitCommit: string;
  treeHash: string;
  lanes: string[];
  path: string;
  coverage: Coverage | null;
  children: CoverageNode[];
}

export interface LineCoverage {
  line: number;
  statements: number;
  /** Per lane; a lane absent here did not reach the line. */
  hits: Record<string, number>;
  partial: boolean;
  changed: boolean;
}

/** GetFileCoverage: one file line by line, and the git blob id a viewer fetches source by. Never source. */
export interface FileCoverage {
  gitCommit: string;
  path: string;
  importPath: string;
  /** "" when the run had uncommitted changes: there is no blob to fetch. */
  blobId: string;
  lang: string;
  granularity: string;
  lanes: string[];
  coverage: Coverage | null;
  lines: LineCoverage[];
  /** The blob came from an uncommitted working tree: it is in no commit, so there is no source to fetch. */
  dirty: boolean;
}

/** GetReleaseTests: a release commit's report, and what changed since the previous release. */
export interface ReleaseTests {
  releaseId: string;
  releaseVersion: string;
  gitCommit: string;
  status: TestEvidenceStatus;
  reason: string;
  report: CommitReport | null;
  previousReleaseVersion: string;
  coverageDeltaPercent: number | null;
  newFailures: TestKey[];
  newFlakes: TestFlake[];
}

export interface TestFlakePage {
  tests: TestFlake[];
  nextPageToken: string;
}

export interface TestOutcome {
  runId: string;
  gitCommit: string;
  branch: string;
  lane: string;
  status: TestCaseStatus;
  attempts: number;
  retryPassed: boolean;
  durationMs: number;
  atMs: number | null;
}

export interface TestFailureSignature {
  signature: string;
  sampleOutput: string;
  count: number;
  verdict: TestFlakeVerdict;
  firstSeenMs: number | null;
  lastSeenMs: number | null;
}

/** GetTest: one test's outcomes over its last runs, its flake record, events and signatures. */
export interface TestDetail {
  flake: TestFlake | null;
  /** Newest first. */
  outcomes: TestOutcome[];
  events: TestFlakeEvent[];
  signatures: TestFailureSignature[];
}

/**
 * Coverage of one language at one granularity, summed over statements:
 * `combined.percent` is Σcovered / Σstatements as the server computed it,
 * never an average of percentages. A figure of one granularity is never
 * mixed with one of the other, nor one language with another.
 */
export interface PortfolioCoverage {
  /** "go" | "ts" | another runner's language. */
  lang: string;
  /** "statement", or "line" when any file was counted by line. */
  granularity: string;
  combined: StatementCoverage;
  /** The projects summed into this figure: 1 on a project's row. */
  projects: number;
}

/** One project's test health on its default branch, as GetTestSummary answers it with no branch named. */
export interface PortfolioProject {
  project: string;
  /** False for a project with no recorded run (or one this org does not have, which reads the same): every figure is then empty. */
  hasRuns: boolean;
  /** The default branch summarised; "" when no recent run names one. */
  branch: string;
  latest: CommitReport | null;
  /** latest's coverage per language, generated code left out. */
  coverage: PortfolioCoverage[];
  /** 0–1; null when no run was judged: never 100% of nothing. */
  passRate: number | null;
  passRateRuns: number;
  suspected: number;
  flaky: number;
  quarantined: number;
  lastRun: TestRun | null;
}

/** GetTestPortfolio's totals: the rows summed by the server, rates weighted, never averaged. */
export interface PortfolioTotals {
  projects: number;
  projectsWithRuns: number;
  projectsWithReport: number;
  /** Every row's passed runs over every row's judged runs; null when none was judged. */
  passRate: number | null;
  passRateRuns: number;
  suspected: number;
  flaky: number;
  quarantined: number;
  /** The rows' latest reports' totals, summed. */
  latestTotals: TestTotals;
  /** Per (language, granularity), by language then granularity. */
  coverage: PortfolioCoverage[];
}

/** GetTestPortfolio: several projects' test health at once, and the server's totals across them. */
export interface TestPortfolio {
  /** In the request's order when projects were named, else most recent run first. */
  projects: PortfolioProject[];
  totals: PortfolioTotals;
  /** No project was named and the org has more than 50 with runs: only the 50 with the most recent run are listed and totalled. */
  truncated: boolean;
}

// ── Requests ────────────────────────────────────────────────────────────────

/** Every request names its org (null: the caller's default) and the forge project. */
export interface TestsRequestBase {
  orgId: string | null;
  project: string;
}

export interface SummaryRequest extends TestsRequestBase {
  /** Unset: the project's default branch. */
  branch?: string;
  /** Only runs recorded against this environment. */
  environmentId?: string;
  trendPoints?: number;
  passRateRuns?: number;
}

/** ListTestRuns' filters: each one optional, unset means no filter. */
export interface TestRunFilters {
  branch?: string;
  gitCommit?: string;
  releaseVersion?: string;
  environmentId?: string;
  lane?: string;
  source?: Exclude<TestRunSource, "unknown">;
  outcome?: TestRunOutcome;
}

export interface RunsRequest extends TestsRequestBase {
  filters: TestRunFilters;
  pageSize?: number;
  pageToken?: string;
}

export interface RunRequest extends TestsRequestBase {
  runId: string;
}

export interface CoverageRequest extends TestsRequestBase {
  target: CoverageTarget;
  /** Only this lane. Unset: combined, every lane beside it. */
  lane?: string;
  /** Repo-relative; "" is the root. */
  path: string;
  baseSha?: string;
  onlyE2e?: boolean;
  changedOnly?: boolean;
}

export interface FileCoverageRequest extends TestsRequestBase {
  target: CoverageTarget;
  path: string;
  baseSha?: string;
}

export interface ReleaseTestsRequest extends TestsRequestBase {
  version: string;
}

/** ListFlakyTests' filters. Unset state: every state but healthy. */
export interface FlakyFilters {
  state?: Exclude<TestFlakeState, "unknown">;
  minScore?: number;
  package?: string;
}

export interface FlakyRequest extends TestsRequestBase {
  filters: FlakyFilters;
  pageSize?: number;
  pageToken?: string;
}

export interface TestRequest extends TestsRequestBase {
  testId: string;
}

/** GetTestPortfolio's request: it names no one project. */
export interface PortfolioRequest {
  orgId: string | null;
  /** At most 50, each once. Empty: every project of the org with a recorded run (the 50 most recent). */
  projects: readonly string[];
}

export interface CallOptions {
  signal?: AbortSignal;
}
