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
 *   GetReleaseTests  a release commit's report and what changed since the previous
 *   ListFlakyTests   tests that are, or were, flaky; paged
 *   GetTest          one test's outcomes, events and failure signatures
 *
 * NOT WIRED YET. This build's control-plane client (proto-vendor/controlplane)
 * has no TestsService: control-plane withholds it from its public contract
 * until it is mounted (control-plane#3286 adds the proto; C4 mounts it). Until
 * the re-vendor (W0), every read answers Unimplemented — what a control plane
 * without the service answers too — and the pages say "this control plane
 * doesn't store test runs yet", never "no test runs".
 *
 * After `task contract:controlplane:sync` brings it in, each function here is
 * one line over the generated client, e.g.
 *
 *   const client = () => getControlPlaneClient(TestsService);
 *   export const getTestSummary = async (req, opts) =>
 *     summaryOf(await client().getTestSummary(summaryRequest(req), opts));
 *
 * with the mapping and the request shapes already in wire.ts.
 */

import { Code, ConnectError } from "@connectrpc/connect";

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

function notInThisClient(): Promise<never> {
  return Promise.reject(new ConnectError("test runs are not in this build's control-plane client yet", Code.Unimplemented));
}

export function getTestSummary(_req: SummaryRequest, _opts?: CallOptions): Promise<TestSummary> {
  return notInThisClient();
}

export function listTestRuns(_req: RunsRequest, _opts?: CallOptions): Promise<TestRunPage> {
  return notInThisClient();
}

export function getTestRun(_req: RunRequest, _opts?: CallOptions): Promise<TestRunDetail> {
  return notInThisClient();
}

export function getCoverage(_req: CoverageRequest, _opts?: CallOptions): Promise<CoverageTree> {
  return notInThisClient();
}

export function getFileCoverage(_req: FileCoverageRequest, _opts?: CallOptions): Promise<FileCoverage> {
  return notInThisClient();
}

export function getReleaseTests(_req: ReleaseTestsRequest, _opts?: CallOptions): Promise<ReleaseTests> {
  return notInThisClient();
}

export function listFlakyTests(_req: FlakyRequest, _opts?: CallOptions): Promise<TestFlakePage> {
  return notInThisClient();
}

export function getTest(_req: TestRequest, _opts?: CallOptions): Promise<TestDetail> {
  return notInThisClient();
}
