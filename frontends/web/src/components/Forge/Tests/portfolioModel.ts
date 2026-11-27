// Copyright (c) 2026 Reliant Labs

/**
 * What the aggregate Tests, Coverage and Overview pages draw from one
 * GetTestPortfolio answer (hooks/test-queries.ts useTestPortfolio): the
 * totals strip's figures and the order of the per-project rows.
 *
 * Every total is the SERVER's (TestPortfolioTotals): a pass rate weighted by
 * runs, coverage summed per (language, granularity) over statements. Nothing
 * here averages a percentage or mixes two languages into one figure. The one
 * thing computed in the browser is a row's rank on the Coverage page, its
 * lowest language, which compares figures and sums none.
 */

import type { PortfolioCoverage, PortfolioProject, PortfolioTotals, TestPortfolio } from "@/services/controlPlane/tests/types";

import type { AggregateTotal } from "../Console/aggregate/AggregatePage";
import type { SeveralApp } from "../Console/severalProjects";
import { formatPassRate, formatPercent } from "./testsModel";

const LANGUAGE_NAMES: Record<string, string> = {
  go: "Go",
  ts: "TypeScript",
  js: "JavaScript",
  py: "Python",
  rb: "Ruby",
  rs: "Rust",
  java: "Java",
  kt: "Kotlin",
  swift: "Swift",
};

/** A coverage language as a person names it: "go" is "Go", "ts" is "TypeScript". */
export function languageName(lang: string): string {
  if (!lang) return "Unknown language";
  return LANGUAGE_NAMES[lang.toLowerCase()] ?? lang.charAt(0).toUpperCase() + lang.slice(1);
}

/** The unit a figure counts: "statements", or "lines" for LCOV and Cobertura input. */
export function granularityWords(granularity: string): string {
  if (granularity === "line") return "lines";
  if (granularity === "statement" || granularity === "") return "statements";
  return granularity;
}

/** "Go · statements": one (language, granularity), the unit coverage is summed in. */
export function coverageName(coverage: Pick<PortfolioCoverage, "lang" | "granularity">): string {
  return `${languageName(coverage.lang)} · ${granularityWords(coverage.granularity)}`;
}

/** The identity of a (language, granularity), for columns and lookups. */
export const coverageKey = (coverage: Pick<PortfolioCoverage, "lang" | "granularity">): string => `${coverage.lang}\u0000${coverage.granularity}`;

const count = (value: number) => value.toLocaleString("en-US");

/** "12,345 / 17,300": covered over counted, the figure's own n. */
export function coverageCounts(coverage: PortfolioCoverage): string {
  return `${count(coverage.combined.covered)} / ${count(coverage.combined.statements)}`;
}

/** A coverage total as the strip shows it: "Go · statements (12,345 / 17,300)", 71.2%. */
export function coverageTotal(coverage: PortfolioCoverage, prefix = ""): AggregateTotal {
  return { label: `${prefix}${coverageName(coverage)} (${coverageCounts(coverage)})`, value: formatPercent(coverage.combined.percent) };
}

/** The pass rate total: the server's runs-weighted rate, with its n. Never 100% of nothing. */
export function passRateTotal(totals: Pick<PortfolioTotals, "passRate" | "passRateRuns">, label = "Pass rate"): AggregateTotal {
  if (totals.passRate === null) return { label: `${label} (no runs judged)`, value: "—" };
  return { label: `${label} over ${count(totals.passRateRuns)} ${totals.passRateRuns === 1 ? "run" : "runs"}`, value: formatPassRate(totals.passRate) };
}

/** The Tests page's totals strip, all of it the server's. */
export function testsTotals(totals: PortfolioTotals): AggregateTotal[] {
  return [
    passRateTotal(totals),
    { label: "Failing in the latest reports", value: count(totals.latestTotals.failed) },
    { label: "Flaky tests", value: count(totals.flaky) },
    { label: "Suspected flaky", value: count(totals.suspected) },
    { label: "Quarantined", value: count(totals.quarantined) },
    { label: "Projects with test runs", value: `${count(totals.projectsWithRuns)} of ${count(totals.projects)}` },
  ];
}

/**
 * What GetTestPortfolio is asked for the projects in scope: their forge apps,
 * each once, so the rows and the totals are exactly the projects shown. Every
 * project of an org too large to name (more than the server takes) is asked
 * as "all": the server lists and totals the 50 with the most recent run, and
 * says so (truncated). A chosen several that large is cut by the hook
 * instead (capped), never widened to projects nobody chose.
 */
export function portfolioProjects(apps: readonly SeveralApp[], mode: "many" | "all", max = 50): readonly string[] | "all" {
  const named = [...new Set(apps.map((app) => app.app))];
  return mode === "all" && named.length > max ? "all" : named;
}

/** The portfolio's rows by forge project. */
export function rowsByProject(portfolio: TestPortfolio | undefined): ReadonlyMap<string, PortfolioProject> {
  return new Map((portfolio?.projects ?? []).map((row) => [row.project, row]));
}

/** A row's lowest-covered language: what the Coverage page ranks by. Null with no coverage. */
export function lowestCoverage(row: PortfolioProject | undefined): PortfolioCoverage | null {
  if (!row) return null;
  let lowest: PortfolioCoverage | null = null;
  for (const coverage of row.coverage) if (!lowest || coverage.combined.percent < lowest.combined.percent) lowest = coverage;
  return lowest;
}

/**
 * The projects as the Coverage page lists them: lowest coverage first (by
 * each project's lowest-covered language), then projects with runs but no
 * coverage, then projects with nothing recorded; ties by name.
 */
export function rankByCoverage(apps: readonly SeveralApp[], rows: ReadonlyMap<string, PortfolioProject>): SeveralApp[] {
  const tier = (app: SeveralApp) => {
    const row = rows.get(app.app);
    if (lowestCoverage(row)) return 0;
    return row?.hasRuns ? 1 : 2;
  };
  return [...apps].sort((a, b) => {
    const byTier = tier(a) - tier(b);
    if (byTier !== 0) return byTier;
    const pa = lowestCoverage(rows.get(a.app))?.combined.percent ?? 0;
    const pb = lowestCoverage(rows.get(b.app))?.combined.percent ?? 0;
    return pa - pb || a.name.localeCompare(b.name);
  });
}

/** The (language, granularity) pairs the totals carry, in the server's order: the Coverage page's columns. */
export function coverageColumns(totals: PortfolioTotals | undefined): readonly PortfolioCoverage[] {
  return totals?.coverage ?? NO_COVERAGE;
}

const NO_COVERAGE: readonly PortfolioCoverage[] = [];
