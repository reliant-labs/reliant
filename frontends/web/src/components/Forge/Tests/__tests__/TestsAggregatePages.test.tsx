// Copyright (c) 2026 Reliant Labs

/**
 * The aggregate Tests › Overview and Tests › Coverage (TestsAggregatePages):
 * one GetTestPortfolio read for the projects in scope, the server's totals
 * as they are, a row per project (coverage ranked lowest first), the
 * drill-down hint, and the states: no data, no access, an older server.
 */

import { cleanup, screen, within } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("../../../Layout/shellScopeNav", () => ({ navigateWithShellScope: vi.fn() }));
vi.mock("../../../Layout/sidebar/shellProject", () => ({
  useShellLastPick: (select: (state: { set: (key: string) => void }) => unknown) => select({ set: vi.fn() }),
  useShellSeveral: () => ({ mode: null, rows: [] }),
  sectionsAppOf: () => null,
  deployedAppOf: () => null,
}));

import type { TestApi } from "@/services/controlPlane/tests/TestApiContext";
import { portfolioOf, WireIngestState, WireRunOutcome, WireRunSource, type WirePortfolioProject, type WireRunSummary } from "@/services/controlPlane/tests/wire";

import { portfolioWire } from "../portfolioFixtures";
import { CoverageAggregatePage, TestsAggregatePage } from "../TestsAggregatePages";
import { renderWithApi } from "./testsTestUtils";

const APPS = [
  { key: "p1", name: "Shop", app: "shop", deployed: true },
  { key: "p2", name: "Blog", app: "blog", deployed: true },
  { key: "p3", name: "Docs", app: "docs", deployed: false },
];

const NOW = Date.now();

function run(project: string, outcome: number): WireRunSummary {
  return {
    runId: `run-${project}`,
    project,
    gitCommit: "9f3c2a1d00",
    treeHash: "t",
    dirty: false,
    branch: "main",
    baseSha: "",
    lane: "unit",
    source: WireRunSource.CI,
    environmentId: "",
    startedAt: timestampFromMs(NOW - 3 * 3_600_000),
    finishedAt: timestampFromMs(NOW - 2 * 3_600_000),
    exitCode: 0,
    outcome,
    ingestState: WireIngestState.INGESTED,
    ingestError: "",
    totals: { passed: 10n, failed: outcome === WireRunOutcome.FAILED ? 2n : 0n, skipped: 0n, retryPassed: 0n },
    forgeVersion: "v1.12.0",
    uploadedBy: "ci",
  };
}

function row(project: string, go: [covered: bigint, statements: bigint] | null, failed: bigint, passRate: number, passRateRuns: number): WirePortfolioProject {
  return {
    project,
    hasRuns: true,
    branch: "main",
    latest: {
      gitCommit: `${project}c0ffee99`,
      treeHash: "t",
      lanes: ["unit"],
      runCount: 1,
      totals: { passed: 40n, failed, skipped: 0n, retryPassed: 0n },
      reportDigest: "",
      updatedAt: timestampFromMs(NOW - 3_600_000),
    },
    coverage: go ? [{ lang: "go", granularity: "statement", combined: { covered: go[0], statements: go[1], percent: (Number(go[0]) / Number(go[1])) * 100 }, projects: 1 }] : [],
    passRate,
    passRateRuns,
    suspectedCount: 1,
    flakyCount: 2,
    quarantinedCount: 0,
    lastRun: run(project, failed > 0n ? WireRunOutcome.FAILED : WireRunOutcome.PASSED),
  };
}

const NO_RUNS = (project: string): WirePortfolioProject => ({
  project,
  hasRuns: false,
  branch: "",
  coverage: [],
  passRate: 0,
  passRateRuns: 0,
  suspectedCount: 0,
  flakyCount: 0,
  quarantinedCount: 0,
});

/** shop covers 900 of 1,000 Go statements, blog 10 of 100: weighted 910 / 1,100 = 82.7%, where an average of percents would say 50%. */
const PORTFOLIO = portfolioWire([row("shop", [900n, 1000n], 0n, 1, 20), row("blog", [10n, 100n], 3n, 0.5, 4), NO_RUNS("docs")]);

function api(getTestPortfolio: TestApi["getTestPortfolio"]): TestApi {
  const refuse = () => Promise.reject(new Error("not read by the aggregate pages"));
  return {
    getTestSummary: refuse,
    listTestRuns: refuse,
    getTestRun: refuse,
    getCoverage: refuse,
    getFileCoverage: refuse,
    getReleaseTests: refuse,
    listFlakyTests: refuse,
    getTest: refuse,
    getTestPortfolio,
  };
}

const answering = () => vi.fn<TestApi["getTestPortfolio"]>(() => Promise.resolve(portfolioOf(PORTFOLIO)));

afterEach(cleanup);

describe("the aggregate Tests › Overview", () => {
  it("reads one portfolio for the projects' apps and shows the server's totals", async () => {
    const read = answering();
    renderWithApi(api(read), <TestsAggregatePage page={{ kind: "tests" }} apps={APPS} mode="many" />);
    const totals = await screen.findByTestId("aggregate-totals");
    expect(read).toHaveBeenCalledTimes(1);
    expect(read.mock.calls[0]![0].projects).toEqual(["blog", "docs", "shop"]);
    // (20 × 1 + 4 × 0.5) / 24, weighted by runs: never the average of 100% and 50%.
    expect(within(totals).getByText("Pass rate over 24 runs")).toBeInTheDocument();
    expect(within(totals).getByText("91.6%")).toBeInTheDocument();
    expect(within(totals).getByText("Failing in the latest reports")).toBeInTheDocument();
    expect(within(totals).getByText("Flaky tests")).toBeInTheDocument();
    expect(within(totals).getByText("Projects with test runs")).toBeInTheDocument();
    expect(within(totals).getByText("2 of 3")).toBeInTheDocument();
    expect(screen.getByRole("note")).toHaveTextContent("Select one project to pick a branch and see its coverage trend over recent commits.");
  });

  it("shows each project's row, and a project with no runs as no data", async () => {
    renderWithApi(api(answering()), <TestsAggregatePage page={{ kind: "tests" }} apps={APPS} mode="many" />);
    const shop = await screen.findByTestId("tests-aggregate-row-p1");
    expect(shop).toHaveAttribute("href", "/forge/p/shop/tests");
    expect(within(shop).getByText("main")).toBeInTheDocument();
    expect(within(shop).getByText("100%")).toBeInTheDocument();
    expect(within(shop).getByText("Passing")).toBeInTheDocument();
    expect(within(shop).getByText("90.0%")).toBeInTheDocument();
    const blog = screen.getByTestId("tests-aggregate-row-p2");
    expect(within(blog).getByText("3 failing")).toBeInTheDocument();
    const docs = screen.getByTestId("tests-aggregate-row-p3");
    expect(within(docs).getByText("No data yet")).toBeInTheDocument();
  });

  it("asks for every project with runs when more are in scope than one read names, and says when the server truncated", async () => {
    const read = vi.fn<TestApi["getTestPortfolio"]>(() => Promise.resolve(portfolioOf({ ...PORTFOLIO, truncated: true })));
    const many = Array.from({ length: 51 }, (_, i) => ({ key: `k${i}`, name: `App ${i}`, app: `app-${i}` }));
    renderWithApi(api(read), <TestsAggregatePage page={{ kind: "tests" }} apps={many} mode="all" />);
    expect(await screen.findByTestId("portfolio-truncated")).toHaveTextContent("Only the 50 projects with the most recent test runs are counted and listed.");
    expect(read.mock.calls[0]![0].projects).toEqual([]);
  });

  it("says the reader's access is limited when the server refuses a caller bound to some environments", async () => {
    renderWithApi(api(() => Promise.reject(new ConnectError("bound to an environment", Code.PermissionDenied))), <TestsAggregatePage page={{ kind: "tests" }} apps={APPS} mode="many" />);
    expect(await screen.findByTestId("portfolio-denied")).toHaveTextContent("Your access is limited to some environments");
    expect(screen.queryByTestId("aggregate-totals")).toBeNull();
    expect(screen.queryByTestId("project-breakdown")).toBeNull();
    // The way out: one project's own page.
    expect(screen.getByTestId("drill-down-hint")).toBeInTheDocument();
  });

  it("reads an older control plane's Unimplemented as not available, never as no runs", async () => {
    renderWithApi(api(() => Promise.reject(new ConnectError("unknown method", Code.Unimplemented))), <TestsAggregatePage page={{ kind: "tests" }} apps={APPS} mode="many" />);
    expect(await screen.findByTestId("portfolio-unavailable")).toHaveTextContent("Test figures across projects aren't available here yet");
    expect(screen.queryByText("No data yet")).toBeNull();
  });
});

describe("the aggregate Tests › Coverage", () => {
  it("shows coverage per language as the server summed it, and ranks projects lowest first", async () => {
    renderWithApi(api(answering()), <CoverageAggregatePage page={{ kind: "tests-coverage" }} apps={APPS} mode="many" />);
    const totals = await screen.findByTestId("aggregate-totals");
    expect(within(totals).getByText("Go · statements (910 / 1,100)")).toBeInTheDocument();
    expect(within(totals).getByText("82.7%")).toBeInTheDocument();
    expect(within(totals).getByText("2 of 3")).toBeInTheDocument();
    const rows = within(screen.getByTestId("project-breakdown")).getAllByTestId(/^coverage-aggregate-row-/);
    expect(rows.map((r) => r.getAttribute("data-testid"))).toEqual(["coverage-aggregate-row-p2", "coverage-aggregate-row-p1", "coverage-aggregate-row-p3"]);
    expect(within(rows[0]!).getByText("10.0%")).toBeInTheDocument();
    expect(within(rows[0]!).getByText("10 / 100")).toBeInTheDocument();
    expect(rows[0]).toHaveAttribute("href", "/forge/p/blog/tests/coverage");
    expect(within(rows[2]!).getByText("No data yet")).toBeInTheDocument();
    expect(screen.getByRole("note")).toHaveTextContent("Select one project to browse its package and file tree.");
  });

  it("says the reader's access is limited on PermissionDenied", async () => {
    renderWithApi(api(() => Promise.reject(new ConnectError("denied", Code.PermissionDenied))), <CoverageAggregatePage page={{ kind: "tests-coverage" }} apps={APPS} mode="all" />);
    expect(await screen.findByTestId("portfolio-denied")).toBeInTheDocument();
  });
});
