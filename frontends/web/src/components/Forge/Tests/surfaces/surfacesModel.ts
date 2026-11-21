// Copyright (c) 2026 Reliant Labs

/**
 * What the Tests surfaces OUTSIDE the Tests pages compute, free of React: the
 * release Tests tab, the Releases list's Tests column, the project Overview's
 * Tests card and its "Needs attention" items, and the nav badge (forge
 * docs/proposals/tests-coverage-flakes.md §3.1–3.2).
 *
 * Each reads what the Tests pages read (GetTestSummary of the default
 * branch, ListFlakyTests, GetReleaseTests), so a figure on the Overview is
 * the figure the Tests Overview shows. None of them narrows to the
 * console's environment scope (testsScope.tsx): a test run belongs to a
 * commit, and the Overview's `prod` would hide nearly all of them.
 */

import type { ReleaseTests, TestFlake, TestSummary } from "@/services/controlPlane/tests/types";

import { formatAgo } from "../../Observability/format";
import { formatDelta, formatPercent, shortSha } from "../testsModel";

/**
 * How far the default branch's coverage may fall before "Needs attention"
 * says so, in percentage points. GetTestSummary has no per-release delta, so
 * the drop is read from what it does carry: the newest report's delta
 * against its base report (the branch's previous report), and each
 * low-coverage package's delta beside it.
 */
export const COVERAGE_DROP_ATTENTION_POINTS = 2;

/** A flaky test counts as new for "Needs attention" while its newest confirmed flake is this recent. */
export const RECENT_FLAKE_MS = 7 * 24 * 3600 * 1000;

// ── The nav badge ───────────────────────────────────────────────────────────

/**
 * The Tests nav group's count: suspected plus flaky tests on the default
 * branch. Suspected is the state people act on (one confirmed flake, delete
 * or rewrite it), so it counts beside flaky; quarantined does not (it is
 * handled), nor does a host failure (never a test's). 0 draws no badge.
 */
export function testsAttentionCount(summary: Pick<TestSummary, "suspected" | "flaky"> | null | undefined): number {
  if (!summary) return 0;
  return Math.max(0, summary.suspected) + Math.max(0, summary.flaky);
}

// ── Needs attention ─────────────────────────────────────────────────────────

export interface TestsAttentionHrefs {
  /** Tests › Flaky tests, narrowed to one state when given. */
  flaky: (state?: "suspected" | "flaky" | "quarantined") => string;
  /** One test's page, by its TestKey id. */
  test: (testId: string) => string;
  /** Tests › Coverage at `path` ("" is the root) of `commit`. */
  coverage: (path: string, commit: string) => string;
}

export interface TestsAttentionItem {
  key: string;
  tone: "warning" | "danger";
  /** Named in bold: a count of tests, a test, a package, the branch. */
  subject: string;
  /** After the subject: "suspected flaky", "quarantine overdue", "coverage fell 8.0 points". */
  what: string;
  /** The muted figure on the right. */
  detail: string;
  href: string;
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;
/** A fall's size, one decimal: "8.0". */
const points = (delta: number) => (Math.round(Math.abs(delta) * 10) / 10).toFixed(1);

/**
 * The Tests rows of the Overview's "Needs attention", worst first:
 *
 *   suspected      every suspected test (one confirmed flake: act now). The
 *                  count is the summary's; the newest one is named.
 *   new flaky      flaky tests whose newest confirmed flake is within
 *                  RECENT_FLAKE_MS.
 *   quarantine     each quarantine past its expiry (still applied, overdue).
 *   coverage       the branch's newest report fell COVERAGE_DROP_ATTENTION_POINTS
 *                  or more against its base report; and each low-coverage
 *                  package that fell as far.
 *
 * `flakes` is the first page of ListFlakyTests (every state but healthy).
 * `flakesMore`: that page is not all of them, so its counts say "at least".
 */
export function testsAttentionItems({
  summary,
  flakes,
  flakesMore = false,
  now,
  hrefs,
}: {
  summary: TestSummary | null;
  flakes: readonly TestFlake[];
  flakesMore?: boolean;
  now: number;
  hrefs: TestsAttentionHrefs;
}): TestsAttentionItem[] {
  const items: TestsAttentionItem[] = [];
  const atLeast = flakesMore ? "at least " : "";

  const suspected = flakes.filter((flake) => flake.state === "suspected").sort((a, b) => (b.lastEventMs ?? 0) - (a.lastEventMs ?? 0));
  const suspectedCount = summary ? summary.suspected : suspected.length;
  if (suspectedCount > 0) {
    const newest = suspected[0];
    items.push({
      key: "tests/suspected",
      tone: "warning",
      subject: plural(suspectedCount, "test", "tests"),
      what: "suspected flaky",
      detail: newest ? `newest ${newest.test.name}${newest.lastEventMs ? `, ${formatAgo(newest.lastEventMs, now)}` : ""}` : "one confirmed flake each",
      href: hrefs.flaky("suspected"),
    });
  }

  const recentFlaky = flakes.filter((flake) => flake.state === "flaky" && flake.lastEventMs !== null && now - flake.lastEventMs <= RECENT_FLAKE_MS);
  if (recentFlaky.length > 0) {
    items.push({
      key: "tests/flaky-recent",
      tone: "danger",
      subject: `${atLeast}${plural(recentFlaky.length, "flaky test", "flaky tests")}`,
      what: "flaked this week",
      detail: recentFlaky.length === 1 ? recentFlaky[0]!.test.name : `${recentFlaky[0]!.test.name} and ${recentFlaky.length - 1} more`,
      href: hrefs.flaky("flaky"),
    });
  }

  for (const flake of flakes) {
    const quarantine = flake.quarantine;
    if (!quarantine?.overdue) continue;
    items.push({
      key: `tests/quarantine/${flake.test.id}`,
      tone: "warning",
      subject: flake.test.name || flake.test.id,
      what: "quarantine overdue",
      detail: quarantine.expiresMs ? `expired ${formatAgo(quarantine.expiresMs, now)}` : "past its expiry",
      href: hrefs.test(flake.test.id),
    });
  }

  const latest = summary?.latest ?? null;
  const delta = latest?.coverage?.deltaPercent ?? null;
  const combined = latest?.coverage?.combined ?? null;
  if (latest && combined && delta !== null && delta <= -COVERAGE_DROP_ATTENTION_POINTS) {
    items.push({
      key: "tests/coverage",
      tone: "warning",
      subject: `Coverage on ${summary!.branch}`,
      what: `fell ${points(delta)} points`,
      detail: `${formatPercent(combined.percent)} at ${shortSha(latest.gitCommit)}`,
      href: hrefs.coverage("", latest.gitCommit),
    });
  }
  for (const pkg of summary?.lowCoverage ?? []) {
    if (pkg.deltaPercent === null || pkg.deltaPercent > -COVERAGE_DROP_ATTENTION_POINTS) continue;
    items.push({
      key: `tests/coverage/${pkg.path}`,
      tone: "warning",
      subject: pkg.path,
      what: `coverage fell ${points(pkg.deltaPercent)} points`,
      detail: pkg.combined ? `${formatPercent(pkg.combined.percent)} on ${summary!.branch}` : `on ${summary!.branch}`,
      href: hrefs.coverage(pkg.path, latest?.gitCommit ?? ""),
    });
  }
  return items;
}

// ── A release's evidence ────────────────────────────────────────────────────

export type ReleaseTestsTone = "success" | "danger" | "neutral";

/**
 * A release's evidence as one mark: its dot and its words. Passed is green
 * only on complete evidence; partial evidence is neutral even when every
 * recorded lane passed (unknown is never green), and a failure is a failure
 * either way.
 */
export function releaseTestsMark(tests: ReleaseTests): { tone: ReleaseTestsTone; words: string; coverage: string | null; title: string } {
  const report = tests.report;
  if (tests.status === "unavailable" || !report) {
    return { tone: "neutral", words: "No report", coverage: null, title: tests.reason || "No test report for this release's commit." };
  }
  const combined = report.coverage?.combined;
  const coverage = combined && combined.statements > 0 ? formatPercent(combined.percent) : null;
  const lanes = report.lanes.join(", ") || "no lanes";
  if (report.totals.failed > 0) {
    return { tone: "danger", words: `${report.totals.failed.toLocaleString()} failed`, coverage, title: `${report.totals.failed} failed across ${lanes}` };
  }
  if (tests.status === "partial") {
    return { tone: "neutral", words: "Partial", coverage, title: tests.reason ? `Partial: ${tests.reason}` : `Partial: only ${lanes} recorded` };
  }
  return { tone: "success", words: "Passed", coverage, title: `${report.totals.passed.toLocaleString()} passed across ${lanes}` };
}

/** "+1.6 since 20261117.0900", or why there is no delta. */
export function releaseDeltaWords(tests: Pick<ReleaseTests, "coverageDeltaPercent" | "previousReleaseVersion">, short: (version: string) => string): string {
  if (!tests.previousReleaseVersion) return "the first release with a report: nothing to compare";
  if (tests.coverageDeltaPercent === null) return `${short(tests.previousReleaseVersion)} has no report to compare`;
  return `${formatDelta(tests.coverageDeltaPercent)} since ${short(tests.previousReleaseVersion)}`;
}
