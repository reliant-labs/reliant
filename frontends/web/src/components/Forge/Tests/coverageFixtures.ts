// Copyright (c) 2026 Reliant Labs

/**
 * The Coverage page's fixtures: shopfront's report at commit 9f3c… (the same
 * report as fixtures.ts's FIXTURE_REPORT) as WIRE messages, so they map
 * through wire.ts exactly as the real client's will; and the source of the
 * one file whose blob the fixture GitHub serves.
 *
 *   the tree      six top-level nodes (five Go packages, one TypeScript
 *                 directory); internal/billing holds three files
 *   refund.go     12 statements, every state: covered, uncovered, partial,
 *                 and (with a base) changed lines. Its blob is served.
 *   invoice.go    run with uncommitted changes: no blob id at all
 *   tax.go        a blob GitHub no longer has
 *
 * The fixture source fetcher (`fixtureCoverageSource`) answers the page's
 * CoverageSourceContext; its scenario picks what GitHub says for EVERY file,
 * so each no-source state can be shown on refund.go. `?source=<scenario>` in
 * the preview (e2e/visual/fixtures/tests.ts).
 */

import { Code, ConnectError } from "@connectrpc/connect";

import type { CoverageSource, FetchCoverageSource } from "@/services/coverageSource/coverageSource";
import type { CoverageRequest, CoverageTree, FileCoverage, FileCoverageRequest } from "@/services/controlPlane/tests/types";
import {
  coverageTreeOf,
  fileCoverageOf,
  WireNodeKind,
  type WireCoverage,
  type WireCoverageNode,
  type WireCoverageResponse,
  type WireDiffCoverage,
  type WireFileCoverageResponse,
  type WireLineCoverage,
  type WireStatementCoverage,
} from "@/services/controlPlane/tests/wire";

const COMMIT = "9f3c2a17be0d4c55a1f0e9d8c7b6a5f4e3d2c1b0";
const TREE = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678";
const LANES = ["unit", "integration", "e2e"];

/** The base the fixture's diff is taken against: main's commit before 9f3c… on the summary's trend (a test holds it there). */
export const FIXTURE_BASE = "aa9ce0c1d2a3b4c5d6e7f8091a2b3c4d5e6f7081";

/** internal/billing/refund.go as the tests ran on it. Its git blob id is FIXTURE_REFUND_BLOB (a test holds the two equal). */
export const FIXTURE_REFUND_SOURCE = [
  "// Copyright (c) 2026 Acme Shopfront",
  "",
  "package billing",
  "",
  "import (",
  '\t"context"',
  '\t"errors"',
  '\t"fmt"',
  ")",
  "",
  "// ErrNothingToRefund is returned for an order that was never paid.",
  'var ErrNothingToRefund = errors.New("billing: nothing to refund")',
  "",
  "// Refund returns an order's payment, in full or in part.",
  "func Refund(ctx context.Context, o Order) error {",
  "\tif o.Paid == 0 {",
  "\t\treturn ErrNothingToRefund",
  "\t}",
  "\tif o.Partial && o.Remaining() < o.Paid {",
  "\t\treturn refundPartial(ctx, o, o.Paid-o.Remaining())",
  "\t}",
  "\tif err := o.Gateway.Refund(ctx, o.PaymentID, o.Paid); err != nil {",
  '\t\treturn fmt.Errorf("billing: refund %s: %w", o.ID, err)',
  "\t}",
  "\to.Paid = 0",
  "\treturn nil",
  "}",
  "",
  "// refundPartial returns part of a payment and keeps the rest on the order.",
  "func refundPartial(ctx context.Context, o Order, amount int64) error {",
  "\tif err := o.Gateway.Refund(ctx, o.PaymentID, amount); err != nil {",
  '\t\treturn fmt.Errorf("billing: partial refund %s: %w", o.ID, err)',
  "\t}",
  "\to.Paid -= amount",
  "\treturn nil",
  "}",
  "",
].join("\n");

/** `git hash-object` of FIXTURE_REFUND_SOURCE. */
export const FIXTURE_REFUND_BLOB = "1d099b1bd69498b1fec4197d9f24e37e997c7393";
const TAX_BLOB = "3c1f0e9d8c7b6a5f4e3d2c1b0a9f8e7d6c5b4a39";

export const FIXTURE_REFUND_PATH = "internal/billing/refund.go";
export const FIXTURE_INVOICE_PATH = "internal/billing/invoice.go";
export const FIXTURE_TAX_PATH = "internal/billing/tax.go";

/** Statement coverage of `statements` at `percent`. */
function cov(statements: number, percent: number): WireStatementCoverage {
  const covered = Math.round((statements * percent) / 100);
  return { statements: BigInt(statements), covered: BigInt(covered), percent: statements === 0 ? 0 : Math.round((covered / statements) * 1000) / 10 };
}

function diff(lines: number, covered: number): WireDiffCoverage {
  return { baseSha: FIXTURE_BASE, lines: BigInt(lines), covered: BigInt(covered), percent: Math.round((covered / lines) * 1000) / 10 };
}

interface FixtureNode {
  path: string;
  kind: number;
  lang: string;
  statements: number;
  combined: number;
  lanes: Partial<Record<string, number>>;
  /** Changed lines against FIXTURE_BASE, and how many of them are covered. */
  changed?: [lines: number, covered: number];
}

const PACKAGE = WireNodeKind.PACKAGE;
const FILE = WireNodeKind.FILE;

const NODES: FixtureNode[] = [
  { path: "cmd/shopfront", kind: PACKAGE, lang: "go", statements: 120, combined: 12.5, lanes: { unit: 8.3, e2e: 12.5 } },
  { path: "frontends/web/src", kind: WireNodeKind.DIRECTORY, lang: "ts", statements: 5_400, combined: 68.2, lanes: { unit: 63.0, e2e: 40.1 } },
  { path: "internal/billing", kind: PACKAGE, lang: "go", statements: 2_310, combined: 58.0, lanes: { unit: 51.2, integration: 30.1, e2e: 44.0 }, changed: [3, 2] },
  { path: "internal/catalog", kind: PACKAGE, lang: "go", statements: 3_020, combined: 81.4, lanes: { unit: 76.0, integration: 41.2, e2e: 60.3 } },
  { path: "internal/checkout", kind: PACKAGE, lang: "go", statements: 2_610, combined: 77.9, lanes: { unit: 70.4, integration: 45.0, e2e: 66.1 }, changed: [14, 9] },
  { path: "internal/webhooks", kind: PACKAGE, lang: "go", statements: 1_140, combined: 61.0, lanes: { unit: 55.4, integration: 20.0, e2e: 31.7 } },
  { path: FIXTURE_INVOICE_PATH, kind: FILE, lang: "go", statements: 940, combined: 64.1, lanes: { unit: 58.2, integration: 33.0, e2e: 47.9 } },
  { path: FIXTURE_REFUND_PATH, kind: FILE, lang: "go", statements: 12, combined: 75.0, lanes: { unit: 75.0, integration: 25.0, e2e: 41.7 }, changed: [3, 2] },
  { path: FIXTURE_TAX_PATH, kind: FILE, lang: "go", statements: 1_358, combined: 52.0, lanes: { unit: 44.6, integration: 29.0, e2e: 42.0 } },
];

const ROOT: FixtureNode = { path: "", kind: WireNodeKind.DIRECTORY, lang: "", statements: 48_210, combined: 74.2, lanes: { unit: 61.4, integration: 38.0, e2e: 52.7 }, changed: [17, 11] };

const parentOf = (path: string) => (path.includes("/") ? path.slice(0, path.lastIndexOf("/")) : "");

/** What GetCoverage lists under `path`: packages and directories below it, and the files directly in it. */
function childrenOf(path: string): FixtureNode[] {
  return NODES.filter((node) => (node.kind === FILE ? parentOf(node.path) === path : path === "" || node.path.startsWith(`${path}/`)));
}

/** The lens: one lane's figure stands in for combined, and the other lanes drop out. */
function lensed(node: FixtureNode, lane: string | undefined) {
  const lanes = Object.fromEntries(
    Object.entries(node.lanes)
      .filter(([name]) => !lane || name === lane)
      .map(([name, percent]) => [name, cov(node.statements, percent!)])
  );
  const combined = lane ? (node.lanes[lane] === undefined ? cov(node.statements, 0) : cov(node.statements, node.lanes[lane]!)) : cov(node.statements, node.combined);
  return { combined, lanes };
}

function nodeOf(node: FixtureNode, req: { lane?: string; baseSha?: string }): WireCoverageNode {
  const { combined, lanes } = lensed(node, req.lane);
  return { path: node.path, kind: node.kind, lang: node.lang, combined, lanes, diff: req.baseSha && node.changed ? diff(...node.changed) : undefined, changed: !!req.baseSha && !!node.changed };
}

function coverageOf(node: FixtureNode, req: { lane?: string; baseSha?: string }): WireCoverage {
  const { combined, lanes } = lensed(node, req.lane);
  return {
    granularity: "statement",
    combined,
    lanes,
    languages: [],
    baseHasReport: !!req.baseSha,
    deltaPercent: req.baseSha ? -0.4 : undefined,
    diff: req.baseSha && node.changed ? diff(...node.changed) : undefined,
    excludedGeneratedStatements: 0n,
  };
}

/** A path that is a prefix of nodes but not one itself ("internal"): its children's sums. */
function aggregate(path: string): FixtureNode | null {
  const below = NODES.filter((node) => node.kind !== FILE && node.path.startsWith(`${path}/`));
  if (below.length === 0) return null;
  const statements = below.reduce((sum, node) => sum + node.statements, 0);
  const weighted = (pick: (node: FixtureNode) => number | undefined) => below.reduce((sum, node) => sum + node.statements * (pick(node) ?? 0), 0) / statements;
  return {
    path,
    kind: WireNodeKind.DIRECTORY,
    lang: "",
    statements,
    combined: weighted((node) => node.combined),
    lanes: Object.fromEntries(LANES.map((lane) => [lane, weighted((node) => node.lanes[lane])])),
  };
}

const notFound = (what: string) => new ConnectError(`${what} is not in this report`, Code.NotFound);

/** GetCoverage over the fixture report. A commit other than the fixture's is not found. */
export function fixtureCoverageResponse(req: CoverageRequest): WireCoverageResponse {
  if ("gitCommit" in req.target && req.target.gitCommit !== COMMIT) throw notFound(`commit ${req.target.gitCommit}`);
  const self = req.path === "" ? ROOT : (NODES.find((node) => node.path === req.path && node.kind !== FILE) ?? aggregate(req.path));
  if (!self) throw notFound(req.path);
  const children = childrenOf(req.path).filter((node) => !req.changedOnly || (req.baseSha && node.changed));
  const lensReq = { lane: req.lane, baseSha: req.baseSha };
  return {
    gitCommit: COMMIT,
    treeHash: TREE,
    lanes: LANES,
    path: req.path,
    coverage: coverageOf(self, lensReq),
    children: children.map((node) => nodeOf(node, lensReq)),
  };
}

/** refund.go line by line: [line, statements, unit, integration, e2e, partial, changed against the base]. Zero hits: the lane did not reach it. */
const REFUND_LINES: [number, number, number, number, number, boolean, boolean][] = [
  [16, 1, 12, 0, 3, false, false],
  [17, 1, 0, 0, 0, false, true],
  [19, 1, 12, 0, 3, true, true],
  [20, 1, 4, 0, 0, false, true],
  [22, 1, 8, 2, 3, false, false],
  [23, 1, 0, 0, 0, false, false],
  [25, 2, 8, 2, 3, false, false],
  [31, 1, 4, 0, 0, false, false],
  [32, 1, 0, 0, 0, false, false],
  [34, 2, 4, 0, 0, false, false],
];

/** A file the fixture does not detail: a few covered lines, so the view has rows. */
const GENERIC_LINES: [number, number, number, number, number, boolean, boolean][] = [
  [12, 1, 40, 6, 9, false, false],
  [13, 2, 40, 6, 9, false, false],
  [18, 1, 0, 0, 0, false, false],
  [24, 1, 22, 0, 4, true, false],
];

function linesOf(rows: typeof REFUND_LINES, baseSha: string | undefined): WireLineCoverage[] {
  return rows.map(([line, statements, unit, integration, e2e, partial, changed]) => {
    const hits: Record<string, bigint> = {};
    if (unit) hits.unit = BigInt(unit);
    if (integration) hits.integration = BigInt(integration);
    if (e2e) hits.e2e = BigInt(e2e);
    return { line, statements, hits, partial, changed: !!baseSha && changed };
  });
}

/** GetFileCoverage over the fixture report: refund.go in full; invoice.go uncommitted; tax.go a blob GitHub lost. */
export function fixtureFileCoverageResponse(req: FileCoverageRequest): WireFileCoverageResponse {
  if ("gitCommit" in req.target && req.target.gitCommit !== COMMIT) throw notFound(`commit ${req.target.gitCommit}`);
  const node = NODES.find((candidate) => candidate.path === req.path && candidate.kind === FILE);
  if (!node) throw notFound(req.path);
  const blobId = node.path === FIXTURE_REFUND_PATH ? FIXTURE_REFUND_BLOB : node.path === FIXTURE_TAX_PATH ? TAX_BLOB : "";
  return {
    gitCommit: COMMIT,
    path: node.path,
    importPath: `github.com/acme/shopfront/${parentOf(node.path)}`,
    blobId,
    lang: node.lang,
    granularity: "statement",
    lanes: LANES,
    coverage: coverageOf(node, { baseSha: req.baseSha }),
    lines: linesOf(node.path === FIXTURE_REFUND_PATH ? REFUND_LINES : GENERIC_LINES, req.baseSha),
  };
}

/** The fixture report through wire.ts, as the TestApi answers it: a path or commit it lacks is NotFound. */
export function fixtureCoverage(req: CoverageRequest): Promise<CoverageTree> {
  try {
    return Promise.resolve(coverageTreeOf(fixtureCoverageResponse(req)));
  } catch (error) {
    return Promise.reject(error);
  }
}

export function fixtureFileCoverage(req: FileCoverageRequest): Promise<FileCoverage> {
  try {
    return Promise.resolve(fileCoverageOf(fixtureFileCoverageResponse(req)));
  } catch (error) {
    return Promise.reject(error);
  }
}

/**
 * What the fixture GitHub says about a blob.
 *
 *   github             serves refund.go's blob; any other blob is gone
 *   tampered           serves refund.go with a line changed (the mismatch guard)
 *   no-repo | uncommitted | gone | revoked | not-yet-available
 *                      every file has that reason
 */
export type CoverageSourceScenario = "github" | "tampered" | "no-repo" | "uncommitted" | "gone" | "revoked" | "not-yet-available";
export const COVERAGE_SOURCE_SCENARIOS: readonly CoverageSourceScenario[] = ["github", "tampered", "no-repo", "uncommitted", "gone", "revoked", "not-yet-available"];

export type FixtureCoverageSource = FetchCoverageSource & { calls: { project: string; blobId: string }[] };

export function fixtureCoverageSource(scenario: CoverageSourceScenario = "github"): FixtureCoverageSource {
  const calls: { project: string; blobId: string }[] = [];
  const fetch = (project: string, blobId: string): Promise<CoverageSource> => {
    calls.push({ project, blobId });
    switch (scenario) {
      case "github":
        return Promise.resolve(blobId === FIXTURE_REFUND_BLOB ? { text: FIXTURE_REFUND_SOURCE } : { unavailable: "gone" });
      case "tampered":
        return Promise.resolve({ text: FIXTURE_REFUND_SOURCE.replace("o.Paid = 0", "o.Paid = 0 // refunded") });
      default:
        return Promise.resolve({ unavailable: scenario });
    }
  };
  return Object.assign(fetch, { calls });
}
