// Copyright (c) 2026 Reliant Labs

/**
 * React Query hooks for the Tests pages: TestsService's reads, over
 * TestApiContext (so tests and the preview hand them fixtures).
 *
 * Every key starts with the org and the forge project, so two orgs or two
 * projects never share an answer, and Refresh (invalidate `all`) re-reads
 * exactly what is on screen. Runs arrive with every CI lane and ingest fills
 * their figures in within a minute, so the summary and the runs list go stale
 * after 30s; a commit's coverage never changes once read, nothing polls.
 *
 * Paged lists pass the request with every page: the server's page token is
 * valid only with the same request.
 */

import { useInfiniteQuery, useQueries, useQuery } from "@tanstack/react-query";

import { keepPreviousInOrgSegment, orgQueryEnabled, useOrgScope, withOrgFailure } from "@/lib/orgScope";
import { useTestApi } from "@/services/controlPlane/tests/TestApiContext";
import { testsRetry, testsRetryDelay } from "@/services/controlPlane/tests/errors";
import type { CoverageTarget, FlakyFilters, TestRunFilters } from "@/services/controlPlane/tests/types";

/** GetTestPortfolio names at most this many projects. */
export const PORTFOLIO_MAX_PROJECTS = 50;

export const testKeys = {
  all: (orgId: string | null, project: string) => ["tests", orgId ?? "", project] as const,
  summary: (orgId: string | null, project: string, branch: string, environmentId: string) => [...testKeys.all(orgId, project), "summary", branch, environmentId] as const,
  runs: (orgId: string | null, project: string, filters: TestRunFilters) => [...testKeys.all(orgId, project), "runs", filters] as const,
  run: (orgId: string | null, project: string, runId: string) => [...testKeys.all(orgId, project), "run", runId] as const,
  coverage: (orgId: string | null, project: string, query: CoverageQuery) => [...testKeys.all(orgId, project), "coverage", query] as const,
  fileCoverage: (orgId: string | null, project: string, target: CoverageTarget, path: string, baseSha: string) =>
    [...testKeys.all(orgId, project), "file", target, path, baseSha] as const,
  release: (orgId: string | null, project: string, version: string) => [...testKeys.all(orgId, project), "release", version] as const,
  flaky: (orgId: string | null, project: string, filters: FlakyFilters) => [...testKeys.all(orgId, project), "flaky", filters] as const,
  test: (orgId: string | null, project: string, testId: string) => [...testKeys.all(orgId, project), "test", testId] as const,
  /**
   * Several projects at once (GetTestPortfolio), sorted so a selection in any
   * order shares one answer. Its third segment is an object, never a project
   * name, so it cannot collide with one project's keys; Refresh of the org
   * (`["tests", orgId]`) covers it.
   */
  portfolio: (orgId: string | null, projects: readonly string[] | "all") =>
    ["tests", orgId ?? "", { portfolio: projects === "all" ? "all" : [...new Set(projects)].sort() }] as const,
};

const READ = {
  retry: testsRetry,
  retryDelay: testsRetryDelay,
} as const;
const LIVE = { ...READ, staleTime: 30_000 } as const;
const SETTLED = { ...READ, staleTime: 5 * 60_000 } as const;

/** Rows per page of the runs and flaky tables. */
export const TESTS_PAGE_SIZE = 50;

/**
 * One branch's test health (the default branch when `branch` is empty), from
 * runs recorded against `environmentId` when one is named. What the Tests
 * Overview reads, and what decides its state. `project` empty: nothing is fetched.
 */
export function useTestSummary(project: string, { branch, environmentId }: { branch?: string; environmentId?: string } = {}) {
  const api = useTestApi();
  const org = useOrgScope();
  return useQuery(
    withOrgFailure(
      {
        queryKey: testKeys.summary(org.orgId, project, branch ?? "", environmentId ?? ""),
        queryFn: ({ signal }: { signal: AbortSignal }) => api.getTestSummary({ orgId: org.orgId, project, branch, environmentId }, { signal }),
        enabled: !!project && orgQueryEnabled(org),
        ...LIVE,
      },
      org
    )
  );
}

/** Recorded runs matching `filters`, newest first, paged by the server's token. `null` filters: nothing is fetched. */
export function useTestRuns(project: string, filters: TestRunFilters | null) {
  const api = useTestApi();
  const org = useOrgScope();
  return useInfiniteQuery(
    withOrgFailure(
      {
        queryKey: testKeys.runs(org.orgId, project, filters ?? {}),
        queryFn: ({ pageParam, signal }: { pageParam: string; signal: AbortSignal }) =>
          api.listTestRuns({ orgId: org.orgId, project, filters: filters ?? {}, pageSize: TESTS_PAGE_SIZE, pageToken: pageParam }, { signal }),
        initialPageParam: "",
        getNextPageParam: (last: { nextPageToken: string }) => last.nextPageToken || undefined,
        enabled: !!project && !!filters && orgQueryEnabled(org),
        // Only an answer read in this org: every key carries the org as a segment (testKeys.all).
        placeholderData: keepPreviousInOrgSegment(org.orgId),
        ...LIVE,
      },
      org
    )
  );
}

/** One run: its failures with output, retry passes, slowest tests and zero-evidence packages. */
export function useTestRun(project: string, runId: string) {
  const api = useTestApi();
  const org = useOrgScope();
  return useQuery(
    withOrgFailure(
      {
        queryKey: testKeys.run(org.orgId, project, runId),
        queryFn: ({ signal }: { signal: AbortSignal }) => api.getTestRun({ orgId: org.orgId, project, runId }, { signal }),
        enabled: !!project && !!runId && orgQueryEnabled(org),
        ...LIVE,
      },
      org
    )
  );
}

/** What GetCoverage is asked: whose report, which path, against which base, through which lens. */
export interface CoverageQuery {
  target: CoverageTarget;
  path: string;
  lane?: string;
  baseSha?: string;
  onlyE2e?: boolean;
  changedOnly?: boolean;
}

/** One path's coverage and its children. `null` query: nothing is fetched. */
export function useCoverage(project: string, query: CoverageQuery | null) {
  const api = useTestApi();
  const org = useOrgScope();
  return useQuery(
    withOrgFailure(
      {
        queryKey: testKeys.coverage(org.orgId, project, query ?? { target: { gitCommit: "" }, path: "" }),
        queryFn: ({ signal }: { signal: AbortSignal }) => api.getCoverage({ orgId: org.orgId, project, ...query! }, { signal }),
        enabled: !!project && !!query && orgQueryEnabled(org),
        // Only an answer read in this org: every key carries the org as a segment (testKeys.all).
        placeholderData: keepPreviousInOrgSegment(org.orgId),
        ...SETTLED,
      },
      org
    )
  );
}

/** One file's coverage line by line, and its blob id. `path` empty: nothing is fetched. */
export function useFileCoverage(project: string, target: CoverageTarget | null, path: string, baseSha?: string) {
  const api = useTestApi();
  const org = useOrgScope();
  return useQuery(
    withOrgFailure(
      {
        queryKey: testKeys.fileCoverage(org.orgId, project, target ?? { gitCommit: "" }, path, baseSha ?? ""),
        queryFn: ({ signal }: { signal: AbortSignal }) => api.getFileCoverage({ orgId: org.orgId, project, target: target!, path, baseSha }, { signal }),
        enabled: !!project && !!target && !!path && orgQueryEnabled(org),
        ...SETTLED,
      },
      org
    )
  );
}

/** A release's test evidence: its commit's report, and what changed since the previous release. */
export function useReleaseTests(project: string, version: string) {
  const api = useTestApi();
  const org = useOrgScope();
  return useQuery(
    withOrgFailure(
      {
        queryKey: testKeys.release(org.orgId, project, version),
        queryFn: ({ signal }: { signal: AbortSignal }) => api.getReleaseTests({ orgId: org.orgId, project, version }, { signal }),
        enabled: !!project && !!version && orgQueryEnabled(org),
        ...LIVE,
      },
      org
    )
  );
}

/** Tests that are, or were, flaky, highest flake rate first. `null` filters: nothing is fetched. */
export function useFlakyTests(project: string, filters: FlakyFilters | null) {
  const api = useTestApi();
  const org = useOrgScope();
  return useInfiniteQuery(
    withOrgFailure(
      {
        queryKey: testKeys.flaky(org.orgId, project, filters ?? {}),
        queryFn: ({ pageParam, signal }: { pageParam: string; signal: AbortSignal }) =>
          api.listFlakyTests({ orgId: org.orgId, project, filters: filters ?? {}, pageSize: TESTS_PAGE_SIZE, pageToken: pageParam }, { signal }),
        initialPageParam: "",
        getNextPageParam: (last: { nextPageToken: string }) => last.nextPageToken || undefined,
        enabled: !!project && !!filters && orgQueryEnabled(org),
        // Only an answer read in this org: every key carries the org as a segment (testKeys.all).
        placeholderData: keepPreviousInOrgSegment(org.orgId),
        ...LIVE,
      },
      org
    )
  );
}

/**
 * Several tests' records at once, by the same key as useTest: what the Flaky
 * tests list draws each row's recent runs from, so opening a test is
 * answered from the cache.
 */
export function useTests(project: string, testIds: readonly string[]) {
  const api = useTestApi();
  const org = useOrgScope();
  return useQueries({
    queries: testIds.map((testId) =>
      withOrgFailure(
        {
          queryKey: testKeys.test(org.orgId, project, testId),
          queryFn: ({ signal }: { signal: AbortSignal }) => api.getTest({ orgId: org.orgId, project, testId }, { signal }),
          enabled: !!project && !!testId && orgQueryEnabled(org),
          ...LIVE,
        },
        org
      )
    ),
  });
}

/** One test: its outcomes over its last runs, flake record, events and failure signatures. */
export function useTest(project: string, testId: string) {
  const api = useTestApi();
  const org = useOrgScope();
  return useQuery(
    withOrgFailure(
      {
        queryKey: testKeys.test(org.orgId, project, testId),
        queryFn: ({ signal }: { signal: AbortSignal }) => api.getTest({ orgId: org.orgId, project, testId }, { signal }),
        enabled: !!project && !!testId && orgQueryEnabled(org),
        ...LIVE,
      },
      org
    )
  );
}

/**
 * Several projects' test health on their default branches, and the server's
 * totals across them (GetTestPortfolio): what the aggregate Tests, Coverage
 * and Overview pages read. `projects` is the forge apps, as every other Tests
 * read names one; `"all"` asks for every project of the org with a recorded
 * run (the server lists and totals the 50 most recent, and says when it
 * truncated). A list longer than the server takes is cut to its first 50
 * (`capped`): totals are never stitched together from several answers in the
 * browser. `null`: nothing is fetched.
 */
export function useTestPortfolio(projects: readonly string[] | "all" | null) {
  const api = useTestApi();
  const org = useOrgScope();
  const named = projects === null || projects === "all" ? [] : [...new Set(projects)].sort();
  const sent = named.slice(0, PORTFOLIO_MAX_PROJECTS);
  const capped = named.length > sent.length;
  const query = useQuery(
    withOrgFailure(
      {
        queryKey: testKeys.portfolio(org.orgId, projects === "all" ? "all" : sent),
        queryFn: ({ signal }: { signal: AbortSignal }) => api.getTestPortfolio({ orgId: org.orgId, projects: sent }, { signal }),
        enabled: (projects === "all" || sent.length > 0) && orgQueryEnabled(org),
        ...LIVE,
      },
      org
    )
  );
  return { ...query, capped };
}
