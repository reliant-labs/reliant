// Copyright (c) 2026 Reliant Labs

/**
 * React Query hooks for the Tests pages: TestsService's reads, over the
 * generated query options (hooks/controlplane/tests-service-hooks_gen.ts).
 * The cache holds the messages; each read maps its answer into the console's
 * types in `select` (services/controlPlane/tests/wire.ts). Under a
 * TransportScope (the forge tour's sample app, a test's fixture) every read
 * goes to the scope's transport, by the QueryClient it runs on.
 *
 * Every request names the org and the forge project, so the generated key
 * does too: two orgs or two projects never share an answer. Runs arrive with
 * every CI lane and ingest fills their figures in within a minute, so the
 * summary and the runs list go stale after 30s; a commit's coverage never
 * changes once read, nothing polls.
 *
 * Without a control plane (config.js's CONTROL_PLANE_API_URL unset) there is
 * no service to ask, and a read on the app's transport fails Unimplemented,
 * the answer of a control plane without TestsService: the pages say "this
 * control plane doesn't store test runs yet", never "unavailable"
 * (onControlPlane).
 */

import { Code, ConnectError } from "@connectrpc/connect";
import { useInfiniteQuery, useQueries, useQuery, type InfiniteData, type QueryClient } from "@tanstack/react-query";

import type { ListFlakyTestsResponse, ListTestRunsResponse } from "@/gen/controlplane/services/tests/v1/tests_pb";
import { testsServiceQueries } from "@/hooks/controlplane/tests-service-hooks_gen";
import { keepPreviousInOrg, orgQueryEnabled, useOrgScope, withOrgFailure, type OrgScope } from "@/lib/orgScope";
import { transportOf } from "@/lib/transportScope";
import { CONTROL_PLANE_API_URL } from "@/services/controlPlane/config";
import { testsRetry, testsRetryDelay } from "@/services/controlPlane/tests/errors";
import type { CoverageTarget, FlakyFilters, TestRunFilters } from "@/services/controlPlane/tests/types";
import {
  coverageRequest,
  coverageTreeOf,
  fileCoverageOf,
  fileCoverageRequest,
  flakePageOf,
  flakyRequest,
  releaseTestsOf,
  releaseTestsRequest,
  runDetailOf,
  runPageOf,
  runRequest,
  runsRequest,
  summaryOf,
  summaryRequest,
  testDetailOf,
  testRequest,
} from "@/services/controlPlane/tests/wire";

const READ = {
  retry: testsRetry,
  retryDelay: testsRetryDelay,
} as const;
const LIVE = { ...READ, staleTime: 30_000 } as const;
const SETTLED = { ...READ, staleTime: 5 * 60_000 } as const;

/** Rows per page of the runs and flaky tables. */
export const TESTS_PAGE_SIZE = 50;

/**
 * `options` (a generated read's, testsServiceQueries) with its fetch refused
 * as Unimplemented when it would reach the app's transport and this
 * deployment has no control plane: the same answer as a control plane
 * without TestsService. A scope's transport (a fixture pack's) needs no
 * control plane. For hooks and for `queryClient.fetchQuery` alike.
 */
export function onControlPlane<O extends { queryFn?: unknown }>(options: O): O {
  const queryFn = options.queryFn as ((context: { client: QueryClient }) => unknown) | undefined;
  if (typeof queryFn !== "function") return options;
  return {
    ...options,
    queryFn: (context: { client: QueryClient }) => {
      refuseWithoutControlPlane(context.client);
      return queryFn(context);
    },
  };
}

/**
 * onControlPlane's refusal, for an imperative caller to make before it reads
 * on `client` (`client.fetchQuery(testsServiceQueries.…)`): Unimplemented when
 * the read would reach the app's transport and this deployment has no control plane.
 */
export function refuseWithoutControlPlane(client: QueryClient): void {
  if (!CONTROL_PLANE_API_URL && transportOf(client) === undefined) throw new ConnectError("no control plane is configured", Code.Unimplemented);
}

/** A paged request as an infinite read takes it: the page token is the read's page param, never its key. */
const firstPage = <R extends { pageToken: string }>({ pageToken: _first, ...req }: R): Omit<R, "pageToken"> => req;

/** A read in `org`: refused without a control plane, and failing with the org's error while it failed to resolve. */
const inOrg = <O extends { queryFn?: unknown }>(options: O, org: OrgScope): O => withOrgFailure(onControlPlane(options), org);

const runPages = (data: InfiniteData<ListTestRunsResponse, string>) => ({ pages: data.pages.map(runPageOf), pageParams: data.pageParams });
const flakePages = (data: InfiniteData<ListFlakyTestsResponse, string>) => ({ pages: data.pages.map(flakePageOf), pageParams: data.pageParams });

/**
 * One branch's test health (the default branch when `branch` is empty), from
 * runs recorded against `environmentId` when one is named. What the Tests
 * Overview reads, and what decides its state. `project` empty: nothing is fetched.
 */
export function useTestSummary(project: string, { branch, environmentId }: { branch?: string; environmentId?: string } = {}) {
  const org = useOrgScope();
  return useQuery(
    inOrg(
      testsServiceQueries.getTestSummary(summaryRequest({ orgId: org.orgId, project, branch, environmentId }), {
        enabled: !!project && orgQueryEnabled(org),
        ...LIVE,
        select: summaryOf,
      }),
      org
    )
  );
}

/** Recorded runs matching `filters`, newest first, paged by the server's token. `null` filters: nothing is fetched. */
export function useTestRuns(project: string, filters: TestRunFilters | null) {
  const org = useOrgScope();
  return useInfiniteQuery(
    inOrg(
      testsServiceQueries.listTestRunsInfinite(firstPage(runsRequest({ orgId: org.orgId, project, filters: filters ?? {}, pageSize: TESTS_PAGE_SIZE })), {
        enabled: !!project && !!filters && orgQueryEnabled(org),
        // Only an answer read in this org: every request names it (generated key).
        placeholderData: keepPreviousInOrg(org.orgId),
        ...LIVE,
        select: runPages,
      }),
      org
    )
  );
}

/** One run: its failures with output, retry passes, slowest tests and zero-evidence packages. */
export function useTestRun(project: string, runId: string) {
  const org = useOrgScope();
  return useQuery(
    inOrg(
      testsServiceQueries.getTestRun(runRequest({ orgId: org.orgId, project, runId }), {
        enabled: !!project && !!runId && orgQueryEnabled(org),
        ...LIVE,
        select: runDetailOf,
      }),
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
  const org = useOrgScope();
  const options = testsServiceQueries.getCoverage(coverageRequest({ orgId: org.orgId, project, ...(query ?? { target: { gitCommit: "" }, path: "" }) }), {
    enabled: !!project && !!query && orgQueryEnabled(org),
    // Do not carry a previous selection through null: QueryObserver retains
    // its last successful query even after observing a disabled empty key.
    ...SETTLED,
    select: coverageTreeOf,
  });
  return useQuery(
    inOrg(
      {
        ...options,
        // A disabled selection is not a request, even one with default fields.
        queryKey: query ? options.queryKey : ["tests-coverage-disabled", org.orgId, project],
      },
      org
    )
  );
}

/** One file's coverage line by line, and its blob id. `path` empty: nothing is fetched. */
export function useFileCoverage(project: string, target: CoverageTarget | null, path: string, baseSha?: string) {
  const org = useOrgScope();
  return useQuery(
    inOrg(
      testsServiceQueries.getFileCoverage(fileCoverageRequest({ orgId: org.orgId, project, target: target ?? { gitCommit: "" }, path, baseSha }), {
        enabled: !!project && !!target && !!path && orgQueryEnabled(org),
        ...SETTLED,
        select: fileCoverageOf,
      }),
      org
    )
  );
}

/** A release's test evidence: its commit's report, and what changed since the previous release. */
export function useReleaseTests(project: string, version: string) {
  const org = useOrgScope();
  return useQuery(
    inOrg(
      testsServiceQueries.getReleaseTests(releaseTestsRequest({ orgId: org.orgId, project, version }), {
        enabled: !!project && !!version && orgQueryEnabled(org),
        ...LIVE,
        select: releaseTestsOf,
      }),
      org
    )
  );
}

/** Tests that are, or were, flaky, highest flake rate first. `null` filters: nothing is fetched. */
export function useFlakyTests(project: string, filters: FlakyFilters | null) {
  const org = useOrgScope();
  return useInfiniteQuery(
    inOrg(
      testsServiceQueries.listFlakyTestsInfinite(firstPage(flakyRequest({ orgId: org.orgId, project, filters: filters ?? {}, pageSize: TESTS_PAGE_SIZE })), {
        enabled: !!project && !!filters && orgQueryEnabled(org),
        // Only an answer read in this org: every request names it (generated key).
        placeholderData: keepPreviousInOrg(org.orgId),
        ...LIVE,
        select: flakePages,
      }),
      org
    )
  );
}

/** GetTest's options for one test, as useTest and useTests read it. */
const testRead = (org: OrgScope, project: string, testId: string) =>
  inOrg(
    testsServiceQueries.getTest(testRequest({ orgId: org.orgId, project, testId }), {
      enabled: !!project && !!testId && orgQueryEnabled(org),
      ...LIVE,
      select: testDetailOf,
    }),
    org
  );

/**
 * Several tests' records at once, by the same key as useTest: what the Flaky
 * tests list draws each row's recent runs from, so opening a test is
 * answered from the cache.
 */
export function useTests(project: string, testIds: readonly string[]) {
  const org = useOrgScope();
  return useQueries({ queries: testIds.map((testId) => testRead(org, project, testId)) });
}

/** One test: its outcomes over its last runs, flake record, events and failure signatures. */
export function useTest(project: string, testId: string) {
  const org = useOrgScope();
  return useQuery(testRead(org, project, testId));
}
