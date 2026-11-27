// Copyright (c) 2026 Reliant Labs

/**
 * shop's Tests pages (Overview, Coverage, Runs, a run, Flaky tests, a test,
 * and each release's Tests tab), told in shopStory's terms, as WIRE messages:
 * handlers/tests.ts serves them as TestsService's answers, and the pages'
 * real client maps them with services/controlPlane/tests/wire.ts. The
 * coverage file view's source (charge.go, SHOP_CHARGE_SOURCE) is served by
 * handlers/coverageSource.ts.
 *
 *   main       every commit of the release ledger (DEMO_RELEASES) ran unit,
 *              integration and e2e in CI (github-actions), in the 40 minutes
 *              between the commit and its release's cut, all passing. Each
 *              is a point on the coverage trend, and each release's head
 *              commit a marker on it.
 *   the flake  v1.8.3's "retry the payment charge once on a timeout" (jon)
 *              brought TestCreateOrder_RetryOnTimeout, whose fake provider
 *              answers at 1.5s, exactly the retry's attempt timeout: it races.
 *              It failed and passed on retry in v1.8.3's unit run (caught: a
 *              new flake of v1.8.3), again on maya's fix branch, and again on
 *              v1.8.4's commit: two main commits, so flaky (acme/shop#142).
 *   the fix    maya's branch fix/payment-deadline pushed the test first
 *              ("test(orders): CreateOrder survives a slow payment provider"),
 *              and its unit run FAILED on it: the incident reproduced, a real
 *              failure. The fix commit made it pass, and became v1.8.4, whose
 *              charge.go (its source is served) is the Coverage page's
 *              example: every changed line covered.
 *   staging    jon's e2e run against staging after v1.8.3 reached it passed:
 *              staging's payment sandbox never times out (DEMO_INCIDENT).
 *   others     one test quarantined since v1.8.2 (the worker's NATS
 *              redelivery, maya, acme/shop#131) and one suspected (a
 *              same-tree split in checkout's confirmation email, on v1.8.2).
 *
 * Commits, authors, releases and environments are shopStory's; times are
 * measured from demoNow() (the page load), like every other demo page.
 */

import { Code, ConnectError } from "@connectrpc/connect";
import { timestampFromMs, type Timestamp } from "@bufbuild/protobuf/wkt";

import { coverageFixtureEngine, GENERIC_COVERAGE_LINES, type CoverageFixtureNode, type CoverageLineRow, type CoverageReportFixture } from "@/components/Forge/Tests/coverageFixtures";
import { portfolioRowWire, portfolioWire } from "@/components/Forge/Tests/portfolioFixtures";
import { runMatchesFilters } from "@/components/Forge/Tests/runFilterFixture";
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
  WireCaseStatus,
  WireEvidenceStatus,
  WireFlakeSignal,
  WireFlakeState,
  WireFlakeVerdict,
  WireIngestState,
  WireNodeKind,
  WireRunOutcome,
  WireRunSource,
  type WireCommitReport,
  type WireCoverage,
  type WireCoverageResponse,
  type WireFailure,
  type WireFileCoverageResponse,
  type WireFlake,
  type WireFlakeEvent,
  type WireListFlakyResponse,
  type WireListRunsResponse,
  type WireOutcome,
  type WirePortfolioCoverage,
  type WirePortfolioResponse,
  type WireReleaseTestsResponse,
  type WireRunResponse,
  type WireRunSummary,
  type WireStatementCoverage,
  type WireSummaryResponse,
  type WireTestKey,
  type WireTestResponse,
  type WireTiming,
} from "@/services/controlPlane/tests/wire";

import { FORGE_VERSION } from "../handlers/console";
import { DEMO_PROJECT } from "../handlers/scope";
import { commitAgeMs, DAY, DEMO_CI_ACTOR, DEMO_RELEASES, DEMO_REPO, demoNow, demoRelease, MINUTE, RUNNING, type DemoEnvKey } from "../shopStory";

const LANES = ["unit", "integration", "e2e"] as const;
type Lane = (typeof LANES)[number];
const REPO_URL = `https://${DEMO_REPO}`;

// ── Commits ─────────────────────────────────────────────────────────────────

/** v1.8.4's test commit, pushed first on maya's branch (its run failed); only the fix commit after it ran on main. */
export const SHOP_FIX_BRANCH = "fix/payment-deadline";
export const SHOP_BRANCH_COMMIT = demoRelease("v1.8.4")!.commits[1]!.sha;

interface ShopCommit {
  sha: string;
  subject: string;
  author: string;
  release: string;
  /** Before now: when it was committed (shopStory's commitAgeMs). */
  ago: number;
  /** Whether it is its release's head (the release was cut from it). */
  head: boolean;
}

const ALL_COMMITS: ShopCommit[] = DEMO_RELEASES.flatMap((r) =>
  r.commits.map((c, i) => ({ sha: c.sha, subject: c.subject, author: c.author, release: r.version, ago: commitAgeMs(r, i), head: i === 0 }))
).sort((a, b) => b.ago - a.ago);

/** The commits main's CI tested, oldest first: every release commit but the fix branch's test commit. */
export const SHOP_MAIN_COMMITS: readonly ShopCommit[] = ALL_COMMITS.filter((c) => c.sha !== SHOP_BRANCH_COMMIT);
const HEAD = SHOP_MAIN_COMMITS.at(-1)!;
const BRANCH = ALL_COMMITS.find((c) => c.sha === SHOP_BRANCH_COMMIT)!;
const commitOf = (sha: string) => ALL_COMMITS.find((c) => c.sha === sha);
const releaseCommit = (version: string) => demoRelease(version)?.commit ?? null;
/** v1.8.3's head: what the fix was branched from, and what prod and staging run. */
const BASE_OF_FIX = releaseCommit("v1.8.3")!;

/**
 * Each tested commit's coverage, as an offset from main's head (every file
 * moves by it), and how many tests each lane ran. v1.8.3's retry added code
 * its tests reached only partly; v1.8.4's new test reaches it all.
 */
const FIGURES: Record<string, { offset: number; tests: Record<Lane, number> }> = {
  "07e2b9d4f1a0": { offset: -1.6, tests: { unit: 588, integration: 131, e2e: 38 } },
  "8d3e17b42a90": { offset: -1.3, tests: { unit: 592, integration: 133, e2e: 38 } },
  c51f08e3a96b: { offset: -0.9, tests: { unit: 597, integration: 134, e2e: 40 } },
  "3b81d5f0e2c4": { offset: -0.9, tests: { unit: 597, integration: 134, e2e: 40 } },
  a40f6c2d9e77: { offset: -0.5, tests: { unit: 603, integration: 138, e2e: 41 } },
  e7d93a2b5c18: { offset: -1.4, tests: { unit: 606, integration: 139, e2e: 41 } },
  "51ab0e9c11d2": { offset: -1.4, tests: { unit: 607, integration: 139, e2e: 41 } },
  "9c2e41d07a3f": { offset: 0, tests: { unit: 607, integration: 139, e2e: 42 } },
};
const SKIPPED: Record<Lane, number> = { unit: 12, integration: 2, e2e: 1 };

/** A clearly synthetic, stable hex string of `length` from `seed`. */
function hexOf(seed: string, length: number): string {
  let out = "";
  for (let i = 0; out.length < length; i++) {
    let h = 0x811c9dc5 ^ i;
    for (const c of `${seed}#${i}`) h = Math.imul(h ^ c.charCodeAt(0), 0x01000193) >>> 0;
    out += h.toString(16).padStart(8, "0");
  }
  return out.slice(0, length);
}

/** A stable ULID-shaped run id (Crockford base32): the same run keeps its URL across page loads. */
function runIdOf(seed: string): string {
  const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
  const hex = hexOf(`run:${seed}`, 64);
  let id = "01JF";
  for (let i = 0; id.length < 26; i++) id += alphabet[parseInt(hex.slice(i * 2, i * 2 + 2), 16) % 32];
  return id;
}

// ── Coverage ────────────────────────────────────────────────────────────────

/** internal/orders/charge.go as v1.8.4 shipped it (the fix). Its git blob id is SHOP_CHARGE_BLOB (a test holds the two equal). */
export const SHOP_CHARGE_SOURCE = [
  "// Copyright (c) 2026 Acme",
  "",
  "package orders",
  "",
  "import (",
  "\t\"context\"",
  "\t\"errors\"",
  "\t\"fmt\"",
  "\t\"time\"",
  "",
  "\t\"github.com/acme/shop/internal/payments\"",
  ")",
  "",
  "// chargeDeadline bounds the whole charge, its retry included, well inside",
  "// CreateOrder's and Checkout's 3s deadline.",
  "const chargeDeadline = 2 * time.Second",
  "",
  "// attemptTimeout is one attempt's share: a retry starts only with time left for it.",
  "const attemptTimeout = 900 * time.Millisecond",
  "",
  "// ErrChargeTimeout is a charge the provider did not answer within chargeDeadline.",
  "var ErrChargeTimeout = errors.New(\"payments: charge failed: provider timeout\")",
  "",
  "// charge charges the order's total, retrying one timed-out attempt while the budget lasts.",
  "func (s *Service) charge(ctx context.Context, o *Order) error {",
  "\tctx, cancel := context.WithTimeout(ctx, chargeDeadline)",
  "\tdefer cancel()",
  "\tvar err error",
  "\tfor attempt := 1; attempt <= 2; attempt++ {",
  "\t\tif attempt > 1 && time.Until(deadlineOf(ctx)) < attemptTimeout {",
  "\t\t\tbreak",
  "\t\t}",
  "\t\terr = s.chargeOnce(ctx, o)",
  "\t\tif err == nil {",
  "\t\t\to.State = StatePaid",
  "\t\t\treturn nil",
  "\t\t}",
  "\t\tif !errors.Is(err, payments.ErrTimeout) {",
  "\t\t\tbreak",
  "\t\t}",
  "\t\ts.log.WarnContext(ctx, \"payments: charge attempt timed out, retrying\", \"order\", o.ID, \"attempt\", attempt)",
  "\t}",
  "\to.State = StatePaymentFailed",
  "\tif errors.Is(err, payments.ErrTimeout) || ctx.Err() != nil {",
  "\t\treturn fmt.Errorf(\"%w after %s\", ErrChargeTimeout, chargeDeadline)",
  "\t}",
  "\treturn fmt.Errorf(\"orders: charge %s: %w\", o.ID, err)",
  "}",
  "",
  "// chargeOnce is one attempt, bounded by attemptTimeout and keyed by the order, so a retry never charges twice.",
  "func (s *Service) chargeOnce(ctx context.Context, o *Order) error {",
  "\tctx, cancel := context.WithTimeout(ctx, attemptTimeout)",
  "\tdefer cancel()",
  "\treturn s.payments.Charge(ctx, payments.Charge{OrderID: o.ID, AmountCents: o.TotalCents, Currency: o.Currency, IdempotencyKey: o.ID})",
  "}",
  "",
  "func deadlineOf(ctx context.Context) time.Time {",
  "\td, _ := ctx.Deadline()",
  "\treturn d",
  "}",
  ""
].join("\n");

/** `git hash-object` of SHOP_CHARGE_SOURCE. */
export const SHOP_CHARGE_BLOB = "901b9d2550ac3532b1c5f0e862e1d9342daa7b29";
export const SHOP_CHARGE_PATH = "internal/orders/charge.go";

/** charge.go at v1.8.4, line by line: [line, statements, unit, integration, e2e, partial, changed since v1.8.3]. */
const CHARGE_LINES: CoverageLineRow[] = [
  [26, 1, 14, 6, 9, false, true],
  [27, 1, 14, 6, 9, false, false],
  [28, 1, 14, 6, 9, false, false],
  [29, 1, 14, 6, 9, false, false],
  [30, 1, 6, 1, 0, true, true],
  [31, 1, 1, 0, 0, false, true],
  [33, 1, 19, 7, 9, false, true],
  [34, 1, 19, 7, 9, false, false],
  [35, 1, 12, 6, 9, false, false],
  [36, 1, 12, 6, 9, false, false],
  [38, 1, 7, 1, 0, false, false],
  [39, 1, 0, 0, 0, false, false],
  [41, 1, 6, 1, 0, false, false],
  [43, 1, 2, 0, 0, false, false],
  [44, 1, 2, 0, 0, true, true],
  [45, 1, 2, 0, 0, false, true],
  [47, 1, 0, 0, 0, false, false],
  [52, 1, 19, 7, 9, false, true],
  [53, 1, 19, 7, 9, false, true],
  [54, 1, 19, 7, 9, false, true],
  [58, 1, 6, 1, 0, false, true],
  [59, 1, 6, 1, 0, false, true],
];

/** A row set's per-lane percent (statements reached by the lane) and combined percent (reached by any). */
function percentsOf(rows: readonly CoverageLineRow[]) {
  const total = rows.reduce((sum, r) => sum + r[1], 0);
  const pct = (reached: (r: CoverageLineRow) => boolean) => Math.round((rows.filter(reached).reduce((sum, r) => sum + r[1], 0) / total) * 1000) / 10;
  return { statements: total, combined: pct((r) => r[2] + r[3] + r[4] > 0), unit: pct((r) => r[2] > 0), integration: pct((r) => r[3] > 0), e2e: pct((r) => r[4] > 0) };
}
const CHARGE = percentsOf(CHARGE_LINES);
const CHARGE_CHANGED = CHARGE_LINES.filter((r) => r[6]);

/** main's head, file by file: [path, statements, combined, unit, integration, e2e] (null: the lane does not reach that code). */
const FILES: [string, number, number, number | null, number | null, number | null][] = [
  ["cmd/api/main.go", 96, 40.6, null, 40.6, 38.5],
  ["cmd/inventory/main.go", 64, 37.5, null, 37.5, 34.4],
  ["cmd/worker/main.go", 71, 39.4, null, 39.4, 35.2],
  ["internal/checkout/checkout.go", 1_262, 84.1, 76.2, 52.0, 61.3],
  ["internal/fulfilment/fulfil.go", 948, 77.3, 69.8, 58.2, 33.0],
  ["internal/inventory/reserve.go", 806, 82.6, 75.1, 57.9, 41.4],
  ["internal/inventory/stock.go", 600, 77.5, 69.0, 51.3, 38.6],
  [SHOP_CHARGE_PATH, CHARGE.statements, CHARGE.combined, CHARGE.unit, CHARGE.integration, CHARGE.e2e],
  ["internal/orders/events.go", 640, 83.3, 75.8, 50.0, 55.9],
  ["internal/orders/orders.go", 1_212, 87.1, 81.0, 52.3, 60.4],
  ["internal/payments/client.go", 702, 61.8, 55.3, 22.9, 30.1],
  ["web/src/catalog/ProductPage.tsx", 1_920, 66.9, 61.2, null, 51.8],
  ["web/src/catalog/Search.tsx", 1_468, 61.8, 55.9, null, 45.4],
  ["web/src/checkout/CheckoutForm.tsx", 1_775, 72.3, 63.0, null, 59.6],
  ["web/src/checkout/Confirmation.tsx", 1_140, 66.9, 58.8, null, 53.9],
];

const parentOf = (path: string) => (path.includes("/") ? path.slice(0, path.lastIndexOf("/")) : "");
const langOf = (path: string) => (path.endsWith(".go") ? "go" : "ts");
const round1 = (value: number) => Math.round(value * 10) / 10;
const clamp = (value: number) => Math.min(100, Math.max(0, round1(value)));

/** Files summed: statements, and each figure weighted by statements (a lane no file has is absent). */
function summed(path: string, kind: number, lang: string, files: readonly CoverageFixtureNode[]): CoverageFixtureNode {
  const statements = files.reduce((sum, f) => sum + f.statements, 0);
  const weighted = (pick: (f: CoverageFixtureNode) => number | undefined) => round1(files.reduce((sum, f) => sum + f.statements * (pick(f) ?? 0), 0) / statements);
  const lanes: Partial<Record<string, number>> = {};
  for (const lane of LANES) if (files.some((f) => f.lanes[lane] !== undefined)) lanes[lane] = weighted((f) => f.lanes[lane]);
  return { path, kind, lang, statements, combined: weighted((f) => f.combined), lanes };
}

/** The report of a tested commit: main's head's files, moved by the commit's offset; charge.go's changed lines only at the head. */
function reportFixture(sha: string): CoverageReportFixture {
  const { offset } = FIGURES[sha]!;
  const atHead = sha === HEAD.sha;
  const files: CoverageFixtureNode[] = FILES.map(([path, statements, combined, unit, integration, e2e]) => {
    const lanes: Partial<Record<string, number>> = {};
    if (unit !== null) lanes.unit = clamp(unit + offset);
    if (integration !== null) lanes.integration = clamp(integration + offset);
    if (e2e !== null) lanes.e2e = clamp(e2e + offset);
    const changed: [number, number] | undefined = atHead && path === SHOP_CHARGE_PATH ? [CHARGE_CHANGED.length, CHARGE_CHANGED.filter((r) => r[2] + r[3] + r[4] > 0).length] : undefined;
    return { path, kind: WireNodeKind.FILE, lang: langOf(path), statements, combined: clamp(combined + offset), lanes, changed };
  });
  const dirs = [...new Set(files.map((f) => parentOf(f.path)))];
  const packages = dirs.map((dir) => {
    const inDir = files.filter((f) => parentOf(f.path) === dir);
    const node = summed(dir, inDir[0]!.lang === "go" ? WireNodeKind.PACKAGE : WireNodeKind.DIRECTORY, inDir[0]!.lang, inDir);
    const changed = inDir.find((f) => f.changed)?.changed;
    return changed ? { ...node, changed } : node;
  });
  const root = summed("", WireNodeKind.DIRECTORY, "", files);
  const rootChanged = files.find((f) => f.changed)?.changed;
  return {
    gitCommit: sha,
    treeHash: hexOf(`tree:${sha}`, 40),
    lanes: [...LANES],
    root: rootChanged ? { ...root, changed: rootChanged } : root,
    nodes: [...packages, ...files],
    changedSince: BASE_OF_FIX,
    base: (baseSha) => {
      const base = SHOP_MAIN_COMMITS.find((c) => c.sha === baseSha);
      return base ? { hasReport: true, deltaPercent: round1(combinedOf(sha) - combinedOf(base.sha)) } : { hasReport: false };
    },
    importPath: (path) => (langOf(path) === "go" ? `${DEMO_REPO}/${parentOf(path)}` : ""),
    // A file's blob at that commit: charge.go's served at the head; every other blob is the repository's, not the demo's.
    blobId: (path) => (atHead && path === SHOP_CHARGE_PATH ? SHOP_CHARGE_BLOB : hexOf(`blob:${sha}:${path}`, 40)),
    lines: (path) => (atHead && path === SHOP_CHARGE_PATH ? CHARGE_LINES : GENERIC_COVERAGE_LINES),
  };
}

const reports = new Map<string, CoverageReportFixture>();
function report(sha: string): CoverageReportFixture | null {
  if (!FIGURES[sha] || sha === SHOP_BRANCH_COMMIT) return null;
  if (!reports.has(sha)) reports.set(sha, reportFixture(sha));
  return reports.get(sha)!;
}
const combinedOf = (sha: string) => report(sha)!.root.combined;
const laneOf = (sha: string, lane: Lane) => report(sha)!.root.lanes[lane] ?? 0;

/** The commit a coverage target reads: a tested commit (by prefix), a release's commit, or an environment's running release. */
function targetCommit(target: CoverageTarget): string | null {
  if ("gitCommit" in target) return SHOP_MAIN_COMMITS.find((c) => target.gitCommit.length >= 7 && c.sha.startsWith(target.gitCommit))?.sha ?? null;
  if ("releaseVersion" in target) return releaseCommit(target.releaseVersion);
  const running = RUNNING[target.environmentId as DemoEnvKey];
  return running ? releaseCommit(running.version) : null;
}

const coverage = coverageFixtureEngine((target) => {
  const sha = targetCommit(target);
  return sha ? report(sha) : null;
});

function stmts(statements: number, percent: number): WireStatementCoverage {
  return { statements: BigInt(statements), covered: BigInt(Math.round((statements * percent) / 100)), percent };
}

// ── Runs ────────────────────────────────────────────────────────────────────

/** When each lane finished after its commit was pushed, and how long it ran. */
const LANE_TIMING: Record<Lane, { after: number; took: number }> = {
  unit: { after: 6 * MINUTE, took: 3 * MINUTE + 40_000 },
  integration: { after: 14 * MINUTE, took: 8 * MINUTE + 50_000 },
  e2e: { after: 24 * MINUTE, took: 7 * MINUTE + 10_000 },
};

/** The flake's race: which unit runs retried it (v1.8.3 onwards, every one). */
const FLAKED_UNIT = new Set([BASE_OF_FIX, SHOP_BRANCH_COMMIT, HEAD.sha]);
/** The quarantined test's two events, in integration runs on main. */
const REDELIVERY_EVENTS = { sameTree: releaseCommit("v1.8.1")!, retry: releaseCommit("v1.8.2")! };

interface RunSpec {
  commit: ShopCommit;
  lane: Lane;
  branch: string;
  kind: "ci" | "env";
}

function totalsOf(spec: RunSpec) {
  const ran = FIGURES[spec.commit.sha]!.tests[spec.lane];
  const failed = spec.commit.sha === SHOP_BRANCH_COMMIT && spec.lane === "unit" ? 1 : 0;
  const retried =
    (spec.lane === "unit" && FLAKED_UNIT.has(spec.commit.sha)) || (spec.lane === "integration" && spec.commit.sha === REDELIVERY_EVENTS.retry) ? 1 : 0;
  return { passed: BigInt(ran - failed), failed: BigInt(failed), skipped: BigInt(SKIPPED[spec.lane]), retryPassed: BigInt(retried) };
}

const ts = (ms: number): Timestamp => timestampFromMs(ms);

/** Every run, newest first, as of `now`. */
function runSpecs(): RunSpec[] {
  const main = SHOP_MAIN_COMMITS.flatMap((commit) => LANES.map((lane): RunSpec => ({ commit, lane, branch: "main", kind: "ci" })));
  return [
    ...main,
    // maya's fix branch: only the unit lane ran before the fix commit replaced it.
    { commit: BRANCH, lane: "unit", branch: SHOP_FIX_BRANCH, kind: "ci" },
    // jon's e2e against staging, after v1.8.3 reached it.
    { commit: commitOf(BASE_OF_FIX)!, lane: "e2e", branch: "main", kind: "env" },
  ];
}

/** When the run finished, before now. */
function finishedAgo(spec: RunSpec): number {
  if (spec.kind === "env") return RUNNING["env-staging"].age - 12 * MINUTE;
  return spec.commit.ago - LANE_TIMING[spec.lane].after;
}

/** The GitHub Actions run number: one per pushed commit, in push order. */
function ciRunNumber(spec: RunSpec): number {
  const order = ALL_COMMITS.findIndex((c) => c.sha === spec.commit.sha);
  return 1830 + order * 3 + LANES.indexOf(spec.lane);
}

function runOfSpec(spec: RunSpec, now: number, app: string = DEMO_PROJECT): WireRunSummary {
  const ago = finishedAgo(spec);
  const totals = totalsOf(spec);
  const failed = totals.failed > 0n;
  const env = spec.kind === "env";
  const ci = env ? undefined : String(ciRunNumber(spec));
  return {
    runId: runIdOf(`${spec.kind}:${spec.branch}:${spec.commit.sha}:${spec.lane}`),
    project: app,
    gitCommit: spec.commit.sha,
    treeHash: hexOf(`tree:${spec.commit.sha}`, 40),
    dirty: false,
    branch: spec.branch,
    baseSha: spec.branch === "main" ? "" : BASE_OF_FIX,
    lane: spec.lane,
    source: env ? WireRunSource.ENV : WireRunSource.CI,
    environmentId: env ? "env-staging" : "",
    ci: ci ? { provider: "github-actions", runId: ci, runUrl: `${REPO_URL}/actions/runs/${ci}`, attempt: 1 } : undefined,
    startedAt: ts(now - ago - LANE_TIMING[spec.lane].took),
    finishedAt: ts(now - ago),
    exitCode: failed ? 1 : 0,
    outcome: failed ? WireRunOutcome.FAILED : WireRunOutcome.PASSED,
    ingestState: WireIngestState.INGESTED,
    ingestError: "",
    totals,
    // The branch's commit has no merged report; its own lane's figure still came with the run.
    coveragePercent: report(spec.commit.sha) ? laneOf(spec.commit.sha, spec.lane) : round1(laneOf(BASE_OF_FIX, spec.lane) + 0.2),
    forgeVersion: FORGE_VERSION,
    uploadedBy: env ? RUNNING["env-staging"].by : DEMO_CI_ACTOR,
    createdAt: ts(now - ago),
  };
}

/** Every run recorded for shop, newest first. */
export function shopRuns(now: number = demoNow(), app: string = DEMO_PROJECT): WireRunSummary[] {
  return runSpecs()
    .map((spec) => ({ spec, ago: finishedAgo(spec) }))
    .sort((a, b) => a.ago - b.ago)
    .map(({ spec }) => runOfSpec(spec, now, app));
}
const specOfRun = (runId: string) => runSpecs().find((spec) => runIdOf(`${spec.kind}:${spec.branch}:${spec.commit.sha}:${spec.lane}`) === runId) ?? null;

/** A commit's merged report: its three lanes on main. */
function commitReport(sha: string, now: number, withDelta: boolean): WireCommitReport {
  const r = report(sha)!;
  const specs = LANES.map((lane): RunSpec => ({ commit: commitOf(sha)!, lane, branch: "main", kind: "ci" }));
  const sum = (pick: (t: ReturnType<typeof totalsOf>) => bigint) => specs.reduce((acc, spec) => acc + pick(totalsOf(spec)), 0n);
  const previous = SHOP_MAIN_COMMITS[SHOP_MAIN_COMMITS.findIndex((c) => c.sha === sha) - 1];
  const wire: WireCoverage = {
    granularity: "statement",
    combined: stmts(r.root.statements, r.root.combined),
    lanes: Object.fromEntries(LANES.map((lane) => [lane, stmts(r.root.statements, laneOf(sha, lane))])),
    onlyE2e: stmts(r.root.statements, 2.4),
    languages: [],
    deltaPercent: withDelta && previous ? round1(r.root.combined - combinedOf(previous.sha)) : undefined,
    baseHasReport: withDelta && !!previous,
    excludedGeneratedStatements: 6_840n,
  };
  return {
    gitCommit: sha,
    treeHash: r.treeHash,
    lanes: [...LANES],
    runCount: LANES.length,
    totals: { passed: sum((t) => t.passed), failed: sum((t) => t.failed), skipped: sum((t) => t.skipped), retryPassed: sum((t) => t.retryPassed) },
    coverage: wire,
    reportDigest: `sha256:${hexOf(`report:${sha}`, 64)}`,
    updatedAt: ts(now - (commitOf(sha)!.ago - LANE_TIMING.e2e.after)),
  };
}

// ── Tests and flakes ────────────────────────────────────────────────────────

/** A Go test's key: `go:<import path>:<name>`, in `<dir>/<file>_test.go`. */
function testKey(dir: string, file: string, name: string, line: number): WireTestKey {
  const pkg = `${DEMO_REPO}/${dir}`;
  return { id: `go:${pkg}:${name}`, lang: "go", package: pkg, name, file: `${dir}/${file}_test.go`, line };
}

export const SHOP_TESTS = {
  retryOnTimeout: testKey("internal/orders", "charge", "TestCreateOrder_RetryOnTimeout", 41),
  slowProvider: testKey("internal/orders", "charge", "TestCreateOrder_SlowPaymentProvider", 82),
  redelivery: testKey("internal/fulfilment", "fulfil", "TestRedeliveryAfterNak", 112),
  confirmation: testKey("internal/checkout", "checkout", "TestCheckout_SendsConfirmation", 203),
  checkoutE2e: testKey("internal/checkout", "checkout", "TestCheckout_EndToEnd", 61),
  reserve: testKey("internal/inventory", "reserve", "TestReserve_FromOrderEvent", 37),
  ordersList: testKey("internal/orders", "orders", "TestListOrders_Paging", 140),
} as const;

const SLOW_PROVIDER_OUTPUT = `=== RUN   TestCreateOrder_SlowPaymentProvider
    charge_test.go:88: CreateOrder with the provider answering in 1.6s took 3.02s, want under the order's 3s deadline
    charge_test.go:91: order ord_test_1: state = payment_failed, want paid
--- FAIL: TestCreateOrder_SlowPaymentProvider (3.02s)`;

const RETRY_RACE_OUTPUT = `=== RUN   TestCreateOrder_RetryOnTimeout
    charge_test.go:57: charge attempts = 1, want 2: the fake provider's 1.5s answer raced the 1.5s attempt timeout
--- FAIL: TestCreateOrder_RetryOnTimeout (1.51s)`;

const REDELIVERY_OUTPUT = `=== RUN   TestRedeliveryAfterNak
    fulfil_test.go:131: order ord_test_7: redelivered after 0s, want after the 2s NAK delay
--- FAIL: TestRedeliveryAfterNak (2.04s)`;

const CONFIRMATION_OUTPUT = `=== RUN   TestCheckout_SendsConfirmation
    checkout_test.go:219: confirmation mails = 0, want 1 (the outbox drained after the assertion)
--- FAIL: TestCheckout_SendsConfirmation (0.31s)`;

const SIG = { retryRace: "sha256:9b1e40c2d7a3", redelivery: "sha256:5f02aa61c9e8", confirmation: "sha256:c3d8e1b04f77" } as const;

const unitRun = (sha: string, branch = "main") => runIdOf(`ci:${branch}:${sha}:unit`);
const integrationRun = (sha: string) => runIdOf(`ci:main:${sha}:integration`);
const runAgo = (sha: string, lane: Lane, kind: "ci" | "env" = "ci") => finishedAgo({ commit: commitOf(sha)!, lane, branch: "main", kind });

function flakes(now: number): WireFlake[] {
  const retryEvents = [...FLAKED_UNIT].map((sha) => runAgo(sha, "unit"));
  return [
    {
      test: SHOP_TESTS.retryOnTimeout,
      state: WireFlakeState.FLAKY,
      // Flaked on both main commits it ran on (a Wilson lower bound over two, so it ranks, not condemns).
      score: 0.342,
      scoreRuns: 2n,
      confirmedEvents30d: FLAKED_UNIT.size,
      signals: [WireFlakeSignal.RETRY_PASS],
      firstSeenAt: ts(now - Math.max(...retryEvents)),
      lastEventAt: ts(now - Math.min(...retryEvents)),
      issueUrl: `${REPO_URL}/issues/142`,
    },
    {
      test: SHOP_TESTS.redelivery,
      state: WireFlakeState.QUARANTINED,
      score: 0.082,
      scoreRuns: 7n,
      confirmedEvents30d: 2,
      signals: [WireFlakeSignal.SAME_TREE, WireFlakeSignal.RETRY_PASS],
      firstSeenAt: ts(now - runAgo(REDELIVERY_EVENTS.sameTree, "integration")),
      lastEventAt: ts(now - runAgo(REDELIVERY_EVENTS.retry, "integration")),
      quarantinedAt: ts(now - 5.5 * DAY),
      quarantine: {
        path: "ci/quarantine/go/internal/fulfilment/TestRedeliveryAfterNak.yaml",
        issueUrl: `${REPO_URL}/issues/131`,
        reason: "NATS redelivery timing under -race: rewrite it on a fake clock (maya)",
        signature: SIG.redelivery,
        expiresAt: ts(now + 8.5 * DAY),
        overdue: false,
      },
      issueUrl: `${REPO_URL}/issues/131`,
    },
    {
      test: SHOP_TESTS.confirmation,
      state: WireFlakeState.SUSPECTED,
      score: 0.026,
      scoreRuns: 7n,
      confirmedEvents30d: 1,
      signals: [WireFlakeSignal.SAME_TREE],
      firstSeenAt: ts(now - runAgo(releaseCommit("v1.8.2")!, "integration")),
      lastEventAt: ts(now - runAgo(releaseCommit("v1.8.2")!, "integration")),
      issueUrl: "",
    },
  ];
}

function event(signal: number, sha: string, runIds: string[], atMs: number, failureSignature: string): WireFlakeEvent {
  return { signal, verdict: WireFlakeVerdict.CONFIRMED, gitCommit: sha, runIds, failureSignature, at: ts(atMs), infraGroup: "" };
}

/** A test's outcomes in its lane's runs (newest first), with the flaked ones marked. */
function outcomesOf(lane: Lane, since: number, flaked: ReadonlySet<string>, now: number, durationMs: number): WireOutcome[] {
  return shopRuns(now)
    .filter((r) => r.lane === lane && r.source === WireRunSource.CI && runAgo(r.gitCommit, lane) <= since)
    .map((r) => ({
      runId: r.runId,
      gitCommit: r.gitCommit,
      branch: r.branch,
      lane,
      status: WireCaseStatus.PASS,
      attempts: flaked.has(r.gitCommit) ? 2 : 1,
      retryPassed: flaked.has(r.gitCommit),
      durationMs: BigInt(durationMs + (parseInt(r.gitCommit.slice(0, 4), 16) % 300)),
      at: r.finishedAt,
    }));
}

function testDetail(testId: string, now: number): WireTestResponse {
  const flake = flakes(now).find((f) => f.test!.id === testId);
  const at = (sha: string, lane: Lane) => now - runAgo(sha, lane);
  switch (testId) {
    case SHOP_TESTS.retryOnTimeout.id: {
      const since = runAgo(BASE_OF_FIX, "unit");
      const events = [...FLAKED_UNIT].map((sha) =>
        event(WireFlakeSignal.RETRY_PASS, sha, [unitRun(sha, sha === SHOP_BRANCH_COMMIT ? SHOP_FIX_BRANCH : "main")], at(sha, "unit"), SIG.retryRace)
      );
      return {
        flake,
        outcomes: outcomesOf("unit", since, FLAKED_UNIT, now, 1_520),
        events: events.sort((a, b) => Number(b.at!.seconds - a.at!.seconds)),
        signatures: [{ signature: SIG.retryRace, sampleOutput: RETRY_RACE_OUTPUT, count: FLAKED_UNIT.size, verdict: WireFlakeVerdict.CONFIRMED, firstSeenAt: ts(at(BASE_OF_FIX, "unit")), lastSeenAt: ts(at(HEAD.sha, "unit")) }],
      };
    }
    case SHOP_TESTS.redelivery.id: {
      const { sameTree, retry } = REDELIVERY_EVENTS;
      return {
        flake,
        outcomes: outcomesOf("integration", Infinity, new Set([retry]), now, 2_040),
        events: [
          event(WireFlakeSignal.RETRY_PASS, retry, [integrationRun(retry)], at(retry, "integration"), SIG.redelivery),
          event(WireFlakeSignal.SAME_TREE, sameTree, [integrationRun(sameTree), runIdOf(`rerun:${sameTree}:integration`)], at(sameTree, "integration"), SIG.redelivery),
        ],
        signatures: [{ signature: SIG.redelivery, sampleOutput: REDELIVERY_OUTPUT, count: 2, verdict: WireFlakeVerdict.CONFIRMED, firstSeenAt: ts(at(sameTree, "integration")), lastSeenAt: ts(at(retry, "integration")) }],
      };
    }
    case SHOP_TESTS.confirmation.id: {
      const sha = releaseCommit("v1.8.2")!;
      return {
        flake,
        outcomes: outcomesOf("integration", Infinity, new Set(), now, 310),
        events: [event(WireFlakeSignal.SAME_TREE, sha, [integrationRun(sha), runIdOf(`rerun:${sha}:integration`)], at(sha, "integration"), SIG.confirmation)],
        signatures: [{ signature: SIG.confirmation, sampleOutput: CONFIRMATION_OUTPUT, count: 1, verdict: WireFlakeVerdict.CONFIRMED, firstSeenAt: ts(at(sha, "integration")), lastSeenAt: ts(at(sha, "integration")) }],
      };
    }
  }
  // A test the runs name that never flaked: healthy, with its outcomes.
  const named = Object.values(SHOP_TESTS).find((key) => key.id === testId);
  if (!named) throw new ConnectError(`no test ${testId}`, Code.NotFound);
  const lane: Lane = named === SHOP_TESTS.checkoutE2e ? "e2e" : "unit";
  const since = named === SHOP_TESTS.slowProvider ? runAgo(HEAD.sha, "unit") : Infinity;
  return {
    flake: { test: named, state: WireFlakeState.HEALTHY, score: 0, scoreRuns: named === SHOP_TESTS.slowProvider ? 1n : 7n, confirmedEvents30d: 0, signals: [], issueUrl: "" },
    outcomes: outcomesOf(lane, since, new Set(), now, named === SHOP_TESTS.checkoutE2e ? 12_400 : 180),
    events: [],
    signatures: [],
  };
}

function runDetail(runId: string, now: number, app: string): WireRunResponse {
  const spec = specOfRun(runId);
  if (!spec) throw new ConnectError(`no run ${runId}`, Code.NotFound);
  const run = runOfSpec(spec, now, app);
  const slowest: Record<Lane, WireTiming[]> = {
    unit: [
      { test: SHOP_TESTS.slowProvider, durationMs: 3_020n },
      { test: SHOP_TESTS.retryOnTimeout, durationMs: 1_510n },
      { test: SHOP_TESTS.ordersList, durationMs: 640n },
    ],
    integration: [
      { test: SHOP_TESTS.redelivery, durationMs: 2_040n },
      { test: SHOP_TESTS.reserve, durationMs: 1_380n },
      { test: SHOP_TESTS.confirmation, durationMs: 310n },
    ],
    e2e: [{ test: SHOP_TESTS.checkoutE2e, durationMs: 12_400n }],
  };
  const sha = spec.commit.sha;
  // The slow-provider test exists from v1.8.4's test commit on.
  const has = (key: WireTestKey) => key !== SHOP_TESTS.slowProvider || sha === SHOP_BRANCH_COMMIT || sha === HEAD.sha;
  const hasRetryTest = SHOP_MAIN_COMMITS.findIndex((c) => c.sha === sha) >= SHOP_MAIN_COMMITS.findIndex((c) => c.sha === BASE_OF_FIX) || sha === SHOP_BRANCH_COMMIT;
  const failures: WireFailure[] =
    sha === SHOP_BRANCH_COMMIT && spec.lane === "unit"
      ? [{ test: SHOP_TESTS.slowProvider, output: SLOW_PROVIDER_OUTPUT, file: SHOP_TESTS.slowProvider.file, line: 88, failureSignature: "sha256:2e7f9a0d41c6", attempts: 2, verdict: WireFlakeVerdict.REAL }]
      : [];
  const retryPasses =
    spec.lane === "unit" && FLAKED_UNIT.has(sha) ? [SHOP_TESTS.retryOnTimeout] : spec.lane === "integration" && sha === REDELIVERY_EVENTS.retry ? [SHOP_TESTS.redelivery] : [];
  return {
    run,
    failures,
    retryPasses,
    slowest: slowest[spec.lane].filter((t) => has(t.test!) && (t.test !== SHOP_TESTS.retryOnTimeout || hasRetryTest)),
    zeroEvidencePackages: spec.lane === "unit" ? ["cmd/api", "cmd/inventory", "cmd/worker"] : [],
    packageCount: spec.lane === "unit" ? 10 : spec.lane === "integration" ? 7 : 2,
  };
}

// ── Summary and releases ────────────────────────────────────────────────────

function summary(now: number, environmentId: string | undefined, app: string): WireSummaryResponse {
  const all = shopRuns(now, app);
  const list = flakes(now);
  const count = (state: number) => list.filter((f) => f.state === state).length;
  const flakeCounts = { suspectedCount: count(WireFlakeState.SUSPECTED), flakyCount: count(WireFlakeState.FLAKY), quarantinedCount: count(WireFlakeState.QUARANTINED) };
  if (environmentId) {
    // Only runs recorded against that environment; its report is the one of the release it runs.
    const runs = all.filter((r) => r.environmentId === environmentId);
    const running = RUNNING[environmentId as DemoEnvKey];
    return {
      branch: "main",
      latest: running ? commitReport(releaseCommit(running.version)!, now, true) : undefined,
      coverageTrend: [],
      releaseMarkers: [],
      passRate: runs.length ? runs.filter((r) => r.outcome === WireRunOutcome.PASSED).length / runs.length : 0,
      passRateRuns: runs.length,
      ...flakeCounts,
      lastRun: runs[0],
      lowCoveragePackages: [],
      hostFailures30d: 0,
    };
  }
  const main = all.filter((r) => r.branch === "main" && r.source === WireRunSource.CI);
  const head = report(HEAD.sha)!;
  const low = head.nodes.filter((n) => n.kind !== WireNodeKind.FILE && n.statements > 500).sort((a, b) => a.combined - b.combined).slice(0, 2);
  const previous = SHOP_MAIN_COMMITS.at(-2)!;
  return {
    branch: "main",
    latest: commitReport(HEAD.sha, now, true),
    coverageTrend: SHOP_MAIN_COMMITS.map((c) => ({
      gitCommit: c.sha,
      at: ts(now - (c.ago - LANE_TIMING.e2e.after)),
      combinedPercent: combinedOf(c.sha),
      lanesPercent: Object.fromEntries(LANES.map((lane) => [lane, laneOf(c.sha, lane)])),
    })),
    releaseMarkers: [...DEMO_RELEASES].reverse().map((r) => ({ releaseVersion: r.version, gitCommit: r.commit, cutAt: ts(now - r.age) })),
    passRate: main.filter((r) => r.outcome === WireRunOutcome.PASSED).length / main.length,
    passRateRuns: main.length,
    ...flakeCounts,
    lastRun: main[0],
    lowCoveragePackages: low.map((n) => {
      const before = report(previous.sha)!.nodes.find((m) => m.path === n.path)!;
      return {
        path: n.path,
        lang: n.lang,
        combined: stmts(n.statements, n.combined),
        lanes: Object.fromEntries(Object.entries(n.lanes).map(([lane, pct]) => [lane, stmts(n.statements, pct!)])),
        deltaPercent: round1(n.combined - before.combined),
      };
    }),
    hostFailures30d: 0,
  };
}

/** GetReleaseTests: every release's head commit has its three-lane report; v1.8.1 is the first, with nothing to compare. */
function releaseTests(version: string, now: number): WireReleaseTestsResponse {
  if (!version) throw new ConnectError("a release version is required", Code.InvalidArgument);
  const index = DEMO_RELEASES.findIndex((r) => r.version === version);
  const r = DEMO_RELEASES[index];
  if (!r) {
    return { releaseId: `rel-${version}`, releaseVersion: version, gitCommit: "", status: WireEvidenceStatus.UNAVAILABLE, reason: "No test report for this commit.", previousReleaseVersion: "", newFailures: [], newFlakes: [] };
  }
  const previous = DEMO_RELEASES[index + 1];
  const list = flakes(now);
  // A flake is new in the release whose commits first flaked it.
  const newFlakes =
    version === "v1.8.3" ? list.filter((f) => f.test === SHOP_TESTS.retryOnTimeout) : version === "v1.8.2" ? list.filter((f) => f.test === SHOP_TESTS.confirmation) : [];
  const commitReportOf = commitReport(r.commit, now, false);
  return {
    releaseId: `rel-${r.version}`,
    releaseVersion: r.version,
    gitCommit: r.commit,
    status: WireEvidenceStatus.OK,
    reason: "",
    report: commitReportOf,
    previousReleaseVersion: previous?.version ?? "",
    coverageDeltaPercent: previous ? round1(combinedOf(r.commit) - combinedOf(previous.commit)) : undefined,
    newFailures: [],
    newFlakes,
  };
}

function pageOf<T>(rows: T[], req: { pageSize?: number; pageToken?: string }, size: number): { rows: T[]; next: string } {
  const start = req.pageToken ? Number(req.pageToken) : 0;
  if (!Number.isInteger(start) || start < 0) throw new ConnectError("page token from another request", Code.InvalidArgument);
  const n = req.pageSize || size;
  return { rows: rows.slice(start, start + n), next: start + n < rows.length ? String(start + n) : "" };
}

function runsPage(req: RunsRequest, now: number, app: string): WireListRunsResponse {
  const matching = shopRuns(now, app).filter((r) => runMatchesFilters(r, req.filters, releaseCommit));
  const page = pageOf(matching, req, 10);
  return { runs: page.rows, nextPageToken: page.next, totalCount: BigInt(matching.length) };
}

const STATES = { healthy: WireFlakeState.HEALTHY, suspected: WireFlakeState.SUSPECTED, flaky: WireFlakeState.FLAKY, quarantined: WireFlakeState.QUARANTINED, fixed: WireFlakeState.FIXED, removed: WireFlakeState.REMOVED } as const;

function flakyPage(req: FlakyRequest, now: number): WireListFlakyResponse {
  const { state, minScore, package: pkg } = req.filters;
  const rows = flakes(now).filter(
    (f) =>
      (state ? f.state === STATES[state] : f.state !== WireFlakeState.HEALTHY) &&
      (minScore === undefined || f.score >= minScore) &&
      (!pkg || f.test!.package === pkg || f.test!.package.endsWith(`/${pkg}`))
  );
  const page = pageOf(rows, req, 50);
  return { tests: page.rows, nextPageToken: page.next };
}

/** TestsService's reads over shop's story, request by request, as WIRE messages; a refusal (NotFound, InvalidArgument) is a thrown ConnectError. */
export interface ShopTestsAnswers {
  getTestSummary(req: Pick<SummaryRequest, "environmentId">): WireSummaryResponse;
  listTestRuns(req: RunsRequest): WireListRunsResponse;
  getTestRun(req: Pick<RunRequest, "runId">): WireRunResponse;
  getCoverage(req: CoverageRequest): WireCoverageResponse;
  getFileCoverage(req: FileCoverageRequest): WireFileCoverageResponse;
  getReleaseTests(req: Pick<ReleaseTestsRequest, "version">): WireReleaseTestsResponse;
  listFlakyTests(req: FlakyRequest): WireListFlakyResponse;
  getTest(req: Pick<TestRequest, "testId">): WireTestResponse;
  /** Each named project's row (only the story's app has runs), and the totals over them; none named: the app with runs. */
  getTestPortfolio(req: { projects: readonly string[] }): WirePortfolioResponse;
}

/**
 * main's head report per language (Go and TypeScript), summed over its files
 * as the server splits a report's root: covered statements over statements,
 * never an average of the files' percentages.
 */
function headLanguages(): WirePortfolioCoverage[] {
  const byLang = new Map<string, { statements: number; covered: number }>();
  for (const node of report(HEAD.sha)!.nodes) {
    if (node.kind !== WireNodeKind.FILE) continue;
    const acc = byLang.get(node.lang) ?? { statements: 0, covered: 0 };
    acc.statements += node.statements;
    acc.covered += Math.round((node.statements * node.combined) / 100);
    byLang.set(node.lang, acc);
  }
  return [...byLang.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([lang, c]) => ({ lang, granularity: "statement", combined: { statements: BigInt(c.statements), covered: BigInt(c.covered), percent: round1((c.covered / c.statements) * 100) }, projects: 1 }));
}

/** shop's runs, coverage and flakes, measured from the page load (handlers/tests.ts serves them). */
export function shopTestsAnswers(now: number = demoNow(), app: string = DEMO_PROJECT): ShopTestsAnswers {
  return {
    getTestSummary: (req) => summary(now, req.environmentId, app),
    listTestRuns: (req) => runsPage(req, now, app),
    getTestRun: (req) => runDetail(req.runId, now, app),
    getCoverage: (req) => coverage.coverageResponse(req),
    getFileCoverage: (req) => coverage.fileCoverageResponse(req),
    getReleaseTests: (req) => releaseTests(req.version, now),
    listFlakyTests: (req) => flakyPage(req, now),
    getTest: (req) => testDetail(req.testId, now),
    getTestPortfolio: (req) =>
      portfolioWire(
        (req.projects.length ? [...new Set(req.projects)] : [app]).map((project) =>
          project === app ? portfolioRowWire(project, summary(now, undefined, app), headLanguages()) : portfolioRowWire(project, undefined)
        )
      ),
  };
}
