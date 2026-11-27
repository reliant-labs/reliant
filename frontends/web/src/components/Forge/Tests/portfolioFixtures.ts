// Copyright (c) 2026 Reliant Labs

/**
 * GetTestPortfolio as a fixture answers it: each named project's row from
 * its GetTestSummary answer (a project without one is a row with has_runs
 * false), and the totals as control-plane computes them (control-plane#3956,
 * tests.proto TestPortfolioTotals): the pass rate weighted by each row's
 * judged runs, coverage summed per (language, granularity) over statements,
 * counts summed. A FIXTURE plays the server here, so it is the one place in
 * the web that sums; the pages never do.
 *
 * Shared by the page tests' fixture api (fixtures.ts) and the shop pack's
 * handler (src/fixtures/shop/model/tests.ts).
 */

import type { WirePortfolioCoverage, WirePortfolioProject, WirePortfolioResponse, WireSummaryResponse } from "@/services/controlPlane/tests/wire";

/**
 * One project's row from its summary: its newest report's coverage per
 * language (generated code already left out), from the report's language
 * split, or `languages` where the fixture's report carries none.
 */
export function portfolioRowWire(project: string, summary: WireSummaryResponse | undefined, languages?: readonly WirePortfolioCoverage[]): WirePortfolioProject {
  if (!summary || (!summary.lastRun && !summary.latest)) {
    return { project, hasRuns: false, branch: "", coverage: [], passRate: 0, passRateRuns: 0, suspectedCount: 0, flakyCount: 0, quarantinedCount: 0 };
  }
  const report = summary.latest?.coverage;
  const coverage: WirePortfolioCoverage[] = languages
    ? languages.map((c) => ({ ...c, combined: c.combined && { ...c.combined } }))
    : (report?.languages ?? [])
    .filter((l) => l.combined && l.combined.statements > 0n)
    .map((l) => ({ lang: l.lang, granularity: report!.granularity || "statement", combined: { ...l.combined! }, projects: 1 }));
  return {
    project,
    hasRuns: !!summary.lastRun,
    branch: summary.branch,
    latest: summary.latest,
    coverage,
    passRate: summary.passRate,
    passRateRuns: summary.passRateRuns,
    suspectedCount: summary.suspectedCount,
    flakyCount: summary.flakyCount,
    quarantinedCount: summary.quarantinedCount,
    lastRun: summary.lastRun,
  };
}

/** The rows and the server's totals over them. */
export function portfolioWire(rows: readonly WirePortfolioProject[], truncated = false): WirePortfolioResponse {
  const judged = rows.reduce((sum, r) => sum + r.passRateRuns, 0);
  const passed = rows.reduce((sum, r) => sum + r.passRate * r.passRateRuns, 0);
  const sum = (pick: (r: WirePortfolioProject) => bigint | undefined) => rows.reduce((total, r) => total + (pick(r) ?? 0n), 0n);
  const coverage = new Map<string, WirePortfolioCoverage>();
  for (const row of rows) {
    for (const c of row.coverage) {
      const key = `${c.lang}\u0000${c.granularity}`;
      const acc = coverage.get(key) ?? { lang: c.lang, granularity: c.granularity, combined: { statements: 0n, covered: 0n, percent: 0 }, projects: 0 };
      acc.combined!.statements += c.combined?.statements ?? 0n;
      acc.combined!.covered += c.combined?.covered ?? 0n;
      acc.projects += 1;
      coverage.set(key, acc);
    }
  }
  for (const c of coverage.values()) {
    const { statements, covered } = c.combined!;
    c.combined!.percent = statements > 0n ? Math.round((Number(covered) / Number(statements)) * 1000) / 10 : 0;
  }
  return {
    projects: [...rows],
    totals: {
      projects: rows.length,
      projectsWithRuns: rows.filter((r) => r.hasRuns).length,
      projectsWithReport: rows.filter((r) => r.latest).length,
      passRate: judged > 0 ? passed / judged : 0,
      passRateRuns: judged,
      suspectedCount: rows.reduce((total, r) => total + r.suspectedCount, 0),
      flakyCount: rows.reduce((total, r) => total + r.flakyCount, 0),
      quarantinedCount: rows.reduce((total, r) => total + r.quarantinedCount, 0),
      latestTotals: {
        passed: sum((r) => r.latest?.totals?.passed),
        failed: sum((r) => r.latest?.totals?.failed),
        skipped: sum((r) => r.latest?.totals?.skipped),
        retryPassed: sum((r) => r.latest?.totals?.retryPassed),
      },
      coverage: [...coverage.values()].sort((a, b) => a.lang.localeCompare(b.lang) || a.granularity.localeCompare(b.granularity)),
    },
    truncated,
  };
}
