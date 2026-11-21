// Copyright (c) 2026 Reliant Labs

/**
 * What Tests › Runs and a run's page compute from TestsService's answers,
 * free of React: the URL's filters (each one an optional field of
 * ListTestRuns), and the words and figures of a run.
 *
 * The environment is not one of these filters: it is the console's one scope
 * (`?env=`), as on every other page, and reaches the request as the scope's
 * environment id.
 */

import type { TestFailure, TestRun, TestRunFilters, TestRunOutcome, TestRunSource } from "@/services/controlPlane/tests/types";

/** The `source` values the URL accepts: every source a run can be recorded from. */
export const RUN_SOURCES = ["ci", "local", "env", "confirm"] as const satisfies readonly Exclude<TestRunSource, "unknown">[];
/** The `status` values the URL accepts: a run's outcome. */
export const RUN_STATUSES = ["passed", "failed", "undetermined"] as const satisfies readonly TestRunOutcome[];

export type RunSourceFilter = (typeof RUN_SOURCES)[number];

/** Tests › Runs' own filters, as the URL holds them. Each is optional; absent means no filter. */
export interface RunsSearch {
  branch?: string;
  /** A commit sha or its prefix, as the server matches it. */
  commit?: string;
  /** Runs of the commit a release was cut from. */
  release?: string;
  lane?: string;
  source?: RunSourceFilter;
  status?: TestRunOutcome;
}

export const RUNS_SEARCH_KEYS = ["branch", "commit", "release", "lane", "source", "status"] as const satisfies readonly (keyof RunsSearch)[];

/** Bounds the route schema holds too (routeSchemas.ts): a ref this long is not a ref. */
const MAX_REF = { branch: 256, commit: 64, release: 256, lane: 64 } as const;

function text(value: unknown, max: number): string | undefined {
  const s = typeof value === "number" ? String(value) : value;
  if (typeof s !== "string") return undefined;
  const trimmed = s.trim();
  return trimmed && trimmed.length <= max ? trimmed : undefined;
}

/** The RunsSearch keys of a raw search object; a value that does not fit is absent. */
export function pickRunsSearch(raw: Record<string, unknown>): RunsSearch {
  const out: RunsSearch = {};
  for (const key of ["branch", "commit", "release", "lane"] as const) {
    const value = text(raw[key], MAX_REF[key]);
    if (value) out[key] = value;
  }
  if ((RUN_SOURCES as readonly unknown[]).includes(raw.source)) out.source = raw.source as RunSourceFilter;
  if ((RUN_STATUSES as readonly unknown[]).includes(raw.status)) out.status = raw.status as TestRunOutcome;
  return out;
}

/** How many of the page's own filters are set (the environment scope is not one of them). */
export function activeRunsFilterCount(search: RunsSearch): number {
  return RUNS_SEARCH_KEYS.filter((key) => search[key] !== undefined).length;
}

/** ListTestRuns' filters: the scope's environment id (none for All) and the page's own. */
export function runsFiltersOf(environmentId: string | undefined, search: RunsSearch): TestRunFilters {
  const filters: TestRunFilters = {};
  if (environmentId) filters.environmentId = environmentId;
  if (search.branch) filters.branch = search.branch;
  if (search.commit) filters.gitCommit = search.commit;
  if (search.release) filters.releaseVersion = search.release;
  if (search.lane) filters.lane = search.lane;
  if (search.source) filters.source = search.source;
  if (search.status) filters.outcome = search.status;
  return filters;
}

export const RUN_SOURCE_LABEL: Record<RunSourceFilter, string> = {
  ci: "CI",
  local: "Local",
  env: "Environment",
  confirm: "Flake confirm",
};

export const RUN_STATUS_LABEL: Record<TestRunOutcome, string> = {
  passed: "Passed",
  failed: "Failed",
  undetermined: "Undetermined",
};

/** A run's source as a column says it: "CI", "Local", "Environment", "Flake confirm". */
export function runSourceLabel(run: TestRun): string {
  return run.source === "unknown" ? "Unknown" : RUN_SOURCE_LABEL[run.source];
}

/** How long the run took, or null when it did not say when it started or finished. */
export function runDurationMs(run: TestRun): number | null {
  if (run.startedMs === null || run.finishedMs === null || run.finishedMs < run.startedMs) return null;
  return run.finishedMs - run.startedMs;
}

/** When the run happened, for "12m ago": when it finished, else when it was recorded. */
export function runAtMs(run: TestRun): number | null {
  return run.finishedMs ?? run.startedMs ?? run.createdMs;
}

/** "1,412 passed · 2 failed · 37 skipped": the counts that are not zero, passed always. */
export function totalsWords(totals: { passed: number; failed: number; skipped: number }): string {
  const parts = [`${totals.passed.toLocaleString()} passed`];
  if (totals.failed > 0) parts.push(`${totals.failed.toLocaleString()} failed`);
  if (totals.skipped > 0) parts.push(`${totals.skipped.toLocaleString()} skipped`);
  return parts.join(" · ");
}

/**
 * What a failure's classification means for its test, in words. Only a
 * confirmed flake counts toward the test's flake state; host and correlated
 * failures never do (forge docs/proposals/tests-coverage-flakes.md §1.5).
 * Null when ingest has not classified it.
 */
export function failureVerdictWords(verdict: TestFailure["verdict"]): { words: string; counted: boolean; title: string } | null {
  switch (verdict) {
    case "confirmed":
      return { words: "Flaky: passed on rerun", counted: true, title: "Failed, then passed on the same code. Counts toward the test's flake state." };
    case "real":
      return { words: "Real failure", counted: false, title: "Failed again on rerun: a real break, not a flake." };
    case "raced":
      return { words: "Data race", counted: false, title: "The output reports a data race: a bug, never counted as a flake." };
    case "inconclusive":
      return { words: "Inconclusive", counted: false, title: "The rerun could not run (a build failure or a timeout)." };
    case "unattributed":
      return { words: "Unattributed", counted: false, title: "The failure could not be tied to a test." };
    case "host":
      return { words: "Host failure: not counted", counted: false, title: "The output matches a host signature (network, disk, port in use, docker, a timeout under load). Never counted against the test." };
    case "correlated":
      return {
        words: "Infra group: not counted",
        counted: false,
        title: "One of 3 or more failures in this run sharing a package, a harness or a signature: one infrastructure event, never counted against the tests.",
      };
    default:
      return null;
  }
}

/** "internal/billing/refund_test.go:42", or "" when the output named no place. */
export function failurePlace(failure: TestFailure): string {
  const file = failure.file || failure.test.file;
  if (!file) return "";
  const line = failure.line || failure.test.line;
  return line > 0 ? `${file}:${line}` : file;
}
