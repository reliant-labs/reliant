// Copyright (c) 2026 Reliant Labs

/**
 * `controlplane.v1.TestsService`'s reads, as the Tests pages call them
 * (TestApiContext.tsx is the seam):
 *
 *   GetTestSummary   one branch's health: coverage trend, pass rate, flakes, last run
 *   ListTestRuns     recorded runs, newest first; paged
 *   GetTestRun       one run's failures, retry passes, slowest tests
 *   GetCoverage      one path's coverage and its children
 *   GetFileCoverage  one file line by line (hits, never source)
 *   GetReleaseTests  a release commit's report and what changed since the previous release
 *   ListFlakyTests   tests that are, or were, flaky; paged
 *   GetTest          one test's outcomes, events and failure signatures
 *   GetTestPortfolio several projects' health at once, and the server's totals
 *
 * Each is one call over the generated client (proto-vendor/controlplane,
 * services/tests/v1), mapped by wire.ts. A control plane that does not serve
 * TestsService yet answers Unimplemented, as does this client with no control
 * plane configured: the pages read that as "this control plane doesn't store
 * test runs yet" (errors.ts isTestsServiceMissing), never as "no test runs".
 */

import { Code, ConnectError, type Client, type Transport } from "@connectrpc/connect";

import { TestsService } from "@/gen/controlplane/services/tests/v1/tests_pb";
import { transport as appTransport } from "@/lib/connect";

import { getControlPlaneClient } from "../client";
import { CONTROL_PLANE_API_URL } from "../config";
import type {
  CallOptions,
  CoverageRequest,
  CoverageTree,
  FileCoverage,
  FileCoverageRequest,
  FlakyRequest,
  PortfolioRequest,
  ReleaseTests,
  ReleaseTestsRequest,
  RunRequest,
  RunsRequest,
  SummaryRequest,
  TestDetail,
  TestFlakePage,
  TestPortfolio,
  TestRequest,
  TestRunDetail,
  TestRunPage,
  TestSummary,
} from "./types";
import {
  coverageRequest,
  coverageTreeOf,
  fileCoverageOf,
  fileCoverageRequest,
  flakePageOf,
  flakyRequest,
  portfolioOf,
  portfolioRequest,
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
} from "./wire";

/**
 * `transport` is the caller's TransportScope's (lib/transportScope.tsx), as
 * getControlPlaneClient takes it. A scope's own transport (a fixture pack's)
 * needs no control plane; none, or the app's, is today's client.
 */
function client(transport?: Transport): Client<typeof TestsService> {
  const scoped = transport !== undefined && transport !== appTransport;
  // Without a control plane there is no service to ask: the same answer as one without it.
  if (!scoped && !CONTROL_PLANE_API_URL) throw new ConnectError("no control plane is configured", Code.Unimplemented);
  return getControlPlaneClient(TestsService, transport);
}

export async function getTestSummary(req: SummaryRequest, opts?: CallOptions, transport?: Transport): Promise<TestSummary> {
  return summaryOf(await client(transport).getTestSummary(summaryRequest(req), opts));
}

export async function listTestRuns(req: RunsRequest, opts?: CallOptions, transport?: Transport): Promise<TestRunPage> {
  return runPageOf(await client(transport).listTestRuns(runsRequest(req), opts));
}

export async function getTestRun(req: RunRequest, opts?: CallOptions, transport?: Transport): Promise<TestRunDetail> {
  return runDetailOf(await client(transport).getTestRun(runRequest(req), opts));
}

export async function getCoverage(req: CoverageRequest, opts?: CallOptions, transport?: Transport): Promise<CoverageTree> {
  return coverageTreeOf(await client(transport).getCoverage(coverageRequest(req), opts));
}

export async function getFileCoverage(req: FileCoverageRequest, opts?: CallOptions, transport?: Transport): Promise<FileCoverage> {
  return fileCoverageOf(await client(transport).getFileCoverage(fileCoverageRequest(req), opts));
}

export async function getReleaseTests(req: ReleaseTestsRequest, opts?: CallOptions, transport?: Transport): Promise<ReleaseTests> {
  return releaseTestsOf(await client(transport).getReleaseTests(releaseTestsRequest(req), opts));
}

export async function listFlakyTests(req: FlakyRequest, opts?: CallOptions, transport?: Transport): Promise<TestFlakePage> {
  return flakePageOf(await client(transport).listFlakyTests(flakyRequest(req), opts));
}

export async function getTest(req: TestRequest, opts?: CallOptions, transport?: Transport): Promise<TestDetail> {
  return testDetailOf(await client(transport).getTest(testRequest(req), opts));
}

/**
 * Several projects' test health and the server's totals across them. A
 * control plane from before control-plane#3956 answers Unimplemented: the
 * aggregate pages read that as "not available here", never as no runs.
 */
export async function getTestPortfolio(req: PortfolioRequest, opts?: CallOptions, transport?: Transport): Promise<TestPortfolio> {
  return portfolioOf(await client(transport).getTestPortfolio(portfolioRequest(req), opts));
}
