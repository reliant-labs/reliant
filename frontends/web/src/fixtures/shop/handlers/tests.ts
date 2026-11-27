// Copyright (c) 2026 Reliant Labs

/**
 * The shop pack's TestsService handlers: the eight reads the Tests pages make
 * (Overview, Coverage, Runs, a run, Flaky tests, a test, a release's Tests
 * tab), and the portfolio the aggregate Tests pages read, answered on the WIRE as control-plane would answer them. The pack
 * composes them with the other services' handlers (../index.ts shopScenario).
 *
 * model/tests.ts already tells shop's runs, coverage and flakes as wire
 * shapes (services/controlPlane/tests/wire.ts's), so each handler decodes the
 * request into the client's plain request, asks the model, and builds the
 * generated response message from its answer. The demo then runs the pages'
 * REAL client (services/controlPlane/tests/testApi.ts and its wire.ts) over
 * the fixture transport, with no TestApiProvider in between.
 */

import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  GetCoverageResponseSchema,
  GetFileCoverageResponseSchema,
  GetReleaseTestsResponseSchema,
  GetTestResponseSchema,
  GetTestRunResponseSchema,
  GetTestPortfolioResponseSchema,
  GetTestSummaryResponseSchema,
  ListFlakyTestsResponseSchema,
  ListTestRunsResponseSchema,
  TestFlakeState,
  TestRunOutcome,
  TestRunSource,
  TestsService,
  type GetCoverageRequest,
  type GetFileCoverageRequest,
  type GetReleaseTestsRequest,
  type GetTestRequest,
  type GetTestPortfolioRequest,
  type GetTestRunRequest,
  type GetTestSummaryRequest,
  type ListFlakyTestsRequest,
  type ListTestRunsRequest,
} from "@/gen/controlplane/services/tests/v1/tests_pb";
import type { CoverageTarget, FlakyFilters, TestRunFilters } from "@/services/controlPlane/tests/types";
import type { MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { shopTestsAnswers } from "../model/tests";
import { demoNow } from "../shopStory";
import { withMessageRequests } from "./defaults";
import { DEMO_SCOPE, scopedToDemo, type PackScope } from "./scope";

const rpc = (method: string) => `${TestsService.typeName}/${method}`;

/** The RPCs served here: every read the Tests pages' client makes (testApi.ts). */
export const TESTS_RPCS = {
  getTestSummary: rpc("GetTestSummary"),
  listTestRuns: rpc("ListTestRuns"),
  getTestRun: rpc("GetTestRun"),
  getCoverage: rpc("GetCoverage"),
  getFileCoverage: rpc("GetFileCoverage"),
  getReleaseTests: rpc("GetReleaseTests"),
  listFlakyTests: rpc("ListFlakyTests"),
  getTest: rpc("GetTest"),
  getTestPortfolio: rpc("GetTestPortfolio"),
} as const;

// ── Wire → the client's plain requests (wire.ts's request builders, inverted) ─

const SOURCE: Partial<Record<TestRunSource, TestRunFilters["source"]>> = {
  [TestRunSource.CI]: "ci",
  [TestRunSource.LOCAL]: "local",
  [TestRunSource.ENV]: "env",
  [TestRunSource.CONFIRM]: "confirm",
};

const OUTCOME: Partial<Record<TestRunOutcome, TestRunFilters["outcome"]>> = {
  [TestRunOutcome.PASSED]: "passed",
  [TestRunOutcome.FAILED]: "failed",
  [TestRunOutcome.UNDETERMINED]: "undetermined",
};

const FLAKE_STATE: Partial<Record<TestFlakeState, FlakyFilters["state"]>> = {
  [TestFlakeState.HEALTHY]: "healthy",
  [TestFlakeState.SUSPECTED]: "suspected",
  [TestFlakeState.FLAKY]: "flaky",
  [TestFlakeState.QUARANTINED]: "quarantined",
  [TestFlakeState.FIXED]: "fixed",
  [TestFlakeState.REMOVED]: "removed",
};

/** The request's scope as the client sends it (an absent optional is undefined). */
const base = (req: { project: string; orgId?: string }) => ({ project: req.project, orgId: req.orgId ?? null });

/** The `target` oneof as the client's CoverageTarget; the server refuses a request without one. */
function targetOf(target: GetCoverageRequest["target"]): CoverageTarget {
  switch (target.case) {
    case "gitCommit":
      return { gitCommit: target.value };
    case "releaseVersion":
      return { releaseVersion: target.value };
    case "environmentId":
      return { environmentId: target.value };
    default:
      throw new ConnectError("a coverage target is required", Code.InvalidArgument);
  }
}

/**
 * TestsService's reads over shop's story as of `now` (the pack clock), with
 * requests normalized to messages and reads outside shop answered empty
 * (scope.ts).
 */
export function testsHandlers(now: () => number = demoNow, scope: PackScope = DEMO_SCOPE): MockHandlers {
  // One model per transport: its answers are fixed by the pack clock, which never moves.
  let model: ReturnType<typeof shopTestsAnswers> | null = null;
  const answers = () => (model ??= shopTestsAnswers(now(), scope.app));
  const handlers: MockHandlers = {
    [TESTS_RPCS.getTestSummary]: (req: GetTestSummaryRequest) =>
      create(GetTestSummaryResponseSchema, answers().getTestSummary({ environmentId: req.environmentId || undefined })),
    [TESTS_RPCS.listTestRuns]: (req: ListTestRunsRequest) =>
      create(
        ListTestRunsResponseSchema,
        answers().listTestRuns({
          ...base(req),
          filters: {
            branch: req.branch || undefined,
            gitCommit: req.gitCommit || undefined,
            releaseVersion: req.releaseVersion || undefined,
            environmentId: req.environmentId || undefined,
            lane: req.lane || undefined,
            source: req.source === undefined ? undefined : SOURCE[req.source],
            outcome: req.outcome === undefined ? undefined : OUTCOME[req.outcome],
          },
          pageSize: req.pageSize,
          pageToken: req.pageToken,
        })
      ),
    [TESTS_RPCS.getTestRun]: (req: GetTestRunRequest) => create(GetTestRunResponseSchema, answers().getTestRun({ runId: req.runId })),
    [TESTS_RPCS.getCoverage]: (req: GetCoverageRequest) =>
      create(
        GetCoverageResponseSchema,
        answers().getCoverage({
          ...base(req),
          target: targetOf(req.target),
          path: req.path,
          lane: req.lane || undefined,
          baseSha: req.baseSha || undefined,
          changedOnly: req.changedOnly,
        })
      ),
    [TESTS_RPCS.getFileCoverage]: (req: GetFileCoverageRequest) =>
      create(GetFileCoverageResponseSchema, answers().getFileCoverage({ ...base(req), target: targetOf(req.target), path: req.path, baseSha: req.baseSha || undefined })),
    [TESTS_RPCS.getReleaseTests]: (req: GetReleaseTestsRequest) => create(GetReleaseTestsResponseSchema, answers().getReleaseTests({ version: req.version })),
    [TESTS_RPCS.listFlakyTests]: (req: ListFlakyTestsRequest) =>
      create(
        ListFlakyTestsResponseSchema,
        answers().listFlakyTests({
          ...base(req),
          filters: { state: req.state === undefined ? undefined : FLAKE_STATE[req.state], minScore: req.minScore, package: req.package || undefined },
          pageSize: req.pageSize,
          pageToken: req.pageToken,
        })
      ),
    [TESTS_RPCS.getTest]: (req: GetTestRequest) => create(GetTestResponseSchema, answers().getTest({ testId: req.testId })),
    // Several projects at once: a row for each named, the story's app with its runs (scope.ts reads a
    // project list as a union, so a list without the app never reaches here).
    [TESTS_RPCS.getTestPortfolio]: (req: GetTestPortfolioRequest) => create(GetTestPortfolioResponseSchema, answers().getTestPortfolio({ projects: req.projects })),
  };
  return withMessageRequests(scopedToDemo(handlers, [TestsService], scope), [TestsService]);
}
