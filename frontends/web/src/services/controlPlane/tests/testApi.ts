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
 *
 * Each is one call over the generated client (proto-vendor/controlplane,
 * services/tests/v1), mapped by wire.ts. A control plane that does not serve
 * TestsService yet answers Unimplemented, as does this client with no control
 * plane configured: the pages read that as "this control plane doesn't store
 * test runs yet" (errors.ts isTestsServiceMissing), never as "no test runs".
 */

import { Code, ConnectError, type Client } from "@connectrpc/connect";

import { TestsService } from "@/gen/controlplane/services/tests/v1/tests_pb";

import { getControlPlaneClient } from "../client";
import { CONTROL_PLANE_API_URL } from "../config";
import type {
  CallOptions,
  CoverageRequest,
  CoverageTree,
  FileCoverage,
  FileCoverageRequest,
  FlakyRequest,
  ReleaseTests,
  ReleaseTestsRequest,
  RunRequest,
  RunsRequest,
  SummaryRequest,
  TestDetail,
  TestFlakePage,
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

function client(): Client<typeof TestsService> {
  // Without a control plane there is no service to ask: the same answer as one without it.
  if (!CONTROL_PLANE_API_URL) throw new ConnectError("no control plane is configured", Code.Unimplemented);
  return getControlPlaneClient(TestsService);
}

export async function getTestSummary(req: SummaryRequest, opts?: CallOptions): Promise<TestSummary> {
  return summaryOf(await client().getTestSummary(summaryRequest(req), opts));
}

export async function listTestRuns(req: RunsRequest, opts?: CallOptions): Promise<TestRunPage> {
  return runPageOf(await client().listTestRuns(runsRequest(req), opts));
}

export async function getTestRun(req: RunRequest, opts?: CallOptions): Promise<TestRunDetail> {
  return runDetailOf(await client().getTestRun(runRequest(req), opts));
}

export async function getCoverage(req: CoverageRequest, opts?: CallOptions): Promise<CoverageTree> {
  return coverageTreeOf(await client().getCoverage(coverageRequest(req), opts));
}

export async function getFileCoverage(req: FileCoverageRequest, opts?: CallOptions): Promise<FileCoverage> {
  return fileCoverageOf(await client().getFileCoverage(fileCoverageRequest(req), opts));
}

export async function getReleaseTests(req: ReleaseTestsRequest, opts?: CallOptions): Promise<ReleaseTests> {
  return releaseTestsOf(await client().getReleaseTests(releaseTestsRequest(req), opts));
}

export async function listFlakyTests(req: FlakyRequest, opts?: CallOptions): Promise<TestFlakePage> {
  return flakePageOf(await client().listFlakyTests(flakyRequest(req), opts));
}

export async function getTest(req: TestRequest, opts?: CallOptions): Promise<TestDetail> {
  return testDetailOf(await client().getTest(testRequest(req), opts));
}
