// Copyright (c) 2026 Reliant Labs

/**
 * The ONLY file that knows TestsService's wire shape: the response messages,
 * as protobuf-es generates them, mapped to the plain types in types.ts, and
 * the requests built from them.
 *
 * The wire interfaces below are the structural subset of the generated
 * messages (gen/controlplane/services/tests/v1/tests_pb.ts, vendored from
 * control-plane at the commit proto-vendor/controlplane/SOURCE names): field
 * names as protobuf-es writes them, enums as their numbers, int64 as bigint,
 * timestamps as google.protobuf.Timestamp. testApi.ts hands each generated
 * response straight to its mapper here, so the compiler checks that every
 * field below exists on the real message with a compatible type. They stay
 * plain interfaces, not the generated types, because the fixtures
 * (components/Forge/Tests/*Fixtures.ts) build them as object literals.
 * Fields added to the proto after the first sync are optional here, so the
 * fixtures need not set them.
 */

import { timestampMs, type Timestamp } from "@bufbuild/protobuf/wkt";

import type {
  CommitReport,
  Coverage,
  CoverageNode,
  CoverageNodeKind,
  CoveragePoint,
  CoverageRequest,
  CoverageTarget,
  CoverageTree,
  DiffCoverage,
  FileCoverage,
  FileCoverageRequest,
  FlakyRequest,
  HostHealth,
  LanguageCoverage,
  LineCoverage,
  PackageCoverage,
  PortfolioCoverage,
  PortfolioProject,
  PortfolioRequest,
  ReleaseMarker,
  ReleaseTests,
  ReleaseTestsRequest,
  RunRequest,
  RunsRequest,
  StatementCoverage,
  SummaryRequest,
  TestCaseStatus,
  TestDetail,
  TestEvidenceStatus,
  TestFailure,
  TestFlake,
  TestFlakeEvent,
  TestFlakePage,
  TestFlakeSignal,
  TestFlakeState,
  TestFlakeVerdict,
  TestIngestState,
  TestKey,
  TestPortfolio,
  TestQuarantine,
  TestRequest,
  TestRun,
  TestRunDetail,
  TestRunOutcome,
  TestRunPage,
  TestRunSource,
  TestSummary,
  TestTotals,
} from "./types";

// ── Enums (services/tests/v1/tests.proto) ────────────────────────────────────

export const WireRunSource = { UNSPECIFIED: 0, CI: 1, LOCAL: 2, ENV: 3, CONFIRM: 4 } as const;
export const WireRunOutcome = { UNSPECIFIED: 0, PASSED: 1, FAILED: 2, UNDETERMINED: 3 } as const;
export const WireIngestState = { UNSPECIFIED: 0, PENDING: 1, INGESTED: 2, FAILED: 3 } as const;
export const WireCaseStatus = { UNSPECIFIED: 0, PASS: 1, FAIL: 2, SKIP: 3 } as const;
export const WireFlakeState = { UNSPECIFIED: 0, HEALTHY: 1, SUSPECTED: 2, FLAKY: 3, QUARANTINED: 4, FIXED: 5, REMOVED: 6 } as const;
export const WireFlakeSignal = { UNSPECIFIED: 0, SAME_TREE: 1, RETRY_PASS: 2, CONFIRM: 3, HISTORY: 4 } as const;
export const WireFlakeVerdict = { UNSPECIFIED: 0, CONFIRMED: 1, REAL: 2, RACED: 3, INCONCLUSIVE: 4, UNATTRIBUTED: 5, HOST: 6, CORRELATED: 7 } as const;
export const WireEvidenceStatus = { UNSPECIFIED: 0, OK: 1, PARTIAL: 2, UNAVAILABLE: 3 } as const;
export const WireNodeKind = { UNSPECIFIED: 0, PACKAGE: 1, DIRECTORY: 2, FILE: 3 } as const;

// ── Messages ────────────────────────────────────────────────────────────────

export interface WireTestKey {
  id: string;
  lang: string;
  package: string;
  name: string;
  file: string;
  line: number;
}

export interface WireTotals {
  passed: bigint;
  failed: bigint;
  skipped: bigint;
  retryPassed: bigint;
}

export interface WireRunCi {
  provider: string;
  runId: string;
  runUrl: string;
  attempt: number;
}

export interface WireStatementCoverage {
  statements: bigint;
  covered: bigint;
  percent: number;
}

export interface WireDiffCoverage {
  baseSha: string;
  lines: bigint;
  covered: bigint;
  percent: number;
}

export interface WireLanguageCoverage {
  lang: string;
  combined?: WireStatementCoverage;
  lanes: { [lane: string]: WireStatementCoverage };
}

export interface WireCoverage {
  granularity: string;
  combined?: WireStatementCoverage;
  lanes: { [lane: string]: WireStatementCoverage };
  onlyE2e?: WireStatementCoverage;
  languages: WireLanguageCoverage[];
  deltaPercent?: number;
  baseHasReport: boolean;
  diff?: WireDiffCoverage;
  excludedGeneratedStatements: bigint;
}

export interface WireCommitReport {
  gitCommit: string;
  treeHash: string;
  lanes: string[];
  runCount: number;
  totals?: WireTotals;
  coverage?: WireCoverage;
  reportDigest: string;
  updatedAt?: Timestamp;
}

export interface WireRunSummary {
  runId: string;
  project: string;
  gitCommit: string;
  treeHash: string;
  dirty: boolean;
  branch: string;
  baseSha: string;
  lane: string;
  source: number;
  environmentId: string;
  ci?: WireRunCi;
  startedAt?: Timestamp;
  finishedAt?: Timestamp;
  exitCode: number;
  outcome: number;
  ingestState: number;
  ingestError: string;
  totals?: WireTotals;
  coveragePercent?: number;
  forgeVersion: string;
  uploadedBy: string;
  createdAt?: Timestamp;
}

export interface WireReleaseMarker {
  releaseVersion: string;
  gitCommit: string;
  cutAt?: Timestamp;
}

export interface WireCoveragePoint {
  gitCommit: string;
  at?: Timestamp;
  combinedPercent: number;
  lanesPercent: { [lane: string]: number };
}

export interface WirePackageCoverage {
  path: string;
  lang: string;
  combined?: WireStatementCoverage;
  lanes: { [lane: string]: WireStatementCoverage };
  deltaPercent?: number;
}

export interface WireQuarantine {
  path: string;
  issueUrl: string;
  reason: string;
  signature: string;
  expiresAt?: Timestamp;
  overdue: boolean;
}

export interface WireFlake {
  test?: WireTestKey;
  state: number;
  score: number;
  scoreRuns: bigint;
  confirmedEvents30d: number;
  signals: number[];
  firstSeenAt?: Timestamp;
  lastEventAt?: Timestamp;
  quarantinedAt?: Timestamp;
  quarantine?: WireQuarantine;
  issueUrl: string;
}

export interface WireFlakeEvent {
  signal: number;
  verdict: number;
  gitCommit: string;
  runIds: string[];
  failureSignature: string;
  at?: Timestamp;
  infraGroup: string;
}

export interface WireSummaryResponse {
  branch: string;
  latest?: WireCommitReport;
  coverageTrend: WireCoveragePoint[];
  releaseMarkers: WireReleaseMarker[];
  passRate: number;
  passRateRuns: number;
  suspectedCount: number;
  flakyCount: number;
  quarantinedCount: number;
  lastRun?: WireRunSummary;
  lowCoveragePackages: WirePackageCoverage[];
  hostFailures30d: number;
  hostHealth?: WireHostHealth[];
  /**
   * NOT IN THE PROTO YET (absent from the vendored tests.proto at
   * control-plane 86c178dbadb5; control-plane#3331 item 5, the name may change
   * when it lands): true when the project acts at `suspected` (forge.yaml
   * tests.flakes.issues on, or quarantine: pr). Absent reads as false.
   */
  actsOnSuspected?: boolean;
}

export interface WireHostHealth {
  signature: string;
  events: number;
  runs: string[];
  tests: string[];
}

export interface WireListRunsResponse {
  runs: WireRunSummary[];
  nextPageToken: string;
  /** Unset when the server did not count them. */
  totalCount?: bigint;
}

export interface WireFailure {
  test?: WireTestKey;
  output: string;
  file: string;
  line: number;
  failureSignature: string;
  attempts: number;
  verdict: number;
  infraGroup?: string;
}

export interface WireTiming {
  test?: WireTestKey;
  durationMs: bigint;
}

export interface WireRunResponse {
  run?: WireRunSummary;
  failures: WireFailure[];
  retryPasses: WireTestKey[];
  slowest: WireTiming[];
  zeroEvidencePackages: string[];
  packageCount: number;
}

export interface WireCoverageNode {
  path: string;
  kind: number;
  lang: string;
  combined?: WireStatementCoverage;
  lanes: { [lane: string]: WireStatementCoverage };
  diff?: WireDiffCoverage;
  changed: boolean;
  dirty?: boolean;
}

export interface WireCoverageResponse {
  gitCommit: string;
  treeHash: string;
  lanes: string[];
  path: string;
  coverage?: WireCoverage;
  children: WireCoverageNode[];
}

export interface WireLineCoverage {
  line: number;
  statements: number;
  hits: { [lane: string]: bigint };
  partial: boolean;
  changed: boolean;
}

export interface WireFileCoverageResponse {
  gitCommit: string;
  path: string;
  importPath: string;
  blobId: string;
  lang: string;
  granularity: string;
  lanes: string[];
  coverage?: WireCoverage;
  lines: WireLineCoverage[];
  dirty?: boolean;
}

export interface WireReleaseTestsResponse {
  releaseId: string;
  releaseVersion: string;
  gitCommit: string;
  status: number;
  reason: string;
  report?: WireCommitReport;
  previousReleaseVersion: string;
  coverageDeltaPercent?: number;
  newFailures: WireTestKey[];
  newFlakes: WireFlake[];
}

export interface WireListFlakyResponse {
  tests: WireFlake[];
  nextPageToken: string;
}

export interface WireOutcome {
  runId: string;
  gitCommit: string;
  branch: string;
  lane: string;
  status: number;
  attempts: number;
  retryPassed: boolean;
  durationMs: bigint;
  at?: Timestamp;
}

export interface WireFailureSignature {
  signature: string;
  sampleOutput: string;
  count: number;
  verdict: number;
  firstSeenAt?: Timestamp;
  lastSeenAt?: Timestamp;
}

export interface WireTestResponse {
  flake?: WireFlake;
  outcomes: WireOutcome[];
  events: WireFlakeEvent[];
  signatures: WireFailureSignature[];
}

export interface WirePortfolioCoverage {
  lang: string;
  granularity: string;
  combined?: WireStatementCoverage;
  projects: number;
}

export interface WirePortfolioProject {
  project: string;
  hasRuns: boolean;
  branch: string;
  latest?: WireCommitReport;
  coverage: WirePortfolioCoverage[];
  passRate: number;
  passRateRuns: number;
  suspectedCount: number;
  flakyCount: number;
  quarantinedCount: number;
  lastRun?: WireRunSummary;
}

export interface WirePortfolioTotals {
  projects: number;
  projectsWithRuns: number;
  projectsWithReport: number;
  passRate: number;
  passRateRuns: number;
  suspectedCount: number;
  flakyCount: number;
  quarantinedCount: number;
  latestTotals?: WireTotals;
  coverage: WirePortfolioCoverage[];
}

export interface WirePortfolioResponse {
  projects: WirePortfolioProject[];
  totals?: WirePortfolioTotals;
  truncated: boolean;
}

// ── Responses → plain types ─────────────────────────────────────────────────

function ms(ts: Timestamp | undefined): number | null {
  if (!ts) return null;
  const value = timestampMs(ts);
  // The zero timestamp is "unset" written out, never an event in 1970.
  return value > 0 ? value : null;
}

/** An int64 count as a number. Counts stay far below 2^53. */
const n = (value: bigint | undefined) => Number(value ?? 0n);

function nameOf<T extends string>(names: Record<number, T>, fallback: T) {
  return (value: number): T => names[value] ?? fallback;
}

export const runSourceOf = nameOf<TestRunSource>({ 1: "ci", 2: "local", 3: "env", 4: "confirm" }, "unknown");
/** An outcome this client does not know reads as undetermined: never as passed. */
export const runOutcomeOf = nameOf<TestRunOutcome>({ 1: "passed", 2: "failed" }, "undetermined");
/** An ingest state this client does not know reads as pending: its figures are not in. */
export const ingestStateOf = nameOf<TestIngestState>({ 2: "ingested", 3: "failed" }, "pending");
export const caseStatusOf = nameOf<TestCaseStatus>({ 1: "pass", 2: "fail", 3: "skip" }, "unknown");
export const flakeStateOf = nameOf<TestFlakeState>({ 1: "healthy", 2: "suspected", 3: "flaky", 4: "quarantined", 5: "fixed", 6: "removed" }, "unknown");
export const flakeSignalOf = nameOf<TestFlakeSignal>({ 1: "same-tree", 2: "retry-pass", 3: "confirm", 4: "history" }, "unknown");
export const flakeVerdictOf = nameOf<TestFlakeVerdict>(
  { 1: "confirmed", 2: "real", 3: "raced", 4: "inconclusive", 5: "unattributed", 6: "host", 7: "correlated" },
  "unknown"
);
/** Evidence this client cannot grade is unavailable, never ok. */
export const evidenceStatusOf = nameOf<TestEvidenceStatus>({ 1: "ok", 2: "partial" }, "unavailable");
export const nodeKindOf = nameOf<CoverageNodeKind>({ 1: "package", 2: "directory", 3: "file" }, "unknown");

function keyOf(k: WireTestKey | undefined): TestKey {
  return { id: k?.id ?? "", lang: k?.lang ?? "", package: k?.package ?? "", name: k?.name ?? "", file: k?.file ?? "", line: k?.line ?? 0 };
}

function totalsOf(t: WireTotals | undefined): TestTotals {
  return { passed: n(t?.passed), failed: n(t?.failed), skipped: n(t?.skipped), retryPassed: n(t?.retryPassed) };
}

function statementsOf(s: WireStatementCoverage | undefined): StatementCoverage | null {
  return s ? { statements: n(s.statements), covered: n(s.covered), percent: s.percent } : null;
}

function lanesOf(lanes: { [lane: string]: WireStatementCoverage } | undefined): Record<string, StatementCoverage> {
  const out: Record<string, StatementCoverage> = {};
  for (const [lane, value] of Object.entries(lanes ?? {})) out[lane] = statementsOf(value)!;
  return out;
}

function diffOf(d: WireDiffCoverage | undefined): DiffCoverage | null {
  return d ? { baseSha: d.baseSha, lines: n(d.lines), covered: n(d.covered), percent: d.percent } : null;
}

export function coverageOf(c: WireCoverage | undefined): Coverage | null {
  if (!c) return null;
  return {
    granularity: c.granularity,
    combined: statementsOf(c.combined),
    lanes: lanesOf(c.lanes),
    onlyE2e: statementsOf(c.onlyE2e),
    languages: c.languages.map((l): LanguageCoverage => ({ lang: l.lang, combined: statementsOf(l.combined), lanes: lanesOf(l.lanes) })),
    // A delta against no base report is no delta, whatever a server says beside it.
    deltaPercent: c.baseHasReport ? (c.deltaPercent ?? null) : null,
    baseHasReport: c.baseHasReport,
    diff: diffOf(c.diff),
    excludedGeneratedStatements: n(c.excludedGeneratedStatements),
  };
}

function reportOf(r: WireCommitReport | undefined): CommitReport | null {
  if (!r) return null;
  return {
    gitCommit: r.gitCommit,
    treeHash: r.treeHash,
    lanes: r.lanes,
    runCount: r.runCount,
    totals: totalsOf(r.totals),
    coverage: coverageOf(r.coverage),
    reportDigest: r.reportDigest,
    updatedMs: ms(r.updatedAt),
  };
}

export function runOf(r: WireRunSummary): TestRun {
  const ingestState = ingestStateOf(r.ingestState);
  return {
    runId: r.runId,
    project: r.project,
    gitCommit: r.gitCommit,
    treeHash: r.treeHash,
    dirty: r.dirty,
    branch: r.branch,
    baseSha: r.baseSha,
    lane: r.lane,
    source: runSourceOf(r.source),
    environmentId: r.environmentId,
    ci: r.ci ? { provider: r.ci.provider, runId: r.ci.runId, runUrl: r.ci.runUrl, attempt: r.ci.attempt } : null,
    startedMs: ms(r.startedAt),
    finishedMs: ms(r.finishedAt),
    exitCode: r.exitCode,
    // Until ingest finishes the counts are not in, so neither is a verdict.
    outcome: ingestState === "ingested" ? runOutcomeOf(r.outcome) : "undetermined",
    ingestState,
    ingestError: r.ingestError,
    totals: totalsOf(r.totals),
    coveragePercent: r.coveragePercent ?? null,
    forgeVersion: r.forgeVersion,
    uploadedBy: r.uploadedBy,
    createdMs: ms(r.createdAt),
  };
}

function quarantineOf(q: WireQuarantine | undefined): TestQuarantine | null {
  if (!q) return null;
  return { path: q.path, issueUrl: q.issueUrl, reason: q.reason, signature: q.signature, expiresMs: ms(q.expiresAt), overdue: q.overdue };
}

export function flakeOf(f: WireFlake): TestFlake {
  return {
    test: keyOf(f.test),
    state: flakeStateOf(f.state),
    score: f.score,
    scoreRuns: n(f.scoreRuns),
    confirmedEvents30d: f.confirmedEvents30d,
    signals: f.signals.map(flakeSignalOf),
    firstSeenMs: ms(f.firstSeenAt),
    lastEventMs: ms(f.lastEventAt),
    quarantinedMs: ms(f.quarantinedAt),
    quarantine: quarantineOf(f.quarantine),
    issueUrl: f.issueUrl,
  };
}

export function summaryOf(res: WireSummaryResponse): TestSummary {
  return {
    branch: res.branch,
    latest: reportOf(res.latest),
    trend: res.coverageTrend.map((p): CoveragePoint => ({ gitCommit: p.gitCommit, atMs: ms(p.at), combinedPercent: p.combinedPercent, lanesPercent: { ...p.lanesPercent } })),
    releaseMarkers: res.releaseMarkers.map((m): ReleaseMarker => ({ releaseVersion: m.releaseVersion, gitCommit: m.gitCommit, cutMs: ms(m.cutAt) })),
    // A rate over no judged run is no rate: proto3 writes it as 0, which would read as "everything failed".
    passRate: res.passRateRuns > 0 ? res.passRate : null,
    passRateRuns: res.passRateRuns,
    suspected: res.suspectedCount,
    flaky: res.flakyCount,
    quarantined: res.quarantinedCount,
    lastRun: res.lastRun ? runOf(res.lastRun) : null,
    lowCoverage: res.lowCoveragePackages.map(
      (p): PackageCoverage => ({ path: p.path, lang: p.lang, combined: statementsOf(p.combined), lanes: lanesOf(p.lanes), deltaPercent: p.deltaPercent ?? null })
    ),
    hostFailures30d: res.hostFailures30d,
    hostHealth: (res.hostHealth ?? []).map((h): HostHealth => ({ signature: h.signature, events: h.events, runs: [...h.runs], tests: [...h.tests] })),
    // Absent until the server says: the customer default, flaky only.
    flakePolicy: res.actsOnSuspected ? "suspected" : "flaky",
  };
}

function portfolioCoverageOf(c: WirePortfolioCoverage): PortfolioCoverage {
  return { lang: c.lang, granularity: c.granularity, combined: statementsOf(c.combined) ?? { statements: 0, covered: 0, percent: 0 }, projects: c.projects };
}

function portfolioProjectOf(p: WirePortfolioProject): PortfolioProject {
  return {
    project: p.project,
    hasRuns: p.hasRuns,
    branch: p.branch,
    latest: reportOf(p.latest),
    // A figure over no statements is no figure: it would read as 0% covered.
    coverage: p.coverage.map(portfolioCoverageOf).filter((c) => c.combined.statements > 0),
    // As GetTestSummary's: a rate over no judged run is no rate.
    passRate: p.passRateRuns > 0 ? p.passRate : null,
    passRateRuns: p.passRateRuns,
    suspected: p.suspectedCount,
    flaky: p.flakyCount,
    quarantined: p.quarantinedCount,
    lastRun: p.lastRun ? runOf(p.lastRun) : null,
  };
}

export function portfolioOf(res: WirePortfolioResponse): TestPortfolio {
  const t = res.totals;
  return {
    projects: res.projects.map(portfolioProjectOf),
    totals: {
      projects: t?.projects ?? 0,
      projectsWithRuns: t?.projectsWithRuns ?? 0,
      projectsWithReport: t?.projectsWithReport ?? 0,
      passRate: t && t.passRateRuns > 0 ? t.passRate : null,
      passRateRuns: t?.passRateRuns ?? 0,
      suspected: t?.suspectedCount ?? 0,
      flaky: t?.flakyCount ?? 0,
      quarantined: t?.quarantinedCount ?? 0,
      latestTotals: totalsOf(t?.latestTotals),
      coverage: (t?.coverage ?? []).map(portfolioCoverageOf).filter((c) => c.combined.statements > 0),
    },
    truncated: res.truncated,
  };
}

export function runPageOf(res: WireListRunsResponse): TestRunPage {
  return { runs: res.runs.map(runOf), nextPageToken: res.nextPageToken, totalCount: res.totalCount === undefined ? null : n(res.totalCount) };
}

export function runDetailOf(res: WireRunResponse): TestRunDetail {
  return {
    run: res.run ? runOf(res.run) : null,
    failures: res.failures.map(
      (f): TestFailure => ({
        test: keyOf(f.test),
        output: f.output,
        file: f.file,
        line: f.line,
        failureSignature: f.failureSignature,
        attempts: f.attempts,
        verdict: flakeVerdictOf(f.verdict),
        infraGroup: f.infraGroup ?? "",
      })
    ),
    retryPasses: res.retryPasses.map(keyOf),
    slowest: res.slowest.map((t) => ({ test: keyOf(t.test), durationMs: n(t.durationMs) })),
    zeroEvidencePackages: res.zeroEvidencePackages,
    packageCount: res.packageCount,
  };
}

export function coverageTreeOf(res: WireCoverageResponse): CoverageTree {
  return {
    gitCommit: res.gitCommit,
    treeHash: res.treeHash,
    lanes: res.lanes,
    path: res.path,
    coverage: coverageOf(res.coverage),
    children: res.children.map(
      (c): CoverageNode => ({
        path: c.path,
        kind: nodeKindOf(c.kind),
        lang: c.lang,
        combined: statementsOf(c.combined),
        lanes: lanesOf(c.lanes),
        diff: diffOf(c.diff),
        changed: c.changed,
        dirty: c.dirty ?? false,
      })
    ),
  };
}

export function fileCoverageOf(res: WireFileCoverageResponse): FileCoverage {
  return {
    gitCommit: res.gitCommit,
    path: res.path,
    importPath: res.importPath,
    blobId: res.blobId,
    lang: res.lang,
    granularity: res.granularity,
    lanes: res.lanes,
    coverage: coverageOf(res.coverage),
    lines: res.lines.map(
      (l): LineCoverage => ({
        line: l.line,
        statements: l.statements,
        hits: Object.fromEntries(Object.entries(l.hits).map(([lane, hits]) => [lane, n(hits)])),
        partial: l.partial,
        changed: l.changed,
      })
    ),
    dirty: res.dirty ?? false,
  };
}

export function releaseTestsOf(res: WireReleaseTestsResponse): ReleaseTests {
  const status = evidenceStatusOf(res.status);
  return {
    releaseId: res.releaseId,
    releaseVersion: res.releaseVersion,
    gitCommit: res.gitCommit,
    status,
    reason: res.reason,
    // Unavailable evidence has no report, whatever a server sends beside it.
    report: status === "unavailable" ? null : reportOf(res.report),
    previousReleaseVersion: res.previousReleaseVersion,
    coverageDeltaPercent: res.coverageDeltaPercent ?? null,
    newFailures: res.newFailures.map(keyOf),
    newFlakes: res.newFlakes.map(flakeOf),
  };
}

export function flakePageOf(res: WireListFlakyResponse): TestFlakePage {
  return { tests: res.tests.map(flakeOf), nextPageToken: res.nextPageToken };
}

export function testDetailOf(res: WireTestResponse): TestDetail {
  return {
    flake: res.flake ? flakeOf(res.flake) : null,
    outcomes: res.outcomes.map((o) => ({
      runId: o.runId,
      gitCommit: o.gitCommit,
      branch: o.branch,
      lane: o.lane,
      status: caseStatusOf(o.status),
      attempts: o.attempts,
      retryPassed: o.retryPassed,
      durationMs: n(o.durationMs),
      atMs: ms(o.at),
    })),
    events: res.events.map(
      (e): TestFlakeEvent => ({
        signal: flakeSignalOf(e.signal),
        verdict: flakeVerdictOf(e.verdict),
        gitCommit: e.gitCommit,
        runIds: e.runIds,
        failureSignature: e.failureSignature,
        atMs: ms(e.at),
        infraGroup: e.infraGroup,
      })
    ),
    signatures: res.signatures.map((s) => ({
      signature: s.signature,
      sampleOutput: s.sampleOutput,
      count: s.count,
      verdict: flakeVerdictOf(s.verdict),
      firstSeenMs: ms(s.firstSeenAt),
      lastSeenMs: ms(s.lastSeenAt),
    })),
  };
}

// ── Requests ────────────────────────────────────────────────────────────────

const RUN_SOURCE_WIRE: Record<NonNullable<RunsRequest["filters"]["source"]>, number> = {
  ci: WireRunSource.CI,
  local: WireRunSource.LOCAL,
  env: WireRunSource.ENV,
  confirm: WireRunSource.CONFIRM,
};
const RUN_OUTCOME_WIRE: Record<TestRunOutcome, number> = {
  passed: WireRunOutcome.PASSED,
  failed: WireRunOutcome.FAILED,
  undetermined: WireRunOutcome.UNDETERMINED,
};
const FLAKE_STATE_WIRE: Record<NonNullable<FlakyRequest["filters"]["state"]>, number> = {
  healthy: WireFlakeState.HEALTHY,
  suspected: WireFlakeState.SUSPECTED,
  flaky: WireFlakeState.FLAKY,
  quarantined: WireFlakeState.QUARANTINED,
  fixed: WireFlakeState.FIXED,
  removed: WireFlakeState.REMOVED,
};

/** Only the fields that are set: every filter is proto3 `optional`, so an absent field means "no filter". */
function defined<T extends Record<string, unknown>>(fields: T): Partial<T> {
  return Object.fromEntries(Object.entries(fields).filter(([, value]) => value !== undefined && value !== null && value !== "")) as Partial<T>;
}

/** The request's `target` oneof, as protobuf-es writes a oneof. */
export function targetOf(target: CoverageTarget) {
  if ("gitCommit" in target) return { case: "gitCommit" as const, value: target.gitCommit };
  if ("releaseVersion" in target) return { case: "releaseVersion" as const, value: target.releaseVersion };
  return { case: "environmentId" as const, value: target.environmentId };
}

export function summaryRequest(req: SummaryRequest) {
  return {
    project: req.project,
    trendPoints: req.trendPoints ?? 0,
    passRateRuns: req.passRateRuns ?? 0,
    ...defined({ branch: req.branch, environmentId: req.environmentId, orgId: req.orgId ?? undefined }),
  };
}

export function runsRequest(req: RunsRequest) {
  const f = req.filters;
  return {
    project: req.project,
    pageSize: req.pageSize ?? 0,
    pageToken: req.pageToken ?? "",
    ...defined({
      branch: f.branch,
      gitCommit: f.gitCommit,
      releaseVersion: f.releaseVersion,
      environmentId: f.environmentId,
      lane: f.lane,
      source: f.source ? RUN_SOURCE_WIRE[f.source] : undefined,
      outcome: f.outcome ? RUN_OUTCOME_WIRE[f.outcome] : undefined,
      orgId: req.orgId ?? undefined,
    }),
  };
}

export function runRequest(req: RunRequest) {
  return { project: req.project, runId: req.runId, ...defined({ orgId: req.orgId ?? undefined }) };
}

export function coverageRequest(req: CoverageRequest) {
  return {
    project: req.project,
    target: targetOf(req.target),
    path: req.path,
    onlyE2e: req.onlyE2e ?? false,
    changedOnly: req.changedOnly ?? false,
    ...defined({ lane: req.lane, baseSha: req.baseSha, orgId: req.orgId ?? undefined }),
  };
}

export function fileCoverageRequest(req: FileCoverageRequest) {
  return { project: req.project, target: targetOf(req.target), path: req.path, ...defined({ baseSha: req.baseSha, orgId: req.orgId ?? undefined }) };
}

export function releaseTestsRequest(req: ReleaseTestsRequest) {
  return { project: req.project, version: req.version, ...defined({ orgId: req.orgId ?? undefined }) };
}

export function flakyRequest(req: FlakyRequest) {
  const f = req.filters;
  return {
    project: req.project,
    pageSize: req.pageSize ?? 0,
    pageToken: req.pageToken ?? "",
    ...defined({ state: f.state ? FLAKE_STATE_WIRE[f.state] : undefined, minScore: f.minScore, package: f.package, orgId: req.orgId ?? undefined }),
  };
}

export function testRequest(req: TestRequest) {
  return { project: req.project, testId: req.testId, ...defined({ orgId: req.orgId ?? undefined }) };
}

/** GetTestPortfolio's request: the projects each once (the server refuses a repeat), in the order given. */
export function portfolioRequest(req: PortfolioRequest) {
  return { projects: [...new Set(req.projects)], ...defined({ orgId: req.orgId ?? undefined }) };
}
